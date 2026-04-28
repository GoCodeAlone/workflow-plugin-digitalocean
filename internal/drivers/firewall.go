package drivers

import (
	"context"
	"fmt"
	"log"
	"reflect"
	"sort"

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

// Diff compares the desired spec against the live firewall recorded on
// `current` to detect in-place reconfiguration. Pre-F7 the body was a stub
// that always returned NeedsUpdate=false — meaning every droplet_ids/tags
// toggle silently no-op'd at plan time. F7 round 2 extends it to compare the
// four canonical fields (`droplet_ids`, `tags`, `inbound_rules`,
// `outbound_rules`).
//
// `droplet_ids` and `tags` use SET semantics: reorder is not a change, since
// DO normalizes membership server-side. Rules use ORDER-SENSITIVE deep-equal
// because rule order is preserved in the API response and may carry user
// intent.
//
// Pre-F7 state without the recorded keys (legacy ResourceOutputs from older
// plugin versions) is treated as having empty fields, which surfaces a Plan
// action on first Diff post-upgrade — the safe over-detect direction.
// (Code-review Finding 1, F7 round 2.)
func (d *FirewallDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	if current == nil {
		return &interfaces.DiffResult{NeedsUpdate: true}, nil
	}
	desiredReq := firewallRequest(desired)
	var changes []interfaces.FieldChange

	curIDs := outputsAsIntSlice(current.Outputs["droplet_ids"])
	if !equalIntSet(desiredReq.DropletIDs, curIDs) {
		changes = append(changes, interfaces.FieldChange{
			Path: "droplet_ids", Old: curIDs, New: desiredReq.DropletIDs,
		})
	}

	curTags := outputsAsStringSlice(current.Outputs["tags"])
	if !equalStringSet(desiredReq.Tags, curTags) {
		changes = append(changes, interfaces.FieldChange{
			Path: "tags", Old: curTags, New: desiredReq.Tags,
		})
	}

	curIn, _ := current.Outputs["inbound_rules"].([]godo.InboundRule)
	if !reflect.DeepEqual(curIn, desiredReq.InboundRules) {
		changes = append(changes, interfaces.FieldChange{
			Path: "inbound_rules", Old: curIn, New: desiredReq.InboundRules,
		})
	}

	curOut, _ := current.Outputs["outbound_rules"].([]godo.OutboundRule)
	if !reflect.DeepEqual(curOut, desiredReq.OutboundRules) {
		changes = append(changes, interfaces.FieldChange{
			Path: "outbound_rules", Old: curOut, New: desiredReq.OutboundRules,
		})
	}

	return &interfaces.DiffResult{NeedsUpdate: len(changes) > 0, Changes: changes}, nil
}

// outputsAsIntSlice tolerantly coerces a stored Outputs value to []int,
// accepting both the in-memory []int that fwOutput writes and the []any of
// numerics that JSON/YAML state round-trips can produce.
func outputsAsIntSlice(v any) []int {
	switch t := v.(type) {
	case []int:
		return append([]int(nil), t...)
	case []any:
		out := make([]int, 0, len(t))
		for _, x := range t {
			switch n := x.(type) {
			case int:
				out = append(out, n)
			case int64:
				out = append(out, int(n))
			case float64:
				out = append(out, int(n))
			}
		}
		return out
	}
	return nil
}

// outputsAsStringSlice is the analogous coercer for []string Outputs.
func outputsAsStringSlice(v any) []string {
	switch t := v.(type) {
	case []string:
		return append([]string(nil), t...)
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// equalIntSet returns true iff a and b contain the same multiset of ints,
// ignoring order. DO normalizes droplet_ids server-side; reorders should
// not produce Plan actions.
func equalIntSet(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	sa := append([]int(nil), a...)
	sb := append([]int(nil), b...)
	sort.Ints(sa)
	sort.Ints(sb)
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}

// equalStringSet is the string analogue of equalIntSet.
func equalStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sa := append([]string(nil), a...)
	sb := append([]string(nil), b...)
	sort.Strings(sa)
	sort.Strings(sb)
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
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
	return &godo.FirewallRequest{
		Name:          spec.Name,
		DropletIDs:    dropletIDsFromConfig(spec.Config),
		Tags:          tagsFromConfig(spec.Config),
		InboundRules:  inboundRulesFromConfig(spec.Config),
		OutboundRules: outboundRulesFromConfig(spec.Config),
	}
}

// inboundRulesFromConfig extracts canonical "inbound_rules" into godo shape.
// Each rule: {protocol, ports, sources: [<CIDR>...]}. Defaults: tcp / all /
// no sources. The Sources struct is always allocated (matching DO API
// convention) so equality comparisons in Diff don't fight nil-vs-empty
// pointer differences.
func inboundRulesFromConfig(cfg map[string]any) []godo.InboundRule {
	raw, ok := cfg["inbound_rules"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]godo.InboundRule, 0, len(raw))
	for _, r := range raw {
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
		out = append(out, rule)
	}
	return out
}

// outboundRulesFromConfig extracts canonical "outbound_rules" into godo
// shape. Mirror of inboundRulesFromConfig but uses Destinations.
func outboundRulesFromConfig(cfg map[string]any) []godo.OutboundRule {
	raw, ok := cfg["outbound_rules"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]godo.OutboundRule, 0, len(raw))
	for _, r := range raw {
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
		out = append(out, rule)
	}
	return out
}

