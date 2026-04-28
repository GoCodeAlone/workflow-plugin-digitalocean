// Package drivers contains ResourceDriver implementations for each DigitalOcean resource type.
package drivers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// ErrResourceNotFound is returned when a resource cannot be located by name or ID.
// It is intended to be used with errors.Is for sentinel matching.
var ErrResourceNotFound = errors.New("resource not found")

// AppPlatformClient is the godo App interface used by AppPlatformDriver (for mocking).
type AppPlatformClient interface {
	Create(ctx context.Context, req *godo.AppCreateRequest) (*godo.App, *godo.Response, error)
	Get(ctx context.Context, appID string) (*godo.App, *godo.Response, error)
	List(ctx context.Context, opts *godo.ListOptions) ([]*godo.App, *godo.Response, error)
	Update(ctx context.Context, appID string, req *godo.AppUpdateRequest) (*godo.App, *godo.Response, error)
	CreateDeployment(ctx context.Context, appID string, req ...*godo.DeploymentCreateRequest) (*godo.Deployment, *godo.Response, error)
	ListDeployments(ctx context.Context, appID string, opts *godo.ListOptions) ([]*godo.Deployment, *godo.Response, error)
	Delete(ctx context.Context, appID string) (*godo.Response, error)
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
	client AppPlatformClient
	region string
}

// NewAppPlatformDriver creates an AppPlatformDriver backed by a real godo client.
func NewAppPlatformDriver(c *godo.Client, region string) *AppPlatformDriver {
	return &AppPlatformDriver{client: c.Apps, region: region}
}

// NewAppPlatformDriverWithClient creates a driver with an injected client (for tests).
func NewAppPlatformDriverWithClient(c AppPlatformClient, region string) *AppPlatformDriver {
	return &AppPlatformDriver{client: c, region: region}
}

func (d *AppPlatformDriver) Create(ctx context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
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

	app, _, err := d.client.Update(ctx, providerID, &godo.AppUpdateRequest{Spec: appSpec})
	if err != nil {
		return nil, fmt.Errorf("app platform update %q: %w", ref.Name, WrapGodoError(err))
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

func (d *AppPlatformDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
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
	return &interfaces.DiffResult{NeedsUpdate: len(changes) > 0, Changes: changes}, nil
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
	return appHealthResult(app), nil
}

// appHealthResult evaluates all three DO deployment slots in priority order and
// returns an accurate HealthResult:
//
//   - ActiveDeployment ACTIVE         → Healthy=true
//   - InProgressDeployment (building) → Healthy=false, "deployment in progress: <phase>"
//   - InProgressDeployment (failed)   → Healthy=false, "deployment failed: <phase>"
//   - PendingDeployment               → Healthy=false, "deployment queued"
//   - none of the above               → Healthy=false, "no deployment found"
func appHealthResult(app *godo.App) *interfaces.HealthResult {
	// 1. Active and healthy.
	if app.ActiveDeployment != nil && app.ActiveDeployment.Phase == godo.DeploymentPhase_Active {
		return &interfaces.HealthResult{Healthy: true}
	}

	// 2. Deployment currently in progress — inspect its phase.
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

	// 4. No deployment at all (first-deploy not yet kicked off, or app never deployed).
	return &interfaces.HealthResult{Healthy: false, Message: "no deployment found"}
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
		if diag := buildDiagnosticFor(dep); diag != nil {
			out = append(out, *diag)
		}
	}
	return out, nil
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
		},
		Status: "running",
	}
	if app.ActiveDeployment == nil {
		out.Status = "pending"
	}
	return out
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
	switch v := cfg["image"].(type) {
	case string:
		if v == "" {
			return nil, fmt.Errorf("image config is empty")
		}
		return ParseImageRef(v)
	case map[string]any:
		return imageSpecFromMap(v)
	default:
		return nil, fmt.Errorf("image config must be a string or map[string]any, got %T", cfg["image"])
	}
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
	return &godo.ImageSourceSpec{
		RegistryType: godo.ImageSourceSpecRegistryType(regType),
		Registry:     registry,
		Repository:   repo,
		Tag:          tag,
	}, nil
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
