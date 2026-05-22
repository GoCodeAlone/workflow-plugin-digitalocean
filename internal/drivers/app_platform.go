// Package drivers contains ResourceDriver implementations for each DigitalOcean resource type.
package drivers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// troubleshootLogTailLines is the number of recent log lines fetched per
// component for failed deployments. 200 is enough to capture a typical
// panic/error trace without overwhelming the operator's terminal.
const troubleshootLogTailLines = 200

// ErrResourceNotFound is returned when a resource cannot be located by name or ID.
// It is an alias for interfaces.ErrResourceNotFound so that cross-package
// errors.Is checks work for the DetectDrift ghost-classification path.
var ErrResourceNotFound = interfaces.ErrResourceNotFound

// AppPlatformClient is the godo App interface used by AppPlatformDriver (for mocking).
type AppPlatformClient interface {
	Create(ctx context.Context, req *godo.AppCreateRequest) (*godo.App, *godo.Response, error)
	Get(ctx context.Context, appID string) (*godo.App, *godo.Response, error)
	List(ctx context.Context, opts *godo.ListOptions) ([]*godo.App, *godo.Response, error)
	Update(ctx context.Context, appID string, req *godo.AppUpdateRequest) (*godo.App, *godo.Response, error)
	CreateDeployment(ctx context.Context, appID string, req ...*godo.DeploymentCreateRequest) (*godo.Deployment, *godo.Response, error)
	ListDeployments(ctx context.Context, appID string, opts *godo.ListOptions) ([]*godo.Deployment, *godo.Response, error)
	Delete(ctx context.Context, appID string) (*godo.Response, error)
	GetLogs(ctx context.Context, appID, deploymentID, component string, logType godo.AppLogType, follow bool, tailLines int) (*godo.AppLogs, *godo.Response, error)
}

type appPlatformMigrationRepairClient interface {
	AppPlatformClient
	ListJobInvocations(ctx context.Context, appID string, opts *godo.ListJobInvocationsOptions) ([]*godo.JobInvocation, *godo.Response, error)
	GetJobInvocation(ctx context.Context, appID string, jobInvocationID string, opts *godo.GetJobInvocationOptions) (*godo.JobInvocation, *godo.Response, error)
	GetJobInvocationLogs(ctx context.Context, appID, jobInvocationID string, opts *godo.GetJobInvocationLogsOptions) (*godo.AppLogs, *godo.Response, error)
	CancelJobInvocation(ctx context.Context, appID, jobInvocationID string, opts *godo.CancelJobInvocationOptions) (*godo.JobInvocation, *godo.Response, error)
}

// AppPlatformDriver manages DigitalOcean App Platform (infra.container_service).
type AppPlatformDriver struct {
	client             AppPlatformClient
	regClient          RegistryClient // NEW: image-presence pre-flight; nil-safe (skips check if nil)
	dnsClient          DomainsClient
	region             string
	deploymentMu       sync.Mutex
	waitingDeployments map[string]*appDeploymentWaitState
}

type appDeploymentWaitState struct {
	targetDeploymentID         string
	previousActiveDeploymentID string
}

// NewAppPlatformDriver creates an AppPlatformDriver backed by a real godo client.
func NewAppPlatformDriver(c *godo.Client, region string) *AppPlatformDriver {
	return &AppPlatformDriver{client: c.Apps, regClient: c.Registry, dnsClient: c.Domains, region: region}
}

// NewAppPlatformDriverWithClient creates a driver with an injected apps client (for tests).
// regClient is nil; image-presence check is skipped.
func NewAppPlatformDriverWithClient(c AppPlatformClient, region string) *AppPlatformDriver {
	return &AppPlatformDriver{client: c, region: region}
}

// NewAppPlatformDriverWithClients creates a driver with both clients injected (for tests).
func NewAppPlatformDriverWithClients(c AppPlatformClient, r RegistryClient, region string) *AppPlatformDriver {
	return &AppPlatformDriver{client: c, regClient: r, region: region}
}

// NewAppPlatformDriverWithDNSClient creates a test driver with injected app and DNS clients.
func NewAppPlatformDriverWithDNSClient(c AppPlatformClient, dns DomainsClient, region string) *AppPlatformDriver {
	return &AppPlatformDriver{client: c, dnsClient: dns, region: region}
}

func (d *AppPlatformDriver) Create(ctx context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	if region, _ := spec.Config["region"].(string); region != "" {
		if err := validateAppPlatformRegion(region); err != nil {
			return nil, fmt.Errorf("app platform create %q: %w", spec.Name, err)
		}
	}

	if d.regClient != nil {
		if err := verifyImageConfigPresentInDOCR(ctx, d.regClient, spec.Config); err != nil {
			return nil, err
		}
	}

	region, _ := spec.Config["region"].(string)
	if region == "" {
		region = d.region
	}
	appSpec, err := buildAppSpec(spec.Name, spec.Config, region)
	if err != nil {
		return nil, fmt.Errorf("app platform build spec: %w", err)
	}

	app, _, err := d.client.Create(ctx, &godo.AppCreateRequest{Spec: appSpec})
	if err != nil {
		return nil, fmt.Errorf("app platform create %q: %w", spec.Name, WrapGodoError(err))
	}
	if app == nil || app.ID == "" {
		return nil, fmt.Errorf("app platform create %q: API returned app with empty ID", spec.Name)
	}
	if err := d.reconcileAppDomainCNAMEs(ctx, app, appSpec.Domains); err != nil {
		return nil, err
	}
	return appOutput(app), nil
}

// SupportsUpsert reports that AppPlatformDriver can locate a resource by name
// alone (empty ProviderID), enabling the ErrResourceAlreadyExists → upsert path
// in DOProvider.Apply. Other drivers that require ProviderID in Read do not
// implement this method and are excluded from the upsert path.
func (d *AppPlatformDriver) SupportsUpsert() bool { return true }

func (d *AppPlatformDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	if ref.ProviderID == "" {
		return d.findAppByName(ctx, ref.Name)
	}
	app, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("app platform read %q: %w", ref.Name, WrapGodoError(err))
	}
	return appOutput(app), nil
}

// findAppByName iterates the paginated app list and returns the first app whose
// Spec.Name matches name. Returns ErrResourceNotFound if no match is found.
func (d *AppPlatformDriver) findAppByName(ctx context.Context, name string) (*interfaces.ResourceOutput, error) {
	opts := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		apps, resp, err := d.client.List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("app platform list: %w", WrapGodoError(err))
		}
		for _, app := range apps {
			if app.Spec != nil && app.Spec.Name == name {
				return appOutput(app), nil
			}
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opts.Page++
	}
	return nil, fmt.Errorf("app %q: %w", name, ErrResourceNotFound)
}

func (d *AppPlatformDriver) Update(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	if region, _ := spec.Config["region"].(string); region != "" {
		if err := validateAppPlatformRegion(region); err != nil {
			return nil, fmt.Errorf("app platform update %q: %w", spec.Name, err)
		}
	}

	if d.regClient != nil {
		if err := verifyImageConfigPresentInDOCR(ctx, d.regClient, spec.Config); err != nil {
			return nil, err
		}
	}

	providerID, err := d.resolveProviderID(ctx, ref)
	if err != nil {
		return nil, err
	}

	region, _ := spec.Config["region"].(string)
	if region == "" {
		region = d.region
	}
	appSpec, err := buildAppSpec(spec.Name, spec.Config, region)
	if err != nil {
		return nil, fmt.Errorf("app platform build spec: %w", err)
	}

	current, _, err := d.client.Get(ctx, providerID)
	if err != nil {
		return nil, fmt.Errorf("app platform update %q: read current app: %w", ref.Name, WrapGodoError(err))
	}
	previousActiveDeploymentID := deploymentID(current.ActiveDeployment)

	app, _, err := d.client.Update(ctx, providerID, &godo.AppUpdateRequest{Spec: appSpec})
	if err != nil {
		return nil, fmt.Errorf("app platform update %q: %w", ref.Name, WrapGodoError(err))
	}
	if app == nil {
		return nil, fmt.Errorf("app platform update %q: API returned nil app", ref.Name)
	}
	targetDeployment := selectUpdateDeployment(app, previousActiveDeploymentID)
	if targetDeployment == nil && previousActiveDeploymentID == "" && appHasNoDeploymentSlots(app) {
		dep, _, err := d.client.CreateDeployment(ctx, providerID, &godo.DeploymentCreateRequest{})
		if err != nil {
			return nil, fmt.Errorf("app platform update %q: create deployment for undeployed app: %w", ref.Name, WrapGodoError(err))
		}
		targetDeployment = dep
		app.InProgressDeployment = dep
	}
	d.setUpdateDeploymentState(providerID, appDeploymentWaitState{
		previousActiveDeploymentID: previousActiveDeploymentID,
		targetDeploymentID:         deploymentID(targetDeployment),
	})
	if err := d.reconcileAppDomainCNAMEs(ctx, app, appSpec.Domains); err != nil {
		return nil, err
	}
	return appOutput(app), nil
}

