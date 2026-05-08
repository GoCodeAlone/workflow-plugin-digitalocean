package drivers

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// internalMockAppClient is a minimal AppPlatformClient for image-presence wiring tests.
// It is intentionally minimal — only Create and Update are called by the image-presence
// path; the other methods are no-ops that return empty results.
type internalMockAppClient struct {
	app      *godo.App
	appErr   error
	listApps []*godo.App
}

func (m *internalMockAppClient) Create(_ context.Context, _ *godo.AppCreateRequest) (*godo.App, *godo.Response, error) {
	return m.app, nil, m.appErr
}
func (m *internalMockAppClient) Get(_ context.Context, _ string) (*godo.App, *godo.Response, error) {
	return m.app, nil, m.appErr
}
func (m *internalMockAppClient) List(_ context.Context, _ *godo.ListOptions) ([]*godo.App, *godo.Response, error) {
	return m.listApps, &godo.Response{}, nil
}
func (m *internalMockAppClient) Update(_ context.Context, _ string, _ *godo.AppUpdateRequest) (*godo.App, *godo.Response, error) {
	return m.app, nil, m.appErr
}
func (m *internalMockAppClient) CreateDeployment(_ context.Context, _ string, _ ...*godo.DeploymentCreateRequest) (*godo.Deployment, *godo.Response, error) {
	return nil, nil, nil
}
func (m *internalMockAppClient) ListDeployments(_ context.Context, _ string, _ *godo.ListOptions) ([]*godo.Deployment, *godo.Response, error) {
	return nil, &godo.Response{}, nil
}
func (m *internalMockAppClient) Delete(_ context.Context, _ string) (*godo.Response, error) {
	return nil, nil
}
func (m *internalMockAppClient) GetLogs(_ context.Context, _, _, _ string, _ godo.AppLogType, _ bool, _ int) (*godo.AppLogs, *godo.Response, error) {
	return nil, nil, nil
}

// internalMockRegistryClient is a package-internal mock used for testing
// verifyImagePresentInDOCR and the AppPlatformDriver image-presence wiring.
// (The registry_test.go mock lives in package drivers_test and is not
// accessible here.)
type internalMockRegistryClient struct {
	reg     *godo.Registry
	err     error
	tags    map[string][]*godo.RepositoryTag
	tagsErr error
}

func (m *internalMockRegistryClient) Create(_ context.Context, _ *godo.RegistryCreateRequest) (*godo.Registry, *godo.Response, error) {
	return m.reg, nil, m.err
}
func (m *internalMockRegistryClient) Get(_ context.Context) (*godo.Registry, *godo.Response, error) {
	return m.reg, nil, m.err
}
func (m *internalMockRegistryClient) Delete(_ context.Context) (*godo.Response, error) {
	return nil, m.err
}
func (m *internalMockRegistryClient) ListRepositoryTags(_ context.Context, _ string, repo string, _ *godo.ListOptions) ([]*godo.RepositoryTag, *godo.Response, error) {
	if m.tagsErr != nil {
		return nil, nil, m.tagsErr
	}
	return m.tags[repo], &godo.Response{}, nil
}

func TestVerifyImagePresentInDOCR_PresentTag(t *testing.T) {
	mock := &internalMockRegistryClient{
		reg: &godo.Registry{Name: "coredump-registry"},
		tags: map[string][]*godo.RepositoryTag{
			"core-dump-server": {{Tag: "abc123"}, {Tag: "def456"}},
		},
	}
	err := verifyImagePresentInDOCR(context.Background(), mock,
		"registry.digitalocean.com/coredump-registry/core-dump-server:abc123")
	if err != nil {
		t.Fatalf("expected nil; got %v", err)
	}
}

func TestVerifyImagePresentInDOCR_AbsentTag(t *testing.T) {
	mock := &internalMockRegistryClient{
		reg: &godo.Registry{Name: "coredump-registry"},
		tags: map[string][]*godo.RepositoryTag{
			"core-dump-server": {{Tag: "def456"}},
		},
	}
	err := verifyImagePresentInDOCR(context.Background(), mock,
		"registry.digitalocean.com/coredump-registry/core-dump-server:abc123")
	if err == nil {
		t.Fatalf("expected ErrImageNotInRegistry; got nil")
	}
	if !errors.Is(err, interfaces.ErrImageNotInRegistry) {
		t.Fatalf("expected wrap of ErrImageNotInRegistry; got %v", err)
	}
}

