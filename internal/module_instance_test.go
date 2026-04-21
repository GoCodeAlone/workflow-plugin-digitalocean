package internal

import (
	"context"
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
)

// compile-time interface checks
var (
	_ sdk.ModuleProvider = (*doPlugin)(nil)
	_ sdk.ModuleInstance = (*doModuleInstance)(nil)
	_ sdk.ServiceInvoker = (*doModuleInstance)(nil)
)

// ── doPlugin.ModuleProvider ───────────────────────────────────────────────────

func TestDoPlugin_ModuleTypes(t *testing.T) {
	p := &doPlugin{}
	types := p.ModuleTypes()
	if len(types) != 1 || types[0] != "iac.provider" {
		t.Errorf("ModuleTypes() = %v, want [\"iac.provider\"]", types)
	}
}

func TestDoPlugin_CreateModule_UnknownType(t *testing.T) {
	p := &doPlugin{}
	_, err := p.CreateModule("something.else", "x", nil)
	if err == nil {
		t.Fatal("expected error for unknown module type")
	}
}

func TestDoPlugin_CreateModule_MissingToken(t *testing.T) {
	p := &doPlugin{}
	_, err := p.CreateModule("iac.provider", "test", map[string]any{})
	if err == nil {
		t.Fatal("expected error when token is missing")
	}
}

func TestDoPlugin_CreateModule_ValidConfig(t *testing.T) {
	// Use a stub token — Initialize creates a godo client but doesn't call the API.
	p := &doPlugin{}
	mod, err := p.CreateModule("iac.provider", "test", map[string]any{
		"token": "test-token",
	})
	if err != nil {
		t.Fatalf("CreateModule: %v", err)
	}
	if mod == nil {
		t.Fatal("expected non-nil ModuleInstance")
	}
	if _, ok := mod.(sdk.ServiceInvoker); !ok {
		t.Errorf("ModuleInstance does not implement ServiceInvoker")
	}
}

// ── doModuleInstance lifecycle ────────────────────────────────────────────────

