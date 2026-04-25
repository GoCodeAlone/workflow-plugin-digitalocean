package drivers_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type mockDomainsClient struct {
	domain                      *godo.Domain
	expectedDomain              string
	getErr                      error
	getErrs                     []error
	createErr                   error
	afterCreateErr              func()
	createdDomains              []string
	records                     []godo.DomainRecord
	recordPages                 [][]godo.DomainRecord
	recordListErrs              []error
	afterRecords                func()
	recordListCalls             []recordListCall
	events                      []string
	attemptedRecords            []createdRecord
	attemptedEdits              []editedRecord
	createdRecords              []createdRecord
	editedRecords               []editedRecord
	createRecordErr             error
	createRecordErrs            []error
	afterCreateRecordErr        func()
	recordsAfterCreateRecordErr []godo.DomainRecord
	recordsAfterRecordListCall  int
	editRecordErr               error
	err                         error
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

func (m *mockDomainsClient) Create(ctx context.Context, req *godo.DomainCreateRequest) (*godo.Domain, *godo.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := m.checkDomain(req.Name); err != nil {
		return nil, nil, err
	}
	m.createdDomains = append(m.createdDomains, req.Name)
	if m.createErr != nil {
		if m.afterCreateErr != nil {
			m.afterCreateErr()
		}
		return nil, nil, m.createErr
	}
	return m.domain, nil, m.err
}
func (m *mockDomainsClient) Get(ctx context.Context, domain string) (*godo.Domain, *godo.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := m.checkDomain(domain); err != nil {
		return nil, nil, err
	}
	if len(m.getErrs) > 0 {
		err := m.getErrs[0]
		m.getErrs = m.getErrs[1:]
		if err != nil {
			return nil, nil, err
		}
	}
	if m.getErr != nil {
		return nil, nil, m.getErr
	}
	return m.domain, nil, m.err
}
func (m *mockDomainsClient) Delete(_ context.Context, domain string) (*godo.Response, error) {
	if err := m.checkDomain(domain); err != nil {
		return nil, err
	}
	return nil, m.err
}
func (m *mockDomainsClient) CreateRecord(ctx context.Context, domain string, req *godo.DomainRecordEditRequest) (*godo.DomainRecord, *godo.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := m.checkDomain(domain); err != nil {
		return nil, nil, err
	}
	m.events = append(m.events, "create_record")
	m.attemptedRecords = append(m.attemptedRecords, createdRecord{domain: domain, req: *req})
	if len(m.createRecordErrs) > 0 {
		err := m.createRecordErrs[0]
		m.createRecordErrs = m.createRecordErrs[1:]
		if err != nil {
			if m.recordsAfterCreateRecordErr != nil && m.recordsAfterRecordListCall == 0 {
				m.records = append([]godo.DomainRecord(nil), m.recordsAfterCreateRecordErr...)
			}
			if m.afterCreateRecordErr != nil {
				m.afterCreateRecordErr()
			}
			return nil, nil, err
		}
	}
	if m.createRecordErr != nil {
		if m.recordsAfterCreateRecordErr != nil && m.recordsAfterRecordListCall == 0 {
			m.records = append([]godo.DomainRecord(nil), m.recordsAfterCreateRecordErr...)
		}
		if m.afterCreateRecordErr != nil {
			m.afterCreateRecordErr()
		}
		return nil, nil, m.createRecordErr
	}
	if m.err != nil {
		return nil, nil, m.err
	}
	id := len(m.allRecords()) + 1
	record := domainRecordFromEditRequest(id, req)
	m.createdRecords = append(m.createdRecords, createdRecord{domain: domain, req: *req})
	m.records = append(m.records, record)
	return &record, nil, m.err
}
func (m *mockDomainsClient) EditRecord(ctx context.Context, domain string, id int, req *godo.DomainRecordEditRequest) (*godo.DomainRecord, *godo.Response, error) {
	m.attemptedEdits = append(m.attemptedEdits, editedRecord{domain: domain, id: id, req: *req})
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := m.checkDomain(domain); err != nil {
		return nil, nil, err
	}
	if m.editRecordErr != nil {
		return nil, nil, m.editRecordErr
	}
	if m.err != nil {
		return nil, nil, m.err
	}
	record := domainRecordFromEditRequest(id, req)
	m.editedRecords = append(m.editedRecords, editedRecord{domain: domain, id: id, req: *req})
	m.replaceRecord(record)
	return &record, nil, m.err
}
func (m *mockDomainsClient) DeleteRecord(_ context.Context, _ string, _ int) (*godo.Response, error) {
	return nil, m.err
}
func (m *mockDomainsClient) Records(ctx context.Context, domain string, opts *godo.ListOptions) ([]godo.DomainRecord, *godo.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
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
	m.events = append(m.events, "records")
	if m.recordsAfterCreateRecordErr != nil && m.recordsAfterRecordListCall > 0 && len(m.recordListCalls) >= m.recordsAfterRecordListCall {
		m.records = append([]godo.DomainRecord(nil), m.recordsAfterCreateRecordErr...)
	}
	if len(m.recordListErrs) > 0 {
		err := m.recordListErrs[0]
		m.recordListErrs = m.recordListErrs[1:]
		if err != nil {
			return nil, nil, err
		}
	}
	if m.err != nil {
		return nil, nil, m.err
	}
	if len(m.recordPages) > 0 {
		if page < 1 || page > len(m.recordPages) {
			return nil, &godo.Response{Links: &godo.Links{Pages: &godo.Pages{}}}, nil
		}
		if m.afterRecords != nil {
			m.afterRecords()
		}
		return append([]godo.DomainRecord(nil), m.recordPages[page-1]...), recordPageResponse(domain, page, len(m.recordPages)), nil
	}
	if m.afterRecords != nil {
		m.afterRecords()
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

func godoStatusErr(code int) error {
	return &godo.ErrorResponse{
		Response: &http.Response{StatusCode: code},
		Message:  http.StatusText(code),
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
	records, ok := out.Outputs["records"].([]map[string]any)
	if !ok {
		t.Fatalf("Outputs[records] = %T, want []map[string]any", out.Outputs["records"])
	}
	if len(records) != 1 {
		t.Fatalf("len(records) = %d, want 1", len(records))
	}
	if got := records[0]["data"]; got != "1.2.3.4" {
		t.Errorf("created output record data = %v, want 1.2.3.4", got)
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

func TestDNSDriver_Create_DoesNotCreateOnTransientGetError(t *testing.T) {
	mock := &mockDomainsClient{
		domain: testDomain(),
		getErr: godoStatusErr(http.StatusInternalServerError),
	}
	d := drivers.NewDNSDriverWithClient(mock)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "example-dns",
		Config: map[string]any{"domain": "example.com"},
	})
	if err == nil {
		t.Fatal("expected transient get error, got nil")
	}
	if !errors.Is(err, interfaces.ErrTransient) {
		t.Fatalf("error = %v, want ErrTransient", err)
	}
	if len(mock.createdDomains) != 0 {
		t.Fatalf("created domains = %v, want none", mock.createdDomains)
	}
}

func TestDNSDriver_Create_ValidatesRecordsBeforeCreatingDomain(t *testing.T) {
	mock := &mockDomainsClient{
		domain: testDomain(),
		getErr: godoStatusErr(http.StatusNotFound),
	}
	d := drivers.NewDNSDriverWithClient(mock)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "A", "name": "@"},
			},
		},
	})
	if err == nil {
		t.Fatal("expected record validation error, got nil")
	}
	if !strings.Contains(err.Error(), "records[0].data is required") {
		t.Fatalf("error = %q, want records[0].data is required", err)
	}
	if len(mock.createdDomains) != 0 {
		t.Fatalf("created domains = %v, want none", mock.createdDomains)
	}
}

