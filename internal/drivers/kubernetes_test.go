package drivers_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type mockK8sClient struct {
	cluster  *godo.KubernetesCluster
	nodePool *godo.KubernetesNodePool
	err      error
}

func (m *mockK8sClient) Create(_ context.Context, _ *godo.KubernetesClusterCreateRequest) (*godo.KubernetesCluster, *godo.Response, error) {
	return m.cluster, nil, m.err
}
func (m *mockK8sClient) Get(_ context.Context, _ string) (*godo.KubernetesCluster, *godo.Response, error) {
	return m.cluster, nil, m.err
}
func (m *mockK8sClient) Update(_ context.Context, _ string, _ *godo.KubernetesClusterUpdateRequest) (*godo.KubernetesCluster, *godo.Response, error) {
	return m.cluster, nil, m.err
}
func (m *mockK8sClient) Delete(_ context.Context, _ string) (*godo.Response, error) {
	return nil, m.err
}
func (m *mockK8sClient) List(_ context.Context, _ *godo.ListOptions) ([]*godo.KubernetesCluster, *godo.Response, error) {
	if m.cluster != nil {
		return []*godo.KubernetesCluster{m.cluster}, nil, nil
	}
	return nil, nil, m.err
}
func (m *mockK8sClient) UpdateNodePool(_ context.Context, _, _ string, _ *godo.KubernetesNodePoolUpdateRequest) (*godo.KubernetesNodePool, *godo.Response, error) {
	if m.nodePool != nil {
		return m.nodePool, nil, m.err
	}
	return &godo.KubernetesNodePool{ID: "pool-1", Count: 5}, nil, m.err
}

func testCluster() *godo.KubernetesCluster {
	return &godo.KubernetesCluster{
		ID:          "k8s-123",
		Name:        "my-cluster",
		RegionSlug:  "nyc3",
		VersionSlug: "1.29",
		Endpoint:    "https://k8s.example.com",
		Status: &godo.KubernetesClusterStatus{
			State: godo.KubernetesClusterStatusRunning,
		},
		NodePools: []*godo.KubernetesNodePool{
			{ID: "pool-1", Name: "default", Count: 3},
		},
	}
}

func TestKubernetesDriver_Create(t *testing.T) {
	mock := &mockK8sClient{cluster: testCluster()}
	d := drivers.NewKubernetesDriverWithClient(mock, "nyc3")

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-cluster",
		Config: map[string]any{
			"node_count": 3,
			"node_size":  "s-2vcpu-4gb",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID != "k8s-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "k8s-123")
	}
}

func TestKubernetesDriver_HealthCheck_Running(t *testing.T) {
	mock := &mockK8sClient{cluster: testCluster()}
	d := drivers.NewKubernetesDriverWithClient(mock, "nyc3")

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name:       "my-cluster",
		ProviderID: "k8s-123",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if !result.Healthy {
		t.Error("expected healthy cluster")
	}
}

func TestKubernetesDriver_Create_Error(t *testing.T) {
	mock := &mockK8sClient{err: fmt.Errorf("api failure")}
	d := drivers.NewKubernetesDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-cluster",
		Config: map[string]any{"node_count": 3},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestKubernetesDriver_Read_Success(t *testing.T) {
	mock := &mockK8sClient{cluster: testCluster()}
	d := drivers.NewKubernetesDriverWithClient(mock, "nyc3")

	out, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name: "my-cluster", ProviderID: "k8s-123",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.ProviderID != "k8s-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "k8s-123")
	}
}

func TestKubernetesDriver_Update_Success(t *testing.T) {
	mock := &mockK8sClient{cluster: testCluster()}
	d := drivers.NewKubernetesDriverWithClient(mock, "nyc3")

	out, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-cluster", ProviderID: "k8s-123",
	}, interfaces.ResourceSpec{Name: "my-cluster", Config: map[string]any{}})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.ProviderID != "k8s-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "k8s-123")
	}
}

func TestKubernetesDriver_Update_Error(t *testing.T) {
	mock := &mockK8sClient{err: fmt.Errorf("update failed")}
	d := drivers.NewKubernetesDriverWithClient(mock, "nyc3")

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-cluster", ProviderID: "k8s-123",
	}, interfaces.ResourceSpec{Name: "my-cluster", Config: map[string]any{}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestKubernetesDriver_Delete_Success(t *testing.T) {
	mock := &mockK8sClient{cluster: testCluster()}
	d := drivers.NewKubernetesDriverWithClient(mock, "nyc3")

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "my-cluster", ProviderID: "k8s-123",
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestKubernetesDriver_Delete_Error(t *testing.T) {
	mock := &mockK8sClient{err: fmt.Errorf("delete failed")}
	d := drivers.NewKubernetesDriverWithClient(mock, "nyc3")

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "my-cluster", ProviderID: "k8s-123",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestKubernetesDriver_Diff_NilCurrent(t *testing.T) {
	mock := &mockK8sClient{}
	d := drivers.NewKubernetesDriverWithClient(mock, "nyc3")

	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{Name: "my-cluster"}, nil)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=true when current is nil")
	}
}

func TestKubernetesDriver_Diff_NoChanges(t *testing.T) {
	mock := &mockK8sClient{}
	d := drivers.NewKubernetesDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{ProviderID: "k8s-123"}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{Name: "my-cluster"}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=false when current exists")
	}
}

func TestKubernetesDriver_HealthCheck_Unhealthy(t *testing.T) {
	cluster := &godo.KubernetesCluster{
		ID:   "k8s-123",
		Name: "my-cluster",
		Status: &godo.KubernetesClusterStatus{
			State:   godo.KubernetesClusterStatusProvisioning,
			Message: "still provisioning",
		},
	}
	mock := &mockK8sClient{cluster: cluster}
	d := drivers.NewKubernetesDriverWithClient(mock, "nyc3")

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name: "my-cluster", ProviderID: "k8s-123",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if result.Healthy {
		t.Errorf("expected unhealthy for provisioning cluster")
	}
}

func TestKubernetesDriver_Scale(t *testing.T) {
	mock := &mockK8sClient{cluster: testCluster()}
	d := drivers.NewKubernetesDriverWithClient(mock, "nyc3")

	out, err := d.Scale(context.Background(), interfaces.ResourceRef{
		Name:       "my-cluster",
		ProviderID: "k8s-123",
	}, 5)
	if err != nil {
		t.Fatalf("Scale: %v", err)
	}
	if out.ProviderID != "k8s-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "k8s-123")
	}
}

func TestKubernetesDriver_Create_EmptyIDFromAPI(t *testing.T) {
	// API returns success but cluster has empty ID — guard must reject it.
	mock := &mockK8sClient{cluster: &godo.KubernetesCluster{Name: "my-cluster"}}
	d := drivers.NewKubernetesDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-cluster",
		Config: map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error for empty ProviderID, got nil")
	}
}

func TestKubernetesDriver_Create_ProviderIDIsAPIAssigned(t *testing.T) {
	// ProviderID must be the API-assigned UUID, not the resource name.
	mock := &mockK8sClient{cluster: testCluster()}
	d := drivers.NewKubernetesDriverWithClient(mock, "nyc3")

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-cluster",
		Config: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID == "my-cluster" {
		t.Errorf("ProviderID must not equal spec.Name %q; got %q", "my-cluster", out.ProviderID)
	}
	if out.ProviderID == "" {
		t.Errorf("ProviderID must not be empty")
	}
}
