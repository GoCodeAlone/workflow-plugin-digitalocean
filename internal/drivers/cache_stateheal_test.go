package drivers

// State-heal tests for CacheDriver.Update / Delete.
// Cache.Update calls Resize (not a direct Update), then Read.

import (
	"context"
	"errors"
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type cacheStateHealMock struct {
	listDBs   []godo.Database
	listErr   error
	listCalls int

	resizeCalledID string
	resizeErr      error

	deleteCalledID string
	deleteErr      error

	// getDB is returned by Get (called by Read after Resize).
	getDB  *godo.Database
	getErr error

	createDB  *godo.Database
	createErr error
}

func (m *cacheStateHealMock) Create(_ context.Context, _ *godo.DatabaseCreateRequest) (*godo.Database, *godo.Response, error) {
	return m.createDB, nil, m.createErr
}
func (m *cacheStateHealMock) Get(_ context.Context, _ string) (*godo.Database, *godo.Response, error) {
	return m.getDB, nil, m.getErr
}
func (m *cacheStateHealMock) List(_ context.Context, _ *godo.ListOptions) ([]godo.Database, *godo.Response, error) {
	m.listCalls++
	return m.listDBs, nil, m.listErr
}
func (m *cacheStateHealMock) Resize(_ context.Context, dbID string, _ *godo.DatabaseResizeRequest) (*godo.Response, error) {
	m.resizeCalledID = dbID
	return nil, m.resizeErr
}
func (m *cacheStateHealMock) Delete(_ context.Context, dbID string) (*godo.Response, error) {
	m.deleteCalledID = dbID
	return nil, m.deleteErr
}

// ── Create ────────────────────────────────────────────────────────────────────

func TestCacheDriver_Create_PersistsUUIDInState(t *testing.T) {
	const wantUUID = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &cacheStateHealMock{
		createDB: &godo.Database{ID: wantUUID, Name: "my-cache"},
	}
	d := NewCacheDriverWithClient(m, "nyc3")
	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-cache",
		Config: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID != wantUUID {
		t.Errorf("ProviderID = %q, want UUID %q", out.ProviderID, wantUUID)
	}
	if out.ProviderID == "my-cache" {
		t.Error("ProviderID must not be the spec name")
	}
}

// ── Update ────────────────────────────────────────────────────────────────────

func TestCacheDriver_Update_UsesExistingUUID(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &cacheStateHealMock{
		getDB: &godo.Database{ID: uuid, Name: "my-cache", Status: "online"},
	}
	d := NewCacheDriverWithClient(m, "nyc3")
	_, err := d.Update(context.Background(),
		interfaces.ResourceRef{Name: "my-cache", ProviderID: uuid},
		interfaces.ResourceSpec{Name: "my-cache", Config: map[string]any{}},
	)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if m.resizeCalledID != uuid {
		t.Errorf("Resize called with %q, want %q", m.resizeCalledID, uuid)
	}
	if m.listCalls != 0 {
		t.Errorf("listCalls = %d, want 0 (heal must not fire for valid UUID)", m.listCalls)
	}
}

func TestCacheDriver_Update_HealsStaleName(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &cacheStateHealMock{
		listDBs: []godo.Database{{ID: uuid, Name: "my-cache", EngineSlug: "redis"}},
		getDB:   &godo.Database{ID: uuid, Name: "my-cache", Status: "online"},
	}
	d := NewCacheDriverWithClient(m, "nyc3")
	out, err := d.Update(context.Background(),
		interfaces.ResourceRef{Name: "my-cache", ProviderID: "my-cache"}, // stale name
		interfaces.ResourceSpec{Name: "my-cache", Config: map[string]any{}},
	)
	if err != nil {
		t.Fatalf("Update with stale name: %v", err)
	}
	if m.listCalls < 1 {
		t.Errorf("listCalls = %d, want ≥ 1 (heal must fire)", m.listCalls)
	}
	if m.resizeCalledID != uuid {
		t.Errorf("Resize called with %q, want UUID %q", m.resizeCalledID, uuid)
	}
	if out.ProviderID != uuid {
		t.Errorf("output ProviderID = %q, want UUID %q", out.ProviderID, uuid)
	}
}

func TestCacheDriver_Update_HealFails_WhenListFails(t *testing.T) {
	m := &cacheStateHealMock{listErr: errors.New("api unavailable")}
	d := NewCacheDriverWithClient(m, "nyc3")
	_, err := d.Update(context.Background(),
		interfaces.ResourceRef{Name: "my-cache", ProviderID: "my-cache"},
		interfaces.ResourceSpec{Name: "my-cache", Config: map[string]any{}},
	)
	if err == nil {
		t.Fatal("expected error when heal lookup fails, got nil")
	}
}

// ── Delete ────────────────────────────────────────────────────────────────────

func TestCacheDriver_Delete_HealsStaleName(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &cacheStateHealMock{
		listDBs: []godo.Database{{ID: uuid, Name: "my-cache", EngineSlug: "redis"}},
	}
	d := NewCacheDriverWithClient(m, "nyc3")
	if err := d.Delete(context.Background(),
		interfaces.ResourceRef{Name: "my-cache", ProviderID: "my-cache"},
	); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if m.deleteCalledID != uuid {
		t.Errorf("Delete called with %q, want UUID %q", m.deleteCalledID, uuid)
	}
}

// ── HealthCheck state-heal tests ─────────────────────────────────────────────

func TestCacheDriver_HealthCheck_HealsStaleName(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &cacheStateHealMock{
		listDBs: []godo.Database{{ID: uuid, Name: "my-cache", EngineSlug: "redis"}},
		getDB:   &godo.Database{ID: uuid, Name: "my-cache", Status: "online"},
	}
	d := NewCacheDriverWithClient(m, "nyc3")
	ref := interfaces.ResourceRef{Name: "my-cache", ProviderID: "my-cache"} // stale name
	result, err := d.HealthCheck(context.Background(), ref)
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if m.listCalls < 1 {
		t.Errorf("listCalls = %d, want ≥ 1 (resolve must fire for stale name)", m.listCalls)
	}
	if !result.Healthy {
		t.Errorf("Healthy = false, want true after state-heal")
	}
}

// ── Scale state-heal tests ────────────────────────────────────────────────────

func TestCacheDriver_Scale_HealsStaleName(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &cacheStateHealMock{
		listDBs: []godo.Database{{ID: uuid, Name: "my-cache", EngineSlug: "redis"}},
		getDB:   &godo.Database{ID: uuid, Name: "my-cache", Status: "online"},
	}
	d := NewCacheDriverWithClient(m, "nyc3")
	ref := interfaces.ResourceRef{Name: "my-cache", ProviderID: "my-cache"} // stale name
	_, err := d.Scale(context.Background(), ref, 3)
	if err != nil {
		t.Fatalf("Scale: %v", err)
	}
	if m.listCalls < 1 {
		t.Errorf("listCalls = %d, want ≥ 1 (resolve must fire for stale name)", m.listCalls)
	}
	if m.resizeCalledID != uuid {
		t.Errorf("Resize called with %q, want UUID %q", m.resizeCalledID, uuid)
	}
}
