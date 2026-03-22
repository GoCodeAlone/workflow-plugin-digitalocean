package drivers_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type mockDatabaseClient struct {
	db  *godo.Database
	err error
}

func (m *mockDatabaseClient) Create(_ context.Context, _ *godo.DatabaseCreateRequest) (*godo.Database, *godo.Response, error) {
	return m.db, nil, m.err
}
func (m *mockDatabaseClient) Get(_ context.Context, _ string) (*godo.Database, *godo.Response, error) {
	return m.db, nil, m.err
}
func (m *mockDatabaseClient) Resize(_ context.Context, _ string, _ *godo.DatabaseResizeRequest) (*godo.Response, error) {
	return nil, m.err
}
func (m *mockDatabaseClient) Delete(_ context.Context, _ string) (*godo.Response, error) {
	return nil, m.err
}

func testDatabase() *godo.Database {
	return &godo.Database{
		ID:          "db-123",
		Name:        "my-db",
		EngineSlug:  "pg",
		VersionSlug: "15",
		SizeSlug:    "db-s-1vcpu-2gb",
		RegionSlug:  "nyc3",
		Status:      "online",
		Connection: &godo.DatabaseConnection{
			Host:     "my-db.db.ondigitalocean.com",
			Port:     5432,
			Database: "defaultdb",
			User:     "doadmin",
			URI:      "postgresql://doadmin@my-db.db.ondigitalocean.com:5432/defaultdb",
		},
	}
}

func TestDatabaseDriver_Create(t *testing.T) {
	mock := &mockDatabaseClient{db: testDatabase()}
	d := drivers.NewDatabaseDriverWithClient(mock, "nyc3")

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-db",
		Config: map[string]any{
			"engine":    "pg",
			"version":   "15",
			"size":      "db-s-1vcpu-2gb",
			"num_nodes": 1,
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID != "db-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "db-123")
	}
	if out.Status != "online" {
		t.Errorf("Status = %q, want %q", out.Status, "online")
	}
	if host, _ := out.Outputs["host"].(string); host == "" {
		t.Error("expected host in outputs")
	}
}

func TestDatabaseDriver_Create_Error(t *testing.T) {
	mock := &mockDatabaseClient{err: fmt.Errorf("api failure")}
	d := drivers.NewDatabaseDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-db",
		Config: map[string]any{"engine": "pg"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDatabaseDriver_Read_Success(t *testing.T) {
	mock := &mockDatabaseClient{db: testDatabase()}
	d := drivers.NewDatabaseDriverWithClient(mock, "nyc3")

	out, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name: "my-db", ProviderID: "db-123",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.ProviderID != "db-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "db-123")
	}
}

func TestDatabaseDriver_Update_Success(t *testing.T) {
	mock := &mockDatabaseClient{db: testDatabase()}
	d := drivers.NewDatabaseDriverWithClient(mock, "nyc3")

	out, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-db", ProviderID: "db-123",
	}, interfaces.ResourceSpec{
		Name:   "my-db",
		Config: map[string]any{"size": "db-s-2vcpu-4gb", "num_nodes": 2},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.ProviderID != "db-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "db-123")
	}
}

func TestDatabaseDriver_Update_Error(t *testing.T) {
	mock := &mockDatabaseClient{err: fmt.Errorf("resize failed")}
	d := drivers.NewDatabaseDriverWithClient(mock, "nyc3")

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-db", ProviderID: "db-123",
	}, interfaces.ResourceSpec{
		Name:   "my-db",
		Config: map[string]any{"size": "db-s-2vcpu-4gb"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDatabaseDriver_Delete_Success(t *testing.T) {
	mock := &mockDatabaseClient{db: testDatabase()}
	d := drivers.NewDatabaseDriverWithClient(mock, "nyc3")

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "my-db", ProviderID: "db-123",
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestDatabaseDriver_Delete_Error(t *testing.T) {
	mock := &mockDatabaseClient{err: fmt.Errorf("delete failed")}
	d := drivers.NewDatabaseDriverWithClient(mock, "nyc3")

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "my-db", ProviderID: "db-123",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDatabaseDriver_Diff_HasChanges(t *testing.T) {
	mock := &mockDatabaseClient{}
	d := drivers.NewDatabaseDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{"size": "db-s-1vcpu-2gb"},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{"size": "db-s-2vcpu-4gb"},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=true for size change")
	}
}

func TestDatabaseDriver_Diff_NoChanges(t *testing.T) {
	mock := &mockDatabaseClient{}
	d := drivers.NewDatabaseDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{"size": "db-s-1vcpu-2gb"},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{"size": "db-s-1vcpu-2gb"},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=false when size unchanged")
	}
}

func TestDatabaseDriver_HealthCheck(t *testing.T) {
	mock := &mockDatabaseClient{db: testDatabase()}
	d := drivers.NewDatabaseDriverWithClient(mock, "nyc3")

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name:       "my-db",
		ProviderID: "db-123",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if !result.Healthy {
		t.Errorf("expected healthy, got message: %s", result.Message)
	}
}

func TestDatabaseDriver_HealthCheck_Unhealthy(t *testing.T) {
	db := &godo.Database{
		ID:     "db-123",
		Name:   "my-db",
		Status: "migrating",
	}
	mock := &mockDatabaseClient{db: db}
	d := drivers.NewDatabaseDriverWithClient(mock, "nyc3")

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name: "my-db", ProviderID: "db-123",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if result.Healthy {
		t.Errorf("expected unhealthy for migrating db")
	}
}
