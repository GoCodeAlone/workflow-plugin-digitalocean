package drivers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	listApps               []*godo.App        // returned by List
	listErr                error              // error returned by List
	deployments            []*godo.Deployment // returned by ListDeployments
	listDeploymentsErr     error              // error returned by ListDeployments
	createDeploymentCalled bool
	lastCreateReq          *godo.AppCreateRequest
	lastUpdateReq          *godo.AppUpdateRequest
	getLogsResult          *godo.AppLogs // returned by GetLogs
	getLogsErr             error         // error returned by GetLogs
	getLogsRequests        []getLogsCall // recorded GetLogs calls
}

// getLogsCall records a single GetLogs invocation for assertion in tests.
type getLogsCall struct {
	appID        string
	deploymentID string
	component    string
	logType      godo.AppLogType
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
	return &godo.Deployment{ID: "dep-1"}, nil, nil
}
func (m *mockAppClient) ListDeployments(_ context.Context, _ string, _ *godo.ListOptions) ([]*godo.Deployment, *godo.Response, error) {
	return m.deployments, &godo.Response{}, m.listDeploymentsErr
}
func (m *mockAppClient) Delete(_ context.Context, _ string) (*godo.Response, error) {
	return nil, m.err
}
func (m *mockAppClient) GetLogs(_ context.Context, appID, deploymentID, component string, logType godo.AppLogType, _ bool, _ int) (*godo.AppLogs, *godo.Response, error) {
	m.getLogsRequests = append(m.getLogsRequests, getLogsCall{
		appID:        appID,
		deploymentID: deploymentID,
		component:    component,
		logType:      logType,
	})
	return m.getLogsResult, nil, m.getLogsErr
}

func testApp() *godo.App {
	return &godo.App{
		ID:      "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
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
	if out.ProviderID != "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5")
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
		ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.ProviderID != "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5")
	}
}

func TestAppPlatformDriver_Delete(t *testing.T) {
	mock := &mockAppClient{app: testApp()}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name:       "my-app",
		ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
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
		ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
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
		Name: "my-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
	}, interfaces.ResourceSpec{
		Name:   "my-app",
		Config: map[string]any{"image": "registry.digitalocean.com/myrepo/myapp:v2", "instance_count": 3},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.ProviderID != "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5")
	}
}

func TestAppPlatformDriver_Update_Error(t *testing.T) {
	mock := &mockAppClient{err: fmt.Errorf("update failed")}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
	}, interfaces.ResourceSpec{
		Name:   "my-app",
		Config: map[string]any{"image": "registry.digitalocean.com/myrepo/myapp:v2"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if mock.lastUpdateReq == nil {
		t.Fatal("expected Update API to be called")
	}
	if mock.createDeploymentCalled {
		t.Error("CreateDeployment should not be called on Update failure")
	}
}

func TestAppPlatformDriver_Update_ErrorSentinelPropagates(t *testing.T) {
	mock := &mockAppClient{err: makeGodoErr(http.StatusTooManyRequests)}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
	}, interfaces.ResourceSpec{
		Name:   "my-app",
		Config: map[string]any{"image": "registry.digitalocean.com/myrepo/myapp:v2"},
	})
	if !errors.Is(err, interfaces.ErrRateLimited) {
		t.Errorf("expected ErrRateLimited sentinel, got: %v", err)
	}
	if mock.lastUpdateReq == nil {
		t.Fatal("expected Update API to be called")
	}
	if mock.createDeploymentCalled {
		t.Error("CreateDeployment should not be called on Update failure")
	}
}

func TestAppPlatformDriver_Update_DoesNotCreateDuplicateDeployment(t *testing.T) {
	mock := &mockAppClient{app: testApp()}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
	}, interfaces.ResourceSpec{
		Name:   "my-app",
		Config: map[string]any{"image": "registry.digitalocean.com/myrepo/myapp:v2"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if mock.lastUpdateReq == nil {
		t.Fatal("expected Update API to be called")
	}
	if mock.createDeploymentCalled {
		t.Error("CreateDeployment should not be called after Apps.Update; DigitalOcean already creates an app-spec deployment for the update")
	}
}

func TestAppPlatformDriver_Update_SendsPreDeployJobRunCommand(t *testing.T) {
	const repairCommand = "/workflow-migrate repair-dirty --expected-dirty-version 20260426000005 --force-version 20260426000004 --up-if-clean --source-dir /migrations --confirm-force FORCE_MIGRATION_METADATA"
	mock := &mockAppClient{app: testApp()}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
	}, interfaces.ResourceSpec{
		Name: "my-app",
		Config: map[string]any{
			"image": "registry.digitalocean.com/myrepo/myapp:v2",
			"jobs": []any{
				map[string]any{
					"name":        "migrate",
					"kind":        "pre_deploy",
					"image":       "registry.digitalocean.com/myrepo/workflow-migrate:v2",
					"run_command": repairCommand,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if mock.lastUpdateReq == nil || mock.lastUpdateReq.Spec == nil {
		t.Fatal("expected Apps.Update request to be captured")
	}
	if len(mock.lastUpdateReq.Spec.Jobs) != 1 {
		t.Fatalf("Update jobs = %d, want 1", len(mock.lastUpdateReq.Spec.Jobs))
	}
	job := mock.lastUpdateReq.Spec.Jobs[0]
	if job.Name != "migrate" {
		t.Fatalf("Update job Name = %q, want migrate", job.Name)
	}
	if job.Kind != godo.AppJobSpecKind_PreDeploy {
		t.Fatalf("Update job Kind = %q, want PRE_DEPLOY", job.Kind)
	}
	if job.Image == nil {
		t.Fatal("Update job Image is nil")
	}
	if got := job.Image.Repository; got != "workflow-migrate" {
		t.Fatalf("Update job image repository = %q, want workflow-migrate", got)
	}
	if got := job.Image.Tag; got != "v2" {
		t.Fatalf("Update job image tag = %q, want v2", got)
	}
	if got := job.RunCommand; got != repairCommand {
		t.Fatalf("Update job RunCommand = %q, want %q", got, repairCommand)
	}
	if mock.createDeploymentCalled {
		t.Error("CreateDeployment should not be called for a pre-deploy job update")
	}
}

func TestAppPlatformDriver_Delete_Error(t *testing.T) {
	mock := &mockAppClient{err: fmt.Errorf("delete failed")}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "my-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
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

// TestAppPlatformDriver_Diff_RegionChangeForcesReplace covers issue #70:
// App Platform region is a Create-only field on godo.AppSpec — UpdateApp
// does not accept region changes. Region drift must surface as ForceNew so
// dependents (vpc_ref to a region-locked VPC) get correctly recreated too.
//
// Region values use App Platform regional slugs ("nyc", "sfo") rather than
// Droplet/VPC datacenter slugs ("nyc1", "nyc3", "sfo3"); the latter now fail
// validateAppPlatformRegion at plan-time.
func TestAppPlatformDriver_Diff_RegionChangeForcesReplace(t *testing.T) {
	mock := &mockAppClient{}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"image":  "registry.digitalocean.com/myrepo/myapp:v1",
			"region": "nyc",
		},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"image":  "registry.digitalocean.com/myrepo/myapp:v1",
			"region": "sfo",
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsReplace {
		t.Fatal("region change must force replace; NeedsReplace=false")
	}
	var found bool
	for _, c := range result.Changes {
		if c.Path == "region" && c.Old == "nyc" && c.New == "sfo" && c.ForceNew {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected FieldChange{Path:region, Old:nyc, New:sfo, ForceNew:true}; got %+v", result.Changes)
	}
}

// TestAppPlatformDriver_Diff_RegionEmptyCurrentSkipped covers the
// upgrade-safe guard: Apps in state from earlier plugin versions (when
// appOutput didn't include region) must not false-positive on the first
// plan after upgrade — they'll Read on next apply to populate the field.
func TestAppPlatformDriver_Diff_RegionEmptyCurrentSkipped(t *testing.T) {
	mock := &mockAppClient{}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"image": "registry.digitalocean.com/myrepo/myapp:v1",
			// no region — represents pre-region-output state
		},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"image":  "registry.digitalocean.com/myrepo/myapp:v1",
			"region": "sfo",
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsReplace {
		t.Error("empty curRegion should not force replace; got NeedsReplace=true")
	}
	for _, c := range result.Changes {
		if c.Path == "region" {
			t.Errorf("empty curRegion should not emit a region change; got %+v", c)
		}
	}
}

// TestAppPlatformDriver_Diff_DetectsExposeChange covers quality-review Finding
// 1: changing `expose` (a security-relevant toggle) on an existing service
// must produce a Plan action — Diff cannot silently no-op the way the
// pre-F4 image-only Diff did.
//
// Today appOutput populates Outputs["expose"] from the live AppSpec
// (HTTPPort==0 && len(InternalPorts)>0 → "internal" else "public"), and
// Diff compares it against the desired config.
func TestAppPlatformDriver_Diff_DetectsExposeChange_PublicToInternal(t *testing.T) {
	mock := &mockAppClient{}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"image":  "registry.digitalocean.com/myrepo/myapp:v1",
			"expose": "public",
		},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"image":     "registry.digitalocean.com/myrepo/myapp:v1",
			"http_port": 4222,
			"expose":    "internal",
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Fatal("expected NeedsUpdate=true when expose toggles public→internal")
	}
	// At least one change must reference "expose" so the user sees what changed.
	found := false
	for _, c := range result.Changes {
		if c.Path == "expose" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a FieldChange with Path=\"expose\"; got %+v", result.Changes)
	}
}

func TestAppPlatformDriver_Diff_DetectsExposeChange_InternalToPublic(t *testing.T) {
	mock := &mockAppClient{}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"image":  "registry.digitalocean.com/myrepo/myapp:v1",
			"expose": "internal",
		},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"image":     "registry.digitalocean.com/myrepo/myapp:v1",
			"http_port": 8080,
			"expose":    "public",
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Fatal("expected NeedsUpdate=true when expose toggles internal→public")
	}
}

