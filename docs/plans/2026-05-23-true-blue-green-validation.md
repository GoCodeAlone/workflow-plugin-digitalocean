# True Blue/Green Validation for DO App Platform Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make `AppBlueGreenDriver` + `AppCanaryDriver` safe against custom-domain collisions on green/canary clone Create, surface an in-rollout custom-domain availability probe during `waitingForDeployment`, enrich deployment-in-progress errors with stateless progress info, pin the actual prevalidated-rolling semantics with tests, and document what each strategy guarantees.

**Architecture:** Surgical fix at the green/canary clone Create call sites (strip `Spec.Domains` only; leave `Ingress` untouched) + a probe-loop extraction so the existing post-Active custom-domain probe can also fire during the `Deploying`/`PendingDeploy` window without changing the gate semantics. Error-string enrichment is purely additive — original prefix and parenthesized deployment ID preserved verbatim, `%w`-wrapping preserves `errors.Is`/`errors.As`.

**Tech Stack:** Go 1.26, `github.com/digitalocean/godo` v1.178, standard testing + table-driven tests; no new dependencies.

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

**PR Grouping:**

| PR # | Title | Tasks | Branch |
|------|-------|-------|--------|
| 1 | fix(app-platform): strip custom domains from green/canary clones + in-rollout probe + progress signal (#159) | Task 1, Task 2, Task 3, Task 4, Task 5, Task 6, Task 7, Task 8 | feat/159-bg-validation-and-rollout-probe |

**Status:** Draft

---

### Task 1: `sanitizeClonedSpecForCreate` + Domains-strip in BG/Canary Create paths

Closes critical C1 from adversarial cycle 1. Adds the sanitization helper and invokes it from `CreateGreen` + `CreateCanary` so the cloned spec the green/canary `Apps.Create` receives no longer carries blue/stable's custom `Spec.Domains`.

**Files:**
- Modify: `internal/drivers/deploy.go` (CreateGreen ~line 326; CreateCanary ~line 469)
- Test: `internal/drivers/deploy_test.go` (3 new tests)

**Step 1: Write failing tests**

Add to `internal/drivers/deploy_test.go`:

```go
func TestSanitizeClonedSpecForCreate_PreservesIngress(t *testing.T) {
	spec := &godo.AppSpec{
		Name: "blue",
		Domains: []*godo.AppDomainSpec{
			{Domain: "example.com", Type: godo.AppDomainSpecType_Primary},
		},
		Ingress: &godo.AppIngressSpec{
			Rules: []*godo.AppIngressSpecRule{
				{Match: &godo.AppIngressSpecRuleRoutingMatch{Path: &godo.AppIngressSpecRuleStringMatch{Prefix: "/api"}}},
				{Match: &godo.AppIngressSpecRuleRoutingMatch{Path: &godo.AppIngressSpecRuleStringMatch{Prefix: "/"}}},
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
	if len(spec.Services) != 1 {
		t.Fatalf("Services altered, got %d entries", len(spec.Services))
	}
}

func TestAppBlueGreenDriver_CreateGreen_StripsCustomDomainsFromGreenSpec(t *testing.T) {
	blueSpec := &godo.AppSpec{
		Name: "blue",
		Domains: []*godo.AppDomainSpec{
			{Domain: "blue.example.com", Type: godo.AppDomainSpecType_Primary},
		},
		Services: []*godo.AppServiceSpec{{
			Name:  "web",
			Image: &godo.ImageSourceSpec{Repository: "old", Tag: "v1"},
		}},
	}
	client := newFakeAppPlatformClient(t)
	client.seedApp("blue-id", &godo.App{ID: "blue-id", Spec: blueSpec, LiveURL: "https://blue.example.com"})

	d := NewAppBlueGreenDriver(client, "nyc1", "blue-id", "blue")
	if err := d.CreateGreen(context.Background(), "registry.example.com/img:v2"); err != nil {
		t.Fatalf("CreateGreen: %v", err)
	}

	if len(client.lastCreateRequest.Spec.Domains) != 0 {
		t.Fatalf("green clone inherited blue's Domains: %#v", client.lastCreateRequest.Spec.Domains)
	}
}

func TestAppCanaryDriver_CreateCanary_StripsCustomDomainsFromCanarySpec(t *testing.T) {
	stableSpec := &godo.AppSpec{
		Name: "stable",
		Domains: []*godo.AppDomainSpec{
			{Domain: "stable.example.com", Type: godo.AppDomainSpecType_Primary},
		},
		Services: []*godo.AppServiceSpec{{
			Name:  "web",
			Image: &godo.ImageSourceSpec{Repository: "old", Tag: "v1"},
		}},
	}
	client := newFakeAppPlatformClient(t)
	client.seedApp("stable-id", &godo.App{ID: "stable-id", Spec: stableSpec, LiveURL: "https://stable.example.com"})

	d := NewAppCanaryDriver(client, "nyc1", "stable-id", "stable")
	if err := d.CreateCanary(context.Background(), "registry.example.com/img:v2"); err != nil {
		t.Fatalf("CreateCanary: %v", err)
	}

	if len(client.lastCreateRequest.Spec.Domains) != 0 {
		t.Fatalf("canary clone inherited stable's Domains: %#v", client.lastCreateRequest.Spec.Domains)
	}
}
```

If `lastCreateRequest` is not already captured by the existing fake, also add the capture in the fake's `Create` method (verify by reading the existing fake in `internal/drivers/deploy_test.go` first; reuse if available).

**Step 2: Run tests to verify they fail**

```
GOWORK=off go test ./internal/drivers/... -run 'TestSanitize|TestAppBlueGreenDriver_CreateGreen_StripsCustomDomains|TestAppCanaryDriver_CreateCanary_StripsCustomDomains' -count=1
```
Expected: FAIL — `sanitizeClonedSpecForCreate undefined` and the Domains assertions fail (current code passes Domains through).

**Step 3: Add `sanitizeClonedSpecForCreate` + call sites**

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

Then in `CreateGreen`, after `greenSpec.Name = d.blueName + "-green"`:

```go
sanitizeClonedSpecForCreate(greenSpec)
```

And in `CreateCanary`, after `canarySpec.Name = d.stableName + "-canary"`:

```go
sanitizeClonedSpecForCreate(canarySpec)
```

**Step 4: Run tests to verify they pass + run full driver tests for regressions**

```
GOWORK=off go test ./internal/drivers/... -count=1
```
Expected: PASS, no regressions.

**Step 5: Commit**

```bash
git add internal/drivers/deploy.go internal/drivers/deploy_test.go
git commit -m "fix(app-platform): strip custom Domains from green/canary cloned spec (#159)

CreateGreen and CreateCanary deep-cloned blue/stable's Spec via cloneAppSpec,
including Spec.Domains. DO App Platform rejects creating a second app that
claims an already-attached custom domain. sanitizeClonedSpecForCreate now
clears only Spec.Domains; Ingress and all other surface is preserved."
```

---

### Task 2: Add `*WithDomainProbe` constructors for BG + Canary drivers

Required to make the in-rollout probe tests (Task 4) injectable. Mirrors the existing `NewAppDeployDriverWithDomainProbe` pattern.

**Files:**
- Modify: `internal/drivers/deploy.go` (AppBlueGreenDriver + AppCanaryDriver structs + constructor block)
- Test: `internal/drivers/deploy_test.go`

**Step 1: Write the failing test**

```go
func TestNewAppBlueGreenDriverWithDomainProbe_InjectsIntoInnerDrivers(t *testing.T) {
	var calls int
	probe := func(_ context.Context, _, _ string) error {
		calls++
		return nil
	}
	d := NewAppBlueGreenDriverWithDomainProbe(nil, nil, "nyc1", "blue-id", "blue", probe)
	if d.blueDriver().domainProbe == nil {
		t.Fatal("blue driver missing injected probe")
	}
	d.greenID = "green-id"
	if d.greenDriver().domainProbe == nil {
		t.Fatal("green driver missing injected probe")
	}
	_ = calls
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

**Step 2: Run tests to verify they fail**

```
GOWORK=off go test ./internal/drivers/... -run 'TestNewAppBlueGreenDriverWithDomainProbe|TestNewAppCanaryDriverWithDomainProbe' -count=1
```
Expected: FAIL — constructors undefined.

**Step 3: Add constructors + plumbing**

Add a `domainProbe AppPlatformDomainProbe` field to `AppBlueGreenDriver` and `AppCanaryDriver` structs. Add constructors:

```go
func NewAppBlueGreenDriverWithDomainProbe(c AppPlatformClient, reg DOCRClient, region, blueID, blueName string, probe AppPlatformDomainProbe) *AppBlueGreenDriver {
	d := NewAppBlueGreenDriverWithRegistry(c, reg, region, blueID, blueName)
	d.domainProbe = probe
	return d
}

func NewAppCanaryDriverWithDomainProbe(c AppPlatformClient, reg DOCRClient, region, stableID, stableName string, probe AppPlatformDomainProbe) *AppCanaryDriver {
	d := NewAppCanaryDriverWithRegistry(c, reg, region, stableID, stableName)
	d.domainProbe = probe
	return d
}
```

Modify `blueDriver()` / `greenDriver()` / `stableDriver()` / `canaryDriver()` accessors so that when they lazily build an inner `*AppDeployDriver` they call `NewAppDeployDriverWithDomainProbe` (the existing variant) when `d.domainProbe != nil`, else fall back to `NewAppDeployDriverWithRegistry`. Verify `NewAppDeployDriverWithDomainProbe` exists at `deploy.go:39` (mentioned in design); if signature differs, adjust.

**Step 4: Run tests to verify they pass**

```
GOWORK=off go test ./internal/drivers/... -count=1
```
Expected: PASS, no regressions.

**Step 5: Commit**

```bash
git add internal/drivers/deploy.go internal/drivers/deploy_test.go
git commit -m "feat(app-platform): NewAppBlueGreenDriver/CanaryDriverWithDomainProbe constructors (#159)

Mirrors NewAppDeployDriverWithDomainProbe. Required so the in-rollout
custom-domain probe added in a follow-up task is injectable in unit tests
without rewriting the http client. Existing constructors unchanged."
```

---

### Task 3: Extract `appPlatformProbeCustomDomains` from post-Active loop

Refactor: pull the per-domain probe loop out of `appPlatformCustomDomainReadinessError` (`app_platform_readiness.go:23`) into a stateless `appPlatformProbeCustomDomains(ctx, app, probe, path) (reachable, total int)` helper. The post-Active path continues to derive its error from the loop's last failure. No behavior change for the existing post-Active gate.

**Files:**
- Modify: `internal/drivers/app_platform_readiness.go`
- Test: `internal/drivers/app_platform_test.go` (add one helper-direct test; verify existing post-Active tests still pass)

**Step 1: Write the failing test**

```go
func TestAppPlatformProbeCustomDomains_CountsReachable(t *testing.T) {
	app := &godo.App{
		Spec: &godo.AppSpec{
			Domains: []*godo.AppDomainSpec{
				{Domain: "a.example.com"},
				{Domain: "b.example.com"},
				{Domain: "c.example.com", Wildcard: true}, // skipped per existing loop
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

**Step 2: Run test to verify it fails**

```
GOWORK=off go test ./internal/drivers/... -run TestAppPlatformProbeCustomDomains_CountsReachable -count=1
```
Expected: FAIL — `appPlatformProbeCustomDomains undefined`.

**Step 3: Add helper + refactor post-Active loop**

In `app_platform_readiness.go`:

```go
// appPlatformProbeCustomDomains GETs each non-wildcard custom domain on app
// and returns how many were reachable (2xx/3xx within the probe timeout).
// total excludes wildcard entries because there is no concrete host to probe.
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

Refactor `appPlatformCustomDomainReadinessError` so its probe loop uses the same iteration but returns the first error encountered (preserves existing behavior). Verify the existing post-Active tests pass unchanged.

**Step 4: Run full driver suite to verify no regression**

```
GOWORK=off go test ./internal/drivers/... -count=1
```
Expected: PASS including all existing readiness tests.

**Step 5: Commit**

```bash
git add internal/drivers/app_platform_readiness.go internal/drivers/app_platform_test.go
git commit -m "refactor(app-platform): extract appPlatformProbeCustomDomains (#159)

No behavior change. The probe loop is now reusable by both the post-Active
readiness gate and the in-rollout availability probe added in a follow-up
task. Existing tests pin the post-Active semantics."
```

---

### Task 4: Wire in-rollout probe into `AppDeployDriver.HealthCheck`

Closes critical C2 from adversarial cycle 1. Composes the new probe helper with `deploymentHealthError` only when the phase is `PendingDeploy` or `Deploying` (the routing-affecting window). Uses `%w` so `errors.Is`/`errors.As` continue to work for any consumer that unwraps.

**Files:**
- Modify: `internal/drivers/deploy.go` (HealthCheck composition + new `isInProgressPhase` helper)
- Test: `internal/drivers/deploy_test.go`

**Step 1: Write the failing test**

```go
func TestAppDeployDriver_HealthCheck_DuringRolloutProbesDomain(t *testing.T) {
	app := &godo.App{
		ID:   "blue-id",
		Spec: &godo.AppSpec{
			Domains: []*godo.AppDomainSpec{{Domain: "blue.example.com"}},
			Services: []*godo.AppServiceSpec{{Name: "web", Image: &godo.ImageSourceSpec{Repository: "r", Tag: "v2"}}},
		},
		InProgressDeployment: &godo.Deployment{
			ID:    "dep-2",
			Phase: godo.DeploymentPhase_Deploying,
		},
		LiveDomains: []*godo.AppDomain{{Spec: &godo.AppDomainSpec{Domain: "blue.example.com"}, Phase: godo.AppJobSpecKindPHASE_Active}},
	}
	client := newFakeAppPlatformClient(t)
	client.seedApp("blue-id", app)

	var probeCalls int
	probe := func(_ context.Context, domain, _ string) error {
		probeCalls++
		return fmt.Errorf("simulated downtime")
	}
	d := NewAppDeployDriverWithDomainProbe(client, nil, "nyc1", "blue-id", "blue", probe)
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
	app := &godo.App{
		ID: "blue-id",
		Spec: &godo.AppSpec{
			Domains: []*godo.AppDomainSpec{{Domain: "blue.example.com"}},
			Services: []*godo.AppServiceSpec{{Name: "web"}},
		},
		InProgressDeployment: &godo.Deployment{ID: "dep-2", Phase: godo.DeploymentPhase_Building},
	}
	client := newFakeAppPlatformClient(t)
	client.seedApp("blue-id", app)

	var probeCalls int
	probe := func(_ context.Context, _, _ string) error { probeCalls++; return nil }
	d := NewAppDeployDriverWithDomainProbe(client, nil, "nyc1", "blue-id", "blue", probe)
	d.waitingForDeployment = true
	d.targetDeploymentID = "dep-2"

	_ = d.HealthCheck(context.Background(), "")
	if probeCalls != 0 {
		t.Fatalf("probe fired during BUILDING phase (%d calls); should be skipped", probeCalls)
	}
}
```

**Step 2: Run tests to verify they fail**

```
GOWORK=off go test ./internal/drivers/... -run 'TestAppDeployDriver_HealthCheck_DuringRollout|TestAppDeployDriver_HealthCheck_DuringBuild' -count=1
```
Expected: FAIL — no probe runs during in-progress.

**Step 3: Add `isInProgressPhase` + wire the probe**

In `internal/drivers/deploy.go`:

```go
func isInProgressPhase(p godo.AppDeploymentPhase) bool {
	switch p {
	case godo.DeploymentPhase_PendingDeploy, godo.DeploymentPhase_Deploying:
		return true
	default:
		return false
	}
}
```

Modify `AppDeployDriver.HealthCheck` (current `deploy.go:81-93` block):

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

**Step 4: Run tests to verify they pass + full driver suite**

```
GOWORK=off go test ./internal/drivers/... -count=1
```
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/drivers/deploy.go internal/drivers/deploy_test.go
git commit -m "feat(app-platform): in-rollout custom-domain probe during DEPLOYING (#159)

HealthCheck now invokes the custom-domain probe during PendingDeploy/Deploying
and appends reachable/total to the in-progress error message via %w-wrap. Old
error string prefix and parenthesized deployment ID preserved verbatim.
PendingBuild/Building phases skip the probe to avoid log noise from the
unchanged-routing window.

Rollback: revert commit; reverts purely additive error-string enrichment +
probe call; no plugin contract change."
```

---

### Task 5: `deploymentProgressString` + wire into `deploymentHealthError`

Adds stateless `[N/M steps; updated Ns ago]` suffix to the in-progress error. Uses `godo.Deployment.Progress` + `Deployment.UpdatedAt`; nil-safe.

**Files:**
- Modify: `internal/drivers/deploy.go` (deploymentHealthError + new deploymentProgressString)
- Test: `internal/drivers/deploy_test.go`

**Step 1: Write the failing tests**

```go
func TestDeploymentProgressString_NilProgressIsEmpty(t *testing.T) {
	got := deploymentProgressString(&godo.Deployment{Phase: godo.DeploymentPhase_Deploying})
	if got != "" {
		t.Fatalf("expected empty string for nil Progress, got %q", got)
	}
}

func TestDeploymentProgressString_UpdatedAtAgeFormatting(t *testing.T) {
	now := time.Now()
	dep := &godo.Deployment{
		Phase:     godo.DeploymentPhase_Deploying,
		UpdatedAt: now.Add(-12 * time.Second),
		Progress: &godo.DeploymentProgress{
			StepsCompleted: 3,
			StepsTotal:     5,
		},
	}
	got := deploymentProgressString(dep)
	if !strings.Contains(got, "3/5 steps") {
		t.Errorf("missing steps fragment: %q", got)
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
		Progress:  &godo.DeploymentProgress{StepsCompleted: 1, StepsTotal: 4},
	}
	err := deploymentHealthError("myapp", dep)
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

**Step 2: Run tests to verify they fail**

```
GOWORK=off go test ./internal/drivers/... -run 'TestDeploymentProgressString|TestDeploymentHealthError_AppendsProgressString' -count=1
```
Expected: FAIL.

**Step 3: Add helper + wire it**

```go
func deploymentProgressString(dep *godo.Deployment) string {
	if dep == nil || dep.Progress == nil {
		return ""
	}
	age := ""
	if !dep.UpdatedAt.IsZero() {
		secs := int(time.Since(dep.UpdatedAt).Round(time.Second).Seconds())
		if secs < 0 {
			secs = 0
		}
		age = fmt.Sprintf("; updated %ds ago", secs)
	}
	return fmt.Sprintf(" [%d/%d steps%s]", dep.Progress.StepsCompleted, dep.Progress.StepsTotal, age)
}
```

Modify `deploymentHealthError`'s in-progress branch (currently `deploy.go:142-146`) to append `deploymentProgressString(dep)` to the error string. **Preserve the existing string exactly up to and including the parenthesized deployment ID** — append only after.

Example before:

```
app deploy: "myapp" deployment in progress: DEPLOYING (dep-9)
```

After:

```
app deploy: "myapp" deployment in progress: DEPLOYING (dep-9) [1/4 steps; updated 7s ago]
```

**Step 4: Run tests + full driver suite**

```
GOWORK=off go test ./internal/drivers/... -count=1
```
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/drivers/deploy.go internal/drivers/deploy_test.go
git commit -m "feat(app-platform): append deployment progress signal to in-progress error (#159)

[N/M steps; updated Xs ago] now appended to the deploymentHealthError
in-progress message. Stateless: derives 'updated Ns ago' from godo
Deployment.UpdatedAt, no counter state on the driver. Original error prefix
and parenthesized deployment ID preserved verbatim so downstream substring
matchers continue to work."
```

---

### Task 6: Pin prevalidated-rolling semantics with explicit tests

Closes the user's "Add tests that distinguish in-place image updates from true traffic handoff semantics" ask. These tests deliberately encode what the BG driver does today so a future maintainer cannot silently regress toward "true promotion" without updating the tests.

**Files:**
- Test: `internal/drivers/deploy_test.go`

**Step 1: Add the four pinning tests**

```go
func TestAppBlueGreenDriver_SwitchTraffic_UpdatesBlueSpec(t *testing.T) {
	blueSpec := &godo.AppSpec{
		Name: "blue",
		Services: []*godo.AppServiceSpec{{
			Name:  "web",
			Image: &godo.ImageSourceSpec{Repository: "r", Tag: "v1"},
		}},
	}
	client := newFakeAppPlatformClient(t)
	client.seedApp("blue-id", &godo.App{ID: "blue-id", Spec: blueSpec})
	d := NewAppBlueGreenDriver(client, "nyc1", "blue-id", "blue")
	if err := d.CreateGreen(context.Background(), "r:v2"); err != nil {
		t.Fatalf("CreateGreen: %v", err)
	}
	if err := d.SwitchTraffic(context.Background()); err != nil {
		t.Fatalf("SwitchTraffic: %v", err)
	}
	// Assert blue's spec was updated to v2 (in-place), not that traffic moved to green.
	if got := client.lastUpdateRequest.Spec.Services[0].Image.Tag; got != "v2" {
		t.Errorf("blue spec tag after SwitchTraffic = %q, want %q (in-place update, not promotion)", got, "v2")
	}
}

func TestAppBlueGreenDriver_GreenURL_IsDefaultIngress_NotCustomDomain(t *testing.T) {
	blueSpec := &godo.AppSpec{
		Name: "blue",
		Domains: []*godo.AppDomainSpec{{Domain: "blue.example.com", Type: godo.AppDomainSpecType_Primary}},
		Services: []*godo.AppServiceSpec{{Name: "web", Image: &godo.ImageSourceSpec{Repository: "r", Tag: "v1"}}},
	}
	client := newFakeAppPlatformClient(t)
	client.seedApp("blue-id", &godo.App{ID: "blue-id", Spec: blueSpec})
	client.createLiveURL = "https://blue-green-abc123.ondigitalocean.app"
	d := NewAppBlueGreenDriver(client, "nyc1", "blue-id", "blue")
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
	blueSpec := &godo.AppSpec{Name: "blue", Services: []*godo.AppServiceSpec{{Name: "web", Image: &godo.ImageSourceSpec{Repository: "r", Tag: "v1"}}}}
	client := newFakeAppPlatformClient(t)
	client.seedApp("blue-id", &godo.App{ID: "blue-id", Spec: blueSpec})
	d := NewAppBlueGreenDriver(client, "nyc1", "blue-id", "blue")
	if err := d.CreateGreen(context.Background(), "r:v2"); err != nil {
		t.Fatalf("CreateGreen: %v", err)
	}
	greenID := d.greenID
	if err := d.DestroyBlue(context.Background()); err != nil {
		t.Fatalf("DestroyBlue: %v", err)
	}
	if !slices.Contains(client.deletedAppIDs, greenID) {
		t.Errorf("expected green %q to be deleted; deletedAppIDs=%v", greenID, client.deletedAppIDs)
	}
	if slices.Contains(client.deletedAppIDs, "blue-id") {
		t.Errorf("blue was deleted; DestroyBlue is misleadingly named — should delete green only")
	}
}

func TestAppBlueGreenDriver_HealthCheck_PostSwitchProbesBlueDeployment(t *testing.T) {
	blueSpec := &godo.AppSpec{
		Name: "blue",
		Services: []*godo.AppServiceSpec{{Name: "web", Image: &godo.ImageSourceSpec{Repository: "r", Tag: "v1"}}},
	}
	client := newFakeAppPlatformClient(t)
	client.seedApp("blue-id", &godo.App{
		ID: "blue-id", Spec: blueSpec,
		InProgressDeployment: &godo.Deployment{ID: "dep-2", Phase: godo.DeploymentPhase_Deploying},
	})
	d := NewAppBlueGreenDriver(client, "nyc1", "blue-id", "blue")
	if err := d.CreateGreen(context.Background(), "r:v2"); err != nil {
		t.Fatalf("CreateGreen: %v", err)
	}
	if err := d.SwitchTraffic(context.Background()); err != nil {
		t.Fatalf("SwitchTraffic: %v", err)
	}
	err := d.HealthCheck(context.Background(), "")
	if err == nil {
		t.Fatal("expected in-progress error for blue's new deployment")
	}
	if !strings.Contains(err.Error(), "blue") {
		t.Errorf("error not from blue driver: %v", err)
	}
}
```

If the existing fake doesn't capture `lastUpdateRequest`, `deletedAppIDs`, or `createLiveURL`, extend it minimally to do so. Read the existing fake first.

**Step 2: Run tests to verify they pass (or fail naturally where the fake needs extension)**

```
GOWORK=off go test ./internal/drivers/... -run TestAppBlueGreenDriver_ -count=1
```
Expected: PASS (tests assert current behavior; no production code changes here other than fake extensions).

**Step 3: Commit**

```bash
git add internal/drivers/deploy_test.go
git commit -m "test(app-platform): pin prevalidated-rolling B/G semantics (#159)

Adds 4 tests that explicitly encode what AppBlueGreenDriver does today:
SwitchTraffic updates blue's spec in-place (not a promotion), GreenEndpoint
returns the default DO ingress (not a custom domain), DestroyBlue deletes
the green clone (misleading method name), HealthCheck after SwitchTraffic
probes blue's new in-progress deployment. Distinguishes in-place semantics
from true traffic-switching so a future regression toward the looser
interpretation will fail these tests."
```

---

### Task 7: Documentation — `docs/DEPLOYMENT_STRATEGIES.md` + README link

Closes the user's "Document the supported guarantees and any DO platform limitations" ask. Single-page operator-facing doc; honest about what each strategy does.

**Files:**
- Create: `docs/DEPLOYMENT_STRATEGIES.md`
- Modify: `README.md` (add one link in a "Deployment strategies" subsection)

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
   in `LiveDomains`.
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

Verified against `workflow-plugin-digitalocean` v2.0.16.
```

**Step 2: Add README link**

In `README.md`, add to the existing TOC / sections (locate by reading the file first):

```markdown
- [Deployment strategies](docs/DEPLOYMENT_STRATEGIES.md) — what `AppDeployDriver`, `AppBlueGreenDriver`, and `AppCanaryDriver` actually do on DO App Platform, including the in-rollout availability probe.
```

**Step 3: Verify with `go test ./...` (no test impact) + visual review**

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

### Task 8: File follow-up issues (front-door + interface rename)

The two deferred items must have GitHub issues so they are not lost. Follow-up filing is in scope per the design § "Follow-ups filed."

**Files:**
- (No code changes; uses `gh issue create`.)

**Step 1: File front-door follow-up**

```bash
gh issue create --title "True B/G via DO Load Balancer + Droplets or external front-door (deferred from #159)" --body "$(cat <<'EOF'
## Context

Issue #159 confirmed that \`AppBlueGreenDriver\` on DO App Platform is
prevalidated rolling, not true traffic-switching. True B/G with
zero-downtime custom-domain handoff requires either:

1. A DO Load Balancer in front of Droplets (the customer manages instances
   directly, not via App Platform).
2. An external front-door (CloudFlare proxy, Fastly, etc.) that can route
   the custom domain to one of multiple backing apps and atomically swap.

Both approaches involve real complexity: provisioning, TLS handoff,
session-affinity decisions, drain behavior, observability. None of them
were implemented as part of #159 (scope B: audit + tests + rollout-probe).

## Acceptance criteria for a follow-up design

- [ ] Choose between DO LB + Droplets vs external proxy (or support both)
- [ ] Design \`AppPrevalidatedRollingDriver\` (current behavior) vs
      \`AppTrueBlueGreenDriver\` (new) split
- [ ] Live validation against gocodealone-multisite or similar real app
- [ ] Documented strategy selection guidance (\`when to use which\`)

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

\`AppBlueGreenDriver\` implements \`module.BlueGreenDriver\` from the
workflow-engine. The name is misleading — it does prevalidated rolling, not
true traffic-switching (see #159 + docs/DEPLOYMENT_STRATEGIES.md).

Renaming requires a coordinated workflow-engine cycle:

1. Engine introduces \`module.PrevalidatedRollingDriver\` interface (alias
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
Expected: both issues present.

**Step 4: Commit (no code changes; commit a one-line update to the design doc cross-linking the follow-ups)**

Edit `docs/plans/2026-05-23-true-blue-green-validation-design.md` § "Follow-ups filed":

```
- True B/G via DO LB+Droplets or external front-door — separate design (filed as #NNN).
- Rename `AppBlueGreenDriver` → `AppPrevalidatedRollingDriver` — requires workflow-engine interface rename; coordinated cycle (filed as #NNN).
```

```bash
git add docs/plans/2026-05-23-true-blue-green-validation-design.md
git commit -m "docs(plan): link filed follow-up issues (#159)"
```
