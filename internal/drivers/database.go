package drivers

import (
	"context"
	"fmt"
	"log"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// DatabaseClient is the godo Databases interface (for mocking).
type DatabaseClient interface {
	Create(ctx context.Context, req *godo.DatabaseCreateRequest) (*godo.Database, *godo.Response, error)
	Get(ctx context.Context, databaseID string) (*godo.Database, *godo.Response, error)
	List(ctx context.Context, opts *godo.ListOptions) ([]godo.Database, *godo.Response, error)
	Resize(ctx context.Context, databaseID string, req *godo.DatabaseResizeRequest) (*godo.Response, error)
	Delete(ctx context.Context, databaseID string) (*godo.Response, error)
	UpdateFirewallRules(ctx context.Context, databaseID string, req *godo.DatabaseUpdateFirewallRulesRequest) (*godo.Response, error)
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
		Rules:      trustedSourceCreateRulesFromConfig(spec.Config),
	}

	db, _, err := d.client.Create(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("database create %q: %w", spec.Name, WrapGodoError(err))
	}
	if db == nil || db.ID == "" {
		return nil, fmt.Errorf("database create %q: API returned database with empty ID", spec.Name)
	}
	return dbOutput(db), nil
}

// SupportsUpsert reports that DatabaseDriver can locate a resource by name alone
// (empty ProviderID), enabling the ErrResourceAlreadyExists → upsert path.
func (d *DatabaseDriver) SupportsUpsert() bool { return true }

func (d *DatabaseDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	if ref.ProviderID == "" {
		return d.findDatabaseByName(ctx, ref.Name)
	}
	db, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("database read %q: %w", ref.Name, WrapGodoError(err))
	}
	return dbOutput(db), nil
}

// findDatabaseByName iterates the paginated database list and returns the first
// database whose Name matches. Returns ErrResourceNotFound if no match is found.
func (d *DatabaseDriver) findDatabaseByName(ctx context.Context, name string) (*interfaces.ResourceOutput, error) {
	opts := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		dbs, resp, err := d.client.List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("database list: %w", WrapGodoError(err))
		}
		for i := range dbs {
			if dbs[i].Name == name {
				return dbOutput(&dbs[i]), nil
			}
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opts.Page++
	}
	return nil, fmt.Errorf("database %q: %w", name, ErrResourceNotFound)
}

// resolveProviderID returns a UUID-like ProviderID for the given ref. If
// ref.ProviderID is already UUID-shaped it is returned as-is. Otherwise a
// WARN is logged and a name-based lookup heals stale state transparently.
// Mirrors AppPlatformDriver.resolveProviderID (v0.7.8).
func (d *DatabaseDriver) resolveProviderID(ctx context.Context, ref interfaces.ResourceRef) (string, error) {
	if isUUIDLike(ref.ProviderID) {
		return ref.ProviderID, nil
	}
	log.Printf("warn: database %q: ProviderID %q is not UUID-like; resolving by name (state-heal)",
		ref.Name, ref.ProviderID)
	out, err := d.findDatabaseByName(ctx, ref.Name)
	if err != nil {
		return "", fmt.Errorf("database state-heal for %q: %w", ref.Name, err)
	}
	return out.ProviderID, nil
}

func (d *DatabaseDriver) Update(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
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
		return nil, fmt.Errorf("database update %q: %w", ref.Name, WrapGodoError(err))
	}
	// Sync firewall rules when trusted_sources key is present in config.
	// "present but empty" clears all rules; absent key leaves existing rules unchanged.
	if rules, ok := trustedSourceFirewallRulesFromConfig(spec.Config); ok {
		_, err = d.client.UpdateFirewallRules(ctx, providerID, &godo.DatabaseUpdateFirewallRulesRequest{
			Rules: rules,
		})
		if err != nil {
			return nil, fmt.Errorf("database update firewall %q: %w", ref.Name, WrapGodoError(err))
		}
	}
	ref.ProviderID = providerID // pass healed ID to Read
	return d.Read(ctx, ref)
}

func (d *DatabaseDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	providerID, err := d.resolveProviderID(ctx, ref)
	if err != nil {
		return err
	}
	_, err = d.client.Delete(ctx, providerID)
	if err != nil {
		return fmt.Errorf("database delete %q: %w", ref.Name, WrapGodoError(err))
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
		return nil, fmt.Errorf("database scale read %q: %w", ref.Name, WrapGodoError(err))
	}
	_, err = d.client.Resize(ctx, ref.ProviderID, &godo.DatabaseResizeRequest{
		SizeSlug: db.SizeSlug,
		NumNodes: replicas,
	})
	if err != nil {
		return nil, fmt.Errorf("database scale %q: %w", ref.Name, WrapGodoError(err))
	}
	return d.Read(ctx, ref)
}

func (d *DatabaseDriver) SensitiveKeys() []string {
	return []string{"uri", "password", "user"}
}

// trustedSourceCreateRulesFromConfig converts canonical "trusted_sources" to
// []*godo.DatabaseCreateFirewallRule for use in a DatabaseCreateRequest.
// Each entry must have "type" (ip_addr|k8s|app|droplet|tag) and "value".
func trustedSourceCreateRulesFromConfig(cfg map[string]any) []*godo.DatabaseCreateFirewallRule {
	raw, ok := cfg["trusted_sources"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	rules := make([]*godo.DatabaseCreateFirewallRule, 0, len(raw))
	for _, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		ruleType := strFromConfig(m, "type", "")
		ruleValue := strFromConfig(m, "value", "")
		if ruleType == "" || ruleValue == "" {
			continue
		}
		rules = append(rules, &godo.DatabaseCreateFirewallRule{
			Type:  ruleType,
			Value: ruleValue,
		})
	}
	return rules
}

// trustedSourceFirewallRulesFromConfig converts canonical "trusted_sources" to
// ([]*godo.DatabaseFirewallRule, bool) for use in a DatabaseUpdateFirewallRulesRequest.
// The bool indicates whether the "trusted_sources" key was present in config at all:
//   - false → key absent → caller should leave existing firewall rules unchanged.
//   - true, empty slice → key present but empty → caller should clear all rules.
//   - true, non-empty slice → key present with rules → caller should apply the rules.
func trustedSourceFirewallRulesFromConfig(cfg map[string]any) ([]*godo.DatabaseFirewallRule, bool) {
	raw, ok := cfg["trusted_sources"]
	if !ok {
		return nil, false // key absent: leave existing rules unchanged
	}
	list, _ := raw.([]any)
	rules := make([]*godo.DatabaseFirewallRule, 0, len(list))
	for _, v := range list {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		ruleType := strFromConfig(m, "type", "")
		ruleValue := strFromConfig(m, "value", "")
		if ruleType == "" || ruleValue == "" {
			continue
		}
		rules = append(rules, &godo.DatabaseFirewallRule{
			Type:  ruleType,
			Value: ruleValue,
		})
	}
	return rules, true
}

func dbOutput(db *godo.Database) *interfaces.ResourceOutput {
	outputs := map[string]any{
		"engine":  db.EngineSlug,
		"region":  db.RegionSlug,
		"size":    db.SizeSlug,
		"version": db.VersionSlug,
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
		Sensitive:  map[string]bool{"uri": true, "password": true, "user": true},
		Status:     db.Status,
	}
}

func (d *DatabaseDriver) ProviderIDFormat() interfaces.ProviderIDFormat { return interfaces.IDFormatUUID }
