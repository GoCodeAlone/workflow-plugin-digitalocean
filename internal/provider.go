package internal

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/iac/wfctlhelpers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/GoCodeAlone/workflow/platform"
	"github.com/digitalocean/godo"
	"golang.org/x/oauth2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// tokenSource implements oauth2.TokenSource for the godo client.
type tokenSource struct{ token string }

func (t *tokenSource) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: t.token}, nil
}

// DOProvider implements interfaces.IaCProvider for DigitalOcean.
type DOProvider struct {
	client  *godo.Client
	region  string
	drivers map[string]interfaces.ResourceDriver

	// bootstrapClientFactory is used by BootstrapStateBackend to build the S3
	// client. Nil means use the default newSpacesS3Client. Tests inject a fake.
	bootstrapClientFactory func(accessKey, secretKey, region string) spacesBucketClient
}

var _ interfaces.IaCProvider = (*DOProvider)(nil)
var _ interfaces.ProviderMigrationRepairer = (*DOProvider)(nil)
var _ interfaces.Enumerator = (*DOProvider)(nil)
var _ interfaces.EnumeratorAll = (*DOProvider)(nil)
var _ interfaces.DriftConfigDetector = (*DOProvider)(nil)
var _ interfaces.ProviderCredentialRevoker = (*DOProvider)(nil)

// NewDOProvider creates an uninitialised DOProvider.
func NewDOProvider() *DOProvider {
	return &DOProvider{}
}

func (p *DOProvider) Name() string    { return "digitalocean" }
func (p *DOProvider) Version() string { return Version }

// Initialize configures the godo client using the provided config map.
// Required: "token".
// Optional: "region" (default "nyc3"), "spaces_access_key", "spaces_secret_key".
//
// The provided ctx is threaded into oauth2.NewClient so callers that inject a
// custom *http.Client via oauth2.HTTPClient (tests, custom transports, proxy
// configurations) flow through to the godo client. Per-request cancellation
// remains controlled by the ctx passed to each subsequent driver call —
// godo wraps each request in ctx via http.Request.WithContext.
//
// addresses workflow-plugin-digitalocean#62: prior implementation hardcoded
// context.Background(), silently dropping any HTTPClient injection.
func (p *DOProvider) Initialize(ctx context.Context, config map[string]any) error {
	if ctx == nil {
		return fmt.Errorf("digitalocean: Initialize requires non-nil ctx")
	}
	token, _ := config["token"].(string)
	if token == "" {
		return fmt.Errorf("digitalocean: missing required config key 'token'")
	}
	region, _ := config["region"].(string)
	if region == "" {
		region = "nyc3"
	}
	p.region = region

	spacesAccessKey, _ := config["spaces_access_key"].(string)
	spacesSecretKey, _ := config["spaces_secret_key"].(string)

	oauthClient := oauth2.NewClient(ctx, &tokenSource{token: token})
	if debugAPIEnabled() {
		// Wrap the transport so every DigitalOcean API call is logged to
		// stderr. The oauth2 client owns the transport; we replace it with a
		// loggingRoundTripper that delegates to the existing transport.
		oauthClient.Transport = newLoggingRoundTripper(oauthClient.Transport)
	}
	p.client = godo.NewClient(oauthClient)

	p.drivers = map[string]interfaces.ResourceDriver{
		"infra.container_service": drivers.NewAppPlatformDriver(p.client, p.region),
		"infra.k8s_cluster":       drivers.NewKubernetesDriver(p.client, p.region),
		"infra.database":          drivers.NewDatabaseDriver(p.client, p.region),
		"infra.cache":             drivers.NewCacheDriver(p.client, p.region),
		"infra.load_balancer":     drivers.NewLoadBalancerDriver(p.client, p.region),
		"infra.vpc":               drivers.NewVPCDriver(p.client, p.region),
		"infra.firewall":          drivers.NewFirewallDriver(p.client),
		"infra.dns":               drivers.NewDNSDriver(p.client),
		"infra.storage":           drivers.NewSpacesDriver(p.client, p.region, spacesAccessKey, spacesSecretKey),
		"infra.spaces_key":        drivers.NewSpacesKeyDriver(p.client),
		"infra.registry":          drivers.NewRegistryDriver(p.client),
		"infra.certificate":       drivers.NewCertificateDriver(p.client),
		"infra.droplet":           drivers.NewDropletDriver(p.client, p.region),
		"infra.volume":            drivers.NewVolumeDriver(p.client, p.region),
		"infra.iam_role":          drivers.NewIAMRoleDriver(),
		"infra.api_gateway":       drivers.NewAPIGatewayDriver(p.client, p.region),
	}
	return nil
}

