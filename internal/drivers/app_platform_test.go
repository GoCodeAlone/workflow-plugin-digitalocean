package drivers_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// makeGodoErr builds a *godo.ErrorResponse with the given HTTP status code,
// matching what WrapGodoError inspects.
func makeGodoErr(statusCode int) error {
	return &godo.ErrorResponse{
		Response: &http.Response{StatusCode: statusCode},
		Message:  http.StatusText(statusCode),
	}
}

// mockAppClient is a mock implementation of AppPlatformClient.
type mockAppClient struct {
	app                    *godo.App
	err                    error
	listApps               []*godo.App // returned by List
	listErr                error       // error returned by List
	createDeploymentErr    error       // error returned by CreateDeployment
	createDeploymentCalled bool
	lastCreateDeployReq    *godo.DeploymentCreateRequest
	lastCreateReq          *godo.AppCreateRequest
	lastUpdateReq          *godo.AppUpdateRequest
}

func (m *mockAppClient) Create(_ context.Context, req *godo.AppCreateRequest) (*godo.App, *godo.Response, error) {
	m.lastCreateReq = req
	return m.app, nil, m.err
}
func (m *mockAppClient) Get(_ context.Context, _ string) (*godo.App, *godo.Response, error) {
	return m.app, nil, m.err
}
func (m *mockAppClient) List(_ context.Context, _ *godo.ListOptions) ([]*godo.App, *godo.Response, error) {
	return m.listApps, &godo.Response{}, m.listErr
}
func (m *mockAppClient) Update(_ context.Context, _ string, req *godo.AppUpdateRequest) (*godo.App, *godo.Response, error) {
	m.lastUpdateReq = req
	return m.app, nil, m.err
}
func (m *mockAppClient) CreateDeployment(_ context.Context, _ string, reqs ...*godo.DeploymentCreateRequest) (*godo.Deployment, *godo.Response, error) {
	m.createDeploymentCalled = true
	for _, r := range reqs {
		m.lastCreateDeployReq = r
	}
	return &godo.Deployment{ID: "dep-1"}, nil, m.createDeploymentErr
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
	if mock.createDeploymentCalled {
		t.Error("CreateDeployment should not be called on Update failure")
	}
}

func TestAppPlatformDriver_Update_TriggersCreateDeployment(t *testing.T) {
	mock := &mockAppClient{app: testApp()}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-app", ProviderID: "app-123",
	}, interfaces.ResourceSpec{
		Name:   "my-app",
		Config: map[string]any{"image": "registry.digitalocean.com/myrepo/myapp:v2"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !mock.createDeploymentCalled {
		t.Error("expected CreateDeployment to be called after Update")
	}
	if mock.lastCreateDeployReq == nil || !mock.lastCreateDeployReq.ForceBuild {
		t.Error("expected CreateDeployment called with ForceBuild=true")
	}
}

func TestAppPlatformDriver_Update_CreateDeploymentFails(t *testing.T) {
	mock := &mockAppClient{
		app:                 testApp(),
		createDeploymentErr: fmt.Errorf("deployment quota exceeded"),
	}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-app", ProviderID: "app-123",
	}, interfaces.ResourceSpec{
		Name:   "my-app",
		Config: map[string]any{"image": "registry.digitalocean.com/myrepo/myapp:v2"},
	})
	if err == nil {
		t.Fatal("expected error when CreateDeployment fails, got nil")
	}
	if !strings.Contains(err.Error(), "deployment quota exceeded") {
		t.Errorf("error should contain original message, got: %v", err)
	}
}

