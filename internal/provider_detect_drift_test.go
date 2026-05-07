package internal

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
)

// fakeDriverForDrift implements interfaces.ResourceDriver with configurable
// Read and Diff behavior for DetectDrift / DetectDriftWithSpecs unit tests.
// No live API calls are made.
//
// When diffResult and diffErr are both nil (the zero value), Diff panics to
// ensure tests that should NOT call Diff fail loudly if they accidentally do.
// Set at least one of diffResult or diffErr to opt in to Diff support.
type fakeDriverForDrift struct {
	readErr    error
	readOutput *interfaces.ResourceOutput

	// diffResult / diffErr control Diff's return value.
	// If both are nil, Diff panics (guards tests that must not call Diff).
	diffResult *interfaces.DiffResult
	diffErr    error
	// diffCalled records whether Diff was invoked.
	diffCalled bool
}

func (f *fakeDriverForDrift) Read(_ context.Context, _ interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	return f.readOutput, f.readErr
}

func (f *fakeDriverForDrift) Diff(_ context.Context, _ interfaces.ResourceSpec, _ *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	if f.diffResult == nil && f.diffErr == nil {
		// DetectDrift must never call Diff when no spec is provided. If this
		// method is called during a no-spec DetectDrift test, panic so the test
		// fails with a clear message.
		panic("fakeDriverForDrift.Diff called — DetectDrift must not call Diff when no spec is provided (empty-spec causes false positives)")
	}
	f.diffCalled = true
	return f.diffResult, f.diffErr
}

// Minimal stubs for the remaining ResourceDriver methods.
func (f *fakeDriverForDrift) Create(_ context.Context, _ interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	return nil, nil
}
func (f *fakeDriverForDrift) Update(_ context.Context, _ interfaces.ResourceRef, _ interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	return nil, nil
}
func (f *fakeDriverForDrift) Delete(_ context.Context, _ interfaces.ResourceRef) error { return nil }
func (f *fakeDriverForDrift) HealthCheck(_ context.Context, _ interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	return nil, nil
}
func (f *fakeDriverForDrift) Scale(_ context.Context, _ interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return nil, nil
}
func (f *fakeDriverForDrift) SensitiveKeys() []string { return nil }

// newProviderWithFakeDriver builds a minimal DOProvider with one fake driver
// registered for the given type. Tests should not call Initialize.
func newProviderWithFakeDriver(resourceType string, d *fakeDriverForDrift) *DOProvider {
	return &DOProvider{
		drivers: map[string]interfaces.ResourceDriver{
			resourceType: d,
		},
	}
}

