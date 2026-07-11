#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
allowlist="${repo_root}/.github/public-workflow-secret-allowlist.json"
workflows=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --allowlist)
      [[ $# -ge 2 ]] || { echo "--allowlist requires a path" >&2; exit 2; }
      allowlist="$2"
      shift 2
      ;;
    --)
      shift
      workflows+=("$@")
      break
      ;;
    -*)
      echo "unknown option: $1" >&2
      exit 2
      ;;
    *)
      workflows+=("$1")
      shift
      ;;
  esac
done

if [[ ${#workflows[@]} -eq 0 ]]; then
  shopt -s nullglob
  workflows=("${repo_root}"/.github/workflows/*.yml "${repo_root}"/.github/workflows/*.yaml)
  shopt -u nullglob
fi

if [[ ${#workflows[@]} -eq 0 ]]; then
  echo "no public workflow files found" >&2
  exit 1
fi

scanner="$(mktemp "${TMPDIR:-/tmp}/public-workflow-policy.XXXXXX.go")"
trap 'rm -f "${scanner}"' EXIT

cat >"${scanner}" <<'GO'
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type allowEntry struct {
	Path      string `json:"path"`
	Secret    string `json:"secret"`
	Rationale string `json:"rationale"`
}

type findingSet struct {
	items []string
}

func (f *findingSet) add(format string, args ...any) {
	f.items = append(f.items, fmt.Sprintf(format, args...))
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func scalars(node *yaml.Node, out *[]string) {
	if node == nil {
		return
	}
	if node.Kind == yaml.ScalarNode {
		*out = append(*out, node.Value)
	}
	for _, child := range node.Content {
		scalars(child, out)
	}
}

func hasMappingScalar(node *yaml.Node, key, value string) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key && node.Content[i+1].Kind == yaml.ScalarNode && strings.EqualFold(node.Content[i+1].Value, value) {
				return true
			}
		}
	}
	for _, child := range node.Content {
		if hasMappingScalar(child, key, value) {
			return true
		}
	}
	return false
}

func knownCredentialVariables(node *yaml.Node, out map[string]bool) {
	if node == nil {
		return
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i].Value
			value := node.Content[i+1]
			if (key == "env" || key == "secrets") && value.Kind == yaml.MappingNode {
				for j := 0; j+1 < len(value.Content); j += 2 {
					name := strings.ToUpper(value.Content[j].Value)
					if knownCloudSecret(name) {
						out[name] = true
					}
				}
			}
		}
	}
	for _, child := range node.Content {
		knownCredentialVariables(child, out)
	}
}

func triggerPresent(root *yaml.Node, name string) bool {
	on := mappingValue(root, "on")
	if on == nil {
		return false
	}
	switch on.Kind {
	case yaml.ScalarNode:
		return on.Value == name
	case yaml.SequenceNode:
		for _, item := range on.Content {
			if item.Value == name {
				return true
			}
		}
	case yaml.MappingNode:
		return mappingValue(on, name) != nil
	}
	return false
}

func lineIsNegativeGuard(line string) bool {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "#") {
		return true
	}
	guardTool := regexp.MustCompile(`(^|[;&|![:space:]])(rg|grep|egrep|fgrep)([[:space:]]|$)`).MatchString(line)
	negativeCheck := strings.HasPrefix(line, "!") || strings.Contains(line, "if ") || strings.Contains(line, "test !")
	return guardTool && negativeCheck
}

var (
	secretRefRE    = regexp.MustCompile(`(?i)secrets(?:\.([A-Za-z_][A-Za-z0-9_]*)|\[[[:space:]]*['"]([A-Za-z_][A-Za-z0-9_]*)['"][[:space:]]*\])`)
	providerCLIRE  = regexp.MustCompile(`(^|[^A-Za-z0-9_.-])(doctl|gcloud|az|aws)([^A-Za-z0-9_.-]|$)`)
	integrationRE  = regexp.MustCompile(`(^|[[:space:]])-tags(?:=|[[:space:]])[^[:space:]]*integration`)
	namedLiveRE    = regexp.MustCompile(`(?i)(-run[=[:space:]]+[^[:space:]]*live|test[A-Za-z0-9_]*live|conformance_live_cloud)`)
	providerSDKRE  = regexp.MustCompile(`(?i)(digitalocean/godo|aws-sdk|azure-sdk|cloud\.google\.com/go|google-cloud-)`)
	providerAPIRE  = regexp.MustCompile(`(?i)(api\.digitalocean\.com|management\.azure\.com|[A-Za-z0-9.-]+\.amazonaws\.com|[A-Za-z0-9.-]+\.googleapis\.com|api\.cloudflare\.com)`)
	githubRunnerRE = regexp.MustCompile(`^(ubuntu-(latest|[0-9]{2}\.[0-9]{2})(-arm)?|windows-(latest|[0-9]{4})|macos-(latest|[0-9]{2})(-(large|xlarge))?)$`)
)

func secretReferences(node *yaml.Node) map[string]bool {
	values := []string{}
	scalars(node, &values)
	secrets := make(map[string]bool)
	for _, value := range values {
		for _, match := range secretRefRE.FindAllStringSubmatch(value, -1) {
			name := match[1]
			if name == "" {
				name = match[2]
			}
			secrets[strings.ToUpper(name)] = true
		}
	}
	return secrets
}

func validateSecretReferences(rel, prefix string, secrets map[string]bool, allowed map[string]allowEntry, referenced map[string]bool, findings *findingSet) {
	for secret := range secrets {
		key := rel + "\x00" + secret
		referenced[key] = true
		if knownCloudSecret(secret) {
			findings.add("%s references known cloud secret %s", prefix, secret)
		} else if _, ok := allowed[key]; !ok {
			findings.add("%s secret %s is not allowlisted", prefix, secret)
		}
	}
}

func checkRunnerSelector(prefix string, runsOn *yaml.Node, findings *findingSet) {
	if runsOn == nil {
		findings.add("%s does not declare a GitHub-hosted runner selector", prefix)
		return
	}
	var selectors []string
	switch runsOn.Kind {
	case yaml.ScalarNode:
		selectors = append(selectors, runsOn.Value)
	case yaml.SequenceNode:
		for _, item := range runsOn.Content {
			if item.Kind != yaml.ScalarNode {
				findings.add("%s uses a non-scalar runner selector", prefix)
				continue
			}
			selectors = append(selectors, item.Value)
		}
	default:
		findings.add("%s uses an unsupported runner selector shape", prefix)
		return
	}
	if len(selectors) == 0 {
		findings.add("%s does not declare a GitHub-hosted runner selector", prefix)
	}
	for _, selector := range selectors {
		selector = strings.TrimSpace(selector)
		if strings.Contains(selector, "${{") {
			findings.add("%s uses forbidden dynamic runner selector %s", prefix, selector)
			continue
		}
		if strings.EqualFold(selector, "self-hosted") {
			findings.add("%s uses forbidden self-hosted runner", prefix)
			continue
		}
		if !githubRunnerRE.MatchString(selector) {
			findings.add("%s runner selector %s is not recognized as GitHub-hosted", prefix, selector)
		}
	}
}

func providerUsesKind(reference string) string {
	lower := strings.ToLower(strings.TrimSpace(reference))
	path := strings.SplitN(lower, "@", 2)[0]
	providerActions := []string{
		"digitalocean/action-doctl",
		"aws-actions/configure-aws-credentials",
		"aws-actions/amazon-ecr-login",
		"aws-actions/amazon-ecs-deploy-task-definition",
		"azure/login",
		"azure/aks-set-context",
		"azure/webapps-deploy",
		"google-github-actions/auth",
		"cloudflare/wrangler-action",
	}
	for _, action := range providerActions {
		if path == action {
			return "action"
		}
	}
	if strings.HasPrefix(path, "google-github-actions/deploy-") {
		return "action"
	}
	if strings.Contains(path, "/.github/workflows/") {
		owner := strings.SplitN(path, "/", 2)[0]
		providerOwner := owner == "digitalocean" || owner == "aws-actions" || owner == "azure" || owner == "google-github-actions" || owner == "cloudflare"
		livePurpose := strings.Contains(path, "live") || strings.Contains(path, "deploy") || strings.Contains(path, "infra") || strings.Contains(path, "conformance") || strings.Contains(path, "smoke")
		if providerOwner && livePurpose {
			return "reusable workflow"
		}
	}
	return ""
}

func knownCloudSecret(name string) bool {
	name = strings.ToUpper(name)
	markers := []string{"DIGITALOCEAN", "AWS", "AZURE", "GCP", "GOOGLE_CLOUD", "CLOUDFLARE"}
	for _, marker := range markers {
		if strings.Contains(name, marker) {
			return true
		}
	}
	if strings.HasPrefix(name, "DO_") && regexp.MustCompile(`(TOKEN|KEY|SECRET|CREDENTIAL)`).MatchString(name) {
		return true
	}
	return strings.Contains(name, "KUBE") && regexp.MustCompile(`(CONFIG|TOKEN|SECRET|CREDENTIAL)`).MatchString(name)
}

func normalizePath(repoRoot, path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(repoRoot, abs)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

func main() {
	if len(os.Args) < 4 || os.Args[1] != "--repo" || os.Args[3] != "--allowlist" {
		fmt.Fprintln(os.Stderr, "usage: scanner --repo ROOT --allowlist FILE WORKFLOW...")
		os.Exit(2)
	}
	repoRoot, err := filepath.Abs(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	allowPath := os.Args[4]
	workflowArgs := os.Args[5:]

	allowFile, err := os.Open(allowPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read allowlist: %v\n", err)
		os.Exit(1)
	}
	defer allowFile.Close()
	var allowlist []allowEntry
	decoder := json.NewDecoder(allowFile)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&allowlist); err != nil {
		fmt.Fprintf(os.Stderr, "parse allowlist: %v\n", err)
		os.Exit(1)
	}

	findings := &findingSet{}
	allowed := make(map[string]allowEntry)
	for _, entry := range allowlist {
		entry.Path = filepath.ToSlash(filepath.Clean(entry.Path))
		entry.Secret = strings.ToUpper(strings.TrimSpace(entry.Secret))
		key := entry.Path + "\x00" + entry.Secret
		if entry.Path == "." || strings.TrimSpace(entry.Secret) == "" || strings.TrimSpace(entry.Rationale) == "" {
			findings.add("invalid allowlist entry for %s: exact path, secret, and rationale are required", entry.Path)
		}
		if _, exists := allowed[key]; exists {
			findings.add("duplicate allowlist entry %s in %s", entry.Secret, entry.Path)
		}
		if knownCloudSecret(entry.Secret) {
			findings.add("known cloud secret %s is categorically unallowlistable in %s", entry.Secret, entry.Path)
		}
		allowed[key] = entry
	}

	referenced := make(map[string]bool)
	for _, workflowPath := range workflowArgs {
		rel, err := normalizePath(repoRoot, workflowPath)
		if err != nil {
			findings.add("normalize workflow path %s: %v", workflowPath, err)
			continue
		}
		data, err := os.ReadFile(workflowPath)
		if err != nil {
			findings.add("read workflow %s: %v", rel, err)
			continue
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil {
			findings.add("parse workflow %s: %v", rel, err)
			continue
		}
		if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
			findings.add("workflow %s must contain a YAML mapping", rel)
			continue
		}
		root := doc.Content[0]
		if hasMappingScalar(root, "id-token", "write") {
			findings.add("workflow %s grants forbidden id-token: write", rel)
		}
		credentialVariables := make(map[string]bool)
		knownCredentialVariables(root, credentialVariables)
		for name := range credentialVariables {
			findings.add("workflow %s declares known cloud credential variable %s", rel, name)
		}
		globalSecrets := make(map[string]bool)
		for i := 0; i+1 < len(root.Content); i += 2 {
			if root.Content[i].Value == "jobs" {
				continue
			}
			for secret := range secretReferences(root.Content[i+1]) {
				globalSecrets[secret] = true
			}
		}
		validateSecretReferences(rel, "workflow "+rel, globalSecrets, allowed, referenced, findings)
		manual := triggerPresent(root, "workflow_dispatch")
		scheduled := triggerPresent(root, "schedule")
		jobs := mappingValue(root, "jobs")
		if jobs == nil || jobs.Kind != yaml.MappingNode {
			findings.add("workflow %s must declare jobs", rel)
			continue
		}
		for i := 0; i+1 < len(jobs.Content); i += 2 {
			jobName := jobs.Content[i].Value
			job := jobs.Content[i+1]
			prefix := rel + " job " + jobName

			checkRunnerSelector(prefix, mappingValue(job, "runs-on"), findings)

			var permissionValues []string
			permissions := mappingValue(job, "permissions")
			if permissions == nil {
				permissions = mappingValue(root, "permissions")
			}
			if idToken := mappingValue(permissions, "id-token"); idToken != nil {
				scalars(idToken, &permissionValues)
				for _, value := range permissionValues {
					if strings.EqualFold(value, "write") {
						findings.add("%s grants forbidden id-token: write", prefix)
					}
				}
			}

			jobSecrets := make(map[string]bool)
			for secret := range globalSecrets {
				jobSecrets[secret] = true
			}
			localSecrets := secretReferences(job)
			validateSecretReferences(rel, prefix, localSecrets, allowed, referenced, findings)
			for secret := range localSecrets {
				jobSecrets[secret] = true
			}

			providerAuthority := false
			hasIntegrationTag := false
			hasProviderSDK := false
			namedLive := false
			if uses := mappingValue(job, "uses"); uses != nil && uses.Kind == yaml.ScalarNode {
				if kind := providerUsesKind(uses.Value); kind != "" {
					providerAuthority = true
					findings.add("%s invokes forbidden provider %s %s", prefix, kind, uses.Value)
				}
			}
			steps := mappingValue(job, "steps")
			if steps != nil && steps.Kind == yaml.SequenceNode {
				for _, step := range steps.Content {
					if uses := mappingValue(step, "uses"); uses != nil && uses.Kind == yaml.ScalarNode {
						if kind := providerUsesKind(uses.Value); kind != "" {
							providerAuthority = true
							findings.add("%s invokes forbidden provider %s %s", prefix, kind, uses.Value)
						}
					}
					run := mappingValue(step, "run")
					if run == nil || run.Kind != yaml.ScalarNode {
						continue
					}
					for _, line := range strings.Split(run.Value, "\n") {
						trimmed := strings.TrimSpace(line)
						if trimmed == "" || strings.HasPrefix(trimmed, "#") || lineIsNegativeGuard(trimmed) {
							continue
						}
						if match := providerCLIRE.FindStringSubmatch(trimmed); match != nil {
							providerAuthority = true
							findings.add("%s executes forbidden provider authority: executable provider CLI %s", prefix, match[2])
						}
						if match := providerAPIRE.FindStringSubmatch(trimmed); match != nil {
							providerAuthority = true
							findings.add("%s executes forbidden fixed provider API %s", prefix, match[1])
						}
						if providerSDKRE.MatchString(trimmed) {
							hasProviderSDK = true
						}
						if integrationRE.MatchString(trimmed) {
							hasIntegrationTag = true
						}
						if namedLiveRE.MatchString(trimmed) {
							namedLive = true
							providerAuthority = true
							findings.add("%s invokes forbidden named live test", prefix)
						}
					}
				}
			}
			for secret := range jobSecrets {
				if knownCloudSecret(secret) {
					providerAuthority = true
				}
			}
			if providerAuthority {
				for secret := range jobSecrets {
					findings.add("%s combines provider authority with secret %s", prefix, secret)
				}
				if hasIntegrationTag {
					findings.add("%s combines integration tag with provider authority", prefix)
				}
				if manual {
					findings.add("%s is a forbidden manual provider-authority job", prefix)
				}
				if scheduled {
					findings.add("%s is a forbidden scheduled provider-authority job", prefix)
				}
			}
			if hasProviderSDK && (providerAuthority || namedLive) {
				findings.add("%s combines provider SDK marker with provider authority", prefix)
			}
		}
	}

	for key, entry := range allowed {
		if !referenced[key] {
			findings.add("stale allowlist entry %s in %s", entry.Secret, entry.Path)
		}
	}

	if len(findings.items) > 0 {
		sort.Strings(findings.items)
		for _, finding := range findings.items {
			fmt.Fprintln(os.Stderr, "policy:", finding)
		}
		os.Exit(1)
	}
	fmt.Printf("public workflow policy passed for %d workflow(s)\n", len(workflowArgs))
}
GO

GOWORK=off go run "${scanner}" --repo "${repo_root}" --allowlist "${allowlist}" "${workflows[@]}"
