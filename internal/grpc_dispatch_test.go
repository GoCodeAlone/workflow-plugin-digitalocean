package internal

// grpc_dispatch_test.go exercises InvokeMethod with args that have been
// round-tripped through the gRPC structpb encoding/decoding boundary. This
// simulates exactly what happens when wfctl calls InvokeService on the plugin:
//
//  1. Client encodes:  mapToStruct(args) → structpb.Struct
//  2. gRPC transport:  structpb.Struct → wire → structpb.Struct
//  3. Server decodes:  structToMap(req.Args) → map[string]any
//
// Testing through this boundary catches arg-decode bugs such as the v0.7.5
// BootstrapStateBackend regression, where the dispatch handler was reading
// args["cfg"] instead of using args directly — the "cfg" wrapper key does not
// exist in the encoded representation because wfctl sends cfg as a flat map.
//
// Every test that would have caught the v0.7.5 class of bug is marked with a
// comment starting "REGRESSION(v0.7.5)".

import (
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
	"google.golang.org/protobuf/types/known/structpb"
)

// grpcRoundTrip encodes args through structpb.NewStruct then decodes via
// .AsMap(), reproducing the exact type coercions at the gRPC InvokeService
// boundary:
//   - All integer types (int, int32, int64, …) → float64 after round-trip
//   - Slices must be []any; native typed slices ([]string, []int, etc.) are
//     rejected by structpb.NewStruct with "proto: invalid type". Convert them
//     to []any before calling this helper.
//   - Nested map[string]any is preserved as map[string]any
//   - nil → nil  (nil *structpb.Struct → structToMap(nil) → nil)
func grpcRoundTrip(t *testing.T, args map[string]any) map[string]any {
	t.Helper()
	if args == nil {
		return nil
	}
	s, err := structpb.NewStruct(args)
	if err != nil {
		t.Fatalf("grpcRoundTrip: structpb.NewStruct failed: %v (check that all values are structpb-compatible)", err)
	}
	return s.AsMap()
}

// ── BootstrapStateBackend regression (v0.7.5 class of bug) ───────────────────

// TestGRPCDispatch_BootstrapStateBackend_FlatArgsRegression is the canonical
// regression test for the v0.7.5 bug class. wfctl sends cfg as a flat map
// directly (no "cfg" wrapper key). After the structpb round-trip the map is
// still flat. A handler that reads args["cfg"] receives nil and silently drops
// all configuration. This test asserts the provider receives the top-level keys.
//
// REGRESSION(v0.7.5): would have failed with the old args["cfg"] unwrap.
func TestGRPCDispatch_BootstrapStateBackend_FlatArgsRegression(t *testing.T) {
	fake := &fakeIaCProvider{
		bootstrapResult: &interfaces.BootstrapResult{
			Bucket:   "bmw-state",
			Region:   "nyc3",
			Endpoint: "https://nyc3.digitaloceanspaces.com",
			EnvVars:  map[string]string{"WFCTL_STATE_BUCKET": "bmw-state"},
		},
	}
	mi := &doModuleInstance{provider: fake}

	// REGRESSION(v0.7.5): these top-level keys must survive the round-trip and
	// reach the provider without being re-wrapped under any "cfg" key.
	args := grpcRoundTrip(t, map[string]any{
		"bucket":    "bmw-state",
		"region":    "nyc3",
		"accessKey": "test-access-key",
		"secretKey": "test-secret-key",
	})

	result, err := mi.InvokeMethod("IaCProvider.BootstrapStateBackend", args)
	if err != nil {
		t.Fatalf("BootstrapStateBackend: unexpected error: %v", err)
	}
	if !fake.bootstrapCalled {
		t.Fatal("BootstrapStateBackend was not called on the provider")
	}
	// REGRESSION(v0.7.5): if handler did args["cfg"], cfg would be nil here.
	if fake.bootstrapCfg == nil {
		t.Fatal("provider received nil cfg — gRPC round-trip lost all config (args not passed flat)")
	}
	if fake.bootstrapCfg["bucket"] != "bmw-state" {
		t.Errorf("provider cfg[bucket] = %q, want %q — top-level key dropped after gRPC encoding",
			fake.bootstrapCfg["bucket"], "bmw-state")
	}
	if fake.bootstrapCfg["region"] != "nyc3" {
		t.Errorf("provider cfg[region] = %q, want %q", fake.bootstrapCfg["region"], "nyc3")
	}
	if result["bucket"] != "bmw-state" {
		t.Errorf("result[bucket] = %q, want %q", result["bucket"], "bmw-state")
	}
}

