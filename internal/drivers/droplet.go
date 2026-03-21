package drivers

import (
	"context"
	"fmt"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// DropletsClient is the godo Droplets interface (for mocking).
type DropletsClient interface {
	Create(ctx context.Context, req *godo.DropletCreateRequest) (*godo.Droplet, *godo.Response, error)
	Get(ctx context.Context, dropletID int) (*godo.Droplet, *godo.Response, error)
	Delete(ctx context.Context, dropletID int) (*godo.Response, error)
}

// DropletDriver manages DigitalOcean Droplets (infra.droplet).
type DropletDriver struct {
	client DropletsClient
	region string
}

// NewDropletDriver creates a DropletDriver backed by a real godo client.
func NewDropletDriver(c *godo.Client, region string) *DropletDriver {
	return &DropletDriver{client: c.Droplets, region: region}
}

// NewDropletDriverWithClient creates a driver with an injected client (for tests).
func NewDropletDriverWithClient(c DropletsClient, region string) *DropletDriver {
	return &DropletDriver{client: c, region: region}
}

func (d *DropletDriver) Create(ctx context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	size := strFromConfig(spec.Config, "size", "s-1vcpu-2gb")
	image := strFromConfig(spec.Config, "image", "ubuntu-24-04-x64")
	region := strFromConfig(spec.Config, "region", d.region)

	req := &godo.DropletCreateRequest{
		Name:   spec.Name,
		Region: region,
		Size:   size,
		Image:  godo.DropletCreateImage{Slug: image},
	}

	droplet, _, err := d.client.Create(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("droplet create %q: %w", spec.Name, err)
	}
	return dropletOutput(droplet), nil
}

func (d *DropletDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	id := providerIDToInt(ref.ProviderID)
	droplet, _, err := d.client.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("droplet read %q: %w", ref.Name, err)
	}
	return dropletOutput(droplet), nil
}

func (d *DropletDriver) Update(_ context.Context, _ interfaces.ResourceRef, _ interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	return nil, fmt.Errorf("droplet: use resize action for size changes; delete and recreate for other changes")
}

func (d *DropletDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	id := providerIDToInt(ref.ProviderID)
	_, err := d.client.Delete(ctx, id)
	if err != nil {
		return fmt.Errorf("droplet delete %q: %w", ref.Name, err)
	}
	return nil
}

func (d *DropletDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	if current == nil {
		return &interfaces.DiffResult{NeedsUpdate: true}, nil
	}
	var changes []interfaces.FieldChange
	if sz := strFromConfig(desired.Config, "size", ""); sz != "" {
		if cur, _ := current.Outputs["size"].(string); cur != sz {
			changes = append(changes, interfaces.FieldChange{
				Path:     "size",
				Old:      cur,
				New:      sz,
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

func (d *DropletDriver) HealthCheck(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	id := providerIDToInt(ref.ProviderID)
	droplet, _, err := d.client.Get(ctx, id)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	healthy := droplet.Status == "active"
	return &interfaces.HealthResult{Healthy: healthy, Message: droplet.Status}, nil
}

func (d *DropletDriver) Scale(_ context.Context, _ interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return nil, fmt.Errorf("droplet does not support scale operation")
}

func dropletOutput(droplet *godo.Droplet) *interfaces.ResourceOutput {
	var publicIP string
	if ip, err := droplet.PublicIPv4(); err == nil {
		publicIP = ip
	}
	return &interfaces.ResourceOutput{
		Name:       droplet.Name,
		Type:       "infra.droplet",
		ProviderID: fmt.Sprintf("%d", droplet.ID),
		Outputs: map[string]any{
			"id":        droplet.ID,
			"public_ip": publicIP,
			"size":      droplet.Size.Slug,
			"region":    droplet.Region.Slug,
			"status":    droplet.Status,
		},
		Status: droplet.Status,
	}
}

// providerIDToInt converts a string provider ID to int for godo Droplet API calls.
func providerIDToInt(id string) int {
	var n int
	fmt.Sscanf(id, "%d", &n)
	return n
}