func TestAppPlatformDriver_AppOutput_RoutesDerivedFromAppSpec(t *testing.T) {
	mock := &mockAppClient{app: &godo.App{
		ID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
		Spec: &godo.AppSpec{
			Name: "my-app",
			Services: []*godo.AppServiceSpec{{
				Name: "my-app",
				Routes: []*godo.AppRouteSpec{{
					Path:               "/",
					PreservePathPrefix: true,
				}},
			}},
		},
		ActiveDeployment: &godo.Deployment{Phase: godo.DeploymentPhase_Active},
	}}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	out, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name:       "my-app",
		ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	routes, _ := out.Outputs["routes"].([]any)
	if len(routes) != 1 {
		t.Fatalf("expected one route in outputs, got %#v", out.Outputs["routes"])
	}
	route, _ := routes[0].(map[string]any)
	if route["path"] != "/" || route["preserve_path_prefix"] != true {
		t.Fatalf("unexpected route output: %#v", route)
	}
}

func TestAppPlatformDriver_AppOutput_RoutesDerivedFromIngressSpec(t *testing.T) {
	path := "/"
	otherPath := "/sidecar"
	mock := &mockAppClient{app: &godo.App{
		ID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
		Spec: &godo.AppSpec{
			Name: "my-app",
			Services: []*godo.AppServiceSpec{{
				Name: "my-app",
			}},
			Ingress: &godo.AppIngressSpec{Rules: []*godo.AppIngressSpecRule{{
				Match: &godo.AppIngressSpecRuleMatch{
					Path: &godo.AppIngressSpecRuleStringMatch{Prefix: &path},
				},
				Component: &godo.AppIngressSpecRuleRoutingComponent{
					Name:               "my-app",
					PreservePathPrefix: true,
				},
			}, {
				Match: &godo.AppIngressSpecRuleMatch{
					Path: &godo.AppIngressSpecRuleStringMatch{Prefix: &otherPath},
				},
				Component: &godo.AppIngressSpecRuleRoutingComponent{
					Name: "sidecar",
				},
			}}},
		},
		ActiveDeployment: &godo.Deployment{Phase: godo.DeploymentPhase_Active},
	}}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	out, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name:       "my-app",
		ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	routes, _ := out.Outputs["routes"].([]any)
	if len(routes) != 1 {
		t.Fatalf("expected one route in outputs, got %#v", out.Outputs["routes"])
	}
	route, _ := routes[0].(map[string]any)
	if route["path"] != "/" || route["preserve_path_prefix"] != true {
		t.Fatalf("unexpected route output: %#v", route)
	}
}

func TestAppPlatformDriver_Diff_DetectsRouteAdd(t *testing.T) {
	mock := &mockAppClient{}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"image":  "registry.digitalocean.com/myrepo/myapp:v1",
			"expose": "public",
			"routes": []any{},
		},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"image": "registry.digitalocean.com/myrepo/myapp:v1",
			"routes": []any{
				map[string]any{"path": "/"},
			},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Fatal("expected NeedsUpdate=true when desired route is missing from current app")
	}
	if !hasChangePath(result.Changes, "routes") {
		t.Fatalf("expected routes FieldChange, got %+v", result.Changes)
	}
}

func TestAppPlatformDriver_Diff_DetectsRouteRemoval(t *testing.T) {
	mock := &mockAppClient{}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"image":  "registry.digitalocean.com/myrepo/myapp:v1",
			"expose": "public",
			"routes": []any{
				map[string]any{"path": "/", "preserve_path_prefix": false},
			},
		},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"image":  "registry.digitalocean.com/myrepo/myapp:v1",
			"routes": []any{},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Fatal("expected NeedsUpdate=true when desired routes explicitly clear current routes")
	}
	if !hasChangePath(result.Changes, "routes") {
		t.Fatalf("expected routes FieldChange, got %+v", result.Changes)
	}
}

func TestAppPlatformDriver_Diff_NoSpuriousRouteChange(t *testing.T) {
	mock := &mockAppClient{}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"image":  "registry.digitalocean.com/myrepo/myapp:v1",
			"expose": "public",
			"routes": []any{
				map[string]any{"path": "/", "preserve_path_prefix": false},
			},
		},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"image": "registry.digitalocean.com/myrepo/myapp:v1",
			"routes": []any{
				map[string]any{"path": "/"},
			},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsUpdate {
		t.Fatalf("expected no route update for equivalent routes, got %+v", result.Changes)
	}
}

