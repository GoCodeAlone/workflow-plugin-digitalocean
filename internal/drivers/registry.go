package drivers

import (
	"context"
	"fmt"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// RegistryClient is the godo Registry interface (for mocking).
type RegistryClient interface {
	Create(ctx context.Context, req *godo.RegistryCreateRequest) (*godo.Registry, *godo.Response, error)
	Get(ctx context.Context) (*godo.Registry, *godo.Response, error)
	Delete(ctx context.Context) (*godo.Response, error)
}

// RegistryDriver manages the DigitalOcean Container Registry (infra.registry).
// Note: DOCR supports one registry per account; Create is idempotent.
type RegistryDriver struct {
	client RegistryClient
}

// NewRegistryDriver creates a RegistryDriver backed by a real godo client.
func NewRegistryDriver(c *godo.Client) *RegistryDriver {
	return &RegistryDriver{client: c.Registry}
}

// NewRegistryDriverWithClient creates a driver with an injected client (for tests).
func NewRegistryDriverWithClient(c RegistryClient) *RegistryDriver {
	return &RegistryDriver{client: c}
}

func (d *RegistryDriver) Create(ctx context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	tier := strFromConfig(spec.Config, "tier", "starter")
	region := strFromConfig(spec.Config, "region", "nyc3")

	reg, _, err := d.client.Create(ctx, &godo.RegistryCreateRequest{
		Name:                 spec.Name,
		SubscriptionTierSlug: tier,
		Region:               region,
	})
	if err != nil {
		return nil, fmt.Errorf("registry create %q: %w", spec.Name, err)
	}
	return registryOutput(reg, spec.Name), nil
}

func (d *RegistryDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	reg, _, err := d.client.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("registry read %q: %w", ref.Name, err)
	}
	return registryOutput(reg, ref.Name), nil
}

func (d *RegistryDriver) Update(ctx context.Context, _ interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	// DOCR does not support in-place updates; return current state.
	reg, _, err := d.client.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("registry update %q: %w", spec.Name, err)
	}
	return registryOutput(reg, spec.Name), nil
}

func (d *RegistryDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	_, err := d.client.Delete(ctx)
	if err != nil {
		return fmt.Errorf("registry delete %q: %w", ref.Name, err)
	}
	return nil
}

func (d *RegistryDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	if current == nil {
		return &interfaces.DiffResult{NeedsUpdate: true}, nil
	}
	return &interfaces.DiffResult{NeedsUpdate: false}, nil
}

func (d *RegistryDriver) HealthCheck(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	_, _, err := d.client.Get(ctx)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	return &interfaces.HealthResult{Healthy: true}, nil
}

func (d *RegistryDriver) Scale(_ context.Context, _ interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return nil, fmt.Errorf("registry does not support scale operation")
}

func registryOutput(reg *godo.Registry, name string) *interfaces.ResourceOutput {
	return &interfaces.ResourceOutput{
		Name:       name,
		Type:       "infra.registry",
		ProviderID: reg.Name,
		Outputs: map[string]any{
			"name":     reg.Name,
			"endpoint": fmt.Sprintf("registry.digitalocean.com/%s", reg.Name),
			"region":   reg.Region,
		},
		Status: "active",
	}
}