// TestGRPCDispatch_BootstrapStateBackend_NilArgsViaGRPC verifies that nil args
// (nil structpb.Struct → structToMap returns nil) are handled without panic and
// the provider is still called with an empty map.
func TestGRPCDispatch_BootstrapStateBackend_NilArgsViaGRPC(t *testing.T) {
	fake := &fakeIaCProvider{
		bootstrapResult: &interfaces.BootstrapResult{Bucket: "default"},
	}
	mi := &doModuleInstance{provider: fake}

	result, err := mi.InvokeMethod("IaCProvider.BootstrapStateBackend", grpcRoundTrip(t, nil))
	if err != nil {
		t.Fatalf("nil args must not error: %v", err)
	}
	if !fake.bootstrapCalled {
		t.Error("provider must be called even with nil args")
	}
	if result == nil {
		t.Error("expected non-nil result map")
	}
}

// TestGRPCDispatch_BootstrapStateBackend_EmptyArgsViaGRPC verifies that an empty
// args map (no keys) is handled without panic at the dispatch layer. The real
// provider would return an error for missing credentials, but the dispatch must
// pass the empty map through (not nil) to the provider.
func TestGRPCDispatch_BootstrapStateBackend_EmptyArgsViaGRPC(t *testing.T) {
	fake := &fakeIaCProvider{bootstrapResult: &interfaces.BootstrapResult{}}
	mi := &doModuleInstance{provider: fake}

	_, err := mi.InvokeMethod("IaCProvider.BootstrapStateBackend", grpcRoundTrip(t, map[string]any{}))
	if err != nil {
		t.Fatalf("empty args must not error at dispatch layer: %v", err)
	}
	if !fake.bootstrapCalled {
		t.Error("provider must be called even with empty args")
	}
	// Empty map → provider receives empty map (not nil).
	if fake.bootstrapCfg == nil {
		t.Error("provider received nil cfg for empty args — want empty map")
	}
}

// TestGRPCDispatch_BootstrapStateBackend_PartialArgs verifies that a partial
// args map (bucket present, credentials absent) is dispatched without panic.
// The real DOProvider will return an error for missing credentials; the test
// only checks that the dispatch layer does not panic and the error is clean.
func TestGRPCDispatch_BootstrapStateBackend_PartialArgs(t *testing.T) {
	p := NewDOProvider()
	p.bootstrapClientFactory = func(_, _, _ string) spacesBucketClient {
		return &fakeBucketClient{}
	}
	mi := &doModuleInstance{provider: p}

	args := grpcRoundTrip(t, map[string]any{
		"bucket": "my-bucket",
		// Missing accessKey and secretKey — provider returns error, not panic.
	})

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("panic with partial args: %v", r)
		}
	}()
	// Expect an error (missing credentials) but no panic.
	_, _ = mi.InvokeMethod("IaCProvider.BootstrapStateBackend", args)
}

// ── Type coercion tests ───────────────────────────────────────────────────────