func TestAppPlatformDriver_Diff_DetectsRouteAdd_OnEmptyState(t *testing.T) {
	mock := &mockAppClient{}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"image":  "registry.digitalocean.com/myrepo/myapp:v1",
			"expose": "public",
			// routes key intentionally absent — state predates this version.
		},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"image": "registry.digitalocean.com/myrepo/myapp:v1",
			"routes": []any{
				map[string]any{"path": "/"},
			},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Fatalf("expected route update when desired declares routes and current.Outputs lacks routes key")
	}
	if !hasChangePath(result.Changes, "routes") {
		t.Fatalf("expected routes FieldChange, got %+v", result.Changes)
	}
}

func TestAppPlatformDriver_Diff_NoSpuriousRouteChange_WhenDesiredOmitted(t *testing.T) {
	mock := &mockAppClient{}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"image":  "registry.digitalocean.com/myrepo/myapp:v1",
			"expose": "public",
			// routes key intentionally absent — state predates this version.
		},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"image": "registry.digitalocean.com/myrepo/myapp:v1",
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsUpdate {
		t.Fatalf("expected no route update when desired omits routes, got %+v", result.Changes)
	}
}

func TestAppPlatformDriver_Diff_DetectsRouteClear_OnEmptyState(t *testing.T) {
	mock := &mockAppClient{}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"image":  "registry.digitalocean.com/myrepo/myapp:v1",
			"expose": "public",
			// routes key intentionally absent — state predates this version.
		},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"image":  "registry.digitalocean.com/myrepo/myapp:v1",
			"routes": []any{},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Fatal("expected route update when desired explicitly clears routes and current.Outputs lacks routes key")
	}
	if !hasChangePath(result.Changes, "routes") {
		t.Fatalf("expected routes FieldChange, got %+v", result.Changes)
	}
}

// TestAppPlatformDriver_AppOutput_ImageDerivedFromAppSpec covers code-reviewer
// round-3 Finding A: `Diff` reads `current.Outputs["image"]` but pre-round-3
// `appOutput` never populated it, so every reconcile of an unchanged service
// emitted a spurious `image` FieldChange. Now `appOutput` derives the image
// string from the live AppSpec so a no-op reconcile produces no Plan action.
//
// Round-trip rule: the derived string must parse back via ParseImageRef to a
// godo.ImageSourceSpec that is structurally equal (RegistryType + Repository +
// Tag) to the user's original `cfg["image"]`. DOCR's Registry field is
// dropped during parse, so for DOCR with empty Registry we substitute the
// Repository as the registry-path placeholder — Diff's structural compare
// (see Diff_NoSpurious_ImageChange below) still finds the round-trip
// equivalent.
func TestAppPlatformDriver_AppOutput_ImageDerivedFromAppSpec(t *testing.T) {
	for _, tc := range []struct {
		name         string
		image        *godo.ImageSourceSpec
		wantNonEmpty bool
		wantContains []string // substrings every well-formed output must include
	}{
		{
			name: "docr_with_empty_registry",
			image: &godo.ImageSourceSpec{
				RegistryType: godo.ImageSourceSpecRegistryType_DOCR,
				Registry:     "",
				Repository:   "myapp",
				Tag:          "v1",
			},
			wantNonEmpty: true,
			wantContains: []string{"registry.digitalocean.com/", "myapp", "v1"},
		},
		{
			name: "docr_with_registry",
			image: &godo.ImageSourceSpec{
				RegistryType: godo.ImageSourceSpecRegistryType_DOCR,
				Registry:     "myrepo",
				Repository:   "myapp",
				Tag:          "v2",
			},
			wantNonEmpty: true,
			wantContains: []string{"registry.digitalocean.com/myrepo/myapp:v2"},
		},
		{
			name: "ghcr",
			image: &godo.ImageSourceSpec{
				RegistryType: godo.ImageSourceSpecRegistryType_Ghcr,
				Registry:     "myorg",
				Repository:   "myapp",
				Tag:          "latest",
			},
			wantNonEmpty: true,
			wantContains: []string{"ghcr.io/myorg/myapp:latest"},
		},
		{
			name: "docker_hub",
			image: &godo.ImageSourceSpec{
				RegistryType: godo.ImageSourceSpecRegistryType_DockerHub,
				Registry:     "library",
				Repository:   "nginx",
				Tag:          "alpine",
			},
			wantNonEmpty: true,
			wantContains: []string{"docker.io/library/nginx:alpine"},
		},
		{
			name:         "nil_image",
			image:        nil,
			wantNonEmpty: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &godo.App{
				ID:               "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
				Spec:             &godo.AppSpec{Name: "img-app", Services: []*godo.AppServiceSpec{{Name: "svc", Image: tc.image, HTTPPort: 8080}}},
				ActiveDeployment: &godo.Deployment{Phase: godo.DeploymentPhase_Active},
			}
			mock := &mockAppClient{app: app}
			d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")
			out, err := d.Read(context.Background(), interfaces.ResourceRef{ProviderID: app.ID, Name: app.Spec.Name})
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			got, _ := out.Outputs["image"].(string)
			if !tc.wantNonEmpty {
				if got != "" {
					t.Errorf("Outputs[\"image\"] = %q, want empty for nil/missing image", got)
				}
				return
			}
			if got == "" {
				t.Fatal("Outputs[\"image\"] empty; want a populated canonical ref")
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("Outputs[\"image\"] = %q, want it to contain %q", got, want)
				}
			}
		})
	}
}

// TestAppPlatformDriver_Diff_DetectsRegistryChange covers F4 round-4 finding:
// imageRefsEqual must compare the Registry field too, otherwise GHCR/DockerHub
// org-changes (same repo+tag, different registry-org) silently slip past Plan.
// DOCR is regression-pinned to confirm the round-trip placeholder doesn't
// trigger a spurious change.
func TestAppPlatformDriver_Diff_DetectsRegistryChange(t *testing.T) {
	for _, tc := range []struct {
		name     string
		current  *godo.ImageSourceSpec
		desired  string
		wantDiff bool
	}{
		{
			// GHCR registry-org migration: orgA → orgB, same repo+tag.
			// Real lifecycle event (e.g. ownership transfer, namespace rename).
			name:     "ghcr_org_change_detected",
			current:  &godo.ImageSourceSpec{RegistryType: godo.ImageSourceSpecRegistryType_Ghcr, Registry: "orgA", Repository: "app", Tag: "v1"},
			desired:  "ghcr.io/orgB/app:v1",
			wantDiff: true,
		},
		{
			// DockerHub registry change: library → myorg, same repo+tag.
			// Real change: switching from official image to fork.
			name:     "dockerhub_registry_change_detected",
			current:  &godo.ImageSourceSpec{RegistryType: godo.ImageSourceSpecRegistryType_DockerHub, Registry: "library", Repository: "redis", Tag: "7"},
			desired:  "docker.io/myorg/redis:7",
			wantDiff: true,
		},
		{
			// DOCR no-op: live spec has Registry="" (DO API convention) and
			// the desired ref's middle segment ("myrepo") is dropped on
			// parse. Both sides should structurally compare equal — the
			// regression-pin for round-3's DOCR fix.
			name:     "docr_placeholder_no_spurious_change",
			current:  &godo.ImageSourceSpec{RegistryType: godo.ImageSourceSpecRegistryType_DOCR, Registry: "", Repository: "myapp", Tag: "v1"},
			desired:  "registry.digitalocean.com/myrepo/myapp:v1",
			wantDiff: false,
		},
		{
			// GHCR no-op: same registry, repo, tag → no change.
			name:     "ghcr_unchanged_no_spurious_change",
			current:  &godo.ImageSourceSpec{RegistryType: godo.ImageSourceSpecRegistryType_Ghcr, Registry: "myorg", Repository: "app", Tag: "v1"},
			desired:  "ghcr.io/myorg/app:v1",
			wantDiff: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &godo.App{
				ID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
				Spec: &godo.AppSpec{
					Name:     "registry-app",
					Services: []*godo.AppServiceSpec{{Name: "svc", Image: tc.current, HTTPPort: 8080}},
				},
				ActiveDeployment: &godo.Deployment{Phase: godo.DeploymentPhase_Active},
			}
			mock := &mockAppClient{app: app}
			d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")
			out, err := d.Read(context.Background(), interfaces.ResourceRef{ProviderID: app.ID, Name: app.Spec.Name})
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
				Config: map[string]any{"image": tc.desired, "http_port": 8080},
			}, out)
			if err != nil {
				t.Fatalf("Diff: %v", err)
			}
			gotImageChange := false
			for _, c := range result.Changes {
				if c.Path == "image" {
					gotImageChange = true
					break
				}
			}
			if gotImageChange != tc.wantDiff {
				t.Errorf("image FieldChange present = %v, want %v\n  current.Outputs[image] = %q\n  desired.cfg[image] = %q\n  changes = %+v",
					gotImageChange, tc.wantDiff, out.Outputs["image"], tc.desired, result.Changes)
			}
		})
	}
}

