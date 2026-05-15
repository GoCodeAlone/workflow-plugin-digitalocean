package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/statebackend"
	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Compile-time guard: doIaCServer MUST satisfy the typed state-backend
// contract so the SDK serve hook auto-registers it at plugin startup.
var _ pb.IaCStateBackendServer = (*doIaCServer)(nil)

func TestIaCServer_ListBackendNames(t *testing.T) {
	s := NewIaCServer()
	resp, err := s.ListBackendNames(context.Background(), &pb.ListBackendNamesRequest{})
	if err != nil {
		t.Fatalf("ListBackendNames: %v", err)
	}
	got := resp.GetBackendNames()
	if len(got) != 1 || got[0] != "spaces" {
		t.Errorf("ListBackendNames = %v, want [spaces]", got)
	}
}

func TestIaCServer_StateBackend_NotConfigured(t *testing.T) {
	s := NewIaCServer()
	// With no store injected, the state RPCs must return a clear error rather
	// than panicking on a nil store.
	if _, err := s.GetState(context.Background(), &pb.GetStateRequest{ResourceId: "x"}); err == nil {
		t.Error("GetState: expected error when backend not configured")
	}
	if _, err := s.SaveState(context.Background(), &pb.SaveStateRequest{State: &pb.IaCState{ResourceId: "x"}}); err == nil {
		t.Error("SaveState: expected error when backend not configured")
	}
	if _, err := s.ListStates(context.Background(), &pb.ListStatesRequest{}); err == nil {
		t.Error("ListStates: expected error when backend not configured")
	}
	if _, err := s.DeleteState(context.Background(), &pb.DeleteStateRequest{ResourceId: "x"}); err == nil {
		t.Error("DeleteState: expected error when backend not configured")
	}
	if _, err := s.Lock(context.Background(), &pb.LockRequest{ResourceId: "x"}); err == nil {
		t.Error("Lock: expected error when backend not configured")
	}
	if _, err := s.Unlock(context.Background(), &pb.UnlockRequest{ResourceId: "x"}); err == nil {
		t.Error("Unlock: expected error when backend not configured")
	}
}

func TestIaCServer_StateBackend_Configure(t *testing.T) {
	s := NewIaCServer()

	// Before Configure, the backend is unconfigured — resolveStore must fail.
	if _, err := s.stateBackend.resolveStore(); err == nil {
		t.Fatal("resolveStore: expected FailedPrecondition before Configure")
	}

	cfg := map[string]any{
		"region": "nyc3",
		"bucket": "tfstate",
		"prefix": "iac-state/",
	}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal cfg: %v", err)
	}
	if _, err := s.Configure(context.Background(), &pb.ConfigureRequest{
		BackendName: "spaces",
		ConfigJson:  cfgJSON,
	}); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	// After Configure, the lazily-constructed store must resolve.
	store, err := s.stateBackend.resolveStore()
	if err != nil {
		t.Fatalf("resolveStore after Configure: %v", err)
	}
	if store == nil {
		t.Fatal("resolveStore after Configure: store is nil")
	}

	// A Configure for a backend name this plugin does not serve must be rejected.
	if _, err := s.Configure(context.Background(), &pb.ConfigureRequest{
		BackendName: "s3",
		ConfigJson:  cfgJSON,
	}); err == nil {
		t.Error("Configure: expected error for unknown backend name")
	}

	// A Configure missing the required 'bucket' config must be rejected.
	noBucket, _ := json.Marshal(map[string]any{"region": "nyc3"})
	if _, err := s.Configure(context.Background(), &pb.ConfigureRequest{
		BackendName: "spaces",
		ConfigJson:  noBucket,
	}); err == nil {
		t.Error("Configure: expected error when 'bucket' config is missing")
	}
}