// Capabilities returns the resource types this provider supports.
func (p *DOProvider) Capabilities() []interfaces.IaCCapabilityDeclaration {
	ops := []string{"create", "read", "update", "delete", "scale"}
	noScale := []string{"create", "read", "update", "delete"}
	return []interfaces.IaCCapabilityDeclaration{
		{ResourceType: "infra.container_service", Tier: 3, Operations: ops},
		{ResourceType: "infra.k8s_cluster", Tier: 1, Operations: ops},
		{ResourceType: "infra.database", Tier: 1, Operations: ops},
		{ResourceType: "infra.cache", Tier: 1, Operations: ops},
		{ResourceType: "infra.load_balancer", Tier: 1, Operations: ops},
		{ResourceType: "infra.vpc", Tier: 1, Operations: ops},
		{ResourceType: "infra.firewall", Tier: 1, Operations: ops},
		{ResourceType: "infra.dns", Tier: 1, Operations: ops},
		{ResourceType: "infra.storage", Tier: 1, Operations: ops},
		{ResourceType: "infra.spaces_key", Tier: 1, Operations: noScale},
		{ResourceType: "infra.registry", Tier: 2, Operations: ops},
		{ResourceType: "infra.certificate", Tier: 1, Operations: ops},
		{ResourceType: "infra.droplet", Tier: 1, Operations: ops},
		{ResourceType: "infra.volume", Tier: 1, Operations: noScale},
		{ResourceType: "infra.iam_role", Tier: 1, Operations: noScale},
		{ResourceType: "infra.api_gateway", Tier: 3, Operations: noScale},
	}
}

// ResourceDriver returns the driver for the given resource type.
func (p *DOProvider) ResourceDriver(resourceType string) (interfaces.ResourceDriver, error) {
	d, ok := p.drivers[resourceType]
	if !ok {
		return nil, fmt.Errorf("digitalocean: unsupported resource type %q", resourceType)
	}
	return d, nil
}

func (p *DOProvider) RepairDirtyMigration(ctx context.Context, req interfaces.MigrationRepairRequest) (*interfaces.MigrationRepairResult, error) {
	driver, err := p.ResourceDriver("infra.container_service")
	if err != nil {
		return nil, err
	}
	repairer, ok := driver.(interfaces.ProviderMigrationRepairer)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "digitalocean: app platform driver does not implement migration repair")
	}
	return repairer.RepairDirtyMigration(ctx, req)
}

// doUnsupportedCanonicalKeys lists canonical keys that the DO plugin does not yet map.
// Each entry is removed from SupportedCanonicalKeys() so wfctl validate can warn callers.
// "sidecars" is now mapped in v0.7.0 Task 37 as sibling AppServiceSpec entries.
var doUnsupportedCanonicalKeys = map[string]struct{}{}

// SupportedCanonicalKeys returns the canonical IaC config keys that this DO provider
// currently maps. Keys in doUnsupportedCanonicalKeys are excluded until their Task
// implementation lands (see comments there).
func (p *DOProvider) SupportedCanonicalKeys() []string {
	all := interfaces.CanonicalKeys()
	out := make([]string, 0, len(all))
	for _, k := range all {
		if _, unsupported := doUnsupportedCanonicalKeys[k]; !unsupported {
			out = append(out, k)
		}
	}
	return out
}

// ResolveSizing maps abstract size tiers to DigitalOcean SKUs.
func (p *DOProvider) ResolveSizing(resourceType string, size interfaces.Size, hints *interfaces.ResourceHints) (*interfaces.ProviderSizing, error) {
	return resolveSizing(resourceType, size, hints)
}

