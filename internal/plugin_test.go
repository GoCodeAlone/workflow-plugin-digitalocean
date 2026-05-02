package internal

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	dopb "github.com/GoCodeAlone/workflow-plugin-digitalocean/proto"
	wfexternal "github.com/GoCodeAlone/workflow/plugin/external"
	externalPb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
	"google.golang.org/protobuf/types/known/anypb"
	"gopkg.in/yaml.v3"
)

// compile-time interface checks
var (
	_ sdk.PluginProvider      = (*doPlugin)(nil)
	_ sdk.ModuleProvider      = (*doPlugin)(nil)
	_ sdk.TypedModuleProvider = (*doPlugin)(nil)
	_ sdk.ContractProvider    = (*doPlugin)(nil)
)

func TestPlugin_Manifest(t *testing.T) {
	p := NewDOPlugin()
	m := p.Manifest()
	var manifest struct {
		Name        string `json:"name"`
		Author      string `json:"author"`
		Description string `json:"description"`
	}
	data, err := os.ReadFile(filepath.Join(testRepoRoot(t), "plugin.json"))
	if err != nil {
		t.Fatalf("read plugin.json: %v", err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse plugin.json: %v", err)
	}
	if m.Name != manifest.Name {
		t.Errorf("Name = %q, want %q", m.Name, manifest.Name)
	}
	if m.Author != manifest.Author {
		t.Errorf("Author = %q, want %q", m.Author, manifest.Author)
	}
	if m.Description != manifest.Description {
		t.Errorf("Description = %q, want %q", m.Description, manifest.Description)
	}
	if m.Version == "" {
		t.Error("expected non-empty Version")
	}
}

// TestPlugin_ContractRegistry verifies that ContractRegistry returns a strict
// module descriptor for "iac.provider" with the correct config message name
// and an embedded FileDescriptorSet.
func TestPlugin_ContractRegistry(t *testing.T) {
	p := NewDOPlugin().(*doPlugin)
	reg := p.ContractRegistry()

	if reg == nil {
		t.Fatal("ContractRegistry returned nil")
	}
	if len(reg.Contracts) != 1 {
		t.Fatalf("expected 1 contract descriptor, got %d", len(reg.Contracts))
	}

	d := reg.Contracts[0]
	if d.Kind != externalPb.ContractKind_CONTRACT_KIND_MODULE {
		t.Errorf("Kind = %v, want CONTRACT_KIND_MODULE", d.Kind)
	}
	if d.ModuleType != iacProviderModuleType {
		t.Errorf("ModuleType = %q, want %q", d.ModuleType, iacProviderModuleType)
	}
	if d.Mode != externalPb.ContractMode_CONTRACT_MODE_STRICT_PROTO {
		t.Errorf("Mode = %v, want CONTRACT_MODE_STRICT_PROTO", d.Mode)
	}
	wantConfig := string((&dopb.IacProviderConfig{}).ProtoReflect().Descriptor().FullName())
	if d.ConfigMessage != wantConfig {
		t.Errorf("ConfigMessage = %q, want %q", d.ConfigMessage, wantConfig)
	}
	if reg.FileDescriptorSet == nil {
		t.Error("FileDescriptorSet is nil; expected embedded proto descriptors")
	}
	if len(reg.FileDescriptorSet.File) == 0 {
		t.Error("FileDescriptorSet has no files")
	}
}

func TestPlugin_StaticContractManifestMatchesRuntimeRegistry(t *testing.T) {
	p := NewDOPlugin().(*doPlugin)
	reg := p.ContractRegistry()
	if reg == nil || len(reg.Contracts) != 1 {
		t.Fatalf("runtime registry must have exactly 1 contract, got %#v", reg)
	}
	runtimeContract := reg.Contracts[0]

	var manifest struct {
		Version   string `json:"version"`
		Contracts []struct {
			Kind   string `json:"kind"`
			Type   string `json:"type"`
			Mode   string `json:"mode"`
			Config string `json:"config"`
		} `json:"contracts"`
	}
	data, err := os.ReadFile(filepath.Join(testRepoRoot(t), "plugin.contracts.json"))
	if err != nil {
		t.Fatalf("read plugin.contracts.json: %v", err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse plugin.contracts.json: %v", err)
	}
	if manifest.Version != "1" {
		t.Errorf("manifest version = %q, want 1", manifest.Version)
	}
	if len(manifest.Contracts) != 1 {
		t.Fatalf("manifest contracts = %d, want 1", len(manifest.Contracts))
	}
	staticContract := manifest.Contracts[0]
	if staticContract.Kind != "module" {
		t.Errorf("manifest kind = %q, want module", staticContract.Kind)
	}
	if staticContract.Type != runtimeContract.ModuleType {
		t.Errorf("manifest type = %q, runtime module type = %q", staticContract.Type, runtimeContract.ModuleType)
	}
	if staticContract.Mode != "strict" {
		t.Errorf("manifest mode = %q, want strict", staticContract.Mode)
	}
	if runtimeContract.Mode != externalPb.ContractMode_CONTRACT_MODE_STRICT_PROTO {
		t.Errorf("runtime mode = %v, want strict proto", runtimeContract.Mode)
	}
	if staticContract.Config != runtimeContract.ConfigMessage {
		t.Errorf("manifest config = %q, runtime config = %q", staticContract.Config, runtimeContract.ConfigMessage)
	}
}

func TestPlugin_GRPCStrictContractsEndToEnd(t *testing.T) {
	repoRoot := testRepoRoot(t)
	t.Setenv("WORKFLOW_PLUGIN_DIGITALOCEAN_DISABLE_LEGACY_MODULE", "1")
	const pluginName = "workflow-plugin-digitalocean"
	pluginsDir := filepath.Join(t.TempDir(), "plugins")
	pluginDir := filepath.Join(pluginsDir, pluginName)
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("create plugin dir: %v", err)
	}
	manifestData, err := os.ReadFile(filepath.Join(repoRoot, "plugin.json"))
	if err != nil {
		t.Fatalf("read plugin.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"), manifestData, 0o644); err != nil {
		t.Fatalf("write temp plugin.json: %v", err)
	}
	contractsData, err := os.ReadFile(filepath.Join(repoRoot, "plugin.contracts.json"))
	if err != nil {
		t.Fatalf("read plugin.contracts.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.contracts.json"), contractsData, 0o644); err != nil {
		t.Fatalf("write temp plugin.contracts.json: %v", err)
	}

	validate := exec.Command("go", "run", "github.com/GoCodeAlone/workflow/cmd/wfctl@"+workflowModuleVersion(t, repoRoot), "plugin", "validate", "--file", filepath.Join(pluginDir, "plugin.json"), "--strict-contracts")
	validate.Dir = repoRoot
	validate.Env = append(os.Environ(), "GOWORK=off", "GOPRIVATE=github.com/GoCodeAlone/*")
	if output, err := validate.CombinedOutput(); err != nil {
		t.Fatalf("strict validate staged plugin package: %v\n%s", err, output)
	}

	cmd := exec.Command("go", "build", "-o", filepath.Join(pluginDir, pluginName), "./cmd/plugin")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPRIVATE=github.com/GoCodeAlone/*")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build plugin binary: %v\n%s", err, output)
	}

	manager := wfexternal.NewExternalPluginManager(pluginsDir, log.New(io.Discard, "", 0))
	adapter, err := manager.LoadPlugin(pluginName)
	if err != nil {
		t.Fatalf("load plugin over gRPC: %v", err)
	}
	t.Cleanup(manager.Shutdown)

	registry := adapter.ContractRegistry()
	if registry == nil || len(registry.Contracts) != 1 {
		t.Fatalf("gRPC contract registry length = %d, want 1", len(registry.GetContracts()))
	}
	contract := registry.Contracts[0]
	if contract.ModuleType != iacProviderModuleType {
		t.Fatalf("gRPC contract module type = %q, want %s", contract.ModuleType, iacProviderModuleType)
	}
	if contract.ConfigMessage != string((&dopb.IacProviderConfig{}).ProtoReflect().Descriptor().FullName()) {
		t.Fatalf("gRPC contract config = %q", contract.ConfigMessage)
	}

	factory := adapter.ModuleFactories()[iacProviderModuleType]
	if factory == nil {
		t.Fatalf("missing %s module factory from gRPC adapter", iacProviderModuleType)
	}
	if _, err := NewDOPlugin().(*doPlugin).CreateModule(iacProviderModuleType, "legacy-disabled", map[string]any{
		"token": "fake-token-for-test",
	}); err == nil {
		t.Fatal("legacy module creation unexpectedly succeeded while disabled; gRPC success would no longer prove typed_config was used")
	}
	module := factory("strict-do", map[string]any{
		"token":             "fake-token-for-test",
		"region":            "nyc3",
		"spaces_access_key": "access",
		"spaces_secret_key": "secret",
	})
	if err := wfexternal.AsModuleError(module); err != nil {
		t.Fatalf("strict gRPC module creation failed: %v", err)
	}
}

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

func workflowModuleVersion(t *testing.T, repoRoot string) string {
	t.Helper()
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Version}}", "github.com/GoCodeAlone/workflow")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPRIVATE=github.com/GoCodeAlone/*")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("resolve workflow module version: %v", err)
	}
	return string(bytes.TrimSpace(output))
}

// TestPlugin_TypedModuleTypes verifies the typed module type list.
func TestPlugin_TypedModuleTypes(t *testing.T) {
	p := NewDOPlugin().(*doPlugin)
	types := p.TypedModuleTypes()
	if len(types) != 1 || types[0] != iacProviderModuleType {
		t.Errorf("TypedModuleTypes = %v, want [%q]", types, iacProviderModuleType)
	}
}

// TestPlugin_CreateTypedModule_NilConfigFallsBack verifies that a nil typed
// config causes CreateTypedModule to return ErrTypedContractNotHandled so
// the gRPC server falls back to the legacy ModuleProvider path.
func TestPlugin_CreateTypedModule_NilConfigFallsBack(t *testing.T) {
	p := NewDOPlugin().(*doPlugin)
	_, err := p.CreateTypedModule(iacProviderModuleType, "mymodule", nil)
	if err == nil {
		t.Fatal("expected ErrTypedContractNotHandled, got nil")
	}
	if !isErrTypedContractNotHandled(err) {
		t.Errorf("expected ErrTypedContractNotHandled, got %v", err)
	}
}

// TestPlugin_CreateTypedModule_UnknownType verifies that unknown types are
// rejected with ErrTypedContractNotHandled.
func TestPlugin_CreateTypedModule_UnknownType(t *testing.T) {
	p := NewDOPlugin().(*doPlugin)
	packed, err := anypb.New(&dopb.IacProviderConfig{Token: "tok"})
	if err != nil {
		t.Fatalf("anypb.New: %v", err)
	}
	_, err = p.CreateTypedModule("unknown.type", "m", packed)
	if err == nil {
		t.Fatal("expected error for unknown type, got nil")
	}
	if !isErrTypedContractNotHandled(err) {
		t.Errorf("expected ErrTypedContractNotHandled, got %v", err)
	}
}

// TestPlugin_CreateTypedModule_TypeMismatch verifies that a type mismatch in
// the Any payload is caught before calling Initialize.
func TestPlugin_CreateTypedModule_TypeMismatch(t *testing.T) {
	p := NewDOPlugin().(*doPlugin)
	// Pack a different message type.
	wrongMsg := &externalPb.ContractDescriptor{}
	packed, err := anypb.New(wrongMsg)
	if err != nil {
		t.Fatalf("anypb.New: %v", err)
	}
	_, err = p.CreateTypedModule(iacProviderModuleType, "m", packed)
	if err == nil {
		t.Fatal("expected type mismatch error, got nil")
	}
}

// TestPlugin_CreateTypedModule_MissingToken verifies that an IacProviderConfig
// without a token causes initialization to fail with a descriptive error.
func TestPlugin_CreateTypedModule_MissingToken(t *testing.T) {
	p := NewDOPlugin().(*doPlugin)
	packed, err := anypb.New(&dopb.IacProviderConfig{})
	if err != nil {
		t.Fatalf("anypb.New: %v", err)
	}
	_, err = p.CreateTypedModule(iacProviderModuleType, "m", packed)
	if err == nil {
		t.Fatal("expected error for missing token, got nil")
	}
}

// TestPlugin_CreateTypedModule_ValidConfig verifies that a correctly populated
// IacProviderConfig results in a non-nil ModuleInstance.
func TestPlugin_CreateTypedModule_ValidConfig(t *testing.T) {
	p := NewDOPlugin().(*doPlugin)
	packed, err := anypb.New(&dopb.IacProviderConfig{Token: "fake-token-for-test"})
	if err != nil {
		t.Fatalf("anypb.New: %v", err)
	}
	inst, err := p.CreateTypedModule(iacProviderModuleType, "m", packed)
	if err != nil {
		t.Fatalf("CreateTypedModule returned error: %v", err)
	}
	if inst == nil {
		t.Fatal("expected non-nil ModuleInstance")
	}
	if _, ok := inst.(*doModuleInstance); !ok {
		t.Fatalf("CreateTypedModule returned %T, want *doModuleInstance to preserve context-aware service invocation", inst)
	}
	if _, ok := inst.(sdk.ServiceContextInvoker); !ok {
		t.Fatalf("CreateTypedModule returned %T without sdk.ServiceContextInvoker", inst)
	}
}

// isErrTypedContractNotHandled checks whether the error (possibly wrapped) is
// or wraps sdk.ErrTypedContractNotHandled.
func isErrTypedContractNotHandled(err error) bool {
	return errors.Is(err, sdk.ErrTypedContractNotHandled)
}

// TestIacConfigToMap verifies that all IacProviderConfig fields are mapped to
// the exact config keys that DOProvider.Initialize consumes, and that empty
// fields are omitted.
func TestIacConfigToMap(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *dopb.IacProviderConfig
		wantKeys []string
		absent   []string
	}{
		{
			name:     "token only",
			cfg:      &dopb.IacProviderConfig{Token: "tok"},
			wantKeys: []string{"token"},
			absent:   []string{"region", "spaces_access_key", "spaces_secret_key"},
		},
		{
			name: "all fields",
			cfg: &dopb.IacProviderConfig{
				Token:           "tok",
				Region:          "nyc3",
				SpacesAccessKey: "access",
				SpacesSecretKey: "secret",
			},
			wantKeys: []string{"token", "region", "spaces_access_key", "spaces_secret_key"},
			absent:   nil,
		},
		{
			name:     "empty config omits all keys",
			cfg:      &dopb.IacProviderConfig{},
			wantKeys: nil,
			absent:   []string{"token", "region", "spaces_access_key", "spaces_secret_key"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := iacConfigToMap(tt.cfg)
			for _, k := range tt.wantKeys {
				if _, ok := m[k]; !ok {
					t.Errorf("key %q missing from map", k)
				}
			}
			for _, k := range tt.absent {
				if _, ok := m[k]; ok {
					t.Errorf("key %q should be absent from map", k)
				}
			}
			// Verify specific values for all-fields case.
			if tt.cfg.Region != "" {
				if got, _ := m["region"].(string); got != tt.cfg.Region {
					t.Errorf("region = %q, want %q", got, tt.cfg.Region)
				}
			}
			if tt.cfg.SpacesAccessKey != "" {
				if got, _ := m["spaces_access_key"].(string); got != tt.cfg.SpacesAccessKey {
					t.Errorf("spaces_access_key = %q, want %q", got, tt.cfg.SpacesAccessKey)
				}
			}
			if tt.cfg.SpacesSecretKey != "" {
				if got, _ := m["spaces_secret_key"].(string); got != tt.cfg.SpacesSecretKey {
					t.Errorf("spaces_secret_key = %q, want %q", got, tt.cfg.SpacesSecretKey)
				}
			}
		})
	}
}
