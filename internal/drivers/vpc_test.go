package drivers_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type mockVPCClient struct {
	vpc     *godo.VPC
	err     error
	listErr error
}

func (m *mockVPCClient) Create(_ context.Context, _ *godo.VPCCreateRequest) (*godo.VPC, *godo.Response, error) {
	return m.vpc, nil, m.err
}
func (m *mockVPCClient) Get(_ context.Context, _ string) (*godo.VPC, *godo.Response, error) {
	return m.vpc, nil, m.err
}
func (m *mockVPCClient) List(_ context.Context, _ *godo.ListOptions) ([]*godo.VPC, *godo.Response, error) {
	if m.listErr != nil {
		return nil, nil, m.listErr
	}
	if m.vpc == nil {
		return nil, nil, nil
	}
	return []*godo.VPC{m.vpc}, nil, nil
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

func TestVPCDriver_Create_Error(t *testing.T) {
	mock := &mockVPCClient{err: fmt.Errorf("api failure")}
	d := drivers.NewVPCDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-vpc",
		Config: map[string]any{"ip_range": "10.0.0.0/16"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestVPCDriver_Read_Success(t *testing.T) {
	mock := &mockVPCClient{vpc: testVPC()}
	d := drivers.NewVPCDriverWithClient(mock, "nyc3")

	out, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name: "my-vpc", ProviderID: "vpc-123",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.ProviderID != "vpc-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "vpc-123")
	}
}

func TestVPCDriver_Update_Success(t *testing.T) {
	mock := &mockVPCClient{vpc: testVPC()}
	d := drivers.NewVPCDriverWithClient(mock, "nyc3")

	out, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-vpc", ProviderID: "vpc-123",
	}, interfaces.ResourceSpec{Name: "my-vpc", Config: map[string]any{}})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.ProviderID != "vpc-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "vpc-123")
	}
}

func TestVPCDriver_Update_Error(t *testing.T) {
	mock := &mockVPCClient{err: fmt.Errorf("update failed")}
	d := drivers.NewVPCDriverWithClient(mock, "nyc3")

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-vpc", ProviderID: "vpc-123",
	}, interfaces.ResourceSpec{Name: "my-vpc", Config: map[string]any{}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestVPCDriver_Delete_Success(t *testing.T) {
	mock := &mockVPCClient{vpc: testVPC()}
	d := drivers.NewVPCDriverWithClient(mock, "nyc3")

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "my-vpc", ProviderID: "vpc-123",
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestVPCDriver_Delete_Error(t *testing.T) {
	mock := &mockVPCClient{err: fmt.Errorf("delete failed")}
	d := drivers.NewVPCDriverWithClient(mock, "nyc3")

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "my-vpc", ProviderID: "vpc-123",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestVPCDriver_Diff_NoChanges(t *testing.T) {
	mock := &mockVPCClient{}
	d := drivers.NewVPCDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{"ip_range": "10.0.0.0/16"},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{"ip_range": "10.0.0.0/16"},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=false when ip_range unchanged")
	}
}

func TestVPCDriver_HealthCheck_Healthy(t *testing.T) {
	mock := &mockVPCClient{vpc: testVPC()}
	d := drivers.NewVPCDriverWithClient(mock, "nyc3")

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name: "my-vpc", ProviderID: "vpc-123",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if !result.Healthy {
		t.Errorf("expected healthy vpc")
	}
}

func TestVPCDriver_HealthCheck_Unhealthy(t *testing.T) {
	mock := &mockVPCClient{err: fmt.Errorf("not found")}
	d := drivers.NewVPCDriverWithClient(mock, "nyc3")

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name: "my-vpc", ProviderID: "vpc-123",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if result.Healthy {
		t.Errorf("expected unhealthy when get fails")
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

func TestVPCDriver_SupportsUpsert(t *testing.T) {
	d := drivers.NewVPCDriverWithClient(&mockVPCClient{}, "nyc3")
	if !d.SupportsUpsert() {
		t.Error("VPCDriver.SupportsUpsert() should return true")
	}
}

func TestVPCDriver_Read_NameBased(t *testing.T) {
	mock := &mockVPCClient{vpc: testVPC()}
	d := drivers.NewVPCDriverWithClient(mock, "nyc3")

	// Read with empty ProviderID triggers name-based lookup.
	out, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name: "my-vpc",
	})
	if err != nil {
		t.Fatalf("Read by name: %v", err)
	}
	if out.ProviderID != "vpc-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "vpc-123")
	}
	if out.Name != "my-vpc" {
		t.Errorf("Name = %q, want %q", out.Name, "my-vpc")
	}
}

func TestVPCDriver_Read_NameBased_NotFound(t *testing.T) {
	mock := &mockVPCClient{vpc: nil}
	d := drivers.NewVPCDriverWithClient(mock, "nyc3")

	_, err := d.Read(context.Background(), interfaces.ResourceRef{Name: "missing-vpc"})
	if !errors.Is(err, drivers.ErrResourceNotFound) {
		t.Fatalf("expected ErrResourceNotFound, got: %v", err)
	}
}

func TestVPCDriver_Create_EmptyIDFromAPI(t *testing.T) {
	// API returns success but VPC has empty ID — guard must reject it.
	mock := &mockVPCClient{vpc: &godo.VPC{Name: "my-vpc"}}
	d := drivers.NewVPCDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-vpc",
		Config: map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error for empty ProviderID, got nil")
	}
}

func TestVPCDriver_Create_ProviderIDIsAPIAssigned(t *testing.T) {
	// ProviderID must be the API-assigned UUID, not the resource name.
	mock := &mockVPCClient{vpc: testVPC()}
	d := drivers.NewVPCDriverWithClient(mock, "nyc3")

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-vpc",
		Config: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID == "my-vpc" {
		t.Errorf("ProviderID must not equal spec.Name %q; got %q", "my-vpc", out.ProviderID)
	}
	if out.ProviderID == "" {
		t.Errorf("ProviderID must not be empty")
	}
}
