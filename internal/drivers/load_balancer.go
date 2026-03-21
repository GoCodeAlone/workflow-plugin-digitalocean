package drivers

import (
	"context"
	"fmt"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// LoadBalancerClient is the godo LoadBalancers interface (for mocking).
type LoadBalancerClient interface {
	Create(ctx context.Context, req *godo.LoadBalancerRequest) (*godo.LoadBalancer, *godo.Response, error)
	Get(ctx context.Context, lbID string) (*godo.LoadBalancer, *godo.Response, error)
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
		return nil, fmt.Errorf("load balancer create %q: %w", spec.Name, err)
	}
	return lbOutput(lb), nil
}

func (d *LoadBalancerDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	lb, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("load balancer read %q: %w", ref.Name, err)
	}
	return lbOutput(lb), nil
}

func (d *LoadBalancerDriver) Update(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
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

	lb, _, err := d.client.Update(ctx, ref.ProviderID, req)
	if err != nil {
		return nil, fmt.Errorf("load balancer update %q: %w", ref.Name, err)
	}
	return lbOutput(lb), nil
}

func (d *LoadBalancerDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	_, err := d.client.Delete(ctx, ref.ProviderID)
	if err != nil {
		return fmt.Errorf("load balancer delete %q: %w", ref.Name, err)
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
