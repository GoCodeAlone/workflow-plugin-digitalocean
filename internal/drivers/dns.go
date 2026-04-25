package drivers

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
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
//	records  []any|[]map[string]any — each: {type, name, data, ttl}
func (d *DNSDriver) Create(ctx context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	domain, err := dnsDomainFromConfig(spec.Config, spec.Name)
	if err != nil {
		return nil, err
	}
	declaredRecords, err := declaredDNSRecords(spec.Config)
	if err != nil {
		return nil, err
	}

	// Idempotent: create domain only if it doesn't exist.
	dom, _, err := d.client.Get(ctx, domain)
	if err != nil {
		wrapped := WrapGodoError(err)
		if !errors.Is(wrapped, interfaces.ErrResourceNotFound) {
			return nil, fmt.Errorf("dns get domain %q: %w", domain, wrapped)
		}
		dom, _, err = d.client.Create(ctx, &godo.DomainCreateRequest{Name: domain})
		if err != nil {
			return nil, fmt.Errorf("dns create domain %q: %w", domain, WrapGodoError(err))
		}
	}

	if err := d.upsertRecords(ctx, domain, declaredRecords); err != nil {
		return nil, err
	}

	records, err := d.listRecords(ctx, domain)
	if err != nil {
		return nil, err
	}
	return dnsOutput(dom, spec.Name, records), nil
}

func (d *DNSDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	domain := ref.ProviderID
	dom, _, err := d.client.Get(ctx, domain)
	if err != nil {
		return nil, fmt.Errorf("dns read %q: %w", ref.Name, WrapGodoError(err))
	}
	records, err := d.listRecords(ctx, domain)
	if err != nil {
		return nil, err
	}
	return dnsOutput(dom, ref.Name, records), nil
}

func (d *DNSDriver) Update(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	domain, err := dnsDomainFromConfig(spec.Config, ref.ProviderID)
	if err != nil {
		return nil, err
	}
	declaredRecords, err := declaredDNSRecords(spec.Config)
	if err != nil {
		return nil, err
	}
	if ref.ProviderID != "" && !strings.EqualFold(domain, ref.ProviderID) {
		return nil, fmt.Errorf("dns update %q: cannot change domain from %q to %q", ref.Name, ref.ProviderID, domain)
	}
	if err := d.upsertRecords(ctx, domain, declaredRecords); err != nil {
		return nil, err
	}
	return d.Read(ctx, interfaces.ResourceRef{Name: ref.Name, ProviderID: domain})
}

func (d *DNSDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	_, err := d.client.Delete(ctx, ref.ProviderID)
	if err != nil {
		return fmt.Errorf("dns delete %q: %w", ref.Name, WrapGodoError(err))
	}
	return nil
}

