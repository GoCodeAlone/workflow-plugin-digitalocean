package drivers_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/digitalocean/godo"
)

// ─── shared mock ─────────────────────────────────────────────────────────────

// deployMockClient supports stateful create/get/update/delete for deploy tests.
type deployMockClient struct {
	apps   map[string]*godo.App
	err    error
	nextID int
}

func newDeployMock() *deployMockClient {
	// Start at 100 so seeded apps like "app-1" don't collide with generated IDs.
	return &deployMockClient{apps: make(map[string]*godo.App), nextID: 100}
}

func (m *deployMockClient) Create(_ context.Context, req *godo.AppCreateRequest) (*godo.App, *godo.Response, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	id := fmt.Sprintf("app-%d", m.nextID)
	m.nextID++
	app := &godo.App{
		ID:      id,
		LiveURL: "https://" + req.Spec.Name + ".example.com",
		Spec:    req.Spec,
		ActiveDeployment: &godo.Deployment{
			Phase: godo.DeploymentPhase_Active,
		},
	}
	m.apps[id] = app
	return app, nil, nil
}

func (m *deployMockClient) Get(_ context.Context, appID string) (*godo.App, *godo.Response, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	app, ok := m.apps[appID]
	if !ok {
		return nil, nil, fmt.Errorf("app %q not found", appID)
	}
	return app, nil, nil
}

func (m *deployMockClient) Update(_ context.Context, appID string, req *godo.AppUpdateRequest) (*godo.App, *godo.Response, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	app, ok := m.apps[appID]
	if !ok {
		return nil, nil, fmt.Errorf("app %q not found", appID)
	}
	app.Spec = req.Spec
	return app, nil, nil
}

func (m *deployMockClient) List(_ context.Context, _ *godo.ListOptions) ([]*godo.App, *godo.Response, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	apps := make([]*godo.App, 0, len(m.apps))
	for _, a := range m.apps {
		apps = append(apps, a)
	}
	return apps, &godo.Response{}, nil
}

func (m *deployMockClient) Delete(_ context.Context, appID string) (*godo.Response, error) {
	if m.err != nil {
		return nil, m.err
	}
	delete(m.apps, appID)
	return nil, nil
}

// seedApp inserts a pre-existing app into the mock's store.
func seedApp(m *deployMockClient, id, name, image string) *godo.App {
	repo, tag := splitImage(image)
	app := &godo.App{
		ID:      id,
		LiveURL: "https://" + name + ".example.com",
		Spec: &godo.AppSpec{
			Name: name,
			Services: []*godo.AppServiceSpec{
				{
					Name:          name,
					InstanceCount: 2,
					Image: &godo.ImageSourceSpec{
						RegistryType: godo.ImageSourceSpecRegistryType_DOCR,
						Repository:   repo,
						Tag:          tag,
					},
				},
			},
		},
		ActiveDeployment: &godo.Deployment{Phase: godo.DeploymentPhase_Active},
	}
	m.apps[id] = app
	return app
}