// Plan computes the set of actions needed to reach the desired state by
// delegating to the canonical platform.ComputePlan helper. The helper
// dispatches per-resource Diff in parallel, classifies replace vs update
// (including the ForceNew → replace promotion), emits creates/deletes in
// dependency-correct order, and consults the diff cache.
//
// The 2-statement form (call + pointer-bridge return) is mandated by the
// W-Refactor / iac-codemod analyzer (cmd/iac-codemod AssertPlanDelegatesToHelper);
// see workflow CHANGELOG entry referencing platform.ComputePlan as the
// canonical target for v2 IaC providers.
//
// addresses workflow-plugin-digitalocean#63: the prior hand-rolled body
// duplicated ComputePlan's classification logic and silently dropped the
// ForceNew → replace upgrade path (only NeedsReplace was honored).
func (p *DOProvider) Plan(ctx context.Context, desired []interfaces.ResourceSpec, current []interfaces.ResourceState) (*interfaces.IaCPlan, error) {
	plan, err := platform.ComputePlan(ctx, p, desired, current)
	return &plan, err
}

// deferredUpdater is an optional interface for ResourceDrivers that accumulate
// resource-level updates that cannot be applied until all plan creates complete.
// The canonical case is DatabaseDriver deferring type=app trusted_sources entries
// that reference apps created later in the same plan. Apply calls
// FlushDeferredUpdates once after the main dispatch loop; errors are appended to
// ApplyResult.Errors so the failure is visible to the operator.
//
// This is a DO-plugin-specific extension point not yet hoisted into
// wfctlhelpers.ApplyPlan; the second-pass flush below preserves the regression
// gate while the v2 dispatch handles the per-action loop.
type deferredUpdater interface {
	HasDeferredUpdates() bool
	FlushDeferredUpdates(ctx context.Context) error
}

// Apply executes the plan via wfctlhelpers.ApplyPlan, then runs the
// DO-specific deferred-update second pass.
//
// PR P-DO TP2: under iacProvider.computePlanVersion: v2 wfctl dispatches
// directly through wfctlhelpers.ApplyPlan and does not call this method.
// The implementation here remains for legacy v1 callers (wfctl < v0.21.0
// or any in-process embedder of the gRPC plugin) and for the deferred-flush
// behavior that wfctlhelpers does not yet hoist.
//
// Per-action upsert recovery, JIT substitution, the Replace cascade, and
// the input-drift postcondition all live in wfctlhelpers.ApplyPlan now —
// drivers that opt into the upsert recovery path implement
// interfaces.UpsertSupporter (DO drivers AppPlatform, VPC, Firewall,
// Database all do; signature: SupportsUpsert() bool). The local
// upsertSupporter interface previously declared here is no longer needed:
// its SupportsUpsert() bool method is structurally identical to
// interfaces.UpsertSupporter, so the existing driver implementations
// satisfy the canonical interface without code change.
//
// wfctl:skip-iac-codemod
//
// The body intentionally wraps wfctlhelpers.ApplyPlan rather than
// matching the codemod's canonical single-statement
// `return wfctlhelpers.ApplyPlan(ctx, p, plan)` shape: the
// post-helper deferred-update flush below is a DO-plugin-specific
// regression gate (see provider_deferred_test.go and CHANGELOG entry
// for staging-deploy-blockers Blocker 2) that wfctlhelpers does not
// hoist. The skip marker tells the codemod's
// AssertApplyDelegatesToHelper analyzer this deviation is intentional.
// When wfctlhelpers grows a deferred-update lifecycle hook, the
// wrapper can collapse and the marker can drop.
func (p *DOProvider) Apply(ctx context.Context, plan *interfaces.IaCPlan) (*interfaces.ApplyResult, error) {
	result, err := wfctlhelpers.ApplyPlan(ctx, p, plan)
	if err != nil {
		// ApplyPlan only returns a top-level error on context cancellation
		// — per-action failures land on result.Errors. Surface the
		// cancellation alongside whatever partial result is in hand so
		// callers can still inspect any actions that completed.
		return result, err
	}

	// Second pass: flush deferred updates accumulated by drivers during the
	// main action loop. These arise when a resource's config (e.g. DB
	// trusted_sources with type=app) references another resource provisioned
	// later in the same plan. By this point all plan creates have completed
	// and the referenced resources exist.
	//
	// Iterate the driver registry directly (not plan.Actions) so that orphaned
	// deferred entries are flushed even when their resource type no longer
	// appears in the current plan (e.g. after a transient flush failure on a
	// prior Apply run). Each driver is checked at most once regardless of how
	// many actions reference it.
	for resourceType, d := range p.drivers {
		du, ok := d.(deferredUpdater)
		if !ok || !du.HasDeferredUpdates() {
			continue
		}
		if flushErr := du.FlushDeferredUpdates(ctx); flushErr != nil {
			result.Errors = append(result.Errors, interfaces.ActionError{
				Resource: resourceType,
				Action:   "deferred_update",
				Error:    flushErr.Error(),
			})
		}
	}

	return result, nil
}

