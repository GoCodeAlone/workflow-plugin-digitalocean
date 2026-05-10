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
			Builds       []string `yaml:"builds"`
			NameTemplate string   `yaml:"name_template"`
			Files        []string `yaml:"files"`
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
	if len(archive.Builds) != 1 || archive.Builds[0] != build.ID {
		t.Fatalf("archive builds = %v, want [%s]", archive.Builds, build.ID)
	}
	if !containsString(archive.Files, "plugin.json") {
		t.Fatalf("archive files = %v, want plugin.json", archive.Files)
	}
	if !containsString(archive.Files, "plugin.contracts.json") {
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
	if err := writeTestArchive(repoRoot, archivePath, archive.Files); err != nil {
		t.Fatalf("write test archive: %v", err)
	}
	if !tarGzContains(archivePath, "plugin.contracts.json") {
		t.Fatal("test release archive missing plugin.contracts.json")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func writeTestArchive(repoRoot, archivePath string, files []string) error {
	out, err := os.Create(archivePath)
	if err != nil {
		return err
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	for _, name := range files {
		data, err := os.ReadFile(filepath.Join(repoRoot, name))
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}); err != nil {
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
