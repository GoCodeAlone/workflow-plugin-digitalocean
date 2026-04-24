package drivers

// State-heal tests for LoadBalancerDriver.Update / Delete.

import (
	"context"
	"errors"
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type lbStateHealMock struct {
	listLBs   []godo.LoadBalancer
	listErr   error
	listCalls int

	updatedLB      *godo.LoadBalancer
	updateErr      error
	updateCalledID string

	deleteCalledID string
	deleteErr      error

	createLB  *godo.LoadBalancer
	createErr error
}

func (m *lbStateHealMock) Create(_ context.Context, _ *godo.LoadBalancerRequest) (*godo.LoadBalancer, *godo.Response, error) {
	return m.createLB, nil, m.createErr
}
func (m *lbStateHealMock) Get(_ context.Context, _ string) (*godo.LoadBalancer, *godo.Response, error) {
	return nil, nil, errors.New("not implemented in lbStateHealMock")
}
func (m *lbStateHealMock) List(_ context.Context, _ *godo.ListOptions) ([]godo.LoadBalancer, *godo.Response, error) {
	m.listCalls++
	return m.listLBs, nil, m.listErr
}
func (m *lbStateHealMock) Update(_ context.Context, lbID string, _ *godo.LoadBalancerRequest) (*godo.LoadBalancer, *godo.Response, error) {
	m.updateCalledID = lbID
	return m.updatedLB, nil, m.updateErr
}
func (m *lbStateHealMock) Delete(_ context.Context, lbID string) (*godo.Response, error) {
	m.deleteCalledID = lbID
	return nil, m.deleteErr
}

// ── Create ────────────────────────────────────────────────────────────────────

func TestLoadBalancerDriver_Create_PersistsUUIDInState(t *testing.T) {
	const wantUUID = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &lbStateHealMock{
		createLB: &godo.LoadBalancer{ID: wantUUID, Name: "my-lb"},
	}
	d := NewLoadBalancerDriverWithClient(m, "nyc3")
	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-lb",
		Config: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID != wantUUID {
		t.Errorf("ProviderID = %q, want UUID %q", out.ProviderID, wantUUID)
	}
	if out.ProviderID == "my-lb" {
		t.Error("ProviderID must not be the spec name")
	}
}

// ── Update ────────────────────────────────────────────────────────────────────

func TestLoadBalancerDriver_Update_UsesExistingUUID(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &lbStateHealMock{
		updatedLB: &godo.LoadBalancer{ID: uuid, Name: "my-lb"},
	}
	d := NewLoadBalancerDriverWithClient(m, "nyc3")
	_, err := d.Update(context.Background(),
		interfaces.ResourceRef{Name: "my-lb", ProviderID: uuid},
		interfaces.ResourceSpec{Name: "my-lb", Config: map[string]any{}},
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

func TestLoadBalancerDriver_Update_HealsStaleName(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &lbStateHealMock{
		listLBs:   []godo.LoadBalancer{{ID: uuid, Name: "my-lb"}},
		updatedLB: &godo.LoadBalancer{ID: uuid, Name: "my-lb"},
	}
	d := NewLoadBalancerDriverWithClient(m, "nyc3")
	out, err := d.Update(context.Background(),
		interfaces.ResourceRef{Name: "my-lb", ProviderID: "my-lb"}, // stale name
		interfaces.ResourceSpec{Name: "my-lb", Config: map[string]any{}},
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

func TestLoadBalancerDriver_Update_HealFails_WhenResourceNotFound(t *testing.T) {
	m := &lbStateHealMock{listErr: errors.New("api unavailable")}
	d := NewLoadBalancerDriverWithClient(m, "nyc3")
	_, err := d.Update(context.Background(),
		interfaces.ResourceRef{Name: "my-lb", ProviderID: "my-lb"},
		interfaces.ResourceSpec{Name: "my-lb", Config: map[string]any{}},
	)
	if err == nil {
		t.Fatal("expected error when heal lookup fails, got nil")
	}
}

// ── Delete ────────────────────────────────────────────────────────────────────

func TestLoadBalancerDriver_Delete_HealsStaleName(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &lbStateHealMock{
		listLBs: []godo.LoadBalancer{{ID: uuid, Name: "my-lb"}},
	}
	d := NewLoadBalancerDriverWithClient(m, "nyc3")
	if err := d.Delete(context.Background(),
		interfaces.ResourceRef{Name: "my-lb", ProviderID: "my-lb"},
	); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if m.deleteCalledID != uuid {
		t.Errorf("Delete called with %q, want UUID %q", m.deleteCalledID, uuid)
	}
}