func TestDNSDriver_Create_RejectsApexCNAMEBeforeCreatingDomain(t *testing.T) {
	mock := &mockDomainsClient{
		domain: testDomain(),
		getErr: godoStatusErr(http.StatusNotFound),
	}
	d := drivers.NewDNSDriverWithClient(mock)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "CNAME", "name": "@", "data": "target.example.com"},
			},
		},
	})
	if err == nil {
		t.Fatal("expected apex CNAME validation error, got nil")
	}
	if !strings.Contains(err.Error(), "CNAME records cannot be declared at the zone apex") {
		t.Fatalf("error = %q, want apex CNAME validation error", err)
	}
	if len(mock.createdDomains) != 0 {
		t.Fatalf("created domains = %v, want none", mock.createdDomains)
	}
	if len(mock.createdRecords) != 0 {
		t.Fatalf("created records = %d, want 0", len(mock.createdRecords))
	}
}

func TestDNSDriver_Create_RejectsMalformedDomainBeforeAPICalls(t *testing.T) {
	tests := []struct {
		name   string
		domain any
	}{
		{name: "non string", domain: 42},
		{name: "empty string", domain: ""},
		{name: "invalid domain name", domain: "not a domain"},
		{name: "single label", domain: "example"},
		{name: "trailing dot fqdn", domain: "example.com."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockDomainsClient{domain: testDomain()}
			d := drivers.NewDNSDriverWithClient(mock)

			_, err := d.Create(context.Background(), interfaces.ResourceSpec{
				Name:   "example.com",
				Config: map[string]any{"domain": tt.domain},
			})
			if err == nil {
				t.Fatal("expected domain validation error, got nil")
			}
			if !strings.Contains(err.Error(), "dns domain") {
				t.Fatalf("error = %q, want dns domain validation error", err)
			}
			if len(mock.createdDomains) != 0 {
				t.Fatalf("created domains = %v, want none", mock.createdDomains)
			}
			if len(mock.recordListCalls) != 0 {
				t.Fatalf("record list calls = %d, want 0", len(mock.recordListCalls))
			}
		})
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

func TestDNSDriver_Create_RejectsEmptyExistingDomainResponse(t *testing.T) {
	mock := &mockDomainsClient{
		expectedDomain: "example.com",
	}
	d := drivers.NewDNSDriverWithClient(mock)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "example-dns",
		Config: map[string]any{"domain": "example.com"},
	})
	if err == nil {
		t.Fatal("expected empty domain response error, got nil")
	}
	if !strings.Contains(err.Error(), "API returned empty domain") {
		t.Fatalf("error = %q, want empty domain response", err)
	}
}

func TestDNSDriver_Create_RejectsEmptyCreatedDomainResponse(t *testing.T) {
	mock := &mockDomainsClient{
		expectedDomain: "example.com",
		getErr:         godoStatusErr(http.StatusNotFound),
	}
	d := drivers.NewDNSDriverWithClient(mock)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "example-dns",
		Config: map[string]any{"domain": "example.com"},
	})
	if err == nil {
		t.Fatal("expected empty created domain response error, got nil")
	}
	if !strings.Contains(err.Error(), "API returned empty domain") {
		t.Fatalf("error = %q, want empty domain response", err)
	}
}

func TestDNSDriver_Create_AdoptsDomainAfterCreateConflict(t *testing.T) {
	mock := &mockDomainsClient{
		domain:         testDomain(),
		expectedDomain: "example.com",
		getErrs:        []error{godoStatusErr(http.StatusNotFound), godoStatusErr(http.StatusNotFound), nil},
		createErr:      godoStatusErr(http.StatusConflict),
	}
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
		t.Fatalf("ProviderID = %q, want example.com", out.ProviderID)
	}
	if len(mock.createdDomains) != 1 {
		t.Fatalf("created domains = %v, want one attempted create", mock.createdDomains)
	}
	if len(mock.createdRecords) != 1 {
		t.Fatalf("created records = %d, want 1", len(mock.createdRecords))
	}
}

func TestDNSDriver_Create_DomainConflictHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mock := &mockDomainsClient{
		domain:         testDomain(),
		expectedDomain: "example.com",
		getErrs:        []error{godoStatusErr(http.StatusNotFound)},
		createErr:      godoStatusErr(http.StatusConflict),
		afterCreateErr: cancel,
	}
	d := drivers.NewDNSDriverWithClient(mock)
	defer cancel()

	_, err := d.Create(ctx, interfaces.ResourceSpec{
		Name:   "example-dns",
		Config: map[string]any{"domain": "example.com"},
	})
	if err == nil {
		t.Fatal("expected canceled context error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(mock.createdDomains) != 1 {
		t.Fatalf("created domains = %v, want one attempted create", mock.createdDomains)
	}
}

func TestDNSDriver_Create_DomainConflictRejectsEmptyPostConflictDomain(t *testing.T) {
	mock := &mockDomainsClient{
		expectedDomain: "example.com",
		getErrs:        []error{godoStatusErr(http.StatusNotFound), nil},
		createErr:      godoStatusErr(http.StatusConflict),
	}
	d := drivers.NewDNSDriverWithClient(mock)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "example-dns",
		Config: map[string]any{"domain": "example.com"},
	})
	if err == nil {
		t.Fatal("expected empty domain response error, got nil")
	}
	if !strings.Contains(err.Error(), "API returned empty domain") {
		t.Fatalf("error = %q, want empty domain response", err)
	}
}

func TestDNSDriver_Create_RetriesTransientGetAfterDomainConflict(t *testing.T) {
	tests := []struct {
		name string
		code int
	}{
		{name: "transient", code: http.StatusInternalServerError},
		{name: "rate limited", code: http.StatusTooManyRequests},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockDomainsClient{
				domain:         testDomain(),
				expectedDomain: "example.com",
				getErrs:        []error{godoStatusErr(http.StatusNotFound), godoStatusErr(tt.code), nil},
				createErr:      godoStatusErr(http.StatusConflict),
			}
			d := drivers.NewDNSDriverWithClient(mock)

			out, err := d.Create(context.Background(), interfaces.ResourceSpec{
				Name:   "example-dns",
				Config: map[string]any{"domain": "example.com"},
			})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if out.ProviderID != "example.com" {
				t.Fatalf("ProviderID = %q, want example.com", out.ProviderID)
			}
		})
	}
}

func TestDNSDriver_Create_DomainConflictReturnsExhaustedRetryableGetError(t *testing.T) {
	mock := &mockDomainsClient{
		domain:         testDomain(),
		expectedDomain: "example.com",
		getErrs: []error{
			godoStatusErr(http.StatusNotFound),
			godoStatusErr(http.StatusTooManyRequests),
			godoStatusErr(http.StatusTooManyRequests),
			godoStatusErr(http.StatusTooManyRequests),
			godoStatusErr(http.StatusTooManyRequests),
			godoStatusErr(http.StatusTooManyRequests),
		},
		createErr: godoStatusErr(http.StatusConflict),
	}
	d := drivers.NewDNSDriverWithClient(mock)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "example-dns",
		Config: map[string]any{"domain": "example.com"},
	})
	if err == nil {
		t.Fatal("expected exhausted rate-limit error, got nil")
	}
	if !errors.Is(err, interfaces.ErrRateLimited) {
		t.Fatalf("error = %v, want ErrRateLimited", err)
	}
	if errors.Is(err, interfaces.ErrResourceAlreadyExists) {
		t.Fatalf("error = %v, should not be classified as ErrResourceAlreadyExists", err)
	}
}