func splitImage(image string) (repo, tag string) {
	parts := strings.SplitN(image, ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return parts[0], "latest"
}

// ─── AppDeployDriver ─────────────────────────────────────────────────────────

func TestAppDeployDriver_Update(t *testing.T) {
	m := newDeployMock()
	seedApp(m, "app-1", "myapp", "registry.digitalocean.com/myrepo/myapp:v1")

	d := drivers.NewAppDeployDriver(m, "nyc3", "app-1", "myapp")
	if err := d.Update(context.Background(), "registry.digitalocean.com/myrepo/myapp:v2"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	img, err := d.CurrentImage(context.Background())
	if err != nil {
		t.Fatalf("CurrentImage: %v", err)
	}
	if img != "registry.digitalocean.com/myrepo/myapp:v2" {
		t.Errorf("CurrentImage = %q, want v2", img)
	}
}

func TestAppDeployDriver_Update_GetError(t *testing.T) {
	m := newDeployMock()
	m.err = fmt.Errorf("api error")
	d := drivers.NewAppDeployDriver(m, "nyc3", "app-1", "myapp")
	if err := d.Update(context.Background(), "image:v2"); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestAppDeployDriver_HealthCheck_Active(t *testing.T) {
	m := newDeployMock()
	seedApp(m, "app-1", "myapp", "image:v1")
	d := drivers.NewAppDeployDriver(m, "nyc3", "app-1", "myapp")
	if err := d.HealthCheck(context.Background(), "/health"); err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
}

func TestAppDeployDriver_HealthCheck_NotActive(t *testing.T) {
	m := newDeployMock()
	app := seedApp(m, "app-1", "myapp", "image:v1")
	app.ActiveDeployment = nil
	d := drivers.NewAppDeployDriver(m, "nyc3", "app-1", "myapp")
	if err := d.HealthCheck(context.Background(), "/health"); err == nil {
		t.Fatal("expected error for non-active app")
	}
}

func TestAppDeployDriver_CurrentImage(t *testing.T) {
	m := newDeployMock()
	seedApp(m, "app-1", "myapp", "registry.digitalocean.com/myrepo/myapp:v1")
	d := drivers.NewAppDeployDriver(m, "nyc3", "app-1", "myapp")
	img, err := d.CurrentImage(context.Background())
	if err != nil {
		t.Fatalf("CurrentImage: %v", err)
	}
	if img != "registry.digitalocean.com/myrepo/myapp:v1" {
		t.Errorf("CurrentImage = %q", img)
	}
}

func TestAppDeployDriver_ReplicaCount(t *testing.T) {
	m := newDeployMock()
	seedApp(m, "app-1", "myapp", "image:v1")
	d := drivers.NewAppDeployDriver(m, "nyc3", "app-1", "myapp")
	n, err := d.ReplicaCount(context.Background())
	if err != nil {
		t.Fatalf("ReplicaCount: %v", err)
	}
	if n != 2 {
		t.Errorf("ReplicaCount = %d, want 2", n)
	}
}

// ─── AppBlueGreenDriver ───────────────────────────────────────────────────────

func TestAppBlueGreenDriver_CreateGreen(t *testing.T) {
	m := newDeployMock()
	seedApp(m, "app-1", "myapp", "registry.digitalocean.com/myrepo/myapp:v1")

	d := drivers.NewAppBlueGreenDriver(m, "nyc3", "app-1", "myapp")
	if err := d.CreateGreen(context.Background(), "registry.digitalocean.com/myrepo/myapp:v2"); err != nil {
		t.Fatalf("CreateGreen: %v", err)
	}
	// A second app should exist.
	if len(m.apps) != 2 {
		t.Errorf("expected 2 apps after CreateGreen, got %d", len(m.apps))
	}
	ep, err := d.GreenEndpoint(context.Background())
	if err != nil {
		t.Fatalf("GreenEndpoint: %v", err)
	}
	if ep == "" {
		t.Error("expected non-empty green endpoint")
	}
}

func TestAppBlueGreenDriver_SwitchTraffic(t *testing.T) {
	m := newDeployMock()
	seedApp(m, "app-1", "myapp", "registry.digitalocean.com/myrepo/myapp:v1")

	d := drivers.NewAppBlueGreenDriver(m, "nyc3", "app-1", "myapp")
	if err := d.CreateGreen(context.Background(), "registry.digitalocean.com/myrepo/myapp:v2"); err != nil {
		t.Fatalf("CreateGreen: %v", err)
	}
	if err := d.SwitchTraffic(context.Background()); err != nil {
		t.Fatalf("SwitchTraffic: %v", err)
	}
	// Blue app (app-1) should now run v2.
	blueApp := m.apps["app-1"]
	if blueApp == nil || blueApp.Spec == nil || len(blueApp.Spec.Services) == 0 {
		t.Fatal("blue app missing after switch")
	}
	svc := blueApp.Spec.Services[0]
	if svc.Image == nil {
		t.Fatal("blue app service has no image after switch")
	}
	if svc.Image.Tag != "v2" {
		t.Errorf("after switch blue image tag = %q, want v2", svc.Image.Tag)
	}
}

func TestAppBlueGreenDriver_DestroyBlue(t *testing.T) {
	m := newDeployMock()
	seedApp(m, "app-1", "myapp", "image:v1")

	d := drivers.NewAppBlueGreenDriver(m, "nyc3", "app-1", "myapp")
	if err := d.CreateGreen(context.Background(), "image:v2"); err != nil {
		t.Fatalf("CreateGreen: %v", err)
	}
	if err := d.DestroyBlue(context.Background()); err != nil {
		t.Fatalf("DestroyBlue: %v", err)
	}
	// Only the blue (app-1) should remain; green clone is deleted.
	if len(m.apps) != 1 {
		t.Errorf("expected 1 app after DestroyBlue, got %d", len(m.apps))
	}
	if _, ok := m.apps["app-1"]; !ok {
		t.Error("expected blue app-1 to still exist after DestroyBlue")
	}
}

func TestAppBlueGreenDriver_GreenEndpoint_NotSet(t *testing.T) {
	m := newDeployMock()
	d := drivers.NewAppBlueGreenDriver(m, "nyc3", "app-1", "myapp")
	if _, err := d.GreenEndpoint(context.Background()); err == nil {
		t.Fatal("expected error when green not created")
	}
}

func TestAppBlueGreenDriver_SwitchTraffic_NoGreen(t *testing.T) {
	m := newDeployMock()
	d := drivers.NewAppBlueGreenDriver(m, "nyc3", "app-1", "myapp")
	if err := d.SwitchTraffic(context.Background()); err == nil {
		t.Fatal("expected error when green not created")
	}
}

func TestAppBlueGreenDriver_DestroyBlue_NoGreen(t *testing.T) {
	m := newDeployMock()
	d := drivers.NewAppBlueGreenDriver(m, "nyc3", "app-1", "myapp")
	if err := d.DestroyBlue(context.Background()); err == nil {
		t.Fatal("expected error when no green to destroy")
	}
}

// ─── AppCanaryDriver ──────────────────────────────────────────────────────────

func TestAppCanaryDriver_CreateCanary(t *testing.T) {
	m := newDeployMock()
	seedApp(m, "app-1", "myapp", "image:v1")

	d := drivers.NewAppCanaryDriver(m, "nyc3", "app-1", "myapp")
	if err := d.CreateCanary(context.Background(), "image:v2"); err != nil {
		t.Fatalf("CreateCanary: %v", err)
	}
	if len(m.apps) != 2 {
		t.Errorf("expected 2 apps (stable + canary), got %d", len(m.apps))
	}
}

func TestAppCanaryDriver_RoutePercent_Unsupported(t *testing.T) {
	m := newDeployMock()
	d := drivers.NewAppCanaryDriver(m, "nyc3", "app-1", "myapp")
	if err := d.RoutePercent(context.Background(), 20); err == nil {
		t.Fatal("expected unsupported error for RoutePercent")
	}
}

func TestAppCanaryDriver_CheckMetricGate_Pass(t *testing.T) {
	m := newDeployMock()
	d := drivers.NewAppCanaryDriver(m, "nyc3", "app-1", "myapp")
	if err := d.CheckMetricGate(context.Background(), "error_rate"); err != nil {
		t.Fatalf("CheckMetricGate: %v", err)
	}
}

func TestAppCanaryDriver_PromoteCanary(t *testing.T) {
	m := newDeployMock()
	seedApp(m, "app-1", "myapp", "image:v1")

	d := drivers.NewAppCanaryDriver(m, "nyc3", "app-1", "myapp")
	if err := d.CreateCanary(context.Background(), "image:v2"); err != nil {
		t.Fatalf("CreateCanary: %v", err)
	}
	if err := d.PromoteCanary(context.Background()); err != nil {
		t.Fatalf("PromoteCanary: %v", err)
	}
	// Only the stable app should remain after promote+destroy canary.
	if len(m.apps) != 1 {
		t.Errorf("expected 1 app after promote, got %d", len(m.apps))
	}
	blueApp := m.apps["app-1"]
	if blueApp == nil || blueApp.Spec == nil || len(blueApp.Spec.Services) == 0 {
		t.Fatal("blue app missing after promote")
	}
	svc := blueApp.Spec.Services[0]
	if svc.Image == nil || svc.Image.Tag != "v2" {
		tag := ""
		if svc.Image != nil {
			tag = svc.Image.Tag
		}
		t.Errorf("after promote stable image tag = %q, want v2", tag)
	}
}

func TestAppCanaryDriver_DestroyCanary(t *testing.T) {
	m := newDeployMock()
	seedApp(m, "app-1", "myapp", "image:v1")

	d := drivers.NewAppCanaryDriver(m, "nyc3", "app-1", "myapp")
	if err := d.CreateCanary(context.Background(), "image:v2"); err != nil {
		t.Fatalf("CreateCanary: %v", err)
	}
	if err := d.DestroyCanary(context.Background()); err != nil {
		t.Fatalf("DestroyCanary: %v", err)
	}
	if len(m.apps) != 1 {
		t.Errorf("expected 1 app after destroy, got %d", len(m.apps))
	}
}

func TestAppCanaryDriver_DestroyCanary_NoCanary(t *testing.T) {
	m := newDeployMock()
	d := drivers.NewAppCanaryDriver(m, "nyc3", "app-1", "myapp")
	if err := d.DestroyCanary(context.Background()); err == nil {
		t.Fatal("expected error when no canary exists")
	}
}

func TestAppCanaryDriver_PromoteCanary_NoCanary(t *testing.T) {
	m := newDeployMock()
	d := drivers.NewAppCanaryDriver(m, "nyc3", "app-1", "myapp")
	if err := d.PromoteCanary(context.Background()); err == nil {
		t.Fatal("expected error when no canary exists")
	}
}