// TestAppPlatformDriver_Diff_NoSpurious_ImageChange covers the practical
// outcome of Finding A: a Read whose Outputs["image"] was derived from the
// AppSpec via appOutput must NOT trigger a spurious FieldChange when the
// user's desired cfg["image"] resolves to the same Repository+Tag. This is
// the production failure mode Copilot flagged (every reconcile emitted a
// noisy image change forever).
func TestAppPlatformDriver_Diff_NoSpurious_ImageChange_DOCR(t *testing.T) {
	const userImage = "registry.digitalocean.com/myrepo/myapp:v1"
	app := &godo.App{
		ID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
		Spec: &godo.AppSpec{
			Name: "round-trip-app",
			Services: []*godo.AppServiceSpec{{
				Name: "svc",
				// godo.ImageSourceSpec mirrors what ParseImageRef(userImage)
				// produces — DOCR drops Registry on parse.
				Image:    &godo.ImageSourceSpec{RegistryType: godo.ImageSourceSpecRegistryType_DOCR, Registry: "", Repository: "myapp", Tag: "v1"},
				HTTPPort: 8080,
			}},
		},
		ActiveDeployment: &godo.Deployment{Phase: godo.DeploymentPhase_Active},
	}
	mock := &mockAppClient{app: app}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")
	out, err := d.Read(context.Background(), interfaces.ResourceRef{ProviderID: app.ID, Name: app.Spec.Name})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{"image": userImage, "http_port": 8080},
	}, out)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	for _, c := range result.Changes {
		if c.Path == "image" {
			t.Errorf("spurious image FieldChange emitted on no-op reconcile: %+v (Outputs[image]=%q vs cfg[image]=%q)",
				c, out.Outputs["image"], userImage)
		}
	}
	if result.NeedsUpdate {
		// Allow other fields to drive an update only if they materially differ;
		// this assertion is a guard against false-positive image changes only.
		// If a future change adds another silent diff, this t.Error will pin it.
		for _, c := range result.Changes {
			t.Errorf("unexpected FieldChange on no-op reconcile: %+v", c)
		}
	}
}

// TestAppPlatformDriver_AppOutput_ExposeDerivedFromAppSpec verifies that the
// live-state derivation from godo.AppSpec is correct: HTTPPort==0 with
// InternalPorts populated → "internal"; everything else → "public". Without
// this, Diff comparing against current state can't tell whether a previously
// applied service is internal or public, so the toggle detection (above)
// would silently no-op on the round-trip.
func TestAppPlatformDriver_AppOutput_ExposeDerivedFromAppSpec(t *testing.T) {
	internalApp := &godo.App{
		ID:               "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
		Spec:             &godo.AppSpec{Name: "internal-app", Services: []*godo.AppServiceSpec{{Name: "svc", HTTPPort: 0, InternalPorts: []int64{4222}}}},
		ActiveDeployment: &godo.Deployment{Phase: godo.DeploymentPhase_Active},
	}
	publicApp := &godo.App{
		ID:               "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb6",
		Spec:             &godo.AppSpec{Name: "public-app", Services: []*godo.AppServiceSpec{{Name: "svc", HTTPPort: 8080}}},
		ActiveDeployment: &godo.Deployment{Phase: godo.DeploymentPhase_Active},
	}

	for _, tc := range []struct {
		name string
		app  *godo.App
		want string
	}{
		{"internal_when_http_port_zero_and_internal_ports_set", internalApp, "internal"},
		{"public_when_http_port_set", publicApp, "public"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockAppClient{app: tc.app}
			d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")
			out, err := d.Read(context.Background(), interfaces.ResourceRef{ProviderID: tc.app.ID, Name: tc.app.Spec.Name})
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			got, _ := out.Outputs["expose"].(string)
			if got != tc.want {
				t.Errorf("Outputs[\"expose\"] = %q, want %q", got, tc.want)
			}
		})
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
		Name: "my-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
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
		ID:   "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
		Spec: &godo.AppSpec{Name: "my-app"},
		// ActiveDeployment nil => pending/unhealthy
	}
	mock := &mockAppClient{app: app}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name: "my-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
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
	app := &godo.App{ID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5", Spec: &godo.AppSpec{Name: "phased-app"}}
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
	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "phased-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"})
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
	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "phased-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"})
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
	result, _ := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "phased-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"})
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
	result, _ := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "phased-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"})
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
	result, _ := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "phased-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"})
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
	result, _ := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "phased-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"})
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
	result, _ := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "phased-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"})
	if result.Healthy {
		t.Error("expected Healthy=false for unknown phase")
	}
	if !strings.Contains(result.Message, "unknown phase") {
		t.Errorf("message should contain 'unknown phase', got: %q", result.Message)
	}
}

// ── Active-slot phase-transition tests (issue #48) ───────────────────────────
//
// When DO promotes a deployment from InProgressDeployment to ActiveDeployment,
// there is a window where InProgressDeployment is nil but
// ActiveDeployment.Phase is still transitioning (e.g. Deploying, PendingDeploy)
// before reaching Active. The previous appHealthResult returned "no deployment
// found" in that window, locking polling callers into a false-negative loop.

// TestAppHealthResult_ActiveSlotDeployingPhase covers the core bug: ActiveDeployment
// with Deploying phase and no InProgressDeployment must return in-progress, not
// "no deployment found".
func TestAppHealthResult_ActiveSlotDeployingPhase(t *testing.T) {
	d := drivers.NewAppPlatformDriverWithClient(&mockAppClient{
		// ActiveDeployment present but Phase still Deploying; InProgress slot nil.
		app: appWithPhases(phasePtr(godo.DeploymentPhase_Deploying), nil, nil),
	}, "nyc3")
	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "phased-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Healthy {
		t.Error("expected Healthy=false while ActiveDeployment.Phase=Deploying")
	}
	if !strings.Contains(result.Message, "in progress") {
		t.Errorf("message should contain 'in progress', got: %q", result.Message)
	}
	// Must NOT fall through to the "no deployment found" terminal branch.
	if strings.Contains(result.Message, "no deployment") {
		t.Errorf("must not return 'no deployment found' during active-slot transition, got: %q", result.Message)
	}
}

