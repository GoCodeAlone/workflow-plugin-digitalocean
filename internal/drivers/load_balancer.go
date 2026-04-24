package drivers

import (
	"context"
	"fmt"
	"log"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// LoadBalancerClient is the godo LoadBalancers interface (for mocking).
type LoadBalancerClient interface {
	Create(ctx context.Context, req *godo.LoadBalancerRequest) (*godo.LoadBalancer, *godo.Response, error)
	Get(ctx context.Context, lbID string) (*godo.LoadBalancer, *godo.Response, error)
	List(ctx context.Context, opts *godo.ListOptions) ([]godo.LoadBalancer, *godo.Response, error)
	Update(ctx context.Context, lbID string, req *godo.LoadBalancerRequest) (*godo.LoadBalancer, *godo.Response, error)
	Delete(ctx context.Context, lbID string) (*godo.Response, error)
}

// LoadBalancerDriver manages DigitalOcean Load Balancers (infra.load_balancer).
type LoadBalancerDriver struct {
	client LoadBalancerClient
	region string
}

// NewLoadBalancerDriver creates a LoadBalancerDriver backed by a real godo client.
func NewLoadBalancerDriver(c *godo.Client, region string) *LoadBalancerDriver {
	return &LoadBalancerDriver{client: c.LoadBalancers, region: region}
}

// NewLoadBalancerDriverWithClient creates a driver with an injected client (for tests).
func NewLoadBalancerDriverWithClient(c LoadBalancerClient, region string) *LoadBalancerDriver {
	return &LoadBalancerDriver{client: c, region: region}
}

func (d *LoadBalancerDriver) Create(ctx context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	region := strFromConfig(spec.Config, "region", d.region)
	algorithm := strFromConfig(spec.Config, "algorithm", "round_robin")

	req := &godo.LoadBalancerRequest{
		Name:      spec.Name,
		Region:    region,
		Algorithm: algorithm,
		ForwardingRules: []godo.ForwardingRule{
			{
				EntryProtocol:  "http",
				EntryPort:      80,
				TargetProtocol: "http",
				TargetPort:     80,
			},
		},
	}

	lb, _, err := d.client.Create(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("load balancer create %q: %w", spec.Name, WrapGodoError(err))
	}
	if lb == nil || lb.ID == "" {
		return nil, fmt.Errorf("load balancer create %q: API returned load balancer with empty ID", spec.Name)
	}
	return lbOutput(lb), nil
}

func (d *LoadBalancerDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	lb, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("load balancer read %q: %w", ref.Name, WrapGodoError(err))
	}
	return lbOutput(lb), nil
}

// findLoadBalancerByName iterates the paginated load balancer list and returns
// the first entry whose Name matches. Returns ErrResourceNotFound if no match.
func (d *LoadBalancerDriver) findLoadBalancerByName(ctx context.Context, name string) (*interfaces.ResourceOutput, error) {
	opts := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		lbs, resp, err := d.client.List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("load balancer list: %w", WrapGodoError(err))
		}
		for i := range lbs {
			if lbs[i].Name == name {
				return lbOutput(&lbs[i]), nil
			}
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opts.Page++
	}
	return nil, fmt.Errorf("load_balancer %q: %w", name, ErrResourceNotFound)
}

// resolveProviderID returns a UUID-like ProviderID for the given ref. If
// ref.ProviderID is already UUID-shaped it is returned as-is. Otherwise a
// WARN is logged and a name-based lookup heals stale state transparently.
// Mirrors AppPlatformDriver.resolveProviderID (v0.7.8).
func (d *LoadBalancerDriver) resolveProviderID(ctx context.Context, ref interfaces.ResourceRef) (string, error) {
	if isUUIDLike(ref.ProviderID) {
		return ref.ProviderID, nil
	}
	log.Printf("warn: load_balancer %q: ProviderID %q is not UUID-like; resolving by name (state-heal)",
		ref.Name, ref.ProviderID)
	out, err := d.findLoadBalancerByName(ctx, ref.Name)
	if err != nil {
		return "", fmt.Errorf("load_balancer state-heal for %q: %w", ref.Name, err)
	}
	return out.ProviderID, nil
}

func (d *LoadBalancerDriver) Update(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	providerID, err := d.resolveProviderID(ctx, ref)
	if err != nil {
		return nil, err
	}
	region := strFromConfig(spec.Config, "region", d.region)
	algorithm := strFromConfig(spec.Config, "algorithm", "round_robin")

	req := &godo.LoadBalancerRequest{
		Name:      spec.Name,
		Region:    region,
		Algorithm: algorithm,
		ForwardingRules: []godo.ForwardingRule{
			{
				EntryProtocol:  "http",
				EntryPort:      80,
				TargetProtocol: "http",
				TargetPort:     80,
			},
		},
	}

	lb, _, err := d.client.Update(ctx, providerID, req)
	if err != nil {
		return nil, fmt.Errorf("load balancer update %q: %w", ref.Name, WrapGodoError(err))
	}
	return lbOutput(lb), nil
}

func (d *LoadBalancerDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	providerID, err := d.resolveProviderID(ctx, ref)
	if err != nil {
		return err
	}
	_, err = d.client.Delete(ctx, providerID)
	if err != nil {
		return fmt.Errorf("load balancer delete %q: %w", ref.Name, WrapGodoError(err))
	}
	return nil
}

func (d *LoadBalancerDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	if current == nil {
		return &interfaces.DiffResult{NeedsUpdate: true}, nil
	}
	return &interfaces.DiffResult{NeedsUpdate: false}, nil
}

func (d *LoadBalancerDriver) HealthCheck(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	lb, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	healthy := lb.Status == "active"
	return &interfaces.HealthResult{Healthy: healthy, Message: lb.Status}, nil
}

func (d *LoadBalancerDriver) Scale(_ context.Context, _ interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return nil, fmt.Errorf("load balancer does not support scale operation")
}

func lbOutput(lb *godo.LoadBalancer) *interfaces.ResourceOutput {
	return &interfaces.ResourceOutput{
		Name:       lb.Name,
		Type:       "infra.load_balancer",
		ProviderID: lb.ID,
		Outputs: map[string]any{
			"ip":     lb.IP,
			"status": lb.Status,
		},
		Status: lb.Status,
	}
}

func (d *LoadBalancerDriver) SensitiveKeys() []string { return nil }
