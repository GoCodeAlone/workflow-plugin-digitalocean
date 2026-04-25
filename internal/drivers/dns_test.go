package drivers_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type mockDomainsClient struct {
	domain          *godo.Domain
	expectedDomain  string
	records         []godo.DomainRecord
	recordPages     [][]godo.DomainRecord
	recordListCalls []recordListCall
	createdRecords  []createdRecord
	editedRecords   []editedRecord
	err             error
}

type recordListCall struct {
	domain  string
	page    int
	perPage int
}

type createdRecord struct {
	domain string
	req    godo.DomainRecordEditRequest
}

type editedRecord struct {
	domain string
	id     int
	req    godo.DomainRecordEditRequest
}

func (m *mockDomainsClient) Create(_ context.Context, req *godo.DomainCreateRequest) (*godo.Domain, *godo.Response, error) {
	if err := m.checkDomain(req.Name); err != nil {
		return nil, nil, err
	}
	return m.domain, nil, m.err
}
func (m *mockDomainsClient) Get(_ context.Context, domain string) (*godo.Domain, *godo.Response, error) {
	if err := m.checkDomain(domain); err != nil {
		return nil, nil, err
	}
	return m.domain, nil, m.err
}
func (m *mockDomainsClient) Delete(_ context.Context, domain string) (*godo.Response, error) {
	if err := m.checkDomain(domain); err != nil {
		return nil, err
	}
	return nil, m.err
}
func (m *mockDomainsClient) CreateRecord(_ context.Context, domain string, req *godo.DomainRecordEditRequest) (*godo.DomainRecord, *godo.Response, error) {
	if err := m.checkDomain(domain); err != nil {
		return nil, nil, err
	}
	id := len(m.allRecords()) + 1
	record := domainRecordFromEditRequest(id, req)
	m.createdRecords = append(m.createdRecords, createdRecord{domain: domain, req: *req})
	m.records = append(m.records, record)
	return &record, nil, m.err
}
func (m *mockDomainsClient) EditRecord(_ context.Context, domain string, id int, req *godo.DomainRecordEditRequest) (*godo.DomainRecord, *godo.Response, error) {
	if err := m.checkDomain(domain); err != nil {
		return nil, nil, err
	}
	record := domainRecordFromEditRequest(id, req)
	m.editedRecords = append(m.editedRecords, editedRecord{domain: domain, id: id, req: *req})
	m.replaceRecord(record)
	return &record, nil, m.err
}
func (m *mockDomainsClient) DeleteRecord(_ context.Context, _ string, _ int) (*godo.Response, error) {
	return nil, m.err
}
func (m *mockDomainsClient) Records(_ context.Context, domain string, opts *godo.ListOptions) ([]godo.DomainRecord, *godo.Response, error) {
	if err := m.checkDomain(domain); err != nil {
		return nil, nil, err
	}
	page := 1
	perPage := 0
	if opts != nil {
		if opts.Page > 0 {
			page = opts.Page
		}
		perPage = opts.PerPage
	}
	m.recordListCalls = append(m.recordListCalls, recordListCall{domain: domain, page: page, perPage: perPage})
	if m.err != nil {
		return nil, nil, m.err
	}
	if len(m.recordPages) > 0 {
		if page < 1 || page > len(m.recordPages) {
			return nil, &godo.Response{Links: &godo.Links{Pages: &godo.Pages{}}}, nil
		}
		return append([]godo.DomainRecord(nil), m.recordPages[page-1]...), recordPageResponse(domain, page, len(m.recordPages)), nil
	}
	return append([]godo.DomainRecord(nil), m.records...), recordPageResponse(domain, page, 1), nil
}

func (m *mockDomainsClient) checkDomain(domain string) error {
	if m.expectedDomain != "" && domain != m.expectedDomain {
		return fmt.Errorf("domain = %q, want %q", domain, m.expectedDomain)
	}
	return nil
}

