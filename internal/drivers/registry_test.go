package drivers_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type mockRegistryClient struct {
	reg *godo.Registry
	err error
}

func (m *mockRegistryClient) Create(_ context.Context, _ *godo.RegistryCreateRequest) (*godo.Registry, *godo.Response, error) {
	return m.reg, nil, m.err
}
func (m *mockRegistryClient) Get(_ context.Context) (*godo.Registry, *godo.Response, error) {
	return m.reg, nil, m.err
}
func (m *mockRegistryClient) Delete(_ context.Context) (*godo.Response, error) {
	return nil, m.err
}

func testRegistry() *godo.Registry {
	return &godo.Registry{
		Name:   "my-registry",
		Region: "nyc3",
	}
}

func TestRegistryDriver_Create(t *testing.T) {
	mock := &mockRegistryClient{reg: testRegistry()}
	d := drivers.NewRegistryDriverWithClient(mock)

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-registry",
		Config: map[string]any{"tier": "starter", "region": "nyc3"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID != "my-registry" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "my-registry")
	}
	if ep, _ := out.Outputs["endpoint"].(string); ep == "" {
		t.Error("expected endpoint in outputs")
	}
}

func TestRegistryDriver_Create_Error(t *testing.T) {
	mock := &mockRegistryClient{err: fmt.Errorf("api failure")}
	d := drivers.NewRegistryDriverWithClient(mock)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-registry",
		Config: map[string]any{"tier": "starter"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestRegistryDriver_Read_Success(t *testing.T) {
	mock := &mockRegistryClient{reg: testRegistry()}
	d := drivers.NewRegistryDriverWithClient(mock)

	out, err := d.Read(context.Background(), interfaces.ResourceRef{Name: "my-registry"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.ProviderID != "my-registry" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "my-registry")
	}
}

func TestRegistryDriver_Update_Success(t *testing.T) {
	mock := &mockRegistryClient{reg: testRegistry()}
	d := drivers.NewRegistryDriverWithClient(mock)

	out, err := d.Update(context.Background(), interfaces.ResourceRef{Name: "my-registry"}, interfaces.ResourceSpec{
		Name:   "my-registry",
		Config: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.ProviderID != "my-registry" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "my-registry")
	}
}

func TestRegistryDriver_Delete_Success(t *testing.T) {
	mock := &mockRegistryClient{reg: testRegistry()}
	d := drivers.NewRegistryDriverWithClient(mock)

	err := d.Delete(context.Background(), interfaces.ResourceRef{Name: "my-registry"})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestRegistryDriver_Delete_Error(t *testing.T) {
	mock := &mockRegistryClient{err: fmt.Errorf("delete failed")}
	d := drivers.NewRegistryDriverWithClient(mock)

	err := d.Delete(context.Background(), interfaces.ResourceRef{Name: "my-registry"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestRegistryDriver_Diff_NilCurrent(t *testing.T) {
	mock := &mockRegistryClient{}
	d := drivers.NewRegistryDriverWithClient(mock)

	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{Name: "my-registry"}, nil)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=true when current is nil")
	}
}

func TestRegistryDriver_Diff_NoChanges(t *testing.T) {
	mock := &mockRegistryClient{}
	d := drivers.NewRegistryDriverWithClient(mock)

	current := &interfaces.ResourceOutput{ProviderID: "my-registry"}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{Name: "my-registry"}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=false when current exists")
	}
}

func TestRegistryDriver_HealthCheck_Healthy(t *testing.T) {
	mock := &mockRegistryClient{reg: testRegistry()}
	d := drivers.NewRegistryDriverWithClient(mock)

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "my-registry"})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if !result.Healthy {
		t.Errorf("expected healthy registry")
	}
}

func TestRegistryDriver_HealthCheck_Unhealthy(t *testing.T) {
	mock := &mockRegistryClient{err: fmt.Errorf("not found")}
	d := drivers.NewRegistryDriverWithClient(mock)

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "my-registry"})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if result.Healthy {
		t.Errorf("expected unhealthy when get fails")
	}
}