// TestAppHealthResult_ActiveSlotErrorPhase covers terminal failure when the
// ActiveDeployment slot is populated with a failed phase.
func TestAppHealthResult_ActiveSlotErrorPhase(t *testing.T) {
	d := drivers.NewAppPlatformDriverWithClient(&mockAppClient{
		app: appWithPhases(phasePtr(godo.DeploymentPhase_Error), nil, nil),
	}, "nyc3")
	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "phased-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Healthy {
		t.Error("expected Healthy=false for ActiveDeployment.Phase=Error")
	}
	if !strings.Contains(result.Message, "failed") {
		t.Errorf("message should contain 'failed', got: %q", result.Message)
	}
}

// TestAppHealthResult_ActiveSlotActivePhaseStillHealthy is a regression guard:
// ActiveDeployment.Phase=Active must remain the healthy path after the new
// checks are inserted.
func TestAppHealthResult_ActiveSlotActivePhaseStillHealthy(t *testing.T) {
	d := drivers.NewAppPlatformDriverWithClient(&mockAppClient{
		app: appWithPhases(phasePtr(godo.DeploymentPhase_Active), nil, nil),
	}, "nyc3")
	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "phased-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Healthy {
		t.Errorf("expected Healthy=true for ActiveDeployment.Phase=Active, got message: %q", result.Message)
	}
}

// TestAppHealthResult_PriorityActiveActiveSlotOverInProgress verifies that when
// ActiveDeployment.Phase=Active and InProgressDeployment.Phase=Deploying both
// exist simultaneously (a valid DO transient state during rolling updates),
// Healthy=true is returned — Active wins.
func TestAppHealthResult_PriorityActiveActiveSlotOverInProgress(t *testing.T) {
	d := drivers.NewAppPlatformDriverWithClient(&mockAppClient{
		app: appWithPhases(
			phasePtr(godo.DeploymentPhase_Active),
			phasePtr(godo.DeploymentPhase_Deploying),
			nil,
		),
	}, "nyc3")
	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "phased-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Healthy {
		t.Errorf("expected Healthy=true when Active(Active)+InProgress(Deploying); got message: %q", result.Message)
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
		"",                                  // empty
		"justarepo",                         // no org/registry prefix
		"registry.digitalocean.com/onlyone", // DOCR with only one path segment
		"ghcr.io/onlyone",                   // GHCR with only org, no repo
		"docker.io/onlyone",                 // docker.io with only one path segment
		"registry.digitalocean.com/reg/workflow migrate:sha",
		"registry.digitalocean.com/reg/:sha",
		"registry.digitalocean.com/reg/app:bad tag",
		"registry.digitalocean.com/reg/app:bad:tag",
		"registry.digitalocean.com/reg/app:",
		"ghcr.io//app:sha",
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
		Name: "bmw-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
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
		ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
	})
	if err != nil {
		t.Fatalf("Read by ID: unexpected error: %v", err)
	}
	if out.ProviderID != "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5")
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

func TestAppPlatformDriver_Create_EmptyIDFromAPI(t *testing.T) {
	// API returns success but app has empty ID — guard must reject it.
	mock := &mockAppClient{app: &godo.App{Spec: &godo.AppSpec{Name: "my-app"}}}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-app",
		Config: map[string]any{"image": "registry.digitalocean.com/myrepo/myapp:v1"},
	})
	if err == nil {
		t.Fatal("expected error for empty ProviderID, got nil")
	}
}

func TestAppPlatformDriver_Create_ProviderIDIsAPIAssigned(t *testing.T) {
	// ProviderID must be the API-assigned UUID, not the resource name.
	mock := &mockAppClient{app: testApp()}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-app",
		Config: map[string]any{"image": "registry.digitalocean.com/myrepo/myapp:v1"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID == "my-app" {
		t.Errorf("ProviderID must not equal spec.Name %q; got %q", "my-app", out.ProviderID)
	}
	if out.ProviderID == "" {
		t.Errorf("ProviderID must not be empty")
	}
}

func TestTroubleshoot_ReturnsDiagnostics(t *testing.T) {
	// Troubleshoot calls client.Get first; historical deployments come from
	// ListDeployments. The app has no current deployment slots, so both
	// historical error deployments should produce diagnostics.
	mock := &mockAppClient{
		app: &godo.App{ID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5", Spec: &godo.AppSpec{Name: "my-app"}},
		deployments: []*godo.Deployment{
			{ID: "dep-abc", Phase: godo.DeploymentPhase_Error, Cause: "image pull failed"},
			{ID: "dep-xyz", Phase: godo.DeploymentPhase_Canceled, Cause: "superseded"},
		},
	}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")
	ref := interfaces.ResourceRef{Name: "my-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"}
	diags, err := d.Troubleshoot(context.Background(), ref, "no deployment found")
	if err != nil {
		t.Fatalf("Troubleshoot: %v", err)
	}
	if len(diags) != 2 {
		t.Fatalf("want 2 diagnostics, got %d", len(diags))
	}
	if diags[0].ID != "dep-abc" {
		t.Errorf("diags[0].ID = %q, want %q", diags[0].ID, "dep-abc")
	}
	if diags[0].Cause != "image pull failed" {
		t.Errorf("diags[0].Cause = %q, want %q", diags[0].Cause, "image pull failed")
	}
	if diags[0].Phase != string(godo.DeploymentPhase_Error) {
		t.Errorf("diags[0].Phase = %q, want %q", diags[0].Phase, godo.DeploymentPhase_Error)
	}
}

func TestTroubleshoot_NoProviderID(t *testing.T) {
	// Empty ProviderID returns (nil, nil) — nothing to query.
	d := drivers.NewAppPlatformDriverWithClient(&mockAppClient{}, "nyc3")
	ref := interfaces.ResourceRef{Name: "my-app"} // no ProviderID
	diags, err := d.Troubleshoot(context.Background(), ref, "")
	if err != nil {
		t.Fatalf("expected nil error for empty ProviderID, got %v", err)
	}
	if diags != nil {
		t.Fatalf("expected nil diagnostics for empty ProviderID, got %v", diags)
	}
}

func TestTroubleshoot_APIError(t *testing.T) {
	// ListDeployments errors are best-effort: Troubleshoot continues with
	// whatever deployment slots the app has. If the app itself has no
	// slots and Get returns a valid app, result is empty (not an error).
	mock := &mockAppClient{
		app:                &godo.App{ID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5", Spec: &godo.AppSpec{Name: "my-app"}},
		listDeploymentsErr: errors.New("api timeout"),
	}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")
	ref := interfaces.ResourceRef{Name: "my-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"}
	_, err := d.Troubleshoot(context.Background(), ref, "")
	if err != nil {
		t.Fatalf("ListDeployments error should not propagate; got: %v", err)
	}
}

// ── Troubleshoot GetLogs tests ────────────────────────────────────────────────

// TestTroubleshoot_AttachesDeployLogsForFailedDeployment verifies that for an
// Error-phase deployment, Troubleshoot calls GetLogs and the returned log
// content (served by an httptest server) appears in Diagnostic.Detail with the
// component name and delimiters.
func TestTroubleshoot_AttachesDeployLogsForFailedDeployment(t *testing.T) {
	const logContent = "error: image pull failed\nexit status 1"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, logContent)
	}))
	defer srv.Close()

	dep := &godo.Deployment{
		ID:    "dep-err-001",
		Phase: godo.DeploymentPhase_Error,
		Cause: "image pull failed",
		Spec: &godo.AppSpec{
			Name: "my-app",
			Services: []*godo.AppServiceSpec{
				{Name: "web"},
			},
		},
	}
	mock := &mockAppClient{
		app: &godo.App{
			ID:                   "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
			Spec:                 &godo.AppSpec{Name: "my-app"},
			InProgressDeployment: dep,
		},
		getLogsResult: &godo.AppLogs{
			HistoricURLs: []string{srv.URL},
		},
	}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")
	ref := interfaces.ResourceRef{Name: "my-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"}

	diags, err := d.Troubleshoot(context.Background(), ref, "deployment failed")
	if err != nil {
		t.Fatalf("Troubleshoot: %v", err)
	}
	if len(diags) == 0 {
		t.Fatal("expected at least 1 diagnostic")
	}
	detail := diags[0].Detail
	if !strings.Contains(detail, logContent) {
		t.Errorf("Diagnostic.Detail missing log content\nDetail: %q\nWanted: %q", detail, logContent)
	}
	if !strings.Contains(detail, `"web"`) {
		t.Errorf("Diagnostic.Detail missing component name %q\nDetail: %q", "web", detail)
	}
	if !strings.Contains(detail, "---") {
		t.Errorf("Diagnostic.Detail missing delimiter\nDetail: %q", detail)
	}
	if !strings.Contains(detail, "Deploy logs") {
		t.Errorf("Diagnostic.Detail missing 'Deploy logs' header\nDetail: %q", detail)
	}
}