// TestGRPCDispatch_Scale_IntReplicasBecomesFloat64 verifies that replicas sent
// as int by the caller arrive as float64 after the structpb round-trip, and
// that intArg correctly handles the float64 form.
func TestGRPCDispatch_Scale_IntReplicasBecomesFloat64(t *testing.T) {
	stub := &stubResourceDriver{scaleOutput: &interfaces.ResourceOutput{Status: "scaling"}}
	provider := &DOProvider{
		drivers: map[string]interfaces.ResourceDriver{"infra.k8s_cluster": stub},
	}
	mi := &doModuleInstance{provider: provider}

	// grpcRoundTrip converts replicas: 5 (int) → 5 (float64).
	args := grpcRoundTrip(t, map[string]any{
		"resource_type":   "infra.k8s_cluster",
		"ref_name":        "my-cluster",
		"ref_type":        "infra.k8s_cluster",
		"ref_provider_id": "k8s-uuid-111",
		"replicas":        5,
	})

	_, err := mi.InvokeMethod("ResourceDriver.Scale", args)
	if err != nil {
		t.Fatalf("Scale with int replicas through gRPC round-trip: %v", err)
	}
	if !stub.scaleCalled {
		t.Error("Scale was not called on the driver")
	}
	if stub.scaleReplicas != 5 {
		t.Errorf("replicas = %d, want 5 (intArg must accept float64 from gRPC round-trip)", stub.scaleReplicas)
	}
}

// TestGRPCDispatch_Diff_CurrentSensitiveAsMapAny verifies that current_sensitive
// arrives as map[string]any{bool} after the structpb round-trip (not the native
// map[string]bool) and is correctly decoded by currentFromArgs.
func TestGRPCDispatch_Diff_CurrentSensitiveAsMapAny(t *testing.T) {
	stub := &stubResourceDriver{diffOutput: &interfaces.DiffResult{NeedsUpdate: false}}
	provider := &DOProvider{
		drivers: map[string]interfaces.ResourceDriver{"infra.database": stub},
	}
	mi := &doModuleInstance{provider: provider}

	args := grpcRoundTrip(t, map[string]any{
		"resource_type":       "infra.database",
		"spec_name":           "my-db",
		"spec_type":           "infra.database",
		"spec_config":         map[string]any{},
		"current_provider_id": "do-db-uuid",
		"current_name":        "my-db",
		"current_type":        "infra.database",
		"current_status":      "running",
		// structpb encodes bool values; map[string]bool → map[string]any{bool} after round-trip.
		"current_sensitive": map[string]any{"password": true, "api_key": false},
	})

	_, err := mi.InvokeMethod("ResourceDriver.Diff", args)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if stub.lastDiffCurrent == nil {
		t.Fatal("expected non-nil current in Diff call")
	}
	if !stub.lastDiffCurrent.Sensitive["password"] {
		t.Errorf("Sensitive[password] = false, want true after gRPC round-trip")
	}
	if stub.lastDiffCurrent.Sensitive["api_key"] {
		t.Errorf("Sensitive[api_key] = true, want false after gRPC round-trip")
	}
}

// ── Nil-args safety matrix ────────────────────────────────────────────────────

// TestGRPCDispatch_NilArgs_NoPanic verifies that every InvokeMethod case
// handles nil args without panicking. A nil args map simulates a gRPC client
// that sent a request with no Args field set (nil structpb.Struct).
// Errors are acceptable; panics are not.
func TestGRPCDispatch_NilArgs_NoPanic(t *testing.T) {
	stub := &stubResourceDriver{}
	fake := &fakeIaCProvider{
		planResult:      &interfaces.IaCPlan{},
		applyResult:     &interfaces.ApplyResult{},
		destroyResult:   &interfaces.DestroyResult{},
		sizingResult:    &interfaces.ProviderSizing{InstanceType: "xs"},
		bootstrapResult: &interfaces.BootstrapResult{},
	}
	repairFake := &fakeRepairProvider{fakeIaCProvider: *fake}
	provider := &DOProvider{
		drivers: map[string]interfaces.ResourceDriver{"infra.droplet": stub},
	}
	miProvider := &doModuleInstance{provider: fake}
	miRepairProvider := &doModuleInstance{provider: repairFake}
	miDrivers := &doModuleInstance{provider: provider}

	cases := []struct {
		inst   *doModuleInstance
		method string
	}{
		// IaCProvider.Initialize is intentionally absent here — it is a no-op by
		// design (see TestGRPCDispatch_Initialize_IsNoOp_ByDesign) and would always
		// pass regardless of args, making it vacuous coverage.
		{miProvider, "IaCProvider.Name"},
		{miProvider, "IaCProvider.Version"},
		{miProvider, "IaCProvider.Capabilities"},
		{miProvider, "IaCProvider.Plan"},
		{miProvider, "IaCProvider.Apply"},
		{miProvider, "IaCProvider.Destroy"},
		{miProvider, "IaCProvider.Status"},
		{miProvider, "IaCProvider.DetectDrift"},
		{miProvider, "IaCProvider.Import"},
		{miProvider, "IaCProvider.ResolveSizing"},
		{miProvider, "IaCProvider.BootstrapStateBackend"},
		{miRepairProvider, "IaCProvider.RepairDirtyMigration"},
		// Driver methods with nil args → resource_type missing → error (not panic).
		{miDrivers, "ResourceDriver.Create"},
		{miDrivers, "ResourceDriver.Read"},
		{miDrivers, "ResourceDriver.Update"},
		{miDrivers, "ResourceDriver.Delete"},
		{miDrivers, "ResourceDriver.Diff"},
		{miDrivers, "ResourceDriver.Scale"},
		{miDrivers, "ResourceDriver.HealthCheck"},
		{miDrivers, "ResourceDriver.SensitiveKeys"},
		{miDrivers, "ResourceDriver.Troubleshoot"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.method, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panic with nil args: %v", r)
				}
			}()
			tc.inst.InvokeMethod(tc.method, nil) //nolint:errcheck // panic-check only
		})
	}
}

