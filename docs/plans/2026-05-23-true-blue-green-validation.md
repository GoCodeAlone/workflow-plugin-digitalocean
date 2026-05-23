# True Blue/Green Validation for DO App Platform Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make `AppBlueGreenDriver` + `AppCanaryDriver` safe against custom-domain collisions on green/canary clone Create, surface an in-rollout custom-domain availability probe during `waitingForDeployment`, enrich deployment-in-progress errors with stateless progress info, pin the actual prevalidated-rolling semantics with tests, and document what each strategy guarantees.

**Architecture:** Surgical fix at the green/canary clone Create call sites (strip `Spec.Domains` only; leave `Ingress` untouched) + a probe-loop extraction so the existing post-Active custom-domain probe can also fire during the `Deploying`/`PendingDeploy` window without changing the gate semantics. Error-string enrichment is purely additive — original prefix and parenthesized deployment ID preserved verbatim, `%w`-wrapping preserves `errors.Is`/`errors.As`.

**Tech Stack:** Go 1.26, `github.com/digitalocean/godo` v1.175.0, standard testing + table-driven tests; no new dependencies.

**Base branch:** main

---

## Scope Manifest

**PR Count:** 1
**Tasks:** 8
**Estimated Lines of Change:** ~700 (informational; not enforced)

**Out of scope:**
- True B/G traffic switching via DO Load Balancer + Droplets or external front-door (filed as follow-up; deferred to a separate design cycle).
- Renaming `AppBlueGreenDriver` → `AppPrevalidatedRollingDriver` (requires workflow-engine `module.BlueGreenDriver` interface coordination; filed as follow-up).
- Live-cluster validation against `gocodealone-multisite` for this PR (changes are observation-only + a strictly safer spec sanitization; live validation belongs to the deferred front-door design).
- Live availability probe SLOs, alerting integrations, dashboards.
- Changes to `wfctl infra apply --wait` polling cadence.
- Stuck-deploy heuristic (dropped per adversarial cycle 1 I1/I2).
- Mutating the committed design doc with post-merge issue numbers (deferred per plan adversarial cycle 1 I5).

**PR Grouping:**

