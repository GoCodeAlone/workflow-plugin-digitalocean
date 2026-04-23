package drivers_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type mockFirewallClient struct {
	fw      *godo.Firewall
	err     error
	listErr error
}

func (m *mockFirewallClient) Create(_ context.Context, _ *godo.FirewallRequest) (*godo.Firewall, *godo.Response, error) {
	return m.fw, nil, m.err
}
func (m *mockFirewallClient) Get(_ context.Context, _ string) (*godo.Firewall, *godo.Response, error) {
	return m.fw, nil, m.err
}
func (m *mockFirewallClient) List(_ context.Context, _ *godo.ListOptions) ([]godo.Firewall, *godo.Response, error) {
	if m.listErr != nil {
		return nil, nil, m.listErr
	}
	if m.fw == nil {
		return nil, nil, nil
	}
	return []godo.Firewall{*m.fw}, nil, nil
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

func TestFirewallDriver_Create_Error(t *testing.T) {
	mock := &mockFirewallClient{err: fmt.Errorf("api failure")}
	d := drivers.NewFirewallDriverWithClient(mock)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-fw",
		Config: map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestFirewallDriver_Read_Success(t *testing.T) {
	mock := &mockFirewallClient{fw: testFirewall()}
	d := drivers.NewFirewallDriverWithClient(mock)

	out, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name: "my-fw", ProviderID: "fw-123",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.ProviderID != "fw-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "fw-123")
	}
}

func TestFirewallDriver_Update_Success(t *testing.T) {
	mock := &mockFirewallClient{fw: testFirewall()}
	d := drivers.NewFirewallDriverWithClient(mock)

	out, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-fw", ProviderID: "fw-123",
	}, interfaces.ResourceSpec{
		Name:   "my-fw",
		Config: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.ProviderID != "fw-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "fw-123")
	}
}

func TestFirewallDriver_Update_Error(t *testing.T) {
	mock := &mockFirewallClient{err: fmt.Errorf("update failed")}
	d := drivers.NewFirewallDriverWithClient(mock)

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-fw", ProviderID: "fw-123",
	}, interfaces.ResourceSpec{
		Name:   "my-fw",
		Config: map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestFirewallDriver_Delete_Success(t *testing.T) {
	mock := &mockFirewallClient{fw: testFirewall()}
	d := drivers.NewFirewallDriverWithClient(mock)

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "my-fw", ProviderID: "fw-123",
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestFirewallDriver_Delete_Error(t *testing.T) {
	mock := &mockFirewallClient{err: fmt.Errorf("delete failed")}
	d := drivers.NewFirewallDriverWithClient(mock)

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "my-fw", ProviderID: "fw-123",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestFirewallDriver_Diff_NilCurrent(t *testing.T) {
	mock := &mockFirewallClient{}
	d := drivers.NewFirewallDriverWithClient(mock)

	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{Name: "my-fw"}, nil)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=true when current is nil")
	}
}

func TestFirewallDriver_Diff_NoChanges(t *testing.T) {
	mock := &mockFirewallClient{}
	d := drivers.NewFirewallDriverWithClient(mock)

	current := &interfaces.ResourceOutput{ProviderID: "fw-123"}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{Name: "my-fw"}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=false when current exists")
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

func TestFirewallDriver_HealthCheck_Unhealthy(t *testing.T) {
	fw := &godo.Firewall{
		ID:     "fw-123",
		Name:   "my-fw",
		Status: "waiting",
	}
	mock := &mockFirewallClient{fw: fw}
	d := drivers.NewFirewallDriverWithClient(mock)

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name: "my-fw", ProviderID: "fw-123",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if result.Healthy {
		t.Errorf("expected unhealthy for firewall with status 'waiting'")
	}
}

func TestFirewallDriver_SupportsUpsert(t *testing.T) {
	d := drivers.NewFirewallDriverWithClient(&mockFirewallClient{})
	if !d.SupportsUpsert() {
		t.Error("FirewallDriver.SupportsUpsert() should return true")
	}
}

func TestFirewallDriver_Read_NameBased(t *testing.T) {
	mock := &mockFirewallClient{fw: testFirewall()}
	d := drivers.NewFirewallDriverWithClient(mock)

	// Read with empty ProviderID triggers name-based lookup.
	out, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name: "my-fw",
	})
	if err != nil {
		t.Fatalf("Read by name: %v", err)
	}
	if out.ProviderID != "fw-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "fw-123")
	}
	if out.Name != "my-fw" {
		t.Errorf("Name = %q, want %q", out.Name, "my-fw")
	}
}

func TestFirewallDriver_Read_NameBased_NotFound(t *testing.T) {
	mock := &mockFirewallClient{fw: nil}
	d := drivers.NewFirewallDriverWithClient(mock)

	_, err := d.Read(context.Background(), interfaces.ResourceRef{Name: "missing-fw"})
	if err == nil {
		t.Fatal("expected ErrResourceNotFound for unknown name, got nil")
	}
}

// TestFirewallDriver_Read_NilClientReturn_NoPanic is a regression test for the
// nil-pointer dereference in fwOutput that would occur if the godo client
// returns (nil, nil, nil) for a Get call. The nil guard added to Read ensures
// a descriptive error is returned instead of a panic.
func TestFirewallDriver_Read_NilClientReturn_NoPanic(t *testing.T) {
	mock := &mockFirewallClient{fw: nil, err: nil} // client returns nil, nil, nil
	d := drivers.NewFirewallDriverWithClient(mock)

	_, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name: "my-fw", ProviderID: "fw-123",
	})
	if err == nil {
		t.Fatal("expected error for nil firewall returned by client, got nil")
	}
}
