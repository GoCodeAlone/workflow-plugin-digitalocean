#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
allowlist="${repo_root}/.github/public-workflow-secret-allowlist.json"
executable_allowlist="${repo_root}/.github/public-workflow-executable-allowlist.json"
workflows=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --allowlist)
      [[ $# -ge 2 ]] || { echo "--allowlist requires a path" >&2; exit 2; }
      allowlist="$2"
      shift 2
      ;;
    --executable-allowlist)
      [[ $# -ge 2 ]] || { echo "--executable-allowlist requires a path" >&2; exit 2; }
      executable_allowlist="$2"
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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path"
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

type executableEntry struct {
	Path string `json:"path"`
	SHA256 string `json:"sha256"`
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
	if !guardTool || !strings.HasPrefix(line, "if ") {
		return false
	}
	then := strings.LastIndex(line, "; then")
	if then < 0 || strings.TrimSpace(line[then+len("; then"):]) != "" {
		return false
	}
	condition := strings.TrimSpace(strings.TrimPrefix(line[:then], "if "))
	if strings.Contains(condition, "$(") || strings.Contains(condition, "`") || strings.Contains(condition, "&&") || strings.Contains(condition, "||") || strings.Contains(condition, ";") {
		return false
	}
	fields := strings.Fields(condition)
	if len(fields) == 0 {
		return false
	}
	return fields[0] == "rg" || fields[0] == "grep" || fields[0] == "egrep" || fields[0] == "fgrep"
}

var (
	secretRefRE    = regexp.MustCompile(`(?i)secrets(?:\.([A-Za-z_][A-Za-z0-9_]*)|\[[[:space:]]*['"]([A-Za-z_][A-Za-z0-9_]*)['"][[:space:]]*\])`)
	providerCLIRE  = regexp.MustCompile(`(^|[^A-Za-z0-9_.-])(doctl|gcloud|az|aws)([^A-Za-z0-9_.-]|$)`)
	integrationRE  = regexp.MustCompile(`(^|[[:space:]])-tags(?:=|[[:space:]])[^[:space:]]*integration`)
	namedLiveRE    = regexp.MustCompile(`(?i)(-run[=[:space:]]+[^[:space:]]*live|test[A-Za-z0-9_]*live|conformance_live_cloud)`)
	providerSDKRE  = regexp.MustCompile(`(?i)(digitalocean/godo|aws-sdk|azure-sdk|cloud\.google\.com/go|google-cloud-)`)
	providerAPIRE  = regexp.MustCompile(`(?i)(api\.digitalocean\.com|management\.azure\.com|[A-Za-z0-9.-]+\.amazonaws\.com|[A-Za-z0-9.-]+\.googleapis\.com|api\.cloudflare\.com)`)
	githubRunnerRE = regexp.MustCompile(`^(ubuntu-(latest|[0-9]{2}\.[0-9]{2})(-arm)?|windows-(latest|[0-9]{4})|macos-(latest|[0-9]{2})(-(large|xlarge))?)$`)
	secretIndexRE  = regexp.MustCompile(`(?i)secrets\[[^]\r\n]+\]`)
	secretWildcardRE = regexp.MustCompile(`(?i)secrets\.\*`)
	wholeSecretsRE = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_])secrets([^A-Za-z0-9_.\[]|$)`)
	literalSecretIndexRE = regexp.MustCompile(`(?i)^secrets\[[[:space:]]*['"][A-Za-z_][A-Za-z0-9_]*['"][[:space:]]*\]$`)
	varsRefRE      = regexp.MustCompile(`(?i)vars(?:\.([A-Za-z_][A-Za-z0-9_]*)|\[[[:space:]]*['"]([A-Za-z_][A-Za-z0-9_]*)['"][[:space:]]*\])`)
	varsIndexRE    = regexp.MustCompile(`(?i)vars\[[^]\r\n]+\]`)
	literalVarsIndexRE = regexp.MustCompile(`(?i)^vars\[[[:space:]]*['"][A-Za-z_][A-Za-z0-9_]*['"][[:space:]]*\]$`)
	dynamicCommandRE = regexp.MustCompile(`(^|[;&|][[:space:]]*)["']?\$(\{)?[A-Za-z_][A-Za-z0-9_]*`)
	evalCommandRE = regexp.MustCompile(`(^|[;&|][[:space:]]*)eval([[:space:]]|$)`)
	shellCCommandRE = regexp.MustCompile(`(^|[;&|][[:space:]]*)(bash|sh)[[:space:]]+-c([[:space:]]|$)`)
)

func validateEnvValues(prefix string, node *yaml.Node, findings *findingSet) {
	if node == nil {
		return
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == "env" && node.Content[i+1].Kind == yaml.MappingNode {
				env := node.Content[i+1]
				for j := 0; j+1 < len(env.Content); j += 2 {
					value := env.Content[j+1].Value
					if match := providerCLIRE.FindStringSubmatch(value); match != nil {
						findings.add("%s environment value contains provider CLI %s", prefix, match[2])
					}
					if match := providerAPIRE.FindStringSubmatch(value); match != nil {
						findings.add("%s environment value contains provider API %s", prefix, match[1])
					}
					if providerSDKRE.MatchString(value) {
						findings.add("%s environment value contains provider SDK marker", prefix)
					}
				}
			}
		}
	}
	for _, child := range node.Content {
		validateEnvValues(prefix, child, findings)
	}
}

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

func validateCredentialSelectors(prefix string, node *yaml.Node, findings *findingSet) {
	values := []string{}
	scalars(node, &values)
	for _, value := range values {
		if wholeSecretsRE.MatchString(value) {
			findings.add("%s uses forbidden whole secrets context", prefix)
		}
		for _, selector := range secretWildcardRE.FindAllString(value, -1) {
			findings.add("%s uses forbidden dynamic secret selector %s", prefix, selector)
		}
		for _, selector := range secretIndexRE.FindAllString(value, -1) {
			if !literalSecretIndexRE.MatchString(selector) {
				findings.add("%s uses forbidden dynamic secret selector %s", prefix, selector)
			}
		}
		for _, match := range varsRefRE.FindAllStringSubmatch(value, -1) {
			name := match[1]
			if name == "" {
				name = match[2]
			}
			name = strings.ToUpper(name)
			if knownCloudSecret(name) {
				findings.add("%s references known cloud credential variable reference %s", prefix, name)
			}
		}
		for _, selector := range varsIndexRE.FindAllString(value, -1) {
			if !literalVarsIndexRE.MatchString(selector) {
				findings.add("%s uses forbidden dynamic variable selector %s", prefix, selector)
			}
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

func checkPermissions(prefix string, permissions *yaml.Node, findings *findingSet) {
	if permissions == nil {
		return
	}
	if permissions.Kind == yaml.ScalarNode {
		value := strings.TrimSpace(permissions.Value)
		switch {
		case value == "read-all":
			return
		case value == "write-all":
			findings.add("%s uses forbidden permissions: write-all", prefix)
		case strings.Contains(value, "${{"):
			findings.add("%s uses forbidden dynamic permissions selector %s", prefix, value)
		default:
			findings.add("%s uses unsupported permissions scalar %s", prefix, value)
		}
		return
	}
	if permissions.Kind != yaml.MappingNode {
		findings.add("%s uses unsupported permissions shape", prefix)
		return
	}
	known := map[string]bool{
		"actions": true, "attestations": true, "checks": true, "contents": true,
		"deployments": true, "discussions": true, "id-token": true, "issues": true,
		"models": true, "packages": true, "pages": true, "pull-requests": true,
		"security-events": true, "statuses": true,
	}
	for i := 0; i+1 < len(permissions.Content); i += 2 {
		key := permissions.Content[i].Value
		valueNode := permissions.Content[i+1]
		if !known[key] {
			findings.add("%s declares unsupported permission %s", prefix, key)
			continue
		}
		if valueNode.Kind != yaml.ScalarNode {
			findings.add("%s permission %s uses unsupported value shape", prefix, key)
			continue
		}
		value := strings.TrimSpace(valueNode.Value)
		if strings.Contains(value, "${{") {
			findings.add("%s permission %s uses forbidden dynamic value %s", prefix, key, value)
			continue
		}
		if value != "read" && value != "write" && value != "none" {
			findings.add("%s permission %s uses unsupported value %s", prefix, key, value)
			continue
		}
		if key == "id-token" && value == "write" {
			findings.add("%s grants forbidden id-token: write", prefix)
		}
	}
}

func unreviewedUsesDiagnostic(reference string, jobLevel bool) string {
	lower := strings.ToLower(strings.TrimSpace(reference))
	if jobLevel {
		return fmt.Sprintf("uses unrecognized reusable workflow %s", reference)
	}
	if strings.HasPrefix(lower, "docker://") {
		return fmt.Sprintf("uses Docker action %s is forbidden", reference)
	}
	if strings.HasPrefix(lower, "./") {
		return fmt.Sprintf("uses local action %s is forbidden", reference)
	}
	parts := strings.SplitN(lower, "@", 2)
	if len(parts) == 2 && parts[1] != "" {
		reviewed := map[string]bool{
			"actions/checkout": true,
			"actions/setup-go": true,
			"actions/upload-artifact": true,
			"gocodealone/setup-wfctl": true,
			"goreleaser/goreleaser-action": true,
			"peter-evans/repository-dispatch": true,
		}
		if reviewed[parts[0]] {
			return ""
		}
	}
	return fmt.Sprintf("uses unreviewed action %s", reference)
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

func pathEscapes(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel)
}

func scriptCommands(line string) []string {
	segments := regexp.MustCompile(`[;&]+|[[:space:]]+\|[[:space:]]+|\|\|`).Split(line, -1)
	result := []string{}
	for _, segment := range segments {
		fields := strings.Fields(strings.TrimSpace(segment))
		if len(fields) == 0 { continue }
		i := 0
		if fields[i] == "if" || fields[i] == "then" || fields[i] == "sudo" || fields[i] == "env" { i++ }
		if i >= len(fields) { continue }
		if fields[i] == "bash" || fields[i] == "sh" || fields[i] == "source" || fields[i] == "." { i++ }
		if i >= len(fields) { continue }
		candidate := strings.Trim(fields[i], `"'`)
		if strings.HasPrefix(candidate, "./") || strings.HasPrefix(candidate, "../") || filepath.IsAbs(candidate) {
			result = append(result, candidate)
		}
	}
	return result
}

func normalizePath(repoRoot, resolvedRepoRoot, workflowPath string) (string, error) {
	abs, err := filepath.Abs(workflowPath)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(repoRoot, abs)
	if err != nil {
		return "", err
	}
	if pathEscapes(rel) {
		return "", fmt.Errorf("workflow path %s is outside repository", workflowPath)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve workflow path %s: %w", workflowPath, err)
	}
	resolvedRel, err := filepath.Rel(resolvedRepoRoot, resolved)
	if err != nil {
		return "", err
	}
	if pathEscapes(resolvedRel) {
		return "", fmt.Errorf("workflow path %s resolves outside repository", workflowPath)
	}
	return filepath.ToSlash(rel), nil
}

func main() {
	if len(os.Args) < 8 || os.Args[1] != "--repo" || os.Args[3] != "--allowlist" || os.Args[5] != "--executables" {
		fmt.Fprintln(os.Stderr, "usage: scanner --repo ROOT --allowlist FILE --executables FILE WORKFLOW...")
		os.Exit(2)
	}
	repoRoot, err := filepath.Abs(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	resolvedRepoRoot, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve repository root: %v\n", err)
		os.Exit(2)
	}
	allowPath := os.Args[4]
	executablePath := os.Args[6]
	workflowArgs := os.Args[7:]

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
	executableFile, err := os.Open(executablePath)
	if err != nil { fmt.Fprintf(os.Stderr, "read executable allowlist: %v\n", err); os.Exit(1) }
	defer executableFile.Close()
	var executableList []executableEntry
	executableDecoder := json.NewDecoder(executableFile)
	executableDecoder.DisallowUnknownFields()
	if err := executableDecoder.Decode(&executableList); err != nil { fmt.Fprintf(os.Stderr, "parse executable allowlist: %v\n", err); os.Exit(1) }
	executables := make(map[string]executableEntry)
	executableReferenced := make(map[string]bool)
	for _, entry := range executableList {
		rawPath := strings.TrimSpace(entry.Path)
		slashPath := strings.ReplaceAll(rawPath, "\\", "/")
		if filepath.IsAbs(rawPath) || path.IsAbs(slashPath) || filepath.VolumeName(rawPath) != "" || slashPath == ".." || strings.HasPrefix(path.Clean(slashPath), "../") {
			findings.add("executable allowlist path %s escapes the repository", rawPath); continue
		}
		entry.Path = path.Clean(slashPath)
		if strings.TrimSpace(entry.Rationale) == "" || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(entry.SHA256) { findings.add("invalid executable allowlist entry %s", entry.Path); continue }
		abs := filepath.Join(repoRoot, filepath.FromSlash(entry.Path))
		rel, pathErr := normalizePath(repoRoot, resolvedRepoRoot, abs)
		if pathErr != nil { findings.add("executable allowlist %v", pathErr); continue }
		data, readErr := os.ReadFile(abs)
		if readErr != nil { findings.add("read executable %s: %v", rel, readErr); continue }
		actual := fmt.Sprintf("%x", sha256.Sum256(data))
		if actual != entry.SHA256 { findings.add("executable hash mismatch for %s", entry.Path) }
		executables[entry.Path] = entry
	}
	allowed := make(map[string]allowEntry)
	for _, entry := range allowlist {
		rawPath := strings.TrimSpace(entry.Path)
		slashPath := strings.ReplaceAll(rawPath, "\\", "/")
		if filepath.IsAbs(rawPath) || path.IsAbs(slashPath) || filepath.VolumeName(rawPath) != "" {
			findings.add("allowlist path %s must be repository-relative", rawPath)
			continue
		}
		entry.Path = path.Clean(slashPath)
		if entry.Path == ".." || strings.HasPrefix(entry.Path, "../") {
			findings.add("allowlist path %s escapes the repository", rawPath)
			continue
		}
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
		rel, err := normalizePath(repoRoot, resolvedRepoRoot, workflowPath)
		if err != nil {
			findings.add("%v", err)
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
		checkPermissions("workflow "+rel, mappingValue(root, "permissions"), findings)
		validateEnvValues("workflow "+rel, root, findings)
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
			validateCredentialSelectors("workflow "+rel, root.Content[i+1], findings)
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

			checkPermissions(prefix, mappingValue(job, "permissions"), findings)
			validateEnvValues(prefix, job, findings)

			jobSecrets := make(map[string]bool)
			for secret := range globalSecrets {
				jobSecrets[secret] = true
			}
			localSecrets := secretReferences(job)
			validateCredentialSelectors(prefix, job, findings)
			validateSecretReferences(rel, prefix, localSecrets, allowed, referenced, findings)
			for secret := range localSecrets {
				jobSecrets[secret] = true
			}

			providerAuthority := false
			hasIntegrationTag := false
			hasProviderSDK := false
			namedLive := false
			if uses := mappingValue(job, "uses"); uses != nil && uses.Kind == yaml.ScalarNode {
				if diagnostic := unreviewedUsesDiagnostic(uses.Value, true); diagnostic != "" {
					providerAuthority = true
					findings.add("%s %s", prefix, diagnostic)
				}
			}
			steps := mappingValue(job, "steps")
			if steps != nil && steps.Kind == yaml.SequenceNode {
				for _, step := range steps.Content {
					if uses := mappingValue(step, "uses"); uses != nil && uses.Kind == yaml.ScalarNode {
						if diagnostic := unreviewedUsesDiagnostic(uses.Value, false); diagnostic != "" {
							providerAuthority = true
							findings.add("%s %s", prefix, diagnostic)
						}
					}
					run := mappingValue(step, "run")
					if run == nil || run.Kind != yaml.ScalarNode {
						continue
					}
					for _, line := range strings.Split(run.Value, "\n") {
						trimmed := strings.TrimSpace(line)
						for _, command := range scriptCommands(trimmed) {
							candidate := command
							if !filepath.IsAbs(candidate) { candidate = filepath.Join(repoRoot, candidate) }
							abs, _ := filepath.Abs(candidate)
							lexicalRel, _ := filepath.Rel(repoRoot, abs)
							if pathEscapes(lexicalRel) {
								findings.add("%s workflow executable path %s is outside repository", prefix, command)
								continue
							}
							scriptRel := filepath.ToSlash(lexicalRel)
							if _, ok := executables[scriptRel]; !ok {
								findings.add("%s invokes unallowlisted executable script %s", prefix, command)
							} else {
								executableReferenced[scriptRel] = true
							}
						}
						if dynamicCommandRE.MatchString(trimmed) {
							findings.add("%s uses forbidden dynamic command execution", prefix)
						}
						if evalCommandRE.MatchString(trimmed) {
							findings.add("%s uses forbidden eval", prefix)
						}
						if shellCCommandRE.MatchString(trimmed) {
							findings.add("%s uses forbidden shell -c", prefix)
						}
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
	for key := range executables {
		if !executableReferenced[key] { findings.add("stale executable allowlist entry %s", key) }
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

GOWORK=off go run "${scanner}" --repo "${repo_root}" --allowlist "${allowlist}" --executables "${executable_allowlist}" "${workflows[@]}"