func (m *mockDomainsClient) allRecords() []godo.DomainRecord {
	if len(m.recordPages) == 0 {
		return m.records
	}
	var records []godo.DomainRecord
	for _, page := range m.recordPages {
		records = append(records, page...)
	}
	return records
}

func (m *mockDomainsClient) replaceRecord(record godo.DomainRecord) {
	if len(m.recordPages) == 0 {
		for i := range m.records {
			if m.records[i].ID == record.ID {
				m.records[i] = record
				return
			}
		}
		m.records = append(m.records, record)
		return
	}
	for pageIndex := range m.recordPages {
		for recordIndex := range m.recordPages[pageIndex] {
			if m.recordPages[pageIndex][recordIndex].ID == record.ID {
				m.recordPages[pageIndex][recordIndex] = record
				return
			}
		}
	}
	m.recordPages[len(m.recordPages)-1] = append(m.recordPages[len(m.recordPages)-1], record)
}

func domainRecordFromEditRequest(id int, req *godo.DomainRecordEditRequest) godo.DomainRecord {
	return godo.DomainRecord{
		ID:       id,
		Type:     req.Type,
		Name:     req.Name,
		Data:     req.Data,
		TTL:      req.TTL,
		Priority: req.Priority,
		Port:     req.Port,
		Weight:   req.Weight,
		Flags:    req.Flags,
		Tag:      req.Tag,
	}
}

func recordPageResponse(domain string, page, pageCount int) *godo.Response {
	pages := &godo.Pages{}
	if page < pageCount {
		pages.Next = fmt.Sprintf("https://api.digitalocean.com/v2/domains/%s/records?page=%d", domain, page+1)
	}
	return &godo.Response{Links: &godo.Links{Pages: pages}}
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
		domain:         testDomain(),
		expectedDomain: "example.com",
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

func TestDNSDriver_Read_IncludesRecordsFromAllPages(t *testing.T) {
	mock := &mockDomainsClient{
		domain:         testDomain(),
		expectedDomain: "example.com",
		recordPages: [][]godo.DomainRecord{
			{{ID: 10, Type: "A", Name: "@", Data: "1.2.3.4", TTL: 300}},
			{{ID: 11, Type: "TXT", Name: "@", Data: "page-two", TTL: 300}},
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
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2", len(records))
	}
	if got := records[1]["data"]; got != "page-two" {
		t.Errorf("second record data = %v, want page-two", got)
	}
	if len(mock.recordListCalls) != 2 {
		t.Fatalf("record list calls = %d, want 2", len(mock.recordListCalls))
	}
	for i, call := range mock.recordListCalls {
		wantPage := i + 1
		if call.domain != "example.com" || call.page != wantPage {
			t.Errorf("record list call %d = domain %q page %d, want example.com page %d", i, call.domain, call.page, wantPage)
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
	if got := mock.createdRecords[0].req.Data; got != "google-site-verification=new" {
		t.Errorf("created record data = %q, want google-site-verification=new", got)
	}
}

func TestDNSDriver_Update_DoesNotDuplicateMatchingRecordOnSecondPage(t *testing.T) {
	mock := &mockDomainsClient{
		domain:         testDomain(),
		expectedDomain: "example.com",
		recordPages: [][]godo.DomainRecord{
			{{ID: 10, Type: "A", Name: "@", Data: "1.2.3.4", TTL: 300}},
			{{ID: 11, Type: "TXT", Name: "@", Data: "page-two", TTL: 300}},
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
				map[string]any{"type": "TXT", "name": "@", "data": "page-two", "ttl": 300},
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
	if len(mock.recordListCalls) < 2 {
		t.Fatalf("record list calls = %d, want at least 2", len(mock.recordListCalls))
	}
	if got := mock.recordListCalls[1].page; got != 2 {
		t.Errorf("second record list page = %d, want 2", got)
	}
}

func TestDNSDriver_Update_ReturnsOutputWithCreatedRecord(t *testing.T) {
	mock := &mockDomainsClient{
		domain:         testDomain(),
		expectedDomain: "example.com",
	}
	d := drivers.NewDNSDriverWithClient(mock)

	out, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "example-dns", ProviderID: "example.com",
	}, interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "A", "name": "@", "data": "1.2.3.4", "ttl": 300},
			},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	records, ok := out.Outputs["records"].([]map[string]any)
	if !ok {
		t.Fatalf("Outputs[records] = %T, want []map[string]any", out.Outputs["records"])
	}
	if len(records) != 1 {
		t.Fatalf("len(records) = %d, want 1", len(records))
	}
	if got := records[0]["data"]; got != "1.2.3.4" {
		t.Errorf("created output data = %v, want 1.2.3.4", got)
	}
	if len(mock.createdRecords) != 1 {
		t.Fatalf("created records = %d, want 1", len(mock.createdRecords))
	}
	if got := mock.createdRecords[0].domain; got != "example.com" {
		t.Errorf("created record domain = %q, want example.com", got)
	}
}

func TestDNSDriver_Update_RejectsConflictingDuplicateDeclarations(t *testing.T) {
	mock := &mockDomainsClient{
		domain:         testDomain(),
		expectedDomain: "example.com",
	}
	d := drivers.NewDNSDriverWithClient(mock)

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "example-dns", ProviderID: "example.com",
	}, interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "TXT", "name": "@", "data": "same", "ttl": 300},
				map[string]any{"type": "TXT", "name": "@", "data": "same", "ttl": 600},
			},
		},
	})
	if err == nil {
		t.Fatal("expected conflicting duplicate error, got nil")
	}
	if !strings.Contains(err.Error(), "conflicting duplicate DNS record") {
		t.Fatalf("error = %q, want conflicting duplicate DNS record", err)
	}
	if len(mock.createdRecords) != 0 {
		t.Fatalf("created records = %d, want 0", len(mock.createdRecords))
	}
}