func (d *AppPlatformDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	providerID, err := d.resolveProviderID(ctx, ref)
	if err != nil {
		return err
	}
	_, err = d.client.Delete(ctx, providerID)
	if err != nil {
		return fmt.Errorf("app platform delete %q: %w", ref.Name, WrapGodoError(err))
	}
	return nil
}

func (d *AppPlatformDriver) reconcileAppDomainCNAMEs(ctx context.Context, app *godo.App, domains []*godo.AppDomainSpec) error {
	if d.dnsClient == nil || app == nil || len(domains) == 0 {
		return nil
	}
	target := appDefaultIngressCNAME(app.DefaultIngress)
	if target == "" {
		return nil
	}
	for _, domain := range domains {
		if domain == nil || strings.TrimSpace(domain.Zone) == "" {
			continue
		}
		name, ok := appDomainDNSRecordName(domain.Domain, domain.Zone)
		if !ok {
			continue
		}
		if err := d.reconcileAppDomainCNAME(ctx, domain.Zone, name, target); err != nil {
			return fmt.Errorf("app platform reconcile DNS %q: %w", domain.Domain, err)
		}
	}
	return nil
}

func appHasNoDeploymentSlots(app *godo.App) bool {
	return app != nil && app.ActiveDeployment == nil && app.InProgressDeployment == nil && app.PendingDeployment == nil
}

func (d *AppPlatformDriver) reconcileAppDomainCNAME(ctx context.Context, zone, name, target string) error {
	records, err := listAppDomainRecords(ctx, d.dnsClient, zone)
	if err != nil {
		return err
	}
	var matching []godo.DomainRecord
	for _, record := range records {
		if !strings.EqualFold(record.Type, "CNAME") || record.Name != name {
			continue
		}
		matching = append(matching, record)
	}
	if len(matching) == 0 {
		_, _, err := d.dnsClient.CreateRecord(ctx, zone, &godo.DomainRecordEditRequest{
			Type: "CNAME",
			Name: name,
			Data: target,
			TTL:  1800,
		})
		if err != nil {
			return fmt.Errorf("dns create app domain CNAME %q/%s: %w", zone, name, WrapGodoError(err))
		}
		return nil
	}

	hasTarget := false
	for _, record := range matching {
		if dnsRecordDataEqual(record.Data, target) {
			hasTarget = true
			break
		}
	}

	keptTarget := hasTarget
	for i, record := range matching {
		if dnsRecordDataEqual(record.Data, target) {
			if keptTarget {
				keptTarget = false
				continue
			}
		}
		if !hasTarget && i == 0 {
			_, _, err := d.dnsClient.EditRecord(ctx, zone, record.ID, &godo.DomainRecordEditRequest{
				Type: "CNAME",
				Name: name,
				Data: target,
				TTL:  1800,
			})
			if err != nil {
				return fmt.Errorf("dns update app domain CNAME %q/%s: %w", zone, name, WrapGodoError(err))
			}
			hasTarget = true
			continue
		}
		if _, err := d.dnsClient.DeleteRecord(ctx, zone, record.ID); err != nil {
			return fmt.Errorf("dns delete stale app domain CNAME %q/%s id %d: %w", zone, name, record.ID, WrapGodoError(err))
		}
	}
	return nil
}

func listAppDomainRecords(ctx context.Context, client DomainsClient, domain string) ([]godo.DomainRecord, error) {
	opts := &godo.ListOptions{Page: 1, PerPage: 200}
	var out []godo.DomainRecord
	for {
		records, resp, err := client.Records(ctx, domain, opts)
		if err != nil {
			return nil, fmt.Errorf("dns list records %q: %w", domain, WrapGodoError(err))
		}
		out = append(out, records...)
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opts.Page++
	}
	return out, nil
}

func appDefaultIngressCNAME(defaultIngress string) string {
	defaultIngress = strings.TrimSpace(defaultIngress)
	if defaultIngress == "" {
		return ""
	}
	if u, err := url.Parse(defaultIngress); err == nil && u.Host != "" {
		defaultIngress = u.Host
	}
	defaultIngress = strings.TrimSuffix(defaultIngress, "/")
	defaultIngress = strings.TrimSuffix(defaultIngress, ".")
	if defaultIngress == "" {
		return ""
	}
	return strings.ToLower(defaultIngress) + "."
}

func appDomainDNSRecordName(domain, zone string) (string, bool) {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	zone = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zone)), ".")
	if domain == "" || zone == "" || domain == zone {
		return "", false
	}
	suffix := "." + zone
	if !strings.HasSuffix(domain, suffix) {
		return "", false
	}
	name := strings.TrimSuffix(domain, suffix)
	if name == "" {
		return "", false
	}
	return name, true
}

func dnsRecordDataEqual(a, b string) bool {
	return strings.EqualFold(strings.TrimSuffix(strings.TrimSpace(a), "."), strings.TrimSuffix(strings.TrimSpace(b), "."))
}

// resolveProviderID returns a UUID-like ProviderID for the given ref.
// If ref.ProviderID is already a valid UUID it is returned as-is.
// If it looks like a name (legacy stale state), a name-based lookup is
// performed and the real UUID is returned so callers never send a non-UUID
// path parameter to the DO API.
func (d *AppPlatformDriver) resolveProviderID(ctx context.Context, ref interfaces.ResourceRef) (string, error) {
	if isUUIDLike(ref.ProviderID) {
		return ref.ProviderID, nil
	}
	log.Printf("warn: app platform %q: ProviderID %q is not UUID-like; resolving by name (state-heal)",
		ref.Name, ref.ProviderID)
	out, err := d.findAppByName(ctx, ref.Name)
	if err != nil {
		return "", fmt.Errorf("app platform state-heal for %q: %w", ref.Name, err)
	}
	return out.ProviderID, nil
}

