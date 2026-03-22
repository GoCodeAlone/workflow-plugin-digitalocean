package drivers_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type mockAPIGatewayClient struct {
	app *godo.App
	err error
}

func (m *mockAPIGatewayClient) Create(_ context.Context, _ *godo.AppCreateRequest) (*godo.App, *godo.Response, error) {
	return m.app, nil, m.err
}
func (m *mockAPIGatewayClient) Get(_ context.Context, _ string) (*godo.App, *godo.Response, error) {
	return m.app, nil, m.err
}
func (m *mockAPIGatewayClient) Update(_ context.Context, _ string, _ *godo.AppUpdateRequest) (*godo.App, *godo.Response, error) {
	return m.app, nil, m.err
}
func (m *mockAPIGatewayClient) Delete(_ context.Context, _ string) (*godo.Response, error) {
	return nil, m.err
}

func testGatewayApp() *godo.App {
	return &godo.App{
		ID:      "app-gw-123",
		LiveURL: "https://gw.example.com",
		Spec:    &godo.AppSpec{Name: "my-gateway"},
		ActiveDeployment: &godo.Deployment{
			Phase: godo.DeploymentPhase_Active,
		},
	}
}

func TestAPIGatewayDriver_Create(t *testing.T) {
	mock := &mockAPIGatewayClient{app: testGatewayApp()}
	d := drivers.NewAPIGatewayDriverWithClient(mock, "nyc3")

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-gateway",
		Config: map[string]any{
			"routes": []any{
				map[string]any{"path": "/api", "component": "api-service"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID != "app-gw-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "app-gw-123")
	}
	if out.Status != "running" {
		t.Errorf("Status = %q, want %q", out.Status, "running")
	}
}

func TestAPIGatewayDriver_Create_Error(t *testing.T) {
	mock := &mockAPIGatewayClient{err: fmt.Errorf("api failure")}
	d := drivers.NewAPIGatewayDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-gateway",
		Config: map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestAPIGatewayDriver_Read_Success(t *testing.T) {
	mock := &mockAPIGatewayClient{app: testGatewayApp()}
	d := drivers.NewAPIGatewayDriverWithClient(mock, "nyc3")

	out, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name: "my-gateway", ProviderID: "app-gw-123",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.ProviderID != "app-gw-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "app-gw-123")
	}
}

func TestAPIGatewayDriver_Update_Success(t *testing.T) {
	mock := &mockAPIGatewayClient{app: testGatewayApp()}
	d := drivers.NewAPIGatewayDriverWithClient(mock, "nyc3")

	out, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-gateway", ProviderID: "app-gw-123",
	}, interfaces.ResourceSpec{
		Name:   "my-gateway",
		Config: map[string]any{"routes": []any{map[string]any{"path": "/v2", "component": "api-svc"}}},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.ProviderID != "app-gw-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "app-gw-123")
	}
}

func TestAPIGatewayDriver_Update_Error(t *testing.T) {
	mock := &mockAPIGatewayClient{err: fmt.Errorf("update failed")}
	d := drivers.NewAPIGatewayDriverWithClient(mock, "nyc3")

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-gateway", ProviderID: "app-gw-123",
	}, interfaces.ResourceSpec{
		Name:   "my-gateway",
		Config: map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestAPIGatewayDriver_Delete_Success(t *testing.T) {
	mock := &mockAPIGatewayClient{app: testGatewayApp()}
	d := drivers.NewAPIGatewayDriverWithClient(mock, "nyc3")

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "my-gateway", ProviderID: "app-gw-123",
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestAPIGatewayDriver_Delete_Error(t *testing.T) {
	mock := &mockAPIGatewayClient{err: fmt.Errorf("delete failed")}
	d := drivers.NewAPIGatewayDriverWithClient(mock, "nyc3")

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "my-gateway", ProviderID: "app-gw-123",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestAPIGatewayDriver_Diff_NilCurrent(t *testing.T) {
	mock := &mockAPIGatewayClient{}
	d := drivers.NewAPIGatewayDriverWithClient(mock, "nyc3")

	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{Name: "my-gateway"}, nil)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=true when current is nil")
	}
}

func TestAPIGatewayDriver_Diff_NoChanges(t *testing.T) {
	mock := &mockAPIGatewayClient{}
	d := drivers.NewAPIGatewayDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{ProviderID: "app-gw-123"}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{Name: "my-gateway"}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=false when current exists")
	}
}

func TestAPIGatewayDriver_HealthCheck(t *testing.T) {
	mock := &mockAPIGatewayClient{app: testGatewayApp()}
	d := drivers.NewAPIGatewayDriverWithClient(mock, "nyc3")

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name: "my-gateway", ProviderID: "app-gw-123",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if !result.Healthy {
		t.Errorf("expected healthy gateway")
	}
}

func TestAPIGatewayDriver_HealthCheck_Unhealthy(t *testing.T) {
	app := &godo.App{
		ID:   "app-gw-123",
		Spec: &godo.AppSpec{Name: "my-gateway"},
		// ActiveDeployment nil => pending/unhealthy
	}
	mock := &mockAPIGatewayClient{app: app}
	d := drivers.NewAPIGatewayDriverWithClient(mock, "nyc3")

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name: "my-gateway", ProviderID: "app-gw-123",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if result.Healthy {
		t.Errorf("expected unhealthy when no active deployment")
	}
}
