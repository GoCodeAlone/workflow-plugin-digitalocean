package drivers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// ErrAppNotFound is returned by resolveAppNamesMap when a requested app name
// is absent from the DO Apps.List result. It wraps ErrResourceNotFound so
// callers using errors.Is(err, ErrResourceNotFound) continue to work; the
// more specific sentinel is used internally to distinguish "app not yet
// created" from API-level failures (rate-limit, transient, auth) that must
// NOT trigger the deferred-update path.
var ErrAppNotFound = fmt.Errorf("trusted_sources app: %w", ErrResourceNotFound)

// deferredFirewallUpdate holds the context needed for a post-apply firewall
// update: the DO database UUID and the full desired ResourceSpec (including
// all trusted_sources entries, both resolvable and deferred).
type deferredFirewallUpdate struct {
	providerID string
	spec       interfaces.ResourceSpec
}

// DatabaseClient is the godo Databases interface (for mocking).
type DatabaseClient interface {
	Create(ctx context.Context, req *godo.DatabaseCreateRequest) (*godo.Database, *godo.Response, error)
	Get(ctx context.Context, databaseID string) (*godo.Database, *godo.Response, error)
	List(ctx context.Context, opts *godo.ListOptions) ([]godo.Database, *godo.Response, error)
	Resize(ctx context.Context, databaseID string, req *godo.DatabaseResizeRequest) (*godo.Response, error)
	Delete(ctx context.Context, databaseID string) (*godo.Response, error)
	UpdateFirewallRules(ctx context.Context, databaseID string, req *godo.DatabaseUpdateFirewallRulesRequest) (*godo.Response, error)
}

// appNameLister is a minimal interface for looking up App Platform app names.
// DatabaseDriver only needs List; using the full AppPlatformClient here would
// unnecessarily couple the database driver to app lifecycle operations.
type appNameLister interface {
	List(ctx context.Context, opts *godo.ListOptions) ([]*godo.App, *godo.Response, error)
}