func (d *AppPlatformDriver) Diff(ctx context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	// Plan-time region validation: surface invalid App Platform region slugs
	// (e.g. Droplet datacenter "nyc1" used where regional "nyc" is required)
	// here, BEFORE Apply forwards the request to the DO API and gets back a
	// misleading "404 Image tag or digest not found".
	if region, _ := desired.Config["region"].(string); region != "" {
		if err := validateAppPlatformRegion(region); err != nil {
			return nil, fmt.Errorf("app platform diff %q: %w", desired.Name, err)
		}
	}

	if d.regClient != nil {
		if err := verifyImageConfigPresentInDOCR(ctx, d.regClient, desired.Config); err != nil {
			return nil, err
		}
	}

	if current == nil {
		return &interfaces.DiffResult{NeedsUpdate: true}, nil
	}
	var changes []interfaces.FieldChange
	if img, _ := desired.Config["image"].(string); img != "" {
		curImg, _ := current.Outputs["image"].(string)
		// Compare structurally: ParseImageRef into godo.ImageSourceSpec on
		// both sides and compare RegistryType+Registry+Repository+Tag. Falls
		// back to raw string equality when either side fails to parse, so
		// unparseable hand-written state still surfaces a change rather than
		// silently matching. Registry inclusion (round 4) catches GHCR /
		// DockerHub registry-org changes; for DOCR the Registry comparison
		// is benign because ParseImageRef discards the middle segment.
		// Round-3 Finding A — fixes spurious image diffs caused by the
		// lossy DOCR Registry round-trip.
		if !imageRefsEqual(img, curImg) {
			changes = append(changes, interfaces.FieldChange{Path: "image", Old: curImg, New: img})
		}
	}
	// Compare canonical `expose` so in-place public↔internal toggles produce a
	// Plan action rather than silently no-op'ing — quality-review F4 Finding 1.
	// Desired side is the canonical string from cfg (default "public" when
	// unset/empty). Current side is the value `appOutput` derived from the
	// live AppSpec at last Read.
	desiredExpose := canonicalExpose(desired.Config)
	curExpose, _ := current.Outputs["expose"].(string)
	if curExpose == "" {
		// Pre-F4 state has no `expose` recorded; treat absence as public so a
		// transition to `internal` is detected.
		curExpose = "public"
	}
	if desiredExpose != curExpose {
		changes = append(changes, interfaces.FieldChange{Path: "expose", Old: curExpose, New: desiredExpose})
	}

	// routes drift — App Platform routes live on the service component and
	// control public ingress. buildAppSpec already sends cfg["routes"] on
	// Create/Update; Diff must also compare them or existing apps never gain a
	// route added after first create. Skip when desired omits routes so older
	// configs do not unexpectedly manage provider defaults after upgrade.
	if desiredRoutes, ok := desiredRoutesCanonicalFromConfig(desired.Config); ok {
		if canonicalExpose(desired.Config) == "internal" {
			desiredRoutes = nil
		}
		curRoutes := routesCanonicalFromOutput(current.Outputs["routes"])
		if _, hasKey := current.Outputs["routes"]; !hasKey {
			changes = append(changes, interfaces.FieldChange{
				Path: "routes", Old: "<unknown>", New: desiredRoutes,
			})
		} else if !routesCanonicalEqual(desiredRoutes, curRoutes) {
			changes = append(changes, interfaces.FieldChange{
				Path: "routes", Old: curRoutes, New: desiredRoutes,
			})
		}
	}

	// region: App Platform's UpdateApp does not accept region changes —
	// region is a Create-only field on godo.AppSpec, so any drift forces
	// replace. Mirror VolumeDriver.Diff's region pattern. Guard
	// curRegion != "" so apps in state from earlier plugin versions
	// (when appOutput didn't include region) don't false-positive on
	// the first plan after upgrade — they'll Read on next apply to
	// populate the field.
	var needsReplace bool
	if region := strFromConfig(desired.Config, "region", ""); region != "" {
		curRegion, _ := current.Outputs["region"].(string)
		if curRegion != "" && curRegion != region {
			changes = append(changes, interfaces.FieldChange{
				Path: "region", Old: curRegion, New: region, ForceNew: true,
			})
			needsReplace = true
		}
	}

	// env_vars / jobs / workers env_vars drift — critical for cascade
	// propagation when an `infra_output:` secret resolves to a new value
	// (e.g. Droplet replace bumping STAGING_PG_HOST → all components
	// referencing it via env_vars need a new rollout). Without these
	// comparisons the App's state.config froze with the first-resolved
	// value and Plan stayed at 0 changes on subsequent Applies.
	//
	// Guards: skip comparison when the corresponding current.Outputs key
	// is absent (state predates this driver version) so a stale state
	// from before doesn't false-positive on the first Plan after upgrade
	// — same pattern Droplet's monitoring/ipv6 comparisons use. Outputs
	// will be populated on the next Read.
	// Hash-based comparison: state.Outputs stores SHA-256 hashes only
	// (never plaintext values — env_vars may contain JIT-substituted
	// secrets). Diff side computes the desired-side hash via the same
	// algorithm and compares strings. FieldChange Old/New are the
	// HASHES, never the values themselves, so the changelog Path
	// communicates "drift detected" without revealing what changed.
	if desiredEnvs, ok := desired.Config["env_vars"].(map[string]any); ok {
		if curHash, hasKey := current.Outputs["env_vars_hash"].(string); hasKey {
			desiredHash := envVarsHashFromConfigMap(desiredEnvs)
			if desiredHash != curHash {
				changes = append(changes, interfaces.FieldChange{
					Path: "env_vars", Old: "[hash:" + curHash[:8] + "...]", New: "[hash:" + desiredHash[:8] + "...]",
				})
			}
		}
	}
	if desiredJobs, ok := desired.Config["jobs"].([]any); ok {
		if curHashes, hasKey := current.Outputs["jobs_hash"].(map[string]any); hasKey {
			desiredHashes := componentHashesByName(desiredJobs)
			if !hashMapsEqual(desiredHashes, curHashes) {
				changes = append(changes, interfaces.FieldChange{
					Path: "jobs", Old: "[hash-map]", New: "[hash-map]",
				})
			}
		}
	}
	if desiredWorkers, ok := desired.Config["workers"].([]any); ok {
		if curHashes, hasKey := current.Outputs["workers_hash"].(map[string]any); hasKey {
			desiredHashes := componentHashesByName(desiredWorkers)
			if !hashMapsEqual(desiredHashes, curHashes) {
				changes = append(changes, interfaces.FieldChange{
					Path: "workers", Old: "[hash-map]", New: "[hash-map]",
				})
			}
		}
	}

	// vpc_uuid / vpc_ref drift — App Platform Update accepts VPC
	// attachment changes in-place (not ForceNew). vpc_ref is the
	// canonical config key (matches Droplet's vpc_uuid alias pattern);
	// also accept vpc_uuid for symmetry.
	desiredVPC := strFromConfig(desired.Config, "vpc_ref", "")
	if desiredVPC == "" {
		desiredVPC = strFromConfig(desired.Config, "vpc_uuid", "")
	}
	if _, hasKey := current.Outputs["vpc_uuid"]; hasKey {
		curVPC, _ := current.Outputs["vpc_uuid"].(string)
		if curVPC != desiredVPC {
			changes = append(changes, interfaces.FieldChange{
				Path: "vpc_uuid", Old: curVPC, New: desiredVPC,
			})
		}
	}

	return &interfaces.DiffResult{
		NeedsUpdate:  len(changes) > 0,
		NeedsReplace: needsReplace,
		Changes:      changes,
	}, nil
}

// componentHashesByName indexes a desired-side jobs/workers list into a
// name → hash map so Diff can compare against current.Outputs[*_hash]
// (which is already name-keyed).
func componentHashesByName(comps []any) map[string]any {
	out := make(map[string]any, len(comps))
	for _, c := range comps {
		m, _ := c.(map[string]any)
		if m == nil {
			continue
		}
		name, _ := m["name"].(string)
		if name == "" {
			continue
		}
		out[name] = componentHashFromConfig(m)
	}
	return out
}

// hashMapsEqual reports whether two name→hash maps describe the same
// set of components. Used by jobs/workers hash drift detection.
func hashMapsEqual(desired, current map[string]any) bool {
	if len(desired) != len(current) {
		return false
	}
	for name, dh := range desired {
		ch, ok := current[name]
		if !ok {
			return false
		}
		if dh != ch {
			return false
		}
	}
	return true
}

// canonicalExpose returns the canonical `expose` value for a desired-spec
// config: "public" (default when unset/empty), "internal", or whatever
// non-empty string the user supplied. Validation of the enum is performed
// later in `applyExposeInternal` during buildAppSpec.
func canonicalExpose(cfg map[string]any) string {
	v, ok := cfg["expose"].(string)
	if !ok {
		return "public"
	}
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "public"
	}
	return v
}

func (d *AppPlatformDriver) HealthCheck(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	providerID, err := d.resolveProviderID(ctx, ref)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	app, _, err := d.client.Get(ctx, providerID)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	if d.hasUpdateDeploymentState(providerID) {
		dep, previousActiveDeploymentID, err := d.currentTargetDeployment(ctx, providerID, app)
		if err != nil {
			return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
		}
		if dep == nil {
			return &interfaces.HealthResult{Healthy: false, Message: fmt.Sprintf("waiting for deployment after update: previous active %s", previousActiveDeploymentID)}, nil
		}
		if err := deploymentHealthError(ref.Name, dep); err != nil {
			return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
		}
		if err := d.reconcileAppDomainCNAMEs(ctx, app, appDomains(app)); err != nil {
			return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
		}
		d.clearUpdateDeploymentState(providerID)
		return &interfaces.HealthResult{Healthy: true}, nil
	}
	listFn := func(ctx context.Context, appID string) ([]*godo.Deployment, error) {
		deps, _, err := d.client.ListDeployments(ctx, appID, &godo.ListOptions{Page: 1, PerPage: 1})
		return deps, err
	}
	result := appHealthResult(ctx, listFn, app)
	if result != nil && result.Healthy {
		if err := d.reconcileAppDomainCNAMEs(ctx, app, appDomains(app)); err != nil {
			return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
		}
	}
	return result, nil
}

func appDomains(app *godo.App) []*godo.AppDomainSpec {
	if app == nil || app.Spec == nil {
		return nil
	}
	return app.Spec.Domains
}

func (d *AppPlatformDriver) setUpdateDeploymentState(providerID string, state appDeploymentWaitState) {
	d.deploymentMu.Lock()
	defer d.deploymentMu.Unlock()
	if d.waitingDeployments == nil {
		d.waitingDeployments = make(map[string]*appDeploymentWaitState)
	}
	stateCopy := state
	d.waitingDeployments[providerID] = &stateCopy
}

func (d *AppPlatformDriver) hasUpdateDeploymentState(providerID string) bool {
	d.deploymentMu.Lock()
	defer d.deploymentMu.Unlock()
	return d.waitingDeployments != nil && d.waitingDeployments[providerID] != nil
}

func (d *AppPlatformDriver) clearUpdateDeploymentState(providerID string) {
	d.deploymentMu.Lock()
	defer d.deploymentMu.Unlock()
	delete(d.waitingDeployments, providerID)
}