func TestDoModuleInstance_Lifecycle(t *testing.T) {
	mi := &doModuleInstance{provider: &DOProvider{}}
	if err := mi.Init(); err != nil {
		t.Errorf("Init: %v", err)
	}
	ctx := context.Background()
	if err := mi.Start(ctx); err != nil {
		t.Errorf("Start: %v", err)
	}
	if err := mi.Stop(ctx); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

// ── doModuleInstance.InvokeMethod ─────────────────────────────────────────────

func TestDoModuleInstance_InvokeMethod_Initialize_NoOp(t *testing.T) {
	mi := &doModuleInstance{provider: &DOProvider{}}
	result, err := mi.InvokeMethod("IaCProvider.Initialize", map[string]any{"token": "x"})
	if err != nil {
		t.Fatalf("IaCProvider.Initialize: %v", err)
	}
	if len(result) != 0 {
		// empty map is expected
	}
}

func TestDoModuleInstance_InvokeMethod_Name(t *testing.T) {
	mi := &doModuleInstance{provider: &DOProvider{}}
	result, err := mi.InvokeMethod("IaCProvider.Name", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["name"] != "digitalocean" {
		t.Errorf("name = %q, want \"digitalocean\"", result["name"])
	}
}

func TestDoModuleInstance_InvokeMethod_Version(t *testing.T) {
	mi := &doModuleInstance{provider: &DOProvider{}}
	result, err := mi.InvokeMethod("IaCProvider.Version", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["version"] == "" {
		t.Error("expected non-empty version")
	}
}

func TestDoModuleInstance_InvokeMethod_Capabilities(t *testing.T) {
	mi := &doModuleInstance{provider: &DOProvider{}}
	result, err := mi.InvokeMethod("IaCProvider.Capabilities", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	caps, ok := result["capabilities"].([]any)
	if !ok || len(caps) == 0 {
		t.Errorf("expected non-empty capabilities, got: %v", result)
	}
}

func TestDoModuleInstance_InvokeMethod_Unknown(t *testing.T) {
	mi := &doModuleInstance{provider: &DOProvider{}}
	_, err := mi.InvokeMethod("IaCProvider.Nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for unknown method")
	}
}

func TestDoModuleInstance_InvokeMethod_Update_MissingResourceType(t *testing.T) {
	mi := &doModuleInstance{provider: &DOProvider{}}
	_, err := mi.InvokeMethod("ResourceDriver.Update", map[string]any{})
	if err == nil {
		t.Fatal("expected error when resource_type is absent")
	}
}

func TestDoModuleInstance_InvokeMethod_Update_DispatchesToDriver(t *testing.T) {
	stub := &stubResourceDriver{}
	provider := &DOProvider{
		drivers: map[string]interfaces.ResourceDriver{
			"infra.container_service": stub,
		},
	}
	mi := &doModuleInstance{provider: provider}

	result, err := mi.InvokeMethod("ResourceDriver.Update", map[string]any{
		"resource_type": "infra.container_service",
		"ref_name":      "bmw-app",
		"ref_type":      "infra.container_service",
		"spec_name":     "bmw-app",
		"spec_type":     "infra.container_service",
		"spec_config":   map[string]any{"image": "registry.example.com/bmw:v2"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !stub.updateCalled {
		t.Error("Update was not called on the driver")
	}
	if result == nil {
		t.Error("expected non-nil result map")
	}
}

func TestDoModuleInstance_InvokeMethod_HealthCheck_DispatchesToDriver(t *testing.T) {
	stub := &stubResourceDriver{healthyResult: true}
	provider := &DOProvider{
		drivers: map[string]interfaces.ResourceDriver{
			"infra.container_service": stub,
		},
	}
	mi := &doModuleInstance{provider: provider}

	result, err := mi.InvokeMethod("ResourceDriver.HealthCheck", map[string]any{
		"resource_type": "infra.container_service",
		"ref_name":      "bmw-app",
		"ref_type":      "infra.container_service",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if result["healthy"] != true {
		t.Errorf("expected healthy=true, got: %v", result)
	}
}

func TestDoModuleInstance_InvokeMethod_HealthCheck_Unhealthy(t *testing.T) {
	stub := &stubResourceDriver{healthyResult: false, healthMessage: "not ready"}
	provider := &DOProvider{
		drivers: map[string]interfaces.ResourceDriver{
			"infra.container_service": stub,
		},
	}
	mi := &doModuleInstance{provider: provider}

	result, err := mi.InvokeMethod("ResourceDriver.HealthCheck", map[string]any{
		"resource_type": "infra.container_service",
		"ref_name":      "bmw-app",
		"ref_type":      "infra.container_service",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if result["healthy"] != false {
		t.Errorf("expected healthy=false, got: %v", result["healthy"])
	}
	if result["message"] != "not ready" {
		t.Errorf("expected message 'not ready', got: %v", result["message"])
	}
}

// ── stub driver ───────────────────────────────────────────────────────────────

type stubResourceDriver struct {
	updateCalled  bool
	healthyResult bool
	healthMessage string
}

func (s *stubResourceDriver) Create(_ context.Context, _ interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	return &interfaces.ResourceOutput{}, nil
}
func (s *stubResourceDriver) Read(_ context.Context, _ interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	return &interfaces.ResourceOutput{}, nil
}
func (s *stubResourceDriver) Update(_ context.Context, _ interfaces.ResourceRef, _ interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	s.updateCalled = true
	return &interfaces.ResourceOutput{Status: "running"}, nil
}
func (s *stubResourceDriver) Delete(_ context.Context, _ interfaces.ResourceRef) error { return nil }
func (s *stubResourceDriver) Diff(_ context.Context, _ interfaces.ResourceSpec, _ *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	return &interfaces.DiffResult{}, nil
}
func (s *stubResourceDriver) HealthCheck(_ context.Context, _ interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	return &interfaces.HealthResult{Healthy: s.healthyResult, Message: s.healthMessage}, nil
}
func (s *stubResourceDriver) Scale(_ context.Context, _ interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return &interfaces.ResourceOutput{}, nil
}
func (s *stubResourceDriver) SensitiveKeys() []string { return nil }