func TestVerifyImagePresentInDOCR_NonDOCRImage_ReturnsNil(t *testing.T) {
	mock := &internalMockRegistryClient{} // intentionally empty; should never be called
	err := verifyImagePresentInDOCR(context.Background(), mock,
		"ghcr.io/gocodealone/core-dump-server:abc123")
	if err != nil {
		t.Fatalf("non-DOCR images must skip presence check; got %v", err)
	}
}

func TestVerifyImagePresentInDOCR_RegistryGetError_ReturnsNil(t *testing.T) {
	mock := &internalMockRegistryClient{err: fmt.Errorf("rate limited")}
	err := verifyImagePresentInDOCR(context.Background(), mock,
		"registry.digitalocean.com/coredump-registry/core-dump-server:abc123")
	if err != nil {
		t.Fatalf("conservative behavior: registry-get error must NOT block apply; got %v", err)
	}
}

func TestVerifyImagePresentInDOCR_ListTagsError_ReturnsNil(t *testing.T) {
	mock := &internalMockRegistryClient{
		reg:     &godo.Registry{Name: "coredump-registry"},
		tagsErr: fmt.Errorf("list-tags 502"),
	}
	err := verifyImagePresentInDOCR(context.Background(), mock,
		"registry.digitalocean.com/coredump-registry/core-dump-server:abc123")
	if err != nil {
		t.Fatalf("conservative behavior: list-tags error must NOT block apply; got %v", err)
	}
}

func TestVerifyImagePresentInDOCR_ParseFailure_ReturnsNil(t *testing.T) {
	mock := &internalMockRegistryClient{}
	err := verifyImagePresentInDOCR(context.Background(), mock, "garbage::not-a-ref")
	if err != nil {
		t.Fatalf("unparseable ref must skip presence check; got %v", err)
	}
}

// --- AppPlatformDriver wiring tests ---

func TestAppPlatformDriver_Diff_RejectsAbsentDOCRImage(t *testing.T) {
	appsMock := &internalMockAppClient{}
	regMock := &internalMockRegistryClient{
		reg:  &godo.Registry{Name: "coredump-registry"},
		tags: map[string][]*godo.RepositoryTag{"core-dump-server": {{Tag: "old"}}},
	}
	d := NewAppPlatformDriverWithClients(appsMock, regMock, "nyc3")

	desired := interfaces.ResourceSpec{
		Type: "infra.container_service",
		Name: "test-app",
		Config: map[string]any{
			"image": "registry.digitalocean.com/coredump-registry/core-dump-server:newgone",
		},
	}
	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{"image": "registry.digitalocean.com/coredump-registry/core-dump-server:old"},
	}
	_, err := d.Diff(context.Background(), desired, current)
	if err == nil || !errors.Is(err, interfaces.ErrImageNotInRegistry) {
		t.Fatalf("expected ErrImageNotInRegistry; got %v", err)
	}
}

func TestAppPlatformDriver_Diff_AcceptsPresentDOCRImage(t *testing.T) {
	appsMock := &internalMockAppClient{}
	regMock := &internalMockRegistryClient{
		reg:  &godo.Registry{Name: "coredump-registry"},
		tags: map[string][]*godo.RepositoryTag{"core-dump-server": {{Tag: "newpresent"}}},
	}
	d := NewAppPlatformDriverWithClients(appsMock, regMock, "nyc3")

	desired := interfaces.ResourceSpec{
		Type: "infra.container_service",
		Name: "test-app",
		Config: map[string]any{
			"image": "registry.digitalocean.com/coredump-registry/core-dump-server:newpresent",
		},
	}
	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{"image": "registry.digitalocean.com/coredump-registry/core-dump-server:old"},
	}
	diff, err := d.Diff(context.Background(), desired, current)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if diff == nil || !diff.NeedsUpdate {
		t.Fatalf("expected NeedsUpdate=true; got %#v", diff)
	}
}

