package drivers_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type mockDomainsClient struct {
	domain  *godo.Domain
	records []godo.DomainRecord
	err     error
}

func (m *mockDomainsClient) Create(_ context.Context, _ *godo.DomainCreateRequest) (*godo.Domain, *godo.Response, error) {
	return m.domain, nil, m.err
}
func (m *mockDomainsClient) Get(_ context.Context, _ string) (*godo.Domain, *godo.Response, error) {
	return m.domain, nil, m.err
}
func (m *mockDomainsClient) Delete(_ context.Context, _ string) (*godo.Response, error) {
	return nil, m.err
}
func (m *mockDomainsClient) CreateRecord(_ context.Context, _ string, _ *godo.DomainRecordEditRequest) (*godo.DomainRecord, *godo.Response, error) {
	return &godo.DomainRecord{ID: 1}, nil, m.err
}
func (m *mockDomainsClient) EditRecord(_ context.Context, _ string, _ int, _ *godo.DomainRecordEditRequest) (*godo.DomainRecord, *godo.Response, error) {
	return &godo.DomainRecord{ID: 1}, nil, m.err
}
func (m *mockDomainsClient) DeleteRecord(_ context.Context, _ string, _ int) (*godo.Response, error) {
	return nil, m.err
}
func (m *mockDomainsClient) Records(_ context.Context, _ string, _ *godo.ListOptions) ([]godo.DomainRecord, *godo.Response, error) {
	return m.records, nil, m.err
}

func testDomain() *godo.Domain {
	return &godo.Domain{
		Name: "example.com",
		TTL:  1800,
	}
}

func TestDNSDriver_Create(t *testing.T) {
	mock := &mockDomainsClient{domain: testDomain()}
	d := drivers.NewDNSDriverWithClient(mock)

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "A", "name": "@", "data": "1.2.3.4", "ttl": 300},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID != "example.com" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "example.com")
	}
}

func TestDNSDriver_Create_Error(t *testing.T) {
	// When Get fails AND Create also fails, should return error.
	mock := &mockDomainsClient{err: fmt.Errorf("api failure")}
	d := drivers.NewDNSDriverWithClient(mock)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "example-dns",
		Config: map[string]any{"domain": "example.com"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDNSDriver_Create_IdempotentExistingDomain(t *testing.T) {
	// When Get succeeds (domain exists), Create should not error.
	mock := &mockDomainsClient{domain: testDomain()}
	d := drivers.NewDNSDriverWithClient(mock)

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "example-dns",
		Config: map[string]any{"domain": "example.com"},
	})
	if err != nil {
		t.Fatalf("Create (idempotent): %v", err)
	}
	if out.ProviderID != "example.com" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "example.com")
	}
}

func TestDNSDriver_Read_Success(t *testing.T) {
	mock := &mockDomainsClient{domain: testDomain()}
	d := drivers.NewDNSDriverWithClient(mock)

	out, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name: "example-dns", ProviderID: "example.com",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.ProviderID != "example.com" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "example.com")
	}
}

func TestDNSDriver_Update_Success(t *testing.T) {
	mock := &mockDomainsClient{domain: testDomain()}
	d := drivers.NewDNSDriverWithClient(mock)

	out, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "example-dns", ProviderID: "example.com",
	}, interfaces.ResourceSpec{
		Name:   "example-dns",
		Config: map[string]any{"domain": "example.com"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.ProviderID != "example.com" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "example.com")
	}
}

func TestDNSDriver_Delete_Success(t *testing.T) {
	mock := &mockDomainsClient{domain: testDomain()}
	d := drivers.NewDNSDriverWithClient(mock)

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "example-dns", ProviderID: "example.com",
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestDNSDriver_Delete_Error(t *testing.T) {
	mock := &mockDomainsClient{err: fmt.Errorf("delete failed")}
	d := drivers.NewDNSDriverWithClient(mock)

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "example-dns", ProviderID: "example.com",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDNSDriver_Diff_NilCurrent(t *testing.T) {
	mock := &mockDomainsClient{}
	d := drivers.NewDNSDriverWithClient(mock)

	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{Name: "example-dns"}, nil)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=true when current is nil")
	}
}

func TestDNSDriver_Diff_NoChanges(t *testing.T) {
	mock := &mockDomainsClient{}
	d := drivers.NewDNSDriverWithClient(mock)

	current := &interfaces.ResourceOutput{ProviderID: "example.com"}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{Name: "example-dns"}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=false when current exists")
	}
}

func TestDNSDriver_HealthCheck_Healthy(t *testing.T) {
	mock := &mockDomainsClient{domain: testDomain()}
	d := drivers.NewDNSDriverWithClient(mock)

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name: "example-dns", ProviderID: "example.com",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if !result.Healthy {
		t.Errorf("expected healthy dns zone")
	}
}

func TestDNSDriver_HealthCheck_Unhealthy(t *testing.T) {
	mock := &mockDomainsClient{err: fmt.Errorf("not found")}
	d := drivers.NewDNSDriverWithClient(mock)

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name: "example-dns", ProviderID: "example.com",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if result.Healthy {
		t.Errorf("expected unhealthy when get fails")
	}
}