// TestGRPCDispatch_EmptyArgs_NoPanic verifies that every InvokeMethod case
// handles an empty {} args map (after structpb round-trip) without panicking.
// An empty args map simulates a gRPC client that sent a request with an empty
// structpb.Struct. Errors are acceptable; panics are not.
func TestGRPCDispatch_EmptyArgs_NoPanic(t *testing.T) {
	stub := &stubResourceDriver{}
	fake := &fakeIaCProvider{
		planResult:      &interfaces.IaCPlan{},
		applyResult:     &interfaces.ApplyResult{},
		destroyResult:   &interfaces.DestroyResult{},
		sizingResult:    &interfaces.ProviderSizing{InstanceType: "xs"},
		bootstrapResult: &interfaces.BootstrapResult{},
	}
	repairFake := &fakeRepairProvider{fakeIaCProvider: *fake}
	provider := &DOProvider{
		drivers: map[string]interfaces.ResourceDriver{"infra.droplet": stub},
	}
	miProvider := &doModuleInstance{provider: fake}
	miRepairProvider := &doModuleInstance{provider: repairFake}
	miDrivers := &doModuleInstance{provider: provider}

	cases := []struct {
		inst   *doModuleInstance
		method string
	}{
		// IaCProvider.Initialize is intentionally absent — see nil-args matrix comment.
		{miProvider, "IaCProvider.Name"},
		{miProvider, "IaCProvider.Version"},
		{miProvider, "IaCProvider.Capabilities"},
		{miProvider, "IaCProvider.Plan"},
		{miProvider, "IaCProvider.Apply"},
		{miProvider, "IaCProvider.Destroy"},
		{miProvider, "IaCProvider.Status"},
		{miProvider, "IaCProvider.DetectDrift"},
		{miProvider, "IaCProvider.Import"},
		{miProvider, "IaCProvider.ResolveSizing"},
		{miProvider, "IaCProvider.BootstrapStateBackend"},
		{miRepairProvider, "IaCProvider.RepairDirtyMigration"},
		{miDrivers, "ResourceDriver.Create"},
		{miDrivers, "ResourceDriver.Read"},
		{miDrivers, "ResourceDriver.Update"},
		{miDrivers, "ResourceDriver.Delete"},
		{miDrivers, "ResourceDriver.Diff"},
		{miDrivers, "ResourceDriver.Scale"},
		{miDrivers, "ResourceDriver.HealthCheck"},
		{miDrivers, "ResourceDriver.SensitiveKeys"},
		{miDrivers, "ResourceDriver.Troubleshoot"},
	}

	empty := grpcRoundTrip(t, map[string]any{})
	for _, tc := range cases {
		tc := tc
		t.Run(tc.method, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panic with empty args: %v", r)
				}
			}()
			tc.inst.InvokeMethod(tc.method, empty) //nolint:errcheck // panic-check only
		})
	}
}

