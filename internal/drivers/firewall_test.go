package drivers_test

import (
	"context"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type mockFirewallClient struct {
	fw  *godo.Firewall
	err error
}

func (m *mockFirewallClient) Create(_ context.Context, _ *godo.FirewallRequest) (*godo.Firewall, *godo.Response, error) {
	return m.fw, nil, m.err
}
func (m *mockFirewallClient) Get(_ context.Context, _ string) (*godo.Firewall, *godo.Response, error) {
	return m.fw, nil, m.err
}
func (m *mockFirewallClient) Update(_ context.Context, _ string, _ *godo.FirewallRequest) (*godo.Firewall, *godo.Response, error) {
	return m.fw, nil, m.err
}
func (m *mockFirewallClient) Delete(_ context.Context, _ string) (*godo.Response, error) {
	return nil, m.err
}

func testFirewall() *godo.Firewall {
	return &godo.Firewall{
		ID:     "fw-123",
		Name:   "my-fw",
		Status: "succeeded",
	}
}

func TestFirewallDriver_Create(t *testing.T) {
	mock := &mockFirewallClient{fw: testFirewall()}
	d := drivers.NewFirewallDriverWithClient(mock)

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-fw",
		Config: map[string]any{
			"inbound_rules": []any{
				map[string]any{"protocol": "tcp", "ports": "80", "sources": []any{"0.0.0.0/0"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID != "fw-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "fw-123")
	}
}

func TestFirewallDriver_HealthCheck(t *testing.T) {
	mock := &mockFirewallClient{fw: testFirewall()}
	d := drivers.NewFirewallDriverWithClient(mock)

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name:       "my-fw",
		ProviderID: "fw-123",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if !result.Healthy {
		t.Errorf("expected healthy firewall")
	}
}