// EnumerateByTag implements the opt-in interfaces.Enumerator interface.
//
// Lists DO resources tagged with the given tag and returns them as
// interfaces.ResourceRef values keyed on the same (Name, Type, ProviderID)
// tuple that the corresponding ResourceDriver(type).Delete consumes. Used by
// `wfctl infra cleanup --tag <name>` to drive tag-scoped teardown.
//
// Implementation strategy:
//   - Tags.Get is queried first as a probe: if the tag itself does not exist
//     in the DO account (404), the result is an empty slice (not an error).
//     This matches operator expectation that running cleanup against a tag
//     that has never been used reports "no resources" rather than failing.
//     A 200 here indicates the tag is known to DO, but the per-resource
//     queries below are what actually populate the result slice — Tags.Get's
//     own response only carries counts + last-tagged metadata, not a full
//     list of tagged resources.
//   - Droplets uses the native ListByTag endpoint.
//   - Volumes and Databases do not expose a tag-filter parameter, so the
//     full list is fetched and filtered client-side on the Tags slice.
//   - Other DO resource types (load balancers, k8s clusters, app platform,
//     etc.) either do not support tags or have not yet been wired here.
//     The cleanup subcommand documents per-provider coverage in
//     workflow/docs/WFCTL.md `#### infra cleanup`.
//
// The contract for the returned ResourceRef.ProviderID matches what each
// ResourceDriver expects on Delete: a string-formatted droplet ID for
// droplets, the volume ID for volumes, and the database cluster UUID for
// databases. Name is the user-facing resource name. Type is the canonical
// `infra.<kind>` matching DOProvider's driver registration.
func (p *DOProvider) EnumerateByTag(ctx context.Context, tag string) ([]interfaces.ResourceRef, error) {
	if p.client == nil {
		return nil, fmt.Errorf("digitalocean: EnumerateByTag called on provider that is not initialized — call Initialize first")
	}
	if tag == "" {
		return nil, fmt.Errorf("digitalocean: EnumerateByTag requires a non-empty tag argument")
	}

	// Probe Tags.Get to distinguish "tag does not exist" (return empty) from
	// "API call failed" (return error). godo wraps non-2xx responses in
	// godo.ErrorResponse with the HTTP Response embedded.
	if _, _, err := p.client.Tags.Get(ctx, tag); err != nil {
		var doErr *godo.ErrorResponse
		if errors.As(err, &doErr) && doErr.Response != nil && doErr.Response.StatusCode == 404 {
			return nil, nil
		}
		return nil, fmt.Errorf("digitalocean: get tag %q: %w", tag, err)
	}

	var refs []interfaces.ResourceRef

	// Droplets — native ListByTag, paginated.
	dropletPage := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		droplets, resp, err := p.client.Droplets.ListByTag(ctx, tag, dropletPage)
		if err != nil {
			return nil, fmt.Errorf("digitalocean: list droplets by tag %q: %w", tag, err)
		}
		for _, d := range droplets {
			refs = append(refs, interfaces.ResourceRef{
				Name:       d.Name,
				Type:       "infra.droplet",
				ProviderID: strconv.Itoa(d.ID),
			})
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		nextPage, err := resp.Links.CurrentPage()
		if err != nil {
			return nil, fmt.Errorf("digitalocean: paginate droplets: %w", err)
		}
		dropletPage.Page = nextPage + 1
	}

	// Volumes — list all, filter client-side on Tags membership.
	volumePage := &godo.ListVolumeParams{ListOptions: &godo.ListOptions{Page: 1, PerPage: 200}}
	for {
		volumes, resp, err := p.client.Storage.ListVolumes(ctx, volumePage)
		if err != nil {
			return nil, fmt.Errorf("digitalocean: list volumes for tag %q: %w", tag, err)
		}
		for _, v := range volumes {
			if !stringSliceContains(v.Tags, tag) {
				continue
			}
			refs = append(refs, interfaces.ResourceRef{
				Name:       v.Name,
				Type:       "infra.volume",
				ProviderID: v.ID,
			})
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		nextPage, err := resp.Links.CurrentPage()
		if err != nil {
			return nil, fmt.Errorf("digitalocean: paginate volumes: %w", err)
		}
		volumePage.ListOptions.Page = nextPage + 1
	}

	// Databases + Caches — list all (single endpoint covers both), filter
	// client-side on Tags membership, and split on EngineSlug.
	//
	// godo.Databases.List returns BOTH SQL-style database clusters (postgres,
	// mysql, mongodb, etc.) AND managed Redis caches in a single response
	// (DO models them under the same /v2/databases endpoint). The DO plugin
	// splits these into separate driver types — DatabaseDriver vs
	// CacheDriver — based on EngineSlug ("redis" → cache; everything else
	// → database). EnumerateByTag must mirror that split so the cleanup
	// dispatcher routes Delete to the correct driver and the dry-run /
	// audit log surfaces the canonical resource type. Emitting every entry
	// as `infra.database` would silently misclassify caches.
	dbPage := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		databases, resp, err := p.client.Databases.List(ctx, dbPage)
		if err != nil {
			return nil, fmt.Errorf("digitalocean: list databases for tag %q: %w", tag, err)
		}
		for _, db := range databases {
			if !stringSliceContains(db.Tags, tag) {
				continue
			}
			resourceType := "infra.database"
			if db.EngineSlug == "redis" {
				resourceType = "infra.cache"
			}
			refs = append(refs, interfaces.ResourceRef{
				Name:       db.Name,
				Type:       resourceType,
				ProviderID: db.ID,
			})
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		nextPage, err := resp.Links.CurrentPage()
		if err != nil {
			return nil, fmt.Errorf("digitalocean: paginate databases: %w", err)
		}
		dbPage.Page = nextPage + 1
	}

	return refs, nil
}

// stringSliceContains reports whether s is present in slice. Used by EnumerateByTag
// to filter resources whose tag list includes the requested tag.
func stringSliceContains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// Destroy deletes the given resources.
func (p *DOProvider) Destroy(ctx context.Context, resources []interfaces.ResourceRef) (*interfaces.DestroyResult, error) {
	result := &interfaces.DestroyResult{}
	for _, ref := range resources {
		d, err := p.ResourceDriver(ref.Type)
		if err != nil {
			result.Errors = append(result.Errors, interfaces.ActionError{
				Resource: ref.Name, Action: "delete", Error: err.Error(),
			})
			continue
		}
		if err := d.Delete(ctx, ref); err != nil {
			result.Errors = append(result.Errors, interfaces.ActionError{
				Resource: ref.Name, Action: "delete", Error: err.Error(),
			})
			continue
		}
		result.Destroyed = append(result.Destroyed, ref.Name)
	}
	return result, nil
}

// Status returns the live status of the given resources.
func (p *DOProvider) Status(ctx context.Context, resources []interfaces.ResourceRef) ([]interfaces.ResourceStatus, error) {
	var statuses []interfaces.ResourceStatus
	for _, ref := range resources {
		d, err := p.ResourceDriver(ref.Type)
		if err != nil {
			statuses = append(statuses, interfaces.ResourceStatus{
				Name: ref.Name, Type: ref.Type, ProviderID: ref.ProviderID, Status: "unknown",
			})
			continue
		}
		out, err := d.Read(ctx, ref)
		if err != nil {
			statuses = append(statuses, interfaces.ResourceStatus{
				Name: ref.Name, Type: ref.Type, ProviderID: ref.ProviderID, Status: "unknown",
			})
			continue
		}
		statuses = append(statuses, interfaces.ResourceStatus{
			Name: out.Name, Type: out.Type, ProviderID: out.ProviderID,
			Status: out.Status, Outputs: out.Outputs,
		})
	}
	return statuses, nil
}

// DetectDrift checks for ghost resources (state entries whose cloud counterpart
// no longer exists) and classifies each ref as Ghost, InSync, or Unknown.
//
//   - errors.Is(err, interfaces.ErrResourceNotFound) → DriftClassGhost (Drifted=true;
//     state has the resource but cloud returns 404). Caller may prune state via
//     wfctl infra apply --refresh.
//   - any other Read error → propagate (transient API failure; do NOT classify as drift).
//   - Read succeeds → DriftClassInSync (Drifted=false).
//   - driver registry lookup fails → DriftClassUnknown (Drifted=true; operator must investigate).
//
// Config-drift detection (DriftClassConfig) requires the desired spec and is
// available via DetectDriftWithSpecs, which is called by invokeProviderDetectDrift
// when the workflow caller injects specs from state's recorded outputs.
//
// Production-safety invariant: only genuine 404s (wrapped with
// interfaces.ErrResourceNotFound) trigger the ghost path. Rate-limit, auth,
// or network errors propagate unchanged so callers cannot accidentally prune
// state on transient failures.
func (p *DOProvider) DetectDrift(ctx context.Context, resources []interfaces.ResourceRef) ([]interfaces.DriftResult, error) {
	return p.DetectDriftWithSpecs(ctx, resources, nil)
}

// DetectDriftWithSpecs is the spec-injection variant of DetectDrift. It runs
// the same ghost/transient/unknown classification as DetectDrift, and when the
// caller supplies a desired ResourceSpec for a ref (keyed by ref.Name), it
// additionally calls driver.Diff to detect config-level drift (DriftClassConfig).
//
// Classification rules (per ref):
//   - driver lookup fails → DriftClassUnknown (Drifted=true).
//   - Read returns ErrResourceNotFound → DriftClassGhost (Drifted=true); spec is
//     ignored because the resource does not exist in the cloud.
//   - Read returns any other error → propagate; discard accumulated results.
//   - Read succeeds and no spec provided → DriftClassInSync (Drifted=false).
//   - Read succeeds and spec provided:
//   - Diff error → propagate; discard accumulated results.
//   - Diff reports NeedsUpdate → DriftClassConfig (Drifted=true); Fields lists
//     the changed config paths.
//   - Diff reports no changes → DriftClassInSync (Drifted=false).
//
// A nil or empty specs map is equivalent to calling DetectDrift.
func (p *DOProvider) DetectDriftWithSpecs(ctx context.Context, resources []interfaces.ResourceRef, specs map[string]interfaces.ResourceSpec) ([]interfaces.DriftResult, error) {
	var results []interfaces.DriftResult
	for _, ref := range resources {
		d, err := p.ResourceDriver(ref.Type)
		if err != nil {
			results = append(results, interfaces.DriftResult{
				Name:    ref.Name,
				Type:    ref.Type,
				Drifted: true,
				Class:   interfaces.DriftClassUnknown,
				Fields:  []string{"provider: " + err.Error()},
			})
			continue
		}

		out, err := d.Read(ctx, ref)
		if err != nil {
			if errors.Is(err, interfaces.ErrResourceNotFound) {
				// Ghost in state — cloud reports the resource does not exist.
				results = append(results, interfaces.DriftResult{
					Name:    ref.Name,
					Type:    ref.Type,
					Drifted: true,
					Class:   interfaces.DriftClassGhost,
				})
				continue
			}
			// Transient or unknown error — discard accumulated results and propagate.
			// Returning partial results is a footgun: callers that use both results
			// and err act on an incomplete drift picture, which may cause incorrect
			// state-prune decisions.
			return nil, fmt.Errorf("detect drift for %s/%s: %w", ref.Type, ref.Name, err)
		}

		// If the caller provided a desired spec for this ref, run Diff to detect
		// config-level drift. An empty specs map skips this path entirely.
		if spec, ok := driftSpecForRef(ref, specs); ok {
			diffResult, diffErr := d.Diff(ctx, spec, out)
			if diffErr != nil {
				return nil, fmt.Errorf("detect drift (config) for %s/%s: %w", ref.Type, ref.Name, diffErr)
			}
			if diffResult != nil && diffResult.NeedsUpdate {
				fields := make([]string, 0, len(diffResult.Changes))
				for _, c := range diffResult.Changes {
					fields = append(fields, c.Path)
				}
				results = append(results, interfaces.DriftResult{
					Name:    ref.Name,
					Type:    ref.Type,
					Drifted: true,
					Class:   interfaces.DriftClassConfig,
					Fields:  fields,
				})
				continue
			}
		}

		results = append(results, interfaces.DriftResult{
			Name:    ref.Name,
			Type:    ref.Type,
			Drifted: false,
			Class:   interfaces.DriftClassInSync,
		})
	}
	return results, nil
}

func driftSpecForRef(ref interfaces.ResourceRef, specs map[string]interfaces.ResourceSpec) (interfaces.ResourceSpec, bool) {
	if len(specs) == 0 {
		return interfaces.ResourceSpec{}, false
	}

	for _, key := range []string{ref.Type + "/" + ref.Name, ref.ProviderID} {
		if key == "" {
			continue
		}
		if spec, ok := specs[key]; ok && driftSpecMatchesRef(spec, ref) {
			return spec, true
		}
	}

	spec, ok := specs[ref.Name]
	if !ok || !driftSpecMatchesRef(spec, ref) {
		return interfaces.ResourceSpec{}, false
	}
	return spec, true
}

func driftSpecMatchesRef(spec interfaces.ResourceSpec, ref interfaces.ResourceRef) bool {
	if spec.Name != "" && spec.Name != ref.Name {
		return false
	}
	if spec.Type != "" && spec.Type != ref.Type {
		return false
	}
	return true
}

// Import brings an existing cloud resource under management.
func (p *DOProvider) Import(ctx context.Context, cloudID string, resourceType string) (*interfaces.ResourceState, error) {
	d, err := p.ResourceDriver(resourceType)
	if err != nil {
		return nil, err
	}
	ref := interfaces.ResourceRef{Name: cloudID, Type: resourceType, ProviderID: cloudID}
	out, err := d.Read(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("digitalocean import: %w", err)
	}
	now := time.Now()
	return &interfaces.ResourceState{
		ID:         cloudID,
		Name:       out.Name,
		Type:       out.Type,
		Provider:   "digitalocean",
		ProviderID: out.ProviderID,
		Outputs:    out.Outputs,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

// RevokeProviderCredential satisfies interfaces.ProviderCredentialRevoker for
// source "digitalocean.spaces". credentialID is the access_key_id of the OLD
// DO Spaces key to revoke. Used by `wfctl infra bootstrap --force-rotate` to
// invalidate the old key after minting its replacement (see ADR 0012).
//
// HTTP response handling:
//   - 204 No Content → success
//   - 404 Not Found  → treated as success (key already gone)
//   - 401/403        → auth error, propagated as fatal
//   - 5xx            → transient error, propagated to caller (caller logs + continues)
func (p *DOProvider) RevokeProviderCredential(ctx context.Context, source string, credentialID string) error {
	if source != "digitalocean.spaces" {
		return fmt.Errorf("digitalocean: RevokeProviderCredential: unknown source %q (only digitalocean.spaces is supported)", source)
	}
	if credentialID == "" {
		return fmt.Errorf("digitalocean: RevokeProviderCredential: credentialID is required")
	}
	if p.client == nil {
		return fmt.Errorf("digitalocean: RevokeProviderCredential: provider not initialized")
	}

	resp, err := p.client.SpacesKeys.Delete(ctx, credentialID)
	if err != nil {
		// 404 means the key is already gone — treat as success.
		if resp != nil && resp.StatusCode == 404 {
			return nil
		}
		return fmt.Errorf("digitalocean: revoke spaces key %q: %w", credentialID, err)
	}
	return nil
}

// EnumerateAll lists every resource of resourceType in the DO account,
// regardless of tag. Used for resource types that don't support tags
// (e.g. spaces_key, where the DO API has no tag surface). Paginates
// transparently using godo.Response.Links so callers don't have to.
//
// Returns []*ResourceOutput per the workflow EnumeratorAll contract
// (interfaces/iac_provider.go) — full metadata is populated so
// downstream callers (wfctl infra audit-keys, prune, rotate-and-prune)
// can filter without re-reading each resource individually.
//
// Implements interfaces.EnumeratorAll (workflow v0.26.0+; opt-in
// interface, type-asserted by the host's IaCProvider proxy).
func (p *DOProvider) EnumerateAll(ctx context.Context, resourceType string) ([]*interfaces.ResourceOutput, error) {
	if p.client == nil {
		return nil, fmt.Errorf("digitalocean: EnumerateAll called on provider that is not initialized — call Initialize first")
	}
	switch resourceType {
	case "infra.spaces_key":
		return p.enumerateAllSpacesKeys(ctx)
	default:
		return nil, fmt.Errorf("digitalocean: EnumerateAll: resource type %q not supported", resourceType)
	}
}

// enumerateAllSpacesKeys paginates GET /v2/spaces/keys via godo's SpacesKeys.List
// using ListOptions{Page,PerPage:200}; loop terminates when godo signals the
// last page (Links.Pages == nil || Pages.Next == ""). Each *ResourceOutput
// carries the name + access_key + created_at + grants so the audit/prune CLIs
// can filter by age or grant scope without an extra Get per key.
//
// ProviderID is set to AccessKey to match SpacesKeyDriver.Create's contract
// (sister driver in internal/drivers/spaces_key.go) — keeps Read/Delete
// dispatch consistent across the lifecycle.
func (p *DOProvider) enumerateAllSpacesKeys(ctx context.Context) ([]*interfaces.ResourceOutput, error) {
	var all []*interfaces.ResourceOutput
	opt := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		keys, resp, err := p.client.SpacesKeys.List(ctx, opt)
		if err != nil {
			return nil, fmt.Errorf("digitalocean: EnumerateAll spaces_key: list page=%d: %w", opt.Page, err)
		}
		for _, k := range keys {
			if k == nil {
				continue
			}
			outputs := map[string]any{
				"name":       k.Name,
				"access_key": k.AccessKey,
				"created_at": k.CreatedAt,
			}
			// grantsToMaps returns nil for empty/nil input — omit the key
			// entirely in that case so the docstring matches reality and
			// downstream consumers can use map-presence as the "has any
			// grants" signal.
			if g := grantsToMaps(k.Grants); g != nil {
				outputs["grants"] = g
			}
			all = append(all, &interfaces.ResourceOutput{
				Name:       k.Name,
				Type:       "infra.spaces_key",
				ProviderID: k.AccessKey, // godo identifier — used by Read/Delete
				Outputs:    outputs,
				Sensitive:  map[string]bool{"access_key": true},
				// Spaces keys don't have a lifecycle status in the godo API;
				// use "active" as the canonical "exists and usable" marker
				// to match SpacesKeyDriver.Create (internal/drivers/spaces_key.go)
				// and the sister bucket driver (internal/drivers/spaces.go).
				Status: "active",
			})
		}
		if resp == nil || resp.Links == nil || resp.Links.Pages == nil || resp.Links.Pages.Next == "" {
			break
		}
		page, err := resp.Links.CurrentPage()
		if err != nil {
			// Propagate the error rather than break-with-partial-result.
			// Silent break would cause prune/audit-keys to miss orphan keys
			// past the failed page boundary, leaving valid credentials in
			// place against DO indefinitely. Sister paginators (droplets,
			// volumes, databases) in EnumerateByTag take this same return-
			// on-error stance — match that contract.
			return nil, fmt.Errorf("digitalocean: EnumerateAll spaces_key: parse next page: %w", err)
		}
		opt.Page = page + 1
	}
	return all, nil
}

// grantsToMaps converts a []*godo.Grant to []map[string]any so it can live in
// ResourceOutput.Outputs (which is map[string]any, not a typed grants slice).
// Returns nil for empty/nil input so the Outputs entry is omitted-rather-than-empty.
func grantsToMaps(grants []*godo.Grant) []map[string]any {
	if len(grants) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(grants))
	for _, g := range grants {
		if g == nil {
			continue
		}
		out = append(out, map[string]any{
			"bucket":     g.Bucket,
			"permission": string(g.Permission),
		})
	}
	return out
}

// Close is a no-op; the godo client has no persistent connection to close.
func (p *DOProvider) Close() error { return nil }

// configHash returns a stable, deterministic hash of a config map.
// Keys are sorted before JSON serialisation so map iteration order does not affect the result.
func configHash(cfg map[string]any) string {
	if len(cfg) == 0 {
		return ""
	}
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	ordered := make([]any, 0, len(keys)*2)
	for _, k := range keys {
		ordered = append(ordered, k, cfg[k])
	}
	data, _ := json.Marshal(ordered)
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)
}