func TestDNSDriver_Create_DomainConflictReturnsOriginalConflictAfterRepeatedNotFound(t *testing.T) {
	tests := []struct {
		name    string
		getErrs []error
	}{
		{
			name: "only not found",
			getErrs: []error{
				godoStatusErr(http.StatusNotFound),
				godoStatusErr(http.StatusNotFound),
				godoStatusErr(http.StatusNotFound),
				godoStatusErr(http.StatusNotFound),
				godoStatusErr(http.StatusNotFound),
				godoStatusErr(http.StatusNotFound),
			},
		},
		{
			name: "retryable followed by not found",
			getErrs: []error{
				godoStatusErr(http.StatusNotFound),
				godoStatusErr(http.StatusTooManyRequests),
				godoStatusErr(http.StatusNotFound),
				godoStatusErr(http.StatusNotFound),
				godoStatusErr(http.StatusNotFound),
				godoStatusErr(http.StatusNotFound),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockDomainsClient{
				domain:         testDomain(),
				expectedDomain: "example.com",
				getErrs:        tt.getErrs,
				createErr:      godoStatusErr(http.StatusConflict),
			}
			d := drivers.NewDNSDriverWithClient(mock)

			_, err := d.Create(context.Background(), interfaces.ResourceSpec{
				Name:   "example-dns",
				Config: map[string]any{"domain": "example.com"},
			})
			if err == nil {
				t.Fatal("expected exhausted conflict error, got nil")
			}
			if !errors.Is(err, interfaces.ErrResourceAlreadyExists) {
				t.Fatalf("error = %v, want ErrResourceAlreadyExists", err)
			}
			if errors.Is(err, interfaces.ErrResourceNotFound) {
				t.Fatalf("error = %v, should not be classified as ErrResourceNotFound", err)
			}
			if errors.Is(err, interfaces.ErrRateLimited) || errors.Is(err, interfaces.ErrTransient) {
				t.Fatalf("error = %v, should not retain stale retryable classification", err)
			}
		})
	}
}

func TestDNSDriver_Create_AdoptsRecordAfterCreateConflict(t *testing.T) {
	mock := &mockDomainsClient{
		domain:                      testDomain(),
		expectedDomain:              "example.com",
		createRecordErrs:            []error{godoStatusErr(http.StatusConflict)},
		recordsAfterCreateRecordErr: []godo.DomainRecord{{ID: 10, Type: "A", Name: "@", Data: "1.2.3.4", TTL: 300}},
		recordsAfterRecordListCall:  3,
	}
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
	if len(mock.attemptedRecords) != 1 {
		t.Fatalf("attempted records = %d, want 1", len(mock.attemptedRecords))
	}
	if len(mock.events) < 3 || mock.events[0] != "records" || mock.events[1] != "create_record" || mock.events[2] != "records" {
		t.Fatalf("events = %v, want records before create_record before post-conflict records", mock.events)
	}
	if len(mock.createdRecords) != 0 {
		t.Fatalf("created records = %d, want 0 after conflict adoption", len(mock.createdRecords))
	}
	records, ok := out.Outputs["records"].([]map[string]any)
	if !ok {
		t.Fatalf("Outputs[records] = %T, want []map[string]any", out.Outputs["records"])
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if got := records[0]["data"]; got != "1.2.3.4" {
		t.Fatalf("record data = %v, want 1.2.3.4", got)
	}
}

func TestDNSDriver_Create_RecordConflictHonorsCanceledContextBeforeRelist(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mock := &mockDomainsClient{
		domain:               testDomain(),
		expectedDomain:       "example.com",
		createRecordErrs:     []error{godoStatusErr(http.StatusConflict)},
		afterCreateRecordErr: cancel,
	}
	d := drivers.NewDNSDriverWithClient(mock)
	defer cancel()

	_, err := d.Create(ctx, interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "A", "name": "@", "data": "1.2.3.4", "ttl": 300},
			},
		},
	})
	if err == nil {
		t.Fatal("expected canceled context error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(mock.attemptedRecords) != 1 {
		t.Fatalf("attempted records = %d, want 1", len(mock.attemptedRecords))
	}
	if len(mock.recordListCalls) != 1 {
		t.Fatalf("record list calls = %d, want only initial pre-create list", len(mock.recordListCalls))
	}
}

func TestDNSDriver_Create_RetriesRetryableRecordListAfterCreateConflict(t *testing.T) {
	tests := []struct {
		name string
		code int
	}{
		{name: "transient", code: http.StatusInternalServerError},
		{name: "rate limited", code: http.StatusTooManyRequests},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockDomainsClient{
				domain:                      testDomain(),
				expectedDomain:              "example.com",
				createRecordErrs:            []error{godoStatusErr(http.StatusConflict)},
				recordListErrs:              []error{nil, godoStatusErr(tt.code), nil},
				recordsAfterCreateRecordErr: []godo.DomainRecord{{ID: 10, Type: "A", Name: "@", Data: "1.2.3.4", TTL: 300}},
				recordsAfterRecordListCall:  3,
			}
			d := drivers.NewDNSDriverWithClient(mock)

			_, err := d.Create(context.Background(), interfaces.ResourceSpec{
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
			if len(mock.recordListCalls) < 3 {
				t.Fatalf("record list calls = %d, want at least 3", len(mock.recordListCalls))
			}
		})
	}
}

func TestDNSDriver_Update_RecordConflictHonorsCanceledContextBeforeRelist(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mock := &mockDomainsClient{
		domain:               testDomain(),
		expectedDomain:       "example.com",
		createRecordErrs:     []error{godoStatusErr(http.StatusConflict)},
		afterCreateRecordErr: cancel,
	}
	d := drivers.NewDNSDriverWithClient(mock)
	defer cancel()

	_, err := d.Update(ctx, interfaces.ResourceRef{
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
	if err == nil {
		t.Fatal("expected canceled context error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(mock.recordListCalls) != 1 {
		t.Fatalf("record list calls = %d, want only initial pre-create list", len(mock.recordListCalls))
	}
}

func TestDNSDriver_Update_RecordConflictHonorsCanceledContextBeforeEdit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	recordListCallsBeforeCancel := 0
	mock := &mockDomainsClient{
		domain:                      testDomain(),
		expectedDomain:              "example.com",
		createRecordErrs:            []error{godoStatusErr(http.StatusConflict)},
		recordsAfterCreateRecordErr: []godo.DomainRecord{{ID: 10, Type: "CNAME", Name: "www", Data: "old.example.com", TTL: 300}},
		recordsAfterRecordListCall:  2,
	}
	mock.afterRecords = func() {
		if len(mock.recordListCalls) >= 2 {
			recordListCallsBeforeCancel = len(mock.recordListCalls)
			cancel()
		}
	}
	d := drivers.NewDNSDriverWithClient(mock)
	defer cancel()

	_, err := d.Update(ctx, interfaces.ResourceRef{
		Name: "example-dns", ProviderID: "example.com",
	}, interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "CNAME", "name": "www", "data": "new.example.com", "ttl": 300},
			},
		},
	})
	if err == nil {
		t.Fatal("expected canceled context error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(mock.attemptedRecords) != 1 {
		t.Fatalf("attempted records = %d, want 1", len(mock.attemptedRecords))
	}
	if len(mock.attemptedEdits) != 0 {
		t.Fatalf("attempted edits = %d, want 0 because driver should guard before EditRecord", len(mock.attemptedEdits))
	}
	if len(mock.editedRecords) != 0 {
		t.Fatalf("edited records = %d, want 0", len(mock.editedRecords))
	}
	if recordListCallsBeforeCancel < 2 {
		t.Fatalf("record list calls before cancel = %d, want post-conflict relist", recordListCallsBeforeCancel)
	}
}