// ── Full round-trip populated-args tests ─────────────────────────────────────

// TestGRPCDispatch_ResourceDriver_Create_PopulatedArgs exercises
// ResourceDriver.Create with fully-populated args through the gRPC round-trip,
// verifying that all fields survive structpb encoding and are correctly decoded.
func TestGRPCDispatch_ResourceDriver_Create_PopulatedArgs(t *testing.T) {
	stub := &stubResourceDriver{createOutput: &interfaces.ResourceOutput{
		ProviderID: "do-123",
		Name:       "my-app",
		Type:       "infra.container_service",
		Status:     "active",
	}}
	provider := &DOProvider{
		drivers: map[string]interfaces.ResourceDriver{"infra.container_service": stub},
	}
	mi := &doModuleInstance{provider: provider}

	args := grpcRoundTrip(t, map[string]any{
		"resource_type": "infra.container_service",
		"spec_name":     "my-app",
		"spec_type":     "infra.container_service",
		"spec_config": map[string]any{
			"image":          "registry.example.com/myapp:v1",
			"instance_count": 2, // becomes float64(2) after round-trip — driver must tolerate
			"region":         "nyc3",
		},
	})

	result, err := mi.InvokeMethod("ResourceDriver.Create", args)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !stub.createCalled {
		t.Error("Create was not called on the driver")
	}
	if result["provider_id"] != "do-123" {
		t.Errorf("provider_id = %q, want %q", result["provider_id"], "do-123")
	}
	if result["status"] != "active" {
		t.Errorf("status = %q, want %q", result["status"], "active")
	}
}

// TestGRPCDispatch_IaCProvider_Plan_PopulatedArgs exercises IaCProvider.Plan
// with both desired and current specs through the gRPC round-trip.
func TestGRPCDispatch_IaCProvider_Plan_PopulatedArgs(t *testing.T) {
	fake := &fakeIaCProvider{planResult: &interfaces.IaCPlan{
		ID: "plan-xyz",
		Actions: []interfaces.PlanAction{
			{Action: "create", Resource: interfaces.ResourceSpec{Name: "my-app", Type: "infra.container_service"}},
		},
	}}
	mi := &doModuleInstance{provider: fake}

	args := grpcRoundTrip(t, map[string]any{
		"desired": []any{
			map[string]any{
				"name":   "my-app",
				"type":   "infra.container_service",
				"config": map[string]any{"image": "myapp:v1"},
			},
		},
		"current": []any{
			map[string]any{
				"name":        "old-app",
				"type":        "infra.container_service",
				"provider_id": "do-old-123",
			},
		},
	})

	result, err := mi.InvokeMethod("IaCProvider.Plan", args)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !fake.planCalled {
		t.Error("Plan was not called on the provider")
	}
	if result["id"] != "plan-xyz" {
		t.Errorf("plan id = %q, want %q", result["id"], "plan-xyz")
	}
	actions, _ := result["actions"].([]any)
	if len(actions) != 1 {
		t.Errorf("expected 1 action, got %d", len(actions))
	}
}

// TestGRPCDispatch_IaCProvider_Destroy_PopulatedRefs exercises IaCProvider.Destroy
// with a refs list through the gRPC round-trip to verify the JSON decode path
// (decodeJSONField) correctly reconstructs []interfaces.ResourceRef from the
// structpb-decoded []any of nested maps.
func TestGRPCDispatch_IaCProvider_Destroy_PopulatedRefs(t *testing.T) {
	fake := &fakeIaCProvider{destroyResult: &interfaces.DestroyResult{
		Destroyed: []string{"my-db", "my-cache"},
	}}
	mi := &doModuleInstance{provider: fake}

	args := grpcRoundTrip(t, map[string]any{
		"refs": []any{
			map[string]any{"name": "my-db", "type": "infra.database", "provider_id": "do-db-1"},
			map[string]any{"name": "my-cache", "type": "infra.cache", "provider_id": "do-cache-1"},
		},
	})

	result, err := mi.InvokeMethod("IaCProvider.Destroy", args)
	if err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if !fake.destroyCalled {
		t.Error("Destroy was not called on the provider")
	}
	destroyed, _ := result["destroyed"].([]any)
	if len(destroyed) != 2 {
		t.Errorf("expected 2 destroyed, got %d", len(destroyed))
	}
}

