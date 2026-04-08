package drivers

import (
	"context"
	"fmt"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// DatabaseClient is the godo Databases interface (for mocking).
type DatabaseClient interface {
	Create(ctx context.Context, req *godo.DatabaseCreateRequest) (*godo.Database, *godo.Response, error)
	Get(ctx context.Context, databaseID string) (*godo.Database, *godo.Response, error)
	Resize(ctx context.Context, databaseID string, req *godo.DatabaseResizeRequest) (*godo.Response, error)
	Delete(ctx context.Context, databaseID string) (*godo.Response, error)
}

// DatabaseDriver manages DigitalOcean Managed Databases (infra.database).
// Supports pg, mysql, redis, mongodb, kafka.
type DatabaseDriver struct {
	client DatabaseClient
	region string
}

// NewDatabaseDriver creates a DatabaseDriver backed by a real godo client.
func NewDatabaseDriver(c *godo.Client, region string) *DatabaseDriver {
	return &DatabaseDriver{client: c.Databases, region: region}
}

// NewDatabaseDriverWithClient creates a driver with an injected client (for tests).
func NewDatabaseDriverWithClient(c DatabaseClient, region string) *DatabaseDriver {
	return &DatabaseDriver{client: c, region: region}
}

func (d *DatabaseDriver) Create(ctx context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	engine := strFromConfig(spec.Config, "engine", "pg")
	version := strFromConfig(spec.Config, "version", "")
	size := strFromConfig(spec.Config, "size", "db-s-1vcpu-1gb")
	region := strFromConfig(spec.Config, "region", d.region)
	numNodes, _ := intFromConfig(spec.Config, "num_nodes", 1)

	req := &godo.DatabaseCreateRequest{
		Name:       spec.Name,
		EngineSlug: engine,
		Version:    version,
		SizeSlug:   size,
		Region:     region,
		NumNodes:   numNodes,
	}

	db, _, err := d.client.Create(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("database create %q: %w", spec.Name, err)
	}
	return dbOutput(db), nil
}

func (d *DatabaseDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	db, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("database read %q: %w", ref.Name, err)
	}
	return dbOutput(db), nil
}

func (d *DatabaseDriver) Update(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	size := strFromConfig(spec.Config, "size", "db-s-1vcpu-1gb")
	numNodes, _ := intFromConfig(spec.Config, "num_nodes", 1)
	_, err := d.client.Resize(ctx, ref.ProviderID, &godo.DatabaseResizeRequest{
		SizeSlug: size,
		NumNodes: numNodes,
	})
	if err != nil {
		return nil, fmt.Errorf("database update %q: %w", ref.Name, err)
	}
	return d.Read(ctx, ref)
}

func (d *DatabaseDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	_, err := d.client.Delete(ctx, ref.ProviderID)
	if err != nil {
		return fmt.Errorf("database delete %q: %w", ref.Name, err)
	}
	return nil
}

func (d *DatabaseDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
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

func (d *DatabaseDriver) HealthCheck(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	db, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	healthy := db.Status == "online"
	return &interfaces.HealthResult{Healthy: healthy, Message: db.Status}, nil
}

func (d *DatabaseDriver) Scale(ctx context.Context, ref interfaces.ResourceRef, replicas int) (*interfaces.ResourceOutput, error) {
	db, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("database scale read %q: %w", ref.Name, err)
	}
	_, err = d.client.Resize(ctx, ref.ProviderID, &godo.DatabaseResizeRequest{
		SizeSlug: db.SizeSlug,
		NumNodes: replicas,
	})
	if err != nil {
		return nil, fmt.Errorf("database scale %q: %w", ref.Name, err)
	}
	return d.Read(ctx, ref)
}

func (d *DatabaseDriver) SensitiveKeys() []string {
	return []string{"uri", "password", "user"}
}

func dbOutput(db *godo.Database) *interfaces.ResourceOutput {
	outputs := map[string]any{
		"engine":   db.EngineSlug,
		"region":   db.RegionSlug,
		"size":     db.SizeSlug,
		"version":  db.VersionSlug,
	}
	if db.Connection != nil {
		outputs["host"] = db.Connection.Host
		outputs["port"] = db.Connection.Port
		outputs["database"] = db.Connection.Database
		outputs["user"] = db.Connection.User
		outputs["uri"] = db.Connection.URI
	}
	return &interfaces.ResourceOutput{
		Name:       db.Name,
		Type:       "infra.database",
		ProviderID: db.ID,
		Outputs:    outputs,
		Status:     db.Status,
	}
}
