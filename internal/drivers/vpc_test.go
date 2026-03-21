package drivers_test

import (
	"context"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type mockVPCClient struct {
	vpc *godo.VPC
	err error
}

func (m *mockVPCClient) Create(_ context.Context, _ *godo.VPCCreateRequest) (*godo.VPC, *godo.Response, error) {
	return m.vpc, nil, m.err
}
func (m *mockVPCClient) Get(_ context.Context, _ string) (*godo.VPC, *godo.Response, error) {
	return m.vpc, nil, m.err
}
func (m *mockVPCClient) Update(_ context.Context, _ string, _ *godo.VPCUpdateRequest) (*godo.VPC, *godo.Response, error) {
	return m.vpc, nil, m.err
}
func (m *mockVPCClient) Delete(_ context.Context, _ string) (*godo.Response, error) {
	return nil, m.err
}

func testVPC() *godo.VPC {
	return &godo.VPC{
		ID:         "vpc-123",
		Name:       "my-vpc",
		RegionSlug: "nyc3",
		IPRange:    "10.0.0.0/16",
		URN:        "do:vpc:vpc-123",
	}
}

func TestVPCDriver_Create(t *testing.T) {
	mock := &mockVPCClient{vpc: testVPC()}
	d := drivers.NewVPCDriverWithClient(mock, "nyc3")

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-vpc",
		Config: map[string]any{
			"ip_range": "10.0.0.0/16",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID != "vpc-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "vpc-123")
	}
}

func TestVPCDriver_Diff_IPRangeChange(t *testing.T) {
	mock := &mockVPCClient{vpc: testVPC()}
	d := drivers.NewVPCDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{"ip_range": "10.0.0.0/16"},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{"ip_range": "172.16.0.0/16"},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsReplace {
		t.Error("expected NeedsReplace for ip_range change")
	}
}
