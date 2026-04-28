package drivers

import (
	"context"
	"fmt"
	"log"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// FirewallClient is the godo Firewalls interface (for mocking).
type FirewallClient interface {
	Create(ctx context.Context, req *godo.FirewallRequest) (*godo.Firewall, *godo.Response, error)
	Get(ctx context.Context, fwID string) (*godo.Firewall, *godo.Response, error)
	List(ctx context.Context, opts *godo.ListOptions) ([]godo.Firewall, *godo.Response, error)
	Update(ctx context.Context, fwID string, req *godo.FirewallRequest) (*godo.Firewall, *godo.Response, error)
	Delete(ctx context.Context, fwID string) (*godo.Response, error)
}

// FirewallDriver manages DigitalOcean Firewalls (infra.firewall).
//
// Targets are required: every firewall must declare at least one of
// `droplet_ids` (a list of Droplet integer IDs) or `tags` (a list of
// Droplet/DOKS-pool tag strings, which auto-attach future resources). Both
// Create and Update reject specs with neither field set.
//
// Note: DO firewalls do NOT attach to App Platform apps. For
// App-Platform-only deployments, omit `infra.firewall` and instead use
// `expose: internal` on the service plus `trusted_sources` on managed
// databases.
type FirewallDriver struct {
	client FirewallClient
}

// NewFirewallDriver creates a FirewallDriver backed by a real godo client.
func NewFirewallDriver(c *godo.Client) *FirewallDriver {
	return &FirewallDriver{client: c.Firewalls}
}

// NewFirewallDriverWithClient creates a driver with an injected client (for tests).
func NewFirewallDriverWithClient(c FirewallClient) *FirewallDriver {
	return &FirewallDriver{client: c}
}

func (d *FirewallDriver) Create(ctx context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	req := firewallRequest(spec)
	if err := validateFirewallTargets(spec.Name, req); err != nil {
		return nil, err
	}
	fw, _, err := d.client.Create(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("firewall create %q: %w", spec.Name, WrapGodoError(err))
	}
	if fw == nil || fw.ID == "" {
		return nil, fmt.Errorf("firewall create %q: API returned firewall with empty ID", spec.Name)
	}
	return fwOutput(fw), nil
}

// SupportsUpsert reports that FirewallDriver can locate a resource by name alone
// (empty ProviderID), enabling the ErrResourceAlreadyExists → upsert path.
func (d *FirewallDriver) SupportsUpsert() bool { return true }

func (d *FirewallDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	if ref.ProviderID == "" {
		return d.findFirewallByName(ctx, ref.Name)
	}
	fw, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("firewall read %q: %w", ref.Name, WrapGodoError(err))
	}
	if fw == nil {
		return nil, fmt.Errorf("firewall read %q: provider returned nil resource", ref.Name)
	}
	return fwOutput(fw), nil
}

// findFirewallByName iterates the paginated firewall list and returns the first
// firewall whose Name matches. Returns ErrResourceNotFound if no match is found.
func (d *FirewallDriver) findFirewallByName(ctx context.Context, name string) (*interfaces.ResourceOutput, error) {
	opts := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		fws, resp, err := d.client.List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("firewall list: %w", WrapGodoError(err))
		}
		for i := range fws {
			if fws[i].Name == name {
				return fwOutput(&fws[i]), nil
			}
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opts.Page++
	}
	return nil, fmt.Errorf("firewall %q: %w", name, ErrResourceNotFound)
}

// resolveProviderID returns a UUID-like ProviderID for the given ref. If
// ref.ProviderID is already UUID-shaped it is returned as-is. Otherwise a
// WARN is logged and a name-based lookup heals stale state transparently.
// Mirrors AppPlatformDriver.resolveProviderID (v0.7.8).
func (d *FirewallDriver) resolveProviderID(ctx context.Context, ref interfaces.ResourceRef) (string, error) {
	if isUUIDLike(ref.ProviderID) {
		return ref.ProviderID, nil
	}
	log.Printf("warn: firewall %q: ProviderID %q is not UUID-like; resolving by name (state-heal)",
		ref.Name, ref.ProviderID)
	out, err := d.findFirewallByName(ctx, ref.Name)
	if err != nil {
		return "", fmt.Errorf("firewall state-heal for %q: %w", ref.Name, err)
	}
	return out.ProviderID, nil
}

func (d *FirewallDriver) Update(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	req := firewallRequest(spec)
	if err := validateFirewallTargets(spec.Name, req); err != nil {
		return nil, err
	}
	providerID, err := d.resolveProviderID(ctx, ref)
	if err != nil {
		return nil, err
	}
	fw, _, err := d.client.Update(ctx, providerID, req)
	if err != nil {
		return nil, fmt.Errorf("firewall update %q: %w", ref.Name, WrapGodoError(err))
	}
	return fwOutput(fw), nil
}

func (d *FirewallDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	providerID, err := d.resolveProviderID(ctx, ref)
	if err != nil {
		return err
	}
	_, err = d.client.Delete(ctx, providerID)
	if err != nil {
		return fmt.Errorf("firewall delete %q: %w", ref.Name, WrapGodoError(err))
	}
	return nil
}

