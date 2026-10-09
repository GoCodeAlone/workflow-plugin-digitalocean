package digitalocean_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/json"
	"fmt"
	"go/version"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type providerProofPin struct {
	SHA256 string
	Mode   uint32
}

type providerTransitionManifest struct {
	BaseCommit    string
	OldContext    string
	FutureContext string
	OldWrapper    string
	FutureWrapper string
	Files         map[string]providerProofPin
}

type providerProofBuffer struct {
	data     bytes.Buffer
	limit    int
	overflow bool
}

func (b *providerProofBuffer) Write(data []byte) (int, error) {
	count := len(data)
	if len(data) > b.limit-b.data.Len() {
		data = data[:b.limit-b.data.Len()]
		b.overflow = true
	}
	_, _ = b.data.Write(data)
	return count, nil
}

func providerProofEnv(targetOS, targetArch string) []string {
	overrides := map[string]string{
		"GOWORK": "off", "GOFLAGS": "-mod=readonly", "GOTOOLCHAIN": "local",
		"CGO_ENABLED": "0", "GOOS": targetOS, "GOARCH": targetArch,
	}
	var result []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[key]; !replaced {
			result = append(result, entry)
		}
	}
	for key, value := range overrides {
		result = append(result, key+"="+value)
	}
	return result
}

func providerProofCommand(t *testing.T, dir string, env []string, program string, args ...string) ([]byte, error) {
	t.Helper()
	budget := 3 * time.Minute
	if deadline, ok := t.Deadline(); ok {
		remaining := time.Until(deadline) - 15*time.Second
		if remaining <= 0 {
			t.Fatal("proof command has no test-deadline cleanup reserve")
		}
		if remaining < budget {
			budget = remaining
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir, cmd.Env, cmd.WaitDelay = dir, env, 5*time.Second
	output := providerProofBuffer{limit: 32 * 1024 * 1024}
	diagnostics := providerProofBuffer{limit: 256 * 1024}
	cmd.Stdout, cmd.Stderr = &output, &diagnostics
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("bounded proof command %s %v: %v", program, args, ctx.Err())
	}
	if output.overflow || diagnostics.overflow {
		t.Fatalf("bounded proof command %s %v exceeded output limit", program, args)
	}
	if err != nil {
		_, _ = output.data.Write(diagnostics.data.Bytes())
	}
	return output.data.Bytes(), err
}

// Successful package-list tests hide t.Log. Keep normalized public dependency
// evidence in the runner's existing summary file, without changing workflows.
func providerProofSummary(t *testing.T, scope string, value any) {
	t.Helper()
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return
	}
	path, temp := os.Getenv("GITHUB_STEP_SUMMARY"), os.Getenv("RUNNER_TEMP")
	if path == "" || temp == "" || !filepath.IsAbs(path) || !filepath.IsAbs(temp) {
		t.Fatal("CI proof needs the existing runner step-summary file")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("CI step-summary path must be an existing regular file")
	}
	resolvedPath, pathErr := filepath.EvalSymlinks(path)
	resolvedTemp, tempErr := filepath.EvalSymlinks(temp)
	relative, relErr := filepath.Rel(resolvedTemp, resolvedPath)
	if pathErr != nil || tempErr != nil || relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatal("CI step-summary path escapes the runner temporary directory")
	}
	// Keep the complete typed evidence compact. Pretty-printing four package
	// graphs can exceed the runner summary budget without adding information.
	data, err := json.Marshal(value)
	if err != nil || len(data) > 512*1024 {
		t.Fatal("cannot encode bounded CI proof summary")
	}
	var record strings.Builder
	record.WriteString("\n### Provider " + scope + " proof\n\n")
	for _, line := range strings.Split(string(data), "\n") {
		record.WriteString("    " + line + "\n")
	}
	text := record.String()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal("cannot append existing CI proof summary")
	}
	opened, openedErr := f.Stat()
	current, currentErr := os.Lstat(path)
	if openedErr != nil || currentErr != nil || !opened.Mode().IsRegular() || !current.Mode().IsRegular() ||
		!os.SameFile(info, opened) || !os.SameFile(opened, current) {
		_ = f.Close()
		t.Fatal("CI step-summary file identity changed before append")
	}
	if opened.Size()+int64(len(text)) > 512*1024 {
		_ = f.Close()
		t.Fatal("cumulative CI proof summary exceeds 512KiB")
	}
	written, writeErr := io.WriteString(f, text)
	syncErr := f.Sync()
	final, finalErr := f.Stat()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || finalErr != nil || written != len(text) ||
		final.Size() != opened.Size()+int64(written) {
		t.Fatal("cannot persist CI proof summary")
	}
}

