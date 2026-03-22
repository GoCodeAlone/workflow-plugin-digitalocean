package drivers_test

import (
	"context"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
)

func TestIAMRoleDriver_Create(t *testing.T) {
	d := drivers.NewIAMRoleDriver()

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "deploy-role",
		Config: map[string]any{"scopes": []any{"read", "write"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID != "deploy-role" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "deploy-role")
	}
	if out.Status != "declared" {
		t.Errorf("Status = %q, want %q", out.Status, "declared")
	}
	// Limitation notice must be present.
	if _, ok := out.Outputs["limitation"]; !ok {
		t.Error("expected 'limitation' in outputs")
	}
}

func TestIAMRoleDriver_HealthCheck(t *testing.T) {
	d := drivers.NewIAMRoleDriver()

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "deploy-role"})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if !result.Healthy {
		t.Errorf("expected healthy (declared) role")
	}
}

func TestIAMRoleDriver_Read_Success(t *testing.T) {
	d := drivers.NewIAMRoleDriver()

	out, err := d.Read(context.Background(), interfaces.ResourceRef{Name: "deploy-role", ProviderID: "deploy-role"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.Status != "declared" {
		t.Errorf("Status = %q, want %q", out.Status, "declared")
	}
}

func TestIAMRoleDriver_Update_Success(t *testing.T) {
	d := drivers.NewIAMRoleDriver()

	out, err := d.Update(context.Background(), interfaces.ResourceRef{Name: "deploy-role"}, interfaces.ResourceSpec{
		Name:   "deploy-role",
		Config: map[string]any{"scopes": []any{"read"}},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.Status != "declared" {
		t.Errorf("Status = %q, want %q", out.Status, "declared")
	}
}

func TestIAMRoleDriver_Delete_NoOp(t *testing.T) {
	d := drivers.NewIAMRoleDriver()

	err := d.Delete(context.Background(), interfaces.ResourceRef{Name: "deploy-role"})
	if err != nil {
		t.Fatalf("Delete should be no-op, got: %v", err)
	}
}

func TestIAMRoleDriver_Diff_NilCurrent(t *testing.T) {
	d := drivers.NewIAMRoleDriver()

	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{Name: "deploy-role"}, nil)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=true when current is nil")
	}
}

func TestIAMRoleDriver_Diff_NoChanges(t *testing.T) {
	d := drivers.NewIAMRoleDriver()

	current := &interfaces.ResourceOutput{ProviderID: "deploy-role", Status: "declared"}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{Name: "deploy-role"}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=false when current exists")
	}
}
