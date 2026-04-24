package drivers_test

import (
	"context"
	"fmt"
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

func TestLoadBalancerDriver_Create_Error(t *testing.T) {
	mock := &mockLBClient{err: fmt.Errorf("api failure")}
	d := drivers.NewLoadBalancerDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-lb",
		Config: map[string]any{"algorithm": "round_robin"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestLoadBalancerDriver_Update_Success(t *testing.T) {
	mock := &mockLBClient{lb: testLB()}
	d := drivers.NewLoadBalancerDriverWithClient(mock, "nyc3")

	out, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-lb", ProviderID: "lb-123",
	}, interfaces.ResourceSpec{
		Name:   "my-lb",
		Config: map[string]any{"algorithm": "least_connections"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.ProviderID != "lb-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "lb-123")
	}
}

func TestLoadBalancerDriver_Update_Error(t *testing.T) {
	mock := &mockLBClient{err: fmt.Errorf("update failed")}
	d := drivers.NewLoadBalancerDriverWithClient(mock, "nyc3")

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-lb", ProviderID: "lb-123",
	}, interfaces.ResourceSpec{
		Name:   "my-lb",
		Config: map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
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

func TestLoadBalancerDriver_Delete_Error(t *testing.T) {
	mock := &mockLBClient{err: fmt.Errorf("delete failed")}
	d := drivers.NewLoadBalancerDriverWithClient(mock, "nyc3")

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "my-lb", ProviderID: "lb-123",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestLoadBalancerDriver_Diff_NilCurrent(t *testing.T) {
	mock := &mockLBClient{}
	d := drivers.NewLoadBalancerDriverWithClient(mock, "nyc3")

	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{Name: "my-lb"}, nil)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=true when current is nil")
	}
}

func TestLoadBalancerDriver_Diff_NoChanges(t *testing.T) {
	mock := &mockLBClient{}
	d := drivers.NewLoadBalancerDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{ProviderID: "lb-123"}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{Name: "my-lb"}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=false when current exists")
	}
}

func TestLoadBalancerDriver_HealthCheck_Unhealthy(t *testing.T) {
	lb := &godo.LoadBalancer{
		ID:     "lb-123",
		Name:   "my-lb",
		Status: "new",
	}
	mock := &mockLBClient{lb: lb}
	d := drivers.NewLoadBalancerDriverWithClient(mock, "nyc3")

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name: "my-lb", ProviderID: "lb-123",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if result.Healthy {
		t.Errorf("expected unhealthy for lb with status 'new'")
	}
}

func TestLoadBalancerDriver_Create_EmptyIDFromAPI(t *testing.T) {
	// API returns success but load balancer has empty ID — guard must reject it.
	mock := &mockLBClient{lb: &godo.LoadBalancer{Name: "my-lb"}}
	d := drivers.NewLoadBalancerDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-lb",
		Config: map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error for empty ProviderID, got nil")
	}
}

func TestLoadBalancerDriver_Create_ProviderIDIsAPIAssigned(t *testing.T) {
	// ProviderID must be the API-assigned UUID, not the resource name.
	mock := &mockLBClient{lb: testLB()}
	d := drivers.NewLoadBalancerDriverWithClient(mock, "nyc3")

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-lb",
		Config: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID == "my-lb" {
		t.Errorf("ProviderID must not equal spec.Name %q; got %q", "my-lb", out.ProviderID)
	}
	if out.ProviderID == "" {
		t.Errorf("ProviderID must not be empty")
	}
}