func (d *FirewallDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	if current == nil {
		return &interfaces.DiffResult{NeedsUpdate: true}, nil
	}
	return &interfaces.DiffResult{NeedsUpdate: false}, nil
}

func (d *FirewallDriver) HealthCheck(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	providerID, err := d.resolveProviderID(ctx, ref)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	fw, _, err := d.client.Get(ctx, providerID)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	if fw == nil {
		return &interfaces.HealthResult{Healthy: false, Message: "provider returned nil firewall"}, nil
	}
	healthy := fw.Status == "succeeded"
	return &interfaces.HealthResult{Healthy: healthy, Message: fw.Status}, nil
}

func (d *FirewallDriver) Scale(_ context.Context, _ interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return nil, fmt.Errorf("firewall does not support scale operation")
}

// firewallRequest builds a godo FirewallRequest from a ResourceSpec.
// Config keys:
//
//	droplet_ids    []int             — Droplet IDs to attach the firewall to.
//	tags           []string          — Droplet tags (auto-attaches future Droplets / DOKS pools).
//	inbound_rules  []map[string]any  — each: {protocol, ports, sources}
//	outbound_rules []map[string]any  — each: {protocol, ports, destinations}
//
// At least one of `droplet_ids` or `tags` must be set; this is enforced by
// validateFirewallTargets, which Create and Update both call.
func firewallRequest(spec interfaces.ResourceSpec) *godo.FirewallRequest {
	req := &godo.FirewallRequest{
		Name:       spec.Name,
		DropletIDs: dropletIDsFromConfig(spec.Config),
		Tags:       tagsFromConfig(spec.Config),
	}

	if rules, ok := spec.Config["inbound_rules"].([]any); ok {
		for _, r := range rules {
			m, _ := r.(map[string]any)
			if m == nil {
				continue
			}
			rule := godo.InboundRule{
				Protocol:  strFromConfig(m, "protocol", "tcp"),
				PortRange: strFromConfig(m, "ports", "all"),
				Sources:   &godo.Sources{},
			}
			if srcs, ok := m["sources"].([]any); ok {
				for _, s := range srcs {
					if addr, ok := s.(string); ok {
						rule.Sources.Addresses = append(rule.Sources.Addresses, addr)
					}
				}
			}
			req.InboundRules = append(req.InboundRules, rule)
		}
	}

	if rules, ok := spec.Config["outbound_rules"].([]any); ok {
		for _, r := range rules {
			m, _ := r.(map[string]any)
			if m == nil {
				continue
			}
			rule := godo.OutboundRule{
				Protocol:     strFromConfig(m, "protocol", "tcp"),
				PortRange:    strFromConfig(m, "ports", "all"),
				Destinations: &godo.Destinations{},
			}
			if dsts, ok := m["destinations"].([]any); ok {
				for _, s := range dsts {
					if addr, ok := s.(string); ok {
						rule.Destinations.Addresses = append(rule.Destinations.Addresses, addr)
					}
				}
			}
			req.OutboundRules = append(req.OutboundRules, rule)
		}
	}

	return req
}

func fwOutput(fw *godo.Firewall) *interfaces.ResourceOutput {
	return &interfaces.ResourceOutput{
		Name:       fw.Name,
		Type:       "infra.firewall",
		ProviderID: fw.ID,
		Outputs: map[string]any{
			"status": fw.Status,
		},
		Status: fw.Status,
	}
}

func (d *FirewallDriver) SensitiveKeys() []string { return nil }

func (d *FirewallDriver) ProviderIDFormat() interfaces.ProviderIDFormat { return interfaces.IDFormatUUID }

// dropletIDsFromConfig extracts the canonical "droplet_ids" list. Accepts the
// numeric variants the modular YAML loader can emit (int, int64, float64).
// Non-numeric entries are silently dropped, matching how other helpers in
// this package degrade malformed input.
func dropletIDsFromConfig(cfg map[string]any) []int {
	raw, ok := cfg["droplet_ids"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]int, 0, len(raw))
	for _, v := range raw {
		switch t := v.(type) {
		case int:
			out = append(out, t)
		case int64:
			out = append(out, int(t))
		case float64:
			out = append(out, int(t))
		}
	}
	return out
}

// tagsFromConfig extracts the canonical "tags" list of Droplet/DOKS-pool tag
// strings. Non-string entries are dropped.
func tagsFromConfig(cfg map[string]any) []string {
	raw, ok := cfg["tags"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// validateFirewallTargets returns the spec-mandated error when the firewall
// request has no DropletIDs and no Tags. The error string is verbatim from
// plan P-2.F7 step 3 — including the em dash and the App Platform clause —
// because operators search for it and reviewers grep for it.
func validateFirewallTargets(name string, req *godo.FirewallRequest) error {
	if len(req.DropletIDs) == 0 && len(req.Tags) == 0 {
		return fmt.Errorf("firewall %q has no targets (specify droplet_ids or tags) — App Platform services cannot be firewall-protected; use expose: internal or trusted_sources", name)
	}
	return nil
}