func TestDNSDriver_Update_NormalEditHonorsCanceledContextBeforeEdit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mock := &mockDomainsClient{
		domain:         testDomain(),
		expectedDomain: "example.com",
		records: []godo.DomainRecord{
			{ID: 10, Type: "CNAME", Name: "www", Data: "old.example.com", TTL: 300},
		},
	}
	mock.afterRecords = cancel
	d := drivers.NewDNSDriverWithClient(mock)
	defer cancel()

	_, err := d.Update(ctx, interfaces.ResourceRef{
		Name: "example-dns", ProviderID: "example.com",
	}, interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "CNAME", "name": "www", "data": "new.example.com", "ttl": 300},
			},
		},
	})
	if err == nil {
		t.Fatal("expected canceled context error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(mock.attemptedEdits) != 0 {
		t.Fatalf("attempted edits = %d, want 0 because driver should guard before EditRecord", len(mock.attemptedEdits))
	}
	if len(mock.editedRecords) != 0 {
		t.Fatalf("edited records = %d, want 0", len(mock.editedRecords))
	}
}

func TestDNSDriver_Create_RecordConflictHonorsCanceledContextBeforeEdit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	recordListCallsBeforeCancel := 0
	mock := &mockDomainsClient{
		domain:                      testDomain(),
		expectedDomain:              "example.com",
		createRecordErrs:            []error{godoStatusErr(http.StatusConflict)},
		recordsAfterCreateRecordErr: []godo.DomainRecord{{ID: 10, Type: "CNAME", Name: "www", Data: "old.example.com", TTL: 300}},
		recordsAfterRecordListCall:  2,
	}
	mock.afterRecords = func() {
		if len(mock.recordListCalls) >= 2 {
			recordListCallsBeforeCancel = len(mock.recordListCalls)
			cancel()
		}
	}
	d := drivers.NewDNSDriverWithClient(mock)
	defer cancel()

	_, err := d.Create(ctx, interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "CNAME", "name": "www", "data": "new.example.com", "ttl": 300},
			},
		},
	})
	if err == nil {
		t.Fatal("expected canceled context error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(mock.attemptedRecords) != 1 {
		t.Fatalf("attempted records = %d, want 1", len(mock.attemptedRecords))
	}
	if len(mock.attemptedEdits) != 0 {
		t.Fatalf("attempted edits = %d, want 0 because driver should guard before EditRecord", len(mock.attemptedEdits))
	}
	if len(mock.editedRecords) != 0 {
		t.Fatalf("edited records = %d, want 0", len(mock.editedRecords))
	}
	if recordListCallsBeforeCancel < 2 {
		t.Fatalf("record list calls before cancel = %d, want post-conflict relist", recordListCallsBeforeCancel)
	}
}

func TestDNSDriver_Update_RetriesRetryableRecordListAfterCreateConflict(t *testing.T) {
	tests := []struct {
		name string
		code int
	}{
		{name: "transient", code: http.StatusInternalServerError},
		{name: "rate limited", code: http.StatusTooManyRequests},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockDomainsClient{
				domain:                      testDomain(),
				expectedDomain:              "example.com",
				createRecordErrs:            []error{godoStatusErr(http.StatusConflict)},
				recordListErrs:              []error{nil, godoStatusErr(tt.code), nil},
				recordsAfterCreateRecordErr: []godo.DomainRecord{{ID: 10, Type: "A", Name: "@", Data: "1.2.3.4", TTL: 300}},
				recordsAfterRecordListCall:  3,
			}
			d := drivers.NewDNSDriverWithClient(mock)

			_, err := d.Update(context.Background(), interfaces.ResourceRef{
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
			if len(mock.recordListCalls) < 3 {
				t.Fatalf("record list calls = %d, want at least 3", len(mock.recordListCalls))
			}
		})
	}
}

func TestDNSDriver_Update_ReturnsExhaustedRetryableRecordListErrorAfterCreateConflict(t *testing.T) {
	mock := &mockDomainsClient{
		domain:           testDomain(),
		expectedDomain:   "example.com",
		createRecordErrs: []error{godoStatusErr(http.StatusConflict)},
		recordListErrs: []error{
			nil,
			godoStatusErr(http.StatusTooManyRequests),
			godoStatusErr(http.StatusTooManyRequests),
			godoStatusErr(http.StatusTooManyRequests),
			godoStatusErr(http.StatusTooManyRequests),
			godoStatusErr(http.StatusTooManyRequests),
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
				map[string]any{"type": "A", "name": "@", "data": "1.2.3.4", "ttl": 300},
			},
		},
	})
	if err == nil {
		t.Fatal("expected exhausted rate-limit error, got nil")
	}
	if !errors.Is(err, interfaces.ErrRateLimited) {
		t.Fatalf("error = %v, want ErrRateLimited", err)
	}
}

func TestDNSDriver_Update_RecordConflictRelistValidatesNameConflicts(t *testing.T) {
	mock := &mockDomainsClient{
		domain:                      testDomain(),
		expectedDomain:              "example.com",
		createRecordErrs:            []error{godoStatusErr(http.StatusConflict)},
		recordsAfterCreateRecordErr: []godo.DomainRecord{{ID: 10, Type: "CNAME", Name: "www", Data: "old.example.com", TTL: 300}},
		recordsAfterRecordListCall:  2,
	}
	d := drivers.NewDNSDriverWithClient(mock)

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "example-dns", ProviderID: "example.com",
	}, interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "A", "name": "www", "data": "1.2.3.4", "ttl": 300},
			},
		},
	})
	if err == nil {
		t.Fatal("expected live conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "conflicting DNS record") {
		t.Fatalf("error = %q, want conflicting DNS record", err)
	}
}

func TestDNSDriver_Create_AcceptsRecordMapSliceConfig(t *testing.T) {
	mock := &mockDomainsClient{
		domain:         testDomain(),
		expectedDomain: "example.com",
	}
	d := drivers.NewDNSDriverWithClient(mock)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []map[string]any{
				{"type": "A", "name": "@", "data": "1.2.3.4", "ttl": 300},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(mock.createdRecords) != 1 {
		t.Fatalf("created records = %d, want 1", len(mock.createdRecords))
	}
	if got := mock.createdRecords[0].req.Data; got != "1.2.3.4" {
		t.Errorf("created record data = %q, want 1.2.3.4", got)
	}
}

func TestDNSDriver_Create_AcceptsDistinctCAARecordsWithSameData(t *testing.T) {
	mock := &mockDomainsClient{
		domain:         testDomain(),
		expectedDomain: "example.com",
	}
	d := drivers.NewDNSDriverWithClient(mock)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "CAA", "name": "@", "data": "letsencrypt.org", "tag": "issue", "flags": 0},
				map[string]any{"type": "CAA", "name": "@", "data": "letsencrypt.org", "tag": "issuewild", "flags": 0},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(mock.createdRecords) != 2 {
		t.Fatalf("created records = %d, want 2", len(mock.createdRecords))
	}
}

func TestDNSDriver_Create_CanonicalizesCAARecordTags(t *testing.T) {
	mock := &mockDomainsClient{
		domain:         testDomain(),
		expectedDomain: "example.com",
	}
	d := drivers.NewDNSDriverWithClient(mock)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "CAA", "name": "@", "data": "letsencrypt.org", "tag": "Issue", "flags": 0},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(mock.createdRecords) != 1 {
		t.Fatalf("created records = %d, want 1", len(mock.createdRecords))
	}
	if got := mock.createdRecords[0].req.Tag; got != "issue" {
		t.Fatalf("created CAA tag = %q, want issue", got)
	}
}