// fwOutput records the firewall's targets (droplet_ids, tags) and rules
// (inbound_rules, outbound_rules) on Outputs so Diff can detect in-place
// reconfiguration. Storing the godo-shape directly keeps the comparison
// symmetric with what `firewallRequest` builds from the desired cfg.
// (F7 round 2 — Diff cascade fix.)
func fwOutput(fw *godo.Firewall) *interfaces.ResourceOutput {
	return &interfaces.ResourceOutput{
		Name:       fw.Name,
		Type:       "infra.firewall",
		ProviderID: fw.ID,
		Outputs: map[string]any{
			"status":         fw.Status,
			"droplet_ids":    append([]int(nil), fw.DropletIDs...),
			"tags":           append([]string(nil), fw.Tags...),
			"inbound_rules":  append([]godo.InboundRule(nil), fw.InboundRules...),
			"outbound_rules": append([]godo.OutboundRule(nil), fw.OutboundRules...),
		},
		Status: fw.Status,
	}
}

func (d *FirewallDriver) SensitiveKeys() []string { return nil }

func (d *FirewallDriver) ProviderIDFormat() interfaces.ProviderIDFormat { return interfaces.IDFormatUUID }

// dropletIDsFromConfig extracts the canonical "droplet_ids" list. Accepts the
// numeric variants the modular YAML loader can emit (int, int64, float64).
// Non-numeric entries and non-positive IDs are silently dropped: Droplet IDs
// are positive integers assigned by the DO API, so 0 / negatives are never
// valid and would only fail at apply time, defeating F7's plan-time-fail
// contract. (Code-review Finding 3, F7 round 2.)
func dropletIDsFromConfig(cfg map[string]any) []int {
	raw, ok := cfg["droplet_ids"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]int, 0, len(raw))
	for _, v := range raw {
		var id int
		switch t := v.(type) {
		case int:
			id = t
		case int64:
			id = int(t)
		case float64:
			id = int(t)
		default:
			continue
		}
		if id <= 0 {
			continue
		}
		out = append(out, id)
	}
	return out
}

// tagsFromConfig extracts the canonical "tags" list of Droplet/DOKS-pool tag
// strings. Non-string entries and empty strings are dropped: the DO API
// rejects empty tags, so a slice that contains only empty strings must fail
// the targets-required validation rather than being silently sent to the
// API. (Code-review Finding 2, F7 round 2.)
func tagsFromConfig(cfg map[string]any) []string {
	raw, ok := cfg["tags"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok && s != "" {
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