func (d *DNSDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	desiredDomain, hasDesiredDomain, err := dnsDomainFromConfigIfPresent(desired.Config)
	if err != nil {
		return nil, err
	}
	desiredRecords, err := declaredDNSRecords(desired.Config)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return &interfaces.DiffResult{NeedsUpdate: true}, nil
	}
	if hasDesiredDomain && !strings.EqualFold(desiredDomain, current.ProviderID) {
		return &interfaces.DiffResult{NeedsUpdate: true}, nil
	}
	if len(desiredRecords) == 0 {
		return &interfaces.DiffResult{NeedsUpdate: false}, nil
	}
	currentRecords, err := dnsRecordsFromOutput(current)
	if err != nil {
		return nil, err
	}
	currentByKey := make(map[string][]godo.DomainRecord)
	for _, record := range currentRecords {
		key := dnsDomainRecordIdentity(record)
		currentByKey[key] = append(currentByKey[key], record)
	}
	for _, desiredRecord := range desiredRecords {
		key := dnsRecordRequestIdentity(desiredRecord)
		candidates := currentByKey[key]
		if len(candidates) == 0 {
			return &interfaces.DiffResult{NeedsUpdate: true}, nil
		}
		currentRecord := candidates[0]
		currentByKey[key] = candidates[1:]
		if !dnsRecordMatchesRequest(currentRecord, desiredRecord) {
			return &interfaces.DiffResult{NeedsUpdate: true}, nil
		}
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
func (d *DNSDriver) upsertRecords(ctx context.Context, domain string, records []godo.DomainRecordEditRequest) error {
	if len(records) == 0 {
		return nil
	}

	existing, err := d.listRecords(ctx, domain)
	if err != nil {
		return err
	}
	if err := validateDNSRecordLiveConflicts(records, existing); err != nil {
		return err
	}

	existingByKey := make(map[string][]godo.DomainRecord)
	for _, r := range existing {
		key := dnsDomainRecordIdentity(r)
		existingByKey[key] = append(existingByKey[key], r)
	}

	seenDeclared := make(map[string]struct{})
	for _, editReq := range records {
		key := dnsRecordRequestIdentity(editReq)
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

func (d *DNSDriver) listRecords(ctx context.Context, domain string) ([]godo.DomainRecord, error) {
	opts := &godo.ListOptions{Page: 1, PerPage: 200}
	var out []godo.DomainRecord
	for {
		records, resp, err := d.client.Records(ctx, domain, opts)
		if err != nil {
			return nil, fmt.Errorf("dns list records %q: %w", domain, WrapGodoError(err))
		}
		out = append(out, records...)
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opts.Page++
	}
	return out, nil
}

func dnsDomainFromConfig(config map[string]any, fallback string) (string, error) {
	domain, ok, err := dnsDomainFromConfigIfPresent(config)
	if err != nil {
		return "", err
	}
	if !ok {
		domain = fallback
	}
	if domain == "" {
		return "", fmt.Errorf("dns domain is required")
	}
	if !isDigitalOceanManagedDomainName(domain) {
		return "", fmt.Errorf("dns domain %q is not a valid domain name", domain)
	}
	return domain, nil
}

func dnsDomainFromConfigIfPresent(config map[string]any) (string, bool, error) {
	value, ok := config["domain"]
	if !ok {
		return "", false, nil
	}
	domain, ok := value.(string)
	if !ok {
		return "", true, fmt.Errorf("dns domain must be a string")
	}
	if domain == "" {
		return "", true, fmt.Errorf("dns domain must not be empty")
	}
	if !isDigitalOceanManagedDomainName(domain) {
		return "", true, fmt.Errorf("dns domain %q is not a valid domain name", domain)
	}
	return domain, true, nil
}

func isDigitalOceanManagedDomainName(domain string) bool {
	return !strings.HasSuffix(domain, ".") &&
		strings.Contains(domain, ".") &&
		interfaces.ValidateProviderID(domain, interfaces.IDFormatDomainName)
}

func declaredDNSRecords(config map[string]any) ([]godo.DomainRecordEditRequest, error) {
	rawValue, ok := config["records"]
	if !ok {
		return nil, nil
	}
	raw, err := dnsRecordConfigMaps(rawValue)
	if err != nil {
		return nil, err
	}
	out := make([]godo.DomainRecordEditRequest, 0, len(raw))
	seen := make(map[string]godo.DomainRecordEditRequest)
	recordsByName := make(map[string][]godo.DomainRecordEditRequest)
	for i, m := range raw {
		req, err := dnsRecordEditRequestFromConfig(i, m)
		if err != nil {
			return nil, err
		}
		key := dnsRecordRequestIdentity(req)
		if existing, exists := seen[key]; exists {
			if !dnsRecordRequestsEqual(existing, req) {
				return nil, fmt.Errorf("dns conflicting duplicate DNS record %s/%s data %q", req.Type, req.Name, req.Data)
			}
			continue
		}
		if err := validateDNSRecordNameConflict(req, recordsByName[strings.ToLower(req.Name)]); err != nil {
			return nil, err
		}
		seen[key] = req
		recordsByName[strings.ToLower(req.Name)] = append(recordsByName[strings.ToLower(req.Name)], req)
		out = append(out, req)
	}
	return out, nil
}

func dnsRecordConfigMaps(rawValue any) ([]map[string]any, error) {
	switch raw := rawValue.(type) {
	case []map[string]any:
		return raw, nil
	case []any:
		maps := make([]map[string]any, 0, len(raw))
		for i, rec := range raw {
			m, ok := rec.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("dns records[%d] must be an object", i)
			}
			maps = append(maps, m)
		}
		return maps, nil
	default:
		return nil, fmt.Errorf("dns records must be a list")
	}
}

func validateDNSRecordNameConflict(req godo.DomainRecordEditRequest, existing []godo.DomainRecordEditRequest) error {
	for _, other := range existing {
		if req.Type == "CNAME" || other.Type == "CNAME" {
			return fmt.Errorf("dns conflicting DNS record %s/%s conflicts with %s/%s", req.Type, req.Name, other.Type, other.Name)
		}
	}
	return nil
}

func validateDNSRecordLiveConflicts(records []godo.DomainRecordEditRequest, existing []godo.DomainRecord) error {
	existingByName := make(map[string][]godo.DomainRecord)
	for _, record := range existing {
		existingByName[strings.ToLower(record.Name)] = append(existingByName[strings.ToLower(record.Name)], record)
	}
	for _, req := range records {
		for _, other := range existingByName[strings.ToLower(req.Name)] {
			if req.Type != "CNAME" && !strings.EqualFold(other.Type, "CNAME") {
				continue
			}
			if req.Type == "CNAME" && strings.EqualFold(other.Type, "CNAME") && other.Data == req.Data {
				continue
			}
			return fmt.Errorf("dns conflicting DNS record %s/%s conflicts with %s/%s", req.Type, req.Name, other.Type, other.Name)
		}
	}
	return nil
}

func dnsRecordEditRequestFromConfig(index int, m map[string]any) (godo.DomainRecordEditRequest, error) {
	recordType, err := dnsOptionalStringField(index, m, "type", "A")
	if err != nil {
		return godo.DomainRecordEditRequest{}, err
	}
	recordType = strings.ToUpper(recordType)
	if !isSupportedDNSRecordType(recordType) {
		return godo.DomainRecordEditRequest{}, fmt.Errorf("dns records[%d].type %q is not supported", index, recordType)
	}
	name, err := dnsOptionalStringField(index, m, "name", "@")
	if err != nil {
		return godo.DomainRecordEditRequest{}, err
	}
	data, err := dnsRequiredStringField(index, m, "data")
	if err != nil {
		return godo.DomainRecordEditRequest{}, err
	}
	tag, err := dnsOptionalStringField(index, m, "tag", "")
	if err != nil {
		return godo.DomainRecordEditRequest{}, err
	}
	ttl, _, err := dnsOptionalIntField(index, m, "ttl", 300, true)
	if err != nil {
		return godo.DomainRecordEditRequest{}, err
	}
	priority, hasPriority, err := dnsOptionalIntField(index, m, "priority", 0, false)
	if err != nil {
		return godo.DomainRecordEditRequest{}, err
	}
	port, hasPort, err := dnsOptionalIntField(index, m, "port", 0, false)
	if err != nil {
		return godo.DomainRecordEditRequest{}, err
	}
	weight, hasWeight, err := dnsOptionalIntField(index, m, "weight", 0, false)
	if err != nil {
		return godo.DomainRecordEditRequest{}, err
	}
	flags, _, err := dnsOptionalIntField(index, m, "flags", 0, false)
	if err != nil {
		return godo.DomainRecordEditRequest{}, err
	}
	req := godo.DomainRecordEditRequest{
		Type:     recordType,
		Name:     name,
		Data:     data,
		TTL:      ttl,
		Priority: priority,
		Port:     port,
		Weight:   weight,
		Flags:    flags,
		Tag:      tag,
	}
	if err := validateDNSRecordRequest(index, req, hasPriority, hasPort, hasWeight); err != nil {
		return godo.DomainRecordEditRequest{}, err
	}
	return req, nil
}

func dnsOptionalStringField(index int, m map[string]any, key, defaultValue string) (string, error) {
	value, ok := m[key]
	if !ok {
		return defaultValue, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("dns records[%d].%s must be a string", index, key)
	}
	if text == "" && defaultValue != "" {
		return "", fmt.Errorf("dns records[%d].%s must not be empty", index, key)
	}
	return text, nil
}

func dnsRequiredStringField(index int, m map[string]any, key string) (string, error) {
	value, ok := m[key]
	if !ok {
		return "", fmt.Errorf("dns records[%d].%s is required", index, key)
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("dns records[%d].%s must be a string", index, key)
	}
	if text == "" {
		return "", fmt.Errorf("dns records[%d].%s is required", index, key)
	}
	return text, nil
}

func dnsOptionalIntField(index int, m map[string]any, key string, defaultValue int, positive bool) (int, bool, error) {
	value, ok := m[key]
	if !ok {
		return defaultValue, false, nil
	}
	var out int
	switch typed := value.(type) {
	case int:
		out = typed
	case int64:
		out = int(typed)
	case float64:
		out = int(typed)
		if typed != float64(out) {
			return 0, true, fmt.Errorf("dns records[%d].%s must be an integer", index, key)
		}
	default:
		return 0, true, fmt.Errorf("dns records[%d].%s must be an integer", index, key)
	}
	if positive && out <= 0 {
		return 0, true, fmt.Errorf("dns records[%d].%s must be positive", index, key)
	}
	if !positive && out < 0 {
		return 0, true, fmt.Errorf("dns records[%d].%s must be non-negative", index, key)
	}
	return out, true, nil
}

func validateDNSRecordRequest(index int, req godo.DomainRecordEditRequest, hasPriority, hasPort, hasWeight bool) error {
	switch req.Type {
	case "A":
		addr, err := netip.ParseAddr(req.Data)
		if err != nil || !addr.Is4() {
			return fmt.Errorf("dns records[%d].data must be an IPv4 address", index)
		}
	case "AAAA":
		addr, err := netip.ParseAddr(req.Data)
		if err != nil || !addr.Is6() {
			return fmt.Errorf("dns records[%d].data must be an IPv6 address", index)
		}
	case "CNAME":
		if _, err := netip.ParseAddr(req.Data); err == nil {
			return fmt.Errorf("dns records[%d].data must be a hostname for CNAME records", index)
		}
	case "MX":
		if !hasPriority {
			return fmt.Errorf("dns records[%d].priority is required for MX records", index)
		}
		if err := validateDNSUint16(index, "priority", req.Priority); err != nil {
			return err
		}
	case "SRV":
		if !hasPriority {
			return fmt.Errorf("dns records[%d].priority is required for SRV records", index)
		}
		if !hasPort {
			return fmt.Errorf("dns records[%d].port is required for SRV records", index)
		}
		if !hasWeight {
			return fmt.Errorf("dns records[%d].weight is required for SRV records", index)
		}
		if err := validateDNSUint16(index, "priority", req.Priority); err != nil {
			return err
		}
		if req.Port <= 0 || req.Port > 65535 {
			return fmt.Errorf("dns records[%d].port must be between 1 and 65535", index)
		}
		if err := validateDNSUint16(index, "weight", req.Weight); err != nil {
			return err
		}
	case "CAA":
		if req.Tag == "" {
			return fmt.Errorf("dns records[%d].tag is required for CAA records", index)
		}
		if strings.ContainsAny(req.Tag, " \t\r\n") {
			return fmt.Errorf("dns records[%d].tag must not contain whitespace", index)
		}
		if req.Flags < 0 || req.Flags > 255 {
			return fmt.Errorf("dns records[%d].flags must be between 0 and 255", index)
		}
	}
	return nil
}

func validateDNSUint16(index int, field string, value int) error {
	if value < 0 || value > 65535 {
		return fmt.Errorf("dns records[%d].%s must be between 0 and 65535", index, field)
	}
	return nil
}

func isSupportedDNSRecordType(recordType string) bool {
	switch recordType {
	case "A", "AAAA", "CAA", "CNAME", "MX", "NS", "SRV", "TXT":
		return true
	default:
		return false
	}
}

func dnsRecordRequestsEqual(a, b godo.DomainRecordEditRequest) bool {
	return strings.EqualFold(a.Type, b.Type) &&
		strings.EqualFold(a.Name, b.Name) &&
		a.Data == b.Data &&
		a.TTL == b.TTL &&
		a.Priority == b.Priority &&
		a.Port == b.Port &&
		a.Weight == b.Weight &&
		a.Flags == b.Flags &&
		a.Tag == b.Tag
}

func dnsRecordsFromOutput(current *interfaces.ResourceOutput) ([]godo.DomainRecord, error) {
	if current.Outputs == nil {
		return nil, nil
	}
	raw, ok := current.Outputs["records"]
	if !ok || raw == nil {
		return nil, nil
	}
	var maps []map[string]any
	switch typed := raw.(type) {
	case []map[string]any:
		maps = typed
	case []any:
		maps = make([]map[string]any, 0, len(typed))
		for i, item := range typed {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("dns outputs.records[%d] must be an object", i)
			}
			maps = append(maps, m)
		}
	default:
		return nil, fmt.Errorf("dns outputs.records must be a list")
	}
	records := make([]godo.DomainRecord, 0, len(maps))
	for i, m := range maps {
		req, err := dnsRecordEditRequestFromConfig(i, m)
		if err != nil {
			return nil, err
		}
		records = append(records, dnsDomainRecordFromRequest(req))
	}
	return records, nil
}

func dnsDomainRecordFromRequest(req godo.DomainRecordEditRequest) godo.DomainRecord {
	return godo.DomainRecord{
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

func dnsRecordRequestIdentity(req godo.DomainRecordEditRequest) string {
	return dnsRecordIdentity(req.Type, req.Name, req.Data, req.Priority, req.Port, req.Weight, req.Flags, req.Tag)
}

func dnsDomainRecordIdentity(record godo.DomainRecord) string {
	return dnsRecordIdentity(record.Type, record.Name, record.Data, record.Priority, record.Port, record.Weight, record.Flags, record.Tag)
}

func dnsRecordIdentity(recordType, name, data string, priority, port, weight, flags int, tag string) string {
	parts := []string{strings.ToUpper(recordType), strings.ToLower(name), data}
	switch strings.ToUpper(recordType) {
	case "CAA":
		parts = append(parts, fmt.Sprint(flags), strings.ToLower(tag))
	case "MX":
		parts = append(parts, fmt.Sprint(priority))
	case "SRV":
		parts = append(parts, fmt.Sprint(priority), fmt.Sprint(port), fmt.Sprint(weight))
	}
	return strings.Join(parts, "\x00")
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