// TestGRPCDispatch_ResourceDriver_Update_PopulatedArgs exercises
// ResourceDriver.Update through the full gRPC round-trip with ref, spec, and
// spec_config all populated.
func TestGRPCDispatch_ResourceDriver_Update_PopulatedArgs(t *testing.T) {
	stub := &stubResourceDriver{}
	provider := &DOProvider{
		drivers: map[string]interfaces.ResourceDriver{"infra.container_service": stub},
	}
	mi := &doModuleInstance{provider: provider}

	args := grpcRoundTrip(t, map[string]any{
		"resource_type":   "infra.container_service",
		"ref_name":        "my-app",
		"ref_type":        "infra.container_service",
		"ref_provider_id": "do-app-uuid",
		"spec_name":       "my-app",
		"spec_type":       "infra.container_service",
		"spec_config": map[string]any{
			"image":  "registry.example.com/myapp:v2",
			"region": "nyc3",
		},
	})

	_, err := mi.InvokeMethod("ResourceDriver.Update", args)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !stub.updateCalled {
		t.Error("Update was not called on the driver")
	}
}

// TestGRPCDispatch_ResourceDriver_HealthCheck_PopulatedArgs exercises
// ResourceDriver.HealthCheck through the full gRPC round-trip.
func TestGRPCDispatch_ResourceDriver_HealthCheck_PopulatedArgs(t *testing.T) {
	stub := &stubResourceDriver{healthyResult: true, healthMessage: "active"}
	provider := &DOProvider{
		drivers: map[string]interfaces.ResourceDriver{"infra.droplet": stub},
	}
	mi := &doModuleInstance{provider: provider}

	args := grpcRoundTrip(t, map[string]any{
		"resource_type":   "infra.droplet",
		"ref_name":        "my-droplet",
		"ref_type":        "infra.droplet",
		"ref_provider_id": "12345678",
	})

	result, err := mi.InvokeMethod("ResourceDriver.HealthCheck", args)
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if result["healthy"] != true {
		t.Errorf("healthy = %v, want true", result["healthy"])
	}
	if result["message"] != "active" {
		t.Errorf("message = %q, want %q", result["message"], "active")
	}
}

// ── Fix 1: Initialize no-op contract ─────────────────────────────────────────

// TestGRPCDispatch_Initialize_IsNoOp_ByDesign documents that the dispatch
// handler for IaCProvider.Initialize is a hardcoded no-op (module_instance.go).
// The provider's own Initialize method is intentionally never called via this
// path — the plugin is already initialized during CreateModule. Any args are
// accepted and silently ignored.
//
// This test is deliberately absent from the nil/empty-args matrices because
// those tests exist to catch dispatch-layer panics; an always-passing case adds
// no signal. This standalone test makes the no-op contract explicit so future
// authors understand why Initialize is excluded from the matrices.
func TestGRPCDispatch_Initialize_IsNoOp_ByDesign(t *testing.T) {
	fake := &fakeIaCProvider{}
	mi := &doModuleInstance{provider: fake}

	// Any args — including fully-populated ones — must be accepted without error.
	result, err := mi.InvokeMethod("IaCProvider.Initialize", grpcRoundTrip(t, map[string]any{
		"token":  "secret",
		"region": "nyc3",
	}))
	if err != nil {
		t.Fatalf("Initialize: unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Initialize: expected non-nil result map")
	}
	// The provider's Initialize must NOT be called — the dispatch is a no-op.
	if fake.initializeCalled {
		t.Error("provider.Initialize was called; dispatch must be a no-op (plugin initialised in CreateModule)")
	}
}

// ── Fix 2: grpcRoundTrip contract — []string rejection ───────────────────────

