package drivers_test

import (
	"context"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type mockLBClient struct {
	lb  *godo.LoadBalancer
	err error
}

func (m *mockLBClient) Create(_ context.Context, _ *godo.LoadBalancerRequest) (*godo.LoadBalancer, *godo.Response, error) {
	return m.lb, nil, m.err
}
func (m *mockLBClient) Get(_ context.Context, _ string) (*godo.LoadBalancer, *godo.Response, error) {
	return m.lb, nil, m.err
}
func (m *mockLBClient) Update(_ context.Context, _ string, _ *godo.LoadBalancerRequest) (*godo.LoadBalancer, *godo.Response, error) {
	return m.lb, nil, m.err
}
func (m *mockLBClient) Delete(_ context.Context, _ string) (*godo.Response, error) {
	return nil, m.err
}

func testLB() *godo.LoadBalancer {
	return &godo.LoadBalancer{
		ID:     "lb-123",
		Name:   "my-lb",
		IP:     "1.2.3.4",
		Status: "active",
	}
}

func TestLoadBalancerDriver_Create(t *testing.T) {
	mock := &mockLBClient{lb: testLB()}
	d := drivers.NewLoadBalancerDriverWithClient(mock, "nyc3")

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-lb",
		Config: map[string]any{"algorithm": "round_robin"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID != "lb-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "lb-123")
	}
	if out.Status != "active" {
		t.Errorf("Status = %q, want %q", out.Status, "active")
	}
	if ip, _ := out.Outputs["ip"].(string); ip != "1.2.3.4" {
		t.Errorf("ip = %q, want %q", ip, "1.2.3.4")
	}
}

func TestLoadBalancerDriver_Read(t *testing.T) {
	mock := &mockLBClient{lb: testLB()}
	d := drivers.NewLoadBalancerDriverWithClient(mock, "nyc3")

	out, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name: "my-lb", ProviderID: "lb-123",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.ProviderID != "lb-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "lb-123")
	}
}

func TestLoadBalancerDriver_HealthCheck_Active(t *testing.T) {
	mock := &mockLBClient{lb: testLB()}
	d := drivers.NewLoadBalancerDriverWithClient(mock, "nyc3")

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name: "my-lb", ProviderID: "lb-123",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if !result.Healthy {
		t.Errorf("expected healthy lb")
	}
}

func TestLoadBalancerDriver_Delete(t *testing.T) {
	mock := &mockLBClient{lb: testLB()}
	d := drivers.NewLoadBalancerDriverWithClient(mock, "nyc3")

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "my-lb", ProviderID: "lb-123",
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
