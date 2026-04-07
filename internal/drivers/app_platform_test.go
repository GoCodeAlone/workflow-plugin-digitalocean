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
	app           *godo.App
	err           error
	lastCreateReq *godo.AppCreateRequest
	lastUpdateReq *godo.AppUpdateRequest
}

func (m *mockAppClient) Create(_ context.Context, req *godo.AppCreateRequest) (*godo.App, *godo.Response, error) {
	m.lastCreateReq = req
	return m.app, nil, m.err
}
func (m *mockAppClient) Get(_ context.Context, _ string) (*godo.App, *godo.Response, error) {
	return m.app, nil, m.err
}
func (m *mockAppClient) Update(_ context.Context, _ string, req *godo.AppUpdateRequest) (*godo.App, *godo.Response, error) {
	m.lastUpdateReq = req
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

func TestAppPlatformDriver_Create_EnvVars(t *testing.T) {
	mock := &mockAppClient{app: testApp()}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-app",
		Config: map[string]any{
			"image": "registry.digitalocean.com/myrepo/myapp:v1",
			"env_vars": map[string]any{
				"SESSION_STORE": "pg",
				"GRPC_PORT":     "8080",
			},
			"secret_env_vars": map[string]any{
				"DATABASE_URL": "postgres://user:pass@host/db",
				"JWT_SECRET":   "s3cr3t",
			},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if mock.lastCreateReq == nil {
		t.Fatal("no create request captured")
	}
	svc := mock.lastCreateReq.Spec.Services[0]
	if len(svc.Envs) != 4 {
		t.Fatalf("expected 4 env vars, got %d", len(svc.Envs))
	}
	envMap := make(map[string]*godo.AppVariableDefinition, len(svc.Envs))
	for _, e := range svc.Envs {
		envMap[e.Key] = e
	}
	if envMap["SESSION_STORE"] == nil || envMap["SESSION_STORE"].Value != "pg" {
		t.Errorf("SESSION_STORE not set correctly")
	}
	if envMap["DATABASE_URL"] == nil || envMap["DATABASE_URL"].Type != godo.AppVariableType_Secret {
		t.Errorf("DATABASE_URL not marked as secret")
	}
}

func TestAppPlatformDriver_Update_EnvVars(t *testing.T) {
	mock := &mockAppClient{app: testApp()}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-app", ProviderID: "app-123",
	}, interfaces.ResourceSpec{
		Name: "my-app",
		Config: map[string]any{
			"image": "registry.digitalocean.com/myrepo/myapp:v2",
			"env_vars": map[string]any{
				"HEALTH_PORT": "8080",
			},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if mock.lastUpdateReq == nil {
		t.Fatal("no update request captured")
	}
	svc := mock.lastUpdateReq.Spec.Services[0]
	if len(svc.Envs) != 1 {
		t.Fatalf("expected 1 env var, got %d", len(svc.Envs))
	}
	if svc.Envs[0].Key != "HEALTH_PORT" || svc.Envs[0].Value != "8080" {
		t.Errorf("HEALTH_PORT env var not set correctly")
	}
}

func TestAppPlatformDriver_Create_NoEnvVars(t *testing.T) {
	mock := &mockAppClient{app: testApp()}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-app",
		Config: map[string]any{"image": "registry.digitalocean.com/myrepo/myapp:v1"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	svc := mock.lastCreateReq.Spec.Services[0]
	if len(svc.Envs) != 0 {
		t.Errorf("expected no env vars when not specified, got %d", len(svc.Envs))
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