func TestDNSDriver_Create_DeduplicatesCAARecordsWithCanonicalTags(t *testing.T) {
	mock := &mockDomainsClient{
		domain:         testDomain(),
		expectedDomain: "example.com",
	}
	d := drivers.NewDNSDriverWithClient(mock)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "CAA", "name": "@", "data": "letsencrypt.org", "tag": "Issue", "flags": 0},
				map[string]any{"type": "CAA", "name": "@", "data": "letsencrypt.org", "tag": "issue", "flags": 0},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(mock.createdRecords) != 1 {
		t.Fatalf("created records = %d, want 1", len(mock.createdRecords))
	}
	if got := mock.createdRecords[0].req.Tag; got != "issue" {
		t.Fatalf("created CAA tag = %q, want issue", got)
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

func TestDNSDriver_Read_RejectsEmptyDomainResponse(t *testing.T) {
	mock := &mockDomainsClient{
		expectedDomain: "example.com",
	}
	d := drivers.NewDNSDriverWithClient(mock)

	_, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name: "example-dns", ProviderID: "example.com",
	})
	if err == nil {
		t.Fatal("expected empty domain response error, got nil")
	}
	if !strings.Contains(err.Error(), "API returned empty domain") {
		t.Fatalf("error = %q, want empty domain response", err)
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
		"flags":    0,
		"tag":      "",
	}
	for key, wantValue := range want {
		if got := records[0][key]; got != wantValue {
			t.Errorf("records[0][%q] = %#v, want %#v", key, got, wantValue)
		}
	}
}

func TestDNSDriver_Read_CanonicalizesCAARecordTagOutputs(t *testing.T) {
	mock := &mockDomainsClient{
		domain:         testDomain(),
		expectedDomain: "example.com",
		records: []godo.DomainRecord{
			{ID: 10, Type: "CAA", Name: "@", Data: "letsencrypt.org", TTL: 300, Flags: 0, Tag: "Issue"},
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
		t.Fatalf("records = %d, want 1", len(records))
	}
	if got := records[0]["tag"]; got != "issue" {
		t.Fatalf("CAA output tag = %v, want issue", got)
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

func TestDNSDriver_Update_RejectsDomainReplacement(t *testing.T) {
	mock := &mockDomainsClient{domain: testDomain()}
	d := drivers.NewDNSDriverWithClient(mock)

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "example-dns", ProviderID: "old.example.com",
	}, interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"domain": "new.example.com",
			"records": []any{
				map[string]any{"type": "A", "name": "@", "data": "1.2.3.4"},
			},
		},
	})
	if err == nil {
		t.Fatal("expected domain replacement error, got nil")
	}
	if !strings.Contains(err.Error(), "cannot change domain") {
		t.Fatalf("error = %q, want cannot change domain", err)
	}
	if len(mock.createdRecords) != 0 {
		t.Fatalf("created records = %d, want 0", len(mock.createdRecords))
	}
}

func TestDNSDriver_Update_RejectsMalformedDomainConfig(t *testing.T) {
	tests := []struct {
		name   string
		domain any
	}{
		{name: "non string", domain: 42},
		{name: "empty string", domain: ""},
		{name: "invalid domain name", domain: "not a domain"},
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
				Name:   "example-dns",
				Config: map[string]any{"domain": tt.domain},
			})
			if err == nil {
				t.Fatal("expected domain validation error, got nil")
			}
			if !strings.Contains(err.Error(), "dns domain") {
				t.Fatalf("error = %q, want dns domain validation error", err)
			}
			if len(mock.recordListCalls) != 0 {
				t.Fatalf("record list calls = %d, want 0", len(mock.recordListCalls))
			}
			if len(mock.createdRecords) != 0 {
				t.Fatalf("created records = %d, want 0", len(mock.createdRecords))
			}
		})
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

func TestDNSDriver_Update_SkipsIdenticalCAARecordWithCanonicalTag(t *testing.T) {
	mock := &mockDomainsClient{
		domain: testDomain(),
		records: []godo.DomainRecord{
			{ID: 10, Type: "CAA", Name: "@", Data: "letsencrypt.org", TTL: 300, Flags: 0, Tag: "issue"},
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
				map[string]any{"type": "CAA", "name": "@", "data": "letsencrypt.org", "ttl": 300, "flags": 0, "tag": "Issue"},
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

func TestDNSDriver_Update_CanonicalizesCAARecordTagOnEdit(t *testing.T) {
	mock := &mockDomainsClient{
		domain: testDomain(),
		records: []godo.DomainRecord{
			{ID: 10, Type: "CAA", Name: "@", Data: "letsencrypt.org", TTL: 300, Flags: 0, Tag: "issue"},
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
				map[string]any{"type": "CAA", "name": "@", "data": "letsencrypt.org", "ttl": 600, "flags": 0, "tag": "Issue"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(mock.editedRecords) != 1 {
		t.Fatalf("edited records = %d, want 1", len(mock.editedRecords))
	}
	if got := mock.editedRecords[0].req.Tag; got != "issue" {
		t.Fatalf("edited CAA tag = %q, want issue", got)
	}
}

func TestDNSDriver_Update_AdoptsRecordAfterCreateConflict(t *testing.T) {
	mock := &mockDomainsClient{
		domain:                      testDomain(),
		expectedDomain:              "example.com",
		createRecordErrs:            []error{godoStatusErr(http.StatusConflict)},
		recordsAfterCreateRecordErr: []godo.DomainRecord{{ID: 10, Type: "A", Name: "@", Data: "1.2.3.4", TTL: 300}},
		recordsAfterRecordListCall:  3,
	}
	d := drivers.NewDNSDriverWithClient(mock)

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
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
	if len(mock.attemptedRecords) != 1 {
		t.Fatalf("attempted records = %d, want 1", len(mock.attemptedRecords))
	}
	if len(mock.createdRecords) != 0 {
		t.Fatalf("created records = %d, want 0 after create conflict adoption", len(mock.createdRecords))
	}
	if len(mock.editedRecords) != 0 {
		t.Fatalf("edited records = %d, want 0", len(mock.editedRecords))
	}
	if len(mock.recordListCalls) < 2 {
		t.Fatalf("record list calls = %d, want at least 2", len(mock.recordListCalls))
	}
}

func TestDNSDriver_Update_RecordConflictRefreshesExistingForLaterRecords(t *testing.T) {
	mock := &mockDomainsClient{
		domain:                      testDomain(),
		expectedDomain:              "example.com",
		createRecordErrs:            []error{godoStatusErr(http.StatusConflict)},
		recordsAfterCreateRecordErr: []godo.DomainRecord{{ID: 10, Type: "A", Name: "@", Data: "1.2.3.4", TTL: 300}, {ID: 11, Type: "CNAME", Name: "www", Data: "old.example.com", TTL: 300}},
		recordsAfterRecordListCall:  2,
	}
	d := drivers.NewDNSDriverWithClient(mock)

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "example-dns", ProviderID: "example.com",
	}, interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "A", "name": "@", "data": "1.2.3.4", "ttl": 300},
				map[string]any{"type": "CNAME", "name": "www", "data": "new.example.com", "ttl": 300},
			},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(mock.attemptedRecords) != 1 {
		t.Fatalf("attempted records = %d, want only the first record create attempt", len(mock.attemptedRecords))
	}
	if len(mock.editedRecords) != 1 {
		t.Fatalf("edited records = %d, want second record edited from refreshed conflict relist", len(mock.editedRecords))
	}
	if got := mock.editedRecords[0].req.Data; got != "new.example.com" {
		t.Fatalf("edited record data = %q, want new.example.com", got)
	}
}

func TestDNSDriver_Update_EditsRecordAfterCreateConflictWithDifferentCNAME(t *testing.T) {
	mock := &mockDomainsClient{
		domain:                      testDomain(),
		expectedDomain:              "example.com",
		createRecordErrs:            []error{godoStatusErr(http.StatusConflict)},
		recordsAfterCreateRecordErr: []godo.DomainRecord{{ID: 10, Type: "CNAME", Name: "www", Data: "old.example.com", TTL: 300}},
		recordsAfterRecordListCall:  3,
	}
	d := drivers.NewDNSDriverWithClient(mock)

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "example-dns", ProviderID: "example.com",
	}, interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "CNAME", "name": "www", "data": "new.example.com", "ttl": 300},
			},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(mock.attemptedRecords) != 1 {
		t.Fatalf("attempted records = %d, want 1", len(mock.attemptedRecords))
	}
	if len(mock.editedRecords) != 1 {
		t.Fatalf("edited records = %d, want 1", len(mock.editedRecords))
	}
	if got := mock.editedRecords[0].req.Data; got != "new.example.com" {
		t.Fatalf("edited record data = %q, want new.example.com", got)
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

func TestDNSDriver_Update_RejectsKnownDNSConflicts(t *testing.T) {
	tests := []struct {
		name    string
		records []any
	}{
		{
			name: "two CNAMEs at same name",
			records: []any{
				map[string]any{"type": "CNAME", "name": "www", "data": "one.example.com"},
				map[string]any{"type": "CNAME", "name": "www", "data": "two.example.com"},
			},
		},
		{
			name: "CNAME conflicts with A",
			records: []any{
				map[string]any{"type": "CNAME", "name": "www", "data": "target.example.com"},
				map[string]any{"type": "A", "name": "www", "data": "1.2.3.4"},
			},
		},
		{
			name: "CNAME conflicts with AAAA",
			records: []any{
				map[string]any{"type": "AAAA", "name": "www", "data": "2001:db8::1"},
				map[string]any{"type": "CNAME", "name": "www", "data": "target.example.com"},
			},
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
					"records": tt.records,
				},
			})
			if err == nil {
				t.Fatal("expected DNS conflict error, got nil")
			}
			if !strings.Contains(err.Error(), "conflicting DNS record") {
				t.Fatalf("error = %q, want conflicting DNS record", err)
			}
			if len(mock.createdRecords) != 0 {
				t.Fatalf("created records = %d, want 0", len(mock.createdRecords))
			}
		})
	}
}

