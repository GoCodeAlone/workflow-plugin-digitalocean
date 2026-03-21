package drivers_test

import (
	"context"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type mockK8sClient struct {
	cluster *godo.KubernetesCluster
	err     error
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
