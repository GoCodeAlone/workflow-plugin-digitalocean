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
//   domain   string            — the zone name (e.g. "example.com")
//   records  []map[string]any  — each: {type, name, data, ttl}
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

	return dnsOutput(dom, spec.Name), nil
}

func (d *DNSDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	domain := ref.ProviderID
	dom, _, err := d.client.Get(ctx, domain)
	if err != nil {
		return nil, fmt.Errorf("dns read %q: %w", ref.Name, WrapGodoError(err))
	}
	return dnsOutput(dom, ref.Name), nil
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
	records, ok := config["records"].([]any)
	if !ok {
		return nil
	}

	existing, _, err := d.client.Records(ctx, domain, nil)
	if err != nil {
		return fmt.Errorf("dns list records %q: %w", domain, WrapGodoError(err))
	}

	existingByName := make(map[string]godo.DomainRecord)
	for _, r := range existing {
		key := strings.ToLower(r.Type) + ":" + r.Name
		existingByName[key] = r
	}

	for _, rec := range records {
		m, _ := rec.(map[string]any)
		if m == nil {
			continue
		}
		rType := strings.ToUpper(strFromConfig(m, "type", "A"))
		rName := strFromConfig(m, "name", "@")
		rData := strFromConfig(m, "data", "")
		rTTL, _ := intFromConfig(m, "ttl", 300)

		editReq := &godo.DomainRecordEditRequest{
			Type: rType,
			Name: rName,
			Data: rData,
			TTL:  rTTL,
		}

		key := strings.ToLower(rType) + ":" + rName
		if existing, found := existingByName[key]; found {
			if _, _, err := d.client.EditRecord(ctx, domain, existing.ID, editReq); err != nil {
				return fmt.Errorf("dns update record %q %s/%s: %w", domain, rType, rName, WrapGodoError(err))
			}
		} else {
			if _, _, err := d.client.CreateRecord(ctx, domain, editReq); err != nil {
				return fmt.Errorf("dns create record %q %s/%s: %w", domain, rType, rName, WrapGodoError(err))
			}
		}
	}
	return nil
}

func dnsOutput(dom *godo.Domain, name string) *interfaces.ResourceOutput {
	return &interfaces.ResourceOutput{
		Name:       name,
		Type:       "infra.dns",
		ProviderID: dom.Name,
		Outputs: map[string]any{
			"domain": dom.Name,
			"ttl":    dom.TTL,
		},
		Status: "active",
	}
}

func (d *DNSDriver) SensitiveKeys() []string { return nil }
