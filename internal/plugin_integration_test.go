//go:build integration

// Package internal integration tests require a network connection (to fetch
// wfctl) and a full Go toolchain build. Run them with:
//
//	go test -tags integration ./internal/
package internal

import (
	"bytes"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	dopb "github.com/GoCodeAlone/workflow-plugin-digitalocean/proto"
	wfexternal "github.com/GoCodeAlone/workflow/plugin/external"
)

// workflowModuleVersion reads the github.com/GoCodeAlone/workflow version
// pinned in go.mod so the integration tests use the same wfctl version the
// plugin is built against.
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

// TestPlugin_GRPCStrictContractsEndToEnd is an integration test that:
//  1. Stages plugin.json and plugin.contracts.json into a temporary plugin dir.
//  2. Runs wfctl --strict-contracts against the staged package.
//  3. Builds the plugin binary and loads it over gRPC.
//  4. Verifies the contract registry is propagated correctly over the gRPC
//     transport and that module creation via the gRPC adapter succeeds.
//
// This test shells out to `go run ...@version` (network) and `go build` so it
// is gated behind the "integration" build tag to keep `go test ./...` hermetic.
func TestPlugin_GRPCStrictContractsEndToEnd(t *testing.T) {
	repoRoot := testRepoRoot(t)
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

	// Validate the staged package with the same wfctl version pinned in go.mod.
	validate := exec.Command("go", "run", "github.com/GoCodeAlone/workflow/cmd/wfctl@"+workflowModuleVersion(t, repoRoot), "plugin", "validate", "--file", filepath.Join(pluginDir, "plugin.json"), "--strict-contracts")
	validate.Dir = repoRoot
	validate.Env = append(os.Environ(), "GOWORK=off", "GOPRIVATE=github.com/GoCodeAlone/*")
	if output, err := validate.CombinedOutput(); err != nil {
		t.Fatalf("strict validate staged plugin package: %v\n%s", err, output)
	}

	// Build the plugin binary so the gRPC adapter can start it.
	buildCmd := exec.Command("go", "build", "-o", filepath.Join(pluginDir, pluginName), "./cmd/plugin")
	buildCmd.Dir = repoRoot
	buildCmd.Env = append(os.Environ(), "GOWORK=off", "GOPRIVATE=github.com/GoCodeAlone/*")
	if output, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build plugin binary: %v\n%s", err, output)
	}

	// Load the plugin over gRPC and verify the contract registry round-trips.
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

	// Verify module creation via the gRPC adapter succeeds with all config fields.
	factory := adapter.ModuleFactories()[iacProviderModuleType]
	if factory == nil {
		t.Fatalf("missing %s module factory from gRPC adapter", iacProviderModuleType)
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
