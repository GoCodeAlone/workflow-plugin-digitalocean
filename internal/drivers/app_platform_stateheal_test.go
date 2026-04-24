package drivers

// State-heal tests for AppPlatformDriver.Update / Delete / isUUIDLike.
// Uses package drivers (not drivers_test) so unexported helpers are accessible.

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// ── mock client for state-heal tests ─────────────────────────────────────────

type stateHealClient struct {
	// Create
	createApp *godo.App
	createErr error

	// List (for findAppByName)
	listApps []*godo.App
	listErr  error

	// Update captures the appID it was called with and returns updatedApp.
	updatedApp     *godo.App
	updateErr      error
	updateCalledID string

	// CreateDeployment returns dep or depErr.
	dep    *godo.Deployment
	depErr error

	// Delete captures the appID it was called with.
	deleteCalledID string
	deleteErr      error
}

func (c *stateHealClient) Create(_ context.Context, _ *godo.AppCreateRequest) (*godo.App, *godo.Response, error) {
	return c.createApp, &godo.Response{Response: &http.Response{StatusCode: 200}}, c.createErr
}
func (c *stateHealClient) Get(_ context.Context, _ string) (*godo.App, *godo.Response, error) {
	return nil, nil, errors.New("not implemented in stateHealClient")
}
func (c *stateHealClient) List(_ context.Context, _ *godo.ListOptions) ([]*godo.App, *godo.Response, error) {
	resp := &godo.Response{Response: &http.Response{StatusCode: 200}, Links: &godo.Links{}}
	return c.listApps, resp, c.listErr
}
func (c *stateHealClient) Update(_ context.Context, appID string, _ *godo.AppUpdateRequest) (*godo.App, *godo.Response, error) {
	c.updateCalledID = appID
	return c.updatedApp, &godo.Response{Response: &http.Response{StatusCode: 200}}, c.updateErr
}
func (c *stateHealClient) CreateDeployment(_ context.Context, _ string, _ ...*godo.DeploymentCreateRequest) (*godo.Deployment, *godo.Response, error) {
	if c.depErr != nil {
		return nil, nil, c.depErr
	}
	if c.dep == nil {
		return &godo.Deployment{ID: "dep-triggered"}, &godo.Response{}, nil
	}
	return c.dep, &godo.Response{}, nil
}
func (c *stateHealClient) ListDeployments(_ context.Context, _ string, _ *godo.ListOptions) ([]*godo.Deployment, *godo.Response, error) {
	return nil, &godo.Response{}, nil
}
func (c *stateHealClient) Delete(_ context.Context, appID string) (*godo.Response, error) {
	c.deleteCalledID = appID
	return &godo.Response{Response: &http.Response{StatusCode: 204}}, c.deleteErr
}

// ── isUUIDLike tests ──────────────────────────────────────────────────────────