func TestDNSDriver_Update_AllowsMultiValueTXTRecords(t *testing.T) {
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
				map[string]any{"type": "TXT", "name": "@", "data": "one"},
				map[string]any{"type": "TXT", "name": "@", "data": "two"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(mock.createdRecords) != 2 {
		t.Fatalf("created records = %d, want 2", len(mock.createdRecords))
	}
}

func TestDNSDriver_Update_RejectsConflictsWithExistingLiveRecordsBeforeMutation(t *testing.T) {
	tests := []struct {
		name     string
		existing []godo.DomainRecord
		records  []any
	}{
		{
			name: "desired A conflicts with existing CNAME",
			existing: []godo.DomainRecord{
				{ID: 10, Type: "CNAME", Name: "www", Data: "target.example.com", TTL: 300},
			},
			records: []any{
				map[string]any{"type": "TXT", "name": "@", "data": "safe-to-create"},
				map[string]any{"type": "A", "name": "www", "data": "1.2.3.4"},
			},
		},
		{
			name: "desired CNAME conflicts with existing A",
			existing: []godo.DomainRecord{
				{ID: 10, Type: "A", Name: "www", Data: "1.2.3.4", TTL: 300},
			},
			records: []any{
				map[string]any{"type": "TXT", "name": "@", "data": "safe-to-create"},
				map[string]any{"type": "CNAME", "name": "www", "data": "target.example.com"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockDomainsClient{
				domain:         testDomain(),
				expectedDomain: "example.com",
				records:        tt.existing,
			}
			d := drivers.NewDNSDriverWithClient(mock)

			_, err := d.Update(context.Background(), interfaces.ResourceRef{
				Name: "example-dns", ProviderID: "example.com",
			}, interfaces.ResourceSpec{
				Name: "example-dns",
				Config: map[string]any{
					"domain":  "example.com",
					"records": tt.records,
				},
			})
			if err == nil {
				t.Fatal("expected live DNS conflict error, got nil")
			}
			if !strings.Contains(err.Error(), "conflicting DNS record") {
				t.Fatalf("error = %q, want conflicting DNS record", err)
			}
			if len(mock.createdRecords) != 0 {
				t.Fatalf("created records = %d, want 0", len(mock.createdRecords))
			}
			if len(mock.editedRecords) != 0 {
				t.Fatalf("edited records = %d, want 0", len(mock.editedRecords))
			}
		})
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
			name:    "SOA desired record unsupported",
			record:  map[string]any{"type": "SOA", "name": "@", "data": "ns1.digitalocean.com hostmaster.example.com 1 10800 3600 604800 1800"},
			wantErr: `records[0].type "SOA" is not supported`,
		},
		{
			name:    "invalid name field type",
			record:  map[string]any{"type": "A", "name": 42, "data": "1.2.3.4"},
			wantErr: `records[0].name must be a string`,
		},
		{
			name:    "name with whitespace",
			record:  map[string]any{"type": "A", "name": "bad label", "data": "1.2.3.4"},
			wantErr: `records[0].name must be a valid DNS record name`,
		},
		{
			name:    "name with bad label",
			record:  map[string]any{"type": "A", "name": "-bad", "data": "1.2.3.4"},
			wantErr: `records[0].name must be a valid DNS record name`,
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
		{
			name:    "A data must be IPv4",
			record:  map[string]any{"type": "A", "name": "@", "data": "2001:db8::1"},
			wantErr: `records[0].data must be an IPv4 address`,
		},
		{
			name:    "A rejects priority",
			record:  map[string]any{"type": "A", "name": "@", "data": "1.2.3.4", "priority": 10},
			wantErr: `records[0].priority is not valid for A records`,
		},
		{
			name:    "A rejects CAA tag",
			record:  map[string]any{"type": "A", "name": "@", "data": "1.2.3.4", "tag": "issue"},
			wantErr: `records[0].tag is not valid for A records`,
		},
		{
			name:    "AAAA data must be IPv6",
			record:  map[string]any{"type": "AAAA", "name": "@", "data": "192.0.2.10"},
			wantErr: `records[0].data must be an IPv6 address`,
		},
		{
			name:    "CNAME rejects SRV port",
			record:  map[string]any{"type": "CNAME", "name": "www", "data": "target.example.com", "port": 443},
			wantErr: `records[0].port is not valid for CNAME records`,
		},
		{
			name:    "MX priority required",
			record:  map[string]any{"type": "MX", "name": "@", "data": "mail.example.com"},
			wantErr: `records[0].priority is required for MX records`,
		},
		{
			name:    "MX rejects port",
			record:  map[string]any{"type": "MX", "name": "@", "data": "mail.example.com", "priority": 10, "port": 25},
			wantErr: `records[0].port is not valid for MX records`,
		},
		{
			name:    "MX data must be hostname",
			record:  map[string]any{"type": "MX", "name": "@", "data": "mail label.example.com", "priority": 10},
			wantErr: `records[0].data must be a hostname for MX records`,
		},
		{
			name:    "NS data must be hostname",
			record:  map[string]any{"type": "NS", "name": "@", "data": "ns_1.example.com"},
			wantErr: `records[0].data must be a hostname for NS records`,
		},
		{
			name:    "TXT rejects weight",
			record:  map[string]any{"type": "TXT", "name": "@", "data": "hello", "weight": 5},
			wantErr: `records[0].weight is not valid for TXT records`,
		},
		{
			name:    "SRV port required",
			record:  map[string]any{"type": "SRV", "name": "_sip._tcp", "data": "sip.example.com", "priority": 10, "weight": 5},
			wantErr: `records[0].port is required for SRV records`,
		},
		{
			name:    "SRV data must be hostname",
			record:  map[string]any{"type": "SRV", "name": "_sip._tcp", "data": "sip_example.com", "priority": 10, "port": 5060, "weight": 5},
			wantErr: `records[0].data must be a hostname for SRV records`,
		},
		{
			name:    "CAA tag required",
			record:  map[string]any{"type": "CAA", "name": "@", "data": "letsencrypt.org", "flags": 0},
			wantErr: `records[0].tag is required for CAA records`,
		},
		{
			name:    "CAA tag allow-list",
			record:  map[string]any{"type": "CAA", "name": "@", "data": "letsencrypt.org", "tag": "accounturi", "flags": 0},
			wantErr: `records[0].tag must be one of issue, issuewild, iodef`,
		},
		{
			name:    "CAA flags range",
			record:  map[string]any{"type": "CAA", "name": "@", "data": "letsencrypt.org", "tag": "issue", "flags": 256},
			wantErr: `records[0].flags must be between 0 and 255`,
		},
		{
			name:    "CAA rejects priority",
			record:  map[string]any{"type": "CAA", "name": "@", "data": "letsencrypt.org", "tag": "issue", "flags": 0, "priority": 1},
			wantErr: `records[0].priority is not valid for CAA records`,
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

func TestDNSDriver_Update_AllowsValidRecordNames(t *testing.T) {
	tests := []struct {
		name   string
		record map[string]any
	}{
		{
			name:   "apex",
			record: map[string]any{"type": "A", "name": "@", "data": "1.2.3.4"},
		},
		{
			name:   "wildcard",
			record: map[string]any{"type": "A", "name": "*.preview", "data": "1.2.3.4"},
		},
		{
			name:   "underscore service labels",
			record: map[string]any{"type": "SRV", "name": "_sip._tcp", "data": "sip.example.com", "priority": 10, "port": 5060, "weight": 5},
		},
		{
			name:   "fqdn",
			record: map[string]any{"type": "CNAME", "name": "WWW.Example.com.", "data": "Target.Example.com."},
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
			if err != nil {
				t.Fatalf("Update: %v", err)
			}
			if len(mock.createdRecords) != 1 {
				t.Fatalf("created records = %d, want 1", len(mock.createdRecords))
			}
		})
	}
}

func TestDNSDriver_Update_CreatesDistinctCAAWhenIdentityFieldsDiffer(t *testing.T) {
	mock := &mockDomainsClient{
		domain:         testDomain(),
		expectedDomain: "example.com",
		records: []godo.DomainRecord{
			{ID: 10, Type: "CAA", Name: "@", Data: "letsencrypt.org", TTL: 300, Tag: "issue", Flags: 0},
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
				map[string]any{"type": "CAA", "name": "@", "data": "letsencrypt.org", "ttl": 300, "tag": "issuewild", "flags": 0},
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
	if got := mock.createdRecords[0].req.Tag; got != "issuewild" {
		t.Errorf("created CAA tag = %q, want issuewild", got)
	}
}

func TestDNSDriver_Update_MatchesHostnameValuedRecordsCanonically(t *testing.T) {
	tests := []struct {
		name     string
		existing godo.DomainRecord
		desired  map[string]any
	}{
		{
			name:     "CNAME",
			existing: godo.DomainRecord{ID: 10, Type: "CNAME", Name: "www", Data: "Target.Example.com.", TTL: 300},
			desired:  map[string]any{"type": "CNAME", "name": "www", "data": "target.example.com", "ttl": 300},
		},
		{
			name:     "MX",
			existing: godo.DomainRecord{ID: 10, Type: "MX", Name: "@", Data: "Mail.Example.com.", TTL: 300, Priority: 10},
			desired:  map[string]any{"type": "MX", "name": "@", "data": "mail.example.com", "ttl": 300, "priority": 10},
		},
		{
			name:     "NS",
			existing: godo.DomainRecord{ID: 10, Type: "NS", Name: "@", Data: "NS1.Example.com.", TTL: 300},
			desired:  map[string]any{"type": "NS", "name": "@", "data": "ns1.example.com", "ttl": 300},
		},
		{
			name:     "SRV",
			existing: godo.DomainRecord{ID: 10, Type: "SRV", Name: "_sip._tcp", Data: "SIP.Example.com.", TTL: 300, Priority: 10, Port: 5060, Weight: 5},
			desired:  map[string]any{"type": "SRV", "name": "_sip._tcp", "data": "sip.example.com", "ttl": 300, "priority": 10, "port": 5060, "weight": 5},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockDomainsClient{
				domain:  testDomain(),
				records: []godo.DomainRecord{tt.existing},
			}
			d := drivers.NewDNSDriverWithClient(mock)

			_, err := d.Update(context.Background(), interfaces.ResourceRef{
				Name: "example-dns", ProviderID: "example.com",
			}, interfaces.ResourceSpec{
				Name: "example-dns",
				Config: map[string]any{
					"domain":  "example.com",
					"records": []any{tt.desired},
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
		})
	}
}

func TestDNSDriver_Update_PreservesTXTCaseSensitiveIdentity(t *testing.T) {
	mock := &mockDomainsClient{
		domain: testDomain(),
		records: []godo.DomainRecord{
			{ID: 10, Type: "TXT", Name: "@", Data: "Token=ABC", TTL: 300},
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
				map[string]any{"type": "TXT", "name": "@", "data": "token=abc", "ttl": 300},
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
}

func TestDNSDriver_Update_EditsExistingCNAMEWhenTargetChanges(t *testing.T) {
	mock := &mockDomainsClient{
		domain: testDomain(),
		records: []godo.DomainRecord{
			{ID: 10, Type: "CNAME", Name: "www", Data: "old.example.com", TTL: 300},
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
				map[string]any{"type": "CNAME", "name": "www", "data": "new.example.com", "ttl": 300},
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
	if mock.editedRecords[0].id != 10 {
		t.Fatalf("edited record ID = %d, want 10", mock.editedRecords[0].id)
	}
	if got := mock.editedRecords[0].req.Data; got != "new.example.com" {
		t.Fatalf("edited CNAME data = %q, want new.example.com", got)
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
					"weight":   10,
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
	if req.TTL != 600 || req.Weight != 10 {
		t.Errorf("edited request TTL/Weight = %d/%d, want 600/10", req.TTL, req.Weight)
	}
	if req.Priority != 20 || req.Port != 5060 {
		t.Errorf("edited request = %+v, want supported fields preserved", req)
	}
}

func TestDNSDriver_Update_CreatesDistinctSRVWhenIdentityFieldsDiffer(t *testing.T) {
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
					"ttl":      300,
					"priority": 20,
					"port":     5060,
					"weight":   30,
				},
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
	if got := mock.createdRecords[0].req.Weight; got != 30 {
		t.Errorf("created SRV weight = %d, want 30", got)
	}
}

func TestDNSDriver_Update_CreateRecordErrorDoesNotPersistRecord(t *testing.T) {
	mock := &mockDomainsClient{
		domain:          testDomain(),
		expectedDomain:  "example.com",
		createRecordErr: fmt.Errorf("create record failed"),
	}
	d := drivers.NewDNSDriverWithClient(mock)

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "example-dns", ProviderID: "example.com",
	}, interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "A", "name": "@", "data": "1.2.3.4"},
			},
		},
	})
	if err == nil {
		t.Fatal("expected create record error, got nil")
	}
	if len(mock.records) != 0 {
		t.Fatalf("persisted records = %+v, want none", mock.records)
	}
}

func TestDNSDriver_Update_EditRecordErrorDoesNotPersistRecord(t *testing.T) {
	mock := &mockDomainsClient{
		domain:         testDomain(),
		expectedDomain: "example.com",
		records: []godo.DomainRecord{
			{ID: 10, Type: "A", Name: "@", Data: "1.2.3.4", TTL: 300},
		},
		editRecordErr: fmt.Errorf("edit record failed"),
	}
	d := drivers.NewDNSDriverWithClient(mock)

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "example-dns", ProviderID: "example.com",
	}, interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "A", "name": "@", "data": "1.2.3.4", "ttl": 600},
			},
		},
	})
	if err == nil {
		t.Fatal("expected edit record error, got nil")
	}
	if got := mock.records[0].TTL; got != 300 {
		t.Fatalf("persisted TTL = %d, want original 300", got)
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

	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{Name: "example.com"}, nil)
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
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{Name: "example.com"}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=false when current exists")
	}
}