// TestTroubleshoot_AttachesBuildLogsForBuildErroredDeployment verifies that
// when a SummaryStep named "build" has status Error, Troubleshoot requests
// AppLogTypeBuild (not Deploy) from the mock.
func TestTroubleshoot_AttachesBuildLogsForBuildErroredDeployment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "build error: compilation failed")
	}))
	defer srv.Close()

	dep := &godo.Deployment{
		ID:    "dep-build-err",
		Phase: godo.DeploymentPhase_Error,
		Cause: "build failed",
		Progress: &godo.DeploymentProgress{
			SummarySteps: []*godo.DeploymentProgressStep{
				{
					Name:   "build",
					Status: godo.DeploymentProgressStepStatus_Error,
					Reason: &godo.DeploymentProgressStepReason{Message: "compilation failed"},
				},
			},
		},
		Spec: &godo.AppSpec{
			Name:     "my-app",
			Services: []*godo.AppServiceSpec{{Name: "api"}},
		},
	}
	mock := &mockAppClient{
		app: &godo.App{
			ID:                   "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
			Spec:                 &godo.AppSpec{Name: "my-app"},
			InProgressDeployment: dep,
		},
		getLogsResult: &godo.AppLogs{
			HistoricURLs: []string{srv.URL},
		},
	}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")
	ref := interfaces.ResourceRef{Name: "my-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"}

	_, err := d.Troubleshoot(context.Background(), ref, "build failed")
	if err != nil {
		t.Fatalf("Troubleshoot: %v", err)
	}
	if len(mock.getLogsRequests) == 0 {
		t.Fatal("expected GetLogs to be called")
	}
	if mock.getLogsRequests[0].logType != godo.AppLogTypeBuild {
		t.Errorf("GetLogs logType = %q, want %q", mock.getLogsRequests[0].logType, godo.AppLogTypeBuild)
	}
}

// TestTroubleshoot_GracefulOnGetLogsError verifies that when GetLogs returns
// an error, the Diagnostic is still produced (no panic, no error return), and
// the Detail contains a visible failure note so operators know why logs are absent.
func TestTroubleshoot_GracefulOnGetLogsError(t *testing.T) {
	dep := &godo.Deployment{
		ID:    "dep-getlogs-err",
		Phase: godo.DeploymentPhase_Error,
		Cause: "image pull failed",
		Spec:  &godo.AppSpec{Name: "my-app", Services: []*godo.AppServiceSpec{{Name: "web"}}},
	}
	mock := &mockAppClient{
		app: &godo.App{
			ID:                   "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
			Spec:                 &godo.AppSpec{Name: "my-app"},
			InProgressDeployment: dep,
		},
		getLogsErr: errors.New("rate limited"),
	}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")
	ref := interfaces.ResourceRef{Name: "my-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"}

	diags, err := d.Troubleshoot(context.Background(), ref, "deployment failed")
	if err != nil {
		t.Fatalf("Troubleshoot should not error on GetLogs failure; got: %v", err)
	}
	if len(diags) == 0 {
		t.Fatal("expected Diagnostic to be produced even when GetLogs fails")
	}
	// Detail must contain a visible failure note (not silently empty).
	if !strings.Contains(diags[0].Detail, "log fetch unavailable") {
		t.Errorf("Detail should contain 'log fetch unavailable' failure note; got: %q", diags[0].Detail)
	}
}

// TestTroubleshoot_GracefulOnHTTPFetchError verifies that when GetLogs returns
// a URL but the HTTP server returns 500, the Diagnostic is still produced
// and Detail contains a visible failure note so operators know why logs are absent.
func TestTroubleshoot_GracefulOnHTTPFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	dep := &godo.Deployment{
		ID:    "dep-http-err",
		Phase: godo.DeploymentPhase_Error,
		Cause: "image pull failed",
		Spec:  &godo.AppSpec{Name: "my-app", Services: []*godo.AppServiceSpec{{Name: "web"}}},
	}
	mock := &mockAppClient{
		app: &godo.App{
			ID:                   "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
			Spec:                 &godo.AppSpec{Name: "my-app"},
			InProgressDeployment: dep,
		},
		getLogsResult: &godo.AppLogs{HistoricURLs: []string{srv.URL}},
	}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")
	ref := interfaces.ResourceRef{Name: "my-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"}

	diags, err := d.Troubleshoot(context.Background(), ref, "deployment failed")
	if err != nil {
		t.Fatalf("Troubleshoot should not error on HTTP fetch failure; got: %v", err)
	}
	if len(diags) == 0 {
		t.Fatal("expected Diagnostic to be produced even when HTTP fetch fails")
	}
	// Detail must contain a visible failure note (not silently empty).
	if !strings.Contains(diags[0].Detail, "log fetch failed") {
		t.Errorf("Detail should contain 'log fetch failed' failure note; got: %q", diags[0].Detail)
	}
}

