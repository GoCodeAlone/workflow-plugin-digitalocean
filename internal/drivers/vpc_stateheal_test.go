package drivers

// State-heal tests for VPCDriver.Update / Delete.

import (
	"context"
	"errors"
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type vpcStateHealMock struct {
	listVPCs  []*godo.VPC
	listErr   error
	listCalls int

	updatedVPC     *godo.VPC
	updateErr      error
	updateCalledID string

	deleteCalledID string
	deleteErr      error

	createVPC *godo.VPC
	createErr error

	// getVPC is returned by Get (used by HealthCheck after heal).
	getVPC *godo.VPC
}

func (m *vpcStateHealMock) Create(_ context.Context, _ *godo.VPCCreateRequest) (*godo.VPC, *godo.Response, error) {
	return m.createVPC, nil, m.createErr
}
func (m *vpcStateHealMock) Get(_ context.Context, _ string) (*godo.VPC, *godo.Response, error) {
	if m.getVPC != nil {
		return m.getVPC, nil, nil
	}
	return nil, nil, errors.New("not implemented in vpcStateHealMock")
}
func (m *vpcStateHealMock) List(_ context.Context, _ *godo.ListOptions) ([]*godo.VPC, *godo.Response, error) {
	m.listCalls++
	return m.listVPCs, nil, m.listErr
}
func (m *vpcStateHealMock) Update(_ context.Context, vpcID string, _ *godo.VPCUpdateRequest) (*godo.VPC, *godo.Response, error) {
	m.updateCalledID = vpcID
	return m.updatedVPC, nil, m.updateErr
}
func (m *vpcStateHealMock) Delete(_ context.Context, vpcID string) (*godo.Response, error) {
	m.deleteCalledID = vpcID
	return nil, m.deleteErr
}

// ── Create ────────────────────────────────────────────────────────────────────

func TestVPCDriver_Create_PersistsUUIDInState(t *testing.T) {
	const wantUUID = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &vpcStateHealMock{
		createVPC: &godo.VPC{ID: wantUUID, Name: "my-vpc"},
	}
	d := NewVPCDriverWithClient(m, "nyc3")
	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-vpc",
		Config: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID != wantUUID {
		t.Errorf("ProviderID = %q, want UUID %q", out.ProviderID, wantUUID)
	}
	if out.ProviderID == "my-vpc" {
		t.Error("ProviderID must not be the spec name")
	}
}

// ── Update ────────────────────────────────────────────────────────────────────

func TestVPCDriver_Update_UsesExistingUUID(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &vpcStateHealMock{
		updatedVPC: &godo.VPC{ID: uuid, Name: "my-vpc"},
	}
	d := NewVPCDriverWithClient(m, "nyc3")
	_, err := d.Update(context.Background(),
		interfaces.ResourceRef{Name: "my-vpc", ProviderID: uuid},
		interfaces.ResourceSpec{Name: "my-vpc", Config: map[string]any{}},
	)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if m.updateCalledID != uuid {
		t.Errorf("Update called with %q, want %q", m.updateCalledID, uuid)
	}
	// List must NOT fire when ProviderID is already a valid UUID.
	if m.listCalls != 0 {
		t.Errorf("listCalls = %d, want 0 (heal must not fire for valid UUID)", m.listCalls)
	}
}

func TestVPCDriver_Update_HealsStaleName(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &vpcStateHealMock{
		listVPCs:   []*godo.VPC{{ID: uuid, Name: "my-vpc"}},
		updatedVPC: &godo.VPC{ID: uuid, Name: "my-vpc"},
	}
	d := NewVPCDriverWithClient(m, "nyc3")
	out, err := d.Update(context.Background(),
		interfaces.ResourceRef{Name: "my-vpc", ProviderID: "my-vpc"}, // stale name
		interfaces.ResourceSpec{Name: "my-vpc", Config: map[string]any{}},
	)
	if err != nil {
		t.Fatalf("Update with stale name: %v", err)
	}
	if m.listCalls < 1 {
		t.Errorf("listCalls = %d, want ≥ 1 (heal must fire)", m.listCalls)
	}
	if m.updateCalledID != uuid {
		t.Errorf("Update called with %q, want UUID %q", m.updateCalledID, uuid)
	}
	if out.ProviderID != uuid {
		t.Errorf("output ProviderID = %q, want UUID %q", out.ProviderID, uuid)
	}
}

func TestVPCDriver_Update_HealFails_WhenResourceNotFound(t *testing.T) {
	m := &vpcStateHealMock{listErr: errors.New("api unavailable")}
	d := NewVPCDriverWithClient(m, "nyc3")
	_, err := d.Update(context.Background(),
		interfaces.ResourceRef{Name: "my-vpc", ProviderID: "my-vpc"},
		interfaces.ResourceSpec{Name: "my-vpc", Config: map[string]any{}},
	)
	if err == nil {
		t.Fatal("expected error when heal lookup fails, got nil")
	}
}

// ── Delete ────────────────────────────────────────────────────────────────────

func TestVPCDriver_Delete_HealsStaleName(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &vpcStateHealMock{
		listVPCs: []*godo.VPC{{ID: uuid, Name: "my-vpc"}},
	}
	d := NewVPCDriverWithClient(m, "nyc3")
	if err := d.Delete(context.Background(),
		interfaces.ResourceRef{Name: "my-vpc", ProviderID: "my-vpc"},
	); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if m.deleteCalledID != uuid {
		t.Errorf("Delete called with %q, want UUID %q", m.deleteCalledID, uuid)
	}
}

// ── HealthCheck state-heal tests ─────────────────────────────────────────────

func TestVPCDriver_HealthCheck_HealsStaleName(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &vpcStateHealMock{
		listVPCs: []*godo.VPC{{ID: uuid, Name: "my-vpc"}},
		getVPC:   &godo.VPC{ID: uuid, Name: "my-vpc"},
	}
	d := NewVPCDriverWithClient(m, "nyc3")
	ref := interfaces.ResourceRef{Name: "my-vpc", ProviderID: "my-vpc"} // stale name
	result, err := d.HealthCheck(context.Background(), ref)
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if m.listCalls < 1 {
		t.Errorf("listCalls = %d, want ≥ 1 (resolve must fire for stale name)", m.listCalls)
	}
	if !result.Healthy {
		t.Errorf("Healthy = false, want true after state-heal")
	}
}