func TestNativePolicyToolGo127Transition(t *testing.T) {
	fixtures := filepath.Join("testdata", "policytool-go127-transition")
	data, err := os.ReadFile(filepath.Join(fixtures, "manifest.json"))
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(data)) != "1bf6d910d7c7b88205c473acf517b4976970efe76fb3338b75a2231da23db2c5" {
		t.Fatal("transition manifest is not the independently frozen exact packet")
	}
	var manifest providerTransitionManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 32 || manifest.FutureContext != "587ecdb66f8ea81d24fa090aadec0adde17fef877cbe7ed5aea9b0208f4d15e1" ||
		manifest.FutureWrapper != "3fbf786aad1448c55a934f3fc3ed22644d5e83fef2a1a299a09b48b803ae7d37" {
		t.Fatal("transition fixture is not the reviewed exact packet")
	}
	for path, pin := range manifest.Files {
		if filepath.IsAbs(path) || filepath.ToSlash(filepath.Clean(path)) != path || strings.HasPrefix(path, "../") ||
			(pin.Mode != 0o644 && pin.Mode != 0o755) {
			t.Fatal("fixture path or mode is not canonical")
		}
		source := filepath.Join(fixtures, filepath.FromSlash(path))
		info, statErr := os.Lstat(source)
		if statErr != nil || !info.Mode().IsRegular() {
			t.Fatal("frozen fixture must be a regular file")
		}
		content, err := os.ReadFile(source)
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(content)) != pin.SHA256 {
			t.Fatalf("frozen fixture hash mismatch: %s", path)
		}
	}
	tmp := t.TempDir()
	materialize := func(name string, phases ...string) string {
		t.Helper()
		root := filepath.Join(tmp, name)
		for _, phase := range phases {
			prefix := phase + "/"
			for path, pin := range manifest.Files {
				if !strings.HasPrefix(path, prefix) {
					continue
				}
				relative := strings.TrimPrefix(path, prefix)
				dest := filepath.Join(root, filepath.FromSlash(relative))
				if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
					t.Fatal(err)
				}
				content, err := os.ReadFile(filepath.Join(fixtures, filepath.FromSlash(path)))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dest, content, os.FileMode(pin.Mode)); err != nil {
					t.Fatal(err)
				}
			}
		}
		return root
	}
	roots := map[string]string{
		"baseline": materialize("baseline", "baseline"),
		"stage":    materialize("stage", "baseline", "stage"),
		"adopt":    materialize("adopt", "baseline", "stage", "adopt"),
		"promote":  materialize("promote", "baseline", "stage", "adopt", "promote"),
	}
	tool := filepath.Join(".github", "workflows", "policytool")
	currentMain, err := os.ReadFile(filepath.Join(tool, "main.go"))
	pinnedMain := manifest.Files["baseline/.github/workflows/policytool/main.go"].SHA256
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(currentMain)) != pinnedMain {
		t.Fatal("native proof must use the reviewed actual checker source")
	}
	binary := filepath.Join(tmp, "policytool")
	env := providerProofEnv(runtime.GOOS, runtime.GOARCH)
	if output, err := providerProofCommand(t, tool, env, "go", "build", "-o", binary, "./main.go"); err != nil {
		t.Fatalf("build actual checker: %v\n%s", err, output)
	}
	built, err := buildinfo.ReadFile(binary)
	if err != nil || built.GoVersion != runtime.Version() || version.Compare(built.GoVersion, "go1.27.2") < 0 {
		t.Fatal("actual checker compiler differs from the patched test compiler")
	}
	var outcomes []string
	check := func(name, trusted, candidate, diagnostic string) {
		t.Helper()
		output, err := providerProofCommand(t, ".", env, binary, "--repo", trusted, "--scan-root", candidate)
		if diagnostic == "" {
			if err != nil {
				t.Fatalf("%s: actual checker rejected: %v\n%s", name, err, output)
			}
		} else {
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 1 || !strings.Contains(string(output), diagnostic) {
				t.Fatalf("%s: expected policy exit1 containing %q, got %v\n%s", name, diagnostic, err, output)
			}
		}
		outcomes = append(outcomes, name+":PASS")
	}
	for _, name := range []string{"baseline", "stage", "adopt", "promote"} {
		check(name+"-self", roots[name], roots[name], "")
	}
	check("baseline-stage", roots["baseline"], roots["stage"], "")
	check("stage-adopt", roots["stage"], roots["adopt"], "")
	check("adopt-promote", roots["adopt"], roots["promote"], "")
	check("baseline-unprepared-adopt", roots["baseline"], roots["adopt"], "no trust group matches workflow")
	check("baseline-unprepared-promote", roots["baseline"], roots["promote"], "no trust group matches workflow")
	check("promoted-obsolete-source", roots["promote"], roots["baseline"], "no trust group matches workflow")

	for _, script := range []string{"check-public-workflow-policy.sh", "test-public-workflow-policy.sh"} {
		name := "tampered-" + script
		candidate := materialize(name, "baseline", "stage", "adopt")
		path := filepath.Join(candidate, ".github", "workflows", "scripts", script)
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := io.WriteString(f, "\n# unexpected executable bytes\n")
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal("cannot materialize negative executable fixture")
		}
		check(name, roots["stage"], candidate, "executable hash mismatch")
	}
	contextCandidate := materialize("tampered-context", "baseline", "stage", "adopt")
	ciPath := filepath.Join(contextCandidate, ".github", "workflows", "ci.yml")
	ci, err := os.ReadFile(ciPath)
	if err != nil || bytes.Count(ci, []byte("go-version: \"1.27.2\"")) != 2 {
		t.Fatal("future CI fixture must contain both reviewed compiler inputs")
	}
	if err := os.WriteFile(ciPath, bytes.ReplaceAll(ci, []byte("go-version: \"1.27.2\""), []byte("go-version: \"1.27.3\"")), 0o644); err != nil {
		t.Fatal(err)
	}
	check("tampered-compiler-context", roots["stage"], contextCandidate, "no trust group matches workflow")
	manifestCandidate := materialize("tampered-candidate-manifest", "baseline", "stage", "adopt")
	manifestPath := filepath.Join(manifestCandidate, ".github", "public-workflow-executable-allowlist.json")
	if err := os.WriteFile(manifestPath, []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	check("candidate-manifests-inert-to-trusted-scan", roots["stage"], manifestCandidate, "")
	check("candidate-manifest-self-scan-rejects", manifestCandidate, manifestCandidate, "unallowlisted executable script")
	moduleCandidate := materialize("tampered-candidate-module", "baseline", "stage", "adopt")
	for name, content := range map[string]string{
		"go.mod":  "module attacker.invalid/never-executed\n\ngo 1.27.2\n",
		"main.go": "package main\nfunc main() { panic(\"candidate-source-executed\") }\n",
	} {
		path := filepath.Join(moduleCandidate, ".github", "workflows", "policytool", name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	check("candidate-module-and-main-inert-to-trusted-native-scan", roots["stage"], moduleCandidate, "")
	// Also exercise the unchanged confinement/integrity wrapper, including its
	// future go.mod pin, with ordinary caches and the patched CI compiler.
	for _, pair := range [][2]string{{"baseline", "stage"}, {"stage", "adopt"}, {"adopt", "promote"}, {"adopt", "adopt"}} {
		trusted, candidate := roots[pair[0]], roots[pair[1]]
		wrapper := filepath.Join(trusted, ".github", "workflows", "scripts", "check-public-workflow-policy.sh")
		output, err := providerProofCommand(t, trusted, env, "bash", wrapper, "--scan-root", candidate)
		if err != nil {
			t.Fatalf("actual wrapper %s-%s rejected: %v\n%s", pair[0], pair[1], err, output)
		}
		outcomes = append(outcomes, "actual-wrapper-"+pair[0]+"-"+pair[1]+":PASS")
	}
	trusted := roots["stage"]
	wrapper := filepath.Join(trusted, ".github", "workflows", "scripts", "check-public-workflow-policy.sh")
	if output, err := providerProofCommand(t, trusted, env, "bash", wrapper, "--scan-root", moduleCandidate); err != nil {
		t.Fatalf("candidate module altered trusted wrapper execution: %v\n%s", err, output)
	}
	outcomes = append(outcomes, "candidate-module-inert-to-actual-trusted-wrapper:PASS")
	// The scanner does not enforce publication ordering or read candidate trust
	// manifests. Do not invent a skip/reorder rejection it does not implement.
	check("stage-authority-can-scan-same-adopted-source-with-promoted-candidate-maps", roots["stage"], roots["promote"], "")
	providerProofSummary(t, "native transition", map[string]any{
		"BaseCommit": manifest.BaseCommit, "FutureContext": manifest.FutureContext,
		"FutureWrapper": manifest.FutureWrapper, "CheckerSourceSHA256": pinnedMain,
		"CheckerGoVersion": built.GoVersion, "Outcomes": outcomes,
		"Limit": "Parser/hash/two-root proof only. Actual phase order, accepted predecessor and old trusted public-policy compiler remain separate admission gates.",
	})
}

type providerResolvedModule struct {
	Path, Version, GoVersion string
	Replace                  *providerResolvedModule
}

type providerResolvedPackage struct {
	ImportPath string
	Module     *providerResolvedModule
	Incomplete bool
	Error      *struct{ Err string }
	DepsErrors []struct{ Err string }
}

func TestReleaseGraphsExcludeUnsupportedOpenPGP(t *testing.T) {
	env := providerProofEnv(runtime.GOOS, runtime.GOARCH)
	modulesJSON, err := providerProofCommand(t, ".", env, "go", "list", "-m", "-json", "all")
	if err != nil {
		t.Fatalf("resolve complete module graph: %v\n%s", err, modulesJSON)
	}
	var modules []providerResolvedModule
	decoder := json.NewDecoder(bytes.NewReader(modulesJSON))
	for {
		var module providerResolvedModule
		err := decoder.Decode(&module)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal("invalid resolved module evidence")
		}
		if module.Replace != nil {
			t.Fatalf("release graph has unexpected replacement: %s", module.Path)
		}
		modules = append(modules, module)
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].Path < modules[j].Path })
	moduleIDs := make(map[string]int)
	cryptoVersion := ""
	for id, module := range modules {
		moduleIDs[module.Path] = id
		if module.Path == "golang.org/x/crypto" {
			cryptoVersion = module.Version
		}
	}
	if cryptoVersion != "v0.57.0" {
		t.Fatalf("module-level GO-2026-5932 applicability needs review at x/crypto %s", cryptoVersion)
	}
	config, err := os.ReadFile(".goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var release struct {
		Builds []struct {
			Main         string
			Env          []string
			Goos, Goarch []string
			Tags         []string
		}
	}
	if err := yaml.Unmarshal(config, &release); err != nil || len(release.Builds) != 1 {
		t.Fatal("cannot resolve actual release build targets")
	}
	build := release.Builds[0]
	if build.Main != "./cmd/plugin" || len(build.Env) != 1 || build.Env[0] != "CGO_ENABLED=0" || len(build.Tags) != 0 || len(build.Goos) != 2 || len(build.Goarch) != 2 {
		t.Fatal("actual release target or environment changes need security-proof review")
	}
	var targets []map[string]any
	for _, targetOS := range build.Goos {
		for _, targetArch := range build.Goarch {
			targetEnv := providerProofEnv(targetOS, targetArch)
			packageJSON, err := providerProofCommand(t, ".", targetEnv, "go", "list", "-deps", "-json", "./cmd/plugin")
			if err != nil {
				t.Fatalf("resolve %s/%s release dependencies: %v\n%s", targetOS, targetArch, err, packageJSON)
			}
			var packages []providerResolvedPackage
			decoder := json.NewDecoder(bytes.NewReader(packageJSON))
			for {
				var pkg providerResolvedPackage
				err := decoder.Decode(&pkg)
				if err == io.EOF {
					break
				}
				if err != nil || pkg.ImportPath == "" || pkg.Incomplete || pkg.Error != nil || len(pkg.DepsErrors) != 0 {
					t.Fatal("release dependency proof is incomplete or contains a package error")
				}
				if pkg.ImportPath == "golang.org/x/crypto/openpgp" || strings.HasPrefix(pkg.ImportPath, "golang.org/x/crypto/openpgp/") {
					t.Fatalf("GO-2026-5932 affected package enters %s/%s release: %s", targetOS, targetArch, pkg.ImportPath)
				}
				packages = append(packages, pkg)
			}
			if len(packages) == 0 {
				t.Fatal("release dependency graph is empty")
			}
			sort.Slice(packages, func(i, j int) bool { return packages[i].ImportPath < packages[j].ImportPath })
			bindings := make([][2]any, 0, len(packages))
			for _, pkg := range packages {
				id := -1
				if pkg.Module != nil {
					selected, ok := moduleIDs[pkg.Module.Path]
					if !ok || pkg.Module.Replace != nil || pkg.Module.Version != modules[selected].Version {
						t.Fatal("package module differs from the complete selected module graph")
					}
					id = selected
				}
				bindings = append(bindings, [2]any{pkg.ImportPath, id})
			}
			binary := filepath.Join(t.TempDir(), "plugin-"+targetOS+"-"+targetArch)
			if output, err := providerProofCommand(t, ".", targetEnv, "go", "build", "-o", binary, "./cmd/plugin"); err != nil {
				t.Fatalf("build %s/%s release graph: %v\n%s", targetOS, targetArch, err, output)
			}
			built, err := buildinfo.ReadFile(binary)
			if err != nil || built.GoVersion != runtime.Version() || version.Compare(built.GoVersion, "go1.27.2") < 0 {
				t.Fatal("built plugin compiler differs from patched root test compiler")
			}
			settings := make(map[string]string)
			for _, setting := range built.Settings {
				switch setting.Key {
				case "GOOS", "GOARCH", "CGO_ENABLED", "vcs.revision", "vcs.modified":
					settings[setting.Key] = setting.Value
				}
			}
			if settings["GOOS"] != targetOS || settings["GOARCH"] != targetArch || settings["CGO_ENABLED"] != "0" {
				t.Fatal("built plugin does not match actual GoReleaser target settings")
			}
			for _, module := range built.Deps {
				id, ok := moduleIDs[module.Path]
				if !ok || module.Replace != nil || module.Version != modules[id].Version {
					t.Fatal("built binary module differs from the selected module graph")
				}
			}
			normalized, err := json.Marshal(bindings)
			if err != nil {
				t.Fatal(err)
			}
			targets = append(targets, map[string]any{
				"GOOS": targetOS, "GOARCH": targetArch, "GoVersion": built.GoVersion,
				"BuildSettings": settings, "PackageBindings": bindings,
				"PackageBindingFormat": "[import path, ResolvedModules index]; -1 means standard library",
				"PackageGraphSHA256":   fmt.Sprintf("%x", sha256.Sum256(normalized)),
				"BinaryModules":        built.Deps,
			})
		}
	}
	providerProofSummary(t, "release dependency", map[string]any{
		"Advisory": "GO-2026-5932", "ModuleFindingRetained": "golang.org/x/crypto@v0.57.0",
		"ReleaseConfigSHA256": fmt.Sprintf("%x", sha256.Sum256(config)),
		"ResolvedModules":     modules, "Targets": targets,
		"Limit": "Affected OpenPGP packages absent from these exact plugin release graphs. Other tooling, test graphs, policy compiler and new vulnerability data require separate analysis.",
	})
}
