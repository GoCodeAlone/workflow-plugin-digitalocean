package internal

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// fakeBucketClient implements spacesBucketClient for unit tests.
type fakeBucketClient struct {
	headErr      error
	createErr    error
	headCalled   bool
	createCalled bool
	lastBucket   string
	lastRegion   string
}

func (f *fakeBucketClient) HeadBucket(_ context.Context, input *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	f.headCalled = true
	if input.Bucket != nil {
		f.lastBucket = *input.Bucket
	}
	if f.headErr != nil {
		return nil, f.headErr
	}
	return &s3.HeadBucketOutput{}, nil
}

func (f *fakeBucketClient) CreateBucket(_ context.Context, input *s3.CreateBucketInput, _ ...func(*s3.Options)) (*s3.CreateBucketOutput, error) {
	f.createCalled = true
	if input.Bucket != nil {
		f.lastBucket = *input.Bucket
	}
	if input.CreateBucketConfiguration != nil {
		f.lastRegion = string(input.CreateBucketConfiguration.LocationConstraint)
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &s3.CreateBucketOutput{}, nil
}

// fakeFactory wraps a fakeBucketClient in a clientFactory-compatible func.
func fakeFactory(c *fakeBucketClient) func(string, string, string) spacesBucketClient {
	return func(_, _, _ string) spacesBucketClient { return c }
}

// ── bootstrapSpacesBucketWithClient unit tests ────────────────────────────────

func TestBootstrapSpacesBucket_AlreadyExists(t *testing.T) {
	client := &fakeBucketClient{headErr: nil} // nil → bucket exists (200 OK)
	if err := bootstrapSpacesBucketWithClient(t.Context(), "my-bucket", "nyc3", client); err != nil {
		t.Fatalf("expected no error when bucket already exists, got: %v", err)
	}
	if !client.headCalled {
		t.Error("expected HeadBucket to be called")
	}
	if client.createCalled {
		t.Error("expected CreateBucket NOT to be called when bucket already exists")
	}
}

func TestBootstrapSpacesBucket_CreatesNew(t *testing.T) {
	client := &fakeBucketClient{
		headErr: &s3types.NotFound{}, // 404 → bucket does not exist
	}
	if err := bootstrapSpacesBucketWithClient(t.Context(), "new-bucket", "nyc3", client); err != nil {
		t.Fatalf("expected no error creating new bucket, got: %v", err)
	}
	if !client.createCalled {
		t.Error("expected CreateBucket to be called when bucket does not exist")
	}
	if client.lastBucket != "new-bucket" {
		t.Errorf("CreateBucket bucket: want %q, got %q", "new-bucket", client.lastBucket)
	}
	if client.lastRegion != "nyc3" {
		t.Errorf("CreateBucket region: want %q, got %q", "nyc3", client.lastRegion)
	}
}

func TestBootstrapSpacesBucket_CreateErrorSurfaced(t *testing.T) {
	createErr := errors.New("spaces quota exceeded")
	client := &fakeBucketClient{
		headErr:   &s3types.NotFound{},
		createErr: createErr,
	}
	err := bootstrapSpacesBucketWithClient(t.Context(), "my-bucket", "nyc3", client)
	if err == nil {
		t.Fatal("expected error when CreateBucket fails, got nil")
	}
	if !errors.Is(err, createErr) {
		t.Errorf("error chain should contain createErr, got: %v", err)
	}
}

func TestBootstrapSpacesBucket_HeadFatalError(t *testing.T) {
	// A non-404 HeadBucket error (e.g. 403 Forbidden) must be surfaced as-is.
	headErr := errors.New("403 Forbidden")
	client := &fakeBucketClient{headErr: headErr}
	err := bootstrapSpacesBucketWithClient(t.Context(), "my-bucket", "nyc3", client)
	if err == nil {
		t.Fatal("expected error for non-404 HeadBucket failure, got nil")
	}
	if !errors.Is(err, headErr) {
		t.Errorf("error chain should contain headErr, got: %v", err)
	}
	if client.createCalled {
		t.Error("CreateBucket must NOT be called on non-404 HeadBucket error")
	}
}

func TestBootstrapSpacesBucket_AlreadyOwnedByYou(t *testing.T) {
	// BucketAlreadyOwnedByYou on Create is treated as idempotent success.
	client := &fakeBucketClient{
		headErr:   &s3types.NotFound{},
		createErr: &s3types.BucketAlreadyOwnedByYou{},
	}
	if err := bootstrapSpacesBucketWithClient(t.Context(), "my-bucket", "nyc3", client); err != nil {
		t.Fatalf("expected no error for BucketAlreadyOwnedByYou, got: %v", err)
	}
}

// ── DOProvider.bootstrapStateBackendWithFactory tests ────────────────────────
// All tests below inject a fakeBucketClient via fakeFactory — no network I/O.

func TestDOProvider_BootstrapStateBackend_CamelCaseKeys(t *testing.T) {
	// BMW infra.yaml uses accessKey/secretKey (camelCase). Verify they are resolved.
	client := &fakeBucketClient{headErr: nil}
	p := NewDOProvider()
	_, err := p.bootstrapStateBackendWithFactory(t.Context(), map[string]any{
		"bucket":    "bmw-state",
		"region":    "nyc3",
		"accessKey": "k",
		"secretKey": "s",
	}, fakeFactory(client))
	if err != nil {
		t.Fatalf("camelCase keys should succeed, got: %v", err)
	}
	if !client.headCalled {
		t.Error("expected HeadBucket to be called")
	}
}

func TestDOProvider_BootstrapStateBackend_SnakeCaseKeys(t *testing.T) {
	// Verify snake_case access_key/secret_key fallback also resolves correctly.
	client := &fakeBucketClient{headErr: nil}
	p := NewDOProvider()
	_, err := p.bootstrapStateBackendWithFactory(t.Context(), map[string]any{
		"bucket":     "bmw-state",
		"region":     "nyc3",
		"access_key": "k",
		"secret_key": "s",
	}, fakeFactory(client))
	if err != nil {
		t.Fatalf("snake_case keys should succeed, got: %v", err)
	}
	if !client.headCalled {
		t.Error("expected HeadBucket to be called")
	}
}

func TestDOProvider_BootstrapStateBackend_MissingBucket(t *testing.T) {
	p := NewDOProvider()
	_, err := p.bootstrapStateBackendWithFactory(t.Context(), map[string]any{
		"region":    "nyc3",
		"accessKey": "k",
		"secretKey": "s",
	}, fakeFactory(&fakeBucketClient{}))
	if !errors.Is(err, errMissingBucket) {
		t.Fatalf("expected errMissingBucket, got: %v", err)
	}
}

func TestDOProvider_BootstrapStateBackend_MissingCredentials(t *testing.T) {
	p := NewDOProvider()
	_, err := p.bootstrapStateBackendWithFactory(t.Context(), map[string]any{
		"bucket": "bmw-state",
		"region": "nyc3",
		// no credentials at all
	}, fakeFactory(&fakeBucketClient{}))
	if !errors.Is(err, errMissingCredentials) {
		t.Fatalf("expected errMissingCredentials, got: %v", err)
	}
}

func TestDOProvider_BootstrapStateBackend_DefaultsRegion(t *testing.T) {
	// When cfg["region"] is absent and the provider has no configured region,
	// BootstrapStateBackend must pass "nyc3" as the region.
	var gotRegion string
	client := &fakeBucketClient{headErr: nil}
	factory := func(_, _, region string) spacesBucketClient {
		gotRegion = region
		return client
	}
	p := NewDOProvider() // p.region == "" (not yet initialized)
	_, err := p.bootstrapStateBackendWithFactory(t.Context(), map[string]any{
		"bucket":    "bmw-state",
		"accessKey": "k",
		"secretKey": "s",
		// no "region"
	}, factory)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotRegion != "nyc3" {
		t.Errorf("default region = %q, want %q", gotRegion, "nyc3")
	}
}

func TestDOProvider_BootstrapStateBackend_IdempotentOnExists(t *testing.T) {
	// HeadBucket success → CreateBucket must NOT be called.
	client := &fakeBucketClient{headErr: nil}
	err := bootstrapSpacesBucketWithClient(t.Context(), "existing-bucket", "sfo3", client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.createCalled {
		t.Error("CreateBucket must NOT be called when bucket already exists")
	}
}