func (d *AppPlatformDriver) currentTargetDeployment(ctx context.Context, providerID string, app *godo.App) (*godo.Deployment, string, error) {
	d.deploymentMu.Lock()
	defer d.deploymentMu.Unlock()
	state := d.waitingDeployments[providerID]
	if state == nil {
		return nil, "", nil
	}
	if dep := selectUpdateDeployment(app, state.previousActiveDeploymentID); dep != nil {
		if state.targetDeploymentID == "" || dep.ID == state.targetDeploymentID {
			state.targetDeploymentID = dep.ID
			return dep, state.previousActiveDeploymentID, nil
		}
		if dep.PreviousDeploymentID == state.targetDeploymentID {
			state.targetDeploymentID = dep.ID
			return dep, state.previousActiveDeploymentID, nil
		}
	}
	deployments, _, err := d.client.ListDeployments(ctx, providerID, &godo.ListOptions{Page: 1, PerPage: 20})
	if err != nil {
		return nil, state.previousActiveDeploymentID, fmt.Errorf("app platform health check %q: list deployments: %w", providerID, WrapGodoError(err))
	}
	for _, dep := range deployments {
		if dep == nil {
			continue
		}
		if state.targetDeploymentID != "" && dep.ID == state.targetDeploymentID {
			return dep, state.previousActiveDeploymentID, nil
		}
		if state.targetDeploymentID == "" && isHistoricalUpdateDeployment(dep, state.previousActiveDeploymentID) {
			state.targetDeploymentID = dep.ID
			return dep, state.previousActiveDeploymentID, nil
		}
	}
	return nil, state.previousActiveDeploymentID, nil
}

// listDeploymentsFn is a function that returns the most recent deployment(s)
// for an app. It is passed to appHealthResult so the fallback path can be
// tested without coupling appHealthResult to a concrete driver type.
type listDeploymentsFn func(ctx context.Context, appID string) ([]*godo.Deployment, error)

// appHealthResult evaluates all three DO deployment slots in priority order and
// returns an accurate HealthResult:
//
//   - ActiveDeployment ACTIVE                    → Healthy=true
//   - ActiveDeployment transitioning (non-Active) → Healthy=false, "deployment in progress (active slot): <phase>" or "deployment failed (active slot): <phase>"
//   - InProgressDeployment (building)            → Healthy=false, "deployment in progress: <phase>"
//   - InProgressDeployment (failed)              → Healthy=false, "deployment failed: <phase>"
//   - PendingDeployment                          → Healthy=false, "deployment queued"
//   - none of the above, with history            → Healthy=false, "latest deployment <id>: <phase>"
//   - none of the above, no history              → Healthy=false, "no deployment found"
//
// listFn is called only when all three slots are nil; it may be nil (in which
// case the ListDeployments fallback is skipped and "no deployment found" is
// returned immediately).
func appHealthResult(ctx context.Context, listFn listDeploymentsFn, app *godo.App) *interfaces.HealthResult {
	// 1. Active and healthy.
	if app.ActiveDeployment != nil && app.ActiveDeployment.Phase == godo.DeploymentPhase_Active {
		return &interfaces.HealthResult{Healthy: true}
	}

	// 2a. ActiveDeployment populated but Phase not yet Active — covers the
	// post-promotion-pre-active window where DO has moved the deployment out of
	// the InProgressDeployment slot but its Phase is still transitioning toward
	// Active. Without this check, the polling loop falls through to "no
	// deployment found" and loops until timeout (issue #48).
	if dep := app.ActiveDeployment; dep != nil {
		switch dep.Phase {
		case godo.DeploymentPhase_PendingBuild,
			godo.DeploymentPhase_Building,
			godo.DeploymentPhase_PendingDeploy,
			godo.DeploymentPhase_Deploying:
			return &interfaces.HealthResult{
				Healthy: false,
				Message: fmt.Sprintf("deployment in progress (active slot): %s", dep.Phase),
			}
		case godo.DeploymentPhase_Error,
			godo.DeploymentPhase_Canceled,
			godo.DeploymentPhase_Superseded:
			return &interfaces.HealthResult{
				Healthy: false,
				Message: fmt.Sprintf("deployment failed (active slot): %s", dep.Phase),
			}
		default:
			// Forward-compat: a future godo release may add new phases.
			// Report "unknown" rather than "failed" to avoid mislabeling.
			return &interfaces.HealthResult{
				Healthy: false,
				Message: fmt.Sprintf("unknown phase (active slot): %s", dep.Phase),
			}
		}
	}

	// 2b. Deployment currently in progress in the InProgress slot — inspect its phase.
	if dep := app.InProgressDeployment; dep != nil {
		switch dep.Phase {
		case godo.DeploymentPhase_PendingBuild,
			godo.DeploymentPhase_Building,
			godo.DeploymentPhase_PendingDeploy,
			godo.DeploymentPhase_Deploying:
			return &interfaces.HealthResult{
				Healthy: false,
				Message: fmt.Sprintf("deployment in progress: %s", dep.Phase),
			}
		case godo.DeploymentPhase_Error,
			godo.DeploymentPhase_Canceled,
			godo.DeploymentPhase_Superseded:
			return &interfaces.HealthResult{
				Healthy: false,
				Message: fmt.Sprintf("deployment failed: %s", dep.Phase),
			}
		default:
			// Forward-compat: a future godo release may add new phases.
			// Report "unknown" rather than "failed" to avoid mislabeling.
			return &interfaces.HealthResult{
				Healthy: false,
				Message: fmt.Sprintf("unknown phase: %s", dep.Phase),
			}
		}
	}

	// 3. Deployment queued but not yet started.
	if app.PendingDeployment != nil {
		return &interfaces.HealthResult{Healthy: false, Message: "deployment queued"}
	}

	// 4. All three slots empty — fall back to deployment history. This catches the
	// case where DO has removed a failed/superseded deployment from all 3 slots
	// before the next one starts (e.g. fast-fail at build time leaves the app
	// with no current deployment but recent history).
	if listFn != nil {
		if hist, err := listFn(ctx, app.ID); err == nil && len(hist) > 0 {
			latest := hist[0]
			return &interfaces.HealthResult{
				Healthy: false,
				Message: fmt.Sprintf("latest deployment %s: %s", shortDepID(latest.ID), latest.Phase),
			}
		}
	}

	// No deployment slot AND no history available.
	return &interfaces.HealthResult{Healthy: false, Message: "no deployment found"}
}

// shortDepID truncates a deployment UUID to its first 8 characters for
// log readability (e.g. "f8b6200c" instead of the full UUID).
func shortDepID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func (d *AppPlatformDriver) Scale(ctx context.Context, ref interfaces.ResourceRef, replicas int) (*interfaces.ResourceOutput, error) {
	providerID, err := d.resolveProviderID(ctx, ref)
	if err != nil {
		return nil, err
	}
	app, _, err := d.client.Get(ctx, providerID)
	if err != nil {
		return nil, fmt.Errorf("app platform scale read %q: %w", ref.Name, WrapGodoError(err))
	}
	spec := app.Spec
	for _, svc := range spec.Services {
		svc.InstanceCount = int64(replicas)
	}
	updated, _, err := d.client.Update(ctx, providerID, &godo.AppUpdateRequest{Spec: spec})
	if err != nil {
		return nil, fmt.Errorf("app platform scale update %q: %w", ref.Name, WrapGodoError(err))
	}
	return appOutput(updated), nil
}

// troubleshootMaxDeployments is the maximum number of recent historical
// deployments fetched by Troubleshoot (slot-based candidates are always
// included; this cap applies separately to the historical listing pass).
const troubleshootMaxDeployments = 5

// Troubleshoot implements interfaces.Troubleshooter for AppPlatformDriver.
// It fetches the app (to inspect its three deployment slots: InProgress,
// Pending, Active) plus recent historical deployments, prioritises them, and
// returns per-deployment Diagnostics so wfctl can render a structured failure
// block on health-check timeout without requiring a DO console visit.
//
// For each candidate deployment in a terminal error phase (Error, Canceled,
// Superseded), Troubleshoot fetches deploy logs via GetLogs and appends the
// last troubleshootLogTailLines of output per component to the Diagnostic.Detail.
// Log fetch failures are best-effort: the Diagnostic is still produced without
// the log block when GetLogs or the HTTP fetch fails.
func (d *AppPlatformDriver) Troubleshoot(ctx context.Context, ref interfaces.ResourceRef, _ string) ([]interfaces.Diagnostic, error) {
	if ref.ProviderID == "" {
		return nil, nil
	}
	app, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("troubleshoot: get app %q: %w", ref.Name, WrapGodoError(err))
	}
	if app == nil {
		return nil, nil
	}
	hist, _, _ := d.client.ListDeployments(ctx, ref.ProviderID, &godo.ListOptions{Page: 1, PerPage: troubleshootMaxDeployments})
	candidates := pickTroubleshootDeployments(app, hist)
	if len(candidates) == 0 {
		return nil, nil
	}
	var out []interfaces.Diagnostic
	for _, dep := range candidates {
		diag := buildDiagnosticFor(dep)
		if diag == nil {
			continue
		}
		// Attach deploy/build logs for terminal error phases.
		if isTerminalErrorPhase(dep.Phase) {
			d.attachDeployLogs(ctx, ref.ProviderID, dep, diag)
		}
		out = append(out, *diag)
	}
	return out, nil
}

// isTerminalErrorPhase reports whether a deployment phase indicates a terminal
// failure for which fetching logs is useful.
func isTerminalErrorPhase(phase godo.DeploymentPhase) bool {
	switch phase {
	case godo.DeploymentPhase_Error,
		godo.DeploymentPhase_Canceled,
		godo.DeploymentPhase_Superseded:
		return true
	}
	return false
}