func TestDNSDriver_Diff_DetectsMissingDeclaredRecord(t *testing.T) {
	mock := &mockDomainsClient{}
	d := drivers.NewDNSDriverWithClient(mock)

	current := &interfaces.ResourceOutput{
		ProviderID: "example.com",
		Outputs: map[string]any{
			"records": []map[string]any{
				{"type": "A", "name": "@", "data": "1.2.3.4", "ttl": 300},
			},
		},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"records": []any{
				map[string]any{"type": "TXT", "name": "@", "data": "missing", "ttl": 300},
			},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Fatal("expected NeedsUpdate=true for missing declared record")
	}
}

func TestDNSDriver_Diff_DomainChangeRequiresReplacement(t *testing.T) {
	mock := &mockDomainsClient{}
	d := drivers.NewDNSDriverWithClient(mock)

	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Name:   "example-dns",
		Config: map[string]any{"domain": "new.example.com"},
	}, &interfaces.ResourceOutput{ProviderID: "old.example.com"})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsReplace {
		t.Fatal("expected NeedsReplace=true for domain change")
	}
	if !result.NeedsUpdate {
		t.Fatal("expected NeedsUpdate=true when replacement is required")
	}
	if len(result.Changes) != 1 {
		t.Fatalf("changes = %d, want 1", len(result.Changes))
	}
	if result.Changes[0].Path != "domain" || !result.Changes[0].ForceNew {
		t.Fatalf("change = %#v, want force-new domain change", result.Changes[0])
	}
}