func TestDNSDriver_Update_RejectsMalformedRecordConfig(t *testing.T) {
	tests := []struct {
		name    string
		record  any
		wantErr string
	}{
		{
			name:    "non map entry",
			record:  "not-a-record",
			wantErr: "records[0] must be an object",
		},
		{
			name:    "invalid type field type",
			record:  map[string]any{"type": 42, "name": "@", "data": "1.2.3.4"},
			wantErr: `records[0].type must be a string`,
		},
		{
			name:    "unsupported record type",
			record:  map[string]any{"type": "BOGUS", "name": "@", "data": "1.2.3.4"},
			wantErr: `records[0].type "BOGUS" is not supported`,
		},
		{
			name:    "invalid name field type",
			record:  map[string]any{"type": "A", "name": 42, "data": "1.2.3.4"},
			wantErr: `records[0].name must be a string`,
		},
		{
			name:    "missing data",
			record:  map[string]any{"type": "A", "name": "@"},
			wantErr: `records[0].data is required`,
		},
		{
			name:    "invalid ttl type",
			record:  map[string]any{"type": "A", "name": "@", "data": "1.2.3.4", "ttl": "300"},
			wantErr: `records[0].ttl must be an integer`,
		},
		{
			name:    "negative priority",
			record:  map[string]any{"type": "MX", "name": "@", "data": "mail.example.com", "priority": -1},
			wantErr: `records[0].priority must be non-negative`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockDomainsClient{
				domain:         testDomain(),
				expectedDomain: "example.com",
			}
			d := drivers.NewDNSDriverWithClient(mock)

			_, err := d.Update(context.Background(), interfaces.ResourceRef{
				Name: "example-dns", ProviderID: "example.com",
			}, interfaces.ResourceSpec{
				Name: "example-dns",
				Config: map[string]any{
					"domain":  "example.com",
					"records": []any{tt.record},
				},
			})
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want substring %q", err, tt.wantErr)
			}
			if len(mock.createdRecords) != 0 {
				t.Fatalf("created records = %d, want 0", len(mock.createdRecords))
			}
		})
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
