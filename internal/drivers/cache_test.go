package drivers_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type mockCacheClient struct {
	db  *godo.Database
	err error
}

func (m *mockCacheClient) Create(_ context.Context, _ *godo.DatabaseCreateRequest) (*godo.Database, *godo.Response, error) {
	return m.db, nil, m.err
}
func (m *mockCacheClient) Get(_ context.Context, _ string) (*godo.Database, *godo.Response, error) {
	return m.db, nil, m.err
}
func (m *mockCacheClient) List(_ context.Context, _ *godo.ListOptions) ([]godo.Database, *godo.Response, error) {
	if m.db != nil {
		return []godo.Database{*m.db}, nil, nil
	}
	return nil, nil, m.err
}
func (m *mockCacheClient) Resize(_ context.Context, _ string, _ *godo.DatabaseResizeRequest) (*godo.Response, error) {
	return nil, m.err
}
func (m *mockCacheClient) Delete(_ context.Context, _ string) (*godo.Response, error) {
	return nil, m.err
}

func testRedisDB() *godo.Database {
	return &godo.Database{
		ID:          "cache-123",
		Name:        "my-cache",
		EngineSlug:  "redis",
		VersionSlug: "7",
		SizeSlug:    "db-s-1vcpu-1gb",
		RegionSlug:  "nyc3",
		Status:      "online",
		Connection: &godo.DatabaseConnection{
			Host: "my-cache.db.ondigitalocean.com",
			Port: 25061,
			URI:  "rediss://my-cache.db.ondigitalocean.com:25061",
		},
	}
}

func TestCacheDriver_Create(t *testing.T) {
	mock := &mockCacheClient{db: testRedisDB()}
	d := drivers.NewCacheDriverWithClient(mock, "nyc3")

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-cache",
		Config: map[string]any{
			"size":    "db-s-1vcpu-1gb",
			"version": "7",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID != "cache-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "cache-123")
	}
	if out.Status != "online" {
		t.Errorf("Status = %q, want %q", out.Status, "online")
	}
}

func TestCacheDriver_Create_Error(t *testing.T) {
	mock := &mockCacheClient{err: fmt.Errorf("api failure")}
	d := drivers.NewCacheDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-cache",
		Config: map[string]any{"size": "db-s-1vcpu-1gb", "version": "7"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCacheDriver_Read_Success(t *testing.T) {
	mock := &mockCacheClient{db: testRedisDB()}
	d := drivers.NewCacheDriverWithClient(mock, "nyc3")

	out, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name: "my-cache", ProviderID: "cache-123",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.ProviderID != "cache-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "cache-123")
	}
}

func TestCacheDriver_Update_Success(t *testing.T) {
	mock := &mockCacheClient{db: testRedisDB()}
	d := drivers.NewCacheDriverWithClient(mock, "nyc3")

	out, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-cache", ProviderID: "cache-123",
	}, interfaces.ResourceSpec{
		Name:   "my-cache",
		Config: map[string]any{"size": "db-s-2vcpu-2gb", "num_nodes": 1},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.ProviderID != "cache-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "cache-123")
	}
}

func TestCacheDriver_Update_Error(t *testing.T) {
	mock := &mockCacheClient{err: fmt.Errorf("resize failed")}
	d := drivers.NewCacheDriverWithClient(mock, "nyc3")

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-cache", ProviderID: "cache-123",
	}, interfaces.ResourceSpec{
		Name:   "my-cache",
		Config: map[string]any{"size": "db-s-2vcpu-2gb"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCacheDriver_Delete_Success(t *testing.T) {
	mock := &mockCacheClient{db: testRedisDB()}
	d := drivers.NewCacheDriverWithClient(mock, "nyc3")

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "my-cache", ProviderID: "cache-123",
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestCacheDriver_Delete_Error(t *testing.T) {
	mock := &mockCacheClient{err: fmt.Errorf("delete failed")}
	d := drivers.NewCacheDriverWithClient(mock, "nyc3")

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "my-cache", ProviderID: "cache-123",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestCacheDriver_Diff_HasChanges(t *testing.T) {
	mock := &mockCacheClient{}
	d := drivers.NewCacheDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{"size": "db-s-1vcpu-1gb"},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{"size": "db-s-2vcpu-2gb"},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=true for size change")
	}
}

func TestCacheDriver_Diff_NoChanges(t *testing.T) {
	mock := &mockCacheClient{}
	d := drivers.NewCacheDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{"size": "db-s-1vcpu-1gb"},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{"size": "db-s-1vcpu-1gb"},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=false when size unchanged")
	}
}

func TestCacheDriver_HealthCheck(t *testing.T) {
	mock := &mockCacheClient{db: testRedisDB()}
	d := drivers.NewCacheDriverWithClient(mock, "nyc3")

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name: "my-cache", ProviderID: "cache-123",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if !result.Healthy {
		t.Errorf("expected healthy cache")
	}
}

func TestCacheDriver_HealthCheck_Unhealthy(t *testing.T) {
	db := &godo.Database{
		ID:     "cache-123",
		Name:   "my-cache",
		Status: "migrating",
	}
	mock := &mockCacheClient{db: db}
	d := drivers.NewCacheDriverWithClient(mock, "nyc3")

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name: "my-cache", ProviderID: "cache-123",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if result.Healthy {
		t.Errorf("expected unhealthy for migrating cache")
	}
}

func TestCacheDriver_Create_EmptyIDFromAPI(t *testing.T) {
	// API returns success but cache cluster has empty ID — guard must reject it.
	mock := &mockCacheClient{db: &godo.Database{Name: "my-cache"}}
	d := drivers.NewCacheDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-cache",
		Config: map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error for empty ProviderID, got nil")
	}
}

func TestCacheDriver_Create_ProviderIDIsAPIAssigned(t *testing.T) {
	// ProviderID must be the API-assigned UUID, not the resource name.
	mock := &mockCacheClient{db: testRedisDB()}
	d := drivers.NewCacheDriverWithClient(mock, "nyc3")

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-cache",
		Config: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID == "my-cache" {
		t.Errorf("ProviderID must not equal spec.Name %q; got %q", "my-cache", out.ProviderID)
	}
	if out.ProviderID == "" {
		t.Errorf("ProviderID must not be empty")
	}
}
