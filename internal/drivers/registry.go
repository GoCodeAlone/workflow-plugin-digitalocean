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
	// ListRepositoryTags lists tags for a repository under the named registry.
	// Used by AppPlatformDriver image-presence pre-flight.
	ListRepositoryTags(ctx context.Context, registry, repository string, opts *godo.ListOptions) ([]*godo.RepositoryTag, *godo.Response, error)
}

// RegistryDriver manages the DigitalOcean Container Registry (infra.registry).
//
// DOCR is account-level: Basic plan supports one registry per account,
// Professional supports up to ten. A registry can host many repositories,
// so multi-project consolidation under one registry is supported on every
// plan tier.
//
// This driver is **account-singleton**: godo's Registry.Get(ctx) and
// Delete(ctx) take no name parameter, so the driver can only see and
// manage one registry per account regardless of plan tier. On Professional
// accounts with multiple registries, the others must be managed out-of-band.
//
// Two deployment topologies are supported:
//   - Owned: declare an infra.registry IaC module; this driver manages
//     create/update/destroy. Use on single-project DO accounts.
//   - Shared: omit the module declaration and reference only the path
//     under ci.registries; bootstrap the registry once out-of-band. Use
//     when multiple projects share a DO account.
//
// See docs/container-registry.md for the canonical pattern, including
// migration guidance and the deploy-time verification snippet for Shared
// consumers. Picking the wrong topology produces a peer-deploy race that
// only surfaces when the second project tries to create the registry.
//
// Create is idempotent against the DO API for same name + region. Update
// is a no-op (DOCR does not support in-place tier/region changes; drift
// is not reconciled).
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
		return nil, fmt.Errorf("registry create %q: %w", spec.Name, WrapGodoError(err))
	}
	return registryOutput(reg, spec.Name), nil
}

func (d *RegistryDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	reg, _, err := d.client.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("registry read %q: %w", ref.Name, WrapGodoError(err))
	}
	return registryOutput(reg, ref.Name), nil
}

func (d *RegistryDriver) Update(ctx context.Context, _ interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	// DOCR does not support in-place updates; return current state.
	reg, _, err := d.client.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("registry update %q: %w", spec.Name, WrapGodoError(err))
	}
	return registryOutput(reg, spec.Name), nil
}

func (d *RegistryDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	_, err := d.client.Delete(ctx)
	if err != nil {
		return fmt.Errorf("registry delete %q: %w", ref.Name, WrapGodoError(err))
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

func (d *RegistryDriver) SensitiveKeys() []string { return nil }

func (d *RegistryDriver) ProviderIDFormat() interfaces.ProviderIDFormat { return interfaces.IDFormatFreeform }
