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
// Read and Diff behavior for DetectDrift unit tests. No live API calls are made.
type fakeDriverForDrift struct {
	readErr    error
	readOutput *interfaces.ResourceOutput
	diffResult *interfaces.DiffResult
	diffErr    error
}

func (f *fakeDriverForDrift) Read(_ context.Context, _ interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	return f.readOutput, f.readErr
}

func (f *fakeDriverForDrift) Diff(_ context.Context, _ interfaces.ResourceSpec, _ *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
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

// TestDetectDrift_DiffReturnsConfig verifies that when Read succeeds and Diff
// reports drift (NeedsUpdate=true with Changes), the result is classified as
// DriftClassConfig.
func TestDetectDrift_DiffReturnsConfig(t *testing.T) {
	p := newProviderWithFakeDriver("infra.vpc", &fakeDriverForDrift{
		readOutput: &interfaces.ResourceOutput{Name: "test-vpc", Type: "infra.vpc", Status: "active"},
		diffResult: &interfaces.DiffResult{
			NeedsUpdate: true,
			Changes: []interfaces.FieldChange{
				{Path: "region", Old: "nyc1", New: "nyc3"},
			},
		},
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
		t.Errorf("expected Drifted=true for config drift")
	}
	if r.Class != interfaces.DriftClassConfig {
		t.Errorf("expected Class=%q, got %q", interfaces.DriftClassConfig, r.Class)
	}
	if len(r.Fields) == 0 {
		t.Errorf("expected Fields to contain drifted field names, got empty")
	}
	found := false
	for _, f := range r.Fields {
		if f == "region" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected Fields to contain %q, got %v", "region", r.Fields)
	}
}

// TestDetectDrift_NoDiffReturnsInSync verifies that when Read succeeds and Diff
// reports no drift (NeedsUpdate=false, no Changes), the result is classified as
// DriftClassInSync with Drifted=false.
func TestDetectDrift_NoDiffReturnsInSync(t *testing.T) {
	p := newProviderWithFakeDriver("infra.vpc", &fakeDriverForDrift{
		readOutput: &interfaces.ResourceOutput{Name: "test-vpc", Type: "infra.vpc", Status: "active"},
		diffResult: &interfaces.DiffResult{NeedsUpdate: false},
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
		t.Errorf("expected Drifted=false for in-sync resource")
	}
	if r.Class != interfaces.DriftClassInSync {
		t.Errorf("expected Class=%q, got %q", interfaces.DriftClassInSync, r.Class)
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
