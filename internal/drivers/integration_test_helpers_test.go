package drivers

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// fakeAppsClient implements AppPlatformClient for integration tests.
// It tracks every call so tests can assert which DO API methods were invoked
// and with which identifiers, returning configurable responses seeded per test.
type fakeAppsClient struct {
	// Seeded responses.
	createResp *godo.App            // returned by Create
	getByID    map[string]*godo.App // keyed by app ID
	updateResp map[string]*godo.App // keyed by app ID (what Update returns)
	deleteErrs map[string]error     // keyed by app ID (nil ⇒ 204)

	// Call log.
	createCalls        []string // app names created
	getCalls           []string // app IDs passed to Get
	updateCalls        []string // app IDs passed to Update
	createDeployCalls  []string // app IDs passed to CreateDeployment
	deleteCalls        []string // app IDs passed to Delete
	listCalls          int
	listDeploymentsFor []string
}

func newFakeAppsClient() *fakeAppsClient {
	return &fakeAppsClient{
		getByID:    map[string]*godo.App{},
		updateResp: map[string]*godo.App{},
		deleteErrs: map[string]error{},
	}
}

// ── AppPlatformClient interface ───────────────────────────────────────────────

func (f *fakeAppsClient) Create(_ context.Context, req *godo.AppCreateRequest) (*godo.App, *godo.Response, error) {
	f.createCalls = append(f.createCalls, req.Spec.Name)
	if f.createResp != nil {
		f.getByID[f.createResp.ID] = f.createResp
		return f.createResp, fakeResp(http.StatusCreated), nil
	}
	// Default: deterministic fake UUID based on name.
	app := &godo.App{
		ID:        fmt.Sprintf("fake-uuid-for-%s", req.Spec.Name),
		Spec:      req.Spec,
		CreatedAt: time.Now(),
	}
	f.getByID[app.ID] = app
	return app, fakeResp(http.StatusCreated), nil
}

func (f *fakeAppsClient) Get(_ context.Context, id string) (*godo.App, *godo.Response, error) {
	f.getCalls = append(f.getCalls, id)
	if app, ok := f.getByID[id]; ok {
		return app, fakeResp(http.StatusOK), nil
	}
	return nil, fakeResp(http.StatusNotFound), &godo.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusNotFound},
		Message:  "not found",
	}
}

func (f *fakeAppsClient) List(_ context.Context, _ *godo.ListOptions) ([]*godo.App, *godo.Response, error) {
	f.listCalls++
	out := make([]*godo.App, 0, len(f.getByID))
	for _, a := range f.getByID {
		out = append(out, a)
	}
	resp := &godo.Response{
		Response: &http.Response{StatusCode: http.StatusOK},
		Links:    &godo.Links{},
	}
	return out, resp, nil
}

func (f *fakeAppsClient) Update(_ context.Context, id string, _ *godo.AppUpdateRequest) (*godo.App, *godo.Response, error) {
	f.updateCalls = append(f.updateCalls, id)
	if app, ok := f.updateResp[id]; ok {
		return app, fakeResp(http.StatusOK), nil
	}
	if app, ok := f.getByID[id]; ok {
		return app, fakeResp(http.StatusOK), nil
	}
	return nil, fakeResp(http.StatusNotFound), &godo.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusNotFound},
		Message:  fmt.Sprintf("not found: %s", id),
	}
}

func (f *fakeAppsClient) CreateDeployment(_ context.Context, id string, _ ...*godo.DeploymentCreateRequest) (*godo.Deployment, *godo.Response, error) {
	f.createDeployCalls = append(f.createDeployCalls, id)
	return &godo.Deployment{ID: fmt.Sprintf("dep-%s", id)}, fakeResp(http.StatusOK), nil
}

func (f *fakeAppsClient) ListDeployments(_ context.Context, id string, _ *godo.ListOptions) ([]*godo.Deployment, *godo.Response, error) {
	f.listDeploymentsFor = append(f.listDeploymentsFor, id)
	return nil, fakeResp(http.StatusOK), nil
}

func (f *fakeAppsClient) Delete(_ context.Context, id string) (*godo.Response, error) {
	f.deleteCalls = append(f.deleteCalls, id)
	if err, ok := f.deleteErrs[id]; ok && err != nil {
		return fakeResp(http.StatusConflict), err
	}
	delete(f.getByID, id)
	return fakeResp(http.StatusNoContent), nil
}

// fakeResp creates a minimal *godo.Response with the given status.
func fakeResp(status int) *godo.Response {
	return &godo.Response{Response: &http.Response{StatusCode: status}}
}

// ── inMemoryState: minimal state round-trip store ─────────────────────────────

type inMemoryState struct {
	resources map[string]interfaces.ResourceState // keyed by Name
}

func newInMemoryState() *inMemoryState {
	return &inMemoryState{resources: map[string]interfaces.ResourceState{}}
}

func (s *inMemoryState) get(name string) (interfaces.ResourceState, bool) {
	r, ok := s.resources[name]
	return r, ok
}

func (s *inMemoryState) put(r interfaces.ResourceState) {
	s.resources[r.Name] = r
}

// ── applySim: simulate wfctl's apply→persist flow for tests ──────────────────

// applySim mimics wfctl's Apply pattern: if no state exists it calls Create,
// otherwise it calls Update with the stored ref. The returned ResourceOutput
// is persisted back to the in-memory state, mirroring wfctl's state-write loop:
//
//	state.ProviderID = out.ProviderID  // wfctl trusts the driver
func applySim(t *testing.T, ctx context.Context, driver *AppPlatformDriver, state *inMemoryState, spec interfaces.ResourceSpec) interfaces.ResourceState {
	t.Helper()

	existing, have := state.get(spec.Name)
	var out *interfaces.ResourceOutput
	var err error
	if !have {
		out, err = driver.Create(ctx, spec)
	} else {
		ref := interfaces.ResourceRef{
			Name:       existing.Name,
			Type:       existing.Type,
			ProviderID: existing.ProviderID,
		}
		out, err = driver.Update(ctx, ref, spec)
	}
	if err != nil {
		t.Fatalf("applySim %q: %v", spec.Name, err)
	}

	rs := interfaces.ResourceState{
		ID:         spec.Name,
		Name:       spec.Name,
		Type:       spec.Type,
		ProviderID: out.ProviderID, // ← the field under test
		Outputs:    out.Outputs,
		UpdatedAt:  time.Now().UTC(),
	}
	state.put(rs)
	return rs
}

// newTestDriver returns an AppPlatformDriver wired to a fakeAppsClient.
func newTestDriver(t *testing.T) (*AppPlatformDriver, *fakeAppsClient) {
	t.Helper()
	client := newFakeAppsClient()
	return &AppPlatformDriver{client: client, region: "nyc3"}, client
}
