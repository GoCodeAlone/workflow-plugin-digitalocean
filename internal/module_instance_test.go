package internal

import (
	"context"
	"errors"
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
		"resource_type":   "infra.database",
		"ref_name":        "my-db",
		"ref_type":        "infra.database",
		"ref_provider_id": "do-456",
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
		"resource_type":       "infra.container_service",
		"spec_name":           "my-app",
		"spec_type":           "infra.container_service",
		"spec_config":         map[string]any{"image": "nginx:2.0"},
		"current_provider_id": "do-abc",
		"current_name":        "my-app",
		"current_type":        "infra.container_service",
		"current_status":      "running",
		"current_outputs":     map[string]any{"url": "https://app.example.com"},
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

func TestDoModuleInstance_InvokeMethod_Diff_CurrentSensitive_GRPCForm(t *testing.T) {
	// current_sensitive arrives as map[string]any through the gRPC boundary
	// (protobuf Struct deserializes nested objects as map[string]any, not map[string]bool).
	// Verify currentFromArgs handles both forms correctly.
	stub := &stubResourceDriver{diffOutput: &interfaces.DiffResult{NeedsUpdate: false}}
	provider := &DOProvider{drivers: map[string]interfaces.ResourceDriver{"infra.database": stub}}
	mi := &doModuleInstance{provider: provider}

	_, err := mi.InvokeMethod("ResourceDriver.Diff", map[string]any{
		"resource_type":       "infra.database",
		"spec_name":           "my-db",
		"spec_type":           "infra.database",
		"spec_config":         map[string]any{},
		"current_provider_id": "do-abc",
		"current_name":        "my-db",
		"current_type":        "infra.database",
		"current_status":      "running",
		// gRPC-deserialized form: values are any (bool), not a typed map[string]bool
		"current_sensitive": map[string]any{"password": true, "api_key": true},
	})
	if err != nil {
		t.Fatalf("Diff with gRPC-form current_sensitive: %v", err)
	}
	if !stub.diffCalled {
		t.Error("Diff was not called on the driver")
	}
	// Verify the sensitive map was decoded into the ResourceOutput passed to Diff.
	if stub.lastDiffCurrent == nil {
		t.Fatal("expected non-nil current passed to Diff")
	}
	if !stub.lastDiffCurrent.Sensitive["password"] {
		t.Errorf("expected sensitive[password]=true, got %v", stub.lastDiffCurrent.Sensitive)
	}
	if !stub.lastDiffCurrent.Sensitive["api_key"] {
		t.Errorf("expected sensitive[api_key]=true, got %v", stub.lastDiffCurrent.Sensitive)
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

// ── IaCProvider bulk-method dispatch tests ────────────────────────────────────

func TestDoModuleInstance_InvokeMethod_Plan_DispatchesToProvider(t *testing.T) {
	fake := &fakeIaCProvider{planResult: &interfaces.IaCPlan{
		ID: "plan-abc",
		Actions: []interfaces.PlanAction{
			{Action: "create", Resource: interfaces.ResourceSpec{Name: "my-db", Type: "infra.database"}},
		},
	}}
	mi := &doModuleInstance{provider: fake}

	result, err := mi.InvokeMethod("IaCProvider.Plan", map[string]any{
		"desired": []any{
			map[string]any{"name": "my-db", "type": "infra.database", "config": map[string]any{}},
		},
		"current": []any{},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !fake.planCalled {
		t.Error("Plan was not called on the provider")
	}
	if result["id"] != "plan-abc" {
		t.Errorf("expected id=plan-abc, got %v", result["id"])
	}
	actions, _ := result["actions"].([]any)
	if len(actions) != 1 {
		t.Errorf("expected 1 action, got %d", len(actions))
	}
}

func TestDoModuleInstance_InvokeMethod_Apply_DispatchesToProvider(t *testing.T) {
	fake := &fakeIaCProvider{applyResult: &interfaces.ApplyResult{
		PlanID:    "plan-abc",
		Resources: []interfaces.ResourceOutput{{ProviderID: "do-111", Status: "active"}},
	}}
	mi := &doModuleInstance{provider: fake}

	result, err := mi.InvokeMethod("IaCProvider.Apply", map[string]any{
		"plan": map[string]any{"id": "plan-abc", "actions": []any{}},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !fake.applyCalled {
		t.Error("Apply was not called on the provider")
	}
	if result["plan_id"] != "plan-abc" {
		t.Errorf("expected plan_id=plan-abc, got %v", result["plan_id"])
	}
	resources, _ := result["resources"].([]any)
	if len(resources) != 1 {
		t.Errorf("expected 1 resource, got %d", len(resources))
	}
}

func TestDoModuleInstance_InvokeMethod_Destroy_DispatchesToProvider(t *testing.T) {
	fake := &fakeIaCProvider{destroyResult: &interfaces.DestroyResult{
		Destroyed: []string{"my-db"},
	}}
	mi := &doModuleInstance{provider: fake}

	result, err := mi.InvokeMethod("IaCProvider.Destroy", map[string]any{
		"refs": []any{
			map[string]any{"name": "my-db", "type": "infra.database", "provider_id": "do-222"},
		},
	})
	if err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if !fake.destroyCalled {
		t.Error("Destroy was not called on the provider")
	}
	destroyed, _ := result["destroyed"].([]any)
	if len(destroyed) != 1 || destroyed[0] != "my-db" {
		t.Errorf("expected destroyed=[my-db], got %v", result["destroyed"])
	}
}

func TestDoModuleInstance_InvokeMethod_Status_DispatchesToProvider(t *testing.T) {
	fake := &fakeIaCProvider{statusResult: []interfaces.ResourceStatus{
		{Name: "my-app", Type: "infra.container_service", ProviderID: "do-333", Status: "running"},
	}}
	mi := &doModuleInstance{provider: fake}

	result, err := mi.InvokeMethod("IaCProvider.Status", map[string]any{
		"refs": []any{
			map[string]any{"name": "my-app", "type": "infra.container_service", "provider_id": "do-333"},
		},
	})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !fake.statusCalled {
		t.Error("Status was not called on the provider")
	}
	statuses, _ := result["statuses"].([]any)
	if len(statuses) != 1 {
		t.Errorf("expected 1 status, got %d", len(statuses))
	}
	s, _ := statuses[0].(map[string]any)
	if s["status"] != "running" {
		t.Errorf("expected status=running, got %v", s["status"])
	}
}

func TestDoModuleInstance_InvokeMethod_DetectDrift_DispatchesToProvider(t *testing.T) {
	fake := &fakeIaCProvider{driftResult: []interfaces.DriftResult{
		{Name: "my-vpc", Type: "infra.vpc", Drifted: true, Fields: []string{"cidr"}},
	}}
	mi := &doModuleInstance{provider: fake}

	result, err := mi.InvokeMethod("IaCProvider.DetectDrift", map[string]any{
		"refs": []any{
			map[string]any{"name": "my-vpc", "type": "infra.vpc", "provider_id": "do-444"},
		},
	})
	if err != nil {
		t.Fatalf("DetectDrift: %v", err)
	}
	if !fake.detectDriftCalled {
		t.Error("DetectDrift was not called on the provider")
	}
	drifts, _ := result["drifts"].([]any)
	if len(drifts) != 1 {
		t.Errorf("expected 1 drift, got %d", len(drifts))
	}
	d, _ := drifts[0].(map[string]any)
	if d["drifted"] != true {
		t.Errorf("expected drifted=true, got %v", d["drifted"])
	}
}

func TestDoModuleInstance_InvokeMethod_Import_DispatchesToProvider(t *testing.T) {
	fake := &fakeIaCProvider{importResult: &interfaces.ResourceState{
		ID: "do-555", Name: "imported-db", Type: "infra.database", Provider: "digitalocean",
	}}
	mi := &doModuleInstance{provider: fake}

	result, err := mi.InvokeMethod("IaCProvider.Import", map[string]any{
		"resource_type": "infra.database",
		"provider_id":   "do-555",
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !fake.importCalled {
		t.Error("Import was not called on the provider")
	}
	if result["id"] != "do-555" {
		t.Errorf("expected id=do-555, got %v", result["id"])
	}
	if result["name"] != "imported-db" {
		t.Errorf("expected name=imported-db, got %v", result["name"])
	}
}

func TestDoModuleInstance_InvokeMethod_ResolveSizing_DispatchesToProvider(t *testing.T) {
	fake := &fakeIaCProvider{sizingResult: &interfaces.ProviderSizing{
		InstanceType: "db-s-1vcpu-1gb",
		Specs:        map[string]any{"cpu": "1", "memory": "1Gi"},
	}}
	mi := &doModuleInstance{provider: fake}

	result, err := mi.InvokeMethod("IaCProvider.ResolveSizing", map[string]any{
		"resource_type": "infra.database",
		"size":          "s",
		"hints":         map[string]any{"cpu": "2"},
	})
	if err != nil {
		t.Fatalf("ResolveSizing: %v", err)
	}
	if !fake.resolveSizingCalled {
		t.Error("ResolveSizing was not called on the provider")
	}
	if result["instance_type"] != "db-s-1vcpu-1gb" {
		t.Errorf("expected instance_type=db-s-1vcpu-1gb, got %v", result["instance_type"])
	}
}

func TestDoModuleInstance_InvokeMethod_ResolveSizing_NoHints(t *testing.T) {
	fake := &fakeIaCProvider{sizingResult: &interfaces.ProviderSizing{InstanceType: "db-xs"}}
	mi := &doModuleInstance{provider: fake}

	_, err := mi.InvokeMethod("IaCProvider.ResolveSizing", map[string]any{
		"resource_type": "infra.database",
		"size":          "xs",
	})
	if err != nil {
		t.Fatalf("ResolveSizing without hints: %v", err)
	}
	if fake.resolveSizingHints != nil {
		t.Errorf("expected nil hints, got %v", fake.resolveSizingHints)
	}
}

// ── IaCProvider.BootstrapStateBackend dispatch tests ─────────────────────────
//
// wfctl sends cfg directly as args (same convention as IaCProvider.Initialize),
// NOT wrapped under a "cfg" key.

func TestDoModuleInstance_InvokeMethod_BootstrapStateBackend_Dispatches(t *testing.T) {
	// Args are sent flat (unwrapped) — bucket/region/accessKey/secretKey at top level.
	fake := &fakeIaCProvider{
		bootstrapResult: &interfaces.BootstrapResult{
			Bucket:   "bmw-state",
			Region:   "nyc3",
			Endpoint: "https://nyc3.digitaloceanspaces.com",
			EnvVars:  map[string]string{"WFCTL_STATE_BUCKET": "bmw-state", "SPACES_BUCKET": "bmw-state"},
		},
	}
	mi := &doModuleInstance{provider: fake}

	result, err := mi.InvokeMethod("IaCProvider.BootstrapStateBackend", map[string]any{
		"bucket":    "bmw-state",
		"region":    "nyc3",
		"accessKey": "k",
		"secretKey": "s",
	})
	if err != nil {
		t.Fatalf("BootstrapStateBackend dispatch: %v", err)
	}
	if !fake.bootstrapCalled {
		t.Error("expected BootstrapStateBackend to be called on the provider")
	}
	// Verify the cfg keys were passed through to the provider.
	if fake.bootstrapCfg["bucket"] != "bmw-state" {
		t.Errorf("provider received bucket = %q, want %q", fake.bootstrapCfg["bucket"], "bmw-state")
	}
	if result["bucket"] != "bmw-state" {
		t.Errorf("result bucket = %q, want %q", result["bucket"], "bmw-state")
	}
	if result["region"] != "nyc3" {
		t.Errorf("result region = %q, want %q", result["region"], "nyc3")
	}
}

func TestDoModuleInstance_InvokeMethod_BootstrapStateBackend_NilResult(t *testing.T) {
	// Provider returns (nil, nil) — method should return empty map, not panic.
	fake := &fakeIaCProvider{bootstrapResult: nil}
	mi := &doModuleInstance{provider: fake}

	result, err := mi.InvokeMethod("IaCProvider.BootstrapStateBackend", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error for nil result: %v", err)
	}
	if result == nil {
		t.Error("expected non-nil result map")
	}
}

func TestDoModuleInstance_InvokeMethod_BootstrapStateBackend_NilArgs(t *testing.T) {
	// InvokeMethod may be called with nil args — must not panic.
	fake := &fakeIaCProvider{bootstrapResult: &interfaces.BootstrapResult{Bucket: "b"}}
	mi := &doModuleInstance{provider: fake}

	result, err := mi.InvokeMethod("IaCProvider.BootstrapStateBackend", nil)
	if err != nil {
		t.Fatalf("nil args should not error: %v", err)
	}
	if result == nil {
		t.Error("expected non-nil result map")
	}
	if !fake.bootstrapCalled {
		t.Error("expected BootstrapStateBackend to be called on the provider")
	}
}

func TestDoModuleInstance_InvokeMethod_BootstrapStateBackend_EndToEnd(t *testing.T) {
	// End-to-end: InvokeMethod → DOProvider.bootstrapStateBackendWithFactory with
	// a fake S3 client. Exercises the full dispatch+config-parse path without network I/O.
	// This catches the class of bug where args schema and dispatch don't align.
	fakeBucket := &fakeBucketClient{headErr: nil} // bucket exists — no create
	p := NewDOProvider()
	p.bootstrapClientFactory = func(accessKey, secretKey, region string) spacesBucketClient {
		return fakeBucket
	}
	mi := &doModuleInstance{provider: p}

	result, err := mi.InvokeMethod("IaCProvider.BootstrapStateBackend", map[string]any{
		"bucket":    "bmw-state",
		"region":    "nyc3",
		"accessKey": "test-key",
		"secretKey": "test-secret",
	})
	if err != nil {
		t.Fatalf("end-to-end dispatch: %v", err)
	}
	if result["bucket"] != "bmw-state" {
		t.Errorf("bucket = %q, want %q", result["bucket"], "bmw-state")
	}
	if result["region"] != "nyc3" {
		t.Errorf("region = %q, want %q", result["region"], "nyc3")
	}
	if !fakeBucket.headCalled {
		t.Error("expected HeadBucket to be called on the fake S3 client")
	}
}

func TestDoModuleInstance_InvokeMethod_RepairDirtyMigration_DispatchesToProvider(t *testing.T) {
	fake := &fakeRepairProvider{
		fakeIaCProvider: fakeIaCProvider{},
		repairResult: &interfaces.MigrationRepairResult{
			ProviderJobID: "job-123",
			Status:        interfaces.MigrationRepairStatusSucceeded,
			Applied:       []string{"20260426000006"},
			Logs:          "repair complete",
		},
	}
	mi := &doModuleInstance{provider: fake}

	result, err := mi.InvokeMethod("IaCProvider.RepairDirtyMigration", map[string]any{
		"request": map[string]any{
			"app_resource_name":      "bmw-staging",
			"database_resource_name": "bmw-staging-db",
			"job_image":              "registry.digitalocean.com/bmw-registry/workflow-migrate:sha",
			"source_dir":             "/migrations",
			"expected_dirty_version": "20260426000005",
			"force_version":          "20260422000001",
			"then_up":                true,
			"confirm_force":          interfaces.MigrationRepairConfirmation,
			"env":                    map[string]any{"DATABASE_URL": "postgres://example"},
			"timeout_seconds":        float64(600),
		},
	})
	if err != nil {
		t.Fatalf("RepairDirtyMigration dispatch: %v", err)
	}
	if !fake.repairCalled {
		t.Fatal("RepairDirtyMigration was not called on provider")
	}
	if fake.repairReq.AppResourceName != "bmw-staging" {
		t.Fatalf("AppResourceName = %q", fake.repairReq.AppResourceName)
	}
	if fake.repairReq.Env["DATABASE_URL"] != "postgres://example" {
		t.Fatalf("DATABASE_URL was not decoded into request env")
	}
	if result["provider_job_id"] != "job-123" {
		t.Fatalf("provider_job_id = %v", result["provider_job_id"])
	}
	if result["status"] != interfaces.MigrationRepairStatusSucceeded {
		t.Fatalf("status = %v", result["status"])
	}
}

func TestDoModuleInstance_InvokeMethod_RepairDirtyMigration_Unsupported(t *testing.T) {
	mi := &doModuleInstance{provider: &fakeIaCProvider{}}

	_, err := mi.InvokeMethod("IaCProvider.RepairDirtyMigration", map[string]any{
		"request": map[string]any{},
	})
	if err == nil {
		t.Fatal("expected unsupported error")
	}
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("code = %s, want %s", status.Code(err), codes.Unimplemented)
	}
}

func TestDoModuleInstance_InvokeMethod_RepairDirtyMigration_PreservesResultOnError(t *testing.T) {
	fake := &fakeRepairProvider{
		fakeIaCProvider: fakeIaCProvider{},
		repairResult: &interfaces.MigrationRepairResult{
			ProviderJobID: "job-running",
			Status:        interfaces.MigrationRepairStatusFailed,
			Logs:          "repair log tail",
			Diagnostics: []interfaces.Diagnostic{{
				ID:     "job-running",
				Phase:  "cancel",
				Cause:  "job invocation cancellation requested",
				Detail: "app_id=app-123 deployment_id=dep-123",
			}},
		},
		repairErr: errors.New("timed out waiting for migration repair job"),
	}
	mi := &doModuleInstance{provider: fake}

	result, err := mi.InvokeMethod("IaCProvider.RepairDirtyMigration", map[string]any{
		"request": map[string]any{
			"app_resource_name":      "bmw-staging",
			"database_resource_name": "bmw-staging-db",
			"job_image":              "registry.digitalocean.com/bmw-registry/workflow-migrate:sha",
			"source_dir":             "/migrations",
			"expected_dirty_version": "20260426000005",
			"force_version":          "20260422000001",
			"confirm_force":          interfaces.MigrationRepairConfirmation,
		},
	})
	if err != nil {
		t.Fatalf("RepairDirtyMigration dispatch should preserve result without transport error, got %v", err)
	}
	if result["provider_job_id"] != "job-running" {
		t.Fatalf("provider_job_id = %v, want job-running", result["provider_job_id"])
	}
	if result["status"] != interfaces.MigrationRepairStatusFailed {
		t.Fatalf("status = %v, want failed", result["status"])
	}
	if result["logs"] != "repair log tail" {
		t.Fatalf("logs = %v, want repair log tail", result["logs"])
	}
}

func TestDoModuleInstance_InvokeMethodContext_RepairDirtyMigration_PropagatesContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake := &fakeRepairProvider{fakeIaCProvider: fakeIaCProvider{}}
	mi := &doModuleInstance{provider: fake}

	_, err := mi.InvokeMethodContext(ctx, "IaCProvider.RepairDirtyMigration", map[string]any{
		"request": map[string]any{
			"app_resource_name":      "bmw-staging",
			"database_resource_name": "bmw-staging-db",
			"job_image":              "registry.digitalocean.com/bmw-registry/workflow-migrate:sha",
			"source_dir":             "/migrations",
			"expected_dirty_version": "20260426000005",
			"force_version":          "20260422000001",
			"confirm_force":          interfaces.MigrationRepairConfirmation,
		},
	})
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
	if fake.repairContextErr == nil {
		t.Fatal("expected canceled context to reach provider")
	}
}

// ── stub driver ───────────────────────────────────────────────────────────────

type stubResourceDriver struct {
	// call tracking
	createCalled        bool
	readCalled          bool
	updateCalled        bool
	deleteCalled        bool
	diffCalled          bool
	scaleCalled         bool
	scaleReplicas       int
	sensitiveKeysCalled bool
	healthyResult       bool
	healthMessage       string

	// args captured on last call
	lastDiffCurrent *interfaces.ResourceOutput

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
func (s *stubResourceDriver) Diff(_ context.Context, _ interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	s.diffCalled = true
	s.lastDiffCurrent = current
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

// ── fake IaCProvider ──────────────────────────────────────────────────────────

type fakeIaCProvider struct {
	// call tracking
	initializeCalled    bool
	planCalled          bool
	applyCalled         bool
	destroyCalled       bool
	statusCalled        bool
	detectDriftCalled   bool
	importCalled        bool
	resolveSizingCalled bool
	resolveSizingHints  *interfaces.ResourceHints
	bootstrapCalled     bool
	bootstrapCfg        map[string]any

	// args captured on last call (for round-trip fidelity assertions)
	lastDesired []interfaces.ResourceSpec
	lastCurrent []interfaces.ResourceState
	lastRefs    []interfaces.ResourceRef

	// return values
	planResult      *interfaces.IaCPlan
	applyResult     *interfaces.ApplyResult
	destroyResult   *interfaces.DestroyResult
	statusResult    []interfaces.ResourceStatus
	driftResult     []interfaces.DriftResult
	importResult    *interfaces.ResourceState
	sizingResult    *interfaces.ProviderSizing
	bootstrapResult *interfaces.BootstrapResult
}

func (f *fakeIaCProvider) Name() string    { return "fake" }
func (f *fakeIaCProvider) Version() string { return "0.0.0" }
func (f *fakeIaCProvider) Initialize(_ context.Context, _ map[string]any) error {
	f.initializeCalled = true
	return nil
}
func (f *fakeIaCProvider) Capabilities() []interfaces.IaCCapabilityDeclaration { return nil }
func (f *fakeIaCProvider) ResourceDriver(_ string) (interfaces.ResourceDriver, error) {
	return &stubResourceDriver{}, nil
}
func (f *fakeIaCProvider) Close() error { return nil }

func (f *fakeIaCProvider) Plan(_ context.Context, desired []interfaces.ResourceSpec, current []interfaces.ResourceState) (*interfaces.IaCPlan, error) {
	f.planCalled = true
	f.lastDesired = desired
	f.lastCurrent = current
	return f.planResult, nil
}
func (f *fakeIaCProvider) Apply(_ context.Context, _ *interfaces.IaCPlan) (*interfaces.ApplyResult, error) {
	f.applyCalled = true
	return f.applyResult, nil
}
func (f *fakeIaCProvider) Destroy(_ context.Context, refs []interfaces.ResourceRef) (*interfaces.DestroyResult, error) {
	f.destroyCalled = true
	f.lastRefs = refs
	return f.destroyResult, nil
}
func (f *fakeIaCProvider) Status(_ context.Context, _ []interfaces.ResourceRef) ([]interfaces.ResourceStatus, error) {
	f.statusCalled = true
	return f.statusResult, nil
}
func (f *fakeIaCProvider) DetectDrift(_ context.Context, _ []interfaces.ResourceRef) ([]interfaces.DriftResult, error) {
	f.detectDriftCalled = true
	return f.driftResult, nil
}
func (f *fakeIaCProvider) Import(_ context.Context, _ string, _ string) (*interfaces.ResourceState, error) {
	f.importCalled = true
	return f.importResult, nil
}
func (f *fakeIaCProvider) ResolveSizing(_ string, _ interfaces.Size, hints *interfaces.ResourceHints) (*interfaces.ProviderSizing, error) {
	f.resolveSizingCalled = true
	f.resolveSizingHints = hints
	return f.sizingResult, nil
}
func (f *fakeIaCProvider) SupportedCanonicalKeys() []string { return interfaces.CanonicalKeys() }
func (f *fakeIaCProvider) BootstrapStateBackend(_ context.Context, cfg map[string]any) (*interfaces.BootstrapResult, error) {
	f.bootstrapCalled = true
	f.bootstrapCfg = cfg
	return f.bootstrapResult, nil
}

type fakeRepairProvider struct {
	fakeIaCProvider
	repairCalled     bool
	repairReq        interfaces.MigrationRepairRequest
	repairContextErr error
	repairResult     *interfaces.MigrationRepairResult
	repairErr        error
}

func (f *fakeRepairProvider) RepairDirtyMigration(ctx context.Context, req interfaces.MigrationRepairRequest) (*interfaces.MigrationRepairResult, error) {
	f.repairCalled = true
	f.repairReq = req
	f.repairContextErr = ctx.Err()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.repairResult, f.repairErr
}