func TestIaCServer_StateBackend_RoundTrip(t *testing.T) {
	s := NewIaCServer()
	store := statebackend.NewSpacesIaCStateStoreWithClient(newMockSpacesClient(), "test-bucket", "iac-state/")
	s.stateBackend.setStateStore(store)

	ctx := context.Background()
	in := &pb.IaCState{
		ResourceId:   "spaces-rt",
		ResourceType: "kubernetes",
		Provider:     "digitalocean",
		Status:       "active",
		OutputsJson:  []byte(`{"endpoint":"https://k8s.example.com"}`),
		ConfigJson:   []byte(`{"region":"nyc3"}`),
	}
	if _, err := s.SaveState(ctx, &pb.SaveStateRequest{State: in}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	got, err := s.GetState(ctx, &pb.GetStateRequest{ResourceId: "spaces-rt"})
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if !got.GetExists() {
		t.Fatal("GetState: expected exists=true")
	}
	if got.GetState().GetProvider() != "digitalocean" {
		t.Errorf("Provider = %q, want digitalocean", got.GetState().GetProvider())
	}
	if string(got.GetState().GetOutputsJson()) != `{"endpoint":"https://k8s.example.com"}` {
		t.Errorf("OutputsJson round-trip mismatch: %s", got.GetState().GetOutputsJson())
	}

	list, err := s.ListStates(ctx, &pb.ListStatesRequest{})
	if err != nil {
		t.Fatalf("ListStates: %v", err)
	}
	if len(list.GetStates()) != 1 {
		t.Errorf("ListStates = %d, want 1", len(list.GetStates()))
	}

	if _, err := s.Lock(ctx, &pb.LockRequest{ResourceId: "spaces-rt"}); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if _, err := s.Unlock(ctx, &pb.UnlockRequest{ResourceId: "spaces-rt"}); err != nil {
		t.Fatalf("Unlock: %v", err)
	}

	if _, err := s.DeleteState(ctx, &pb.DeleteStateRequest{ResourceId: "spaces-rt"}); err != nil {
		t.Fatalf("DeleteState: %v", err)
	}
	after, err := s.GetState(ctx, &pb.GetStateRequest{ResourceId: "spaces-rt"})
	if err != nil {
		t.Fatalf("GetState after delete: %v", err)
	}
	if after.GetExists() {
		t.Error("GetState after delete: expected exists=false")
	}
}

// mockSpacesClient is an in-memory statebackend.SpacesS3Client for the
// round-trip test.
type mockSpacesClient struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newMockSpacesClient() *mockSpacesClient {
	return &mockSpacesClient{objects: make(map[string][]byte)}
}

func (m *mockSpacesClient) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := aws.ToString(input.Key)
	data, ok := m.objects[key]
	if !ok {
		return nil, &types.NoSuchKey{Message: aws.String("NoSuchKey: " + key)}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(data))}, nil
}

func (m *mockSpacesClient) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := aws.ToString(input.Key)
	if aws.ToString(input.IfNoneMatch) == "*" {
		if _, exists := m.objects[key]; exists {
			return nil, fmt.Errorf("PreconditionFailed: object %q already exists", key)
		}
	}
	data, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	m.objects[key] = data
	return &s3.PutObjectOutput{}, nil
}

func (m *mockSpacesClient) DeleteObject(_ context.Context, input *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, aws.ToString(input.Key))
	return &s3.DeleteObjectOutput{}, nil
}

func (m *mockSpacesClient) ListObjectsV2(_ context.Context, input *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := aws.ToString(input.Prefix)
	var contents []types.Object
	for key := range m.objects {
		if strings.HasPrefix(key, prefix) {
			contents = append(contents, types.Object{Key: aws.String(key)})
		}
	}
	return &s3.ListObjectsV2Output{Contents: contents, IsTruncated: aws.Bool(false)}, nil
}

func (m *mockSpacesClient) HeadObject(_ context.Context, input *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := aws.ToString(input.Key)
	if _, ok := m.objects[key]; !ok {
		return nil, &types.NotFound{Message: aws.String("NotFound: " + key)}
	}
	return &s3.HeadObjectOutput{}, nil
}
