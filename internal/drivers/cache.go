package drivers

import (
	"context"
	"fmt"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// CacheClient is the godo Databases interface used for Redis cache clusters (for mocking).
type CacheClient interface {
	Create(ctx context.Context, req *godo.DatabaseCreateRequest) (*godo.Database, *godo.Response, error)
	Get(ctx context.Context, databaseID string) (*godo.Database, *godo.Response, error)
	Resize(ctx context.Context, databaseID string, req *godo.DatabaseResizeRequest) (*godo.Response, error)
	Delete(ctx context.Context, databaseID string) (*godo.Response, error)
}

// CacheDriver manages DigitalOcean Managed Redis clusters (infra.cache).
// It uses the same Databases API as DatabaseDriver with engine=redis.
type CacheDriver struct {
	client CacheClient
	region string
}

// NewCacheDriver creates a CacheDriver backed by a real godo client.
func NewCacheDriver(c *godo.Client, region string) *CacheDriver {
	return &CacheDriver{client: c.Databases, region: region}
}

// NewCacheDriverWithClient creates a driver with an injected client (for tests).
func NewCacheDriverWithClient(c CacheClient, region string) *CacheDriver {
	return &CacheDriver{client: c, region: region}
}

func (d *CacheDriver) Create(ctx context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	size := strFromConfig(spec.Config, "size", "db-s-1vcpu-1gb")
	version := strFromConfig(spec.Config, "version", "7")
	region := strFromConfig(spec.Config, "region", d.region)
	numNodes, _ := intFromConfig(spec.Config, "num_nodes", 1)

	req := &godo.DatabaseCreateRequest{
		Name:       spec.Name,
		EngineSlug: "redis",
		Version:    version,
		SizeSlug:   size,
		Region:     region,
		NumNodes:   numNodes,
	}

	db, _, err := d.client.Create(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("cache create %q: %w", spec.Name, WrapGodoError(err))
	}
	return cacheOutput(db), nil
}

func (d *CacheDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	db, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("cache read %q: %w", ref.Name, WrapGodoError(err))
	}
	return cacheOutput(db), nil
}

func (d *CacheDriver) Update(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	size := strFromConfig(spec.Config, "size", "db-s-1vcpu-1gb")
	numNodes, _ := intFromConfig(spec.Config, "num_nodes", 1)
	_, err := d.client.Resize(ctx, ref.ProviderID, &godo.DatabaseResizeRequest{
		SizeSlug: size,
		NumNodes: numNodes,
	})
	if err != nil {
		return nil, fmt.Errorf("cache update %q: %w", ref.Name, WrapGodoError(err))
	}
	return d.Read(ctx, ref)
}

func (d *CacheDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	_, err := d.client.Delete(ctx, ref.ProviderID)
	if err != nil {
		return fmt.Errorf("cache delete %q: %w", ref.Name, WrapGodoError(err))
	}
	return nil
}

func (d *CacheDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	if current == nil {
		return &interfaces.DiffResult{NeedsUpdate: true}, nil
	}
	var changes []interfaces.FieldChange
	if sz := strFromConfig(desired.Config, "size", ""); sz != "" {
		if cur, _ := current.Outputs["size"].(string); cur != sz {
			changes = append(changes, interfaces.FieldChange{Path: "size", Old: cur, New: sz})
		}
	}
	return &interfaces.DiffResult{NeedsUpdate: len(changes) > 0, Changes: changes}, nil
}

func (d *CacheDriver) HealthCheck(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	db, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	healthy := db.Status == "online"
	return &interfaces.HealthResult{Healthy: healthy, Message: db.Status}, nil
}

func (d *CacheDriver) Scale(ctx context.Context, ref interfaces.ResourceRef, replicas int) (*interfaces.ResourceOutput, error) {
	db, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("cache scale read %q: %w", ref.Name, WrapGodoError(err))
	}
	_, err = d.client.Resize(ctx, ref.ProviderID, &godo.DatabaseResizeRequest{
		SizeSlug: db.SizeSlug,
		NumNodes: replicas,
	})
	if err != nil {
		return nil, fmt.Errorf("cache scale %q: %w", ref.Name, WrapGodoError(err))
	}
	return d.Read(ctx, ref)
}

func cacheOutput(db *godo.Database) *interfaces.ResourceOutput {
	outputs := map[string]any{
		"engine":  db.EngineSlug,
		"region":  db.RegionSlug,
		"size":    db.SizeSlug,
		"version": db.VersionSlug,
	}
	if db.Connection != nil {
		outputs["host"] = db.Connection.Host
		outputs["port"] = db.Connection.Port
		outputs["uri"] = db.Connection.URI
	}
	return &interfaces.ResourceOutput{
		Name:       db.Name,
		Type:       "infra.cache",
		ProviderID: db.ID,
		Outputs:    outputs,
		Status:     db.Status,
	}
}

func (d *CacheDriver) SensitiveKeys() []string { return nil }