func TestAppPlatformDriver_Update_CreateDeploymentSentinelPropagates(t *testing.T) {
	mock := &mockAppClient{
		app:                 testApp(),
		createDeploymentErr: makeGodoErr(http.StatusTooManyRequests),
	}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-app", ProviderID: "app-123",
	}, interfaces.ResourceSpec{
		Name:   "my-app",
		Config: map[string]any{"image": "registry.digitalocean.com/myrepo/myapp:v2"},
	})
	if !errors.Is(err, interfaces.ErrRateLimited) {
		t.Errorf("expected ErrRateLimited sentinel, got: %v", err)
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

// ── HealthCheck deployment-phase tests ───────────────────────────────────────

func appWithPhases(active, inProgress, pending *godo.DeploymentPhase) *godo.App {
	app := &godo.App{ID: "app-999", Spec: &godo.AppSpec{Name: "phased-app"}}
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

func phasePtr(p godo.DeploymentPhase) *godo.DeploymentPhase { return &p }

func TestAppPlatformDriver_HealthCheck_Active(t *testing.T) {
	d := drivers.NewAppPlatformDriverWithClient(&mockAppClient{
		app: appWithPhases(phasePtr(godo.DeploymentPhase_Active), nil, nil),
	}, "nyc3")
	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "phased-app", ProviderID: "app-999"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Healthy {
		t.Errorf("expected Healthy=true for ACTIVE phase, got message: %q", result.Message)
	}
}

func TestAppPlatformDriver_HealthCheck_InProgress_Building(t *testing.T) {
	d := drivers.NewAppPlatformDriverWithClient(&mockAppClient{
		app: appWithPhases(nil, phasePtr(godo.DeploymentPhase_Building), nil),
	}, "nyc3")
	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "phased-app", ProviderID: "app-999"})
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

func TestAppPlatformDriver_HealthCheck_InProgress_Deploying(t *testing.T) {
	d := drivers.NewAppPlatformDriverWithClient(&mockAppClient{
		app: appWithPhases(nil, phasePtr(godo.DeploymentPhase_Deploying), nil),
	}, "nyc3")
	result, _ := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "phased-app", ProviderID: "app-999"})
	if result.Healthy {
		t.Error("expected Healthy=false while DEPLOYING")
	}
	if !strings.Contains(result.Message, "in progress") {
		t.Errorf("message should contain 'in progress', got: %q", result.Message)
	}
}

func TestAppPlatformDriver_HealthCheck_InProgress_Failed(t *testing.T) {
	d := drivers.NewAppPlatformDriverWithClient(&mockAppClient{
		app: appWithPhases(nil, phasePtr(godo.DeploymentPhase_Error), nil),
	}, "nyc3")
	result, _ := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "phased-app", ProviderID: "app-999"})
	if result.Healthy {
		t.Error("expected Healthy=false for ERROR phase")
	}
	if !strings.Contains(result.Message, "failed") {
		t.Errorf("message should contain 'failed', got: %q", result.Message)
	}
}

func TestAppPlatformDriver_HealthCheck_PendingDeployment(t *testing.T) {
	d := drivers.NewAppPlatformDriverWithClient(&mockAppClient{
		app: appWithPhases(nil, nil, phasePtr(godo.DeploymentPhase_PendingBuild)),
	}, "nyc3")
	result, _ := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "phased-app", ProviderID: "app-999"})
	if result.Healthy {
		t.Error("expected Healthy=false with only a pending deployment")
	}
	if !strings.Contains(result.Message, "queued") {
		t.Errorf("message should contain 'queued', got: %q", result.Message)
	}
}

func TestAppPlatformDriver_HealthCheck_NoDeployment(t *testing.T) {
	d := drivers.NewAppPlatformDriverWithClient(&mockAppClient{
		app: appWithPhases(nil, nil, nil),
	}, "nyc3")
	result, _ := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "phased-app", ProviderID: "app-999"})
	if result.Healthy {
		t.Error("expected Healthy=false with no deployments")
	}
	if !strings.Contains(result.Message, "no deployment") {
		t.Errorf("message should contain 'no deployment', got: %q", result.Message)
	}
}

func TestAppPlatformDriver_HealthCheck_InProgress_UnknownPhase(t *testing.T) {
	d := drivers.NewAppPlatformDriverWithClient(&mockAppClient{
		app: appWithPhases(nil, phasePtr(godo.DeploymentPhase_Unknown), nil),
	}, "nyc3")
	result, _ := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "phased-app", ProviderID: "app-999"})
	if result.Healthy {
		t.Error("expected Healthy=false for unknown phase")
	}
	if !strings.Contains(result.Message, "unknown phase") {
		t.Errorf("message should contain 'unknown phase', got: %q", result.Message)
	}
}

// ── ParseImageRef unit tests ──────────────────────────────────────────────────

