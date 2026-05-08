package drivers

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

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
		reg:  &godo.Registry{Name: "coredump-registry"},
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
		reg:  &godo.Registry{Name: "coredump-registry"},
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
