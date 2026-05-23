# True blue/green validation for DO App Platform — design

Issue: GoCodeAlone/workflow-plugin-digitalocean#159
Scope: B — audit + tests + rollout-progress probe (NOT front-door redesign)
Date: 2026-05-23

## Audit findings

### What `AppBlueGreenDriver` actually does (verbatim from `internal/drivers/deploy.go:186-311`)

1. `CreateGreen(image)` — clones blue's `App.Spec`, renames to `<blueName>-green`, sets the new image, calls `Apps.Create`. Records the green app's ID + `LiveURL` (default `*.ondigitalocean.app`).
2. `HealthCheck` — while `stableCheck=false`, delegates to the green driver; once `SwitchTraffic` flips `stableCheck=true`, delegates to blue.
3. `SwitchTraffic` — reads green's current image, calls `d.blueDriver().Update(image)`. This updates **blue's spec** with the green image, which DO App Platform reconciles as a rolling in-place deploy of blue. **Blue's custom domain stays attached the whole time.** Green is never promoted to serve the custom domain.
4. `DestroyBlue` — deletes the green clone (misleading method name; the blue app is never destroyed).
5. `GreenEndpoint` — returns the green clone's `*.ondigitalocean.app` URL.

### What `AppDeployDriver.HealthCheck` already gates on

After v2.0.15 (PR #157):

1. `waitingForDeployment` (set by `Update`) → blocks until target deployment reaches `godo.DeploymentPhase_Active`.
2. After Active → `appPlatformCustomDomainReadinessError` runs:
   - Every non-default custom domain on the App spec must have `Phase == PHASE_Active` in `LiveDomains`.
   - Every non-wildcard custom domain must return 2xx/3xx on `GET https://<domain><readiness path>` within 5s.

So the post-rollout readiness gate is in place. What's missing per the issue is:

- **Honest documentation** that `AppBlueGreenDriver` is not true traffic-switching — it is prevalidated in-place rolling. The "green" environment exists only as a pre-flight smoke target.
- **Tests** that distinguish prevalidated-rolling from true B/G so the semantics cannot quietly regress.
- **Rollout-time progress signal** so an operator running `wfctl infra apply --wait` sees deployment progress (which step / steps_completed / steps_total) rather than a static "deployment in progress" message that gives no indication of whether anything is moving.

## Out of scope

- True traffic-switching via DO Load Balancer + Droplets (deferred; filed as follow-up). DO App Platform does not expose custom-domain handoff between apps without TLS re-issuance + DNS cutover, both of which are downtime-inducing.
- CloudFlare / external front-door wiring.
- Rewriting `SwitchTraffic` to actually perform a green-promotion (not possible on App Platform alone).
- Renaming `AppBlueGreenDriver` (would break the `module.BlueGreenDriver` interface contract; rename belongs in a separate workflow-engine cycle).

## Proposed changes

### 1. Documentation — `docs/DEPLOYMENT_STRATEGIES.md` (new)

A single page that documents what each strategy actually does on DO App Platform:

- `AppDeployDriver` — in-place rolling via DO App Platform's own RollingUpdate; rollout health = deployment phase Active + custom-domain HTTPS readiness.
- `AppBlueGreenDriver` — **prevalidated rolling**, not true traffic-switching. Green is created as a separate App for pre-flight image validation; SwitchTraffic discards the green and updates blue in place. Guarantees: image starts cleanly under DO's own runtime checks before production blue is touched. Non-guarantees: zero-downtime custom-domain handoff (relies on DO's in-place rolling).
- `AppCanaryDriver` — not supported; reroute to LB+Droplets.

Linked from `README.md`.

### 2. Tests — pin prevalidated-rolling semantics

Add to `internal/drivers/deploy_test.go`:

- `TestAppBlueGreenDriver_SwitchTraffic_UpdatesBlueSpec` — assert post-SwitchTraffic, blue's spec image equals the green image (in-place update, not promotion).
- `TestAppBlueGreenDriver_GreenURL_IsDefaultIngress_NotCustomDomain` — assert `GreenEndpoint()` returns the green clone's `*.ondigitalocean.app` URL, not any custom domain on the blue app.
- `TestAppBlueGreenDriver_HealthCheck_PostSwitchProbesBlueDeploymentAndDomain` — after SwitchTraffic, HealthCheck must probe blue's new in-progress deployment AND, once Active, the custom-domain readiness path (same gate as `AppDeployDriver`).
- `TestAppBlueGreenDriver_DestroyBlue_DeletesGreenNotBlue` — explicit assertion about the misleading method name's actual behavior.