// TestTroubleshoot_PerComponentLogs verifies that for a multi-component
// deployment (2 services + 1 worker), each component produces a separate
// labeled log block in Diagnostic.Detail.
func TestTroubleshoot_PerComponentLogs(t *testing.T) {
	// Each component gets its own httptest log content.
	logBySrv := map[string]string{} // url → content
	makeServer := func(content string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, content)
		}))
		logBySrv[srv.URL] = content
		return srv
	}
	srvWeb := makeServer("web: error pulling image")
	srvApi := makeServer("api: OOM killed")
	srvWorker := makeServer("worker: panic at startup")
	defer srvWeb.Close()
	defer srvApi.Close()
	defer srvWorker.Close()

	dep := &godo.Deployment{
		ID:    "dep-multi-comp",
		Phase: godo.DeploymentPhase_Error,
		Cause: "multiple components failed",
		Spec: &godo.AppSpec{
			Name:     "my-app",
			Services: []*godo.AppServiceSpec{{Name: "web"}, {Name: "api"}},
			Workers:  []*godo.AppWorkerSpec{{Name: "worker"}},
		},
	}

	callCount := 0
	servers := []string{srvWeb.URL, srvApi.URL, srvWorker.URL}
	mock := &mockAppClient{
		app: &godo.App{
			ID:                   "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
			Spec:                 &godo.AppSpec{Name: "my-app"},
			InProgressDeployment: dep,
		},
	}
	// Override GetLogs to return different URLs per call.
	// We use a custom mock that cycles through server URLs.
	mockFn := &cyclingLogsMock{
		mockAppClient: mock,
		serverURLs:    servers,
		callCount:     &callCount,
	}
	d := drivers.NewAppPlatformDriverWithClient(mockFn, "nyc3")
	ref := interfaces.ResourceRef{Name: "my-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"}

	diags, err := d.Troubleshoot(context.Background(), ref, "deployment failed")
	if err != nil {
		t.Fatalf("Troubleshoot: %v", err)
	}
	if len(diags) == 0 {
		t.Fatal("expected at least 1 diagnostic")
	}
	detail := diags[0].Detail
	for _, comp := range []string{`"web"`, `"api"`, `"worker"`} {
		if !strings.Contains(detail, comp) {
			t.Errorf("Detail missing component %s\nDetail: %q", comp, detail)
		}
	}
	for _, logLine := range []string{"web: error pulling image", "api: OOM killed", "worker: panic at startup"} {
		if !strings.Contains(detail, logLine) {
			t.Errorf("Detail missing log line %q\nDetail: %q", logLine, detail)
		}
	}
	// Should have 3 delimited blocks.
	blockCount := strings.Count(detail, "---")
	if blockCount < 6 { // each block uses 2 "---" delimiters
		t.Errorf("expected at least 6 '---' delimiters for 3 log blocks, got %d\nDetail: %q", blockCount, detail)
	}
}

// cyclingLogsMock extends mockAppClient with per-call URL cycling for GetLogs.
type cyclingLogsMock struct {
	*mockAppClient
	serverURLs []string
	callCount  *int
}

func (m *cyclingLogsMock) GetLogs(_ context.Context, appID, deploymentID, component string, logType godo.AppLogType, _ bool, _ int) (*godo.AppLogs, *godo.Response, error) {
	m.mockAppClient.getLogsRequests = append(m.mockAppClient.getLogsRequests, getLogsCall{
		appID: appID, deploymentID: deploymentID, component: component, logType: logType,
	})
	idx := *m.callCount
	*m.callCount++
	if idx >= len(m.serverURLs) {
		return nil, nil, errors.New("no more server URLs")
	}
	return &godo.AppLogs{HistoricURLs: []string{m.serverURLs[idx]}}, nil, nil
}

// ── appHealthResult ListDeployments fallback tests ────────────────────────────

// TestAppHealthResult_AllSlotsNilFallsBackToListDeployments verifies that when
// all 3 deployment slots are nil but ListDeployments returns 1 deployment,
// HealthResult.Message contains the short deployment ID and its phase.
func TestAppHealthResult_AllSlotsNilFallsBackToListDeployments(t *testing.T) {
	const depID = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	d := drivers.NewAppPlatformDriverWithClient(&mockAppClient{
		app: appWithPhases(nil, nil, nil),
		deployments: []*godo.Deployment{
			{ID: depID, Phase: godo.DeploymentPhase_Error},
		},
	}, "nyc3")
	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name:       "phased-app",
		ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Healthy {
		t.Error("expected Healthy=false")
	}
	// Must contain the short ID (first 8 chars) and the phase.
	shortID := depID[:8]
	if !strings.Contains(result.Message, shortID) {
		t.Errorf("Message = %q, want it to contain short ID %q", result.Message, shortID)
	}
	if !strings.Contains(result.Message, string(godo.DeploymentPhase_Error)) {
		t.Errorf("Message = %q, want it to contain phase %q", result.Message, godo.DeploymentPhase_Error)
	}
}

// TestAppHealthResult_AllSlotsNilEmptyHistory verifies that when all 3 slots
// are nil and ListDeployments returns empty, the message is "no deployment found".
func TestAppHealthResult_AllSlotsNilEmptyHistory(t *testing.T) {
	d := drivers.NewAppPlatformDriverWithClient(&mockAppClient{
		app:         appWithPhases(nil, nil, nil),
		deployments: []*godo.Deployment{},
	}, "nyc3")
	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name:       "phased-app",
		ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Healthy {
		t.Error("expected Healthy=false")
	}
	if !strings.Contains(result.Message, "no deployment found") {
		t.Errorf("Message = %q, want 'no deployment found'", result.Message)
	}
}

// TestAppHealthResult_AllSlotsNilListDeploymentsError verifies that when all
// 3 slots are nil and ListDeployments returns an error, the code falls through
// to "no deployment found" without panicking or returning an error.
func TestAppHealthResult_AllSlotsNilListDeploymentsError(t *testing.T) {
	d := drivers.NewAppPlatformDriverWithClient(&mockAppClient{
		app:                appWithPhases(nil, nil, nil),
		listDeploymentsErr: errors.New("api error"),
	}, "nyc3")
	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name:       "phased-app",
		ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Healthy {
		t.Error("expected Healthy=false")
	}
	if !strings.Contains(result.Message, "no deployment found") {
		t.Errorf("Message = %q, want 'no deployment found'", result.Message)
	}
}

// TestAppPlatformDriver_Diff_RejectsInvalidRegionAtPlanTime confirms the
// validator is wired into Diff() so an invalid App Platform region surfaces
// during plan (NOT apply). User-facing fix for the "App platform 'nyc1'"
// case where the DO API returns a misleading "404 Image tag or digest not
// found" instead of an actionable validation error.
func TestAppPlatformDriver_Diff_RejectsInvalidRegionAtPlanTime(t *testing.T) {
	mock := &mockAppClient{}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc")

	_, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Name: "my-app",
		Config: map[string]any{
			"image":  "registry.digitalocean.com/myrepo/myapp:v1",
			"region": "nyc1", // datacenter slug — invalid for App Platform
		},
	}, nil)
	if err == nil {
		t.Fatal("Diff with invalid App Platform region must error at plan time")
	}
	msg := err.Error()
	if !strings.Contains(msg, "nyc1") {
		t.Errorf("error must name the offending region: %s", msg)
	}
	if !strings.Contains(msg, "my-app") {
		t.Errorf("error must name the resource: %s", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "datacenter") {
		t.Errorf("error must explain datacenter-slug confusion: %s", msg)
	}
}

// TestAppPlatformDriver_Create_RejectsInvalidRegion gives symmetric Create-
// path coverage so a direct caller (not via Plan) gets the same protection,
// before the request hits the DO API.
func TestAppPlatformDriver_Create_RejectsInvalidRegion(t *testing.T) {
	mock := &mockAppClient{}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-app",
		Config: map[string]any{
			"image":  "registry.digitalocean.com/myrepo/myapp:v1",
			"region": "sfo3",
		},
	})
	if err == nil {
		t.Fatal("Create with invalid App Platform region must error")
	}
	if !strings.Contains(err.Error(), "sfo3") {
		t.Errorf("error must name the offending region: %v", err)
	}
	if mock.lastCreateReq != nil {
		t.Error("Create API must not be called when region validation fails")
	}
}

