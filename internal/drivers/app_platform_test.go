package drivers_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// mockAppClient is a mock implementation of AppPlatformClient.
type mockAppClient struct {
	app *godo.App
	err error
}

func (m *mockAppClient) Create(_ context.Context, _ *godo.AppCreateRequest) (*godo.App, *godo.Response, error) {
	return m.app, nil, m.err
}
func (m *mockAppClient) Get(_ context.Context, _ string) (*godo.App, *godo.Response, error) {
	return m.app, nil, m.err
}
func (m *mockAppClient) Update(_ context.Context, _ string, _ *godo.AppUpdateRequest) (*godo.App, *godo.Response, error) {
	return m.app, nil, m.err
}
func (m *mockAppClient) Delete(_ context.Context, _ string) (*godo.Response, error) {
	return nil, m.err
}

func testApp() *godo.App {
	return &godo.App{
		ID:      "app-123",
		LiveURL: "https://test.app.example.com",
		Spec:    &godo.AppSpec{Name: "my-app"},
		ActiveDeployment: &godo.Deployment{
			Phase: godo.DeploymentPhase_Active,
		},
	}
}

func TestAppPlatformDriver_Create(t *testing.T) {
	mock := &mockAppClient{app: testApp()}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	spec := interfaces.ResourceSpec{
		Name: "my-app",
		Type: "infra.container_service",
		Config: map[string]any{
			"image":          "registry.digitalocean.com/myrepo/myapp:v1",
			"instance_count": 2,
		},
	}

	out, err := d.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID != "app-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "app-123")
	}
	if out.Status != "running" {
		t.Errorf("Status = %q, want %q", out.Status, "running")
	}
}

func TestAppPlatformDriver_Read(t *testing.T) {
	mock := &mockAppClient{app: testApp()}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	out, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name:       "my-app",
		ProviderID: "app-123",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.ProviderID != "app-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "app-123")
	}
}

func TestAppPlatformDriver_Delete(t *testing.T) {
	mock := &mockAppClient{app: testApp()}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name:       "my-app",
		ProviderID: "app-123",
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestAppPlatformDriver_HealthCheck(t *testing.T) {
	mock := &mockAppClient{app: testApp()}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name:       "my-app",
		ProviderID: "app-123",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if !result.Healthy {
		t.Errorf("expected healthy")
	}
}

func TestAppPlatformDriver_Create_Error(t *testing.T) {
	mock := &mockAppClient{err: fmt.Errorf("api failure")}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-app",
		Config: map[string]any{"image": "registry.digitalocean.com/myrepo/myapp:v1"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestAppPlatformDriver_Update_Success(t *testing.T) {
	mock := &mockAppClient{app: testApp()}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	out, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-app", ProviderID: "app-123",
	}, interfaces.ResourceSpec{
		Name:   "my-app",
		Config: map[string]any{"image": "registry.digitalocean.com/myrepo/myapp:v2", "instance_count": 3},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.ProviderID != "app-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "app-123")
	}
}

func TestAppPlatformDriver_Update_Error(t *testing.T) {
	mock := &mockAppClient{err: fmt.Errorf("update failed")}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-app", ProviderID: "app-123",
	}, interfaces.ResourceSpec{
		Name:   "my-app",
		Config: map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestAppPlatformDriver_Delete_Error(t *testing.T) {
	mock := &mockAppClient{err: fmt.Errorf("delete failed")}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "my-app", ProviderID: "app-123",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestAppPlatformDriver_Diff_NilCurrent(t *testing.T) {
	mock := &mockAppClient{}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{Name: "my-app"}, nil)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate when current is nil")
	}
}

func TestAppPlatformDriver_Diff_HasChanges(t *testing.T) {
	mock := &mockAppClient{}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{"image": "registry.digitalocean.com/myrepo/myapp:v1"},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{"image": "registry.digitalocean.com/myrepo/myapp:v2"},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=true for image change")
	}
}

func TestAppPlatformDriver_Diff_NoChanges(t *testing.T) {
	mock := &mockAppClient{}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{"image": "registry.digitalocean.com/myrepo/myapp:v1"},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{"image": "registry.digitalocean.com/myrepo/myapp:v1"},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=false when image unchanged")
	}
}

func TestAppPlatformDriver_HealthCheck_Unhealthy(t *testing.T) {
	app := &godo.App{
		ID:   "app-123",
		Spec: &godo.AppSpec{Name: "my-app"},
		// ActiveDeployment nil => pending/unhealthy
	}
	mock := &mockAppClient{app: app}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name: "my-app", ProviderID: "app-123",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if result.Healthy {
		t.Errorf("expected unhealthy when no active deployment")
	}
}
