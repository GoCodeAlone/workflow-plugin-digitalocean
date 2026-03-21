package drivers

import (
	"context"
	"fmt"
	"time"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// SpacesBucket is a minimal representation of a DigitalOcean Spaces bucket.
type SpacesBucket struct {
	Name      string
	Region    string
	CreatedAt time.Time
}

// SpacesBucketClient is the interface for Spaces bucket management (injectable for mocking).
// DigitalOcean Spaces uses an S3-compatible API; this interface abstracts those calls.
type SpacesBucketClient interface {
	CreateBucket(ctx context.Context, name, region string) (*SpacesBucket, error)
	GetBucket(ctx context.Context, name, region string) (*SpacesBucket, error)
	DeleteBucket(ctx context.Context, name, region string) error
}

// SpacesDriver manages DigitalOcean Spaces object storage buckets (infra.storage).
type SpacesDriver struct {
	client SpacesBucketClient
	region string
}

// NewSpacesDriver creates a SpacesDriver. Since godo does not provide Spaces bucket
// management (it uses the S3-compatible API), this driver requires a SpacesBucketClient.
// For production use, wrap an S3-compatible client; for tests, inject a mock.
func NewSpacesDriver(_ *godo.Client, region string) *SpacesDriver {
	return &SpacesDriver{client: &noopSpacesClient{}, region: region}
}

// NewSpacesDriverWithClient creates a driver with an injected client (for tests).
func NewSpacesDriverWithClient(c SpacesBucketClient, region string) *SpacesDriver {
	return &SpacesDriver{client: c, region: region}
}

func (d *SpacesDriver) Create(ctx context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	region := strFromConfig(spec.Config, "region", d.region)
	bucket, err := d.client.CreateBucket(ctx, spec.Name, region)
	if err != nil {
		return nil, fmt.Errorf("spaces create %q: %w", spec.Name, err)
	}
	return spacesOutput(bucket, spec.Name), nil
}

func (d *SpacesDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	bucket, err := d.client.GetBucket(ctx, ref.Name, d.region)
	if err != nil {
		return nil, fmt.Errorf("spaces read %q: %w", ref.Name, err)
	}
	return spacesOutput(bucket, ref.Name), nil
}

func (d *SpacesDriver) Update(_ context.Context, _ interfaces.ResourceRef, _ interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	return nil, fmt.Errorf("spaces: bucket properties are immutable after creation")
}

func (d *SpacesDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	if err := d.client.DeleteBucket(ctx, ref.Name, d.region); err != nil {
		return fmt.Errorf("spaces delete %q: %w", ref.Name, err)
	}
	return nil
}

func (d *SpacesDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	if current == nil {
		return &interfaces.DiffResult{NeedsUpdate: true}, nil
	}
	return &interfaces.DiffResult{NeedsUpdate: false}, nil
}

func (d *SpacesDriver) HealthCheck(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	_, err := d.client.GetBucket(ctx, ref.Name, d.region)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	return &interfaces.HealthResult{Healthy: true}, nil
}

func (d *SpacesDriver) Scale(_ context.Context, _ interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return nil, fmt.Errorf("spaces does not support scale operation")
}

func spacesOutput(b *SpacesBucket, name string) *interfaces.ResourceOutput {
	return &interfaces.ResourceOutput{
		Name:       name,
		Type:       "infra.storage",
		ProviderID: b.Name,
		Outputs: map[string]any{
			"name":     b.Name,
			"region":   b.Region,
			"endpoint": fmt.Sprintf("https://%s.%s.digitaloceanspaces.com", b.Name, b.Region),
		},
		Status: "active",
	}
}

// noopSpacesClient is a no-op implementation used when no real S3 client is configured.
type noopSpacesClient struct{}

func (c *noopSpacesClient) CreateBucket(_ context.Context, name, region string) (*SpacesBucket, error) {
	return &SpacesBucket{Name: name, Region: region, CreatedAt: time.Now()}, nil
}

func (c *noopSpacesClient) GetBucket(_ context.Context, name, region string) (*SpacesBucket, error) {
	return &SpacesBucket{Name: name, Region: region}, nil
}

func (c *noopSpacesClient) DeleteBucket(_ context.Context, _, _ string) error {
	return nil
}