func TestDNSDriver_Diff_LogicalNameDoesNotForceDomainReplacement(t *testing.T) {
	mock := &mockDomainsClient{}
	d := drivers.NewDNSDriverWithClient(mock)

	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Name:   "example-dns",
		Config: map[string]any{},
	}, &interfaces.ResourceOutput{ProviderID: "example.com"})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsReplace || result.NeedsUpdate {
		t.Fatalf("expected no replacement/update from logical name alone, got %#v", result)
	}
}

func TestDNSDriver_Diff_DetectsChangedDeclaredRecordFields(t *testing.T) {
	mock := &mockDomainsClient{}
	d := drivers.NewDNSDriverWithClient(mock)

	current := &interfaces.ResourceOutput{
		ProviderID: "example.com",
		Outputs: map[string]any{
			"records": []map[string]any{
				{"type": "SRV", "name": "_sip._tcp", "data": "sip.example.com", "ttl": 300, "priority": 20, "port": 5060, "weight": 10},
			},
		},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"records": []any{
				map[string]any{"type": "SRV", "name": "_sip._tcp", "data": "sip.example.com", "ttl": 600, "priority": 20, "port": 5060, "weight": 10},
			},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Fatal("expected NeedsUpdate=true for changed record fields")
	}
}

func TestDNSDriver_Diff_NoChangesForMatchingDeclaredRecords(t *testing.T) {
	mock := &mockDomainsClient{}
	d := drivers.NewDNSDriverWithClient(mock)

	current := &interfaces.ResourceOutput{
		ProviderID: "example.com",
		Outputs: map[string]any{
			"records": []map[string]any{
				{"type": "TXT", "name": "@", "data": "one", "ttl": 300},
				{"type": "TXT", "name": "@", "data": "undeclared", "ttl": 300},
			},
		},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"records": []any{
				map[string]any{"type": "TXT", "name": "@", "data": "one", "ttl": 300},
			},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsUpdate {
		t.Fatal("expected NeedsUpdate=false when declared record exists and matches")
	}
}

func TestDNSDriver_Diff_NoChangesForMatchingCAARecordWithCanonicalTag(t *testing.T) {
	mock := &mockDomainsClient{}
	d := drivers.NewDNSDriverWithClient(mock)

	current := &interfaces.ResourceOutput{
		ProviderID: "example.com",
		Outputs: map[string]any{
			"records": []map[string]any{
				{"type": "CAA", "name": "@", "data": "letsencrypt.org", "ttl": 300, "flags": 0, "tag": "issue"},
			},
		},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"records": []any{
				map[string]any{"type": "CAA", "name": "@", "data": "letsencrypt.org", "ttl": 300, "flags": 0, "tag": "Issue"},
			},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsUpdate || result.NeedsReplace {
		t.Fatalf("expected no diff, got %#v", result)
	}
}

func TestDNSDriver_Diff_IgnoresSOAInCurrentOutputs(t *testing.T) {
	mock := &mockDomainsClient{}
	d := drivers.NewDNSDriverWithClient(mock)

	current := &interfaces.ResourceOutput{
		ProviderID: "example.com",
		Outputs: map[string]any{
			"records": []map[string]any{
				{"type": "SOA", "name": "@", "data": "ns1.digitalocean.com hostmaster.example.com 1 10800 3600 604800 1800", "ttl": 1800},
				{"type": "A", "name": "@", "data": "1.2.3.4", "ttl": 300},
			},
		},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Name: "example-dns",
		Config: map[string]any{
			"records": []any{
				map[string]any{"type": "A", "name": "@", "data": "1.2.3.4", "ttl": 300},
			},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsUpdate {
		t.Fatal("expected NeedsUpdate=false when declared record matches and current has provider-owned SOA")
	}
}

func TestDNSDriver_Diff_ValidatesDesiredRecords(t *testing.T) {
	mock := &mockDomainsClient{}
	d := drivers.NewDNSDriverWithClient(mock)

	_, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Name: "example.com",
		Config: map[string]any{
			"records": []any{
				map[string]any{"type": "CNAME", "name": "www", "data": "one.example.com"},
				map[string]any{"type": "A", "name": "www", "data": "1.2.3.4"},
			},
		},
	}, &interfaces.ResourceOutput{ProviderID: "example.com"})
	if err == nil {
		t.Fatal("expected desired record validation error, got nil")
	}
	if !strings.Contains(err.Error(), "conflicting DNS record") {
		t.Fatalf("error = %q, want conflicting DNS record", err)
	}
}

func TestDNSDriver_Diff_RejectsMalformedDomainConfig(t *testing.T) {
	tests := []struct {
		name   string
		domain any
	}{
		{name: "non string", domain: 42},
		{name: "empty string", domain: ""},
		{name: "invalid domain name", domain: "not a domain"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockDomainsClient{}
			d := drivers.NewDNSDriverWithClient(mock)

			_, err := d.Diff(context.Background(), interfaces.ResourceSpec{
				Name:   "example-dns",
				Config: map[string]any{"domain": tt.domain},
			}, &interfaces.ResourceOutput{ProviderID: "example.com"})
			if err == nil {
				t.Fatal("expected domain validation error, got nil")
			}
			if !strings.Contains(err.Error(), "dns domain") {
				t.Fatalf("error = %q, want dns domain validation error", err)
			}
		})
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
