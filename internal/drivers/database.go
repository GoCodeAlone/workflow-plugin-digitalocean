package drivers

import (
	"context"
	"fmt"
	"log"
	"sort"

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
	client     DatabaseClient
	appsClient AppPlatformClient // optional; used to resolve type=app trusted_source names to UUIDs
	region     string
}

// NewDatabaseDriver creates a DatabaseDriver backed by a real godo client.
// The godo Apps client is wired automatically to support type=app trusted_source
// name → UUID resolution at apply time.
func NewDatabaseDriver(c *godo.Client, region string) *DatabaseDriver {
	return &DatabaseDriver{client: c.Databases, appsClient: c.Apps, region: region}
}

// NewDatabaseDriverWithClient creates a driver with an injected database client (for tests).
// appsClient may be nil when tests do not exercise type=app trusted_source rules.
func NewDatabaseDriverWithClient(c DatabaseClient, region string) *DatabaseDriver {
	return &DatabaseDriver{client: c, region: region}
}

// NewDatabaseDriverWithClients creates a driver with injected database and apps
// clients (for tests that exercise type=app trusted_source name → UUID resolution).
func NewDatabaseDriverWithClients(c DatabaseClient, apps AppPlatformClient, region string) *DatabaseDriver {
	return &DatabaseDriver{client: c, appsClient: apps, region: region}
}

