package digitalocean_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// These mandatory Go assertions replace reliance on shell negative searches
// returning false when rg is unavailable. Their input is the real checkout.
func publicWorkflowStaticFindings(files map[string]string) []string {
	var findings []string
	policy := files[".github/workflows/public-workflow-policy.yml"]
	if regexp.MustCompile(`Check out candidate|path: candidate|persist-credentials: true|git (checkout|switch|worktree)`).MatchString(policy) {
		findings = append(findings, "candidate executable checkout")
	}
	if strings.Contains(policy, "verify-public-workflow-branch-protection.sh") {
		findings = append(findings, "privileged governance reader")
	}
	registry := regexp.MustCompile(`repo_dispatch_token|notify-workflow-registry|peter-evans/repository-dispatch`)
	allowedAutomaticToken := regexp.MustCompile(`^GITHUB_TOKEN\b`)
	for path, content := range files {
		workflow := filepath.Dir(path) == ".github/workflows" && (strings.HasSuffix(path, ".yml") || strings.HasSuffix(path, ".yaml"))
		allowlist := path == ".github/public-workflow-secret-allowlist.json" || path == ".github/public-workflow-action-allowlist.json"
		if (workflow || allowlist) && registry.MatchString(content) {
			findings = append(findings, "publisher registry authority")
		}
		if workflow {
			if strings.Contains(content, "secrets[") {
				findings = append(findings, "dynamic secret selector")
			}
			for remaining := content; ; {
				index := strings.Index(remaining, "secrets.")
				if index < 0 {
					break
				}
				remaining = remaining[index+len("secrets."):]
				if !allowedAutomaticToken.MatchString(remaining) {
					findings = append(findings, "named repository secret")
				}
			}
		}
		if path == ".github/workflows/ci.yml" || path == ".github/workflows/iac-host-conformance.yml" || path == ".github/workflows/grpc-version-sync.yml" {
			if regexp.MustCompile(`secrets\.|RELEASES_TOKEN|GOPRIVATE|x-access-token`).MatchString(content) {
				findings = append(findings, "PR workflow credential authority")
			}
		}
	}
	return findings
}

func TestPublicWorkflowPortableStaticAssertions(t *testing.T) {
	files := map[string]string{}
	paths := []string{".github/public-workflow-secret-allowlist.json", ".github/public-workflow-action-allowlist.json"}
	entries, err := os.ReadDir(".github/workflows")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".yml") || strings.HasSuffix(entry.Name(), ".yaml") {
			paths = append(paths, filepath.Join(".github/workflows", entry.Name()))
		}
	}
	for _, path := range paths {
		data, err := bootstrapRegularFile(path, 128*1024)
		if err != nil {
			t.Fatalf("real static assertion input %s: %v", path, err)
		}
		files[path] = string(data)
	}
	for _, required := range []string{".github/workflows/public-workflow-policy.yml", ".github/workflows/ci.yml", ".github/workflows/iac-host-conformance.yml", ".github/workflows/grpc-version-sync.yml"} {
		if files[required] == "" {
			t.Fatalf("mandatory static assertion input missing: %s", required)
		}
	}
	if findings := publicWorkflowStaticFindings(files); len(findings) != 0 {
		t.Fatalf("real portable public workflow assertions: %v", findings)
	}
}

func TestPublicWorkflowPortableStaticAssertionsRejectRealControls(t *testing.T) {
	for _, control := range []struct{ Path, Content string }{
		{".github/workflows/public-workflow-policy.yml", "persist-credentials: true"},
		{".github/workflows/public-workflow-policy.yml", "git switch candidate"},
		{".github/workflows/public-workflow-policy.yml", "verify-public-workflow-branch-protection.sh"},
		{".github/workflows/other.yml", "secrets.DIGITALOCEAN_TOKEN"},
		{".github/workflows/other.yml", "secrets.GITHUB_TOKEN_SUFFIX"},
		{".github/workflows/other.yml", "secrets['GITHUB_TOKEN']"},
		{".github/public-workflow-action-allowlist.json", "peter-evans/repository-dispatch"},
		{".github/workflows/ci.yml", "secrets.GITHUB_TOKEN"},
		{".github/workflows/iac-host-conformance.yml", "GOPRIVATE"},
		{".github/workflows/grpc-version-sync.yml", "x-access-token"},
	} {
		if len(publicWorkflowStaticFindings(map[string]string{control.Path: control.Content})) == 0 {
			t.Fatalf("portable control was accepted: %s", control.Path)
		}
	}
	if findings := publicWorkflowStaticFindings(map[string]string{".github/workflows/release.yml": "secrets.GITHUB_TOKEN"}); len(findings) != 0 {
		t.Fatalf("automatic release token incorrectly rejected: %v", findings)
	}
}

func TestPublicWorkflowFixturesRejectMissingOrBrokenRGBeforeGitOrGo(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal("policy fixture regression requires bash")
	}
	for _, control := range []struct{ Name, RG string }{
		{"missing", ""},
		{"broken", "#!/bin/sh\nexit 127\n"},
		{"always-match", "#!/bin/sh\nexit 0\n"},
		{"never-match", "#!/bin/sh\nexit 1\n"},
	} {
		t.Run(control.Name, func(t *testing.T) {
			bin := t.TempDir()
			sentinel := filepath.Join(bin, "git-or-go-ran")
			for _, child := range []string{"git", "go"} {
				if err := os.WriteFile(filepath.Join(bin, child), []byte("#!/bin/sh\n: > \"$RG_CHILD_SENTINEL\"\nexit 99\n"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if control.RG != "" {
				if err := os.WriteFile(filepath.Join(bin, "rg"), []byte(control.RG), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command(bash, ".github/workflows/scripts/test-public-workflow-policy.sh")
			command.Env = []string{"PATH=" + bin, "RG_CHILD_SENTINEL=" + sentinel}
			output, err := command.CombinedOutput()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 1 || strings.TrimSpace(string(output)) != "public workflow policy fixtures require working rg with PCRE2" {
				t.Fatalf("fixture did not fail closed for %s: %v: %s", control.Name, err, output)
			}
			if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
				t.Fatalf("git or Go was invoked before rejecting %s rg", control.Name)
			}
		})
	}
}
