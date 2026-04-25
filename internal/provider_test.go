package internal

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
)

// compile-time interface check
var _ interfaces.IaCProvider = (*DOProvider)(nil)

func TestDOProvider_Name(t *testing.T) {
	p := NewDOProvider()
	if p.Name() != "digitalocean" {
		t.Errorf("Name = %q, want %q", p.Name(), "digitalocean")
	}
}

func TestDOProvider_Capabilities(t *testing.T) {
	p := NewDOProvider()
	caps := p.Capabilities()
	if len(caps) == 0 {
		t.Fatal("expected non-empty capabilities")
	}
	types := make(map[string]bool)
	for _, c := range caps {
		types[c.ResourceType] = true
	}
	required := []string{
		"infra.container_service",
		"infra.k8s_cluster",
		"infra.database",
		"infra.cache",
		"infra.load_balancer",
		"infra.vpc",
		"infra.firewall",
		"infra.dns",
		"infra.storage",
		"infra.registry",
		"infra.certificate",
		"infra.droplet",
		"infra.iam_role",
		"infra.api_gateway",
	}
	for _, rt := range required {
		if !types[rt] {
			t.Errorf("missing capability: %s", rt)
		}
	}
}

func TestDOProvider_Initialize_MissingToken(t *testing.T) {
	p := NewDOProvider()
	err := p.Initialize(t.Context(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing token")
	}
}

func TestDOProvider_ResolveSizing(t *testing.T) {
	p := NewDOProvider()
	result, err := p.ResolveSizing("infra.database", interfaces.SizeM, nil)
	if err != nil {
		t.Fatalf("ResolveSizing: %v", err)
	}
	if result.InstanceType != "db-s-2vcpu-4gb" {
		t.Errorf("InstanceType = %q, want %q", result.InstanceType, "db-s-2vcpu-4gb")
	}
}

func TestDOProvider_ResolveSizing_NoopType(t *testing.T) {
	p := NewDOProvider()
	result, err := p.ResolveSizing("infra.vpc", interfaces.SizeM, nil)
	if err != nil {
		t.Fatalf("ResolveSizing vpc: %v", err)
	}
	if result.InstanceType != "n/a" {
		t.Errorf("InstanceType = %q, want n/a", result.InstanceType)
	}
}

func TestDOProvider_ResolveSizing_UnknownReturnsError(t *testing.T) {
	p := NewDOProvider()
	_, err := p.ResolveSizing("infra.unknown_thing", interfaces.SizeM, nil)
	if err == nil {
		t.Fatal("expected error for unknown resource type")
	}
}

func TestDOProvider_ResourceDriver_Unknown(t *testing.T) {
	p := NewDOProvider()
	_, err := p.ResourceDriver("infra.unknown_thing")
	if err == nil {
		t.Fatal("expected error for unknown resource type")
	}
}

func TestDOProvider_SupportedCanonicalKeys(t *testing.T) {
	p := NewDOProvider()
	keys := p.SupportedCanonicalKeys()
	if len(keys) == 0 {
		t.Fatal("SupportedCanonicalKeys returned empty slice")
	}
	keySet := make(map[string]bool, len(keys))
	for _, k := range keys {
		keySet[k] = true
	}

	// Keys the DO provider actively maps in this release (v0.7.0).
	supported := []string{
		"name", "region", "image", "http_port", "instance_count", "size",
		"env_vars", "env_vars_secret", "autoscaling", "routes", "health_check",
		"liveness_check", "cors", "protocol", "internal_ports", "build_command", "run_command",
		"dockerfile_path", "source_dir", "termination", "domains", "alerts",
		"log_destinations", "ingress", "egress", "maintenance", "vpc_ref",
		"jobs", "workers", "static_sites", "sidecars", "provider_specific",
	}
	for _, k := range supported {
		if !keySet[k] {
			t.Errorf("SupportedCanonicalKeys missing expected key %q", k)
		}
	}
}

func TestConfigHash_Deterministic(t *testing.T) {
	cfg := map[string]any{"b": 2, "a": 1, "c": "three"}
	h1 := configHash(cfg)
	h2 := configHash(cfg)
	if h1 != h2 {
		t.Errorf("configHash not deterministic: %q != %q", h1, h2)
	}
}

func TestConfigHash_DifferentConfigs(t *testing.T) {
	h1 := configHash(map[string]any{"engine": "pg", "size": "db-s-1vcpu-2gb"})
	h2 := configHash(map[string]any{"engine": "pg", "size": "db-s-2vcpu-4gb"})
	if h1 == h2 {
		t.Error("expected different hashes for different configs")
	}
}

func TestConfigHash_Empty(t *testing.T) {
	h := configHash(nil)
	if h != "" {
		t.Errorf("expected empty hash for nil config, got %q", h)
	}
}

type planDiffFakeDriver struct {
	diffResult      *interfaces.DiffResult
	diffCalls       int
	receivedSpec    interfaces.ResourceSpec
	receivedCurrent *interfaces.ResourceOutput
}

func (f *planDiffFakeDriver) Create(_ context.Context, _ interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	return nil, nil
}
func (f *planDiffFakeDriver) Read(_ context.Context, _ interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	return nil, nil
}
func (f *planDiffFakeDriver) Update(_ context.Context, _ interfaces.ResourceRef, _ interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	return nil, nil
}
func (f *planDiffFakeDriver) Delete(_ context.Context, _ interfaces.ResourceRef) error { return nil }
func (f *planDiffFakeDriver) Diff(_ context.Context, spec interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	f.diffCalls++
	f.receivedSpec = spec
	f.receivedCurrent = current
	return f.diffResult, nil
}
func (f *planDiffFakeDriver) HealthCheck(_ context.Context, _ interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	return nil, nil
}
func (f *planDiffFakeDriver) Scale(_ context.Context, _ interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return nil, nil
}
func (f *planDiffFakeDriver) SensitiveKeys() []string { return nil }

func TestDOProvider_Plan_UsesDriverDiffForExistingResource(t *testing.T) {
	spec := interfaces.ResourceSpec{
		Name: "example-dns",
		Type: "infra.dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "TXT", "name": "@", "data": "expected", "ttl": 300},
			},
		},
	}
	fake := &planDiffFakeDriver{
		diffResult: &interfaces.DiffResult{
			NeedsUpdate: true,
			Changes: []interfaces.FieldChange{
				{Path: "records[TXT/@/expected]", Old: nil, New: "expected"},
			},
		},
	}
	p := &DOProvider{drivers: map[string]interfaces.ResourceDriver{"infra.dns": fake}}

	plan, err := p.Plan(t.Context(), []interfaces.ResourceSpec{spec}, []interfaces.ResourceState{
		{
			Name:          "example-dns",
			Type:          "infra.dns",
			ProviderID:    "example.com",
			AppliedConfig: spec.Config,
			Outputs: map[string]any{
				"records": []map[string]any{
					{"type": "TXT", "name": "@", "data": "other", "ttl": 300},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if fake.diffCalls != 1 {
		t.Fatalf("Diff calls = %d, want 1", fake.diffCalls)
	}
	if fake.receivedCurrent == nil {
		t.Fatal("Diff current = nil, want reconstructed ResourceOutput")
	}
	if fake.receivedCurrent.ProviderID != "example.com" {
		t.Errorf("Diff current ProviderID = %q, want example.com", fake.receivedCurrent.ProviderID)
	}
	if len(plan.Actions) != 1 {
		t.Fatalf("plan actions = %d, want 1", len(plan.Actions))
	}
	action := plan.Actions[0]
	if action.Action != "update" {
		t.Fatalf("plan action = %q, want update", action.Action)
	}
	if len(action.Changes) != 1 || action.Changes[0].Path != "records[TXT/@/expected]" {
		t.Fatalf("plan action changes = %+v, want driver changes", action.Changes)
	}
}

func TestDOProvider_Plan_UsesReplaceForDriverNeedsReplace(t *testing.T) {
	spec := interfaces.ResourceSpec{
		Name:   "example-vpc",
		Type:   "infra.vpc",
		Config: map[string]any{"ip_range": "10.20.0.0/16"},
	}
	fake := &planDiffFakeDriver{
		diffResult: &interfaces.DiffResult{
			NeedsReplace: true,
			Changes: []interfaces.FieldChange{
				{Path: "ip_range", Old: "10.10.0.0/16", New: "10.20.0.0/16", ForceNew: true},
			},
		},
	}
	p := &DOProvider{drivers: map[string]interfaces.ResourceDriver{"infra.vpc": fake}}

	plan, err := p.Plan(t.Context(), []interfaces.ResourceSpec{spec}, []interfaces.ResourceState{
		{
			Name:          "example-vpc",
			Type:          "infra.vpc",
			ProviderID:    "vpc-123",
			AppliedConfig: map[string]any{"ip_range": "10.10.0.0/16"},
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Actions) != 1 {
		t.Fatalf("plan actions = %d, want 1", len(plan.Actions))
	}
	action := plan.Actions[0]
	if action.Action != "replace" {
		t.Fatalf("plan action = %q, want replace", action.Action)
	}
	if action.Current == nil || action.Current.ProviderID != "vpc-123" {
		t.Fatalf("plan current = %+v, want provider ID vpc-123", action.Current)
	}
	if len(action.Changes) != 1 || !action.Changes[0].ForceNew {
		t.Fatalf("plan action changes = %+v, want ForceNew change", action.Changes)
	}
}

func TestDOProvider_Plan_TreatsNoopDriverDiffAsAuthoritative(t *testing.T) {
	spec := interfaces.ResourceSpec{
		Name: "example-dns",
		Type: "infra.dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "TXT", "name": "@", "data": "expected", "ttl": 300},
			},
		},
	}
	fake := &planDiffFakeDriver{
		diffResult: &interfaces.DiffResult{NeedsUpdate: false, NeedsReplace: false},
	}
	p := &DOProvider{drivers: map[string]interfaces.ResourceDriver{"infra.dns": fake}}

	plan, err := p.Plan(t.Context(), []interfaces.ResourceSpec{spec}, []interfaces.ResourceState{
		{
			Name:       "example-dns",
			Type:       "infra.dns",
			ProviderID: "example.com",
			AppliedConfig: map[string]any{
				"domain": "example.com",
				"records": []any{
					map[string]any{"type": "TXT", "name": "@", "data": "stale", "ttl": 300},
				},
			},
			Outputs: map[string]any{
				"records": []map[string]any{
					{"type": "TXT", "name": "@", "data": "expected", "ttl": 300},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if fake.diffCalls != 1 {
		t.Fatalf("Diff calls = %d, want 1", fake.diffCalls)
	}
	if len(plan.Actions) != 0 {
		t.Fatalf("plan actions = %+v, want none from authoritative driver diff", plan.Actions)
	}
}

type replaceFakeDriver struct {
	calls     []string
	deleteRef interfaces.ResourceRef
}

func (f *replaceFakeDriver) Create(_ context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	f.calls = append(f.calls, "create")
	return &interfaces.ResourceOutput{Name: spec.Name, Type: spec.Type, ProviderID: "new-provider-id"}, nil
}
func (f *replaceFakeDriver) Read(_ context.Context, _ interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	return nil, nil
}
func (f *replaceFakeDriver) Update(_ context.Context, _ interfaces.ResourceRef, _ interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	f.calls = append(f.calls, "update")
	return nil, nil
}
func (f *replaceFakeDriver) Delete(_ context.Context, ref interfaces.ResourceRef) error {
	f.calls = append(f.calls, "delete")
	f.deleteRef = ref
	return nil
}
func (f *replaceFakeDriver) Diff(_ context.Context, _ interfaces.ResourceSpec, _ *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	return nil, nil
}
func (f *replaceFakeDriver) HealthCheck(_ context.Context, _ interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	return nil, nil
}
func (f *replaceFakeDriver) Scale(_ context.Context, _ interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return nil, nil
}
func (f *replaceFakeDriver) SensitiveKeys() []string { return nil }

func TestDOProvider_Apply_ReplaceDeletesThenCreates(t *testing.T) {
	fake := &replaceFakeDriver{}
	p := &DOProvider{drivers: map[string]interfaces.ResourceDriver{"infra.vpc": fake}}

	spec := interfaces.ResourceSpec{Name: "example-vpc", Type: "infra.vpc", Config: map[string]any{"ip_range": "10.20.0.0/16"}}
	plan := &interfaces.IaCPlan{
		ID: "plan-replace",
		Actions: []interfaces.PlanAction{{
			Action:   "replace",
			Resource: spec,
			Current:  &interfaces.ResourceState{Name: "example-vpc", Type: "infra.vpc", ProviderID: "old-provider-id"},
		}},
	}

	result, err := p.Apply(t.Context(), plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(result.Errors) > 0 {
		t.Fatalf("Apply returned errors: %v", result.Errors)
	}
	if got, want := strings.Join(fake.calls, ","), "delete,create"; got != want {
		t.Fatalf("calls = %s, want %s", got, want)
	}
	if fake.deleteRef.ProviderID != "old-provider-id" {
		t.Fatalf("delete ProviderID = %q, want old-provider-id", fake.deleteRef.ProviderID)
	}
	if len(result.Resources) != 1 || result.Resources[0].ProviderID != "new-provider-id" {
		t.Fatalf("result resources = %+v, want new provider ID", result.Resources)
	}
}

// ── fake driver for Apply upsert test ─────────────────────────────────────────

// upsertFakeDriver is a minimal ResourceDriver that:
//   - always returns ErrResourceAlreadyExists on Create
//   - returns a fixed ResourceOutput on Read (simulating discovery by name)
//   - records the ref passed to Read and Update so the test can assert the
//     upsert path was taken with the correct arguments
type upsertFakeDriver struct {
	createCalls int
	updateCalls int
	readCalls   int
	lastReadRef interfaces.ResourceRef
	updatedRef  interfaces.ResourceRef
}

func (f *upsertFakeDriver) Create(_ context.Context, _ interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	f.createCalls++
	return nil, fmt.Errorf("create conflict: %w", interfaces.ErrResourceAlreadyExists)
}

func (f *upsertFakeDriver) Read(_ context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	f.readCalls++
	f.lastReadRef = ref
	return &interfaces.ResourceOutput{
		Name:       ref.Name,
		Type:       ref.Type,
		ProviderID: "discovered-provider-id",
	}, nil
}

func (f *upsertFakeDriver) Update(_ context.Context, ref interfaces.ResourceRef, _ interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	f.updateCalls++
	f.updatedRef = ref
	return &interfaces.ResourceOutput{
		Name:       ref.Name,
		Type:       ref.Type,
		ProviderID: ref.ProviderID,
	}, nil
}

func (f *upsertFakeDriver) Delete(_ context.Context, _ interfaces.ResourceRef) error { return nil }
func (f *upsertFakeDriver) Diff(_ context.Context, _ interfaces.ResourceSpec, _ *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	return nil, nil
}
func (f *upsertFakeDriver) HealthCheck(_ context.Context, _ interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	return nil, nil
}
func (f *upsertFakeDriver) Scale(_ context.Context, _ interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return nil, nil
}
func (f *upsertFakeDriver) SensitiveKeys() []string { return nil }

// SupportsUpsert opts this fake into the upsert path, mirroring AppPlatformDriver.
func (f *upsertFakeDriver) SupportsUpsert() bool { return true }

// TestDOProvider_Apply_UpsertOnAlreadyExists verifies that when a create action
// hits ErrResourceAlreadyExists, Apply:
//  1. Gates on SupportsUpsert — only proceeds for drivers that opt in.
//  2. Calls Read (by name, empty ProviderID) to discover the existing ProviderID.
//  3. Validates existing.ProviderID is non-empty before calling Update.
//  4. Calls Update with the discovered ProviderID.
//  5. Returns the resource in ApplyResult.Resources (no errors).
func TestDOProvider_Apply_UpsertOnAlreadyExists(t *testing.T) {
	fake := &upsertFakeDriver{}
	p := &DOProvider{
		drivers: map[string]interfaces.ResourceDriver{
			"infra.container_service": fake,
		},
	}

	spec := interfaces.ResourceSpec{
		Name:   "bmw-app",
		Type:   "infra.container_service",
		Config: map[string]any{"image": "registry/bmw:latest"},
	}
	plan := &interfaces.IaCPlan{
		ID:      "plan-test",
		Actions: []interfaces.PlanAction{{Action: "create", Resource: spec}},
	}

	result, err := p.Apply(t.Context(), plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(result.Errors) > 0 {
		t.Fatalf("Apply returned errors: %v", result.Errors)
	}

	// Create was attempted once.
	if fake.createCalls != 1 {
		t.Errorf("createCalls = %d, want 1", fake.createCalls)
	}
	// Read was called with empty ProviderID (triggers name-based lookup in drivers).
	if fake.readCalls != 1 {
		t.Errorf("readCalls = %d, want 1", fake.readCalls)
	}
	if fake.lastReadRef.ProviderID != "" {
		t.Errorf("Read called with non-empty ProviderID %q; want empty for name-based lookup", fake.lastReadRef.ProviderID)
	}
	if fake.lastReadRef.Name != spec.Name || fake.lastReadRef.Type != spec.Type {
		t.Errorf("Read ref = {%q, %q}, want {%q, %q}", fake.lastReadRef.Name, fake.lastReadRef.Type, spec.Name, spec.Type)
	}
	// Update was called with the discovered ProviderID.
	if fake.updateCalls != 1 {
		t.Errorf("updateCalls = %d, want 1", fake.updateCalls)
	}
	if fake.updatedRef.ProviderID != "discovered-provider-id" {
		t.Errorf("updatedRef.ProviderID = %q, want %q", fake.updatedRef.ProviderID, "discovered-provider-id")
	}

	// Resource appears in result.
	if len(result.Resources) != 1 || result.Resources[0].Name != "bmw-app" {
		t.Errorf("result.Resources = %v, want [{bmw-app ...}]", result.Resources)
	}
}

// noUpsertFakeDriver is a ResourceDriver that returns ErrResourceAlreadyExists
// on Create but does NOT implement SupportsUpsert. It simulates drivers like
// VPC/database/firewall that require ProviderID for Read.
// SupportsUpsert is intentionally absent so it does not satisfy upsertSupporter.
type noUpsertFakeDriver struct {
	createCalls int
	readCalls   int
	updateCalls int
}

func (f *noUpsertFakeDriver) Create(_ context.Context, _ interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	f.createCalls++
	return nil, fmt.Errorf("create conflict: %w", interfaces.ErrResourceAlreadyExists)
}
func (f *noUpsertFakeDriver) Read(_ context.Context, _ interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	f.readCalls++
	return nil, nil
}
func (f *noUpsertFakeDriver) Update(_ context.Context, _ interfaces.ResourceRef, _ interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	f.updateCalls++
	return nil, nil
}
func (f *noUpsertFakeDriver) Delete(_ context.Context, _ interfaces.ResourceRef) error { return nil }
func (f *noUpsertFakeDriver) Diff(_ context.Context, _ interfaces.ResourceSpec, _ *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	return nil, nil
}
func (f *noUpsertFakeDriver) HealthCheck(_ context.Context, _ interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	return nil, nil
}
func (f *noUpsertFakeDriver) Scale(_ context.Context, _ interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return nil, nil
}
func (f *noUpsertFakeDriver) SensitiveKeys() []string { return nil }

// TestDOProvider_Apply_NoUpsertForUnsupportedDriver verifies that when a driver
// does not implement SupportsUpsert, Apply does NOT call Read or Update — it
// surfaces the original ErrResourceAlreadyExists as an action error.
func TestDOProvider_Apply_NoUpsertForUnsupportedDriver(t *testing.T) {
	fake := &noUpsertFakeDriver{}
	p := &DOProvider{
		drivers: map[string]interfaces.ResourceDriver{
			"infra.database": fake,
		},
	}

	spec := interfaces.ResourceSpec{
		Name:   "bmw-db",
		Type:   "infra.database",
		Config: map[string]any{"engine": "postgres"},
	}
	plan := &interfaces.IaCPlan{
		ID:      "plan-test",
		Actions: []interfaces.PlanAction{{Action: "create", Resource: spec}},
	}

	result, err := p.Apply(t.Context(), plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Create was attempted once before the conflict was detected.
	if fake.createCalls != 1 {
		t.Errorf("createCalls = %d, want 1", fake.createCalls)
	}
	// Apply must return an action error — upsert is not available.
	if len(result.Errors) != 1 {
		t.Fatalf("expected 1 action error, got %d: %v", len(result.Errors), result.Errors)
	}
	if !strings.Contains(result.Errors[0].Error, interfaces.ErrResourceAlreadyExists.Error()) {
		t.Errorf("action error should mention ErrResourceAlreadyExists, got: %s", result.Errors[0].Error)
	}
	// Read and Update must not have been called.
	if fake.readCalls != 0 {
		t.Errorf("readCalls = %d, want 0 (no upsert for unsupported driver)", fake.readCalls)
	}
	if fake.updateCalls != 0 {
		t.Errorf("updateCalls = %d, want 0", fake.updateCalls)
	}
}

// ── integration: upsert across all four driver types ─────────────────────────

// multiUpsertFakeDriver is a per-resource-type fake that always returns
// ErrResourceAlreadyExists on Create, implements SupportsUpsert, and records
// all call counts and the ProviderID passed to Update so assertions can verify
// the full upsert path.
type multiUpsertFakeDriver struct {
	providerID        string
	createCalls       int
	readCalls         int
	updateCalls       int
	updatedProviderID string
}

func (f *multiUpsertFakeDriver) Create(_ context.Context, _ interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	f.createCalls++
	return nil, fmt.Errorf("already exists: %w", interfaces.ErrResourceAlreadyExists)
}
func (f *multiUpsertFakeDriver) Read(_ context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	f.readCalls++
	return &interfaces.ResourceOutput{Name: ref.Name, Type: ref.Type, ProviderID: f.providerID}, nil
}
func (f *multiUpsertFakeDriver) Update(_ context.Context, ref interfaces.ResourceRef, _ interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	f.updateCalls++
	f.updatedProviderID = ref.ProviderID
	return &interfaces.ResourceOutput{Name: ref.Name, Type: ref.Type, ProviderID: ref.ProviderID}, nil
}
func (f *multiUpsertFakeDriver) Delete(_ context.Context, _ interfaces.ResourceRef) error { return nil }
func (f *multiUpsertFakeDriver) Diff(_ context.Context, _ interfaces.ResourceSpec, _ *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	return nil, nil
}
func (f *multiUpsertFakeDriver) HealthCheck(_ context.Context, _ interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	return nil, nil
}
func (f *multiUpsertFakeDriver) Scale(_ context.Context, _ interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return nil, nil
}
func (f *multiUpsertFakeDriver) SensitiveKeys() []string { return nil }
func (f *multiUpsertFakeDriver) SupportsUpsert() bool    { return true }

// TestDOProvider_Apply_UpsertAllDrivers verifies that when VPC, Firewall,
// Database, and container_service resources all pre-exist (Create returns
// ErrResourceAlreadyExists), Apply upserts every one of them: Read is called
// with empty ProviderID, then Update is called with the discovered ProviderID.
func TestDOProvider_Apply_UpsertAllDrivers(t *testing.T) {
	resources := []struct {
		rtype      string
		name       string
		providerID string
	}{
		{"infra.vpc", "bmw-vpc", "vpc-aaa"},
		{"infra.firewall", "bmw-fw", "fw-bbb"},
		{"infra.database", "bmw-db", "db-ccc"},
		{"infra.container_service", "bmw-app", "app-ddd"},
	}

	fakes := make(map[string]*multiUpsertFakeDriver, len(resources))
	driverMap := make(map[string]interfaces.ResourceDriver, len(resources))
	for _, r := range resources {
		f := &multiUpsertFakeDriver{providerID: r.providerID}
		fakes[r.rtype] = f
		driverMap[r.rtype] = f
	}

	p := &DOProvider{drivers: driverMap}

	actions := make([]interfaces.PlanAction, 0, len(resources))
	for _, r := range resources {
		actions = append(actions, interfaces.PlanAction{
			Action: "create",
			Resource: interfaces.ResourceSpec{
				Name:   r.name,
				Type:   r.rtype,
				Config: map[string]any{},
			},
		})
	}
	plan := &interfaces.IaCPlan{ID: "plan-multi", Actions: actions}

	result, err := p.Apply(t.Context(), plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(result.Errors) > 0 {
		t.Fatalf("Apply returned errors: %v", result.Errors)
	}
	if len(result.Resources) != len(resources) {
		t.Errorf("result.Resources len = %d, want %d", len(result.Resources), len(resources))
	}

	for _, r := range resources {
		f := fakes[r.rtype]
		if f.createCalls != 1 {
			t.Errorf("%s: createCalls = %d, want 1", r.rtype, f.createCalls)
		}
		if f.readCalls != 1 {
			t.Errorf("%s: readCalls = %d, want 1", r.rtype, f.readCalls)
		}
		if f.updateCalls != 1 {
			t.Errorf("%s: updateCalls = %d, want 1", r.rtype, f.updateCalls)
		}
		// Verify the discovered ProviderID from Read was propagated into Update.
		if f.updatedProviderID != r.providerID {
			t.Errorf("%s: Update called with ProviderID %q, want %q", r.rtype, f.updatedProviderID, r.providerID)
		}
	}
}
