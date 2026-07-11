package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"mvdan.cc/sh/v3/syntax"
)

func parseShell(t *testing.T, source string) *syntax.File {
	t.Helper()
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(source), "test")
	if err != nil {
		t.Fatalf("parse shell: %v", err)
	}
	return file
}

func firstCall(t *testing.T, file *syntax.File) *syntax.CallExpr {
	t.Helper()
	var call *syntax.CallExpr
	syntax.Walk(file, func(node syntax.Node) bool {
		if call != nil {
			return false
		}
		if candidate, ok := node.(*syntax.CallExpr); ok {
			call = candidate
			return false
		}
		return true
	})
	if call == nil {
		t.Fatal("shell source did not contain a call")
	}
	return call
}

func TestResolvedProgramUnwrapsReviewedWrappers(t *testing.T) {
	file := parseShell(t, `MODE=ci command exec env AUTH=x ./scripts/live.sh`)
	program, _, resolved := resolvedProgram(firstCall(t, file))
	if !resolved {
		t.Fatal("expected wrapped program to resolve")
	}
	value, literal := literalWord(program)
	if !literal || value != "./scripts/live.sh" {
		t.Fatalf("resolved program = %q, literal=%v", value, literal)
	}
}

func TestResolvedProgramRejectsDynamicCommand(t *testing.T) {
	file := parseShell(t, `sudo "$TOOL"`)
	_, _, resolved := resolvedProgram(firstCall(t, file))
	if resolved {
		t.Fatal("dynamic wrapped command resolved as a literal program")
	}
}

func TestPureRejectionGuardConfinesDenyPattern(t *testing.T) {
	patterns := map[string]string{"PROVIDER_DENY_PATTERN": "doctl|api.digitalocean.com"}
	safe := parseShell(t, `if rg "$PROVIDER_DENY_PATTERN" .github/workflows/; then echo blocked; exit 1; fi`)
	if !pureRejectionGuard(safe, patterns) {
		t.Fatal("expected parser-proven rejection guard")
	}
	unsafe := parseShell(t, `if rg "$PROVIDER_DENY_PATTERN" .github/workflows/; then exit 1; fi; "$PROVIDER_DENY_PATTERN"`)
	if pureRejectionGuard(unsafe, patterns) {
		t.Fatal("deny pattern escaped rejection guard")
	}
}

func TestDefaultsRunShell(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("defaults:\n  run:\n    shell: bash\n"), &doc); err != nil {
		t.Fatalf("parse workflow defaults: %v", err)
	}
	shell := defaultsRunShell(doc.Content[0])
	if shell == nil || shell.Value != "bash" {
		t.Fatalf("defaults shell = %#v", shell)
	}
}

func TestExpressionIdentifiersIgnoresStringData(t *testing.T) {
	masked := expressionIdentifiers(`format('No secrets are used', secrets.RELEASES_TOKEN)`)
	if strings.Contains(masked, "No secrets are used") {
		t.Fatalf("expression string data was not masked: %q", masked)
	}
	if !strings.Contains(masked, "secrets.RELEASES_TOKEN") {
		t.Fatalf("secret identifier was masked: %q", masked)
	}
}

func TestCommandAllowlistRequiresExactPrefix(t *testing.T) {
	allowed := map[string]commandEntry{
		commandKey(".github/workflows/ci.yml", "go", []string{"test"}): {
			Path:       ".github/workflows/ci.yml",
			Command:    "go",
			ArgvPrefix: []string{"test"},
			Rationale:  "Run the Go test suite without allowing go run.",
		},
	}
	if _, ok := matchCommand(".github/workflows/ci.yml", "go", []string{"test", "./..."}, allowed); !ok {
		t.Fatal("reviewed go test prefix did not match")
	}
	if _, ok := matchCommand(".github/workflows/ci.yml", "go", []string{"run", "./cmd/tool"}, allowed); ok {
		t.Fatal("wrong go subcommand matched reviewed prefix")
	}
	if _, ok := matchCommand(".github/workflows/release.yml", "go", []string{"test", "./..."}, allowed); ok {
		t.Fatal("command allowlist leaked across workflow paths")
	}
}

func TestResolvedProgramUnwrapsAllowedWrappersButRejectsSudo(t *testing.T) {
	for _, source := range []string{
		`command go test ./...`,
		`exec go test ./...`,
		`env GOWORK=off go test ./...`,
	} {
		file := parseShell(t, source)
		program, args, resolved := resolvedProgram(firstCall(t, file))
		value, literal := literalWord(program)
		if !resolved || !literal || value != "go" || len(args) < 1 {
			t.Fatalf("%q resolved to %q, args=%d, resolved=%v", source, value, len(args), resolved)
		}
	}
	file := parseShell(t, `sudo go test ./...`)
	if _, _, resolved := resolvedProgram(firstCall(t, file)); resolved {
		t.Fatal("sudo unexpectedly resolved as a reviewed wrapper")
	}
}

func TestKnownCloudSecretRejectsSpacesCredentials(t *testing.T) {
	for _, name := range []string{
		"SPACES_ACCESS_KEY_ID",
		"SPACES_SECRET_ACCESS_KEY",
		"DIGITALOCEAN_SPACES_ACCESS_KEY_ID",
		"DIGITALOCEAN_SPACES_SECRET_ACCESS_KEY",
		"DO_SPACES_ACCESS_KEY_ID",
		"DO_SPACES_SECRET_ACCESS_KEY",
	} {
		if !knownCloudSecret(name) {
			t.Errorf("%s was not categorized as a cloud secret", name)
		}
	}
}

func TestDecodeJSONRequiresExactlyOneValue(t *testing.T) {
	tmp := t.TempDir()
	valid := filepath.Join(tmp, "valid.json")
	if err := os.WriteFile(valid, []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	var target []allowEntry
	if err := decodeJSONFile(valid, &target); err != nil {
		t.Fatalf("valid JSON failed: %v", err)
	}
	for name, content := range map[string]string{
		"trailing-object": `[] {}`,
		"trailing-garbage": `[] garbage`,
	} {
		path := filepath.Join(tmp, name+".json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := decodeJSONFile(path, &target); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