func TestParseImageRef_DOCR(t *testing.T) {
	spec, err := drivers.ParseImageRef("registry.digitalocean.com/foo/bar:v1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.RegistryType != godo.ImageSourceSpecRegistryType_DOCR {
		t.Errorf("RegistryType = %q, want DOCR", spec.RegistryType)
	}
	if spec.Registry != "" {
		t.Errorf("Registry = %q, want empty (must be empty for DOCR)", spec.Registry)
	}
	if spec.Repository != "bar" {
		t.Errorf("Repository = %q, want %q", spec.Repository, "bar")
	}
	if spec.Tag != "v1" {
		t.Errorf("Tag = %q, want %q", spec.Tag, "v1")
	}
}

func TestParseImageRef_GHCR(t *testing.T) {
	spec, err := drivers.ParseImageRef("ghcr.io/org/app:sha256abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.RegistryType != godo.ImageSourceSpecRegistryType_Ghcr {
		t.Errorf("RegistryType = %q, want GHCR", spec.RegistryType)
	}
	if spec.Registry != "org" {
		t.Errorf("Registry = %q, want %q", spec.Registry, "org")
	}
	if spec.Repository != "app" {
		t.Errorf("Repository = %q, want %q", spec.Repository, "app")
	}
	if spec.Tag != "sha256abc" {
		t.Errorf("Tag = %q, want %q", spec.Tag, "sha256abc")
	}
}

func TestParseImageRef_DockerHub_Explicit(t *testing.T) {
	spec, err := drivers.ParseImageRef("docker.io/org/app:tag")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.RegistryType != godo.ImageSourceSpecRegistryType_DockerHub {
		t.Errorf("RegistryType = %q, want DOCKER_HUB", spec.RegistryType)
	}
	if spec.Registry != "org" {
		t.Errorf("Registry = %q, want %q", spec.Registry, "org")
	}
	if spec.Repository != "app" {
		t.Errorf("Repository = %q, want %q", spec.Repository, "app")
	}
	if spec.Tag != "tag" {
		t.Errorf("Tag = %q, want %q", spec.Tag, "tag")
	}
}

func TestParseImageRef_DockerHub_Bare(t *testing.T) {
	spec, err := drivers.ParseImageRef("org/app:tag")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.RegistryType != godo.ImageSourceSpecRegistryType_DockerHub {
		t.Errorf("RegistryType = %q, want DOCKER_HUB", spec.RegistryType)
	}
	if spec.Registry != "org" {
		t.Errorf("Registry = %q, want %q", spec.Registry, "org")
	}
	if spec.Repository != "app" {
		t.Errorf("Repository = %q, want %q", spec.Repository, "app")
	}
}

func TestParseImageRef_NoTag(t *testing.T) {
	// Missing tag defaults to "latest".
	spec, err := drivers.ParseImageRef("registry.digitalocean.com/myregistry/myapp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Tag != "latest" {
		t.Errorf("Tag = %q, want %q (default)", spec.Tag, "latest")
	}
}

func TestParseImageRef_Malformed(t *testing.T) {
	cases := []string{
		"",                                     // empty
		"justarepo",                            // no org/registry prefix
		"registry.digitalocean.com/onlyone",    // DOCR with only one path segment
		"ghcr.io/onlyone",                      // GHCR with only org, no repo
		"docker.io/onlyone",                    // docker.io with only one path segment
	}
	for _, tc := range cases {
		_, err := drivers.ParseImageRef(tc)
		if err == nil {
			t.Errorf("ParseImageRef(%q): expected error, got nil", tc)
		}
	}
}

// ── Create with nested image spec ────────────────────────────────────────────

