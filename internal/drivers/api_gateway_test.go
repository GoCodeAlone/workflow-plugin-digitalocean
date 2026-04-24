package drivers_test

import (
	"context"
	"fmt"
	"strings"
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

// ── APIGateway HealthCheck deployment-phase tests ────────────────────────────

func gwAppWithPhases(active, inProgress, pending *godo.DeploymentPhase) *godo.App {
	app := &godo.App{ID: "app-gw-999", Spec: &godo.AppSpec{Name: "phased-gw"}}
	if active != nil {
		app.ActiveDeployment = &godo.Deployment{Phase: *active}
	}
	if inProgress != nil {
		app.InProgressDeployment = &godo.Deployment{Phase: *inProgress}
	}
	if pending != nil {
		app.PendingDeployment = &godo.Deployment{Phase: *pending}
	}
	return app
}

func gwPhasePtr(p godo.DeploymentPhase) *godo.DeploymentPhase { return &p }

func TestAPIGatewayDriver_HealthCheck_InProgress_Building(t *testing.T) {
	d := drivers.NewAPIGatewayDriverWithClient(&mockAPIGatewayClient{
		app: gwAppWithPhases(nil, gwPhasePtr(godo.DeploymentPhase_Building), nil),
	}, "nyc3")
	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "phased-gw", ProviderID: "app-gw-999"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Healthy {
		t.Error("expected Healthy=false while BUILDING")
	}
	if !strings.Contains(result.Message, "in progress") {
		t.Errorf("message should contain 'in progress', got: %q", result.Message)
	}
}

func TestAPIGatewayDriver_HealthCheck_InProgress_Deploying(t *testing.T) {
	d := drivers.NewAPIGatewayDriverWithClient(&mockAPIGatewayClient{
		app: gwAppWithPhases(nil, gwPhasePtr(godo.DeploymentPhase_Deploying), nil),
	}, "nyc3")
	result, _ := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "phased-gw", ProviderID: "app-gw-999"})
	if result.Healthy {
		t.Error("expected Healthy=false while DEPLOYING")
	}
	if !strings.Contains(result.Message, "in progress") {
		t.Errorf("message should contain 'in progress', got: %q", result.Message)
	}
}

func TestAPIGatewayDriver_HealthCheck_InProgress_Failed(t *testing.T) {
	d := drivers.NewAPIGatewayDriverWithClient(&mockAPIGatewayClient{
		app: gwAppWithPhases(nil, gwPhasePtr(godo.DeploymentPhase_Error), nil),
	}, "nyc3")
	result, _ := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "phased-gw", ProviderID: "app-gw-999"})
	if result.Healthy {
		t.Error("expected Healthy=false for ERROR phase")
	}
	if !strings.Contains(result.Message, "failed") {
		t.Errorf("message should contain 'failed', got: %q", result.Message)
	}
}

func TestAPIGatewayDriver_HealthCheck_PendingDeployment(t *testing.T) {
	d := drivers.NewAPIGatewayDriverWithClient(&mockAPIGatewayClient{
		app: gwAppWithPhases(nil, nil, gwPhasePtr(godo.DeploymentPhase_PendingBuild)),
	}, "nyc3")
	result, _ := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "phased-gw", ProviderID: "app-gw-999"})
	if result.Healthy {
		t.Error("expected Healthy=false with only a pending deployment")
	}
	if !strings.Contains(result.Message, "queued") {
		t.Errorf("message should contain 'queued', got: %q", result.Message)
	}
}

func TestAPIGatewayDriver_HealthCheck_NoDeployment(t *testing.T) {
	d := drivers.NewAPIGatewayDriverWithClient(&mockAPIGatewayClient{
		app: gwAppWithPhases(nil, nil, nil),
	}, "nyc3")
	result, _ := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "phased-gw", ProviderID: "app-gw-999"})
	if result.Healthy {
		t.Error("expected Healthy=false with no deployments")
	}
	if !strings.Contains(result.Message, "no deployment") {
		t.Errorf("message should contain 'no deployment', got: %q", result.Message)
	}
}

func TestAPIGatewayDriver_HealthCheck_InProgress_UnknownPhase(t *testing.T) {
	d := drivers.NewAPIGatewayDriverWithClient(&mockAPIGatewayClient{
		app: gwAppWithPhases(nil, gwPhasePtr(godo.DeploymentPhase_Unknown), nil),
	}, "nyc3")
	result, _ := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "phased-gw", ProviderID: "app-gw-999"})
	if result.Healthy {
		t.Error("expected Healthy=false for unknown phase")
	}
	if !strings.Contains(result.Message, "unknown phase") {
		t.Errorf("message should contain 'unknown phase', got: %q", result.Message)
	}
}

func TestAPIGatewayDriver_Create_EmptyIDFromAPI(t *testing.T) {
	// API returns success but app has empty ID — guard must reject it.
	mock := &mockAPIGatewayClient{app: &godo.App{Spec: &godo.AppSpec{Name: "my-gateway"}}}
	d := drivers.NewAPIGatewayDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-gateway",
		Config: map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error for empty ProviderID, got nil")
	}
}

func TestAPIGatewayDriver_Create_ProviderIDIsUUID(t *testing.T) {
	// ProviderID must be the API-assigned UUID, not the resource name.
	mock := &mockAPIGatewayClient{app: testGatewayApp()}
	d := drivers.NewAPIGatewayDriverWithClient(mock, "nyc3")

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-gateway",
		Config: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID == "my-gateway" {
		t.Errorf("ProviderID must not equal spec.Name %q; got %q", "my-gateway", out.ProviderID)
	}
	if out.ProviderID == "" {
		t.Errorf("ProviderID must not be empty")
	}
}