// attachDeployLogs fetches DO deploy (or build) logs for a failed deployment
// and appends delimited log blocks to diag.Detail. One block per component.
// All four failure modes (GetLogs API error, HTTP-fetch error, empty
// HistoricURLs, empty body) also append a brief failure note to diag.Detail so
// the cause is visible in operator-facing output. The existing stderr writes
// are preserved for plugin-debug but are captured at TRACE level by
// hashicorp/go-plugin and not surfaced to operators by default.
func (d *AppPlatformDriver) attachDeployLogs(ctx context.Context, appID string, dep *godo.Deployment, diag *interfaces.Diagnostic) {
	logType := chooseLogType(dep)
	components := deploymentComponents(dep)

	header := "Deploy logs"
	if logType == godo.AppLogTypeBuild {
		header = "Build logs"
	}

	for _, comp := range components {
		label := comp
		if label == "" {
			label = "<all>"
		}

		logs, _, err := d.client.GetLogs(ctx, appID, dep.ID, comp, logType, false, troubleshootLogTailLines)
		if err != nil {
			fmt.Fprintf(os.Stderr, "troubleshoot: GetLogs app=%s dep=%s component=%q: %v\n", appID, dep.ID, comp, err)
			diag.Detail += fmt.Sprintf("\n\n---\n%s — component %q: log fetch unavailable (GetLogs API error: %v)\n---", header, label, err)
			continue
		}
		if logs == nil || len(logs.HistoricURLs) == 0 {
			diag.Detail += fmt.Sprintf("\n\n---\n%s — component %q: no historic logs returned by DO API\n---", header, label)
			continue
		}
		tail, err := fetchLogTail(ctx, logs.HistoricURLs, troubleshootLogTailLines)
		if err != nil {
			// Don't print err verbatim — net/http error strings embed the
			// presigned URL, leaking AWS-style signed credentials to the
			// operator's terminal log. Surface only the redacted shape.
			fmt.Fprintf(os.Stderr, "troubleshoot: log HTTP fetch app=%s dep=%s component=%q: %s (presigned URL not logged)\n", appID, dep.ID, comp, redactURLError(err))
			diag.Detail += fmt.Sprintf("\n\n---\n%s — component %q: log fetch failed (%s)\n---", header, label, redactURLError(err))
			continue
		}
		if tail == "" {
			diag.Detail += fmt.Sprintf("\n\n---\n%s — component %q: log fetch returned empty body\n---", header, label)
			continue
		}
		diag.Detail += fmt.Sprintf("\n\n---\n%s — component %q (last %d lines):\n%s\n---", header, label, troubleshootLogTailLines, tail)
	}
}

// redactURLError returns an error string with any embedded URL replaced by
// "<redacted-url>". DO's GetLogs returns presigned HistoricURLs that embed
// signed credentials; net/http errors typically include the full request URL,
// so verbatim logging would leak short-lived creds to the operator's log.
func redactURLError(err error) string {
	if err == nil {
		return ""
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		// url.Error.Error() = "Op URL: Err". Hide the URL.
		return fmt.Sprintf("%s <redacted-url>: %v", ue.Op, ue.Err)
	}
	return err.Error()
}

// chooseLogType returns AppLogTypeBuild when the deployment's Progress
// indicates a build-phase failure (a SummaryStep named "build" has status
// Error), and AppLogTypeDeploy otherwise. Defaults to AppLogTypeDeploy when
// Progress is nil or no build step error is found.
func chooseLogType(dep *godo.Deployment) godo.AppLogType {
	if dep.Progress == nil {
		return godo.AppLogTypeDeploy
	}
	for _, step := range dep.Progress.SummarySteps {
		if step.Status == godo.DeploymentProgressStepStatus_Error &&
			strings.EqualFold(step.Name, "build") {
			return godo.AppLogTypeBuild
		}
	}
	return godo.AppLogTypeDeploy
}

// deploymentComponents returns the list of component names to fetch logs for.
// It prefers the deployment-level arrays (Services, StaticSites, Workers, Jobs,
// Functions) which are populated by both ListDeployments and GetDeployment.
// If those are all empty it falls back to dep.Spec.* (populated by richer
// GetDeployment calls that include the full spec), and finally to [""] — the
// DO API treats component="" as "all components aggregated".
func deploymentComponents(dep *godo.Deployment) []string {
	var names []string

	// 1. Prefer deployment-level arrays: populated by ListDeployments and
	// GetDeployment alike. dep.Spec may be nil from ListDeployments, but these
	// fields are always present when the deployment has components.
	for _, svc := range dep.Services {
		if svc != nil && svc.Name != "" {
			names = append(names, svc.Name)
		}
	}
	for _, ss := range dep.StaticSites {
		if ss != nil && ss.Name != "" {
			names = append(names, ss.Name)
		}
	}
	for _, w := range dep.Workers {
		if w != nil && w.Name != "" {
			names = append(names, w.Name)
		}
	}
	for _, job := range dep.Jobs {
		if job != nil && job.Name != "" {
			names = append(names, job.Name)
		}
	}
	for _, fn := range dep.Functions {
		if fn != nil && fn.Name != "" {
			names = append(names, fn.Name)
		}
	}
	if len(names) > 0 {
		return names
	}

	// 2. Fallback: Spec-level arrays populated by a richer GetDeployment call
	// that includes the full AppSpec. Present when dep came from CreateDeployment
	// or a single-resource Get, absent from ListDeployments.
	if dep.Spec != nil {
		for _, svc := range dep.Spec.Services {
			if svc != nil && svc.Name != "" {
				names = append(names, svc.Name)
			}
		}
		for _, ss := range dep.Spec.StaticSites {
			if ss != nil && ss.Name != "" {
				names = append(names, ss.Name)
			}
		}
		for _, job := range dep.Spec.Jobs {
			if job != nil && job.Name != "" {
				names = append(names, job.Name)
			}
		}
		for _, w := range dep.Spec.Workers {
			if w != nil && w.Name != "" {
				names = append(names, w.Name)
			}
		}
		for _, fn := range dep.Spec.Functions {
			if fn != nil && fn.Name != "" {
				names = append(names, fn.Name)
			}
		}
		if len(names) > 0 {
			return names
		}
	}

	// 3. Last resort: aggregate ("" = DO API returns logs across all components).
	return []string{""}
}

// fetchLogTail fetches log content from the most recent historic URL (index 0)
// and returns the last tailLines lines. HistoricURLs is ordered newest-first
// per the DO API. A 10 MB cap prevents pathological responses from exhausting
// memory. The local 30s client timeout is a defense in depth: callers
// typically pass a ctx with a deadline, but if a future caller passes
// context.Background, http.DefaultClient (no timeout) would let the fetch
// hang indefinitely on a slow presigned URL.
var fetchLogClient = &http.Client{Timeout: 30 * time.Second}

