package drivers

import (
	"context"
	"fmt"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// FirewallClient is the godo Firewalls interface (for mocking).
type FirewallClient interface {
	Create(ctx context.Context, req *godo.FirewallRequest) (*godo.Firewall, *godo.Response, error)
	Get(ctx context.Context, fwID string) (*godo.Firewall, *godo.Response, error)
	Update(ctx context.Context, fwID string, req *godo.FirewallRequest) (*godo.Firewall, *godo.Response, error)
	Delete(ctx context.Context, fwID string) (*godo.Response, error)
}

// FirewallDriver manages DigitalOcean Firewalls (infra.firewall).
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
	fw, _, err := d.client.Create(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("firewall create %q: %w", spec.Name, WrapGodoError(err))
	}
	return fwOutput(fw), nil
}

func (d *FirewallDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	fw, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("firewall read %q: %w", ref.Name, WrapGodoError(err))
	}
	return fwOutput(fw), nil
}

func (d *FirewallDriver) Update(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	req := firewallRequest(spec)
	fw, _, err := d.client.Update(ctx, ref.ProviderID, req)
	if err != nil {
		return nil, fmt.Errorf("firewall update %q: %w", ref.Name, WrapGodoError(err))
	}
	return fwOutput(fw), nil
}

func (d *FirewallDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	_, err := d.client.Delete(ctx, ref.ProviderID)
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
	fw, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	healthy := fw.Status == "succeeded"
	return &interfaces.HealthResult{Healthy: healthy, Message: fw.Status}, nil
}

func (d *FirewallDriver) Scale(_ context.Context, _ interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return nil, fmt.Errorf("firewall does not support scale operation")
}

// firewallRequest builds a godo FirewallRequest from a ResourceSpec.
// Config keys:
//   inbound_rules  []map[string]any  — each: {protocol, ports, sources}
//   outbound_rules []map[string]any  — each: {protocol, ports, destinations}
func firewallRequest(spec interfaces.ResourceSpec) *godo.FirewallRequest {
	req := &godo.FirewallRequest{Name: spec.Name}

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