// TestGRPCDispatch_TypedSliceRejectedByStructpb documents the documented
// constraint in grpcRoundTrip: native typed slices ([]string, []int, etc.) are
// rejected by structpb.NewStruct. Callers must convert to []any first.
// This test exists so the contract is machine-verified, not just commented.
func TestGRPCDispatch_TypedSliceRejectedByStructpb(t *testing.T) {
	_, err := structpb.NewStruct(map[string]any{
		"strs": []string{"a", "b"},
	})
	if err == nil {
		t.Fatal("structpb.NewStruct accepted []string — contract violated; update grpcRoundTrip docstring")
	}
	// Valid: []any wrapping the same strings passes.
	_, err = structpb.NewStruct(map[string]any{
		"strs": []any{"a", "b"},
	})
	if err != nil {
		t.Fatalf("structpb.NewStruct rejected []any{string}: %v", err)
	}
}

// ── Fix 3: currentFromArgs map[string]bool branch + malformed-sensitive ──────

// TestGRPCDispatch_Diff_CurrentSensitive_NativeMapBool documents that the
// map[string]bool branch in currentFromArgs is dead across the gRPC boundary
// (structpb.AsMap() only produces map[string]any), but is reachable for
// in-process callers who pass a native Go ResourceOutput. This test exercises
// that in-process path directly, without a structpb round-trip.
func TestGRPCDispatch_Diff_CurrentSensitive_NativeMapBool(t *testing.T) {
	stub := &stubResourceDriver{diffOutput: &interfaces.DiffResult{NeedsUpdate: false}}
	provider := &DOProvider{
		drivers: map[string]interfaces.ResourceDriver{"infra.database": stub},
	}
	mi := &doModuleInstance{provider: provider}

	// Bypass grpcRoundTrip — pass native map[string]bool directly to exercise
	// the in-process branch (NOT reachable via gRPC; structpb always yields map[string]any).
	args := map[string]any{
		"resource_type":       "infra.database",
		"spec_name":           "my-db",
		"spec_type":           "infra.database",
		"spec_config":         map[string]any{},
		"current_provider_id": "do-db-uuid",
		"current_name":        "my-db",
		"current_type":        "infra.database",
		"current_status":      "running",
		// Native map[string]bool — only reachable in-process, not via gRPC.
		"current_sensitive": map[string]bool{"password": true, "api_key": false},
	}

	_, err := mi.InvokeMethod("ResourceDriver.Diff", args)
	if err != nil {
		t.Fatalf("Diff with native map[string]bool: %v", err)
	}
	if stub.lastDiffCurrent == nil {
		t.Fatal("expected non-nil current in Diff call")
	}
	if !stub.lastDiffCurrent.Sensitive["password"] {
		t.Errorf("Sensitive[password] = false, want true (native map[string]bool branch)")
	}
}

// TestGRPCDispatch_Diff_CurrentSensitive_MalformedValues verifies that when
// current_sensitive arrives with non-bool values (e.g. string "true") after the
// gRPC boundary, the map[string]any decode arm silently drops those entries
// (only bool values are accepted). This documents the known silent-drop behaviour
// so callers are aware it is intentional rather than a latent bug.
func TestGRPCDispatch_Diff_CurrentSensitive_MalformedValues(t *testing.T) {
	stub := &stubResourceDriver{diffOutput: &interfaces.DiffResult{}}
	provider := &DOProvider{
		drivers: map[string]interfaces.ResourceDriver{"infra.database": stub},
	}
	mi := &doModuleInstance{provider: provider}

	// Non-bool value under current_sensitive (e.g. a string "true") arrives from
	// a misbehaving client. After the decode loop, that key is silently dropped.
	args := map[string]any{
		"resource_type":       "infra.database",
		"spec_name":           "my-db",
		"spec_type":           "infra.database",
		"spec_config":         map[string]any{},
		"current_provider_id": "do-db-uuid",
		"current_name":        "my-db",
		"current_type":        "infra.database",
		"current_status":      "running",
		"current_sensitive":   map[string]any{"password": "true"}, // string, not bool
	}

	_, err := mi.InvokeMethod("ResourceDriver.Diff", args)
	if err != nil {
		t.Fatalf("Diff with malformed sensitive: %v", err)
	}
	if stub.lastDiffCurrent == nil {
		t.Fatal("expected non-nil current")
	}
	// Malformed entry silently dropped — Sensitive map is nil (len == 0 guard).
	if len(stub.lastDiffCurrent.Sensitive) != 0 {
		t.Errorf("Sensitive = %v, want empty (malformed non-bool value must be dropped silently)",
			stub.lastDiffCurrent.Sensitive)
	}
}

