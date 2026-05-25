package internal

// plugin_test.go — release-archive consistency check.
//
// Verifies that plugin.json's `downloads` URLs line up with the
// archive shape GoReleaser actually publishes (one tar.gz per
// goos/goarch with plugin.json + plugin.contracts.json bundled).
// Pre-strict-contracts-cutover this file also covered Manifest /
// ContractRegistry / CreateModule round-trips on doPlugin; that
// surface was deleted in Task 9 (the typed pb.IaCProvider*Server
// surface in iacserver.go replaces it). The release-archive test
// is unrelated to dispatch and survives unchanged.

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestPluginGoReleaserArchiveShape verifies the GoReleaser archive
// structure (one tar.gz per goos/goarch with plugin.json +
// plugin.contracts.json bundled). The pre-758 cross-check that
// committed plugin.json.downloads URLs equal {{ .ProjectName }}-{{ .Os }}-{{ .Arch }}
// .tar.gz under v$Version was retired in workflow#758 — the committed
// .version field is a "0.0.0" sentinel and the goreleaser before-hook
// rewrites .release/plugin.json with the actual tag; the tarball-shipped
// URLs are what matters, and `wfctl plugin validate-contract
// --release-dir .release --for-publish --tag <vX.Y.Z>` enforces that
// invariant at release time (workflow#758 Layer 1).
func TestPluginGoReleaserArchiveShape(t *testing.T) {
	repoRoot := testRepoRoot(t)
	var releaseCfg struct {
		Builds []struct {
			ID     string   `yaml:"id"`
			Goos   []string `yaml:"goos"`
			Goarch []string `yaml:"goarch"`
		} `yaml:"builds"`
		Archives []struct {
			ID           string            `yaml:"id"`
			IDs          []string          `yaml:"ids"`
			NameTemplate string            `yaml:"name_template"`
			Files        []archiveFileSpec `yaml:"files"`
		} `yaml:"archives"`
	}
	releaseData, err := os.ReadFile(filepath.Join(repoRoot, ".goreleaser.yaml"))
	if err != nil {
		t.Fatalf("read .goreleaser.yaml: %v", err)
	}
	if err := yaml.Unmarshal(releaseData, &releaseCfg); err != nil {
		t.Fatalf("parse .goreleaser.yaml: %v", err)
	}
	if len(releaseCfg.Builds) != 1 {
		t.Fatalf("expected 1 GoReleaser build, got %d", len(releaseCfg.Builds))
	}
	if len(releaseCfg.Archives) != 1 {
		t.Fatalf("expected 1 GoReleaser archive, got %d", len(releaseCfg.Archives))
	}
	build := releaseCfg.Builds[0]
	archive := releaseCfg.Archives[0]
	if archive.NameTemplate != "{{ .ProjectName }}-{{ .Os }}-{{ .Arch }}" {
		t.Fatalf("unsupported archive name_template %q", archive.NameTemplate)
	}
	if len(archive.IDs) != 1 || archive.IDs[0] != build.ID {
		t.Fatalf("archive ids = %v, want [%s]", archive.IDs, build.ID)
	}
	if !containsArchiveFile(archive.Files, ".release/plugin.json", "plugin.json") {
		t.Fatalf("archive files = %v, want plugin.json", archive.Files)
	}
	if !containsArchiveFile(archive.Files, "plugin.contracts.json", "plugin.contracts.json") {
		t.Fatalf("archive files = %v, want plugin.contracts.json", archive.Files)
	}

	manifestData, err := os.ReadFile(filepath.Join(repoRoot, "plugin.json"))
	if err != nil {
		t.Fatalf("read plugin.json: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "plugin.tar.gz")
	if err := writeTestArchive(repoRoot, archivePath, archive.Files, map[string][]byte{
		".release/plugin.json": manifestData,
	}); err != nil {
		t.Fatalf("write test archive: %v", err)
	}
	if !tarGzContains(archivePath, "plugin.contracts.json") {
		t.Fatal("test release archive missing plugin.contracts.json")
	}
}

func TestGoReleaserReleaseManifestValidationDirectory(t *testing.T) {
	repoRoot := testRepoRoot(t)
	releaseData, err := os.ReadFile(filepath.Join(repoRoot, ".goreleaser.yaml"))
	if err != nil {
		t.Fatalf("read .goreleaser.yaml: %v", err)
	}
	text := string(releaseData)
	for _, want := range []string{
		"rm -rf .release",
		"cp plugin.json .release/plugin.json",
		"cp plugin.contracts.json .release/plugin.contracts.json",
		"--file .release/plugin.json --strict-contracts",
		"WFCTL_VERSION=$(GOWORK=off go list -m github.com/GoCodeAlone/workflow",
		"cp plugin.json cmd/plugin/plugin.json",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf(".goreleaser.yaml release validation hook missing %q", want)
		}
	}

	dir := filepath.Join(t.TempDir(), ".release")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir release dir: %v", err)
	}
	for _, name := range []string{"plugin.json", "plugin.contracts.json"} {
		data, err := os.ReadFile(filepath.Join(repoRoot, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatalf("write release %s: %v", name, err)
		}
	}
	for _, name := range []string{"plugin.json", "plugin.contracts.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("release validation directory missing %s: %v", name, err)
		}
	}

	if strings.Contains(text, "dist/release") {
		t.Fatal(".goreleaser.yaml must not generate manifests under dist; GoReleaser requires dist to remain empty before build")
	}
}