func fetchLogTail(ctx context.Context, urls []string, tailLines int) (string, error) {
	if len(urls) == 0 {
		return "", nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", urls[0], nil)
	if err != nil {
		return "", err
	}
	resp, err := fetchLogClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("log fetch HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024)) // 10 MB cap
	if err != nil {
		return "", err
	}
	return tailString(string(body), tailLines), nil
}

// tailString returns the last n lines of s. If s has fewer than n lines, the
// entire string is returned.
func tailString(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// pickTroubleshootDeployments returns up to 3 candidate deployments in priority
// order: InProgress > Pending > Active, followed by unique historical entries.
// All three deployment slots (InProgress, Pending, Active) are collected in
// priority order; historical deployments fill remaining capacity up to 3 total.
func pickTroubleshootDeployments(app *godo.App, historical []*godo.Deployment) []*godo.Deployment {
	seen := map[string]bool{}
	var result []*godo.Deployment
	add := func(dep *godo.Deployment) {
		if dep == nil || dep.ID == "" || seen[dep.ID] {
			return
		}
		seen[dep.ID] = true
		result = append(result, dep)
	}
	add(app.InProgressDeployment)
	add(app.PendingDeployment)
	add(app.ActiveDeployment)
	for _, dep := range historical {
		if len(result) >= 3 {
			break
		}
		add(dep)
	}
	return result
}

// buildDiagnosticFor synthesises a Diagnostic from a single deployment's state.
// It scans Progress.SummarySteps for a failed phase name (e.g. "pre_deploy",
// "build") first, then falls back to leaf Progress.Steps, and finally to the
// deployment-level Cause field.
// Returns nil when there is nothing actionable to report (active/healthy).
func buildDiagnosticFor(dep *godo.Deployment) *interfaces.Diagnostic {
	cause, phase := deploymentCauseAndPhase(dep)
	if cause == "" {
		return nil
	}
	return &interfaces.Diagnostic{
		ID:    dep.ID,
		Phase: phase,
		Cause: cause,
		At:    dep.CreatedAt,
	}
}

// deploymentCauseAndPhase extracts the most actionable (cause, phase) pair.
// Priority: SummarySteps error > Steps error > dep.Cause > terminal phase.
func deploymentCauseAndPhase(dep *godo.Deployment) (cause, phase string) {
	if dep.Progress != nil {
		// SummarySteps carry DO's high-level phase names ("pre_deploy", "build", …).
		for _, step := range dep.Progress.SummarySteps {
			if step.Status == godo.DeploymentProgressStepStatus_Error {
				msg := ""
				if step.Reason != nil {
					msg = step.Reason.Message
				}
				if msg == "" {
					msg = dep.Cause
				}
				if msg != "" {
					return extractCause(msg), step.Name
				}
			}
		}
		// Fall back to leaf-level steps.
		for _, step := range dep.Progress.Steps {
			if step.Status == godo.DeploymentProgressStepStatus_Error &&
				step.Reason != nil && step.Reason.Message != "" {
				return extractCause(step.Reason.Message), string(dep.Phase)
			}
		}
	}
	if dep.Cause != "" {
		return dep.Cause, string(dep.Phase)
	}
	// Report explicitly terminal phases even without a message.
	switch dep.Phase {
	case godo.DeploymentPhase_Error, godo.DeploymentPhase_Canceled:
		return string(dep.Phase), string(dep.Phase)
	}
	return "", ""
}

// extractCause scans a log tail (or a short message) for common error
// patterns and returns the first matching line. Falls back to the last
// non-empty line when no pattern matches.
func extractCause(tail string) string {
	patterns := []string{
		"Error:", "error:", "exit status", "exit code",
		"failed to", "panic:", "fatal:", "FATAL",
	}
	for _, line := range strings.Split(tail, "\n") {
		for _, p := range patterns {
			if strings.Contains(line, p) {
				return strings.TrimSpace(line)
			}
		}
	}
	// Fallback: last non-empty line.
	lines := strings.Split(strings.TrimRight(tail, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return s
		}
	}
	return ""
}

func appOutput(app *godo.App) *interfaces.ResourceOutput {
	out := &interfaces.ResourceOutput{
		Name:       app.Spec.Name,
		Type:       "infra.container_service",
		ProviderID: app.ID,
		Outputs: map[string]any{
			"live_url": app.LiveURL,
			// `expose` is derived from the first service component:
			// HTTPPort==0 with InternalPorts populated → "internal";
			// otherwise "public". Stored on Outputs so Diff can detect
			// in-place public↔internal toggles — F4 Finding 1.
			"expose": deriveExposeFromAppSpec(app.Spec),
			// `image` is derived from the first service's ImageSourceSpec
			// and formatted as a canonical user-facing ref. Stored on
			// Outputs so Diff can structurally compare against the user's
			// desired `cfg["image"]` and avoid emitting spurious image
			// FieldChanges on no-op reconciles — F4 round-3 Finding A.
			"image": deriveImageFromAppSpec(app.Spec),
			// `region` is the App Platform region from AppSpec.Region.
			// Stored on Outputs so Diff can detect region drift and
			// surface it as ForceNew (App Platform's UpdateApp does not
			// accept region changes — region is a Create-only field).
			"region": app.Spec.Region,
			// env_vars / jobs / workers env_vars drift-detection — surfaced
			// as content HASHES (sha256 over canonical-sorted JSON) instead
			// of raw maps. v1.0.5 stored the maps verbatim, but the values
			// come back from DO API JIT-resolved: a `${STAGING_PG_PASSWORD}`
			// placeholder in infra.yaml that wfctl substituted before
			// sending to DO surfaced in state.outputs as the plaintext
			// password (state file lives in DO Spaces — accessible to
			// anyone with the Spaces creds + via `wfctl infra outputs`).
			// Hashing keeps Diff drift-detection (hash mismatch →
			// NeedsUpdate) while never persisting plaintext secret values
			// to state. Hashes also stabilize the structpb-round-trip:
			// the same env_vars always hash to the same string regardless
			// of map iteration order.
			"env_vars_hash": serviceEnvVarsHashFromSpec(app.Spec),
			"jobs_hash":     jobsHashFromSpec(app.Spec),
			"workers_hash":  workersHashFromSpec(app.Spec),
			// `routes` records the first service component's public ingress
			// paths. Stored on Outputs so Diff can add/remove routes on
			// existing apps instead of only honoring them at Create time.
			"routes": routesCanonicalFromSpec(app.Spec),
			// `vpc_uuid` records the App's VPC attachment so Diff can
			// detect attachment drift. App Platform Update accepts VPC
			// changes in-place; ForceNew not required.
			"vpc_uuid": vpcUUIDFromSpec(app.Spec),
		},
		Status: "running",
	}
	if app.ActiveDeployment == nil {
		out.Status = "pending"
	}
	return out
}

// desiredRoutesCanonicalFromConfig converts cfg["routes"] into the same
// canonical shape produced from DO AppSpec. The boolean distinguishes absent
// routes (do not manage on upgrade) from an explicit empty list (clear routes).
func desiredRoutesCanonicalFromConfig(cfg map[string]any) ([]any, bool) {
	raw, ok := cfg["routes"].([]any)
	if !ok {
		return nil, false
	}
	return routesCanonicalFromConfigList(raw), true
}

func routesCanonicalFromConfigList(raw []any) []any {
	if len(raw) == 0 {
		return nil
	}
	out := make([]any, 0, len(raw))
	for _, v := range raw {
		m, _ := v.(map[string]any)
		if m == nil {
			continue
		}
		path, _ := m["path"].(string)
		if path == "" {
			path = "/"
		}
		preservePathPrefix, _ := m["preserve_path_prefix"].(bool)
		out = append(out, routeCanonicalMap(path, preservePathPrefix))
	}
	return out
}

func routesCanonicalFromOutput(v any) []any {
	raw, _ := v.([]any)
	if len(raw) == 0 {
		return nil
	}
	out := make([]any, 0, len(raw))
	for _, item := range raw {
		m, _ := item.(map[string]any)
		if m == nil {
			continue
		}
		path, _ := m["path"].(string)
		if path == "" {
			path = "/"
		}
		preservePathPrefix, _ := m["preserve_path_prefix"].(bool)
		out = append(out, routeCanonicalMap(path, preservePathPrefix))
	}
	return out
}

func routesCanonicalFromSpec(spec *godo.AppSpec) []any {
	if spec == nil {
		return nil
	}
	primaryServiceName := ""
	if len(spec.Services) > 0 && spec.Services[0] != nil {
		primaryServiceName = spec.Services[0].Name
	}
	if routes := routesCanonicalFromIngressSpec(spec.Ingress, primaryServiceName); len(routes) > 0 {
		return routes
	}
	if len(spec.Services) == 0 || spec.Services[0] == nil || len(spec.Services[0].Routes) == 0 {
		return nil
	}
	out := make([]any, 0, len(spec.Services[0].Routes))
	for _, r := range spec.Services[0].Routes {
		if r == nil {
			continue
		}
		path := r.Path
		if path == "" {
			path = "/"
		}
		out = append(out, routeCanonicalMap(path, r.PreservePathPrefix))
	}
	return out
}

func routesCanonicalFromIngressSpec(ingress *godo.AppIngressSpec, serviceName string) []any {
	if ingress == nil || len(ingress.Rules) == 0 {
		return nil
	}
	out := make([]any, 0, len(ingress.Rules))
	for _, rule := range ingress.Rules {
		if rule == nil || rule.Match == nil || rule.Match.Path == nil || rule.Component == nil {
			continue
		}
		if serviceName != "" && rule.Component.Name != serviceName {
			continue
		}
		path := ""
		if rule.Match.Path.Prefix != nil {
			path = *rule.Match.Path.Prefix
		} else if rule.Match.Path.Exact != nil {
			path = *rule.Match.Path.Exact
		}
		if path == "" {
			path = "/"
		}
		out = append(out, routeCanonicalMap(path, rule.Component.PreservePathPrefix))
	}
	return out
}

func routeCanonicalMap(path string, preservePathPrefix bool) map[string]any {
	return map[string]any{
		"path":                 path,
		"preserve_path_prefix": preservePathPrefix,
	}
}

func routesCanonicalEqual(desired, current []any) bool {
	if len(desired) != len(current) {
		return false
	}
	for i := range desired {
		dm, _ := desired[i].(map[string]any)
		cm, _ := current[i].(map[string]any)
		if dm == nil || cm == nil {
			return dm == nil && cm == nil
		}
		if dm["path"] != cm["path"] || dm["preserve_path_prefix"] != cm["preserve_path_prefix"] {
			return false
		}
	}
	return true
}

// serviceEnvVarsHashFromSpec returns the SHA-256 hash of the first
// service component's env_vars as canonical-sorted JSON. Hash-only
// representation keeps Diff drift-detection working (hash mismatch →
// NeedsUpdate) without persisting plaintext env_var values to state.
// CRITICAL for security: env_vars may contain JIT-substituted secrets
// (e.g. `${STAGING_PG_PASSWORD}` in infra.yaml that wfctl resolves to
// the actual password before sending to DO). Returns "" when the spec
// has no services or the first service has no Envs.
func serviceEnvVarsHashFromSpec(spec *godo.AppSpec) string {
	if spec == nil || len(spec.Services) == 0 || spec.Services[0] == nil {
		return ""
	}
	return envVarsHash(spec.Services[0].Envs)
}

// jobsHashFromSpec returns SHA-256 hashes per job (keyed by name) of
// each job's env_vars + kind. Hash-only mirrors the security model of
// serviceEnvVarsHashFromSpec; jobs typically carry the same secret-
// substituted DATABASE_URL the top-level service does, so the same
// plaintext-leak risk applies.
func jobsHashFromSpec(spec *godo.AppSpec) map[string]any {
	if spec == nil || len(spec.Jobs) == 0 {
		return nil
	}
	out := make(map[string]any, len(spec.Jobs))
	for _, j := range spec.Jobs {
		if j == nil || j.Name == "" {
			continue
		}
		out[j.Name] = componentHash(string(j.Kind), j.Envs)
	}
	return out
}

// workersHashFromSpec mirrors jobsHashFromSpec for AppSpec.Workers.
func workersHashFromSpec(spec *godo.AppSpec) map[string]any {
	if spec == nil || len(spec.Workers) == 0 {
		return nil
	}
	out := make(map[string]any, len(spec.Workers))
	for _, w := range spec.Workers {
		if w == nil || w.Name == "" {
			continue
		}
		out[w.Name] = componentHash("", w.Envs)
	}
	return out
}

// envVarsHash computes the SHA-256 of canonical-sorted JSON over the
// env_vars (key+value pairs). Sorting by key ensures map-iteration
// order doesn't affect the hash. Empty/nil envs hash to "" (not
// sha256("[]")) so empty-vs-absent are treated identically.
func envVarsHash(envs []*godo.AppVariableDefinition) string {
	if len(envs) == 0 {
		return ""
	}
	keys := make([]string, 0, len(envs))
	byKey := make(map[string]string, len(envs))
	for _, e := range envs {
		if e == nil {
			continue
		}
		keys = append(keys, e.Key)
		byKey[e.Key] = e.Value
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		// Length-prefix each field so "AB"+"C" cannot collide with "A"+"BC".
		fmt.Fprintf(h, "%d:%s=%d:%s\x00", len(k), k, len(byKey[k]), byKey[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// componentHash hashes a component's kind + env_vars together so Diff
// detects changes to either dimension on jobs/workers.
func componentHash(kind string, envs []*godo.AppVariableDefinition) string {
	h := sha256.New()
	fmt.Fprintf(h, "kind=%s\x00", kind)
	io.WriteString(h, envVarsHash(envs))
	return hex.EncodeToString(h.Sum(nil))
}

// envVarsHashFromConfigMap is the desired-side counterpart for envVarsHash:
// hashes a map[string]any (the shape infra.yaml YAML decodes to) using the
// same canonical-sorted-JSON algorithm. Values are coerced to string via
// fmt.Sprintf to match envVarsFromConfig's coercion.
func envVarsHashFromConfigMap(m map[string]any) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		v := fmt.Sprintf("%v", m[k])
		fmt.Fprintf(h, "%d:%s=%d:%s\x00", len(k), k, len(v), v)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// componentHashFromConfig hashes a desired-side jobs[]/workers[] entry
// (canonical-shape: map[string]any{"name", "kind", "env_vars"}). Mirrors
// componentHash so desired and current hashes are directly comparable.
func componentHashFromConfig(m map[string]any) string {
	kind, _ := m["kind"].(string)
	// Match godo's UPPER_SNAKE convention: pre_deploy → PRE_DEPLOY etc.
	kind = strings.ToUpper(kind)
	envs, _ := m["env_vars"].(map[string]any)
	h := sha256.New()
	fmt.Fprintf(h, "kind=%s\x00", kind)
	io.WriteString(h, envVarsHashFromConfigMap(envs))
	return hex.EncodeToString(h.Sum(nil))
}

// vpcUUIDFromSpec extracts the App's attached VPC UUID. Empty string when
// the App is not VPC-attached (DO's default — public-network only).
func vpcUUIDFromSpec(spec *godo.AppSpec) string {
	if spec == nil || spec.Vpc == nil {
		return ""
	}
	return spec.Vpc.ID
}

// deriveImageFromAppSpec returns a canonical user-facing image ref string for
// the first service component of an AppSpec, suitable for use as
// Outputs["image"] in Diff comparisons. Returns "" when the AppSpec has no
// services or the first service lacks an Image. Round-3 Finding A.
func deriveImageFromAppSpec(spec *godo.AppSpec) string {
	if spec == nil || len(spec.Services) == 0 {
		return ""
	}
	svc := spec.Services[0]
	if svc == nil {
		return ""
	}
	return formatImageSpec(svc.Image)
}

// formatImageSpec reverses ParseImageRef: it formats a godo.ImageSourceSpec
// back into a canonical user-facing image ref string. The format is chosen so
// that ParseImageRef(formatImageSpec(spec)) returns a struct with the same
// RegistryType, Registry, Repository, and Tag — except for DOCR.
//
// DOCR exception: the DO API convention leaves Registry empty on the wire
// (see ParseImageRef's DOCR case which discards the middle path segment).
// formatImageSpec compensates by substituting Repository as the path-segment
// placeholder when Registry=="", so the emitted ref is parseable and
// structurally equivalent on round-trip — but it is NOT a byte-exact
// preservation of whatever the user originally wrote. The user's
// `registry.digitalocean.com/myrepo/myapp:v1` and our derived
// `registry.digitalocean.com/myapp/myapp:v1` parse to the same {DOCR,
// Registry:"", Repository:"myapp", Tag:"v1"} struct, which is what
// imageRefsEqual compares. Outputs["image"] should therefore be treated as
// a canonical identifier, not a verbatim copy of cfg["image"].
//
// Used by deriveImageFromAppSpec and as the basis for imageRefsEqual's
// structural compare.
func formatImageSpec(img *godo.ImageSourceSpec) string {
	if img == nil || img.Repository == "" {
		return ""
	}
	tag := img.Tag
	if tag == "" {
		tag = "latest"
	}
	switch img.RegistryType {
	case godo.ImageSourceSpecRegistryType_DOCR:
		// DOCR requires 3 path segments to parse (registry.digitalocean.com /
		// <registry> / <repo>). When Registry is empty (the DO API
		// convention), substitute Repository as the placeholder so the round
		// trip yields a parseable ref with the same Repository+Tag — DOCR
		// drops the middle segment during parse, so a structural compare
		// (RegistryType+Registry+Repository+Tag, with Registry="" on both
		// sides for DOCR) still matches the user's input.
		reg := img.Registry
		if reg == "" {
			reg = img.Repository
		}
		return fmt.Sprintf("registry.digitalocean.com/%s/%s:%s", reg, img.Repository, tag)
	case godo.ImageSourceSpecRegistryType_Ghcr:
		if img.Registry == "" {
			return fmt.Sprintf("ghcr.io/%s:%s", img.Repository, tag)
		}
		return fmt.Sprintf("ghcr.io/%s/%s:%s", img.Registry, img.Repository, tag)
	case godo.ImageSourceSpecRegistryType_DockerHub:
		if img.Registry == "" {
			return fmt.Sprintf("docker.io/%s:%s", img.Repository, tag)
		}
		return fmt.Sprintf("docker.io/%s/%s:%s", img.Registry, img.Repository, tag)
	default:
		// Unknown registry type — emit a best-effort ref. ParseImageRef will
		// likely fail on these, which means imageRefsEqual will fall back to
		// raw string compare — still safer than emitting "".
		if img.Registry == "" {
			return fmt.Sprintf("%s:%s", img.Repository, tag)
		}
		return fmt.Sprintf("%s/%s:%s", img.Registry, img.Repository, tag)
	}
}

// imageRefsEqual compares two image-ref strings structurally: parses both via
// ParseImageRef and compares RegistryType+Registry+Repository+Tag. Falls back
// to raw string equality when either side fails to parse. Two empty strings
// are equal; an empty paired with a non-empty is unequal.
//
// Registry is compared in the structural form, not the raw string. This
// matters for GHCR/DockerHub: `ghcr.io/orgA/app:v1` vs `ghcr.io/orgB/app:v1`
// is a real change Plan must surface (round-4 finding). For DOCR the
// comparison is still safe — ParseImageRef discards the middle segment for
// DOCR, so both sides yield Registry="" regardless of the
// formatImageSpec placeholder used during the round-trip.
func imageRefsEqual(a, b string) bool {
	if a == b {
		return true
	}
	if a == "" || b == "" {
		return false
	}
	aSpec, aErr := ParseImageRef(a)
	bSpec, bErr := ParseImageRef(b)
	if aErr != nil || bErr != nil {
		return false
	}
	return aSpec.RegistryType == bSpec.RegistryType &&
		aSpec.Registry == bSpec.Registry &&
		aSpec.Repository == bSpec.Repository &&
		aSpec.Tag == bSpec.Tag
}

// deriveExposeFromAppSpec inspects the first service component of an AppSpec
// to determine whether the deployed app is public or internal. App Platform
// allows multiple services per app; the canonical `expose` key applies to the
// primary service (matching the pattern used in buildAppSpec where `svc` is
// services[0]). Apps with no services default to "public" (cannot be
// internal-only by definition).
func deriveExposeFromAppSpec(spec *godo.AppSpec) string {
	if spec == nil || len(spec.Services) == 0 {
		return "public"
	}
	svc := spec.Services[0]
	if svc == nil {
		return "public"
	}
	if svc.HTTPPort == 0 && len(svc.InternalPorts) > 0 {
		return "internal"
	}
	return "public"
}

// ParseImageRef parses a flat image reference string into a DO App Platform
// ImageSourceSpec. Supports:
//
//   - registry.digitalocean.com/<registry>/<repo>:<tag>  → DOCR (Registry left empty per DO API requirement)
//   - ghcr.io/<org>/<repo>:<tag>                         → GHCR
//   - docker.io/<org>/<repo>:<tag>                       → DOCKER_HUB
//   - <org>/<repo>:<tag>                                 → DOCKER_HUB (bare form)
//
// A missing tag defaults to "latest".
func ParseImageRef(imageStr string) (*godo.ImageSourceSpec, error) {
	if imageStr == "" {
		return nil, fmt.Errorf("image ref is empty")
	}
	if strings.TrimSpace(imageStr) != imageStr || strings.ContainsAny(imageStr, " \t\n\r") {
		return nil, fmt.Errorf("invalid image ref %q: whitespace is not allowed", imageStr)
	}

	// Separate tag from the path reference.
	ref, tag, hasExplicitTag := strings.Cut(imageStr, ":")
	if hasExplicitTag && tag == "" {
		return nil, fmt.Errorf("invalid image ref %q: explicit tag is empty", imageStr)
	}
	if strings.Contains(tag, ":") {
		return nil, fmt.Errorf("invalid image ref %q: tag must not contain ':'", imageStr)
	}
	if tag == "" {
		tag = "latest"
	}
	if strings.ContainsAny(tag, " \t\n\r") {
		return nil, fmt.Errorf("invalid image ref %q: tag whitespace is not allowed", imageStr)
	}

	parts := strings.Split(ref, "/")
	for _, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("invalid image ref %q: empty path segment", imageStr)
		}
		if strings.ContainsAny(part, " \t\n\r") {
			return nil, fmt.Errorf("invalid image ref %q: path whitespace is not allowed", imageStr)
		}
	}

	switch {
	case strings.HasPrefix(ref, "registry.digitalocean.com/"):
		// registry.digitalocean.com/<registry>/<repo>
		if len(parts) < 3 {
			return nil, fmt.Errorf("invalid DOCR image ref %q: expected registry.digitalocean.com/<registry>/<repo>", imageStr)
		}
		return &godo.ImageSourceSpec{
			RegistryType: godo.ImageSourceSpecRegistryType_DOCR,
			// Registry must be left empty for DOCR per DO API.
			Repository: parts[len(parts)-1],
			Tag:        tag,
		}, nil

	case strings.HasPrefix(ref, "ghcr.io/"):
		// ghcr.io/<org>/<repo>
		if len(parts) < 3 {
			return nil, fmt.Errorf("invalid GHCR image ref %q: expected ghcr.io/<org>/<repo>", imageStr)
		}
		return &godo.ImageSourceSpec{
			RegistryType: godo.ImageSourceSpecRegistryType_Ghcr,
			Registry:     parts[1],
			Repository:   parts[len(parts)-1],
			Tag:          tag,
		}, nil

	case strings.HasPrefix(ref, "docker.io/"):
		// docker.io/<org>/<repo>
		if len(parts) < 3 {
			return nil, fmt.Errorf("invalid Docker Hub image ref %q: expected docker.io/<org>/<repo>", imageStr)
		}
		return &godo.ImageSourceSpec{
			RegistryType: godo.ImageSourceSpecRegistryType_DockerHub,
			Registry:     parts[1],
			Repository:   parts[len(parts)-1],
			Tag:          tag,
		}, nil

	default:
		// Bare <org>/<repo> format treated as Docker Hub.
		if len(parts) < 2 {
			return nil, fmt.Errorf("unsupported image ref %q: expected <org>/<repo>[:tag] or a registry-prefixed form (registry.digitalocean.com, ghcr.io, docker.io)", imageStr)
		}
		return &godo.ImageSourceSpec{
			RegistryType: godo.ImageSourceSpecRegistryType_DockerHub,
			Registry:     parts[0],
			Repository:   parts[len(parts)-1],
			Tag:          tag,
		}, nil
	}
}

// imageSpecFromConfig extracts an ImageSourceSpec from spec.Config["image"].
// Accepts either a flat string (parsed by ParseImageRef) or an already-nested
// map[string]any with keys registry_type, repository, tag, registry.
func imageSpecFromConfig(cfg map[string]any) (*godo.ImageSourceSpec, error) {
	var img *godo.ImageSourceSpec
	var err error
	switch v := cfg["image"].(type) {
	case string:
		if v == "" {
			return nil, fmt.Errorf("image config is empty")
		}
		img, err = ParseImageRef(v)
	case map[string]any:
		img, err = imageSpecFromMap(v)
	default:
		return nil, fmt.Errorf("image config must be a string or map[string]any, got %T", cfg["image"])
	}
	if err != nil {
		return nil, err
	}
	applyRegistryCredentialsFromConfig(img, cfg)
	return img, nil
}

func imageSpecFromMap(m map[string]any) (*godo.ImageSourceSpec, error) {
	repo, _ := m["repository"].(string)
	if repo == "" {
		return nil, fmt.Errorf("image map config missing required field 'repository'")
	}
	regType, _ := m["registry_type"].(string)
	if regType == "" {
		regType = string(godo.ImageSourceSpecRegistryType_DOCR)
	}
	tag, _ := m["tag"].(string)
	if tag == "" {
		tag = "latest"
	}
	registry, _ := m["registry"].(string)
	registryCredentials, _ := m["registry_credentials"].(string)
	return &godo.ImageSourceSpec{
		RegistryType:        godo.ImageSourceSpecRegistryType(regType),
		Registry:            registry,
		Repository:          repo,
		Tag:                 tag,
		RegistryCredentials: registryCredentials,
	}, nil
}

func applyRegistryCredentialsFromConfig(img *godo.ImageSourceSpec, cfg map[string]any) {
	if img == nil || img.RegistryCredentials != "" {
		return
	}
	if credentials, ok := cfg["registry_credentials"].(string); ok && credentials != "" {
		img.RegistryCredentials = credentials
		return
	}
	ps, ok := cfg["provider_specific"].(map[string]any)
	if !ok {
		return
	}
	do, ok := ps["digitalocean"].(map[string]any)
	if !ok {
		return
	}
	if credentials, ok := do["registry_credentials"].(string); ok && credentials != "" {
		img.RegistryCredentials = credentials
	}
}

// envVarsFromConfig converts the "env_vars" map in spec config to App Platform
// environment variable definitions. Secret vars use "env_vars_secret" (canonical key).
// "secret_env_vars" is a legacy alias: it is used only when "env_vars_secret" is
// absent from the config entirely (key-presence check, not length check).
func envVarsFromConfig(cfg map[string]any) []*godo.AppVariableDefinition {
	raw, _ := cfg["env_vars"].(map[string]any)
	// Prefer the canonical key; fall back to the legacy alias only when the
	// canonical key is not present at all (not just empty).
	var secrets map[string]any
	if v, ok := cfg["env_vars_secret"]; ok {
		secrets, _ = v.(map[string]any)
	} else {
		secrets, _ = cfg["secret_env_vars"].(map[string]any)
	}
	if len(raw) == 0 && len(secrets) == 0 {
		return nil
	}
	envs := make([]*godo.AppVariableDefinition, 0, len(raw)+len(secrets))
	for k, v := range raw {
		envs = append(envs, &godo.AppVariableDefinition{
			Key:   k,
			Value: fmt.Sprintf("%v", v),
			Type:  godo.AppVariableType_General,
			Scope: godo.AppVariableScope_RunAndBuildTime,
		})
	}
	for k, v := range secrets {
		envs = append(envs, &godo.AppVariableDefinition{
			Key:   k,
			Value: fmt.Sprintf("%v", v),
			Type:  godo.AppVariableType_Secret,
			Scope: godo.AppVariableScope_RunAndBuildTime,
		})
	}
	return envs
}

func (d *AppPlatformDriver) SensitiveKeys() []string { return nil }

func (d *AppPlatformDriver) ProviderIDFormat() interfaces.ProviderIDFormat {
	return interfaces.IDFormatUUID
}
