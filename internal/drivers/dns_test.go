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
	domain         *godo.Domain
	records        []godo.DomainRecord
	createdRecords []godo.DomainRecordEditRequest
	editedRecords  []editedRecord
	err            error
}

type editedRecord struct {
	id  int
	req godo.DomainRecordEditRequest
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
func (m *mockDomainsClient) CreateRecord(_ context.Context, _ string, req *godo.DomainRecordEditRequest) (*godo.DomainRecord, *godo.Response, error) {
	m.createdRecords = append(m.createdRecords, *req)
	return &godo.DomainRecord{ID: 1}, nil, m.err
}
func (m *mockDomainsClient) EditRecord(_ context.Context, _ string, id int, req *godo.DomainRecordEditRequest) (*godo.DomainRecord, *godo.Response, error) {
	m.editedRecords = append(m.editedRecords, editedRecord{id: id, req: *req})
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

func TestDNSDriver_Read_IncludesExistingDomainRecords(t *testing.T) {
	mock := &mockDomainsClient{
		domain: testDomain(),
		records: []godo.DomainRecord{
			{
				ID:       10,
				Type:     "SRV",
				Name:     "_sip._tcp",
				Data:     "sip.example.com",
				TTL:      600,
				Priority: 20,
				Port:     5060,
				Weight:   30,
				Flags:    1,
				Tag:      "issue",
			},
		},
	}
	d := drivers.NewDNSDriverWithClient(mock)

	out, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name: "example-dns", ProviderID: "example.com",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	records, ok := out.Outputs["records"].([]map[string]any)
	if !ok {
		t.Fatalf("Outputs[records] = %T, want []map[string]any", out.Outputs["records"])
	}
	if len(records) != 1 {
		t.Fatalf("len(records) = %d, want 1", len(records))
	}
	want := map[string]any{
		"type":     "SRV",
		"name":     "_sip._tcp",
		"data":     "sip.example.com",
		"ttl":      600,
		"priority": 20,
		"port":     5060,
		"weight":   30,
		"flags":    1,
		"tag":      "issue",
	}
	for key, wantValue := range want {
		if got := records[0][key]; got != wantValue {
			t.Errorf("records[0][%q] = %#v, want %#v", key, got, wantValue)
		}
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

func TestDNSDriver_Update_SkipsIdenticalRecord(t *testing.T) {
	mock := &mockDomainsClient{
		domain: testDomain(),
		records: []godo.DomainRecord{
			{
				ID:       10,
				Type:     "SRV",
				Name:     "_sip._tcp",
				Data:     "sip.example.com",
				TTL:      600,
				Priority: 20,
				Port:     5060,
				Weight:   30,
				Flags:    1,
				Tag:      "issue",
			},
		},
	}
	d := drivers.NewDNSDriverWithClient(mock)

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "example-dns", ProviderID: "example.com",
	}, interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{
					"type":     "SRV",
					"name":     "_sip._tcp",
					"data":     "sip.example.com",
					"ttl":      600,
					"priority": 20,
					"port":     5060,
					"weight":   30,
					"flags":    1,
					"tag":      "issue",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(mock.createdRecords) != 0 {
		t.Fatalf("created records = %d, want 0", len(mock.createdRecords))
	}
	if len(mock.editedRecords) != 0 {
		t.Fatalf("edited records = %d, want 0", len(mock.editedRecords))
	}
}

func TestDNSDriver_Update_CreatesDistinctRecordWithSameTypeAndName(t *testing.T) {
	mock := &mockDomainsClient{
		domain: testDomain(),
		records: []godo.DomainRecord{
			{ID: 10, Type: "TXT", Name: "@", Data: "google-site-verification=old", TTL: 300},
		},
	}
	d := drivers.NewDNSDriverWithClient(mock)

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "example-dns", ProviderID: "example.com",
	}, interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "TXT", "name": "@", "data": "google-site-verification=new", "ttl": 300},
			},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(mock.editedRecords) != 0 {
		t.Fatalf("edited records = %d, want 0", len(mock.editedRecords))
	}
	if len(mock.createdRecords) != 1 {
		t.Fatalf("created records = %d, want 1", len(mock.createdRecords))
	}
	if got := mock.createdRecords[0].Data; got != "google-site-verification=new" {
		t.Errorf("created record data = %q, want google-site-verification=new", got)
	}
}

func TestDNSDriver_Update_UpdatesConservativelyMatchedRecordWhenFieldsDiffer(t *testing.T) {
	mock := &mockDomainsClient{
		domain: testDomain(),
		records: []godo.DomainRecord{
			{
				ID:       10,
				Type:     "SRV",
				Name:     "_sip._tcp",
				Data:     "sip.example.com",
				TTL:      300,
				Priority: 20,
				Port:     5060,
				Weight:   10,
				Flags:    1,
				Tag:      "issue",
			},
		},
	}
	d := drivers.NewDNSDriverWithClient(mock)

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "example-dns", ProviderID: "example.com",
	}, interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{
					"type":     "SRV",
					"name":     "_sip._tcp",
					"data":     "sip.example.com",
					"ttl":      600,
					"priority": 20,
					"port":     5060,
					"weight":   30,
					"flags":    1,
					"tag":      "issue",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(mock.createdRecords) != 0 {
		t.Fatalf("created records = %d, want 0", len(mock.createdRecords))
	}
	if len(mock.editedRecords) != 1 {
		t.Fatalf("edited records = %d, want 1", len(mock.editedRecords))
	}
	if got := mock.editedRecords[0].id; got != 10 {
		t.Errorf("edited ID = %d, want 10", got)
	}
	req := mock.editedRecords[0].req
	if req.TTL != 600 || req.Weight != 30 {
		t.Errorf("edited request TTL/Weight = %d/%d, want 600/30", req.TTL, req.Weight)
	}
	if req.Priority != 20 || req.Port != 5060 || req.Flags != 1 || req.Tag != "issue" {
		t.Errorf("edited request = %+v, want supported fields preserved", req)
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