func TestPluginBinaryEmbedsManifest(t *testing.T) {
	repoRoot := testRepoRoot(t)
	rootManifest, err := os.ReadFile(filepath.Join(repoRoot, "plugin.json"))
	if err != nil {
		t.Fatalf("read root plugin.json: %v", err)
	}
	cmdManifest, err := os.ReadFile(filepath.Join(repoRoot, "cmd", "plugin", "plugin.json"))
	if err != nil {
		t.Fatalf("read cmd/plugin/plugin.json: %v", err)
	}
	if string(cmdManifest) != string(rootManifest) {
		t.Fatal("cmd/plugin/plugin.json must match root plugin.json so go:embed exposes release truth")
	}
	mainData, err := os.ReadFile(filepath.Join(repoRoot, "cmd", "plugin", "main.go"))
	if err != nil {
		t.Fatalf("read cmd/plugin/main.go: %v", err)
	}
	mainText := string(mainData)
	for _, want := range []string{
		"//go:embed plugin.json",
		"ManifestProvider: sdk.MustEmbedManifest(pluginJSON)",
	} {
		if !strings.Contains(mainText, want) {
			t.Fatalf("cmd/plugin/main.go missing %q", want)
		}
	}
}

type archiveFileSpec struct {
	Src string
	Dst string
}

func (s *archiveFileSpec) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		s.Src = value.Value
		s.Dst = value.Value
		return nil
	case yaml.MappingNode:
		var raw struct {
			Src string `yaml:"src"`
			Dst string `yaml:"dst"`
		}
		if err := value.Decode(&raw); err != nil {
			return err
		}
		s.Src = raw.Src
		s.Dst = raw.Dst
		if s.Dst == "" {
			s.Dst = raw.Src
		}
		return nil
	default:
		return fmt.Errorf("unsupported archive file spec kind %v", value.Kind)
	}
}

func containsArchiveFile(values []archiveFileSpec, src, dst string) bool {
	for _, value := range values {
		if value.Src == src && value.Dst == dst {
			return true
		}
	}
	return false
}

func writeTestArchive(repoRoot, archivePath string, files []archiveFileSpec, overrides map[string][]byte) error {
	out, err := os.Create(archivePath)
	if err != nil {
		return err
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	for _, file := range files {
		data, ok := overrides[file.Src]
		if !ok {
			var err error
			data, err = os.ReadFile(filepath.Join(repoRoot, file.Src))
			if err != nil {
				return err
			}
		}
		if err := tw.WriteHeader(&tar.Header{Name: file.Dst, Mode: 0o644, Size: int64(len(data))}); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}
	return nil
}

func tarGzContains(archivePath, want string) bool {
	file, err := os.Open(archivePath)
	if err != nil {
		return false
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return false
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return false
		}
		if err != nil {
			return false
		}
		if header.Name == want {
			return true
		}
	}
}

func testRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(filepath.Dir(file))
}
