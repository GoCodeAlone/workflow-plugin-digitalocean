package drivers_test

import (
	"context"
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
