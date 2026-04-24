package drivers

// State-heal tests for KubernetesDriver.Update / Delete.

import (
	"context"
	"errors"
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type k8sStateHealMock struct {
	listClusters []*godo.KubernetesCluster
	listErr      error
	listCalls    int

	updatedCluster *godo.KubernetesCluster
	updateErr      error
	updateCalledID string

	deleteCalledID string
	deleteErr      error

	createCluster *godo.KubernetesCluster
	createErr     error

	// getCluster is returned by Get (used by HealthCheck and Scale after heal).
	getCluster *godo.KubernetesCluster
}

func (m *k8sStateHealMock) Create(_ context.Context, _ *godo.KubernetesClusterCreateRequest) (*godo.KubernetesCluster, *godo.Response, error) {
	return m.createCluster, nil, m.createErr
}
func (m *k8sStateHealMock) Get(_ context.Context, _ string) (*godo.KubernetesCluster, *godo.Response, error) {
	if m.getCluster != nil {
		return m.getCluster, nil, nil
	}
	return nil, nil, errors.New("not implemented in k8sStateHealMock")
}
func (m *k8sStateHealMock) List(_ context.Context, _ *godo.ListOptions) ([]*godo.KubernetesCluster, *godo.Response, error) {
	m.listCalls++
	return m.listClusters, nil, m.listErr
}
func (m *k8sStateHealMock) Update(_ context.Context, clusterID string, _ *godo.KubernetesClusterUpdateRequest) (*godo.KubernetesCluster, *godo.Response, error) {
	m.updateCalledID = clusterID
	return m.updatedCluster, nil, m.updateErr
}
func (m *k8sStateHealMock) Delete(_ context.Context, clusterID string) (*godo.Response, error) {
	m.deleteCalledID = clusterID
	return nil, m.deleteErr
}
func (m *k8sStateHealMock) UpdateNodePool(_ context.Context, _, _ string, _ *godo.KubernetesNodePoolUpdateRequest) (*godo.KubernetesNodePool, *godo.Response, error) {
	return &godo.KubernetesNodePool{}, nil, nil
}

// ── Create ────────────────────────────────────────────────────────────────────

func TestKubernetesDriver_Create_PersistsUUIDInState(t *testing.T) {
	const wantUUID = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &k8sStateHealMock{
		createCluster: &godo.KubernetesCluster{ID: wantUUID, Name: "my-cluster"},
	}
	d := NewKubernetesDriverWithClient(m, "nyc3")
	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-cluster",
		Config: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID != wantUUID {
		t.Errorf("ProviderID = %q, want UUID %q", out.ProviderID, wantUUID)
	}
	if out.ProviderID == "my-cluster" {
		t.Error("ProviderID must not be the spec name")
	}
}

// ── Update ────────────────────────────────────────────────────────────────────

func TestKubernetesDriver_Update_UsesExistingUUID(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &k8sStateHealMock{
		updatedCluster: &godo.KubernetesCluster{ID: uuid, Name: "my-cluster"},
	}
	d := NewKubernetesDriverWithClient(m, "nyc3")
	_, err := d.Update(context.Background(),
		interfaces.ResourceRef{Name: "my-cluster", ProviderID: uuid},
		interfaces.ResourceSpec{Name: "my-cluster", Config: map[string]any{}},
	)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if m.updateCalledID != uuid {
		t.Errorf("Update called with %q, want %q", m.updateCalledID, uuid)
	}
	if m.listCalls != 0 {
		t.Errorf("listCalls = %d, want 0 (heal must not fire for valid UUID)", m.listCalls)
	}
}

func TestKubernetesDriver_Update_HealsStaleName(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &k8sStateHealMock{
		listClusters:   []*godo.KubernetesCluster{{ID: uuid, Name: "my-cluster"}},
		updatedCluster: &godo.KubernetesCluster{ID: uuid, Name: "my-cluster"},
	}
	d := NewKubernetesDriverWithClient(m, "nyc3")
	out, err := d.Update(context.Background(),
		interfaces.ResourceRef{Name: "my-cluster", ProviderID: "my-cluster"}, // stale name
		interfaces.ResourceSpec{Name: "my-cluster", Config: map[string]any{}},
	)
	if err != nil {
		t.Fatalf("Update with stale name: %v", err)
	}
	if m.listCalls < 1 {
		t.Errorf("listCalls = %d, want ≥ 1 (heal must fire)", m.listCalls)
	}
	if m.updateCalledID != uuid {
		t.Errorf("Update called with %q, want UUID %q", m.updateCalledID, uuid)
	}
	if out.ProviderID != uuid {
		t.Errorf("output ProviderID = %q, want UUID %q", out.ProviderID, uuid)
	}
}

func TestKubernetesDriver_Update_HealFails_WhenResourceNotFound(t *testing.T) {
	m := &k8sStateHealMock{listErr: errors.New("api unavailable")}
	d := NewKubernetesDriverWithClient(m, "nyc3")
	_, err := d.Update(context.Background(),
		interfaces.ResourceRef{Name: "my-cluster", ProviderID: "my-cluster"},
		interfaces.ResourceSpec{Name: "my-cluster", Config: map[string]any{}},
	)
	if err == nil {
		t.Fatal("expected error when heal lookup fails, got nil")
	}
}

// ── Delete ────────────────────────────────────────────────────────────────────

func TestKubernetesDriver_Delete_HealsStaleName(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &k8sStateHealMock{
		listClusters: []*godo.KubernetesCluster{{ID: uuid, Name: "my-cluster"}},
	}
	d := NewKubernetesDriverWithClient(m, "nyc3")
	if err := d.Delete(context.Background(),
		interfaces.ResourceRef{Name: "my-cluster", ProviderID: "my-cluster"},
	); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if m.deleteCalledID != uuid {
		t.Errorf("Delete called with %q, want UUID %q", m.deleteCalledID, uuid)
	}
}

// ── HealthCheck state-heal tests ─────────────────────────────────────────────

func TestKubernetesDriver_HealthCheck_HealsStaleName(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	m := &k8sStateHealMock{
		listClusters: []*godo.KubernetesCluster{{ID: uuid, Name: "my-cluster"}},
		getCluster: &godo.KubernetesCluster{
			ID:     uuid,
			Name:   "my-cluster",
			Status: &godo.KubernetesClusterStatus{State: godo.KubernetesClusterStatusRunning},
		},
	}
	d := NewKubernetesDriverWithClient(m, "nyc3")
	ref := interfaces.ResourceRef{Name: "my-cluster", ProviderID: "my-cluster"} // stale name
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

func TestKubernetesDriver_Scale_HealsStaleName(t *testing.T) {
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	pool := &godo.KubernetesNodePool{ID: "pool-1", Count: 1}
	m := &k8sStateHealMock{
		listClusters: []*godo.KubernetesCluster{{ID: uuid, Name: "my-cluster"}},
		getCluster:   &godo.KubernetesCluster{ID: uuid, Name: "my-cluster", NodePools: []*godo.KubernetesNodePool{pool}},
	}
	d := NewKubernetesDriverWithClient(m, "nyc3")
	ref := interfaces.ResourceRef{Name: "my-cluster", ProviderID: "my-cluster"} // stale name
	_, err := d.Scale(context.Background(), ref, 3)
	if err != nil {
		t.Fatalf("Scale: %v", err)
	}
	if m.listCalls < 1 {
		t.Errorf("listCalls = %d, want ≥ 1 (resolve must fire for stale name)", m.listCalls)
	}
}
