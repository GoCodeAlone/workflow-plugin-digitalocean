package drivers

import (
	"context"
	"fmt"
	"log"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// VPCClient is the godo VPCs interface (for mocking).
type VPCClient interface {
	Create(ctx context.Context, req *godo.VPCCreateRequest) (*godo.VPC, *godo.Response, error)
	Get(ctx context.Context, vpcID string) (*godo.VPC, *godo.Response, error)
	List(ctx context.Context, opts *godo.ListOptions) ([]*godo.VPC, *godo.Response, error)
	Update(ctx context.Context, vpcID string, req *godo.VPCUpdateRequest) (*godo.VPC, *godo.Response, error)
	Delete(ctx context.Context, vpcID string) (*godo.Response, error)
}

// VPCDriver manages DigitalOcean VPCs (infra.vpc).
type VPCDriver struct {
	client VPCClient
	region string
}

// NewVPCDriver creates a VPCDriver backed by a real godo client.
func NewVPCDriver(c *godo.Client, region string) *VPCDriver {
	return &VPCDriver{client: c.VPCs, region: region}
}

// NewVPCDriverWithClient creates a driver with an injected client (for tests).
func NewVPCDriverWithClient(c VPCClient, region string) *VPCDriver {
	return &VPCDriver{client: c, region: region}
}

func (d *VPCDriver) Create(ctx context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	region := strFromConfig(spec.Config, "region", d.region)
	ipRange := strFromConfig(spec.Config, "ip_range", "10.0.0.0/16")

	req := &godo.VPCCreateRequest{
		Name:        spec.Name,
		RegionSlug:  region,
		IPRange:     ipRange,
	}

	vpc, _, err := d.client.Create(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("vpc create %q: %w", spec.Name, WrapGodoError(err))
	}
	if vpc == nil || vpc.ID == "" {
		return nil, fmt.Errorf("vpc create %q: API returned vpc with empty ID", spec.Name)
	}
	return vpcOutput(vpc), nil
}

// SupportsUpsert reports that VPCDriver can locate a resource by name alone
// (empty ProviderID), enabling the ErrResourceAlreadyExists → upsert path.
func (d *VPCDriver) SupportsUpsert() bool { return true }

func (d *VPCDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	if ref.ProviderID == "" {
		return d.findVPCByName(ctx, ref.Name)
	}
	vpc, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("vpc read %q: %w", ref.Name, WrapGodoError(err))
	}
	return vpcOutput(vpc), nil
}

// findVPCByName iterates the paginated VPC list and returns the first VPC whose
// Name matches. Returns ErrResourceNotFound if no match is found.
func (d *VPCDriver) findVPCByName(ctx context.Context, name string) (*interfaces.ResourceOutput, error) {
	opts := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		vpcs, resp, err := d.client.List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("vpc list: %w", WrapGodoError(err))
		}
		for _, vpc := range vpcs {
			if vpc.Name == name {
				return vpcOutput(vpc), nil
			}
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opts.Page++
	}
	return nil, fmt.Errorf("vpc %q: %w", name, ErrResourceNotFound)
}

// resolveProviderID returns a UUID-like ProviderID for the given ref. If
// ref.ProviderID is already UUID-shaped it is returned as-is. Otherwise a
// WARN is logged and a name-based lookup heals stale state transparently.
// Mirrors AppPlatformDriver.resolveProviderID (v0.7.8).
func (d *VPCDriver) resolveProviderID(ctx context.Context, ref interfaces.ResourceRef) (string, error) {
	if isUUIDLike(ref.ProviderID) {
		return ref.ProviderID, nil
	}
	log.Printf("warn: vpc %q: ProviderID %q is not UUID-like; resolving by name (state-heal)",
		ref.Name, ref.ProviderID)
	out, err := d.findVPCByName(ctx, ref.Name)
	if err != nil {
		return "", fmt.Errorf("vpc state-heal for %q: %w", ref.Name, err)
	}
	return out.ProviderID, nil
}

func (d *VPCDriver) Update(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	providerID, err := d.resolveProviderID(ctx, ref)
	if err != nil {
		return nil, err
	}
	req := &godo.VPCUpdateRequest{
		Name: spec.Name,
	}
	vpc, _, err := d.client.Update(ctx, providerID, req)
	if err != nil {
		return nil, fmt.Errorf("vpc update %q: %w", ref.Name, WrapGodoError(err))
	}
	return vpcOutput(vpc), nil
}

func (d *VPCDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	providerID, err := d.resolveProviderID(ctx, ref)
	if err != nil {
		return err
	}
	_, err = d.client.Delete(ctx, providerID)
	if err != nil {
		return fmt.Errorf("vpc delete %q: %w", ref.Name, WrapGodoError(err))
	}
	return nil
}

func (d *VPCDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	if current == nil {
		return &interfaces.DiffResult{NeedsUpdate: true}, nil
	}
	var changes []interfaces.FieldChange
	if ipRange := strFromConfig(desired.Config, "ip_range", ""); ipRange != "" {
		curRange, _ := current.Outputs["ip_range"].(string)
		if ipRange != curRange {
			changes = append(changes, interfaces.FieldChange{
				Path:     "ip_range",
				Old:      curRange,
				New:      ipRange,
				ForceNew: true,
			})
		}
	}
	return &interfaces.DiffResult{
		NeedsUpdate:  len(changes) > 0,
		NeedsReplace: len(changes) > 0,
		Changes:      changes,
	}, nil
}

func (d *VPCDriver) HealthCheck(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	providerID, err := d.resolveProviderID(ctx, ref)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	_, _, err = d.client.Get(ctx, providerID)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	return &interfaces.HealthResult{Healthy: true}, nil
}

func (d *VPCDriver) Scale(_ context.Context, _ interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return nil, fmt.Errorf("vpc does not support scale operation")
}

func vpcOutput(vpc *godo.VPC) *interfaces.ResourceOutput {
	return &interfaces.ResourceOutput{
		Name:       vpc.Name,
		Type:       "infra.vpc",
		ProviderID: vpc.ID,
		Outputs: map[string]any{
			"ip_range": vpc.IPRange,
			"region":   vpc.RegionSlug,
			"urn":      vpc.URN,
		},
		Status: "active",
	}
}

func (d *VPCDriver) SensitiveKeys() []string { return nil }

func (d *VPCDriver) ProviderIDFormat() interfaces.ProviderIDFormat { return interfaces.IDFormatUUID }
