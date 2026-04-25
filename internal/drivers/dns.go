package drivers

import (
	"context"
	"fmt"
	"strings"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// DomainsClient is the godo Domains interface (for mocking).
type DomainsClient interface {
	Create(ctx context.Context, req *godo.DomainCreateRequest) (*godo.Domain, *godo.Response, error)
	Get(ctx context.Context, name string) (*godo.Domain, *godo.Response, error)
	Delete(ctx context.Context, name string) (*godo.Response, error)
	CreateRecord(ctx context.Context, domain string, req *godo.DomainRecordEditRequest) (*godo.DomainRecord, *godo.Response, error)
	EditRecord(ctx context.Context, domain string, id int, req *godo.DomainRecordEditRequest) (*godo.DomainRecord, *godo.Response, error)
	DeleteRecord(ctx context.Context, domain string, id int) (*godo.Response, error)
	Records(ctx context.Context, domain string, opts *godo.ListOptions) ([]godo.DomainRecord, *godo.Response, error)
}

// DNSDriver manages DigitalOcean Domains and DNS records (infra.dns).
// Idempotent: creates domain if missing, creates or updates records.
type DNSDriver struct {
	client DomainsClient
}

// NewDNSDriver creates a DNSDriver backed by a real godo client.
func NewDNSDriver(c *godo.Client) *DNSDriver {
	return &DNSDriver{client: c.Domains}
}

// NewDNSDriverWithClient creates a driver with an injected client (for tests).
func NewDNSDriverWithClient(c DomainsClient) *DNSDriver {
	return &DNSDriver{client: c}
}

// Create creates a domain and any declared DNS records idempotently.
// Config keys:
//
//	domain   string            — the zone name (e.g. "example.com")
//	records  []map[string]any  — each: {type, name, data, ttl}
func (d *DNSDriver) Create(ctx context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	domain := strFromConfig(spec.Config, "domain", spec.Name)

	// Idempotent: create domain only if it doesn't exist.
	dom, _, err := d.client.Get(ctx, domain)
	if err != nil {
		dom, _, err = d.client.Create(ctx, &godo.DomainCreateRequest{Name: domain})
		if err != nil {
			return nil, fmt.Errorf("dns create domain %q: %w", domain, WrapGodoError(err))
		}
	}

	if err := d.upsertRecords(ctx, domain, spec.Config); err != nil {
		return nil, err
	}

	return dnsOutput(dom, spec.Name, nil), nil
}

func (d *DNSDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	domain := ref.ProviderID
	dom, _, err := d.client.Get(ctx, domain)
	if err != nil {
		return nil, fmt.Errorf("dns read %q: %w", ref.Name, WrapGodoError(err))
	}
	records, _, err := d.client.Records(ctx, domain, nil)
	if err != nil {
		return nil, fmt.Errorf("dns list records %q: %w", domain, WrapGodoError(err))
	}
	return dnsOutput(dom, ref.Name, records), nil
}

func (d *DNSDriver) Update(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	domain := strFromConfig(spec.Config, "domain", ref.ProviderID)
	if err := d.upsertRecords(ctx, domain, spec.Config); err != nil {
		return nil, err
	}
	return d.Read(ctx, ref)
}

func (d *DNSDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	_, err := d.client.Delete(ctx, ref.ProviderID)
	if err != nil {
		return fmt.Errorf("dns delete %q: %w", ref.Name, WrapGodoError(err))
	}
	return nil
}

func (d *DNSDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	if current == nil {
		return &interfaces.DiffResult{NeedsUpdate: true}, nil
	}
	return &interfaces.DiffResult{NeedsUpdate: false}, nil
}

func (d *DNSDriver) HealthCheck(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	_, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	return &interfaces.HealthResult{Healthy: true}, nil
}

func (d *DNSDriver) Scale(_ context.Context, _ interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return nil, fmt.Errorf("dns does not support scale operation")
}

// upsertRecords creates or updates DNS records for a domain.
func (d *DNSDriver) upsertRecords(ctx context.Context, domain string, config map[string]any) error {
	records := declaredDNSRecords(config)
	if len(records) == 0 {
		return nil
	}

	existing, _, err := d.client.Records(ctx, domain, nil)
	if err != nil {
		return fmt.Errorf("dns list records %q: %w", domain, WrapGodoError(err))
	}

	existingByKey := make(map[string][]godo.DomainRecord)
	for _, r := range existing {
		key := dnsRecordIdentity(r.Type, r.Name, r.Data)
		existingByKey[key] = append(existingByKey[key], r)
	}

	seenDeclared := make(map[string]struct{})
	for _, editReq := range records {
		key := dnsRecordIdentity(editReq.Type, editReq.Name, editReq.Data)
		if _, seen := seenDeclared[key]; seen {
			continue
		}
		seenDeclared[key] = struct{}{}

		candidates := existingByKey[key]
		if len(candidates) > 0 {
			existing := candidates[0]
			existingByKey[key] = candidates[1:]
			if dnsRecordMatchesRequest(existing, editReq) {
				continue
			}
			if _, _, err := d.client.EditRecord(ctx, domain, existing.ID, &editReq); err != nil {
				return fmt.Errorf("dns update record %q %s/%s: %w", domain, editReq.Type, editReq.Name, WrapGodoError(err))
			}
		} else {
			if _, _, err := d.client.CreateRecord(ctx, domain, &editReq); err != nil {
				return fmt.Errorf("dns create record %q %s/%s: %w", domain, editReq.Type, editReq.Name, WrapGodoError(err))
			}
		}
	}
	return nil
}

func declaredDNSRecords(config map[string]any) []godo.DomainRecordEditRequest {
	raw, ok := config["records"].([]any)
	if !ok {
		return nil
	}
	out := make([]godo.DomainRecordEditRequest, 0, len(raw))
	for _, rec := range raw {
		m, _ := rec.(map[string]any)
		if m == nil {
			continue
		}
		out = append(out, dnsRecordEditRequestFromConfig(m))
	}
	return out
}

func dnsRecordEditRequestFromConfig(m map[string]any) godo.DomainRecordEditRequest {
	ttl, _ := intFromConfig(m, "ttl", 300)
	priority, _ := intFromConfig(m, "priority", 0)
	port, _ := intFromConfig(m, "port", 0)
	weight, _ := intFromConfig(m, "weight", 0)
	flags, _ := intFromConfig(m, "flags", 0)
	return godo.DomainRecordEditRequest{
		Type:     strings.ToUpper(strFromConfig(m, "type", "A")),
		Name:     strFromConfig(m, "name", "@"),
		Data:     strFromConfig(m, "data", ""),
		TTL:      ttl,
		Priority: priority,
		Port:     port,
		Weight:   weight,
		Flags:    flags,
		Tag:      strFromConfig(m, "tag", ""),
	}
}

func dnsRecordIdentity(recordType, name, data string) string {
	return strings.ToUpper(recordType) + "\x00" + strings.ToLower(name) + "\x00" + data
}

func dnsRecordMatchesRequest(record godo.DomainRecord, req godo.DomainRecordEditRequest) bool {
	return strings.EqualFold(record.Type, req.Type) &&
		strings.EqualFold(record.Name, req.Name) &&
		record.Data == req.Data &&
		record.TTL == req.TTL &&
		record.Priority == req.Priority &&
		record.Port == req.Port &&
		record.Weight == req.Weight &&
		record.Flags == req.Flags &&
		record.Tag == req.Tag
}

func dnsOutput(dom *godo.Domain, name string, records []godo.DomainRecord) *interfaces.ResourceOutput {
	outputs := map[string]any{
		"domain": dom.Name,
		"ttl":    dom.TTL,
	}
	if records != nil {
		outputs["records"] = dnsRecordOutputs(records)
	}
	return &interfaces.ResourceOutput{
		Name:       name,
		Type:       "infra.dns",
		ProviderID: dom.Name,
		Outputs:    outputs,
		Status:     "active",
	}
}

func dnsRecordOutputs(records []godo.DomainRecord) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		out = append(out, map[string]any{
			"type":     record.Type,
			"name":     record.Name,
			"data":     record.Data,
			"ttl":      record.TTL,
			"priority": record.Priority,
			"port":     record.Port,
			"weight":   record.Weight,
			"flags":    record.Flags,
			"tag":      record.Tag,
		})
	}
	return out
}

func (d *DNSDriver) SensitiveKeys() []string { return nil }

func (d *DNSDriver) ProviderIDFormat() interfaces.ProviderIDFormat {
	return interfaces.IDFormatDomainName
}
