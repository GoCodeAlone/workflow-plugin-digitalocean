package internal

import (
	"context"
	"fmt"
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

// TestDOProvider_Apply_UpsertOnAlreadyExists verifies that when a create action
// hits ErrResourceAlreadyExists, Apply:
//  1. Calls Read (by name, empty ProviderID) to discover the existing ProviderID.
//  2. Calls Update with the discovered ProviderID.
//  3. Returns the resource in ApplyResult.Resources (no errors).
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