func TestIsUUIDLike_TableDriven(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5", true},
		{"00000000-0000-0000-0000-000000000000", true},
		{"bmw-staging", false},
		{"", false},
		{"f8b6200c3bba48a78bf17a3e3a885eb5", false},   // no hyphens
		{"f8b6200c-3bba-48a7-8bf1", false},             // too short
		{"f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5-xx", false}, // too long
		{"f8b6200c_3bba_48a7_8bf1_7a3e3a885eb5", false}, // wrong separator
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := isUUIDLike(c.in)
			if got != c.want {
				t.Errorf("isUUIDLike(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// ── Create preserves UUID (regression: name must not be used as ProviderID) ──

func TestCreate_ProviderIDIsUUIDFromAPI(t *testing.T) {
	// API returns an app with a real UUID. ProviderID in the returned
	// ResourceOutput must equal that UUID, not the spec name.
	const wantUUID = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	c := &stateHealClient{
		createApp: &godo.App{
			ID:   wantUUID,
			Spec: &godo.AppSpec{Name: "bmw-staging"},
		},
	}
	d := NewAppPlatformDriverWithClient(c, "nyc3")
	spec := interfaces.ResourceSpec{
		Name:   "bmw-staging",
		Config: map[string]any{"image": "docker.io/myorg/myapp:latest"},
	}
	out, err := d.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID != wantUUID {
		t.Errorf("ProviderID = %q, want UUID %q", out.ProviderID, wantUUID)
	}
	if out.ProviderID == "bmw-staging" {
		t.Error("ProviderID must not be the spec name")
	}
}

// ── Update state-heal tests ──────────────────────────────────────────────────

func TestUpdate_UsesUUIDWhenProviderIDIsValid(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	c := &stateHealClient{
		updatedApp: &godo.App{
			ID:   uuid,
			Spec: &godo.AppSpec{Name: "bmw-staging"},
		},
	}
	d := NewAppPlatformDriverWithClient(c, "nyc3")
	ref := interfaces.ResourceRef{Name: "bmw-staging", ProviderID: uuid}
	spec := interfaces.ResourceSpec{
		Name:   "bmw-staging",
		Config: map[string]any{"image": "docker.io/myorg/myapp:latest"},
	}
	_, err := d.Update(context.Background(), ref, spec)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if c.updateCalledID != uuid {
		t.Errorf("Update called with ID %q, want UUID %q", c.updateCalledID, uuid)
	}
	// findAppByName should NOT have been called (List should not have been invoked).
	if len(c.listApps) != 0 && c.updateCalledID != uuid {
		t.Error("findAppByName should not be called when ProviderID is already a UUID")
	}
}

func TestUpdate_HealsStaleName(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	c := &stateHealClient{
		// List returns the real app so findAppByName resolves name → UUID.
		listApps: []*godo.App{
			{ID: uuid, Spec: &godo.AppSpec{Name: "bmw-staging"}},
		},
		updatedApp: &godo.App{
			ID:   uuid,
			Spec: &godo.AppSpec{Name: "bmw-staging"},
		},
	}
	d := NewAppPlatformDriverWithClient(c, "nyc3")
	// Stale state: ProviderID is the name, not a UUID.
	ref := interfaces.ResourceRef{Name: "bmw-staging", ProviderID: "bmw-staging"}
	spec := interfaces.ResourceSpec{
		Name:   "bmw-staging",
		Config: map[string]any{"image": "docker.io/myorg/myapp:latest"},
	}
	out, err := d.Update(context.Background(), ref, spec)
	if err != nil {
		t.Fatalf("Update with stale name ProviderID: %v", err)
	}
	// Update API must have been called with the UUID, not the name.
	if c.updateCalledID != uuid {
		t.Errorf("Update called with %q, want UUID %q (state-heal failed)", c.updateCalledID, uuid)
	}
	// Returned output must carry the healed UUID.
	if out.ProviderID != uuid {
		t.Errorf("ResourceOutput.ProviderID = %q, want UUID %q", out.ProviderID, uuid)
	}
}

func TestUpdate_HealStaleName_LookupFails(t *testing.T) {
	// findAppByName fails — Update must propagate the error rather than
	// silently using the stale name as a path parameter.
	c := &stateHealClient{
		listErr: errors.New("api unavailable"),
	}
	d := NewAppPlatformDriverWithClient(c, "nyc3")
	ref := interfaces.ResourceRef{Name: "bmw-staging", ProviderID: "bmw-staging"}
	spec := interfaces.ResourceSpec{
		Name:   "bmw-staging",
		Config: map[string]any{"image": "docker.io/myorg/myapp:latest"},
	}
	_, err := d.Update(context.Background(), ref, spec)
	if err == nil {
		t.Fatal("expected error when name lookup fails, got nil")
	}
}

// ── Delete state-heal tests ──────────────────────────────────────────────────

func TestDelete_UsesUUIDWhenProviderIDIsValid(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	c := &stateHealClient{}
	d := NewAppPlatformDriverWithClient(c, "nyc3")
	ref := interfaces.ResourceRef{Name: "bmw-staging", ProviderID: uuid}
	if err := d.Delete(context.Background(), ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if c.deleteCalledID != uuid {
		t.Errorf("Delete called with ID %q, want UUID %q", c.deleteCalledID, uuid)
	}
}

func TestDelete_HealsStaleName(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	c := &stateHealClient{
		listApps: []*godo.App{
			{ID: uuid, Spec: &godo.AppSpec{Name: "bmw-staging"}},
		},
	}
	d := NewAppPlatformDriverWithClient(c, "nyc3")
	ref := interfaces.ResourceRef{Name: "bmw-staging", ProviderID: "bmw-staging"}
	if err := d.Delete(context.Background(), ref); err != nil {
		t.Fatalf("Delete with stale name ProviderID: %v", err)
	}
	if c.deleteCalledID != uuid {
		t.Errorf("Delete called with %q, want UUID %q (state-heal failed)", c.deleteCalledID, uuid)
	}
}

func TestDelete_HealStaleName_LookupFails(t *testing.T) {
	c := &stateHealClient{listErr: errors.New("api unavailable")}
	d := NewAppPlatformDriverWithClient(c, "nyc3")
	ref := interfaces.ResourceRef{Name: "bmw-staging", ProviderID: "bmw-staging"}
	if err := d.Delete(context.Background(), ref); err == nil {
		t.Fatal("expected error when name lookup fails, got nil")
	}
}
