package drivers_test

import (
	"context"
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