func (d *DatabaseDriver) Create(ctx context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	engine := strFromConfig(spec.Config, "engine", "pg")
	version := strFromConfig(spec.Config, "version", "")
	size := strFromConfig(spec.Config, "size", "db-s-1vcpu-1gb")
	region := strFromConfig(spec.Config, "region", d.region)
	numNodes, _ := intFromConfig(spec.Config, "num_nodes", 1)

	createRules, err := d.buildCreateFirewallRules(ctx, spec.Config)
	if err != nil {
		return nil, fmt.Errorf("database create %q: %w", spec.Name, err)
	}

	req := &godo.DatabaseCreateRequest{
		Name:       spec.Name,
		EngineSlug: engine,
		Version:    version,
		SizeSlug:   size,
		Region:     region,
		NumNodes:   numNodes,
		Rules:      createRules,
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
	// Resolve firewall rules BEFORE mutating the database. buildUpdateFirewallRules
	// can fail for config errors (wrong type) or app name→UUID resolution failures,
	// so validating upfront avoids partial applies caused by those preflight issues.
	// A later UpdateFirewallRules call can still fail after Resize due to API/service
	// errors, so this reduces—but does not eliminate—the chance of partial applies.
	fwRules, fwPresent, fwErr := d.buildUpdateFirewallRules(ctx, spec.Config)
	if fwErr != nil {
		return nil, fmt.Errorf("database update firewall %q: %w", ref.Name, fwErr)
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
	if fwPresent {
		_, err = d.client.UpdateFirewallRules(ctx, providerID, &godo.DatabaseUpdateFirewallRulesRequest{
			Rules: fwRules,
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

func (d *DatabaseDriver) Scale(ctx context.Context, ref interfaces.ResourceRef, replicas int) (*interfaces.ResourceOutput, error) {
	providerID, err := d.resolveProviderID(ctx, ref)
	if err != nil {
		return nil, err
	}
	db, _, err := d.client.Get(ctx, providerID)
	if err != nil {
		return nil, fmt.Errorf("database scale read %q: %w", ref.Name, WrapGodoError(err))
	}
	_, err = d.client.Resize(ctx, providerID, &godo.DatabaseResizeRequest{
		SizeSlug: db.SizeSlug,
		NumNodes: replicas,
	})
	if err != nil {
		return nil, fmt.Errorf("database scale %q: %w", ref.Name, WrapGodoError(err))
	}
	ref.ProviderID = providerID
	return d.Read(ctx, ref)
}

func (d *DatabaseDriver) SensitiveKeys() []string {
	return []string{"uri", "password", "user"}
}

// resolveAppNamesMap builds a name→UUID map for all non-UUID app names found
// in the raw trusted_sources list. A single paginated listing pass is made
// (potentially multiple page requests) regardless of how many type=app rules
// exist, avoiding repeated full scans. Pagination stops early once all
// requested names have been resolved. Returns an error if:
//   - the Apps client is nil and a non-UUID name requires resolution,
//   - the DO Apps.List API call fails, or
//   - a requested app name is not found (wraps ErrResourceNotFound).
func (d *DatabaseDriver) resolveAppNamesMap(ctx context.Context, raw []any) (map[string]string, error) {
	// Collect the distinct non-UUID app names that need resolution.
	needed := map[string]struct{}{}
	for _, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if strFromConfig(m, "type", "") != "app" {
			continue
		}
		val := strFromConfig(m, "value", "")
		if val == "" || isUUIDLike(val) {
			continue
		}
		needed[val] = struct{}{}
	}
	if len(needed) == 0 {
		return nil, nil // nothing to look up
	}
	if d.appsClient == nil {
		// Sort names so the error message is deterministic regardless of map
		// iteration order (important when multiple type=app rules are present).
		names := make([]string, 0, len(needed))
		for n := range needed {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, fmt.Errorf(
			"trusted_source type=app value %q is not a UUID and no Apps client is configured for name lookup; pass the app UUID directly",
			names[0],
		)
	}
	resolved := make(map[string]string, len(needed))
	opts := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		apps, resp, err := d.appsClient.List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("app list for trusted_source resolution: %w", WrapGodoError(err))
		}
		for _, app := range apps {
			if app.Spec == nil {
				continue
			}
			if _, want := needed[app.Spec.Name]; want {
				resolved[app.Spec.Name] = app.ID
				if len(resolved) == len(needed) {
					return resolved, nil // all names resolved — no more pages needed
				}
			}
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opts.Page++
	}
	// Verify every requested name was found.
	for name := range needed {
		if _, ok := resolved[name]; !ok {
			return nil, fmt.Errorf("trusted_source app %q: %w", name, ErrResourceNotFound)
		}
	}
	return resolved, nil
}

// buildCreateFirewallRules converts "trusted_sources" config to
// []*godo.DatabaseCreateFirewallRule, resolving type=app values to UUIDs via
// a single Apps.List pass (see resolveAppNamesMap).
func (d *DatabaseDriver) buildCreateFirewallRules(ctx context.Context, cfg map[string]any) ([]*godo.DatabaseCreateFirewallRule, error) {
	rawVal, exists := cfg["trusted_sources"]
	if !exists {
		return nil, nil
	}
	raw, ok := rawVal.([]any)
	if !ok {
		return nil, fmt.Errorf("trusted_sources: expected a list of rule objects, got %T; check your config syntax (use a YAML list, not a scalar)", rawVal)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	appUUIDs, err := d.resolveAppNamesMap(ctx, raw)
	if err != nil {
		return nil, err
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
		if ruleType == "app" {
			if uuid, found := appUUIDs[ruleValue]; found {
				ruleValue = uuid
			}
			// UUID-shaped values are used as-is (not in the appUUIDs map).
		}
		rules = append(rules, &godo.DatabaseCreateFirewallRule{
			Type:  ruleType,
			Value: ruleValue,
		})
	}
	return rules, nil
}

// buildUpdateFirewallRules converts "trusted_sources" config to
// ([]*godo.DatabaseFirewallRule, bool, error). The bool mirrors
// trustedSourceFirewallRulesFromConfig: false means key absent (leave unchanged);
// true means key present (apply the returned slice, even if empty). type=app
// values are resolved to UUIDs via a single Apps.List pass (see resolveAppNamesMap).
func (d *DatabaseDriver) buildUpdateFirewallRules(ctx context.Context, cfg map[string]any) ([]*godo.DatabaseFirewallRule, bool, error) {
	raw, ok := cfg["trusted_sources"]
	if !ok {
		return nil, false, nil // key absent: leave existing rules unchanged
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, false, fmt.Errorf("trusted_sources: expected a list of rule objects, got %T; check your config syntax (use a YAML list, not a scalar)", raw)
	}
	appUUIDs, err := d.resolveAppNamesMap(ctx, list)
	if err != nil {
		return nil, false, err
	}
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
		if ruleType == "app" {
			if uuid, found := appUUIDs[ruleValue]; found {
				ruleValue = uuid
			}
			// UUID-shaped values are used as-is (not in the appUUIDs map).
		}
		rules = append(rules, &godo.DatabaseFirewallRule{
			Type:  ruleType,
			Value: ruleValue,
		})
	}
	return rules, true, nil
}

// trustedSourceCreateRulesFromConfig converts canonical "trusted_sources" to
// []*godo.DatabaseCreateFirewallRule for use in a DatabaseCreateRequest.
// Each entry must have "type" (ip_addr|k8s|app|droplet|tag) and "value".
// NOTE: this function does NOT resolve type=app names to UUIDs. Use
// buildCreateFirewallRules (a DatabaseDriver method) when a context is available.
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
//
// NOTE: this function does NOT resolve type=app names to UUIDs. Use
// buildUpdateFirewallRules (a DatabaseDriver method) when a context is available.
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
