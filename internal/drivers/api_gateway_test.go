package drivers_test

import (
	"context"
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
