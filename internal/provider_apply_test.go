package internal

import (
	"context"
	"errors"
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
)

// deleteFakeDriver records calls to Delete so tests can assert dispatch.
type deleteFakeDriver struct {
	deleteCalls  int
	deletedRef   interfaces.ResourceRef
	deleteReturn error
}

func (f *deleteFakeDriver) Create(_ context.Context, s interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	return &interfaces.ResourceOutput{Name: s.Name, Type: s.Type, ProviderID: "created-id"}, nil
}
func (f *deleteFakeDriver) Read(_ context.Context, _ interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	return nil, nil
}
func (f *deleteFakeDriver) Update(_ context.Context, _ interfaces.ResourceRef, s interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	return &interfaces.ResourceOutput{Name: s.Name, Type: s.Type, ProviderID: "updated-id"}, nil
}
func (f *deleteFakeDriver) Delete(_ context.Context, ref interfaces.ResourceRef) error {
	f.deleteCalls++
	f.deletedRef = ref
	return f.deleteReturn
}
func (f *deleteFakeDriver) Diff(_ context.Context, _ interfaces.ResourceSpec, _ *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	return nil, nil
}
func (f *deleteFakeDriver) HealthCheck(_ context.Context, _ interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	return nil, nil
}
func (f *deleteFakeDriver) Scale(_ context.Context, _ interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return nil, nil
}
func (f *deleteFakeDriver) SensitiveKeys() []string { return nil }

// TestDOProvider_Apply_DeleteAction verifies that a "delete" plan action:
//  1. dispatches to d.Delete with a ref built from action.Current (ProviderID)
//  2. returns no error on success
//  3. does NOT add a resource entry to ApplyResult.Resources (deleted resources
//     have no post-apply output)
func TestDOProvider_Apply_DeleteAction(t *testing.T) {
	fake := &deleteFakeDriver{}
	p := &DOProvider{drivers: map[string]interfaces.ResourceDriver{"infra.firewall": fake}}

	plan := &interfaces.IaCPlan{
		ID: "plan-delete",
		Actions: []interfaces.PlanAction{{
			Action:   "delete",
			Resource: interfaces.ResourceSpec{Name: "bmw-staging-firewall", Type: "infra.firewall"},
			Current: &interfaces.ResourceState{
				Name:       "bmw-staging-firewall",
				Type:       "infra.firewall",
				ProviderID: "do-firewall-abc123",
			},
		}},
	}

	result, err := p.Apply(t.Context(), plan)
	if err != nil {
		t.Fatalf("Apply returned non-nil error: %v", err)
	}
	if len(result.Errors) > 0 {
		t.Fatalf("Apply returned action errors: %v", result.Errors)
	}
	if fake.deleteCalls != 1 {
		t.Fatalf("Delete called %d times, want 1", fake.deleteCalls)
	}
	if fake.deletedRef.ProviderID != "do-firewall-abc123" {
		t.Errorf("Delete called with ProviderID %q, want do-firewall-abc123", fake.deletedRef.ProviderID)
	}
	if fake.deletedRef.Name != "bmw-staging-firewall" {
		t.Errorf("Delete called with Name %q, want bmw-staging-firewall", fake.deletedRef.Name)
	}
	// Deleted resource should NOT appear in Resources (it no longer exists).
	if len(result.Resources) != 0 {
		t.Errorf("ApplyResult.Resources = %d entries, want 0 for a delete action", len(result.Resources))
	}
}

// TestDOProvider_Apply_DeleteAction_MissingCurrent verifies that a delete
// action with no Current state is collected as an error (not a panic/crash).
func TestDOProvider_Apply_DeleteAction_MissingCurrent(t *testing.T) {
	fake := &deleteFakeDriver{}
	p := &DOProvider{drivers: map[string]interfaces.ResourceDriver{"infra.firewall": fake}}

	plan := &interfaces.IaCPlan{
		ID: "plan-delete-no-current",
		Actions: []interfaces.PlanAction{{
			Action:  "delete",
			Resource: interfaces.ResourceSpec{Name: "orphan-firewall", Type: "infra.firewall"},
			Current: nil, // missing — defensive handling required
		}},
	}

	result, err := p.Apply(t.Context(), plan)
	if err != nil {
		t.Fatalf("Apply returned top-level error (want nil + error in result.Errors): %v", err)
	}
	if len(result.Errors) == 0 {
		t.Fatal("expected error in result.Errors for delete with missing Current, got none")
	}
	if fake.deleteCalls != 0 {
		t.Errorf("Delete should not be called when Current is nil, but called %d times", fake.deleteCalls)
	}
}

// TestDOProvider_Apply_DeleteAction_DriverError verifies that a driver error
// on Delete is collected in result.Errors (Apply itself still returns nil).
func TestDOProvider_Apply_DeleteAction_DriverError(t *testing.T) {
	driverErr := errors.New("DO API: firewall not found")
	fake := &deleteFakeDriver{deleteReturn: driverErr}
	p := &DOProvider{drivers: map[string]interfaces.ResourceDriver{"infra.firewall": fake}}

	plan := &interfaces.IaCPlan{
		ID: "plan-delete-err",
		Actions: []interfaces.PlanAction{{
			Action:   "delete",
			Resource: interfaces.ResourceSpec{Name: "bmw-staging-firewall", Type: "infra.firewall"},
			Current: &interfaces.ResourceState{
				Name: "bmw-staging-firewall", Type: "infra.firewall", ProviderID: "do-firewall-abc123",
			},
		}},
	}

	result, err := p.Apply(t.Context(), plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(result.Errors) == 0 {
		t.Fatal("expected error in result.Errors for failed delete, got none")
	}
	if result.Errors[0].Action != "delete" {
		t.Errorf("ActionError.Action = %q, want delete", result.Errors[0].Action)
	}
}

// TestDOProvider_Apply_DeleteAndCreate_NilOutGuard verifies that a plan with
// a delete followed by a create does not panic: the delete produces nil output
// and the create produces non-nil output. Both must land correctly in result.
func TestDOProvider_Apply_DeleteAndCreate_NilOutGuard(t *testing.T) {
	fake := &deleteFakeDriver{}
	p := &DOProvider{drivers: map[string]interfaces.ResourceDriver{"infra.firewall": fake}}

	plan := &interfaces.IaCPlan{
		ID: "plan-delete-then-create",
		Actions: []interfaces.PlanAction{
			{
				Action:   "delete",
				Resource: interfaces.ResourceSpec{Name: "old-firewall", Type: "infra.firewall"},
				Current: &interfaces.ResourceState{
					Name: "old-firewall", Type: "infra.firewall", ProviderID: "old-fw-id",
				},
			},
			{
				Action:   "create",
				Resource: interfaces.ResourceSpec{Name: "new-firewall", Type: "infra.firewall"},
			},
		},
	}

	result, err := p.Apply(t.Context(), plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(result.Errors) > 0 {
		t.Fatalf("Apply errors: %v", result.Errors)
	}
	// Only the create result should be in Resources (1 entry, not 2).
	if len(result.Resources) != 1 {
		t.Errorf("ApplyResult.Resources = %d entries, want 1 (delete produces no output)", len(result.Resources))
	}
	if len(result.Resources) > 0 && result.Resources[0].Name != "new-firewall" {
		t.Errorf("Resources[0].Name = %q, want new-firewall", result.Resources[0].Name)
	}
}