// TestDetectDrift_NotFoundReturnsGhost verifies that when a driver's Read
// returns errors.Is(err, interfaces.ErrResourceNotFound), DetectDrift classifies
// the result as DriftClassGhost with Drifted=true.
func TestDetectDrift_NotFoundReturnsGhost(t *testing.T) {
	p := newProviderWithFakeDriver("infra.vpc", &fakeDriverForDrift{
		readErr: fmt.Errorf("vpc %q: %w", "test-vpc", interfaces.ErrResourceNotFound),
	})
	refs := []interfaces.ResourceRef{{Name: "test-vpc", Type: "infra.vpc"}}

	results, err := p.DetectDrift(context.Background(), refs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if !r.Drifted {
		t.Errorf("expected Drifted=true for ghost-in-state, got false")
	}
	if r.Class != interfaces.DriftClassGhost {
		t.Errorf("expected Class=%q, got %q", interfaces.DriftClassGhost, r.Class)
	}
	if r.Name != "test-vpc" {
		t.Errorf("expected Name=test-vpc, got %q", r.Name)
	}
}

// TestDetectDrift_ReadOkReturnsInSync verifies that when Read succeeds, the result
// is classified as DriftClassInSync with Drifted=false — even when Diff would
// return a NeedsUpdate result. DetectDrift (without specs) must NOT call Diff;
// config-drift detection requires injected specs via DetectDriftWithSpecs.
//
// The fakeDriverForDrift.Diff method panics when diffResult/diffErr are both nil,
// so any invocation from DetectDrift will cause this test to fail with a clear message.
func TestDetectDrift_ReadOkReturnsInSync(t *testing.T) {
	p := newProviderWithFakeDriver("infra.vpc", &fakeDriverForDrift{
		readOutput: &interfaces.ResourceOutput{Name: "test-vpc", Type: "infra.vpc", Status: "active"},
	})
	refs := []interfaces.ResourceRef{{Name: "test-vpc", Type: "infra.vpc"}}

	results, err := p.DetectDrift(context.Background(), refs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Drifted {
		t.Errorf("expected Drifted=false: DetectDrift must not call Diff when no spec is provided (empty-spec Diff causes false positives)")
	}
	if r.Class != interfaces.DriftClassInSync {
		t.Errorf("expected Class=%q, got %q (Diff was called — must not be)", interfaces.DriftClassInSync, r.Class)
	}
}

// TestDetectDrift_TransientErrorPropagates verifies that when a driver's Read
// returns a non-404 error, DetectDrift propagates it without classifying the
// resource as drifted. This prevents spurious state-prune on transient API failures.
func TestDetectDrift_TransientErrorPropagates(t *testing.T) {
	transient := errors.New("DO API rate limit exceeded")
	p := newProviderWithFakeDriver("infra.vpc", &fakeDriverForDrift{
		readErr: transient,
	})
	refs := []interfaces.ResourceRef{{Name: "test-vpc", Type: "infra.vpc"}}

	_, err := p.DetectDrift(context.Background(), refs)
	if err == nil {
		t.Fatal("expected transient error to propagate, got nil")
	}
	if !errors.Is(err, transient) {
		t.Errorf("expected error chain to include transient error, got: %v", err)
	}
}

// TestDetectDrift_UnknownTypeReturnsUnknown verifies that when a ref.Type has
// no registered driver, the result is classified as DriftClassUnknown.
func TestDetectDrift_UnknownTypeReturnsUnknown(t *testing.T) {
	p := &DOProvider{
		drivers: map[string]interfaces.ResourceDriver{},
	}
	refs := []interfaces.ResourceRef{{Name: "mystery", Type: "infra.unknown_type"}}

	results, err := p.DetectDrift(context.Background(), refs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Class != interfaces.DriftClassUnknown {
		t.Errorf("expected Class=%q for unknown type, got %q", interfaces.DriftClassUnknown, r.Class)
	}
	if len(r.Fields) == 0 {
		t.Errorf("expected Fields to contain driver-resolution error, got empty")
	}
}

// TestDetectDrift_TransientErrorDiscardsPriorResults verifies that when a
// transient (non-404) Read error occurs mid-loop, DetectDrift returns nil results
// and the propagated error. Callers must not act on a partial drift picture.
func TestDetectDrift_TransientErrorDiscardsPriorResults(t *testing.T) {
	transient := errors.New("DO API rate limit exceeded")
	// First ref: Read OK → would append an InSync result.
	// Second ref: Read returns transient error → must discard the first result.
	p := &DOProvider{
		drivers: map[string]interfaces.ResourceDriver{
			"infra.vpc": &fakeDriverForDrift{
				readOutput: &interfaces.ResourceOutput{Name: "ok-vpc", Type: "infra.vpc", Status: "active"},
			},
			"infra.droplet": &fakeDriverForDrift{
				readErr: transient,
			},
		},
	}
	refs := []interfaces.ResourceRef{
		{Name: "ok-vpc", Type: "infra.vpc"},
		{Name: "bad-droplet", Type: "infra.droplet"},
	}

	results, err := p.DetectDrift(context.Background(), refs)
	if err == nil {
		t.Fatal("expected transient error to propagate, got nil")
	}
	if !errors.Is(err, transient) {
		t.Errorf("expected error chain to include transient error, got: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil results on transient error, got %v", results)
	}
}

// TestErrResourceNotFound_AliasedToInterfacesSentinel verifies that the local
// drivers.ErrResourceNotFound sentinel is the same as interfaces.ErrResourceNotFound,
// so cross-package errors.Is checks work for the ghost-detection path.
func TestErrResourceNotFound_AliasedToInterfacesSentinel(t *testing.T) {
	// Simulate what a driver does on list-scan not-found: wraps drivers.ErrResourceNotFound.
	// Since drivers.ErrResourceNotFound is now aliased to interfaces.ErrResourceNotFound,
	// errors.Is must resolve to true for the canonical sentinel.
	wrapped := fmt.Errorf("resource test: %w", drivers.ErrResourceNotFound)
	if !errors.Is(wrapped, interfaces.ErrResourceNotFound) {
		t.Error("drivers.ErrResourceNotFound must satisfy errors.Is(err, interfaces.ErrResourceNotFound)")
	}
	// Also verify the identity (same pointer).
	if drivers.ErrResourceNotFound != interfaces.ErrResourceNotFound {
		t.Error("drivers.ErrResourceNotFound is not the same value as interfaces.ErrResourceNotFound")
	}
}

// ── DetectDriftWithSpecs tests ────────────────────────────────────────────────

// TestDetectDriftWithSpecs_ConfigDriftDetected verifies that when a spec is
// provided for a ref and driver.Diff reports NeedsUpdate, DetectDriftWithSpecs
// classifies the result as DriftClassConfig with Drifted=true and populates
// Fields with the changed config paths.
func TestDetectDriftWithSpecs_ConfigDriftDetected(t *testing.T) {
	p := newProviderWithFakeDriver("infra.vpc", &fakeDriverForDrift{
		readOutput: &interfaces.ResourceOutput{Name: "my-vpc", Type: "infra.vpc", Status: "active"},
		diffResult: &interfaces.DiffResult{
			NeedsUpdate: true,
			Changes: []interfaces.FieldChange{
				{Path: "ip_range", Old: "10.0.0.0/8", New: "192.168.0.0/16"},
			},
		},
	})
	refs := []interfaces.ResourceRef{{Name: "my-vpc", Type: "infra.vpc"}}
	specs := map[string]interfaces.ResourceSpec{
		"my-vpc": {Name: "my-vpc", Type: "infra.vpc", Config: map[string]any{"ip_range": "192.168.0.0/16"}},
	}

	results, err := p.DetectDriftWithSpecs(context.Background(), refs, specs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if !r.Drifted {
		t.Errorf("expected Drifted=true for config drift, got false")
	}
	if r.Class != interfaces.DriftClassConfig {
		t.Errorf("expected Class=%q, got %q", interfaces.DriftClassConfig, r.Class)
	}
	if len(r.Fields) != 1 || r.Fields[0] != "ip_range" {
		t.Errorf("expected Fields=[ip_range], got %v", r.Fields)
	}
}

// TestDetectDriftWithSpecs_SpecProvidedNoChanges verifies that when a spec is
// provided but driver.Diff reports no changes, the result is DriftClassInSync.
func TestDetectDriftWithSpecs_SpecProvidedNoChanges(t *testing.T) {
	p := newProviderWithFakeDriver("infra.vpc", &fakeDriverForDrift{
		readOutput: &interfaces.ResourceOutput{Name: "my-vpc", Type: "infra.vpc", Status: "active"},
		diffResult: &interfaces.DiffResult{NeedsUpdate: false},
	})
	refs := []interfaces.ResourceRef{{Name: "my-vpc", Type: "infra.vpc"}}
	specs := map[string]interfaces.ResourceSpec{
		"my-vpc": {Name: "my-vpc", Type: "infra.vpc", Config: map[string]any{"ip_range": "10.0.0.0/8"}},
	}

	results, err := p.DetectDriftWithSpecs(context.Background(), refs, specs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Drifted {
		t.Errorf("expected Drifted=false when Diff reports no changes, got true")
	}
	if r.Class != interfaces.DriftClassInSync {
		t.Errorf("expected Class=%q, got %q", interfaces.DriftClassInSync, r.Class)
	}
}

// TestDetectDriftWithSpecs_GhostOverridesSpec verifies that when a ref maps to a
// ghost (Read returns ErrResourceNotFound), the result is DriftClassGhost even
// when a spec is provided — the spec is irrelevant for a missing resource.
func TestDetectDriftWithSpecs_GhostOverridesSpec(t *testing.T) {
	p := newProviderWithFakeDriver("infra.vpc", &fakeDriverForDrift{
		readErr: fmt.Errorf("vpc: %w", interfaces.ErrResourceNotFound),
		// diffResult left nil: Diff must NOT be called for a ghost resource.
	})
	refs := []interfaces.ResourceRef{{Name: "ghost-vpc", Type: "infra.vpc"}}
	specs := map[string]interfaces.ResourceSpec{
		"ghost-vpc": {Name: "ghost-vpc", Type: "infra.vpc", Config: map[string]any{"ip_range": "10.0.0.0/8"}},
	}

	results, err := p.DetectDriftWithSpecs(context.Background(), refs, specs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if !r.Drifted {
		t.Errorf("expected Drifted=true for ghost-in-state, got false")
	}
	if r.Class != interfaces.DriftClassGhost {
		t.Errorf("expected Class=%q, got %q", interfaces.DriftClassGhost, r.Class)
	}
}

// TestDetectDriftWithSpecs_DiffErrorPropagates verifies that when driver.Diff
// returns an error, DetectDriftWithSpecs propagates it and discards accumulated
// results (same safety invariant as Read errors).
func TestDetectDriftWithSpecs_DiffErrorPropagates(t *testing.T) {
	diffErr := errors.New("DO API internal error")
	p := newProviderWithFakeDriver("infra.vpc", &fakeDriverForDrift{
		readOutput: &interfaces.ResourceOutput{Name: "my-vpc", Type: "infra.vpc", Status: "active"},
		diffErr:    diffErr,
	})
	refs := []interfaces.ResourceRef{{Name: "my-vpc", Type: "infra.vpc"}}
	specs := map[string]interfaces.ResourceSpec{
		"my-vpc": {Name: "my-vpc", Type: "infra.vpc", Config: map[string]any{"ip_range": "10.0.0.0/8"}},
	}

	results, err := p.DetectDriftWithSpecs(context.Background(), refs, specs)
	if err == nil {
		t.Fatal("expected Diff error to propagate, got nil")
	}
	if !errors.Is(err, diffErr) {
		t.Errorf("expected error chain to include Diff error, got: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil results on Diff error, got %v", results)
	}
}

// TestDetectDriftWithSpecs_NoSpecFallsBackToInSync verifies that when no spec is
// provided for a ref (empty specs map), the result is DriftClassInSync after a
// successful Read — Diff is not called.
func TestDetectDriftWithSpecs_NoSpecFallsBackToInSync(t *testing.T) {
	p := newProviderWithFakeDriver("infra.vpc", &fakeDriverForDrift{
		readOutput: &interfaces.ResourceOutput{Name: "my-vpc", Type: "infra.vpc", Status: "active"},
		// diffResult left nil: Diff must NOT be called when no spec is in the map.
	})
	refs := []interfaces.ResourceRef{{Name: "my-vpc", Type: "infra.vpc"}}

	results, err := p.DetectDriftWithSpecs(context.Background(), refs, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Drifted {
		t.Errorf("expected Drifted=false when no spec provided, got true")
	}
	if r.Class != interfaces.DriftClassInSync {
		t.Errorf("expected Class=%q, got %q", interfaces.DriftClassInSync, r.Class)
	}
}

// TestDetectDriftWithSpecs_MixedRefsOnlySpecRefsDiff verifies that when a specs
// map contains an entry for only one of two refs (of different types), Diff is
// called only for the ref that has a spec; the other is classified InSync from
// Read alone.
func TestDetectDriftWithSpecs_MixedRefsOnlySpecRefsDiff(t *testing.T) {
	driverWithSpec := &fakeDriverForDrift{
		readOutput: &interfaces.ResourceOutput{Name: "my-vpc", Type: "infra.vpc", Status: "active"},
		diffResult: &interfaces.DiffResult{
			NeedsUpdate: true,
			Changes:     []interfaces.FieldChange{{Path: "ip_range"}},
		},
	}
	driverNoSpec := &fakeDriverForDrift{
		readOutput: &interfaces.ResourceOutput{Name: "my-droplet", Type: "infra.droplet", Status: "active"},
		// diffResult nil — Diff must not be called for my-droplet.
	}
	p := &DOProvider{
		drivers: map[string]interfaces.ResourceDriver{
			"infra.vpc":     driverWithSpec,
			"infra.droplet": driverNoSpec,
		},
	}
	refs := []interfaces.ResourceRef{
		{Name: "my-vpc", Type: "infra.vpc"},
		{Name: "my-droplet", Type: "infra.droplet"},
	}
	specs := map[string]interfaces.ResourceSpec{
		"my-vpc": {Name: "my-vpc", Type: "infra.vpc", Config: map[string]any{"ip_range": "192.168.0.0/16"}},
		// my-droplet intentionally omitted — no spec → no Diff
	}

	results, err := p.DetectDriftWithSpecs(context.Background(), refs, specs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	// my-vpc should be DriftClassConfig
	if results[0].Class != interfaces.DriftClassConfig {
		t.Errorf("my-vpc: expected Class=%q, got %q", interfaces.DriftClassConfig, results[0].Class)
	}
	if !results[0].Drifted {
		t.Errorf("my-vpc: expected Drifted=true")
	}
	// my-droplet should be DriftClassInSync (no spec → no Diff)
	if results[1].Class != interfaces.DriftClassInSync {
		t.Errorf("my-droplet: expected Class=%q, got %q", interfaces.DriftClassInSync, results[1].Class)
	}
	if results[1].Drifted {
		t.Errorf("my-droplet: expected Drifted=false")
	}
	if !driverWithSpec.diffCalled {
		t.Error("expected Diff to be called for my-vpc")
	}
}
