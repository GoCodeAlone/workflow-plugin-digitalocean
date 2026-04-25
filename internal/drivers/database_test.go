package drivers_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type mockDatabaseClient struct {
	db              *godo.Database
	err             error
	firewallErr     error
	lastCreateReq   *godo.DatabaseCreateRequest
	lastFirewallReq *godo.DatabaseUpdateFirewallRulesRequest
}

func (m *mockDatabaseClient) Create(_ context.Context, req *godo.DatabaseCreateRequest) (*godo.Database, *godo.Response, error) {
	m.lastCreateReq = req
	return m.db, nil, m.err
}
func (m *mockDatabaseClient) Get(_ context.Context, _ string) (*godo.Database, *godo.Response, error) {
	return m.db, nil, m.err
}
func (m *mockDatabaseClient) List(_ context.Context, _ *godo.ListOptions) ([]godo.Database, *godo.Response, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	if m.db == nil {
		return nil, nil, nil
	}
	return []godo.Database{*m.db}, nil, nil
}
func (m *mockDatabaseClient) Resize(_ context.Context, _ string, _ *godo.DatabaseResizeRequest) (*godo.Response, error) {
	return nil, m.err
}
func (m *mockDatabaseClient) Delete(_ context.Context, _ string) (*godo.Response, error) {
	return nil, m.err
}
func (m *mockDatabaseClient) UpdateFirewallRules(_ context.Context, _ string, req *godo.DatabaseUpdateFirewallRulesRequest) (*godo.Response, error) {
	m.lastFirewallReq = req
	return nil, m.firewallErr
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

func TestDatabaseDriver_Create_WithTrustedSources(t *testing.T) {
	mock := &mockDatabaseClient{db: testDatabase()}
	d := drivers.NewDatabaseDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-db",
		Config: map[string]any{
			"engine": "pg",
			"trusted_sources": []any{
				map[string]any{"type": "ip_addr", "value": "10.0.0.1/32"},
				map[string]any{"type": "k8s", "value": "k8s-cluster-uuid"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if mock.lastCreateReq == nil {
		t.Fatal("no create request captured")
	}
	if len(mock.lastCreateReq.Rules) != 2 {
		t.Fatalf("expected 2 firewall rules, got %d", len(mock.lastCreateReq.Rules))
	}
	if mock.lastCreateReq.Rules[0].Type != "ip_addr" || mock.lastCreateReq.Rules[0].Value != "10.0.0.1/32" {
		t.Errorf("rule[0] = {%s %s}, want {ip_addr 10.0.0.1/32}",
			mock.lastCreateReq.Rules[0].Type, mock.lastCreateReq.Rules[0].Value)
	}
	if mock.lastCreateReq.Rules[1].Type != "k8s" || mock.lastCreateReq.Rules[1].Value != "k8s-cluster-uuid" {
		t.Errorf("rule[1] = {%s %s}, want {k8s k8s-cluster-uuid}",
			mock.lastCreateReq.Rules[1].Type, mock.lastCreateReq.Rules[1].Value)
	}
}

func TestDatabaseDriver_Update_WithTrustedSources(t *testing.T) {
	mock := &mockDatabaseClient{db: testDatabase()}
	d := drivers.NewDatabaseDriverWithClient(mock, "nyc3")

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-db", ProviderID: "db-123",
	}, interfaces.ResourceSpec{
		Name: "my-db",
		Config: map[string]any{
			"size": "db-s-2vcpu-4gb",
			"trusted_sources": []any{
				map[string]any{"type": "ip_addr", "value": "192.168.1.0/24"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if mock.lastFirewallReq == nil {
		t.Fatal("UpdateFirewallRules was not called")
	}
	if len(mock.lastFirewallReq.Rules) != 1 {
		t.Fatalf("expected 1 firewall rule, got %d", len(mock.lastFirewallReq.Rules))
	}
	if mock.lastFirewallReq.Rules[0].Type != "ip_addr" || mock.lastFirewallReq.Rules[0].Value != "192.168.1.0/24" {
		t.Errorf("rule[0] = {%s %s}, want {ip_addr 192.168.1.0/24}",
			mock.lastFirewallReq.Rules[0].Type, mock.lastFirewallReq.Rules[0].Value)
	}
}

func TestDatabaseDriver_Update_NoTrustedSources_SkipsFirewall(t *testing.T) {
	mock := &mockDatabaseClient{db: testDatabase()}
	d := drivers.NewDatabaseDriverWithClient(mock, "nyc3")

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-db", ProviderID: "db-123",
	}, interfaces.ResourceSpec{
		Name:   "my-db",
		Config: map[string]any{"size": "db-s-2vcpu-4gb"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if mock.lastFirewallReq != nil {
		t.Error("UpdateFirewallRules should not be called when trusted_sources is absent")
	}
}

func TestDatabaseDriver_Update_EmptyTrustedSources_ClearsFirewall(t *testing.T) {
	// trusted_sources present but empty → UpdateFirewallRules called with empty rules (clears all).
	mock := &mockDatabaseClient{db: testDatabase()}
	d := drivers.NewDatabaseDriverWithClient(mock, "nyc3")

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-db", ProviderID: "db-123",
	}, interfaces.ResourceSpec{
		Name: "my-db",
		Config: map[string]any{
			"size":            "db-s-2vcpu-4gb",
			"trusted_sources": []any{}, // key present, but empty
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if mock.lastFirewallReq == nil {
		t.Fatal("UpdateFirewallRules must be called when trusted_sources key is present (even if empty)")
	}
	if len(mock.lastFirewallReq.Rules) != 0 {
		t.Errorf("expected 0 rules to clear firewall, got %d", len(mock.lastFirewallReq.Rules))
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

func TestDatabaseDriver_SupportsUpsert(t *testing.T) {
	d := drivers.NewDatabaseDriverWithClient(&mockDatabaseClient{}, "nyc3")
	if !d.SupportsUpsert() {
		t.Error("DatabaseDriver.SupportsUpsert() should return true")
	}
}

func TestDatabaseDriver_Read_NameBased(t *testing.T) {
	mock := &mockDatabaseClient{db: testDatabase()}
	d := drivers.NewDatabaseDriverWithClient(mock, "nyc3")

	// Read with empty ProviderID triggers name-based lookup.
	out, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name: "my-db",
	})
	if err != nil {
		t.Fatalf("Read by name: %v", err)
	}
	if out.ProviderID != "db-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "db-123")
	}
	if out.Name != "my-db" {
		t.Errorf("Name = %q, want %q", out.Name, "my-db")
	}
}

func TestDatabaseDriver_Read_NameBased_NotFound(t *testing.T) {
	mock := &mockDatabaseClient{db: nil}
	d := drivers.NewDatabaseDriverWithClient(mock, "nyc3")

	_, err := d.Read(context.Background(), interfaces.ResourceRef{Name: "missing-db"})
	if !errors.Is(err, drivers.ErrResourceNotFound) {
		t.Fatalf("expected ErrResourceNotFound, got: %v", err)
	}
}

func TestDatabaseDriver_Create_EmptyIDFromAPI(t *testing.T) {
	// API returns success but database has empty ID — guard must reject it.
	mock := &mockDatabaseClient{db: &godo.Database{Name: "my-db"}}
	d := drivers.NewDatabaseDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-db",
		Config: map[string]any{"engine": "pg"},
	})
	if err == nil {
		t.Fatal("expected error for empty ProviderID, got nil")
	}
}

func TestDatabaseDriver_Create_ProviderIDIsAPIAssigned(t *testing.T) {
	// ProviderID must be the API-assigned UUID, not the resource name.
	mock := &mockDatabaseClient{db: testDatabase()}
	d := drivers.NewDatabaseDriverWithClient(mock, "nyc3")

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-db",
		Config: map[string]any{"engine": "pg"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID == "my-db" {
		t.Errorf("ProviderID must not equal spec.Name %q; got %q", "my-db", out.ProviderID)
	}
	if out.ProviderID == "" {
		t.Errorf("ProviderID must not be empty")
	}
}

// --- type=app trusted_source name→UUID resolution ---

func makeAppForTrustedSource(name, id string) *godo.App {
	return &godo.App{
		ID:   id,
		Spec: &godo.AppSpec{Name: name},
		ActiveDeployment: &godo.Deployment{
			Phase: godo.DeploymentPhase_Active,
		},
	}
}

func TestDatabaseDriver_Create_WithTrustedSources_AppNameResolved(t *testing.T) {
	// type=app rule whose value is an app name (not UUID) should be resolved
	// to the app's UUID before sending to the DO API.
	const appUUID = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	dbMock := &mockDatabaseClient{db: testDatabase()}
	appMock := &mockAppClient{
		listApps: []*godo.App{makeAppForTrustedSource("bmw-staging", appUUID)},
	}
	d := drivers.NewDatabaseDriverWithClients(dbMock, appMock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-db",
		Config: map[string]any{
			"engine": "pg",
			"trusted_sources": []any{
				map[string]any{"type": "app", "value": "bmw-staging"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if dbMock.lastCreateReq == nil {
		t.Fatal("no create request captured")
	}
	if len(dbMock.lastCreateReq.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(dbMock.lastCreateReq.Rules))
	}
	rule := dbMock.lastCreateReq.Rules[0]
	if rule.Type != "app" {
		t.Errorf("rule.Type = %q, want %q", rule.Type, "app")
	}
	if rule.Value != appUUID {
		t.Errorf("rule.Value = %q, want UUID %q (app name should have been resolved)", rule.Value, appUUID)
	}
}

func TestDatabaseDriver_Create_WithTrustedSources_AppUUIDPassThrough(t *testing.T) {
	// type=app rule whose value is already UUID-shaped must NOT trigger a name lookup.
	const appUUID = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	dbMock := &mockDatabaseClient{db: testDatabase()}
	appMock := &mockAppClient{listErr: fmt.Errorf("should not be called")}
	d := drivers.NewDatabaseDriverWithClients(dbMock, appMock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-db",
		Config: map[string]any{
			"engine": "pg",
			"trusted_sources": []any{
				map[string]any{"type": "app", "value": appUUID},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v (apps List must not be called for UUID-shaped values)", err)
	}
	if dbMock.lastCreateReq == nil || len(dbMock.lastCreateReq.Rules) != 1 {
		t.Fatalf("expected 1 rule")
	}
	if dbMock.lastCreateReq.Rules[0].Value != appUUID {
		t.Errorf("rule.Value = %q, want %q", dbMock.lastCreateReq.Rules[0].Value, appUUID)
	}
}

func TestDatabaseDriver_Update_WithTrustedSources_AppNameResolved(t *testing.T) {
	// type=app rule in an Update firewall call should also resolve name→UUID.
	const appUUID = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	dbMock := &mockDatabaseClient{db: testDatabase()}
	appMock := &mockAppClient{
		listApps: []*godo.App{makeAppForTrustedSource("bmw-staging", appUUID)},
	}
	d := drivers.NewDatabaseDriverWithClients(dbMock, appMock, "nyc3")

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-db", ProviderID: "db-123",
	}, interfaces.ResourceSpec{
		Name: "my-db",
		Config: map[string]any{
			"size": "db-s-2vcpu-4gb",
			"trusted_sources": []any{
				map[string]any{"type": "app", "value": "bmw-staging"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if dbMock.lastFirewallReq == nil {
		t.Fatal("UpdateFirewallRules was not called")
	}
	if len(dbMock.lastFirewallReq.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(dbMock.lastFirewallReq.Rules))
	}
	rule := dbMock.lastFirewallReq.Rules[0]
	if rule.Type != "app" {
		t.Errorf("rule.Type = %q, want %q", rule.Type, "app")
	}
	if rule.Value != appUUID {
		t.Errorf("rule.Value = %q, want UUID %q (app name should have been resolved)", rule.Value, appUUID)
	}
}

func TestDatabaseDriver_Create_AppNameNotFound_ReturnsError(t *testing.T) {
	// When the app name cannot be found in the Apps list, Create must return an
	// error that wraps ErrResourceNotFound and names the missing app.
	dbMock := &mockDatabaseClient{db: testDatabase()}
	appMock := &mockAppClient{listApps: []*godo.App{}} // empty list — name not found
	d := drivers.NewDatabaseDriverWithClients(dbMock, appMock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-db",
		Config: map[string]any{
			"engine": "pg",
			"trusted_sources": []any{
				map[string]any{"type": "app", "value": "nonexistent-app"},
			},
		},
	})
	if err == nil {
		t.Fatal("expected error when app name not found, got nil")
	}
	if !errors.Is(err, drivers.ErrResourceNotFound) {
		t.Errorf("expected error to wrap drivers.ErrResourceNotFound; got: %v", err)
	}
	if !strings.Contains(err.Error(), "nonexistent-app") {
		t.Errorf("expected error to mention missing app name %q; got: %v", "nonexistent-app", err)
	}
}

func TestDatabaseDriver_Create_AppType_NoAppsClient_ReturnsError(t *testing.T) {
	// When no Apps client is configured and the rule value is not UUID-shaped,
	// Create must return an actionable error (not a generic one) that tells the
	// caller the value is not a UUID and no client is available for lookup.
	dbMock := &mockDatabaseClient{db: testDatabase()}
	// NewDatabaseDriverWithClient does NOT wire an apps client.
	d := drivers.NewDatabaseDriverWithClient(dbMock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-db",
		Config: map[string]any{
			"engine": "pg",
			"trusted_sources": []any{
				map[string]any{"type": "app", "value": "bmw-staging"},
			},
		},
	})
	if err == nil {
		t.Fatal("expected error when apps client is nil and value is not UUID, got nil")
	}
	if !strings.Contains(err.Error(), "not a UUID") {
		t.Errorf("expected error to mention 'not a UUID'; got: %v", err)
	}
	if !strings.Contains(err.Error(), "bmw-staging") {
		t.Errorf("expected error to mention the offending value %q; got: %v", "bmw-staging", err)
	}
}
