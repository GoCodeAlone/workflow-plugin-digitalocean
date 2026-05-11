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
	"encoding/json"
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

func TestPluginDownloadsMatchGoReleaserArchives(t *testing.T) {
	repoRoot := testRepoRoot(t)
	var manifest struct {
		Name      string `json:"name"`
		Version   string `json:"version"`
		Downloads []struct {
			OS   string `json:"os"`
			Arch string `json:"arch"`
			URL  string `json:"url"`
		} `json:"downloads"`
	}
	manifestData, err := os.ReadFile(filepath.Join(repoRoot, "plugin.json"))
	if err != nil {
		t.Fatalf("read plugin.json: %v", err)
	}
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("parse plugin.json: %v", err)
	}

	var releaseCfg struct {
		Builds []struct {
			ID     string   `yaml:"id"`
			Goos   []string `yaml:"goos"`
			Goarch []string `yaml:"goarch"`
		} `yaml:"builds"`
		Archives []struct {
			ID           string   `yaml:"id"`
			IDs          []string `yaml:"ids"`
			NameTemplate string   `yaml:"name_template"`
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
		t.Fatalf("unsupported archive name_template %q; update download manifest generation/test", archive.NameTemplate)
	}
	if len(archive.IDs) != 1 || archive.IDs[0] != build.ID {
		t.Fatalf("archive ids = %v, want [%s]", archive.IDs, build.ID)
	}
	if !containsArchiveFile(archive.Files, "dist/release/plugin.json", "plugin.json") {
		t.Fatalf("archive files = %v, want plugin.json", archive.Files)
	}
	if !containsArchiveFile(archive.Files, "plugin.contracts.json", "plugin.contracts.json") {
		t.Fatalf("archive files = %v, want plugin.contracts.json", archive.Files)
	}

	want := make(map[string]string)
	for _, goos := range build.Goos {
		for _, goarch := range build.Goarch {
			key := goos + "/" + goarch
			want[key] = fmt.Sprintf(
				"https://github.com/GoCodeAlone/%s/releases/download/v%s/%s-%s-%s.tar.gz",
				manifest.Name,
				manifest.Version,
				manifest.Name,
				goos,
				goarch,
			)
		}
	}
	got := make(map[string]string, len(manifest.Downloads))
	for _, dl := range manifest.Downloads {
		got[dl.OS+"/"+dl.Arch] = dl.URL
	}
	if len(got) != len(want) {
		t.Fatalf("download count = %d, want %d (%v)", len(got), len(want), want)
	}
	for key, wantURL := range want {
		if gotURL := got[key]; gotURL != wantURL {
			t.Errorf("download %s = %q, want %q", key, gotURL, wantURL)
		}
	}

	archivePath := filepath.Join(t.TempDir(), "plugin.tar.gz")
	if err := writeTestArchive(repoRoot, archivePath, archive.Files, map[string][]byte{
		"dist/release/plugin.json": manifestData,
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
		"cp plugin.json dist/release/plugin.json",
		"cp plugin.contracts.json dist/release/plugin.contracts.json",
		"--file dist/release/plugin.json --strict-contracts",
		"WFCTL_VERSION=$(GOWORK=off go list -m github.com/GoCodeAlone/workflow",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf(".goreleaser.yaml release validation hook missing %q", want)
		}
	}

	dir := filepath.Join(t.TempDir(), "dist", "release")
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
