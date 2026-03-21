package drivers_test

import (
	"context"
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
