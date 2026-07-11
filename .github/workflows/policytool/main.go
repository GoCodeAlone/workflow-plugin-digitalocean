package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"mvdan.cc/sh/v3/syntax"
)

type allowEntry struct {
	Path      string `json:"path"`
	Secret    string `json:"secret"`
	Rationale string `json:"rationale"`
}

type executableEntry struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	Rationale string `json:"rationale"`
}

type commandEntry struct {
	Path             string `json:"path"`
	Command          string `json:"command"`
	InvocationSHA256 string `json:"invocationSHA256"`
	Rationale        string `json:"rationale"`
}

type actionEntry struct {
	Path      string `json:"path"`
	Uses      string `json:"uses"`
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

var (
	secretRefRE          = regexp.MustCompile(`(?i)secrets(?:\.([A-Za-z_][A-Za-z0-9_]*)|\[[[:space:]]*['"]([A-Za-z_][A-Za-z0-9_]*)['"][[:space:]]*\])`)
	providerCLIRE        = regexp.MustCompile(`(^|[^A-Za-z0-9_.-])(doctl|gcloud|az|aws)([^A-Za-z0-9_.-]|$)`)
	integrationRE        = regexp.MustCompile(`(^|[[:space:]])-tags(?:=|[[:space:]])[^[:space:]]*integration`)
	namedLiveRE          = regexp.MustCompile(`(?i)(-run[=[:space:]]+[^[:space:]]*live|test[A-Za-z0-9_]*live|conformance_live_cloud)`)
	providerSDKRE        = regexp.MustCompile(`(?i)(digitalocean/godo|aws-sdk|azure-sdk|cloud\.google\.com/go|google-cloud-)`)
	providerAPIRE        = regexp.MustCompile(`(?i)(api\.digitalocean\.com|management\.azure\.com|[A-Za-z0-9.-]+\.amazonaws\.com|[A-Za-z0-9.-]+\.googleapis\.com|api\.cloudflare\.com)`)
	githubRunnerRE       = regexp.MustCompile(`^(ubuntu-(latest|[0-9]{2}\.[0-9]{2})(-arm)?|windows-(latest|[0-9]{4})|macos-(latest|[0-9]{2})(-(large|xlarge))?)$`)
	secretIndexRE        = regexp.MustCompile(`(?i)secrets\[[^]\r\n]+\]`)
	secretWildcardRE     = regexp.MustCompile(`(?i)secrets\.\*`)
	wholeSecretsRE       = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_])secrets([^A-Za-z0-9_.\[]|$)`)
	literalSecretIndexRE = regexp.MustCompile(`(?i)^secrets\[[[:space:]]*['"][A-Za-z_][A-Za-z0-9_]*['"][[:space:]]*\]$`)
	varsRefRE            = regexp.MustCompile(`(?i)vars(?:\.([A-Za-z_][A-Za-z0-9_]*)|\[[[:space:]]*['"]([A-Za-z_][A-Za-z0-9_]*)['"][[:space:]]*\])`)
	varsIndexRE          = regexp.MustCompile(`(?i)vars\[[^]\r\n]+\]`)
	literalVarsIndexRE   = regexp.MustCompile(`(?i)^vars\[[[:space:]]*['"][A-Za-z_][A-Za-z0-9_]*['"][[:space:]]*\]$`)
	envAssignmentRE      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
)

func githubExpressions(value string) ([]string, bool) {
	expressions := []string{}
	for {
		start := strings.Index(value, "${{")
		if start < 0 {
			return expressions, true
		}
		value = value[start+3:]
		end := strings.Index(value, "}}")
		if end < 0 {
			return expressions, false
		}
		expressions = append(expressions, strings.TrimSpace(value[:end]))
		value = value[end+2:]
	}
}

func normalizeGithubExpressions(value string) (string, error) {
	var out strings.Builder
	for {
		start := strings.Index(value, "${{")
		if start < 0 {
			out.WriteString(value)
			return out.String(), nil
		}
		out.WriteString(value[:start])
		remainder := value[start+3:]
		end := strings.Index(remainder, "}}")
		if end < 0 {
			return "", fmt.Errorf("unterminated GitHub expression")
		}
		expressionSource := value[start : start+3+end+2]
		digest := sha256.Sum256([]byte(expressionSource))
		fmt.Fprintf(&out, "__GITHUB_EXPRESSION_%x__", digest)
		value = remainder[end+2:]
	}
}

func expressionIdentifiers(value string) string {
	masked := []byte(value)
	for index := 0; index < len(masked); {
		quote := masked[index]
		if quote != '\'' && quote != '"' {
			index++
			continue
		}
		previous := index - 1
		for previous >= 0 && (masked[previous] == ' ' || masked[previous] == '\t') {
			previous--
		}
		// Preserve a quoted bracket selector so exact `secrets['NAME']` and
		// `vars['NAME']` selectors remain statically reviewable. Other string
		// contents are data, not expression identifiers.
		preserve := false
		if previous >= 0 && masked[previous] == '[' {
			selectorPrefix := strings.TrimSpace(string(masked[:previous]))
			preserve = regexp.MustCompile(`(?i)(secrets|vars)$`).MatchString(selectorPrefix)
		}
		index++
		for index < len(masked) {
			if masked[index] == quote {
				if index+1 < len(masked) && masked[index+1] == quote {
					if !preserve {
						masked[index] = ' '
						masked[index+1] = ' '
					}
					index += 2
					continue
				}
				index++
				break
			}
			if !preserve {
				masked[index] = ' '
			}
			index++
		}
	}
	return string(masked)
}

func providerMarker(value string) bool {
	return providerCLIRE.MatchString(value) || providerAPIRE.MatchString(value) || providerSDKRE.MatchString(value)

}

func envValues(prefix string, env *yaml.Node, allowDenyPattern bool, findings *findingSet) map[string]string {
	patterns := make(map[string]string)
	if env == nil {
		return patterns
	}
	if env.Kind != yaml.MappingNode {
		findings.add("%s uses unsupported environment shape", prefix)
		return patterns
	}
	for i := 0; i+1 < len(env.Content); i += 2 {
		name := env.Content[i].Value
		valueNode := env.Content[i+1]
		if valueNode.Kind != yaml.ScalarNode {
			findings.add("%s environment variable %s uses unsupported value shape", prefix, name)
			continue
		}
		value := valueNode.Value
		if strings.HasSuffix(name, "_DENY_PATTERN") && providerMarker(value) {
			if allowDenyPattern {
				patterns[name] = value
			} else {
				findings.add("%s provider deny pattern %s must be step-local", prefix, name)
			}
			continue
		}
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
	return patterns
}

func secretReferences(node *yaml.Node) map[string]bool {
	values := []string{}
	scalars(node, &values)
	secrets := make(map[string]bool)
	for _, value := range values {
		expressions, _ := githubExpressions(value)
		for _, expression := range expressions {
			expression = expressionIdentifiers(expression)
			for _, match := range secretRefRE.FindAllStringSubmatch(expression, -1) {
				name := match[1]
				if name == "" {
					name = match[2]
				}
				secrets[strings.ToUpper(name)] = true
			}
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
		expressions, complete := githubExpressions(value)
		if !complete {
			findings.add("%s contains an unterminated GitHub expression", prefix)
		}
		for _, expression := range expressions {
			originalExpression := expression
			expression = expressionIdentifiers(originalExpression)
			if wholeSecretsRE.MatchString(expression) {
				findings.add("%s uses forbidden whole secrets context", prefix)
			}
			for _, selector := range secretWildcardRE.FindAllString(expression, -1) {
				findings.add("%s uses forbidden dynamic secret selector %s", prefix, selector)
			}
			for _, bounds := range secretIndexRE.FindAllStringIndex(expression, -1) {
				selector := originalExpression[bounds[0]:bounds[1]]
				if !literalSecretIndexRE.MatchString(selector) {
					findings.add("%s uses forbidden dynamic secret selector %s", prefix, selector)
				}
			}
			for _, match := range varsRefRE.FindAllStringSubmatch(expression, -1) {
				name := match[1]
				if name == "" {
					name = match[2]
				}
				name = strings.ToUpper(name)
				if knownCloudSecret(name) {
					findings.add("%s references known cloud credential variable reference %s", prefix, name)
				}
			}
			for _, bounds := range varsIndexRE.FindAllStringIndex(expression, -1) {
				selector := originalExpression[bounds[0]:bounds[1]]
				if !literalVarsIndexRE.MatchString(selector) {
					findings.add("%s uses forbidden dynamic variable selector %s", prefix, selector)
				}
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

func defaultsRunShell(node *yaml.Node) *yaml.Node {
	defaults := mappingValue(node, "defaults")
	run := mappingValue(defaults, "run")
	return mappingValue(run, "shell")
}

func checkShell(prefix string, shell *yaml.Node, findings *findingSet) {
	if shell == nil {
		return
	}
	if shell.Kind != yaml.ScalarNode || strings.Contains(shell.Value, "${{") {
		findings.add("%s uses forbidden dynamic shell %s", prefix, shell.Value)
		return
	}
	if strings.TrimSpace(shell.Value) != "bash" {
		findings.add("%s uses forbidden custom shell %s", prefix, shell.Value)
	}
}

func knownCloudSecret(name string) bool {
	name = strings.ToUpper(name)
	spacesCredentials := map[string]bool{
		"SPACES_ACCESS_KEY_ID":                  true,
		"SPACES_SECRET_ACCESS_KEY":              true,
		"DIGITALOCEAN_SPACES_ACCESS_KEY_ID":     true,
		"DIGITALOCEAN_SPACES_SECRET_ACCESS_KEY": true,
		"DO_SPACES_ACCESS_KEY_ID":               true,
		"DO_SPACES_SECRET_ACCESS_KEY":           true,
	}
	if spacesCredentials[name] {
		return true
	}
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

func decodeJSONFile(filePath string, target any) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return fmt.Errorf("unexpected trailing JSON data: %w", err)
	}
	return nil
}

func invocationDigest(call *syntax.CallExpr) (string, error) {
	var canonical bytes.Buffer
	if err := syntax.NewPrinter().Print(&canonical, call); err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical.Bytes())
	return fmt.Sprintf("%x", digest), nil
}

func commandKey(workflowPath, command, invocationSHA256 string) string {
	return workflowPath + "\x00" + command + "\x00" + invocationSHA256
}

func actionKey(workflowPath, uses string) string {
	return workflowPath + "\x00" + uses
}

func matchAction(workflowPath, uses string, allowed map[string]actionEntry) (string, bool) {
	key := actionKey(workflowPath, uses)
	_, ok := allowed[key]
	return key, ok
}

func knownProviderCommand(command string, prefix []string) bool {
	forbidden := map[string]bool{
		"ansible": true, "aws": true, "az": true, "curl": true,
		"doctl": true, "docker": true, "gcloud": true, "helm": true,
		"http": true, "https": true, "kubectl": true, "mc": true,
		"node": true, "npm": true, "npx": true, "perl": true,
		"php": true, "powershell": true, "pulumi": true, "pwsh": true,
		"python": true, "python3": true, "rclone": true, "ruby": true,
		"s3cmd": true, "terraform": true, "tofu": true, "wget": true,
	}
	if forbidden[strings.ToLower(command)] {
		return true
	}
	return strings.EqualFold(command, "go") && len(prefix) > 0 && prefix[0] == "run"
}

func pathEscapes(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel)
}

func literalParts(parts []syntax.WordPart, out *strings.Builder) bool {
	for _, part := range parts {
		switch part := part.(type) {
		case *syntax.Lit:
			out.WriteString(part.Value)
		case *syntax.SglQuoted:
			out.WriteString(part.Value)
		case *syntax.DblQuoted:
			if !literalParts(part.Parts, out) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func literalWord(word *syntax.Word) (string, bool) {
	if word == nil {
		return "", false
	}
	var out strings.Builder
	if !literalParts(word.Parts, &out) {
		return "", false
	}
	return out.String(), true
}

func exactParameterParts(parts []syntax.WordPart) (string, bool) {
	if len(parts) != 1 {
		return "", false
	}
	switch part := parts[0].(type) {
	case *syntax.ParamExp:
		if part.Param == nil || part.Excl || part.Length || part.Width || part.IsSet || part.Index != nil || part.Slice != nil || part.Repl != nil || part.Exp != nil || part.Names != 0 || len(part.Modifiers) != 0 {
			return "", false
		}
		return part.Param.Value, true
	case *syntax.DblQuoted:
		return exactParameterParts(part.Parts)
	default:
		return "", false
	}
}

func exactParameterWord(word *syntax.Word) (string, bool) {
	if word == nil {
		return "", false
	}
	return exactParameterParts(word.Parts)
}

func skipWrapperOptions(args []*syntax.Word, index int, wrapper string) (int, bool) {
	for index < len(args) {
		value, literal := literalWord(args[index])
		if !literal {
			return index, false
		}
		if value == "--" {
			return index + 1, true
		}
		if wrapper == "env" && envAssignmentRE.MatchString(value) {
			index++
			continue
		}
		if !strings.HasPrefix(value, "-") || value == "-" {
			return index, true
		}
		// Wrapper options can consume or embed command words. Reject them
		// instead of guessing where execution begins.
		return index, false
	}
	return index, true
}

func resolvedProgram(call *syntax.CallExpr) (*syntax.Word, []*syntax.Word, bool) {
	args := call.Args
	index := 0
	for index < len(args) {
		value, literal := literalWord(args[index])
		if !literal || strings.Contains(value, "__GITHUB_EXPRESSION_") {
			return args[index], nil, false
		}
		base := path.Base(value)
		if base == "sudo" {
			return args[index], nil, false
		}
		if base != "command" && base != "exec" && base != "env" {
			return args[index], args[index+1:], true
		}
		index++
		var ok bool
		index, ok = skipWrapperOptions(args, index, base)
		if !ok {
			if index < len(args) {
				return args[index], nil, false
			}
			return nil, nil, false
		}
	}
	return nil, nil, true
}

func resolvedWorkflowCommand(call *syntax.CallExpr) (*syntax.Word, []*syntax.Word, bool) {
	program, args, resolved := resolvedProgram(call)
	if !resolved || program == nil {
		return program, args, resolved
	}
	value, literal := literalWord(program)
	if !literal {
		return program, nil, false
	}
	base := path.Base(value)
	if base != "bash" && base != "sh" && base != "source" && base != "." {
		return program, args, true
	}
	index := 0
	for index < len(args) {
		argument, ok := literalWord(args[index])
		if !ok {
			return args[index], nil, false
		}
		if argument == "--" {
			index++
			break
		}
		if !strings.HasPrefix(argument, "-") || argument == "-" {
			break
		}
		// Shell options such as -c can execute arbitrary text and are never
		// treated as a local-script invocation.
		return args[index], nil, false
	}
	if index >= len(args) {
		return nil, nil, false
	}
	return args[index], args[index+1:], true
}

func directCall(stmt *syntax.Stmt) (*syntax.CallExpr, bool) {
	if stmt == nil || stmt.Negated || stmt.Background || len(stmt.Redirs) != 0 {
		return nil, false
	}
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	return call, ok && len(call.Assigns) == 0
}

func pureRejectionGuard(file *syntax.File, requiredPatterns map[string]string) bool {
	if file == nil || len(file.Stmts) != 1 {
		return false
	}
	clause, ok := file.Stmts[0].Cmd.(*syntax.IfClause)
	if !ok || clause.Else != nil || len(clause.Cond) != 1 || len(clause.Then) == 0 {
		return false
	}
	condition, ok := directCall(clause.Cond[0])
	if !ok || len(condition.Args) < 3 {
		return false
	}
	tool, literal := literalWord(condition.Args[0])
	if !literal || !map[string]bool{"rg": true, "grep": true, "egrep": true, "fgrep": true}[path.Base(tool)] {
		return false
	}
	patternIndex := 1
	for patternIndex < len(condition.Args) {
		value, ok := literalWord(condition.Args[patternIndex])
		if !ok || !strings.HasPrefix(value, "-") || value == "-" {
			break
		}
		patternIndex++
	}
	if patternIndex >= len(condition.Args)-1 {
		return false
	}
	if len(requiredPatterns) > 0 {
		name, ok := exactParameterWord(condition.Args[patternIndex])
		if !ok {
			return false
		}
		if _, ok := requiredPatterns[name]; !ok || len(requiredPatterns) != 1 {
			return false
		}
	} else if _, ok := literalWord(condition.Args[patternIndex]); !ok {
		return false
	}
	for _, argument := range condition.Args[patternIndex+1:] {
		if _, ok := literalWord(argument); !ok {
			return false
		}
	}
	failed := false
	for _, stmt := range clause.Then {
		call, ok := directCall(stmt)
		if !ok || len(call.Args) == 0 {
			return false
		}
		command, ok := literalWord(call.Args[0])
		if !ok {
			return false
		}
		switch path.Base(command) {
		case "echo", "printf":
			for _, argument := range call.Args[1:] {
				if _, ok := literalWord(argument); !ok {
					return false
				}
			}
		case "false":
			if len(call.Args) != 1 {
				return false
			}
			failed = true
		case "exit":
			if len(call.Args) != 2 {
				return false
			}
			code, ok := literalWord(call.Args[1])
			if !ok || code == "0" {
				return false
			}
			failed = true
		default:
			return false
		}
	}
	return failed
}

type shellAnalysis struct {
	providerAuthority bool
	hasIntegrationTag bool
	hasProviderSDK    bool
	namedLive         bool
}

func inspectShell(prefix, workflowPath, source string, file *syntax.File, pureGuard bool, repoRoot string, executables map[string]executableEntry, executableReferenced map[string]bool, commands map[string]commandEntry, commandReferenced map[string]bool, findings *findingSet) shellAnalysis {
	analysis := shellAnalysis{
		hasIntegrationTag: integrationRE.MatchString(source),
		hasProviderSDK:    providerSDKRE.MatchString(source),
		namedLive:         namedLiveRE.MatchString(source),
	}
	if analysis.namedLive {
		analysis.providerAuthority = true
		findings.add("%s invokes forbidden named live test", prefix)
	}
	if !pureGuard {
		if match := providerAPIRE.FindStringSubmatch(source); match != nil {
			analysis.providerAuthority = true
			findings.add("%s executes forbidden fixed provider API %s", prefix, match[1])
		}
	}
	syntax.Walk(file, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		commandWord, commandWords, resolved := resolvedWorkflowCommand(call)
		command, literal := literalWord(commandWord)
		if commandWord == nil || !resolved || !literal || strings.Contains(command, "__GITHUB_EXPRESSION_") {
			findings.add("%s uses forbidden dynamic command execution", prefix)
			return true
		}
		base := strings.ToLower(path.Base(command))
		if map[string]bool{"doctl": true, "gcloud": true, "az": true, "aws": true}[base] {
			analysis.providerAuthority = true
			findings.add("%s executes forbidden provider authority: executable provider CLI %s", prefix, base)
		}
		if strings.HasPrefix(command, "./") || strings.HasPrefix(command, "../") || filepath.IsAbs(command) {
			candidate := command
			if !filepath.IsAbs(candidate) {
				candidate = filepath.Join(repoRoot, candidate)
			}
			abs, err := filepath.Abs(candidate)
			if err != nil {
				findings.add("%s cannot resolve workflow executable path %s", prefix, command)
				return true
			}
			lexicalRel, err := filepath.Rel(repoRoot, abs)
			if err != nil || pathEscapes(lexicalRel) {
				findings.add("%s workflow executable path %s is outside repository", prefix, command)
				return true
			}
			scriptRel := filepath.ToSlash(lexicalRel)
			if _, ok := executables[scriptRel]; !ok {
				findings.add("%s invokes unallowlisted executable script %s", prefix, command)
			} else {
				executableReferenced[scriptRel] = true
			}
			return true
		}
		builtins := map[string]bool{
			"[": true, "echo": true, "exit": true, "false": true,
			"printf": true, "set": true, "test": true, "true": true,
		}
		if builtins[base] {
			return true
		}
		if pureGuard && map[string]bool{"rg": true, "grep": true, "egrep": true, "fgrep": true}[base] {
			// pureRejectionGuard proved this search can only reject content and
			// cannot pass its pattern or matches to an execution path.
			return true
		}
		argv := make([]string, 0, len(commandWords))
		for _, word := range commandWords {
			value, ok := literalWord(word)
			if !ok || strings.Contains(value, "__GITHUB_EXPRESSION_") {
				argv = append(argv, "__DYNAMIC_ARGUMENT__")
				continue
			}
			argv = append(argv, value)
		}
		if knownProviderCommand(base, argv) {
			findings.add("%s executes categorically forbidden command %s with argv %q", prefix, base, argv)
			return true
		}
		invocationSHA256, err := invocationDigest(call)
		if err != nil {
			findings.add("%s cannot canonicalize command %s: %v", prefix, base, err)
			return true
		}
		key := commandKey(workflowPath, base, invocationSHA256)
		if _, ok := commands[key]; !ok {
			findings.add("%s executes unreviewed exact invocation of %s (sha256:%s)", prefix, base, invocationSHA256)
		} else {
			commandReferenced[key] = true
		}
		return true
	})
	return analysis
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
	var repoArg, allowPath, executablePath, commandPath, actionPath string
	workflowArgs := []string{}
	args := os.Args[1:]
	for len(args) > 0 {
		switch args[0] {
		case "--repo":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "--repo requires a path")
				os.Exit(2)
			}
			repoArg = args[1]
			args = args[2:]
		case "--allowlist":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "--allowlist requires a path")
				os.Exit(2)
			}
			allowPath = args[1]
			args = args[2:]
		case "--executable-allowlist":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "--executable-allowlist requires a path")
				os.Exit(2)
			}
			executablePath = args[1]
			args = args[2:]
		case "--command-allowlist":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "--command-allowlist requires a path")
				os.Exit(2)
			}
			commandPath = args[1]
			args = args[2:]
		case "--action-allowlist":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "--action-allowlist requires a path")
				os.Exit(2)
			}
			actionPath = args[1]
			args = args[2:]
		case "--":
			workflowArgs = append(workflowArgs, args[1:]...)
			args = nil
		default:
			if strings.HasPrefix(args[0], "-") {
				fmt.Fprintf(os.Stderr, "unknown option: %s\n", args[0])
				os.Exit(2)
			}
			workflowArgs = append(workflowArgs, args[0])
			args = args[1:]
		}
	}
	if repoArg == "" {
		fmt.Fprintln(os.Stderr, "usage: policytool --repo ROOT [--allowlist FILE] [--executable-allowlist FILE] [--command-allowlist FILE] [--action-allowlist FILE] [WORKFLOW...]")
		os.Exit(2)
	}
	repoRoot, err := filepath.Abs(repoArg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	resolvedRepoRoot, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve repository root: %v\n", err)
		os.Exit(2)
	}
	if allowPath == "" {
		allowPath = filepath.Join(repoRoot, ".github", "public-workflow-secret-allowlist.json")
	}
	if executablePath == "" {
		executablePath = filepath.Join(repoRoot, ".github", "public-workflow-executable-allowlist.json")
	}
	if commandPath == "" {
		commandPath = filepath.Join(repoRoot, ".github", "public-workflow-command-allowlist.json")
	}
	if actionPath == "" {
		actionPath = filepath.Join(repoRoot, ".github", "public-workflow-action-allowlist.json")
	}
	if len(workflowArgs) == 0 {
		for _, pattern := range []string{"*.yml", "*.yaml"} {
			matches, globErr := filepath.Glob(filepath.Join(repoRoot, ".github", "workflows", pattern))
			if globErr != nil {
				fmt.Fprintf(os.Stderr, "glob workflows: %v\n", globErr)
				os.Exit(1)
			}
			workflowArgs = append(workflowArgs, matches...)
		}
	}
	if len(workflowArgs) == 0 {
		fmt.Fprintln(os.Stderr, "no public workflow files found")
		os.Exit(1)
	}

	var allowlist []allowEntry
	if err := decodeJSONFile(allowPath, &allowlist); err != nil {
		fmt.Fprintf(os.Stderr, "read allowlist: %v\n", err)
		os.Exit(1)
	}

	findings := &findingSet{}
	var executableList []executableEntry
	if err := decodeJSONFile(executablePath, &executableList); err != nil {
		fmt.Fprintf(os.Stderr, "read executable allowlist: %v\n", err)
		os.Exit(1)
	}
	var commandList []commandEntry
	if err := decodeJSONFile(commandPath, &commandList); err != nil {
		fmt.Fprintf(os.Stderr, "read command allowlist: %v\n", err)
		os.Exit(1)
	}
	var actionList []actionEntry
	if err := decodeJSONFile(actionPath, &actionList); err != nil {
		fmt.Fprintf(os.Stderr, "read action allowlist: %v\n", err)
		os.Exit(1)
	}
	executables := make(map[string]executableEntry)
	executableReferenced := make(map[string]bool)
	for _, entry := range executableList {
		rawPath := strings.TrimSpace(entry.Path)
		slashPath := strings.ReplaceAll(rawPath, "\\", "/")
		if filepath.IsAbs(rawPath) || path.IsAbs(slashPath) || filepath.VolumeName(rawPath) != "" || slashPath == ".." || strings.HasPrefix(path.Clean(slashPath), "../") {
			findings.add("executable allowlist path %s escapes the repository", rawPath)
			continue
		}
		entry.Path = path.Clean(slashPath)
		if strings.TrimSpace(entry.Rationale) == "" || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(entry.SHA256) {
			findings.add("invalid executable allowlist entry %s", entry.Path)
			continue
		}
		if _, exists := executables[entry.Path]; exists {
			findings.add("duplicate executable allowlist entry %s", entry.Path)
			continue
		}
		abs := filepath.Join(repoRoot, filepath.FromSlash(entry.Path))
		rel, pathErr := normalizePath(repoRoot, resolvedRepoRoot, abs)
		if pathErr != nil {
			findings.add("executable allowlist %v", pathErr)
			continue
		}
		data, readErr := os.ReadFile(abs)
		if readErr != nil {
			findings.add("read executable %s: %v", rel, readErr)
			continue
		}
		actual := fmt.Sprintf("%x", sha256.Sum256(data))
		if actual != entry.SHA256 {
			findings.add("executable hash mismatch for %s", entry.Path)
		}
		executables[entry.Path] = entry
	}
	commands := make(map[string]commandEntry)
	commandReferenced := make(map[string]bool)
	for _, entry := range commandList {
		rawPath := strings.TrimSpace(entry.Path)
		slashPath := strings.ReplaceAll(rawPath, "\\", "/")
		if filepath.IsAbs(rawPath) || path.IsAbs(slashPath) || filepath.VolumeName(rawPath) != "" {
			findings.add("command allowlist path %s must be repository-relative", rawPath)
			continue
		}
		entry.Path = path.Clean(slashPath)
		if entry.Path == ".." || strings.HasPrefix(entry.Path, "../") {
			findings.add("command allowlist path %s escapes the repository", rawPath)
			continue
		}
		entry.Command = strings.ToLower(strings.TrimSpace(entry.Command))
		if entry.Path == "." || !strings.HasPrefix(entry.Path, ".github/workflows/") ||
			entry.Command == "" || entry.Command != path.Base(entry.Command) ||
			!regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(entry.InvocationSHA256) ||
			strings.TrimSpace(entry.Rationale) == "" {
			findings.add("invalid command allowlist entry for %s: exact workflow path, command, invocationSHA256, and rationale are required", entry.Path)
			continue
		}
		if knownProviderCommand(entry.Command, nil) {
			findings.add("provider-capable command %s is categorically unallowlistable in %s", entry.Command, entry.Path)
			continue
		}
		key := commandKey(entry.Path, entry.Command, entry.InvocationSHA256)
		if _, exists := commands[key]; exists {
			findings.add("duplicate command allowlist entry %s sha256:%s in %s", entry.Command, entry.InvocationSHA256, entry.Path)
			continue
		}
		commands[key] = entry
	}
	actions := make(map[string]actionEntry)
	actionReferenced := make(map[string]bool)
	for _, entry := range actionList {
		rawPath := strings.TrimSpace(entry.Path)
		slashPath := strings.ReplaceAll(rawPath, "\\", "/")
		if filepath.IsAbs(rawPath) || path.IsAbs(slashPath) || filepath.VolumeName(rawPath) != "" {
			findings.add("action allowlist path %s must be repository-relative", rawPath)
			continue
		}
		entry.Path = path.Clean(slashPath)
		if entry.Path == ".." || strings.HasPrefix(entry.Path, "../") {
			findings.add("action allowlist path %s escapes the repository", rawPath)
			continue
		}
		entry.Uses = strings.TrimSpace(entry.Uses)
		validReference := regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+@[A-Za-z0-9_.-]+$`).MatchString(entry.Uses)
		if entry.Path == "." || !strings.HasPrefix(entry.Path, ".github/workflows/") ||
			!validReference || strings.Contains(entry.Uses, "${{") || strings.TrimSpace(entry.Rationale) == "" {
			findings.add("invalid action allowlist entry for %s: exact workflow path, full static uses reference, and rationale are required", entry.Path)
			continue
		}
		if providerMarker(entry.Uses) || strings.Contains(strings.ToLower(entry.Uses), "digitalocean/") {
			findings.add("provider action %s is categorically unallowlistable in %s", entry.Uses, entry.Path)
			continue
		}
		key := actionKey(entry.Path, entry.Uses)
		if _, exists := actions[key]; exists {
			findings.add("duplicate action allowlist entry %s in %s", entry.Uses, entry.Path)
			continue
		}
		actions[key] = entry
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
		workflowShell := defaultsRunShell(root)
		checkShell("workflow "+rel, workflowShell, findings)
		envValues("workflow "+rel, mappingValue(root, "env"), false, findings)
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
			jobShell := defaultsRunShell(job)
			checkShell(prefix, jobShell, findings)
			if jobShell == nil {
				jobShell = workflowShell
			}
			envValues(prefix, mappingValue(job, "env"), false, findings)

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
				if strings.Contains(uses.Value, "${{") {
					providerAuthority = true
					findings.add("%s uses forbidden dynamic reusable workflow %s", prefix, uses.Value)
				} else if key, ok := matchAction(rel, uses.Value, actions); !ok {
					providerAuthority = true
					findings.add("%s uses unreviewed exact reusable workflow %s", prefix, uses.Value)
				} else {
					actionReferenced[key] = true
				}
			}
			steps := mappingValue(job, "steps")
			if steps != nil && steps.Kind == yaml.SequenceNode {
				for _, step := range steps.Content {
					if uses := mappingValue(step, "uses"); uses != nil && uses.Kind == yaml.ScalarNode {
						if strings.Contains(uses.Value, "${{") {
							providerAuthority = true
							findings.add("%s uses forbidden dynamic action %s", prefix, uses.Value)
						} else if key, ok := matchAction(rel, uses.Value, actions); !ok {
							providerAuthority = true
							findings.add("%s uses unreviewed exact action %s", prefix, uses.Value)
						} else {
							actionReferenced[key] = true
						}
					}
					denyPatterns := envValues(prefix, mappingValue(step, "env"), true, findings)
					run := mappingValue(step, "run")
					if run == nil || run.Kind != yaml.ScalarNode {
						if len(denyPatterns) > 0 {
							findings.add("%s uses provider deny pattern outside a pure rejection guard", prefix)
						}
						continue
					}
					shell := mappingValue(step, "shell")
					if shell == nil {
						shell = jobShell
					}
					if shell == nil {
						findings.add("%s does not declare an explicit Bash shell", prefix)
					} else {
						checkShell(prefix, shell, findings)
					}
					normalized, err := normalizeGithubExpressions(run.Value)
					if err != nil {
						findings.add("%s shell parse failed: %v", prefix, err)
						continue
					}
					file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(normalized), rel+":"+jobName)
					if err != nil {
						findings.add("%s shell parse failed: %v", prefix, err)
						continue
					}
					pureGuard := pureRejectionGuard(file, denyPatterns)
					if len(denyPatterns) > 0 && !pureGuard {
						findings.add("%s uses provider deny pattern outside a pure rejection guard", prefix)
					}
					analysis := inspectShell(prefix, rel, normalized, file, pureGuard, repoRoot, executables, executableReferenced, commands, commandReferenced, findings)
					providerAuthority = providerAuthority || analysis.providerAuthority
					hasIntegrationTag = hasIntegrationTag || analysis.hasIntegrationTag
					hasProviderSDK = hasProviderSDK || analysis.hasProviderSDK
					namedLive = namedLive || analysis.namedLive
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
		if !executableReferenced[key] {
			findings.add("stale executable allowlist entry %s", key)
		}
	}
	for key, entry := range commands {
		if !commandReferenced[key] {
			findings.add("stale command allowlist entry %s sha256:%s in %s", entry.Command, entry.InvocationSHA256, entry.Path)
		}
	}
	for key, entry := range actions {
		if !actionReferenced[key] {
			findings.add("stale action allowlist entry %s in %s", entry.Uses, entry.Path)
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