func TestAppPlatformDriver_Create_BuildsNestedImageSpec(t *testing.T) {
	mock := &mockAppClient{app: testApp()}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "bmw-app",
		Config: map[string]any{
			"image": "registry.digitalocean.com/bmw-registry/buymywishlist:abc123",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if mock.lastCreateReq == nil {
		t.Fatal("no create request captured")
	}
	svc := mock.lastCreateReq.Spec.Services[0]
	img := svc.Image
	if img == nil {
		t.Fatal("service Image is nil")
	}
	if img.RegistryType != godo.ImageSourceSpecRegistryType_DOCR {
		t.Errorf("RegistryType = %q, want DOCR", img.RegistryType)
	}
	if img.Registry != "" {
		t.Errorf("Registry = %q, want empty (must be empty for DOCR)", img.Registry)
	}
	if img.Repository != "buymywishlist" {
		t.Errorf("Repository = %q, want %q", img.Repository, "buymywishlist")
	}
	if img.Tag != "abc123" {
		t.Errorf("Tag = %q, want %q", img.Tag, "abc123")
	}
}

func TestAppPlatformDriver_Update_BuildsNestedImageSpec(t *testing.T) {
	mock := &mockAppClient{app: testApp()}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "bmw-app", ProviderID: "app-123",
	}, interfaces.ResourceSpec{
		Name: "bmw-app",
		Config: map[string]any{
			"image": "registry.digitalocean.com/bmw-registry/buymywishlist:def456",
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	if mock.lastUpdateReq == nil {
		t.Fatal("no update request captured")
	}
	svc := mock.lastUpdateReq.Spec.Services[0]
	img := svc.Image
	if img == nil {
		t.Fatal("service Image is nil")
	}
	if img.RegistryType != godo.ImageSourceSpecRegistryType_DOCR {
		t.Errorf("RegistryType = %q, want DOCR", img.RegistryType)
	}
	if img.Repository != "buymywishlist" {
		t.Errorf("Repository = %q, want %q", img.Repository, "buymywishlist")
	}
	if img.Tag != "def456" {
		t.Errorf("Tag = %q, want %q", img.Tag, "def456")
	}
}

// ── Read by name ─────────────────────────────────────────────────────────────

func TestAppPlatformDriver_Read_ByName_Found(t *testing.T) {
	app := &godo.App{
		ID:      "app-456",
		LiveURL: "https://bmw.example.com",
		Spec:    &godo.AppSpec{Name: "bmw-app"},
		ActiveDeployment: &godo.Deployment{
			Phase: godo.DeploymentPhase_Active,
		},
	}
	mock := &mockAppClient{listApps: []*godo.App{app}}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	out, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name: "bmw-app",
		// ProviderID intentionally empty — should trigger name-based lookup.
	})
	if err != nil {
		t.Fatalf("Read by name: unexpected error: %v", err)
	}
	if out.ProviderID != "app-456" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "app-456")
	}
	if out.Name != "bmw-app" {
		t.Errorf("Name = %q, want %q", out.Name, "bmw-app")
	}
	if out.Status != "running" {
		t.Errorf("Status = %q, want %q", out.Status, "running")
	}
}

func TestAppPlatformDriver_Read_ByName_NotFound(t *testing.T) {
	mock := &mockAppClient{listApps: []*godo.App{}} // empty list
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	_, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name: "missing-app",
	})
	if err == nil {
		t.Fatal("expected error for unknown name, got nil")
	}
	if !errors.Is(err, drivers.ErrResourceNotFound) {
		t.Errorf("expected ErrResourceNotFound, got: %v", err)
	}
}

func TestAppPlatformDriver_Read_ByID_StillWorks(t *testing.T) {
	mock := &mockAppClient{app: testApp()}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	out, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name:       "my-app",
		ProviderID: "app-123",
	})
	if err != nil {
		t.Fatalf("Read by ID: unexpected error: %v", err)
	}
	if out.ProviderID != "app-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "app-123")
	}
}

func TestAppPlatformDriver_Create_NestedMapImageSpec(t *testing.T) {
	mock := &mockAppClient{app: testApp()}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "bmw-app",
		Config: map[string]any{
			"image": map[string]any{
				"registry_type": "DOCR",
				"repository":    "buymywishlist",
				"tag":           "v2",
			},
		},
	})
	if err != nil {
		t.Fatalf("Create with nested map: %v", err)
	}
	svc := mock.lastCreateReq.Spec.Services[0]
	img := svc.Image
	if img == nil {
		t.Fatal("service Image is nil")
	}
	if img.RegistryType != godo.ImageSourceSpecRegistryType_DOCR {
		t.Errorf("RegistryType = %q, want DOCR", img.RegistryType)
	}
	if img.Repository != "buymywishlist" {
		t.Errorf("Repository = %q, want %q", img.Repository, "buymywishlist")
	}
	if img.Tag != "v2" {
		t.Errorf("Tag = %q, want %q", img.Tag, "v2")
	}
}
