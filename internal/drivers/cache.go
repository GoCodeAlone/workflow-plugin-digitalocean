package drivers

import (
	"context"
	"fmt"
	"log"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// CacheClient is the godo Databases interface used for Redis cache clusters (for mocking).
type CacheClient interface {
	Create(ctx context.Context, req *godo.DatabaseCreateRequest) (*godo.Database, *godo.Response, error)
	Get(ctx context.Context, databaseID string) (*godo.Database, *godo.Response, error)
	List(ctx context.Context, opts *godo.ListOptions) ([]godo.Database, *godo.Response, error)
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
	if db == nil || db.ID == "" {
		return nil, fmt.Errorf("cache create %q: API returned cache with empty ID", spec.Name)
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

// findCacheByName iterates the paginated database list (Redis engine only)
// and returns the first entry whose Name matches. Returns ErrResourceNotFound
// if no match is found.
func (d *CacheDriver) findCacheByName(ctx context.Context, name string) (*interfaces.ResourceOutput, error) {
	opts := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		dbs, resp, err := d.client.List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("cache list: %w", WrapGodoError(err))
		}
		for i := range dbs {
			if dbs[i].Name == name && dbs[i].EngineSlug == "redis" {
				return cacheOutput(&dbs[i]), nil
			}
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opts.Page++
	}
	return nil, fmt.Errorf("cache %q: %w", name, ErrResourceNotFound)
}

// resolveProviderID returns a UUID-like ProviderID for the given ref. If
// ref.ProviderID is already UUID-shaped it is returned as-is. Otherwise a
// WARN is logged and a name-based lookup heals stale state transparently.
// Mirrors AppPlatformDriver.resolveProviderID (v0.7.8).
func (d *CacheDriver) resolveProviderID(ctx context.Context, ref interfaces.ResourceRef) (string, error) {
	if isUUIDLike(ref.ProviderID) {
		return ref.ProviderID, nil
	}
	log.Printf("warn: cache %q: ProviderID %q is not UUID-like; resolving by name (state-heal)",
		ref.Name, ref.ProviderID)
	out, err := d.findCacheByName(ctx, ref.Name)
	if err != nil {
		return "", fmt.Errorf("cache state-heal for %q: %w", ref.Name, err)
	}
	return out.ProviderID, nil
}

func (d *CacheDriver) Update(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	providerID, err := d.resolveProviderID(ctx, ref)
	if err != nil {
		return nil, err
	}
	size := strFromConfig(spec.Config, "size", "db-s-1vcpu-1gb")
	numNodes, _ := intFromConfig(spec.Config, "num_nodes", 1)
	_, err = d.client.Resize(ctx, providerID, &godo.DatabaseResizeRequest{
		SizeSlug: size,
		NumNodes: numNodes,
	})
	if err != nil {
		return nil, fmt.Errorf("cache update %q: %w", ref.Name, WrapGodoError(err))
	}
	ref.ProviderID = providerID // pass healed ID to Read
	return d.Read(ctx, ref)
}

func (d *CacheDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	providerID, err := d.resolveProviderID(ctx, ref)
	if err != nil {
		return err
	}
	_, err = d.client.Delete(ctx, providerID)
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
	providerID, err := d.resolveProviderID(ctx, ref)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	db, _, err := d.client.Get(ctx, providerID)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	healthy := db.Status == "online"
	return &interfaces.HealthResult{Healthy: healthy, Message: db.Status}, nil
}

func (d *CacheDriver) Scale(ctx context.Context, ref interfaces.ResourceRef, replicas int) (*interfaces.ResourceOutput, error) {
	providerID, err := d.resolveProviderID(ctx, ref)
	if err != nil {
		return nil, err
	}
	db, _, err := d.client.Get(ctx, providerID)
	if err != nil {
		return nil, fmt.Errorf("cache scale read %q: %w", ref.Name, WrapGodoError(err))
	}
	_, err = d.client.Resize(ctx, providerID, &godo.DatabaseResizeRequest{
		SizeSlug: db.SizeSlug,
		NumNodes: replicas,
	})
	if err != nil {
		return nil, fmt.Errorf("cache scale %q: %w", ref.Name, WrapGodoError(err))
	}
	ref.ProviderID = providerID
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

func (d *CacheDriver) ProviderIDFormat() interfaces.ProviderIDFormat { return interfaces.IDFormatUUID }
