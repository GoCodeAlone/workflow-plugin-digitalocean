package drivers_test

import (
	"context"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type mockDropletClient struct {
	droplet *godo.Droplet
	err     error
}

func (m *mockDropletClient) Create(_ context.Context, _ *godo.DropletCreateRequest) (*godo.Droplet, *godo.Response, error) {
	return m.droplet, nil, m.err
}
func (m *mockDropletClient) Get(_ context.Context, _ int) (*godo.Droplet, *godo.Response, error) {
	return m.droplet, nil, m.err
}
func (m *mockDropletClient) Delete(_ context.Context, _ int) (*godo.Response, error) {
	return nil, m.err
}

func testDroplet() *godo.Droplet {
	return &godo.Droplet{
		ID:     42,
		Name:   "my-droplet",
		Status: "active",
		Size:   &godo.Size{Slug: "s-1vcpu-2gb"},
		Region: &godo.Region{Slug: "nyc3"},
		Networks: &godo.Networks{
			V4: []godo.NetworkV4{
				{IPAddress: "1.2.3.4", Type: "public"},
			},
		},
	}
}

func TestDropletDriver_Create(t *testing.T) {
	mock := &mockDropletClient{droplet: testDroplet()}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-droplet",
		Config: map[string]any{
			"size":  "s-1vcpu-2gb",
			"image": "ubuntu-24-04-x64",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID != "42" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "42")
	}
	if out.Status != "active" {
		t.Errorf("Status = %q, want %q", out.Status, "active")
	}
}

func TestDropletDriver_HealthCheck(t *testing.T) {
	mock := &mockDropletClient{droplet: testDroplet()}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name:       "my-droplet",
		ProviderID: "42",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if !result.Healthy {
		t.Errorf("expected healthy droplet")
	}
}