// TestAppPlatformDriver_Diff_AcceptsValidRegion confirms the validation
// only rejects bad inputs — a valid region passes through cleanly.
func TestAppPlatformDriver_Diff_AcceptsValidRegion(t *testing.T) {
	mock := &mockAppClient{}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc")

	_, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Name: "my-app",
		Config: map[string]any{
			"image":  "registry.digitalocean.com/myrepo/myapp:v1",
			"region": "fra",
		},
	}, nil)
	if err != nil {
		t.Errorf("Diff with valid App Platform region must not error: %v", err)
	}
}

// TestAppPlatformDriver_Diff_DetectsEnvVarsDrift is the regression test for
// the silent-stale-DATABASE_URL bug: when an `infra_output:` secret
// reference (e.g. STAGING_PG_HOST sourced from coredump-staging-pg.private_ip)
// resolves to a NEW value on Apply (Droplet replace bumps the IP), the
// App's env_vars need to be re-pushed to DO. Prior Diff only compared
// image/expose/region, missing env_vars entirely → state.config DATABASE_URL
// froze with the first-resolved IP, every subsequent Plan returned 0
// changes, and the migrate job kept dialing the long-gone IP.
//
// Surfaced on core-dump deploy run 25653219644: state.config DATABASE_URL
// pinned to 10.20.0.2 while the live Droplet had moved to 10.20.0.6.
func TestAppPlatformDriver_Diff_DetectsEnvVarsDrift(t *testing.T) {
	mock := &mockAppClient{}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc")

	// Current-side: a stable hash placeholder representing the OLD
	// env_vars (any non-empty hash that differs from the desired hash
	// suffices to assert drift detection).
	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"image":         "registry.digitalocean.com/myrepo/myapp:v1",
			"region":        "nyc",
			"env_vars_hash": "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"image":  "registry.digitalocean.com/myrepo/myapp:v1",
			"region": "nyc",
			"env_vars": map[string]any{
				"DATABASE_URL": "postgres://u:p@10.20.0.6:5432/db",
			},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Fatalf("expected NeedsUpdate=true when env_vars hash mismatched")
	}
	if result.NeedsReplace {
		t.Errorf("env_vars change must NOT force replace (in-place Update suffices)")
	}
}

// TestAppPlatformDriver_Diff_DetectsJobEnvVarsDrift covers the migrate-job
// case specifically — pre_deploy jobs carry their own env_vars that can
// independently go stale.
func TestAppPlatformDriver_Diff_DetectsJobEnvVarsDrift(t *testing.T) {
	mock := &mockAppClient{}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"image":  "registry.digitalocean.com/myrepo/myapp:v1",
			"region": "nyc",
			"jobs_hash": map[string]any{
				"migrate": "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
			},
		},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"image":  "registry.digitalocean.com/myrepo/myapp:v1",
			"region": "nyc",
			"jobs": []any{
				map[string]any{
					"name": "migrate",
					"kind": "pre_deploy",
					"env_vars": map[string]any{
						"DATABASE_URL": "postgres://u:p@10.20.0.6:5432/db",
					},
				},
			},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Fatal("expected NeedsUpdate=true when job env_vars hash differs")
	}
}

// TestAppPlatformDriver_Diff_NoSpurious_EnvVarsChange_OnEmptyState guards
// the upgrade path: state from a prior plugin version (no env_vars in
// Outputs) must NOT false-positive on the first Plan post-upgrade.
func TestAppPlatformDriver_Diff_NoSpurious_EnvVarsChange_OnEmptyState(t *testing.T) {
	mock := &mockAppClient{}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"image":  "registry.digitalocean.com/myrepo/myapp:v1",
			"region": "nyc",
			// env_vars key intentionally absent — state predates this version
		},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"image":  "registry.digitalocean.com/myrepo/myapp:v1",
			"region": "nyc",
			"env_vars": map[string]any{
				"DATABASE_URL": "postgres://u:p@10.20.0.6:5432/db",
			},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=false when current.Outputs lacks env_vars key (upgrade path)")
	}
}

// TestAppPlatformDriver_Diff_DetectsVPCDrift covers VPC attachment change.
func TestAppPlatformDriver_Diff_DetectsVPCDrift(t *testing.T) {
	mock := &mockAppClient{}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"image":    "registry.digitalocean.com/myrepo/myapp:v1",
			"region":   "nyc",
			"vpc_uuid": "", // App was created outside VPC
		},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"image":   "registry.digitalocean.com/myrepo/myapp:v1",
			"region":  "nyc",
			"vpc_ref": "14badc41-c954-4dfd-b1e2-87b72eb8a147",
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Fatal("expected NeedsUpdate=true when vpc_ref differs from current vpc_uuid")
	}
	if result.NeedsReplace {
		t.Errorf("VPC attachment change must NOT force replace (in-place Update suffices)")
	}
}

func hasChangePath(changes []interfaces.FieldChange, path string) bool {
	for _, c := range changes {
		if c.Path == path {
			return true
		}
	}
	return false
}

// TestAppPlatformDriver_appOutput_DoesNotLeakPlaintextSecrets is the
// regression test for the v1.0.5 security bug: appOutput surfaced
// env_vars / jobs / workers as raw maps, exposing JIT-substituted
// secret values (e.g. plaintext STAGING_PG_PASSWORD baked into
// DATABASE_URL) to state.Outputs — which lives in DO Spaces and is
// dumped by `wfctl infra outputs`. v1.0.6+ stores SHA-256 hashes only.
//
// This test inspects the appOutput map directly to assert no string
// matching a known secret value appears anywhere — defends against
// any future code path that would re-introduce plaintext surfacing.
func TestAppPlatformDriver_appOutput_DoesNotLeakPlaintextSecrets(t *testing.T) {
	const secretValue = "SUPER_SECRET_PASSWORD_DO_NOT_LEAK"
	app := &godo.App{
		ID: "app-uuid",
		Spec: &godo.AppSpec{
			Name:   "test-app",
			Region: "nyc",
			Services: []*godo.AppServiceSpec{
				{
					Name:  "svc",
					Image: &godo.ImageSourceSpec{RegistryType: "DOCR", Repository: "r", Tag: "v1"},
					Envs:  []*godo.AppVariableDefinition{{Key: "DATABASE_URL", Value: "postgres://u:" + secretValue + "@host/db"}},
				},
			},
			Jobs: []*godo.AppJobSpec{
				{
					Name: "migrate", Kind: "PRE_DEPLOY",
					Envs: []*godo.AppVariableDefinition{{Key: "DATABASE_URL", Value: "postgres://u:" + secretValue + "@host/db"}},
				},
			},
		},
	}
	outputs := drivers.AppOutputForTest(app)
	data, err := json.Marshal(outputs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), secretValue) {
		t.Fatalf("plaintext secret leaked into state.Outputs JSON: %s", data)
	}
	// Sanity: hash fields are present.
	if outputs["env_vars_hash"] == nil {
		t.Error("env_vars_hash should be populated")
	}
	if outputs["jobs_hash"] == nil {
		t.Error("jobs_hash should be populated")
	}
}
