package drivers

// State-heal tests for APIGatewayDriver.Update / Delete.

import (
	"context"
	"errors"
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type apiGatewayStateHealMock struct {
	listApps  []*godo.App
	listErr   error
	listCalls int

	updatedApp     *godo.App
	updateErr      error
	updateCalledID string

	deleteCalledID string
	deleteErr      error

	createApp *godo.App
	createErr error

	// getApp is returned by Get (used by HealthCheck after heal).
	getApp *godo.App
}

func (m *apiGatewayStateHealMock) Create(_ context.Context, _ *godo.AppCreateRequest) (*godo.App, *godo.Response, error) {
	return m.createApp, nil, m.createErr
}
func (m *apiGatewayStateHealMock) Get(_ context.Context, _ string) (*godo.App, *godo.Response, error) {
	if m.getApp != nil {
		return m.getApp, nil, nil
	}
	return nil, nil, errors.New("not implemented in apiGatewayStateHealMock")
}
func (m *apiGatewayStateHealMock) List(_ context.Context, _ *godo.ListOptions) ([]*godo.App, *godo.Response, error) {
	m.listCalls++
	return m.listApps, nil, m.listErr
}
func (m *apiGatewayStateHealMock) Update(_ context.Context, appID string, _ *godo.AppUpdateRequest) (*godo.App, *godo.Response, error) {
	m.updateCalledID = appID
	return m.updatedApp, nil, m.updateErr
}
func (m *apiGatewayStateHealMock) Delete(_ context.Context, appID string) (*godo.Response, error) {
	m.deleteCalledID = appID
	return nil, m.deleteErr
}

// ── Create ────────────────────────────────────────────────────────────────────

func TestAPIGatewayDriver_Create_PersistsUUIDInState(t *testing.T) {
	const wantUUID = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &apiGatewayStateHealMock{
		createApp: &godo.App{ID: wantUUID, Spec: &godo.AppSpec{Name: "my-gateway"}},
	}
	d := NewAPIGatewayDriverWithClient(m, "nyc3")
	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-gateway",
		Config: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID != wantUUID {
		t.Errorf("ProviderID = %q, want UUID %q", out.ProviderID, wantUUID)
	}
	if out.ProviderID == "my-gateway" {
		t.Error("ProviderID must not be the spec name")
	}
}

// ── Update ────────────────────────────────────────────────────────────────────

func TestAPIGatewayDriver_Update_UsesExistingUUID(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &apiGatewayStateHealMock{
		updatedApp: &godo.App{ID: uuid, Spec: &godo.AppSpec{Name: "my-gateway"}},
	}
	d := NewAPIGatewayDriverWithClient(m, "nyc3")
	_, err := d.Update(context.Background(),
		interfaces.ResourceRef{Name: "my-gateway", ProviderID: uuid},
		interfaces.ResourceSpec{Name: "my-gateway", Config: map[string]any{}},
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

func TestAPIGatewayDriver_Update_HealsStaleName(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &apiGatewayStateHealMock{
		listApps:   []*godo.App{{ID: uuid, Spec: &godo.AppSpec{Name: "my-gateway"}}},
		updatedApp: &godo.App{ID: uuid, Spec: &godo.AppSpec{Name: "my-gateway"}},
	}
	d := NewAPIGatewayDriverWithClient(m, "nyc3")
	out, err := d.Update(context.Background(),
		interfaces.ResourceRef{Name: "my-gateway", ProviderID: "my-gateway"}, // stale name
		interfaces.ResourceSpec{Name: "my-gateway", Config: map[string]any{}},
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

func TestAPIGatewayDriver_Update_HealFails_WhenListFails(t *testing.T) {
	m := &apiGatewayStateHealMock{listErr: errors.New("api unavailable")}
	d := NewAPIGatewayDriverWithClient(m, "nyc3")
	_, err := d.Update(context.Background(),
		interfaces.ResourceRef{Name: "my-gateway", ProviderID: "my-gateway"},
		interfaces.ResourceSpec{Name: "my-gateway", Config: map[string]any{}},
	)
	if err == nil {
		t.Fatal("expected error when heal lookup fails, got nil")
	}
}

// ── Delete ────────────────────────────────────────────────────────────────────

func TestAPIGatewayDriver_Delete_HealsStaleName(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &apiGatewayStateHealMock{
		listApps: []*godo.App{{ID: uuid, Spec: &godo.AppSpec{Name: "my-gateway"}}},
	}
	d := NewAPIGatewayDriverWithClient(m, "nyc3")
	if err := d.Delete(context.Background(),
		interfaces.ResourceRef{Name: "my-gateway", ProviderID: "my-gateway"},
	); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if m.deleteCalledID != uuid {
		t.Errorf("Delete called with %q, want UUID %q", m.deleteCalledID, uuid)
	}
}

// ── HealthCheck state-heal tests ─────────────────────────────────────────────

func TestAPIGatewayDriver_HealthCheck_HealsStaleName(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &apiGatewayStateHealMock{
		listApps: []*godo.App{{ID: uuid, Spec: &godo.AppSpec{Name: "my-gateway"}}},
		getApp: &godo.App{
			ID:   uuid,
			Spec: &godo.AppSpec{Name: "my-gateway"},
			ActiveDeployment: &godo.Deployment{
				Phase: godo.DeploymentPhase_Active,
			},
		},
	}
	d := NewAPIGatewayDriverWithClient(m, "nyc3")
	ref := interfaces.ResourceRef{Name: "my-gateway", ProviderID: "my-gateway"} // stale name
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
