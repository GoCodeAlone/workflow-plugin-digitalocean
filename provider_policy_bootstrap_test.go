package digitalocean_test

import (
	"bytes"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const bootstrapBase = "0f72e69febaf02e512abb075c3547f5b2eeac5ad"
const bootstrapBaseTree = "365109d6a442c7583bd172a0e6d1dd85c5d46b8d"
const bootstrapInventorySHA = "691f7b85863123faea3f3ce1139b5fb4153ede520377e566e7ced5c4cfd62d14"
const bootstrapRepo = "GoCodeAlone/workflow-plugin-digitalocean"
const bootstrapProofRef = "refs/heads/prep/policytool-bootstrap-proof-20261010"

type bootstrapFile struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Blob   string `json:"blob"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

type bootstrapInventory struct {
	Commit string          `json:"commit"`
	Tree   string          `json:"tree"`
	Files  []bootstrapFile `json:"files"`
}

type bootstrapEvent struct {
	Ref        string `json:"ref"`
	Before     string `json:"before"`
	After      string `json:"after"`
	Number     int    `json:"number"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest struct {
		Head struct {
			SHA  string `json:"sha"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
		Base struct {
			SHA string `json:"sha"`
		} `json:"base"`
	} `json:"pull_request"`
}

type bootstrapBinding struct {
	Mode        string
	Candidate   string
	Base        string
	WorkflowSHA string
	Checkout    string
	Merge       string
}

var bootstrapCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func bindBootstrapEvent(name, workflowRef, workflowSHA, checkout, preparationSHA string, event bootstrapEvent) (bootstrapBinding, error) {
	b := bootstrapBinding{Base: bootstrapBase, WorkflowSHA: workflowSHA, Checkout: checkout}
	if event.Repository.FullName != bootstrapRepo || !bootstrapCommitPattern.MatchString(workflowSHA) || !bootstrapCommitPattern.MatchString(checkout) {
		return b, errors.New("bootstrap requires exact repository and commit identities")
	}
	switch {
	case name == "push" && event.Ref == bootstrapProofRef:
		if workflowRef != bootstrapRepo+"/.github/workflows/ci.yml@"+bootstrapProofRef || event.After != workflowSHA || checkout != preparationSHA || workflowSHA == preparationSHA || !bootstrapCommitPattern.MatchString(preparationSHA) {
			return b, errors.New("preparation must bind helper workflow separately from pinned candidate checkout")
		}
		b.Mode, b.Candidate = "preparation-only", preparationSHA
	case name == "pull_request" && event.Number == 201:
		if preparationSHA != "" || workflowRef != bootstrapRepo+"/.github/workflows/ci.yml@refs/pull/201/merge" || workflowSHA != checkout || event.PullRequest.Base.SHA != bootstrapBase || event.PullRequest.Head.Repo.FullName != bootstrapRepo || !bootstrapCommitPattern.MatchString(event.PullRequest.Head.SHA) {
			return b, errors.New("adoption requires real PR201 on the exact accepted base")
		}
		b.Mode, b.Candidate, b.Merge = "adoption-pr", event.PullRequest.Head.SHA, checkout
	case name == "push" && event.Ref == "refs/heads/main":
		if preparationSHA != "" || workflowRef != bootstrapRepo+"/.github/workflows/ci.yml@refs/heads/main" || event.Before != bootstrapBase || event.After != checkout || workflowSHA != checkout {
			return b, errors.New("bootstrap main proof is only the first adoption push")
		}
		b.Mode, b.Candidate = "first-adoption-main", checkout
	default:
		return b, errors.New("finite bootstrap does not admit this event; canonical restoration and reviewed retirement are required")
	}
	return b, nil
}

func bootstrapHash(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func bootstrapRegularFile(path string, limit int64) ([]byte, error) {
	i, err := os.Lstat(path)
	if err != nil || !i.Mode().IsRegular() || i.Size() > limit {
		return nil, errors.New("bootstrap input must be a bounded regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(i, opened) {
		return nil, errors.New("bootstrap input identity changed")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("bootstrap input exceeds its bound")
	}
	return data, nil
}

func bootstrapSDK(root, sourceRoot string, inventory []bootstrapSDKFile) (map[string]string, error) {
	physical, err := filepath.EvalSymlinks(root)
	if err != nil || physical != root || root != "/opt/hostedtoolcache/go/1.27.2/x64" || strings.HasPrefix(root+"/", sourceRoot+"/") {
		return nil, errors.New("compiler is outside the admitted hosted setup-go installation")
	}
	if err := verifyBootstrapSDKFiles(root, inventory); err != nil {
		return nil, err
	}
	versionData, err := bootstrapRegularFile(filepath.Join(root, "VERSION"), 1024)
	if err != nil || strings.Split(string(versionData), "\n")[0] != "go1.27.2" {
		return nil, errors.New("SDK VERSION is not the patched compiler")
	}
	pins := map[string]string{"VERSION": bootstrapHash(versionData)}
	for _, relative := range []string{"bin/go", "pkg/tool/linux_amd64/compile", "pkg/tool/linux_amd64/link", "pkg/tool/linux_amd64/asm"} {
		path := filepath.Join(root, relative)
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil || canonical != path {
			return nil, errors.New("compiler component is not canonical")
		}
		data, err := bootstrapRegularFile(path, 64*1024*1024)
		if err != nil {
			return nil, err
		}
		info, err := buildinfo.ReadFile(path)
		if err != nil || info.GoVersion != "go1.27.2" {
			return nil, errors.New("compiler component build information is not patched")
		}
		pins[relative] = bootstrapHash(data)
	}
	return pins, nil
}

func bootstrapChildEnv(sdk string) []string {
	// Retain only ordinary existing CI caches. No ambient secrets, custom Go
	// environment, Git helper/extraheader, workspace or proxy credentials.
	return []string{
		"PATH=" + sdk + "/bin:/usr/bin:/bin", "HOME=/home/runner", "LANG=C.UTF-8",
		"GOROOT=" + sdk, "GOENV=off", "GOWORK=off", "GOTOOLCHAIN=local", "GOFLAGS=-mod=readonly",
		"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64", "GOPROXY=https://proxy.golang.org", "GOSUMDB=sum.golang.org",
		"GOPRIVATE=", "GONOPROXY=", "GONOSUMDB=", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/bin/false", "GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=0",
	}
}

func bootstrapGit(t *testing.T, root string, env []string, args ...string) []byte {
	t.Helper()
	options := []string{"-c", "credential.helper=", "-c", "core.askPass=/bin/false", "-c", "credential.interactive=never", "-c", "http.https://github.com/.extraheader=", "-c", "core.hooksPath=/dev/null"}
	output, err := providerProofCommand(t, root, env, "/usr/bin/git", append(options, args...)...)
	if err != nil {
		t.Fatalf("bounded public Git object operation failed: %v", err)
	}
	return output
}

func bootstrapTreeEntries(data []byte) ([]bootstrapFile, error) {
	var entries []bootstrapFile
	seen := map[string]bool{}
	for _, record := range bytes.Split(data, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		parts := bytes.SplitN(record, []byte{'\t'}, 2)
		if len(parts) != 2 {
			return nil, errors.New("malformed Git tree entry")
		}
		header := strings.Fields(string(parts[0]))
		name := string(parts[1])
		if len(header) != 3 || (header[0] != "100644" && header[0] != "100755") || header[1] != "blob" || !bootstrapCommitPattern.MatchString(header[2]) || !safeBootstrapPath(name) || seen[name] {
			return nil, errors.New("unsafe, duplicate or nonregular Git entry")
		}
		seen[name] = true
		entries = append(entries, bootstrapFile{Path: name, Mode: header[0], Blob: header[2]})
	}
	if len(entries) == 0 || len(entries) > 512 {
		return nil, errors.New("Git tree inventory exceeds the finite bound")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func safeBootstrapPath(name string) bool {
	for _, value := range name {
		if value < 32 || value == 127 {
			return false
		}
	}
	return name != "" && name != "." && !strings.Contains(name, "\\") && !filepath.IsAbs(name) && filepath.ToSlash(filepath.Clean(name)) == name && name != ".." && !strings.HasPrefix(name, "../") && !strings.Contains("/"+name+"/", "/.git/")
}

func materializeBootstrapTree(t *testing.T, source, commit, destination string, env []string, expected *bootstrapInventory) bootstrapInventory {
	t.Helper()
	if !bootstrapCommitPattern.MatchString(commit) {
		t.Fatal("tree materialization needs an exact commit")
	}
	tree := strings.TrimSpace(string(bootstrapGit(t, source, env, "rev-parse", commit+"^{tree}")))
	entries, err := bootstrapTreeEntries(bootstrapGit(t, source, env, "ls-tree", "-rz", commit))
	if err != nil {
		t.Fatal(err)
	}
	if expected != nil && (expected.Commit != commit || expected.Tree != tree || len(expected.Files) != len(entries)) {
		t.Fatal("accepted source inventory does not match the real Git tree")
	}
	total := 0
	for i := range entries {
		entry := &entries[i]
		data := bootstrapGit(t, source, env, "cat-file", "blob", entry.Blob)
		entry.Bytes, entry.SHA256 = len(data), bootstrapHash(data)
		total += len(data)
		if len(data) > 2*1024*1024 || total > 8*1024*1024 {
			t.Fatal("complete source tree exceeds the finite byte bound")
		}
		if expected != nil && *entry != expected.Files[i] {
			t.Fatal("accepted blob/mode/hash inventory mismatch")
		}
		path := filepath.Join(destination, filepath.FromSlash(entry.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if entry.Mode == "100755" {
			mode = 0o755
		}
		if err := os.WriteFile(path, data, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	return bootstrapInventory{Commit: commit, Tree: tree, Files: entries}
}

func verifyBootstrapAuthority(root string, inventory bootstrapInventory) error {
	for _, entry := range inventory.Files {
		path := filepath.Join(root, filepath.FromSlash(entry.Path))
		data, err := bootstrapRegularFile(path, 2*1024*1024)
		info, statErr := os.Lstat(path)
		mode := os.FileMode(0o644)
		if entry.Mode == "100755" {
			mode = 0o755
		}
		if err != nil || statErr != nil || info.Mode().Perm() != mode || bootstrapHash(data) != entry.SHA256 || len(data) != entry.Bytes {
			return errors.New("accepted authority bytes changed")
		}
	}
	files, err := os.ReadDir(filepath.Join(root, ".github/workflows/policytool"))
	if err != nil || len(files) != 4 {
		return errors.New("accepted policytool layout changed")
	}
	for _, entry := range files {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || (entry.Name() != "main.go" && entry.Name() != "main_test.go" && entry.Name() != "go.mod" && entry.Name() != "go.sum") {
			return errors.New("unexpected accepted policytool entry")
		}
	}
	return nil
}

func bootstrapReceipt(t *testing.T, value any, compact any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil || len(data) > 256*1024 {
		t.Fatal("bootstrap receipt encoding exceeds the finite bound")
	}
	root := os.Getenv("RUNNER_TEMP")
	physical, err := filepath.EvalSymlinks(root)
	if err != nil || !filepath.IsAbs(root) || physical != root {
		t.Fatal("bootstrap receipt needs canonical existing runner temporary directory")
	}
	path := filepath.Join(root, "provider-policy-bootstrap-receipt.json")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal("bootstrap receipt must have exclusive creation")
	}
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0o600 || opened.Size() != 0 {
		_ = f.Close()
		t.Fatal("bootstrap receipt opening identity or mode is invalid")
	}
	n, writeErr := f.Write(append(data, '\n'))
	syncErr := f.Sync()
	final, finalErr := f.Stat()
	current, currentErr := os.Lstat(path)
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || n != len(data)+1 || finalErr != nil || currentErr != nil || !current.Mode().IsRegular() || !os.SameFile(opened, final) || !os.SameFile(final, current) || final.Size() != int64(n) || final.Mode().Perm() != 0o600 {
		t.Fatal("complete bootstrap receipt persistence failed")
	}
	stored, err := bootstrapRegularFile(path, 256*1024+1)
	closed, closedErr := os.Lstat(path)
	if err != nil || closedErr != nil || !os.SameFile(final, closed) || closed.Size() != int64(n) || closed.Mode().Perm() != 0o600 || !bytes.Equal(stored, append(data, '\n')) {
		t.Fatal("complete bootstrap receipt readback failed")
	}
	providerProofSummary(t, "actual accepted policy bootstrap", map[string]any{"ReceiptSHA256": bootstrapHash(stored), "ReceiptBytes": len(stored), "Proof": compact})
}

func bootstrapRunIdentity(runID, attempt string) error {
	positive := regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	if !positive.MatchString(runID) || !positive.MatchString(attempt) {
		return errors.New("bootstrap needs positive exact decimal run and attempt identities")
	}
	return nil
}

func verifyBootstrapCandidateTree(checkoutTree, candidateTree string) error {
	if !bootstrapCommitPattern.MatchString(checkoutTree) || !bootstrapCommitPattern.MatchString(candidateTree) || checkoutTree != candidateTree {
		return errors.New("executed checkout/tested merge tree differs from the complete candidate tree")
	}
	return nil
}

func TestActualAcceptedPolicyBootstrap(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("GITHUB_REPOSITORY") != bootstrapRepo || runtime.Version() != "go1.27.2" || runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Fatal("finite bootstrap requires the admitted hosted Go1.27.2 producer; no local or generic event fallback")
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	sdk := runtime.GOROOT()
	env := bootstrapChildEnv(sdk)
	checkout := strings.TrimSpace(string(bootstrapGit(t, root, env, "rev-parse", "HEAD")))
	eventData, err := bootstrapRegularFile(os.Getenv("GITHUB_EVENT_PATH"), 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrapRunIdentity(os.Getenv("GITHUB_RUN_ID"), os.Getenv("GITHUB_RUN_ATTEMPT")); err != nil {
		t.Fatal(err)
	}
	var event bootstrapEvent
	if err := json.Unmarshal(eventData, &event); err != nil {
		t.Fatal("invalid actual GitHub event metadata")
	}
	binding, err := bindBootstrapEvent(os.Getenv("GITHUB_EVENT_NAME"), os.Getenv("GITHUB_WORKFLOW_REF"), os.Getenv("GITHUB_WORKFLOW_SHA"), checkout, os.Getenv("PROVIDER_BOOTSTRAP_PREPARATION_SHA"), event)
	if err != nil {
		t.Fatal(err)
	}
	sdkArchive := bootstrapSDKArchive(t)
	sdkInventory, err := bootstrapSupplierSDK(sdkArchive)
	if err != nil {
		t.Fatal(err)
	}
	sdkPins, err := bootstrapSDK(sdk, root, sdkInventory)
	if err != nil {
		t.Fatal(err)
	}
	manifestData, err := bootstrapRegularFile("testdata/provider-policy-bootstrap-accepted-0f72.json", 128*1024)
	if err != nil || bootstrapHash(manifestData) != bootstrapInventorySHA {
		t.Fatal("accepted inventory source digest mismatch")
	}
	var frozen bootstrapInventory
	decoder := json.NewDecoder(bytes.NewReader(manifestData))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&frozen) != nil || frozen.Commit != bootstrapBase || frozen.Tree != bootstrapBaseTree || len(frozen.Files) != 217 {
		t.Fatal("accepted inventory is not the reviewed exact base")
	}
	bootstrapGit(t, root, env, "fetch", "--quiet", "--no-tags", "--depth=64", "https://github.com/"+bootstrapRepo+".git", bootstrapBase, binding.Candidate)
	if _, err := providerProofCommand(t, root, env, "/usr/bin/git", "merge-base", "--is-ancestor", bootstrapBase, binding.Candidate); err != nil {
		t.Fatal("candidate does not contain the actual accepted stage")
	}
	tmp := t.TempDir()
	trusted, candidate := filepath.Join(tmp, "trusted"), filepath.Join(tmp, "candidate")
	accepted := materializeBootstrapTree(t, root, bootstrapBase, trusted, env, &frozen)
	scanned := materializeBootstrapTree(t, root, binding.Candidate, candidate, env, nil)
	checkoutTree := strings.TrimSpace(string(bootstrapGit(t, root, env, "rev-parse", "HEAD^{tree}")))
	if err := verifyBootstrapCandidateTree(checkoutTree, scanned.Tree); err != nil {
		t.Fatal(err)
	}
	if err := verifyBootstrapAuthority(trusted, accepted); err != nil {
		t.Fatal(err)
	}
	goProgram := filepath.Join(sdk, "bin/go")
	goEnv, err := providerProofCommand(t, trusted, env, goProgram, "env", "-json", "GOVERSION", "GOROOT", "GOCACHE", "GOMODCACHE")
	if err != nil {
		t.Fatal("verified compiler environment failed")
	}
	var metadata map[string]string
	if json.Unmarshal(goEnv, &metadata) != nil || metadata["GOVERSION"] != "go1.27.2" || metadata["GOROOT"] != sdk || metadata["GOCACHE"] != "/home/runner/.cache/go-build" || metadata["GOMODCACHE"] != "/home/runner/go/pkg/mod" {
		t.Fatal("patched compiler or ordinary cache identity drift")
	}
	binary := filepath.Join(tmp, "accepted-checker")
	toolRoot := filepath.Join(trusted, ".github/workflows/policytool")
	if output, err := providerProofCommand(t, toolRoot, env, goProgram, "build", "-trimpath", "-o", binary, "./main.go"); err != nil {
		t.Fatalf("build exact accepted checker with patched compiler: %v\n%s", err, output)
	}
	built, err := buildinfo.ReadFile(binary)
	if err != nil || built.GoVersion != "go1.27.2" || len(built.Deps) != 2 {
		t.Fatal("accepted checker compiler/dependency build information drift")
	}
	modules := map[string]string{}
	for _, dependency := range built.Deps {
		if dependency.Replace != nil {
			t.Fatal("accepted checker dependency replacement")
		}
		modules[dependency.Path] = dependency.Version
	}
	if modules["gopkg.in/yaml.v3"] != "v3.0.1" || modules["mvdan.cc/sh/v3"] != "v3.13.1" {
		t.Fatal("accepted checker does not contain the exact pinned parser dependencies")
	}
	checkerBytes, err := bootstrapRegularFile(binary, 64*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	checkerSHA := bootstrapHash(checkerBytes)
	var outcomes []string
	scanAt := func(label, authorityRoot, scanRoot, diagnostic string) {
		t.Helper()
		output, err := providerProofCommand(t, trusted, []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}, binary, "--repo", authorityRoot, "--scan-root", scanRoot)
		if diagnostic == "" {
			if err != nil || !strings.Contains(string(output), "public workflow policy passed") {
				t.Fatalf("real accepted scanner rejected %s: %v\n%s", label, err, output)
			}
		} else {
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 1 || !strings.Contains(string(output), diagnostic) {
				t.Fatalf("real scanner failed negative %s: %v\n%s", label, err, output)
			}
		}
		outcomes = append(outcomes, label+":PASS")
	}
	scan := func(label, scanRoot, diagnostic string) { scanAt(label, trusted, scanRoot, diagnostic) }
	scan("complete-actual-candidate", candidate, "")
	unprepared := filepath.Join(tmp, "unprepared")
	materializeBootstrapTree(t, root, "075c2c291a1f933692d9949650fc41d20c0a63f9", unprepared, env, nil)
	scanAt("reject-actual-unprepared-predecessor", unprepared, candidate, "no trust group matches workflow")
	// Candidate source/modules/maps are data, never executable authority.
	for _, poison := range []struct{ Path, Content string }{
		{".github/workflows/policytool/main.go", "package main\nfunc main() { panic(\"candidate executed\") }\n"},
		{".github/workflows/policytool/go.mod", "module attacker.invalid/never-run\n\ngo 1.27.2\n"},
		{".github/public-workflow-executable-allowlist.json", "[]\n"},
	} {
		path := filepath.Join(candidate, poison.Path)
		original, err := os.ReadFile(path)
		if err != nil || os.WriteFile(path, []byte(poison.Content), 0o644) != nil {
			t.Fatal("cannot materialize candidate-authority poison control")
		}
		scan("inert-"+poison.Path, candidate, "")
		if os.WriteFile(path, original, 0o644) != nil {
			t.Fatal("cannot restore owned poison control")
		}
	}
	for _, control := range []struct{ Path, Diagnostic string }{
		{".github/workflows/scripts/test-public-workflow-policy.sh", "executable hash mismatch"},
		{".github/workflows/ci.yml", "no trust group matches workflow"},
	} {
		path := filepath.Join(candidate, control.Path)
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		changed := append(append([]byte(nil), original...), []byte("\n# unreviewed executable bytes\n")...)
		if strings.HasSuffix(control.Path, "ci.yml") {
			changed = bytes.ReplaceAll(original, []byte("go-version: \"1.27.2\""), []byte("go-version: \"1.27.3\""))
			if bytes.Equal(changed, original) {
				t.Fatal("compiler-context control did not mutate actual workflow")
			}
		}
		if os.WriteFile(path, changed, 0o644) != nil {
			t.Fatal("cannot materialize hostile workflow control")
		}
		scan("reject-"+control.Path, candidate, control.Diagnostic)
		if os.WriteFile(path, original, 0o644) != nil {
			t.Fatal("cannot restore hostile workflow control")
		}
	}
	mainPath := filepath.Join(trusted, ".github/workflows/policytool/main.go")
	mainSource, _ := os.ReadFile(mainPath)
	if os.WriteFile(mainPath, append(append([]byte(nil), mainSource...), '\n'), 0o644) != nil || verifyBootstrapAuthority(trusted, accepted) == nil {
		t.Fatal("tampered accepted source was not rejected")
	}
	if os.WriteFile(mainPath, mainSource, 0o644) != nil {
		t.Fatal("cannot restore accepted source")
	}
	outcomes = append(outcomes, "reject-tampered-accepted-source:PASS")
	modulePath := filepath.Join(toolRoot, "go.mod")
	moduleSource, moduleErr := os.ReadFile(modulePath)
	if moduleErr != nil || os.WriteFile(modulePath, append(append([]byte(nil), moduleSource...), '\n'), 0o644) != nil || verifyBootstrapAuthority(trusted, accepted) == nil {
		t.Fatal("tampered accepted module was not rejected")
	}
	if os.WriteFile(modulePath, moduleSource, 0o644) != nil {
		t.Fatal("cannot restore accepted module")
	}
	outcomes = append(outcomes, "reject-tampered-accepted-module:PASS")
	wrongTree := strings.Repeat("0", 40)
	if wrongTree == scanned.Tree {
		wrongTree = strings.Repeat("f", 40)
	}
	if verifyBootstrapCandidateTree(wrongTree, scanned.Tree) == nil || verifyBootstrapCandidateTree(checkoutTree, wrongTree) == nil {
		t.Fatal("actual checkout/candidate wrong-tree control was admitted")
	}
	outcomes = append(outcomes, "reject-wrong-actual-checkout-or-candidate-tree:PASS")
	extra := filepath.Join(toolRoot, "unexpected.go")
	if os.WriteFile(extra, []byte("package main\n"), 0o644) != nil || verifyBootstrapAuthority(trusted, accepted) == nil {
		t.Fatal("extra accepted policytool source was not rejected")
	}
	if os.Remove(extra) != nil {
		t.Fatal("cannot close owned negative source")
	}
	outcomes = append(outcomes, "reject-extra-accepted-policytool-source:PASS")
	if err := verifyBootstrapAuthority(trusted, accepted); err != nil {
		t.Fatal(err)
	}
	if err := verifyBootstrapAuthority(candidate, scanned); err != nil {
		t.Fatal("owned controls did not restore the full actual candidate")
	}
	scan("restored-complete-actual-candidate", candidate, "")
	finalSDK, err := bootstrapSDK(sdk, root, sdkInventory)
	finalSDKJSON, _ := json.Marshal(finalSDK)
	sdkJSON, _ := json.Marshal(sdkPins)
	finalBinary, binaryErr := bootstrapRegularFile(binary, 64*1024*1024)
	if err != nil || binaryErr != nil || !bytes.Equal(finalSDKJSON, sdkJSON) || bootstrapHash(finalBinary) != checkerSHA {
		t.Fatal("SDK/checker identity changed during actual proof")
	}
	sdkInventoryJSON, _ := json.Marshal(sdkInventory)
	bootstrapReceipt(t, map[string]any{
		"Schema": "provider-actual-accepted-policy-bootstrap/v1", "Binding": binding,
		"WorkflowRef": os.Getenv("GITHUB_WORKFLOW_REF"), "RunID": os.Getenv("GITHUB_RUN_ID"), "RunAttempt": os.Getenv("GITHUB_RUN_ATTEMPT"),
		"AcceptedCommit": bootstrapBase, "AcceptedTree": accepted.Tree, "AcceptedInventorySHA256": bootstrapInventorySHA,
		"Candidate": scanned, "CheckoutTree": checkoutTree, "SDKRoot": sdk, "SDKComponentsSHA256": sdkPins,
		"SDKArchiveURL": bootstrapSDKURL, "SDKArchiveSHA256": bootstrapSDKArchiveSHA, "SDKArchiveBytes": len(sdkArchive), "SDKInventorySHA256": bootstrapHash(sdkInventoryJSON), "SDKFiles": len(sdkInventory),
		"Compiler": built.GoVersion, "CompilerEnvironment": metadata, "CheckerSHA256": checkerSHA, "CheckerModules": modules,
		"Outcomes": outcomes, "Admission": "Supporting real candidate proof only. Exact operator source/SDK provenance and cutover permission are separate. Historical Go1264 declarations and GO-2026-5932 remain visible.",
	}, map[string]any{"Binding": binding, "CandidateTree": scanned.Tree, "SDKArchiveSHA256": bootstrapSDKArchiveSHA, "SDKInventorySHA256": bootstrapHash(sdkInventoryJSON), "CheckerSHA256": checkerSHA, "Outcomes": outcomes})
}

func TestBootstrapRejectsUnsafeTreeEntries(t *testing.T) {
	for _, record := range []string{
		"120000 blob " + bootstrapBase + "\tlink\x00", "160000 commit " + bootstrapBase + "\tmodule\x00",
		"100644 blob " + bootstrapBase + "\t../escape\x00", "100644 blob " + bootstrapBase + "\t.git/config\x00",
		"100644 blob " + bootstrapBase + "\t.\x00", "100644 blob " + bootstrapBase + "\tdata/.git/config\x00",
		"100644 blob " + bootstrapBase + "\tduplicate\x00" + "100644 blob " + bootstrapBase + "\tduplicate\x00",
	} {
		if _, err := bootstrapTreeEntries([]byte(record)); err == nil {
			t.Fatal("unsafe actual Git inventory was accepted")
		}
	}
}

func TestBootstrapRejectsUnknownCompilerBeforeInvocation(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "was-executed")
	if os.Mkdir(filepath.Join(root, "bin"), 0o700) != nil || os.WriteFile(filepath.Join(root, "bin/go"), []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o700) != nil {
		t.Fatal("cannot materialize fake compiler")
	}
	if _, err := bootstrapSDK(root, filepath.Join(root, "source"), nil); err == nil {
		t.Fatal("unknown compiler was admitted")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unknown compiler was invoked before authentication")
	}
	if _, err := bootstrapSDK("/opt/hostedtoolcache/go/1.26.4/x64", root, nil); err == nil {
		t.Fatal("old compiler path was admitted")
	}
}

func TestBootstrapRejectsUnadmittedEvents(t *testing.T) {
	helper := strings.Repeat("a", 40)
	candidate := strings.Repeat("b", 40)
	event := bootstrapEvent{Ref: bootstrapProofRef, After: helper}
	event.Repository.FullName = bootstrapRepo
	workflow := bootstrapRepo + "/.github/workflows/ci.yml@" + bootstrapProofRef
	if _, err := bindBootstrapEvent("push", workflow, helper, candidate, candidate, event); err != nil {
		t.Fatal(err)
	}
	collapsed := event
	collapsed.After = candidate
	if _, err := bindBootstrapEvent("push", workflow, candidate, candidate, candidate, collapsed); err == nil {
		t.Fatal("helper and candidate roles were collapsed")
	}
	for _, change := range []func(*bootstrapEvent){
		func(e *bootstrapEvent) { e.Repository.FullName = "attacker/repo" },
		func(e *bootstrapEvent) { e.Ref = "refs/heads/other" },
		func(e *bootstrapEvent) { e.After = "" },
		func(e *bootstrapEvent) { e.Ref = "refs/heads/main"; e.Before = strings.Repeat("0", 40) },
	} {
		copy := event
		change(&copy)
		if _, err := bindBootstrapEvent("push", workflow, helper, candidate, candidate, copy); err == nil {
			t.Fatal("unadmitted real event metadata was accepted")
		}
	}
	for _, value := range []string{"", strings.Repeat("f", 40)} {
		if _, err := bindBootstrapEvent("push", workflow, helper, candidate, value, event); err == nil {
			t.Fatal("missing/mismatched candidate checkout was accepted")
		}
	}
	if _, err := bindBootstrapEvent("pull_request", workflow, bootstrapBase, bootstrapBase, "", event); err == nil {
		t.Fatal("missing actual PR201/base/head metadata was accepted")
	}
	for _, value := range []string{"", "0", "-1", "+1", "01", "1.0", "1\n", strings.Repeat("1", 21)} {
		if bootstrapRunIdentity(value, "1") == nil || bootstrapRunIdentity("1", value) == nil {
			t.Fatal("invalid run identity was admitted")
		}
	}
	if err := bootstrapRunIdentity("38006288192", "1"); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapAdoptionEventBindings(t *testing.T) {
	candidate, merge := strings.Repeat("b", 40), strings.Repeat("c", 40)
	pr := bootstrapEvent{Number: 201}
	pr.Repository.FullName = bootstrapRepo
	pr.PullRequest.Head.SHA, pr.PullRequest.Head.Repo.FullName, pr.PullRequest.Base.SHA = candidate, bootstrapRepo, bootstrapBase
	workflow := bootstrapRepo + "/.github/workflows/ci.yml@refs/pull/201/merge"
	if b, err := bindBootstrapEvent("pull_request", workflow, merge, merge, "", pr); err != nil || b.Candidate != candidate || b.Merge != merge {
		t.Fatal("actual PR201 roles were not admitted")
	}
	for _, mutate := range []func(*bootstrapEvent){
		func(e *bootstrapEvent) { e.Number = 203 }, func(e *bootstrapEvent) { e.PullRequest.Base.SHA = candidate },
		func(e *bootstrapEvent) { e.PullRequest.Head.SHA = "" }, func(e *bootstrapEvent) { e.PullRequest.Head.Repo.FullName = "attacker/repo" },
	} {
		changed := pr
		mutate(&changed)
		if _, err := bindBootstrapEvent("pull_request", workflow, merge, merge, "", changed); err == nil {
			t.Fatal("changed PR201 metadata was admitted")
		}
	}
	if _, err := bindBootstrapEvent("pull_request", workflow+"wrong", merge, merge, "", pr); err == nil {
		t.Fatal("wrong PR workflow was admitted")
	}
	main := bootstrapEvent{Ref: "refs/heads/main", Before: bootstrapBase, After: candidate}
	main.Repository.FullName = bootstrapRepo
	workflow = bootstrapRepo + "/.github/workflows/ci.yml@refs/heads/main"
	if b, err := bindBootstrapEvent("push", workflow, candidate, candidate, "", main); err != nil || b.Mode != "first-adoption-main" {
		t.Fatal("finite first main roles were not admitted")
	}
	for _, mutate := range []func(*bootstrapEvent){
		func(e *bootstrapEvent) { e.Before = merge }, func(e *bootstrapEvent) { e.After = merge },
		func(e *bootstrapEvent) { e.Ref = "refs/heads/other" },
	} {
		changed := main
		mutate(&changed)
		if _, err := bindBootstrapEvent("push", workflow, candidate, candidate, "", changed); err == nil {
			t.Fatal("changed first main metadata was admitted")
		}
	}
	if _, err := bindBootstrapEvent("push", workflow+"wrong", candidate, candidate, "", main); err == nil {
		t.Fatal("wrong first main workflow was admitted")
	}
}
