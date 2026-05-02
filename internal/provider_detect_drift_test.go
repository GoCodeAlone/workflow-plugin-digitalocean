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
// Read behavior for DetectDrift unit tests. No live API calls are made.
// DetectDrift does not call Diff, so no diffResult/diffErr fields are needed.
type fakeDriverForDrift struct {
	readErr    error
	readOutput *interfaces.ResourceOutput
}

func (f *fakeDriverForDrift) Read(_ context.Context, _ interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	return f.readOutput, f.readErr
}

func (f *fakeDriverForDrift) Diff(_ context.Context, _ interfaces.ResourceSpec, _ *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	// DetectDrift must never call Diff. If this method is called during a
	// DetectDrift test, panic so the test fails with a clear message.
	panic("fakeDriverForDrift.Diff called — DetectDrift must not call Diff (empty-spec causes false positives)")
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
// return a NeedsUpdate result. DetectDrift must NOT call Diff; config-drift
// detection is deferred to wfctl infra plan which has access to the declared spec.
//
// The fakeDriverForDrift.Diff method panics if called, so any invocation from
// DetectDrift will cause this test to fail with an explicit panic message.
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
		t.Errorf("expected Drifted=false: DetectDrift must not call Diff (empty-spec Diff causes false positives)")
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