| PR # | Title | Tasks | Branch |
|------|-------|-------|--------|
| 1 | fix(app-platform): strip custom domains from green/canary clones + in-rollout probe + progress signal (#159) | Task 1, Task 2, Task 3, Task 4, Task 5, Task 6, Task 7, Task 8 | feat/159-bg-validation-and-rollout-probe |

**Status:** Draft

---

## Verified API surface (read this before writing code)

The plan code samples below were verified against the actual repository on cycle-2 revision:

- **Existing fake client:** `deployMockClient` constructed via `newDeployMock()` (deploy_test.go:23) with `seedApp(m, id, name, image)` helper (deploy_test.go:109). The mock's `Create` (deploy_test.go:32) currently does NOT capture the request — it returns `{ID, LiveURL, Spec, ActiveDeployment}` and stores into `m.apps`. **Step 0 of Task 1 extends the mock** with `lastCreateRequest *godo.AppCreateRequest`, `lastUpdateRequest *godo.AppUpdateRequest`, `deletedAppIDs []string`, and `createLiveURL string` (override) fields, captured in the corresponding methods. This is the ONLY code change in the mock and is shared by Tasks 1/2/4/6.
- **`godo.DeploymentProgress` fields** (apps.gen.go:956): `PendingSteps`, `RunningSteps`, `SuccessSteps`, `ErrorSteps`, `TotalSteps`. There is **no** `StepsCompleted`/`StepsTotal`. Plan uses `SuccessSteps+ErrorSteps` for "completed" and `TotalSteps` for "total".
- **Live custom domain status** lives at `app.Domains []*godo.AppDomain` (used at app_domain_test.go:116, app_platform_test.go:184, deploy_test.go:268). **There is no `LiveDomains` field.** Spec-side claims live at `app.Spec.Domains []*godo.AppDomainSpec`.
- **`NewAppDeployDriverWithDomainProbe`** at deploy.go:39 signature: `(c AppPlatformClient, region, appID, appName string, probe AppPlatformDomainProbe)` — 5 args, NO `RegistryClient`. Plan does NOT call this from BG/Canary accessors; instead BG/Canary store the probe and post-assign `inner.domainProbe = d.domainProbe` after `NewAppDeployDriverWithRegistry(...)` returns (same package, `domainProbe` is unexported but accessible).
- **Test package boundary:** existing `internal/drivers/deploy_test.go` and `internal/drivers/app_platform_test.go` are both `package drivers_test` (black-box). Tests that need to touch unexported fields (`d.domainProbe`, `d.greenID`, `d.waitingForDeployment`, the `appPlatformProbeCustomDomains` helper) are added to a NEW white-box file `internal/drivers/deploy_internal_test.go` in `package drivers`. Tests that only use the public API stay in `deploy_test.go`.

---

### Task 1: Extend mock + `sanitizeClonedSpecForCreate` + Domains-strip in BG/Canary Create paths

Closes critical C1 from design adversarial cycle 1. Also extends the shared mock with request-capture fields used by later tasks.

**Files:**
- Modify: `internal/drivers/deploy_test.go` (extend `deployMockClient`)
- Modify: `internal/drivers/deploy.go` (add helper, call from CreateGreen ~line 326 + CreateCanary ~line 469)
- Test: `internal/drivers/deploy_test.go` (3 new tests in `package drivers_test`)

**Step 1: Extend the mock with capture fields**

In `internal/drivers/deploy_test.go`, extend `deployMockClient`:

```go
type deployMockClient struct {
	apps              map[string]*godo.App
	deployments       map[string][]*godo.Deployment
	err               error
	nextID            int
	lastCreateRequest *godo.AppCreateRequest
	lastUpdateRequest *godo.AppUpdateRequest
	deletedAppIDs     []string
	createLiveURL     string // override for the LiveURL returned by Create; empty → derived from name
}
```

Update `Create` to capture + honor override:

```go
func (m *deployMockClient) Create(_ context.Context, req *godo.AppCreateRequest) (*godo.App, *godo.Response, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	m.lastCreateRequest = req
	id := fmt.Sprintf("app-%d", m.nextID)
	m.nextID++
	live := m.createLiveURL
	if live == "" {
		live = "https://" + req.Spec.Name + ".example.com"
	}
	app := &godo.App{
		ID:      id,
		LiveURL: live,
		Spec:    req.Spec,
		ActiveDeployment: &godo.Deployment{Phase: godo.DeploymentPhase_Active},
	}
	m.apps[id] = app
	return app, nil, nil
}
```

Update `Update` and `Delete`:

```go
func (m *deployMockClient) Update(_ context.Context, appID string, req *godo.AppUpdateRequest) (*godo.App, *godo.Response, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	m.lastUpdateRequest = req
	app, ok := m.apps[appID]
	if !ok {
		return nil, nil, fmt.Errorf("app %q not found", appID)
	}
	app.Spec = req.Spec
	return app, nil, nil
}
func (m *deployMockClient) Delete(_ context.Context, appID string) (*godo.Response, error) {
	if m.err != nil {
		return nil, m.err
	}
	m.deletedAppIDs = append(m.deletedAppIDs, appID)
	delete(m.apps, appID)
	return nil, nil
}
```

Verify nothing else needs to read these fields; the additions are purely additive.

**Step 2: Write the failing tests**

Append to `internal/drivers/deploy_test.go`. (The Ingress-preservation test lives in the white-box file added in Task 2, where the unexported helper is directly callable; the two per-driver tests below stay in `deploy_test.go` and assert the public effect.)

```go
func TestAppBlueGreenDriver_CreateGreen_StripsCustomDomainsFromGreenSpec(t *testing.T) {
	m := newDeployMock()
	app := seedApp(m, "blue-id", "blue", "registry.digitalocean.com/myrepo/app:v1")
	app.Spec.Domains = []*godo.AppDomainSpec{
		{Domain: "blue.example.com", Type: godo.AppDomainSpecType_Primary},
	}

	d := drivers.NewAppBlueGreenDriver(m, "nyc1", "blue-id", "blue")
	if err := d.CreateGreen(context.Background(), "registry.digitalocean.com/myrepo/app:v2"); err != nil {
		t.Fatalf("CreateGreen: %v", err)
	}
	if m.lastCreateRequest == nil {
		t.Fatal("Create was not invoked")
	}
	if len(m.lastCreateRequest.Spec.Domains) != 0 {
		t.Fatalf("green clone inherited blue's Domains: %#v", m.lastCreateRequest.Spec.Domains)
	}
}

func TestAppCanaryDriver_CreateCanary_StripsCustomDomainsFromCanarySpec(t *testing.T) {
	m := newDeployMock()
	app := seedApp(m, "stable-id", "stable", "registry.digitalocean.com/myrepo/app:v1")
	app.Spec.Domains = []*godo.AppDomainSpec{
		{Domain: "stable.example.com", Type: godo.AppDomainSpecType_Primary},
	}

	d := drivers.NewAppCanaryDriver(m, "nyc1", "stable-id", "stable")
	if err := d.CreateCanary(context.Background(), "registry.digitalocean.com/myrepo/app:v2"); err != nil {
		t.Fatalf("CreateCanary: %v", err)
	}
	if m.lastCreateRequest == nil {
		t.Fatal("Create was not invoked")
	}
	if len(m.lastCreateRequest.Spec.Domains) != 0 {
		t.Fatalf("canary clone inherited stable's Domains: %#v", m.lastCreateRequest.Spec.Domains)
	}
}
```

**Step 3: Run tests to verify they fail**

```
GOWORK=off go test ./internal/drivers/... -run 'TestAppBlueGreenDriver_CreateGreen_StripsCustomDomains|TestAppCanaryDriver_CreateCanary_StripsCustomDomains' -count=1
```
Expected: FAIL — Domains pass through to lastCreateRequest unmodified.

**Step 4: Add `sanitizeClonedSpecForCreate` + call sites**

In `internal/drivers/deploy.go`:

```go
// sanitizeClonedSpecForCreate prepares a spec that was deep-copied from another
// app for use in an Apps.Create call. It clears only the custom-domain claim
// fields that would collide with the source app on DO App Platform; everything
// else (Services / Workers / Jobs / Functions / StaticSites / Ingress) is left
// untouched so the new app is a faithful image-swapped clone.
func sanitizeClonedSpecForCreate(spec *godo.AppSpec) {
	if spec == nil {
		return
	}
	spec.Domains = nil
}
```

In `CreateGreen`, after `greenSpec.Name = d.blueName + "-green"`:

```go
sanitizeClonedSpecForCreate(greenSpec)
```

In `CreateCanary`, after `canarySpec.Name = d.stableName + "-canary"`:

```go
sanitizeClonedSpecForCreate(canarySpec)
```

**Step 5: Run tests + full driver suite**

```
GOWORK=off go test ./internal/drivers/... -count=1
```
Expected: PASS.

**Step 6: Commit**

```bash
git add internal/drivers/deploy.go internal/drivers/deploy_test.go
git commit -m "fix(app-platform): strip custom Domains from green/canary cloned spec (#159)

CreateGreen and CreateCanary deep-cloned blue/stable's Spec via cloneAppSpec,
including Spec.Domains. DO App Platform rejects creating a second app that
claims an already-attached custom domain. sanitizeClonedSpecForCreate now
clears only Spec.Domains; Ingress and all other surface is preserved.
Mock client extended to capture lastCreateRequest / lastUpdateRequest /
deletedAppIDs / createLiveURL for these and downstream tests.

Rollback: revert commit; purely additive (strictly fewer fields populated)."
```

---

### Task 2: `NewAppBlueGreenDriverWithDomainProbe` + `NewAppCanaryDriverWithDomainProbe` + white-box test file

Required so the in-rollout probe tests (Task 4) and the helper-direct test (Task 3) can touch unexported state.

**Files:**
- Modify: `internal/drivers/deploy.go` (add `domainProbe` field to BG + Canary structs; add constructors; update accessors to propagate probe)
- Create: `internal/drivers/deploy_internal_test.go` (NEW; `package drivers`; white-box)

**Step 1: Write the failing tests in the new white-box file**

Create `internal/drivers/deploy_internal_test.go`:

```go
package drivers

import (
	"context"
	"testing"

	"github.com/digitalocean/godo"
)

func TestSanitizeClonedSpecForCreate_PreservesIngress(t *testing.T) {
	spec := &godo.AppSpec{
		Name: "blue",
		Domains: []*godo.AppDomainSpec{
			{Domain: "example.com", Type: godo.AppDomainSpecType_Primary},
		},
		Ingress: &godo.AppIngressSpec{
			Rules: []*godo.AppIngressSpecRule{
				{Match: &godo.AppIngressSpecRuleMatch{Path: &godo.AppIngressSpecRuleStringMatch{Prefix: "/api"}}},
				{Match: &godo.AppIngressSpecRuleMatch{Path: &godo.AppIngressSpecRuleStringMatch{Prefix: "/"}}},
			},
		},
		Services: []*godo.AppServiceSpec{{Name: "web"}},
	}
	sanitizeClonedSpecForCreate(spec)
	if len(spec.Domains) != 0 {
		t.Fatalf("Domains not cleared, got %d entries", len(spec.Domains))
	}
	if spec.Ingress == nil || len(spec.Ingress.Rules) != 2 {
		t.Fatalf("Ingress.Rules altered, got %#v", spec.Ingress)
	}
}

func TestNewAppBlueGreenDriverWithDomainProbe_InjectsIntoInnerDrivers(t *testing.T) {
	probe := func(_ context.Context, _, _ string) error { return nil }
	d := NewAppBlueGreenDriverWithDomainProbe(nil, nil, "nyc1", "blue-id", "blue", probe)
	if d.blueDriver().domainProbe == nil {
		t.Fatal("blue driver missing injected probe")
	}
	d.greenID = "green-id"
	if d.greenDriver().domainProbe == nil {
		t.Fatal("green driver missing injected probe")
	}
}

func TestNewAppCanaryDriverWithDomainProbe_InjectsIntoInnerDrivers(t *testing.T) {
	probe := func(_ context.Context, _, _ string) error { return nil }
	d := NewAppCanaryDriverWithDomainProbe(nil, nil, "nyc1", "stable-id", "stable", probe)
	if d.stableDriver().domainProbe == nil {
		t.Fatal("stable driver missing injected probe")
	}
	d.canaryID = "canary-id"
	if d.canaryDriver().domainProbe == nil {
		t.Fatal("canary driver missing injected probe")
	}
}
```

If `Ingress` field uses different godo names in v1.175.0, adjust to whatever fields the actual `godo.AppIngressSpecRule` provides (the helper only needs to assert the Ingress block survives sanitization byte-for-byte; structural shape is incidental).

**Step 2: Run tests to verify they fail**

```
GOWORK=off go test ./internal/drivers/... -run 'TestNewAppBlueGreenDriverWithDomainProbe|TestNewAppCanaryDriverWithDomainProbe|TestSanitizeClonedSpecForCreate_PreservesIngress' -count=1
```
Expected: FAIL — constructors undefined; `sanitizeClonedSpecForCreate` undefined unless Task 1 has already shipped.

**Step 3: Add `domainProbe` field + constructors + accessor propagation**

In `internal/drivers/deploy.go`, add field to `AppBlueGreenDriver`:

```go
type AppBlueGreenDriver struct {
	client      AppPlatformClient
	regClient   RegistryClient
	region      string
	blueID      string
	blueName    string
	greenID     string
	greenURL    string
	stableCheck bool
	blueDeploy  *AppDeployDriver
	greenDeploy *AppDeployDriver
	domainProbe AppPlatformDomainProbe // optional; nil → default HTTPS probe
}
```

And to `AppCanaryDriver`:

```go
type AppCanaryDriver struct {
	client       AppPlatformClient
	regClient    RegistryClient
	region       string
	stableID     string
	stableName   string
	canaryID     string
	stableDeploy *AppDeployDriver
	canaryDeploy *AppDeployDriver
	domainProbe  AppPlatformDomainProbe
}
```

Add constructors:

```go
// NewAppBlueGreenDriverWithDomainProbe is like NewAppBlueGreenDriverWithRegistry
// but also injects probe into both inner *AppDeployDriver instances; used in
// unit tests to substitute the HTTPS probe.
func NewAppBlueGreenDriverWithDomainProbe(c AppPlatformClient, r RegistryClient, region, blueID, blueName string, probe AppPlatformDomainProbe) *AppBlueGreenDriver {
	d := NewAppBlueGreenDriverWithRegistry(c, r, region, blueID, blueName)
	d.domainProbe = probe
	return d
}

func NewAppCanaryDriverWithDomainProbe(c AppPlatformClient, r RegistryClient, region, stableID, stableName string, probe AppPlatformDomainProbe) *AppCanaryDriver {
	d := NewAppCanaryDriverWithRegistry(c, r, region, stableID, stableName)
	d.domainProbe = probe
	return d
}
```

Modify the four accessors so they propagate `d.domainProbe` into the inner driver after `NewAppDeployDriverWithRegistry` returns:

```go
func (d *AppBlueGreenDriver) blueDriver() *AppDeployDriver {
	if d.blueDeploy == nil {
		d.blueDeploy = NewAppDeployDriverWithRegistry(d.client, d.regClient, d.region, d.blueID, d.blueName)
		d.blueDeploy.domainProbe = d.domainProbe
	}
	return d.blueDeploy
}
```

Same shape for `greenDriver()`, `stableDriver()`, `canaryDriver()`. (Verify the existing struct already has `regClient` — it does per deploy.go:391.)

**Step 4: Run tests to verify they pass**

```
GOWORK=off go test ./internal/drivers/... -count=1
```
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/drivers/deploy.go internal/drivers/deploy_internal_test.go
git commit -m "feat(app-platform): NewAppBlueGreenDriver/CanaryDriverWithDomainProbe + white-box test file (#159)

Adds domainProbe field to AppBlueGreenDriver and AppCanaryDriver, with
WithDomainProbe constructors that propagate the probe into both inner
*AppDeployDriver instances via the existing accessors. Existing constructors
unchanged. New white-box test file deploy_internal_test.go for tests that
need to touch unexported state.

Rollback: revert commit; constructors are additive surface."
```

---

### Task 3: Extract `appPlatformProbeCustomDomains` from post-Active loop

Refactor: pull the per-domain probe loop into a stateless `appPlatformProbeCustomDomains(ctx, app, probe, pathOverride) (reachable, total int)` helper. Post-Active path continues to derive its error from the loop's last failure. No behavior change for the existing post-Active gate.

**Files:**
- Modify: `internal/drivers/app_platform_readiness.go`
- Test: `internal/drivers/deploy_internal_test.go` (white-box; same file from Task 2)

**Step 1: Write the failing test**

Append to `internal/drivers/deploy_internal_test.go`:

```go
func TestAppPlatformProbeCustomDomains_CountsReachable(t *testing.T) {
	app := &godo.App{
		Spec: &godo.AppSpec{
			Domains: []*godo.AppDomainSpec{
				{Domain: "a.example.com"},
				{Domain: "b.example.com"},
				{Domain: "c.example.com", Wildcard: true}, // excluded
			},
		},
	}
	var calls []string
	probe := func(_ context.Context, domain, _ string) error {
		calls = append(calls, domain)
		if domain == "b.example.com" {
			return fmt.Errorf("simulated 503")
		}
		return nil
	}
	reachable, total := appPlatformProbeCustomDomains(context.Background(), app, probe, "/healthz")
	if total != 2 {
		t.Fatalf("total = %d, want 2 (wildcard excluded)", total)
	}
	if reachable != 1 {
		t.Fatalf("reachable = %d, want 1", reachable)
	}
	if len(calls) != 2 {
		t.Fatalf("probe called %d times, want 2", len(calls))
	}
}
```

Add `"fmt"` to the imports of `deploy_internal_test.go` if not already present.

**Step 2: Run test to verify it fails**

```
GOWORK=off go test ./internal/drivers/... -run TestAppPlatformProbeCustomDomains_CountsReachable -count=1
```
Expected: FAIL.

**Step 3: Add helper + refactor**

In `internal/drivers/app_platform_readiness.go`:

```go
// appPlatformProbeCustomDomains GETs each non-wildcard custom domain on app
// and returns how many were reachable (probe returned nil) and how many were
// attempted in total. Wildcard entries are excluded (no concrete host).
func appPlatformProbeCustomDomains(ctx context.Context, app *godo.App, probe AppPlatformDomainProbe, pathOverride string) (reachable, total int) {
	domains := appPlatformCustomDomains(app)
	if len(domains) == 0 {
		return 0, 0
	}
	if probe == nil {
		probe = defaultAppPlatformDomainProbe
	}
	path := appPlatformReadinessPath(app, pathOverride)
	for _, spec := range domains {
		if spec.Wildcard {
			continue
		}
		total++
		if err := probe(ctx, spec.Domain, path); err == nil {
			reachable++
		}
	}
	return reachable, total
}
```

Refactor `appPlatformCustomDomainReadinessError` so its probe loop returns the first error encountered (preserves existing behavior). Verify existing post-Active tests still pass unchanged.

**Step 4: Run full driver suite**

```
GOWORK=off go test ./internal/drivers/... -count=1
```
Expected: PASS including the existing readiness tests.

**Step 5: Commit**

```bash
git add internal/drivers/app_platform_readiness.go internal/drivers/deploy_internal_test.go
git commit -m "refactor(app-platform): extract appPlatformProbeCustomDomains (#159)

Stateless helper reusable by both the post-Active readiness gate and the
in-rollout availability probe added in a follow-up task. No behavior change;
existing tests pin the post-Active semantics.

Rollback: revert commit; helper is purely additive."
```

---

### Task 4: Wire in-rollout probe into `AppDeployDriver.HealthCheck`

Closes critical C2. Composes the new probe helper with `deploymentHealthError` only when phase is `PendingDeploy` or `Deploying`. Uses `%w` so `errors.Is`/`errors.As` continue to work.

**Files:**
- Modify: `internal/drivers/deploy.go` (HealthCheck composition + new `isInProgressPhase` helper)
- Test: `internal/drivers/deploy_internal_test.go` (white-box; same file)

**Step 1: Write failing tests**

Append to `internal/drivers/deploy_internal_test.go`:

```go
func TestAppDeployDriver_HealthCheck_DuringRolloutProbesDomain(t *testing.T) {
	// Use a tiny stub client inline; the deployMockClient isn't accessible
	// from package drivers (it lives in package drivers_test). For the
	// targeted HealthCheck tests we need only Get + ListDeployments.
	stub := &healthCheckProbeStub{
		app: &godo.App{
			ID: "blue-id",
			Spec: &godo.AppSpec{
				Domains: []*godo.AppDomainSpec{{Domain: "blue.example.com"}},
				Services: []*godo.AppServiceSpec{{Name: "web", Image: &godo.ImageSourceSpec{Repository: "r", Tag: "v2"}}},
			},
			InProgressDeployment: &godo.Deployment{
				ID:    "dep-2",
				Phase: godo.DeploymentPhase_Deploying,
			},
			Domains: []*godo.AppDomain{{Spec: &godo.AppDomainSpec{Domain: "blue.example.com"}, Phase: godo.AppJobSpecKindPHASE_Active}},
		},
	}
	var probeCalls int
	probe := func(_ context.Context, _, _ string) error {
		probeCalls++
		return fmt.Errorf("simulated downtime")
	}
	d := NewAppDeployDriverWithDomainProbe(stub, "nyc1", "blue-id", "blue", probe)
	d.waitingForDeployment = true
	d.targetDeploymentID = "dep-2"

	err := d.HealthCheck(context.Background(), "")
	if err == nil {
		t.Fatal("expected in-progress error during DEPLOYING phase")
	}
	if probeCalls == 0 {
		t.Fatal("expected probe to fire during DEPLOYING phase")
	}
	if !strings.Contains(err.Error(), "domain probe: 0/1") {
		t.Errorf("missing reachable/total fragment in error: %v", err)
	}
	if !strings.Contains(err.Error(), "deployment in progress: DEPLOYING") {
		t.Errorf("missing original prefix in error: %v", err)
	}
}

func TestAppDeployDriver_HealthCheck_DuringBuildPhaseSkipsProbe(t *testing.T) {
	stub := &healthCheckProbeStub{
		app: &godo.App{
			ID: "blue-id",
			Spec: &godo.AppSpec{
				Domains: []*godo.AppDomainSpec{{Domain: "blue.example.com"}},
				Services: []*godo.AppServiceSpec{{Name: "web"}},
			},
			InProgressDeployment: &godo.Deployment{ID: "dep-2", Phase: godo.DeploymentPhase_Building},
		},
	}
	var probeCalls int
	probe := func(_ context.Context, _, _ string) error { probeCalls++; return nil }
	d := NewAppDeployDriverWithDomainProbe(stub, "nyc1", "blue-id", "blue", probe)
	d.waitingForDeployment = true
	d.targetDeploymentID = "dep-2"

	_ = d.HealthCheck(context.Background(), "")
	if probeCalls != 0 {
		t.Fatalf("probe fired during BUILDING phase (%d calls); should be skipped", probeCalls)
	}
}

// healthCheckProbeStub is the minimum AppPlatformClient surface the
// HealthCheck probe tests need. It returns the same app for every Get and
// an empty deployments list. Defined in deploy_internal_test.go to avoid
// pulling the deployMockClient (which lives in package drivers_test).
type healthCheckProbeStub struct{ app *godo.App }

func (s *healthCheckProbeStub) Create(_ context.Context, _ *godo.AppCreateRequest) (*godo.App, *godo.Response, error) {
	return nil, nil, nil
}
func (s *healthCheckProbeStub) Get(_ context.Context, _ string) (*godo.App, *godo.Response, error) {
	return s.app, nil, nil
}
func (s *healthCheckProbeStub) Update(_ context.Context, _ string, _ *godo.AppUpdateRequest) (*godo.App, *godo.Response, error) {
	return s.app, nil, nil
}
func (s *healthCheckProbeStub) List(_ context.Context, _ *godo.ListOptions) ([]*godo.App, *godo.Response, error) {
	return []*godo.App{s.app}, nil, nil
}
func (s *healthCheckProbeStub) CreateDeployment(_ context.Context, _ string, _ ...*godo.DeploymentCreateRequest) (*godo.Deployment, *godo.Response, error) {
	return nil, nil, nil
}
func (s *healthCheckProbeStub) ListDeployments(_ context.Context, _ string, _ *godo.ListOptions) ([]*godo.Deployment, *godo.Response, error) {
	return nil, nil, nil
}
func (s *healthCheckProbeStub) Delete(_ context.Context, _ string) (*godo.Response, error) {
	return nil, nil
}
func (s *healthCheckProbeStub) GetLogs(_ context.Context, _, _, _ string, _ godo.AppLogType, _ bool, _ int) (*godo.AppLogs, *godo.Response, error) {
	return nil, nil, nil
}
```

Add `"strings"` to the imports if not already present.

If the `AppPlatformClient` interface has more methods than the stub above implements, the build will surface them at compile time — extend the stub minimally to satisfy. Read the interface definition in `internal/drivers/app_platform_client.go` (or wherever it lives) before running.

**Step 2: Run tests to verify they fail**

```
GOWORK=off go test ./internal/drivers/... -run 'TestAppDeployDriver_HealthCheck_DuringRollout|TestAppDeployDriver_HealthCheck_DuringBuild' -count=1
```
Expected: FAIL.

**Step 3: Add `isInProgressPhase` + wire the probe**

In `internal/drivers/deploy.go`:

```go
// isInProgressPhase is the set of deployment phases where the rolling
// replace is materially affecting routing, and so probing the custom
// domain mid-rollout will surface availability windows. Build phases are
// excluded because the old code is still serving and the probe would
// always succeed there, producing log noise.
func isInProgressPhase(p godo.AppDeploymentPhase) bool {
	switch p {
	case godo.DeploymentPhase_PendingDeploy, godo.DeploymentPhase_Deploying:
		return true
	default:
		return false
	}
}
```

Modify `AppDeployDriver.HealthCheck`'s in-progress branch (current `deploy.go:81-93`):

```go
if d.waitingForDeployment {
	dep, derr := d.currentTargetDeployment(ctx, app)
	if derr != nil {
		return derr
	}
	if dep == nil {
		return fmt.Errorf("app deploy: %q waiting for deployment after update", d.appName)
	}
	if err := deploymentHealthError(d.appName, dep); err != nil {
		if isInProgressPhase(dep.Phase) {
			reachable, total := appPlatformProbeCustomDomains(ctx, app, d.domainProbe, path)
			if total > 0 {
				return fmt.Errorf("%w; domain probe: %d/%d custom domains reachable", err, reachable, total)
			}
		}
		return err
	}
	return appPlatformCustomDomainReadinessError(ctx, d.appName, app, d.domainProbe, path)
}
```

**Step 4: Run tests + full suite**

```
GOWORK=off go test ./internal/drivers/... -count=1
```
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/drivers/deploy.go internal/drivers/deploy_internal_test.go
git commit -m "feat(app-platform): in-rollout custom-domain probe during DEPLOYING (#159)

HealthCheck now invokes the custom-domain probe during PendingDeploy/Deploying
and appends reachable/total to the in-progress error via %w-wrap. Old error
string prefix and parenthesized deployment ID preserved verbatim. Build
phases skip the probe to avoid noise from the unchanged-routing window.

Rollback: revert commit; purely additive error-string enrichment + probe call;
no contract change."
```

---

### Task 5: `deploymentProgressString` + wire into `deploymentHealthError`

Adds `[completed/total steps; updated Ns ago]` suffix using actual godo fields.

**Files:**
- Modify: `internal/drivers/deploy.go` (deploymentHealthError + new deploymentProgressString)
- Test: `internal/drivers/deploy_test.go` (`package drivers_test`; uses public surface only)

**Step 1: Write failing tests in `deploy_test.go`**

```go
func TestDeploymentProgressString_NilProgressIsEmpty(t *testing.T) {
	got := drivers.DeploymentProgressStringForTest(&godo.Deployment{Phase: godo.DeploymentPhase_Deploying})
	if got != "" {
		t.Fatalf("expected empty string for nil Progress, got %q", got)
	}
}

func TestDeploymentProgressString_UpdatedAtAgeFormatting(t *testing.T) {
	dep := &godo.Deployment{
		Phase:     godo.DeploymentPhase_Deploying,
		UpdatedAt: time.Now().Add(-12 * time.Second),
		Progress: &godo.DeploymentProgress{
			SuccessSteps: 2,
			ErrorSteps:   1,
			TotalSteps:   5,
		},
	}
	got := drivers.DeploymentProgressStringForTest(dep)
	if !strings.Contains(got, "3/5 steps") {
		t.Errorf("missing steps fragment (2 success + 1 error = 3/5): %q", got)
	}
	if !strings.Contains(got, "updated 12s ago") {
		t.Errorf("missing updated-age fragment: %q", got)
	}
}

func TestDeploymentHealthError_AppendsProgressString(t *testing.T) {
	dep := &godo.Deployment{
		ID:        "dep-9",
		Phase:     godo.DeploymentPhase_Deploying,
		UpdatedAt: time.Now().Add(-7 * time.Second),
		Progress:  &godo.DeploymentProgress{SuccessSteps: 1, TotalSteps: 4},
	}
	err := drivers.DeploymentHealthErrorForTest("myapp", dep)
	if err == nil {
		t.Fatal("expected error for DEPLOYING phase")
	}
	if !strings.Contains(err.Error(), "deployment in progress: DEPLOYING (dep-9)") {
		t.Errorf("original prefix not preserved: %v", err)
	}
	if !strings.Contains(err.Error(), "1/4 steps") {
		t.Errorf("progress steps not appended: %v", err)
	}
}
```

The `*ForTest` wrappers are needed because `deploymentProgressString` and `deploymentHealthError` are unexported and the test file is `package drivers_test`. Add to a new file `internal/drivers/export_test.go` — the `_test.go` suffix is **required** so these exports only exist during `go test` and never leak into the production binary:

```go
package drivers

// Test-only exports. The _test.go suffix ensures these are only compiled
// during `go test`, never into the production binary.
var (
	DeploymentProgressStringForTest    = deploymentProgressString
	DeploymentHealthErrorForTest       = deploymentHealthError
	SanitizeClonedSpecForCreateForTest = sanitizeClonedSpecForCreate
)
```

(No imports needed — the `var = funcName` pattern just rebinds the unexported function value to an exported name.)

Add `"time"` to the imports of `deploy_test.go`.

**Step 2: Run tests to verify they fail**

```
GOWORK=off go test ./internal/drivers/... -run 'TestDeploymentProgressString|TestDeploymentHealthError_AppendsProgressString' -count=1
```
Expected: FAIL.

**Step 3: Add helper + wire it**

In `internal/drivers/deploy.go`:

```go
// deploymentProgressString formats a stateless summary of a deployment's
// step progress for inclusion in an in-progress error message. Returns
// the empty string when Progress is nil (common during early Build phases).
func deploymentProgressString(dep *godo.Deployment) string {
	if dep == nil || dep.Progress == nil {
		return ""
	}
	completed := dep.Progress.SuccessSteps + dep.Progress.ErrorSteps
	age := ""
	if !dep.UpdatedAt.IsZero() {
		secs := int(time.Since(dep.UpdatedAt).Round(time.Second).Seconds())
		if secs < 0 {
			secs = 0
		}
		age = fmt.Sprintf("; updated %ds ago", secs)
	}
	return fmt.Sprintf(" [%d/%d steps%s]", completed, dep.Progress.TotalSteps, age)
}
```

Modify the in-progress branch of `deploymentHealthError`:

```go
case godo.DeploymentPhase_PendingBuild,
	godo.DeploymentPhase_Building,
	godo.DeploymentPhase_PendingDeploy,
	godo.DeploymentPhase_Deploying:
	return fmt.Errorf("app deploy: %q deployment in progress: %s (%s)%s", appName, dep.Phase, dep.ID, deploymentProgressString(dep))
```

Original prefix `app deploy: %q deployment in progress: %s (%s)` is unchanged; `%s` of `deploymentProgressString(dep)` appends either `""` or ` [N/M steps; updated Xs ago]`. Add `"time"` import to deploy.go if not already present.

**Step 4: Run tests + full suite**

```
GOWORK=off go test ./internal/drivers/... -count=1
```
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/drivers/deploy.go internal/drivers/deploy_test.go internal/drivers/export_for_test.go
git commit -m "feat(app-platform): append deployment progress signal to in-progress error (#159)

[completed/total steps; updated Xs ago] now appended to the deployment
in-progress message. Stateless: derives 'updated Ns ago' from godo
Deployment.UpdatedAt, completed from SuccessSteps + ErrorSteps. Original
error prefix and parenthesized deployment ID preserved verbatim so
downstream substring matchers continue to work.

Rollback: revert commit; format additive."
```

---

### Task 6: Pin prevalidated-rolling semantics with explicit tests

Tests that explicitly encode current BG behavior so a future maintainer cannot silently regress toward "true promotion." Split: black-box tests go in `deploy_test.go`; tests needing unexported state go in `deploy_internal_test.go`.

**Files:**
- Test: `internal/drivers/deploy_test.go` (black-box; uses public API + mock fields from Task 1)
- Test: `internal/drivers/deploy_internal_test.go` (white-box; needs `d.greenID` and post-SwitchTraffic state)

**Step 1: Add the four pinning tests**

Append to `internal/drivers/deploy_test.go` (`package drivers_test`):

```go
func TestAppBlueGreenDriver_SwitchTraffic_UpdatesBlueSpec(t *testing.T) {
	m := newDeployMock()
	seedApp(m, "blue-id", "blue", "r:v1")
	d := drivers.NewAppBlueGreenDriver(m, "nyc1", "blue-id", "blue")
	if err := d.CreateGreen(context.Background(), "r:v2"); err != nil {
		t.Fatalf("CreateGreen: %v", err)
	}
	if err := d.SwitchTraffic(context.Background()); err != nil {
		t.Fatalf("SwitchTraffic: %v", err)
	}
	if m.lastUpdateRequest == nil {
		t.Fatal("Update was not invoked on blue (SwitchTraffic should in-place-update blue's spec)")
	}
	got := m.lastUpdateRequest.Spec.Services[0].Image.Tag
	if got != "v2" {
		t.Errorf("blue spec tag after SwitchTraffic = %q, want %q (in-place update, not promotion)", got, "v2")
	}
}

func TestAppBlueGreenDriver_GreenURL_IsDefaultIngress_NotCustomDomain(t *testing.T) {
	m := newDeployMock()
	app := seedApp(m, "blue-id", "blue", "r:v1")
	app.Spec.Domains = []*godo.AppDomainSpec{{Domain: "blue.example.com", Type: godo.AppDomainSpecType_Primary}}
	m.createLiveURL = "https://blue-green-abc123.ondigitalocean.app"

	d := drivers.NewAppBlueGreenDriver(m, "nyc1", "blue-id", "blue")
	if err := d.CreateGreen(context.Background(), "r:v2"); err != nil {
		t.Fatalf("CreateGreen: %v", err)
	}
	got, err := d.GreenEndpoint(context.Background())
	if err != nil {
		t.Fatalf("GreenEndpoint: %v", err)
	}
	if !strings.Contains(got, "ondigitalocean.app") {
		t.Errorf("GreenEndpoint = %q, expected default DO ingress (NOT a custom domain)", got)
	}
	if strings.Contains(got, "blue.example.com") {
		t.Errorf("GreenEndpoint = %q, custom domain leaked to green clone", got)
	}
}

func TestAppBlueGreenDriver_DestroyBlue_DeletesGreenNotBlue(t *testing.T) {
	m := newDeployMock()
	seedApp(m, "blue-id", "blue", "r:v1")
	d := drivers.NewAppBlueGreenDriver(m, "nyc1", "blue-id", "blue")
	if err := d.CreateGreen(context.Background(), "r:v2"); err != nil {
		t.Fatalf("CreateGreen: %v", err)
	}
	if err := d.DestroyBlue(context.Background()); err != nil {
		t.Fatalf("DestroyBlue: %v", err)
	}
	// blue-id must still be present; the green clone (the one with an "app-N"
	// ID generated by Create) must be deleted.
	if _, blueLives := m.apps["blue-id"]; !blueLives {
		t.Errorf("blue was deleted; DestroyBlue is misleadingly named — should delete green only")
	}
	if len(m.deletedAppIDs) == 0 {
		t.Errorf("expected green clone to be deleted; deletedAppIDs=%v", m.deletedAppIDs)
	}
	for _, id := range m.deletedAppIDs {
		if id == "blue-id" {
			t.Errorf("DestroyBlue deleted blue (%q); must delete green clone only", id)
		}
	}
}
```

Append to `internal/drivers/deploy_internal_test.go` (`package drivers`; needs unexported state):

```go
func TestAppBlueGreenDriver_HealthCheck_PostSwitchProbesBlueDeployment(t *testing.T) {
	stub := &healthCheckProbeStub{
		app: &godo.App{
			ID: "blue-id",
			Spec: &godo.AppSpec{
				Services: []*godo.AppServiceSpec{{Name: "web", Image: &godo.ImageSourceSpec{Repository: "r", Tag: "v2"}}},
			},
			InProgressDeployment: &godo.Deployment{ID: "dep-2", Phase: godo.DeploymentPhase_Deploying},
		},
	}
	d := NewAppBlueGreenDriverWithDomainProbe(stub, nil, "nyc1", "blue-id", "blue", nil)
	// Simulate the state after CreateGreen + SwitchTraffic without exercising
	// the full path (already tested in deploy_test.go SwitchTraffic test).
	d.greenID = "green-id"
	d.stableCheck = true
	d.blueDriver().waitingForDeployment = true
	d.blueDriver().targetDeploymentID = "dep-2"

	err := d.HealthCheck(context.Background(), "")
	if err == nil {
		t.Fatal("expected in-progress error for blue's new deployment")
	}
	if !strings.Contains(err.Error(), "deployment in progress: DEPLOYING") {
		t.Errorf("error did not surface blue's in-progress deployment: %v", err)
	}
}
```

**Step 2: Run tests to verify they pass**

```
GOWORK=off go test ./internal/drivers/... -run TestAppBlueGreenDriver_ -count=1
```
Expected: PASS (these tests assert current production behavior + the new sanitization fix from Task 1).

**Step 3: Commit**

```bash
git add internal/drivers/deploy_test.go internal/drivers/deploy_internal_test.go
git commit -m "test(app-platform): pin prevalidated-rolling B/G semantics (#159)

4 tests explicitly encode what AppBlueGreenDriver does today:
SwitchTraffic updates blue's spec in-place (not a promotion), GreenEndpoint
returns the default DO ingress (not a custom domain), DestroyBlue deletes
the green clone (misleading method name), HealthCheck after SwitchTraffic
probes blue's new in-progress deployment. Distinguishes in-place semantics
from true traffic-switching so a future regression toward the looser
interpretation will fail these tests."
```

---

### Task 7: Documentation — `docs/DEPLOYMENT_STRATEGIES.md` + README link

**Files:**
- Create: `docs/DEPLOYMENT_STRATEGIES.md`
- Modify: `README.md` (one link in an appropriate section; locate by inspection)

**Step 1: Write `docs/DEPLOYMENT_STRATEGIES.md`**

```markdown
# Deployment Strategies (DO App Platform)

`workflow-plugin-digitalocean` exposes three deployment-strategy drivers via the
workflow-engine module interfaces:

- `AppDeployDriver` — in-place rolling
- `AppBlueGreenDriver` — **prevalidated rolling** (not true traffic-switching)
- `AppCanaryDriver` — partial; `RoutePercent` unsupported

This document describes what each driver actually does, what it guarantees,
and what it does not guarantee.

## `AppDeployDriver` (in-place rolling)

`Update(image)` rewrites the App spec with the new image; DO App Platform's
own RollingUpdate semantics replace running instances. `HealthCheck` polls
until:

1. The target deployment reaches `godo.DeploymentPhase_Active`.
2. Every non-default custom domain on the App spec has `Phase == PHASE_Active`
   in `app.Domains` (the live status array).
3. Every non-wildcard custom domain returns 2xx/3xx on
   `GET https://<domain><readiness path>` within 5s.

**Availability guarantees:**

- For `InstanceCount >= 2`: DO replaces instances rolling. Custom-domain
  HTTPS reachability is verified once the deployment is Active.
- For `InstanceCount < 2`: DO restarts the single instance stop-then-start;
  brief downtime is expected. The in-rollout availability probe will surface
  this in operator logs.

**In-rollout probe.** During `PendingDeploy` / `Deploying`, `HealthCheck`
also probes each non-wildcard custom domain and appends
`; domain probe: X/Y custom domains reachable` to the in-progress error
message. This is observation-only — the gate continues to be "deployment
Active + post-Active domain probe."

## `AppBlueGreenDriver` (prevalidated rolling)

**This driver does not provide true traffic-switching on DO App Platform.**
DO App Platform does not natively support routing a single custom domain to
one of several apps; once attached, a custom domain stays attached to its
app. Switching it to a different app requires DNS / TLS cutover, which is
itself downtime-inducing.

What `AppBlueGreenDriver` actually does:

1. `CreateGreen(image)` — creates a separate "green" app with the new image.
   Custom `Spec.Domains` are stripped from the cloned spec so the green
   clone does not collide with the live custom domain. Green is reachable
   only at its default `*.ondigitalocean.app` URL.
2. `HealthCheck` (pre-Switch) — verifies the green app deploys cleanly under
   DO's runtime checks.
3. `SwitchTraffic` — calls `Update(greenImage)` on the **blue** app,
   triggering DO's in-place rolling deploy on blue with the validated
   image. Blue's custom domain stays attached the whole time.
4. `HealthCheck` (post-Switch) — same gate as `AppDeployDriver` on blue,
   including the in-rollout availability probe described above.
5. `DestroyBlue` — deletes the **green** clone (the method name is
   misleading; the blue app is never destroyed).

**What this buys you:** the new image is started under DO's runtime checks
in isolation before blue is touched. Failed-to-start images never cause
a blue rollout.

**What this does not buy you:** zero-downtime custom-domain handoff. Same
availability properties as `AppDeployDriver` once the rolling deploy of blue
begins.

For true B/G with zero-downtime custom-domain handoff, see the deferred
front-door design (DO LB + Droplets, or an external proxy) — tracked in a
follow-up issue.

## `AppCanaryDriver` (partial)

`CreateCanary` creates a separate canary app (with custom domains stripped,
same as `AppBlueGreenDriver`). `RoutePercent` returns an explicit
"unsupported on App Platform" error; use DO Load Balancer + Droplets for
canary traffic splitting.

## Operator notes

- The in-rollout availability probe meaningfully fires on the blue
  post-`SwitchTraffic` re-deploy. On the green/canary clone, no custom
  domains are attached, so the probe is a no-op there.
- The deployment-in-progress error message includes
  `[N/M steps; updated Xs ago]` (when `Deployment.Progress` is populated by
  DO) so `wfctl infra apply --wait` shows whether the rollout is moving.
```

**Step 2: Add README link**

In `README.md`, locate an appropriate section (e.g., a "Documentation" or "Reference" subsection; if none exists, append after the existing top-level intro). Add:

```markdown
- [Deployment strategies](docs/DEPLOYMENT_STRATEGIES.md) — what `AppDeployDriver`, `AppBlueGreenDriver`, and `AppCanaryDriver` actually do on DO App Platform, including the in-rollout availability probe.
```

**Step 3: Verify**

```
GOWORK=off go test ./... -count=1
```
Expected: PASS (docs don't affect tests; sanity-check no accidental code change).

**Step 4: Commit**

```bash
git add docs/DEPLOYMENT_STRATEGIES.md README.md
git commit -m "docs: deployment-strategies guarantees and limitations (#159)

Honest documentation of what AppDeployDriver, AppBlueGreenDriver, and
AppCanaryDriver actually provide on DO App Platform: in-place rolling vs
prevalidated rolling (the latter is NOT true traffic-switching), custom-
domain readiness gate, in-rollout availability probe behavior, and
InstanceCount<2 single-instance non-guarantee. References deferred front-
door follow-up for true B/G."
```

---

### Task 8: File follow-up issues + capture numbers in PR body (do NOT mutate the design doc)

Per plan adversarial cycle 1 finding I5, the design doc is treated as immutable after cycle-3 PASS. Follow-up issue numbers go into the PR body (and the post-merge retro), not back into the design file.

**Files:**
- (No file changes; uses `gh issue create`.)

**Step 1: File front-door follow-up**

```bash
gh issue create --title "True B/G via DO Load Balancer + Droplets or external front-door (deferred from #159)" --body "$(cat <<'EOF'
## Context

Issue #159 confirmed that `AppBlueGreenDriver` on DO App Platform is
prevalidated rolling, not true traffic-switching. True B/G with
zero-downtime custom-domain handoff requires either:

1. A DO Load Balancer in front of Droplets (the customer manages instances
   directly, not via App Platform).
2. An external front-door (CloudFlare proxy, Fastly, etc.) that can route
   the custom domain to one of multiple backing apps and atomically swap.

Both approaches involve real complexity: provisioning, TLS handoff,
session-affinity decisions, drain behavior, observability. None were
implemented as part of #159 (scope B: audit + tests + rollout-probe).

## Acceptance criteria for a follow-up design

- [ ] Choose between DO LB + Droplets vs external proxy (or support both)
- [ ] Design `AppPrevalidatedRollingDriver` (current behavior) vs
      `AppTrueBlueGreenDriver` (new) split
- [ ] Live validation against gocodealone-multisite or similar real app
- [ ] Documented strategy selection guidance (`when to use which`)

## References

- Closed parent: workflow-plugin-digitalocean#159
- Honest-docs file: docs/DEPLOYMENT_STRATEGIES.md
EOF
)"
```

**Step 2: File interface-rename follow-up**

```bash
gh issue create --title "Rename AppBlueGreenDriver → AppPrevalidatedRollingDriver (workflow-engine interface coordination)" --body "$(cat <<'EOF'
## Context

`AppBlueGreenDriver` implements `module.BlueGreenDriver` from the
workflow-engine. The name is misleading — it does prevalidated rolling, not
true traffic-switching (see #159 + docs/DEPLOYMENT_STRATEGIES.md).

Renaming requires a coordinated workflow-engine cycle:

1. Engine introduces `module.PrevalidatedRollingDriver` interface (alias
   or new contract).
2. DO plugin (and any other plugin implementing the old interface) migrates.
3. Old name deprecated for at least one minor release.

## References

- Parent: workflow-plugin-digitalocean#159
- Honest-docs file: docs/DEPLOYMENT_STRATEGIES.md
EOF
)"
```

**Step 3: Confirm both issues filed**

```
gh issue list --state open --search 'true B/G OR Rename AppBlueGreenDriver' --limit 5
```
Expected: both issues present. Record the issue numbers; they will be cited in the PR body when PR is opened.

**Step 4: No commit**

Task 8 produces no code or doc changes — the issue numbers are captured in the eventual PR body, not in any committed file. The design doc remains immutable.