// DatabaseDriver manages DigitalOcean Managed Databases (infra.database).
// Supports pg, mysql, redis, mongodb, kafka.
type DatabaseDriver struct {
	client     DatabaseClient
	appsClient appNameLister // optional; used to resolve type=app trusted_source names to UUIDs
	region     string

	// pendingFirewallUpdates accumulates deferred trusted_sources updates that
	// could not be applied at create/update time because referenced apps did not
	// exist yet. DOProvider.Apply calls FlushDeferredUpdates after all plan
	// actions complete so the full rule set is applied once apps are provisioned.
	pendingFirewallUpdates []deferredFirewallUpdate
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
// apps must implement appNameLister (i.e. have a List method); any *fakeAppsClient
// or real godo.AppsService satisfies this.
func NewDatabaseDriverWithClients(c DatabaseClient, apps appNameLister, region string) *DatabaseDriver {
	return &DatabaseDriver{client: c, appsClient: apps, region: region}
}

func (d *DatabaseDriver) Create(ctx context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	engine := strFromConfig(spec.Config, "engine", "pg")
	version := strFromConfig(spec.Config, "version", "")
	size := strFromConfig(spec.Config, "size", "db-s-1vcpu-1gb")
	region := strFromConfig(spec.Config, "region", d.region)
	numNodes, _ := intFromConfig(spec.Config, "num_nodes", 1)

	createRules, err := d.buildCreateFirewallRules(ctx, spec.Config)
	deferred := false
	if err != nil {
		if !errors.Is(err, ErrAppNotFound) {
			// Real API failure or config error — propagate immediately.
			return nil, fmt.Errorf("database create %q: %w", spec.Name, err)
		}
		// type=app trusted_source(s) reference not-yet-existing app(s) — create
		// DB without app-based rules now and queue a deferred firewall update for
		// the post-apply pass (after all plan creates have completed and apps exist).
		createRules, err = d.buildCreateFirewallRulesExcludingApps(spec.Config)
		if err != nil {
			return nil, fmt.Errorf("database create %q: build partial rules: %w", spec.Name, err)
		}
		deferred = true
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

	if deferred {
		d.pendingFirewallUpdates = append(d.pendingFirewallUpdates, deferredFirewallUpdate{
			providerID: db.ID,
			spec:       spec,
		})
	}

	return dbOutput(db), nil
}

// SupportsUpsert reports that DatabaseDriver can locate a resource by name alone
// (empty ProviderID), enabling the ErrResourceAlreadyExists → upsert path.
func (d *DatabaseDriver) SupportsUpsert() bool { return true }

func (d *DatabaseDriver) AdoptionRef(spec interfaces.ResourceSpec) (interfaces.ResourceRef, bool, error) {
	if !boolFromConfig(spec.Config, "adopt_existing", false) {
		return interfaces.ResourceRef{}, false, nil
	}
	if strings.TrimSpace(spec.Name) == "" {
		return interfaces.ResourceRef{}, false, fmt.Errorf("database adoption requires resource name")
	}
	resourceType := spec.Type
	if resourceType == "" {
		resourceType = "infra.database"
	}
	return interfaces.ResourceRef{Name: spec.Name, Type: resourceType}, true, nil
}

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
	db, err := d.lookupDatabaseByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if db.ID == "" {
		return dbOutput(db), nil
	}
	hydrated, _, err := d.client.Get(ctx, db.ID)
	if err != nil {
		return nil, fmt.Errorf("database read %q after name lookup: %w", name, WrapGodoError(err))
	}
	if hydrated == nil {
		return dbOutput(db), nil
	}
	return dbOutput(hydrated), nil
}

func (d *DatabaseDriver) lookupDatabaseByName(ctx context.Context, name string) (*godo.Database, error) {
	opts := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		dbs, resp, err := d.client.List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("database list: %w", WrapGodoError(err))
		}
		for i := range dbs {
			if dbs[i].Name == name {
				return &dbs[i], nil
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
	db, err := d.lookupDatabaseByName(ctx, ref.Name)
	if err != nil {
		return "", fmt.Errorf("database state-heal for %q: %w", ref.Name, err)
	}
	return db.ID, nil
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
	deferred := false
	if fwErr != nil {
		if !errors.Is(fwErr, ErrAppNotFound) {
			// Real API failure or config error — propagate immediately.
			return nil, fmt.Errorf("database update firewall %q: %w", ref.Name, fwErr)
		}
		// type=app trusted_source(s) reference not-yet-existing app(s) — apply the
		// resolvable subset of rules now and queue the full spec for a deferred
		// post-apply update.
		fwRules, fwPresent, fwErr = d.buildUpdateFirewallRulesExcludingApps(spec.Config)
		if fwErr != nil {
			return nil, fmt.Errorf("database update %q: build partial rules: %w", ref.Name, fwErr)
		}
		deferred = true
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
	if deferred {
		d.pendingFirewallUpdates = append(d.pendingFirewallUpdates, deferredFirewallUpdate{
			providerID: providerID,
			spec:       spec,
		})
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
	var needsReplace bool
	for _, field := range []struct {
		key string
		def string
	}{
		{key: "engine", def: "pg"},
		{key: "version"},
		{key: "region", def: d.region},
	} {
		desiredValue := strFromConfig(desired.Config, field.key, field.def)
		if desiredValue == "" {
			continue
		}
		currentValue, _ := current.Outputs[field.key].(string)
		if currentValue != "" && currentValue != desiredValue {
			changes = append(changes, interfaces.FieldChange{
				Path: field.key, Old: currentValue, New: desiredValue, ForceNew: true,
			})
			needsReplace = true
		}
	}
	if sz := strFromConfig(desired.Config, "size", ""); sz != "" {
		if cur, _ := current.Outputs["size"].(string); cur != sz {
			changes = append(changes, interfaces.FieldChange{Path: "size", Old: cur, New: sz})
		}
	}
	if numNodes, ok := intFromConfig(desired.Config, "num_nodes", 0); ok {
		if cur, ok := intOutputFromMap(current.Outputs, "num_nodes"); ok && cur != numNodes {
			changes = append(changes, interfaces.FieldChange{Path: "num_nodes", Old: cur, New: numNodes})
		}
	}
	return &interfaces.DiffResult{
		NeedsUpdate:  len(changes) > 0,
		NeedsReplace: needsReplace,
		Changes:      changes,
	}, nil
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
		quoted := make([]string, len(names))
		for i, n := range names {
			quoted[i] = fmt.Sprintf("%q", n)
		}
		return nil, fmt.Errorf(
			"trusted_sources: type=app value(s) %s are not UUIDs and no Apps client is configured for name lookup; pass app UUIDs directly",
			strings.Join(quoted, ", "),
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
	// Collect every name that was not found and report them all at once.
	// Sort for deterministic output regardless of map iteration order.
	missing := make([]string, 0, len(needed))
	for name := range needed {
		if _, ok := resolved[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return resolved, nil
	}
	sort.Strings(missing)
	quotedMissing := make([]string, len(missing))
	for i, name := range missing {
		quotedMissing[i] = fmt.Sprintf("%q", name)
	}
	// Use ErrAppNotFound (not ErrResourceNotFound directly) so callers can
	// distinguish "app not yet created" from API-level failures. ErrAppNotFound
	// wraps ErrResourceNotFound so errors.Is(err, ErrResourceNotFound) still works.
	return nil, fmt.Errorf("trusted_sources app(s) %s: %w",
		strings.Join(quotedMissing, ", "), ErrAppNotFound)
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
// ([]*godo.DatabaseFirewallRule, bool, error). The bool reflects whether the
// "trusted_sources" key was present in the config:
//   - false: key absent — caller should leave existing firewall rules unchanged.
//   - true:  key present — caller should apply the returned rules (or surface the error).
//
// Returning true on error paths lets callers distinguish "key absent" from
// "key present but invalid/unresolvable" without inspecting the error type.
// type=app values are resolved to UUIDs via a single Apps.List pass (see resolveAppNamesMap).
func (d *DatabaseDriver) buildUpdateFirewallRules(ctx context.Context, cfg map[string]any) ([]*godo.DatabaseFirewallRule, bool, error) {
	raw, ok := cfg["trusted_sources"]
	if !ok {
		return nil, false, nil // key absent: leave existing rules unchanged
	}
	list, ok := raw.([]any)
	if !ok {
		// Key is present but wrong type — return true so callers don't treat this as "absent".
		return nil, true, fmt.Errorf("trusted_sources: expected a list of rule objects, got %T; check your config syntax (use a YAML list, not a scalar)", raw)
	}
	appUUIDs, err := d.resolveAppNamesMap(ctx, list)
	if err != nil {
		// Key is present but resolution failed — return true (key present) + error.
		return nil, true, err
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
		"engine":    db.EngineSlug,
		"num_nodes": float64(db.NumNodes),
		"region":    db.RegionSlug,
		"size":      db.SizeSlug,
		"version":   db.VersionSlug,
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

func (d *DatabaseDriver) ProviderIDFormat() interfaces.ProviderIDFormat {
	return interfaces.IDFormatUUID
}

func intOutputFromMap(outputs map[string]any, key string) (int, bool) {
	switch v := outputs[key].(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float32:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

// buildCreateFirewallRulesExcludingApps builds DatabaseCreateFirewallRules from
// the "trusted_sources" config, omitting type=app entries that require name
// resolution (non-UUID values). Used when buildCreateFirewallRules returns
// ErrAppNotFound so the DB can be created with the resolvable subset of rules
// while slug-based app rules are deferred. UUID-shaped type=app values are
// passed through directly since they require no resolution.
func (d *DatabaseDriver) buildCreateFirewallRulesExcludingApps(cfg map[string]any) ([]*godo.DatabaseCreateFirewallRule, error) {
	rawVal, exists := cfg["trusted_sources"]
	if !exists {
		return nil, nil
	}
	raw, ok := rawVal.([]any)
	if !ok {
		return nil, fmt.Errorf("trusted_sources: expected a list of rule objects, got %T; check your config syntax (use a YAML list, not a scalar)", rawVal)
	}
	rules := make([]*godo.DatabaseCreateFirewallRule, 0, len(raw))
	for _, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		ruleType := strFromConfig(m, "type", "")
		ruleValue := strFromConfig(m, "value", "")
		if ruleType == "" || ruleValue == "" || (ruleType == "app" && !isUUIDLike(ruleValue)) {
			continue // skip non-UUID app-type entries; they will be applied in the deferred pass
		}
		rules = append(rules, &godo.DatabaseCreateFirewallRule{
			Type:  ruleType,
			Value: ruleValue,
		})
	}
	return rules, nil
}

// buildUpdateFirewallRulesExcludingApps builds DatabaseFirewallRules from the
// "trusted_sources" config, omitting type=app entries that require name
// resolution (non-UUID values). Used when buildUpdateFirewallRules returns
// ErrAppNotFound so the partial rule set can be applied immediately while
// slug-based app rules are deferred. UUID-shaped type=app values are passed
// through directly since they require no resolution.
func (d *DatabaseDriver) buildUpdateFirewallRulesExcludingApps(cfg map[string]any) ([]*godo.DatabaseFirewallRule, bool, error) {
	raw, ok := cfg["trusted_sources"]
	if !ok {
		return nil, false, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, true, fmt.Errorf("trusted_sources: expected a list of rule objects, got %T; check your config syntax (use a YAML list, not a scalar)", raw)
	}
	rules := make([]*godo.DatabaseFirewallRule, 0, len(list))
	for _, v := range list {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		ruleType := strFromConfig(m, "type", "")
		ruleValue := strFromConfig(m, "value", "")
		if ruleType == "" || ruleValue == "" || (ruleType == "app" && !isUUIDLike(ruleValue)) {
			continue // skip non-UUID app-type entries; they will be applied in the deferred pass
		}
		rules = append(rules, &godo.DatabaseFirewallRule{
			Type:  ruleType,
			Value: ruleValue,
		})
	}
	return rules, true, nil
}

// HasDeferredUpdates reports whether the driver has pending firewall updates
// queued from Create or Update calls where type=app trusted_sources entries
// referenced apps that did not exist yet. DOProvider.Apply checks this after
// the main action loop and calls FlushDeferredUpdates when true.
func (d *DatabaseDriver) HasDeferredUpdates() bool {
	return len(d.pendingFirewallUpdates) > 0
}

// FlushDeferredUpdates applies all pending firewall updates that were deferred
// during Create/Update because referenced app(s) did not exist yet. Each entry
// re-resolves its type=app trusted_sources entries (app must now exist) and
// calls UpdateFirewallRules with the full rule set. Returns an error if any
// update fails — the caller (DOProvider.Apply) appends these to result.Errors
// so the failure is visible to the operator.
//
// Only successfully-flushed entries are removed from the queue. Entries that
// failed (rule-resolution error or UpdateFirewallRules API error) are retained
// so that a subsequent Apply automatically re-attempts the flush without
// requiring operator intervention. This matters because DatabaseDriver.Diff
// does not compare trusted_sources, meaning a retry Apply would otherwise
// produce no update action and the second-pass flush would never fire.
func (d *DatabaseDriver) FlushDeferredUpdates(ctx context.Context) error {
	if len(d.pendingFirewallUpdates) == 0 {
		return nil
	}
	var errs []string
	var remaining []deferredFirewallUpdate
	for _, pending := range d.pendingFirewallUpdates {
		fwRules, _, err := d.buildUpdateFirewallRules(ctx, pending.spec.Config)
		if err != nil {
			errs = append(errs, fmt.Sprintf("deferred firewall update %q: resolve rules: %v", pending.spec.Name, err))
			remaining = append(remaining, pending) // retain for retry
			continue
		}
		_, err = d.client.UpdateFirewallRules(ctx, pending.providerID, &godo.DatabaseUpdateFirewallRulesRequest{
			Rules: fwRules,
		})
		if err != nil {
			errs = append(errs, fmt.Sprintf("deferred firewall update %q: %v", pending.spec.Name, WrapGodoError(err)))
			remaining = append(remaining, pending) // retain for retry
		}
	}
	d.pendingFirewallUpdates = remaining // only cleared entries were successfully flushed
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}