func TestAppPlatformDriver_Diff_SkipsNonDOCRImage(t *testing.T) {
	appsMock := &internalMockAppClient{}
	regMock := &internalMockRegistryClient{} // no tags configured; presence check would fail if invoked
	d := NewAppPlatformDriverWithClients(appsMock, regMock, "nyc3")

	desired := interfaces.ResourceSpec{
		Type: "infra.container_service", Name: "x",
		Config: map[string]any{"image": "ghcr.io/gocodealone/core-dump-server:abc"},
	}
	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{"image": "ghcr.io/gocodealone/core-dump-server:old"},
	}
	_, err := d.Diff(context.Background(), desired, current)
	if err != nil {
		t.Fatalf("non-DOCR images must not invoke presence check: %v", err)
	}
}

func TestAppPlatformDriver_Create_RejectsAbsentDOCRImage(t *testing.T) {
	appsMock := &internalMockAppClient{}
	regMock := &internalMockRegistryClient{
		reg:  &godo.Registry{Name: "coredump-registry"},
		tags: map[string][]*godo.RepositoryTag{"core-dump-server": {}},
	}
	d := NewAppPlatformDriverWithClients(appsMock, regMock, "nyc3")
	spec := interfaces.ResourceSpec{
		Type: "infra.container_service", Name: "test-app",
		Config: map[string]any{
			"image": "registry.digitalocean.com/coredump-registry/core-dump-server:gone",
		},
	}
	_, err := d.Create(context.Background(), spec)
	if !errors.Is(err, interfaces.ErrImageNotInRegistry) {
		t.Fatalf("Create must reject absent DOCR image; got %v", err)
	}
}

func TestAppPlatformDriver_Update_RejectsAbsentDOCRImage(t *testing.T) {
	appsMock := &internalMockAppClient{
		app: &godo.App{ID: "app-uuid-123"},
	}
	regMock := &internalMockRegistryClient{
		reg:  &godo.Registry{Name: "coredump-registry"},
		tags: map[string][]*godo.RepositoryTag{"core-dump-server": {}},
	}
	d := NewAppPlatformDriverWithClients(appsMock, regMock, "nyc3")
	spec := interfaces.ResourceSpec{
		Type: "infra.container_service", Name: "test-app",
		Config: map[string]any{
			"image": "registry.digitalocean.com/coredump-registry/core-dump-server:gone",
		},
	}
	ref := interfaces.ResourceRef{Name: "test-app", ProviderID: "app-uuid-123"}
	_, err := d.Update(context.Background(), ref, spec)
	if !errors.Is(err, interfaces.ErrImageNotInRegistry) {
		t.Fatalf("Update must reject absent DOCR image; got %v", err)
	}
}

// TestAppPlatformDriver_Diff_MapFormImageConfig verifies that structured image
// config (map[string]any) is also checked by the image-presence pre-flight.
// This covers the Copilot finding that only string-form images were checked.
func TestAppPlatformDriver_Diff_MapFormImageConfig_RejectsAbsent(t *testing.T) {
	appsMock := &internalMockAppClient{}
	regMock := &internalMockRegistryClient{
		reg:  &godo.Registry{Name: "coredump-registry"},
		tags: map[string][]*godo.RepositoryTag{"core-dump-server": {}}, // empty: all tags absent
	}
	d := NewAppPlatformDriverWithClients(appsMock, regMock, "nyc3")

	desired := interfaces.ResourceSpec{
		Type: "infra.container_service", Name: "test-app",
		Config: map[string]any{
			"image": map[string]any{
				"registry_type": "DOCR",
				"repository":    "core-dump-server",
				"tag":           "gone",
			},
		},
	}
	current := &interfaces.ResourceOutput{Outputs: map[string]any{}}
	_, err := d.Diff(context.Background(), desired, current)
	if !errors.Is(err, interfaces.ErrImageNotInRegistry) {
		t.Fatalf("Diff must reject absent DOCR image in map-form config; got %v", err)
	}
}