These tests use the existing fake `AppPlatformClient` pattern and the new `AppPlatformDomainProbe` fake.

### 3. Rollout-progress probe — surface deployment progress in HealthCheck errors

Today, `deploymentHealthError` returns:

```
app deploy: "myapp" deployment in progress: DEPLOYING (a1b2-...)
```

That message gives the operator no signal that anything is moving. DO's `godo.Deployment` carries a `Progress *DeploymentProgress` struct with `StepsCompleted`, `StepsTotal`, `RunningSteps`, `PendingSteps`, `SuccessSteps`, `ErrorSteps`. Surface this:

```
app deploy: "myapp" deployment in progress: DEPLOYING (a1b2-...) [3/5 steps; running: deploy:web; pending: deploy:worker]
```

Plus a stuck-deploy heuristic: if `Phase` and `StepsCompleted` have not advanced across `N` (default 12) consecutive `HealthCheck` calls, append `; no progress for ~Ns` to the error so operator log shows it has stalled. This is informational only — the error continues to be returned each call so wfctl --wait's exponential-backoff continues unchanged.

Implementation:

- New helper `deploymentProgressString(dep *godo.Deployment) string` formatting the Progress struct safely (handles nil).
- `AppDeployDriver` tracks `lastProgressSig` (Phase + StepsCompleted hash) and `consecutiveNoProgress` counter; reset on signature change.
- `deploymentHealthError` accepts the counter and appends the stalled-warning fragment when over threshold.
- No new public API; threshold is a package-level `var` for test overrideability.

### 4. CHANGELOG entry

v2.0.16 — Documentation + tests + rollout-progress signal. No behavior change to the rolling-deploy gate itself.

## Assumptions

A1. `godo.Deployment.Progress` is populated for in-progress deployments. (Verified against godo v1.178 source.)
A2. DO App Platform's own rolling deploy is the actual zero-downtime mechanism. If DO's rolling implementation drops requests under load, that's an upstream platform property; not solvable in-plugin without leaving App Platform entirely.
A3. The `module.BlueGreenDriver` interface from workflow-engine cannot be renamed in this PR; the misleading "blue/green" name is documented instead.
A4. wfctl `infra apply --wait` polls `HealthCheck` on a backoff; the rollout-progress probe only changes the error MESSAGE, not the polling cadence.
A5. The user's stated zero-downtime experience with v2.0.15 (gocodealone-multisite deploy run 26287776784, smoke pass immediately after wait) means the existing post-rollout gate is correct in practice. The fix here is honesty + observability, not new gates.

## Top 3 doubts (self-challenge)

D1. **Is surfacing Progress in error messages overengineered?** Mitigation: the bar is low (one helper + counter); skip the stuck-warning if the adversarial-review calls it YAGNI. Strip down to Progress-only string if needed.
D2. **Will the tests against an in-memory client really catch a regression of the "switch to green-promotion" kind?** Mitigation: the tests assert on the App Spec mutation pattern, which is the actual mechanism. A future maintainer who tries to make SwitchTraffic do a real promotion would have to change blue's spec OR rewire HealthCheck delegation — both caught.
D3. **Does the docs page risk going stale?** Mitigation: keep it short (single page); link from README; mark with a "verified against v2.0.16" footer so drift is visible.

## Rollback

- Pure-additive change: new tests + new doc file + improved error string. No interface, no contract, no behavior gate change.
- Revert is `git revert <commit>` cleanly; no migration, no state, no flag.
- Downstream wfctl that pattern-matches on the old error string substring (`"deployment in progress: "`) continues to match — the old prefix is preserved; only the suffix is enriched.

## Test plan

- Unit tests added per §2.
- `go test ./internal/drivers/... -count=1 -race` green.
- Full repo `go test ./... -count=1` green.
- Manual: dry-run on a real DO app via gocodealone-multisite is NOT required for this PR (deferred to a follow-up that exercises rollout-progress messaging in a live run).
