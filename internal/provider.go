package internal

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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
	// and the referenced resources exist. Each driver type is flushed at
	// most once.
	seen := make(map[string]struct{}, len(plan.Actions))
	for _, action := range plan.Actions {
		if _, done := seen[action.Resource.Type]; done {
			continue
		}
		seen[action.Resource.Type] = struct{}{}
		d, dErr := p.ResourceDriver(action.Resource.Type)
		if dErr != nil {
			continue
		}
		du, ok := d.(deferredUpdater)
		if !ok || !du.HasDeferredUpdates() {
			continue
		}
		if flushErr := du.FlushDeferredUpdates(ctx); flushErr != nil {
			result.Errors = append(result.Errors, interfaces.ActionError{
				Resource: action.Resource.Name,
				Action:   "deferred_update",
				Error:    flushErr.Error(),
			})
		}
	}

	return result, nil
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
// Config-drift detection (DriftClassConfig) is out of scope here: the
// IaCProvider interface receives only refs, not the parsed declared config, so
// passing an empty ResourceSpec to driver Diff methods causes false positives
// (e.g. VPC reads ip_range from spec.Config; AppPlatform canonicalExpose
// defaults to "public" on an empty spec). Use `wfctl infra plan` for
// config-drift detection — it has access to the full declared spec and surfaces
// config drift as update actions.
//
// Production-safety invariant: only genuine 404s (wrapped with
// interfaces.ErrResourceNotFound) trigger the ghost path. Rate-limit, auth,
// or network errors propagate unchanged so callers cannot accidentally prune
// state on transient failures.
func (p *DOProvider) DetectDrift(ctx context.Context, resources []interfaces.ResourceRef) ([]interfaces.DriftResult, error) {
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

		_, err = d.Read(ctx, ref)
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

		// Read succeeded — classify as InSync. Config-drift detection routes
		// through `wfctl infra plan` which has access to the declared spec.
		results = append(results, interfaces.DriftResult{
			Name:    ref.Name,
			Type:    ref.Type,
			Drifted: false,
			Class:   interfaces.DriftClassInSync,
		})
	}
	return results, nil
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