// ── Fix 4: Plan + Destroy round-trip fidelity ─────────────────────────────────

// TestGRPCDispatch_IaCProvider_Plan_RoundTripFidelity verifies that desired and
// current slices survive structpb encoding and are correctly decoded before being
// forwarded to the provider. The fake captures lastDesired/lastCurrent so we can
// assert post-decode shape, not just that the hardcoded return value came back.
func TestGRPCDispatch_IaCProvider_Plan_RoundTripFidelity(t *testing.T) {
	fake := &fakeIaCProvider{planResult: &interfaces.IaCPlan{ID: "plan-rt"}}
	mi := &doModuleInstance{provider: fake}

	args := grpcRoundTrip(t, map[string]any{
		"desired": []any{
			map[string]any{
				"name":   "my-app",
				"type":   "infra.container_service",
				"config": map[string]any{"image": "myapp:v1"},
			},
		},
		"current": []any{
			map[string]any{
				"name":        "old-app",
				"type":        "infra.container_service",
				"provider_id": "do-old-123",
				"status":      "running",
			},
		},
	})

	_, err := mi.InvokeMethod("IaCProvider.Plan", args)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(fake.lastDesired) != 1 {
		t.Fatalf("lastDesired len = %d, want 1", len(fake.lastDesired))
	}
	if fake.lastDesired[0].Name != "my-app" {
		t.Errorf("lastDesired[0].Name = %q, want %q", fake.lastDesired[0].Name, "my-app")
	}
	if fake.lastDesired[0].Type != "infra.container_service" {
		t.Errorf("lastDesired[0].Type = %q, want %q", fake.lastDesired[0].Type, "infra.container_service")
	}
	if len(fake.lastCurrent) != 1 {
		t.Fatalf("lastCurrent len = %d, want 1", len(fake.lastCurrent))
	}
	if fake.lastCurrent[0].Name != "old-app" {
		t.Errorf("lastCurrent[0].Name = %q, want %q", fake.lastCurrent[0].Name, "old-app")
	}
}

// TestGRPCDispatch_IaCProvider_Destroy_RoundTripFidelity verifies that refs
// survive the structpb round-trip and are correctly decoded before being
// forwarded to the provider. The fake captures lastRefs for post-decode shape
// assertions.
func TestGRPCDispatch_IaCProvider_Destroy_RoundTripFidelity(t *testing.T) {
	fake := &fakeIaCProvider{destroyResult: &interfaces.DestroyResult{
		Destroyed: []string{"my-db", "my-cache"},
	}}
	mi := &doModuleInstance{provider: fake}

	args := grpcRoundTrip(t, map[string]any{
		"refs": []any{
			map[string]any{"name": "my-db", "type": "infra.database", "provider_id": "do-db-1"},
			map[string]any{"name": "my-cache", "type": "infra.cache", "provider_id": "do-cache-1"},
		},
	})

	_, err := mi.InvokeMethod("IaCProvider.Destroy", args)
	if err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if len(fake.lastRefs) != 2 {
		t.Fatalf("lastRefs len = %d, want 2", len(fake.lastRefs))
	}
	if fake.lastRefs[0].Name != "my-db" {
		t.Errorf("lastRefs[0].Name = %q, want %q", fake.lastRefs[0].Name, "my-db")
	}
	if fake.lastRefs[0].ProviderID != "do-db-1" {
		t.Errorf("lastRefs[0].ProviderID = %q, want %q", fake.lastRefs[0].ProviderID, "do-db-1")
	}
	if fake.lastRefs[1].Name != "my-cache" {
		t.Errorf("lastRefs[1].Name = %q, want %q", fake.lastRefs[1].Name, "my-cache")
	}
}
