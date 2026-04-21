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

func TestDoModuleInstance_InvokeMethod_Create_DispatchesToDriver(t *testing.T) {
	stub := &stubResourceDriver{createOutput: &interfaces.ResourceOutput{
		ProviderID: "do-123", Name: "my-app", Type: "infra.container_service", Status: "active",
	}}
	provider := &DOProvider{drivers: map[string]interfaces.ResourceDriver{"infra.container_service": stub}}
	mi := &doModuleInstance{provider: provider}

	result, err := mi.InvokeMethod("ResourceDriver.Create", map[string]any{
		"resource_type": "infra.container_service",
		"spec_name":     "my-app",
		"spec_type":     "infra.container_service",
		"spec_config":   map[string]any{"image": "nginx:latest"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !stub.createCalled {
		t.Error("Create was not called on the driver")
	}
	if result["provider_id"] != "do-123" {
		t.Errorf("expected provider_id=do-123, got %v", result["provider_id"])
	}
	if result["status"] != "active" {
		t.Errorf("expected status=active, got %v", result["status"])
	}
}

func TestDoModuleInstance_InvokeMethod_Create_MissingResourceType(t *testing.T) {
	mi := &doModuleInstance{provider: &DOProvider{}}
	_, err := mi.InvokeMethod("ResourceDriver.Create", map[string]any{})
	if err == nil {
		t.Fatal("expected error when resource_type is absent")
	}
}

func TestDoModuleInstance_InvokeMethod_Read_DispatchesToDriver(t *testing.T) {
	stub := &stubResourceDriver{readOutput: &interfaces.ResourceOutput{
		ProviderID: "do-456", Name: "my-db", Type: "infra.database", Status: "running",
		Outputs: map[string]any{"host": "db.example.com"},
	}}
	provider := &DOProvider{drivers: map[string]interfaces.ResourceDriver{"infra.database": stub}}
	mi := &doModuleInstance{provider: provider}

	result, err := mi.InvokeMethod("ResourceDriver.Read", map[string]any{
		"resource_type":    "infra.database",
		"ref_name":         "my-db",
		"ref_type":         "infra.database",
		"ref_provider_id":  "do-456",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !stub.readCalled {
		t.Error("Read was not called on the driver")
	}
	if result["provider_id"] != "do-456" {
		t.Errorf("expected provider_id=do-456, got %v", result["provider_id"])
	}
	outputs, _ := result["outputs"].(map[string]any)
	if outputs["host"] != "db.example.com" {
		t.Errorf("expected host in outputs, got %v", outputs)
	}
}

func TestDoModuleInstance_InvokeMethod_Delete_DispatchesToDriver(t *testing.T) {
	stub := &stubResourceDriver{}
	provider := &DOProvider{drivers: map[string]interfaces.ResourceDriver{"infra.vpc": stub}}
	mi := &doModuleInstance{provider: provider}

	result, err := mi.InvokeMethod("ResourceDriver.Delete", map[string]any{
		"resource_type":   "infra.vpc",
		"ref_name":        "my-vpc",
		"ref_type":        "infra.vpc",
		"ref_provider_id": "do-789",
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !stub.deleteCalled {
		t.Error("Delete was not called on the driver")
	}
	if result == nil {
		t.Error("expected non-nil result map")
	}
}

func TestDoModuleInstance_InvokeMethod_Diff_DispatchesToDriver(t *testing.T) {
	stub := &stubResourceDriver{diffOutput: &interfaces.DiffResult{
		NeedsUpdate: true,
		Changes: []interfaces.FieldChange{
			{Path: "image", Old: "nginx:1.0", New: "nginx:2.0", ForceNew: false},
		},
	}}
	provider := &DOProvider{drivers: map[string]interfaces.ResourceDriver{"infra.container_service": stub}}
	mi := &doModuleInstance{provider: provider}

	result, err := mi.InvokeMethod("ResourceDriver.Diff", map[string]any{
		"resource_type":    "infra.container_service",
		"spec_name":        "my-app",
		"spec_type":        "infra.container_service",
		"spec_config":      map[string]any{"image": "nginx:2.0"},
		"current_provider_id": "do-abc",
		"current_name":     "my-app",
		"current_type":     "infra.container_service",
		"current_status":   "running",
		"current_outputs":  map[string]any{"url": "https://app.example.com"},
	})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !stub.diffCalled {
		t.Error("Diff was not called on the driver")
	}
	if result["needs_update"] != true {
		t.Errorf("expected needs_update=true, got %v", result["needs_update"])
	}
	changes, _ := result["changes"].([]any)
	if len(changes) != 1 {
		t.Errorf("expected 1 change, got %d", len(changes))
	}
}

func TestDoModuleInstance_InvokeMethod_Scale_DispatchesToDriver(t *testing.T) {
	stub := &stubResourceDriver{scaleOutput: &interfaces.ResourceOutput{
		ProviderID: "do-777", Status: "scaling",
	}}
	provider := &DOProvider{drivers: map[string]interfaces.ResourceDriver{"infra.k8s_cluster": stub}}
	mi := &doModuleInstance{provider: provider}

	result, err := mi.InvokeMethod("ResourceDriver.Scale", map[string]any{
		"resource_type":   "infra.k8s_cluster",
		"ref_name":        "my-cluster",
		"ref_type":        "infra.k8s_cluster",
		"ref_provider_id": "do-777",
		"replicas":        3,
	})
	if err != nil {
		t.Fatalf("Scale: %v", err)
	}
	if !stub.scaleCalled {
		t.Error("Scale was not called on the driver")
	}
	if stub.scaleReplicas != 3 {
		t.Errorf("expected replicas=3, got %d", stub.scaleReplicas)
	}
	if result["status"] != "scaling" {
		t.Errorf("expected status=scaling, got %v", result["status"])
	}
}

func TestDoModuleInstance_InvokeMethod_Scale_ReplicasAsFloat(t *testing.T) {
	// JSON numbers unmarshal as float64; the dispatch must handle both int and float64.
	stub := &stubResourceDriver{scaleOutput: &interfaces.ResourceOutput{Status: "scaling"}}
	provider := &DOProvider{drivers: map[string]interfaces.ResourceDriver{"infra.k8s_cluster": stub}}
	mi := &doModuleInstance{provider: provider}

	_, err := mi.InvokeMethod("ResourceDriver.Scale", map[string]any{
		"resource_type": "infra.k8s_cluster",
		"ref_name":      "my-cluster",
		"ref_type":      "infra.k8s_cluster",
		"replicas":      float64(5),
	})
	if err != nil {
		t.Fatalf("Scale with float64 replicas: %v", err)
	}
	if stub.scaleReplicas != 5 {
		t.Errorf("expected replicas=5, got %d", stub.scaleReplicas)
	}
}

func TestDoModuleInstance_InvokeMethod_SensitiveKeys_DispatchesToDriver(t *testing.T) {
	stub := &stubResourceDriver{sensitiveKeys: []string{"password", "api_key"}}
	provider := &DOProvider{drivers: map[string]interfaces.ResourceDriver{"infra.database": stub}}
	mi := &doModuleInstance{provider: provider}

	result, err := mi.InvokeMethod("ResourceDriver.SensitiveKeys", map[string]any{
		"resource_type": "infra.database",
	})
	if err != nil {
		t.Fatalf("SensitiveKeys: %v", err)
	}
	if !stub.sensitiveKeysCalled {
		t.Error("SensitiveKeys was not called on the driver")
	}
	keys, _ := result["keys"].([]string)
	if len(keys) != 2 || keys[0] != "password" || keys[1] != "api_key" {
		t.Errorf("expected [password api_key], got %v", result["keys"])
	}
}

func TestDoModuleInstance_InvokeMethod_ResourceOutputSensitive(t *testing.T) {
	// Verify resourceOutputToMap includes the sensitive field.
	stub := &stubResourceDriver{createOutput: &interfaces.ResourceOutput{
		ProviderID: "do-999",
		Status:     "active",
		Sensitive:  map[string]bool{"password": true},
	}}
	provider := &DOProvider{drivers: map[string]interfaces.ResourceDriver{"infra.database": stub}}
	mi := &doModuleInstance{provider: provider}

	result, err := mi.InvokeMethod("ResourceDriver.Create", map[string]any{
		"resource_type": "infra.database",
		"spec_name":     "my-db",
		"spec_type":     "infra.database",
		"spec_config":   map[string]any{},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	sensitive, _ := result["sensitive"].(map[string]bool)
	if !sensitive["password"] {
		t.Errorf("expected sensitive[password]=true, got %v", result["sensitive"])
	}
}

// ── stub driver ───────────────────────────────────────────────────────────────

type stubResourceDriver struct {
	// call tracking
	createCalled       bool
	readCalled         bool
	updateCalled       bool
	deleteCalled       bool
	diffCalled         bool
	scaleCalled        bool
	scaleReplicas      int
	sensitiveKeysCalled bool
	healthyResult      bool
	healthMessage      string

	// return values
	createOutput  *interfaces.ResourceOutput
	readOutput    *interfaces.ResourceOutput
	diffOutput    *interfaces.DiffResult
	scaleOutput   *interfaces.ResourceOutput
	sensitiveKeys []string
}

func (s *stubResourceDriver) Create(_ context.Context, _ interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	s.createCalled = true
	if s.createOutput != nil {
		return s.createOutput, nil
	}
	return &interfaces.ResourceOutput{}, nil
}
func (s *stubResourceDriver) Read(_ context.Context, _ interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	s.readCalled = true
	if s.readOutput != nil {
		return s.readOutput, nil
	}
	return &interfaces.ResourceOutput{}, nil
}
func (s *stubResourceDriver) Update(_ context.Context, _ interfaces.ResourceRef, _ interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	s.updateCalled = true
	return &interfaces.ResourceOutput{Status: "running"}, nil
}
func (s *stubResourceDriver) Delete(_ context.Context, _ interfaces.ResourceRef) error {
	s.deleteCalled = true
	return nil
}
func (s *stubResourceDriver) Diff(_ context.Context, _ interfaces.ResourceSpec, _ *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	s.diffCalled = true
	if s.diffOutput != nil {
		return s.diffOutput, nil
	}
	return &interfaces.DiffResult{}, nil
}
func (s *stubResourceDriver) HealthCheck(_ context.Context, _ interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	return &interfaces.HealthResult{Healthy: s.healthyResult, Message: s.healthMessage}, nil
}
func (s *stubResourceDriver) Scale(_ context.Context, _ interfaces.ResourceRef, replicas int) (*interfaces.ResourceOutput, error) {
	s.scaleCalled = true
	s.scaleReplicas = replicas
	if s.scaleOutput != nil {
		return s.scaleOutput, nil
	}
	return &interfaces.ResourceOutput{}, nil
}
func (s *stubResourceDriver) SensitiveKeys() []string {
	s.sensitiveKeysCalled = true
	return s.sensitiveKeys
}
