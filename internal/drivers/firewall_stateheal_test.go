package drivers

// State-heal tests for FirewallDriver.Update / Delete.

import (
	"context"
	"errors"
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type firewallStateHealMock struct {
	listFWs   []godo.Firewall
	listErr   error
	listCalls int

	updatedFW      *godo.Firewall
	updateErr      error
	updateCalledID string

	deleteCalledID string
	deleteErr      error

	createFW  *godo.Firewall
	createErr error
}

func (m *firewallStateHealMock) Create(_ context.Context, _ *godo.FirewallRequest) (*godo.Firewall, *godo.Response, error) {
	return m.createFW, nil, m.createErr
}
func (m *firewallStateHealMock) Get(_ context.Context, _ string) (*godo.Firewall, *godo.Response, error) {
	return nil, nil, errors.New("not implemented in firewallStateHealMock")
}
func (m *firewallStateHealMock) List(_ context.Context, _ *godo.ListOptions) ([]godo.Firewall, *godo.Response, error) {
	m.listCalls++
	return m.listFWs, nil, m.listErr
}
func (m *firewallStateHealMock) Update(_ context.Context, fwID string, _ *godo.FirewallRequest) (*godo.Firewall, *godo.Response, error) {
	m.updateCalledID = fwID
	return m.updatedFW, nil, m.updateErr
}
func (m *firewallStateHealMock) Delete(_ context.Context, fwID string) (*godo.Response, error) {
	m.deleteCalledID = fwID
	return nil, m.deleteErr
}

// ── Create ────────────────────────────────────────────────────────────────────

func TestFirewallDriver_Create_PersistsUUIDInState(t *testing.T) {
	const wantUUID = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &firewallStateHealMock{
		createFW: &godo.Firewall{ID: wantUUID, Name: "my-fw"},
	}
	d := NewFirewallDriverWithClient(m)
	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-fw",
		Config: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID != wantUUID {
		t.Errorf("ProviderID = %q, want UUID %q", out.ProviderID, wantUUID)
	}
	if out.ProviderID == "my-fw" {
		t.Error("ProviderID must not be the spec name")
	}
}

// ── Update ────────────────────────────────────────────────────────────────────

func TestFirewallDriver_Update_UsesExistingUUID(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &firewallStateHealMock{
		updatedFW: &godo.Firewall{ID: uuid, Name: "my-fw"},
	}
	d := NewFirewallDriverWithClient(m)
	_, err := d.Update(context.Background(),
		interfaces.ResourceRef{Name: "my-fw", ProviderID: uuid},
		interfaces.ResourceSpec{Name: "my-fw", Config: map[string]any{}},
	)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if m.updateCalledID != uuid {
		t.Errorf("Update called with %q, want %q", m.updateCalledID, uuid)
	}
	if m.listCalls != 0 {
		t.Errorf("listCalls = %d, want 0 (heal must not fire for valid UUID)", m.listCalls)
	}
}

func TestFirewallDriver_Update_HealsStaleName(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &firewallStateHealMock{
		listFWs:   []godo.Firewall{{ID: uuid, Name: "my-fw"}},
		updatedFW: &godo.Firewall{ID: uuid, Name: "my-fw"},
	}
	d := NewFirewallDriverWithClient(m)
	out, err := d.Update(context.Background(),
		interfaces.ResourceRef{Name: "my-fw", ProviderID: "my-fw"}, // stale name
		interfaces.ResourceSpec{Name: "my-fw", Config: map[string]any{}},
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

func TestFirewallDriver_Update_HealFails_WhenResourceNotFound(t *testing.T) {
	m := &firewallStateHealMock{listErr: errors.New("api unavailable")}
	d := NewFirewallDriverWithClient(m)
	_, err := d.Update(context.Background(),
		interfaces.ResourceRef{Name: "my-fw", ProviderID: "my-fw"},
		interfaces.ResourceSpec{Name: "my-fw", Config: map[string]any{}},
	)
	if err == nil {
		t.Fatal("expected error when heal lookup fails, got nil")
	}
}

// ── Delete ────────────────────────────────────────────────────────────────────

func TestFirewallDriver_Delete_HealsStaleName(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &firewallStateHealMock{
		listFWs: []godo.Firewall{{ID: uuid, Name: "my-fw"}},
	}
	d := NewFirewallDriverWithClient(m)
	if err := d.Delete(context.Background(),
		interfaces.ResourceRef{Name: "my-fw", ProviderID: "my-fw"},
	); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if m.deleteCalledID != uuid {
		t.Errorf("Delete called with %q, want UUID %q", m.deleteCalledID, uuid)
	}
}
