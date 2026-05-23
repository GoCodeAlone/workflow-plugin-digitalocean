# True blue/green validation for DO App Platform — design

Issue: GoCodeAlone/workflow-plugin-digitalocean#159
Scope: B — audit + tests + in-rollout availability probe + green-spec safety fix
Date: 2026-05-23 (revised after adversarial cycle 1)

## Audit findings

### What `AppBlueGreenDriver` actually does (verbatim from `internal/drivers/deploy.go:296-400`)

1. `CreateGreen(image)` — calls `cloneAppSpec(blueApp.Spec)` (full JSON round-trip, `app_platform_migration_repair.go:563`), renames to `<blueName>-green`, sets the new image, calls `Apps.Create`. Records the green app's ID + `LiveURL` (default `*.ondigitalocean.app`). **Bug surfaced by review:** the deep-copy preserves `Spec.Domains`. DO App Platform's Create API rejects (or worse, races TLS reissuance) when a second app claims an already-attached non-default custom domain.
2. `HealthCheck` — while `stableCheck=false`, delegates to the green driver; once `SwitchTraffic` flips `stableCheck=true`, delegates to blue.
3. `SwitchTraffic` — reads green's current image, calls `d.blueDriver().Update(image)`. This updates **blue's spec** with the green image, which DO App Platform reconciles as a rolling in-place deploy of blue. Blue's custom domain stays attached the whole time. Green is never promoted to serve the custom domain.
4. `DestroyBlue` — deletes the green clone (misleading method name; the blue app is never destroyed).
5. `GreenEndpoint` — returns the green clone's `*.ondigitalocean.app` URL.

`AppCanaryDriver.CreateCanary` (`deploy.go:469`) clones `stableApp.Spec` the same way and has the same Domains-inheritance bug.

### What `AppDeployDriver.HealthCheck` already gates on (v2.0.15)

1. `waitingForDeployment` (set by `Update`) → blocks until target deployment reaches `godo.DeploymentPhase_Active`.
2. After Active → `appPlatformCustomDomainReadinessError` runs:
   - Every non-default custom domain on the App spec must have `Phase == PHASE_Active` in `LiveDomains`.
   - Every non-wildcard custom domain must return 2xx/3xx on `GET https://<domain><readiness path>` within 5s.

That gate is **post-Active only**. Nothing probes availability while the rolling replace is in flight — the exact window the issue calls out ("rollout-time availability probes, not only post-rollout smoke checks").

## Proposed changes

### 1. Code fix — strip custom domains + ingress from cloned spec in BG + Canary

`CreateGreen` and `CreateCanary` are the only call sites of `cloneAppSpec` that hand the result to `Apps.Create`. The other call sites (`Update` in deploy.go:54, the migration-repair helpers) hand to `Apps.Update` on the **same** app, where Domains pass-through is desired. Add `sanitizeClonedSpecForCreate(spec)` invoked from CreateGreen + CreateCanary that:

- Clears `spec.Domains` (the legacy field) and per-domain entries in `spec.Ingress.Rules` that reference custom domains.
- Resets `spec.Name` and `spec.Region` to caller-supplied values (already done outside the helper).
- Leaves Services / Workers / Jobs / Functions / Static Sites otherwise intact (image swap happens after).

This is a small, surgical fix; the alternative — deferring to a follow-up — leaves the BG/Canary drivers footgun-laden while we ship "honest documentation" claiming they're safe. Better to fix the foot.

### 2. In-rollout availability probe — fire during `waitingForDeployment`

Today `HealthCheck` returns early on non-Active phase, so no domain probe runs while DO is rolling. Add an **availability probe** that runs each `HealthCheck` call during `waitingForDeployment`:

- For each non-wildcard custom domain on the App spec, `GET https://<domain><readiness path>` with the same 5s timeout the post-Active probe already uses.
- Failures are **not** elevated to errors — they're appended to the existing in-progress error message as `; domain probe: X/Y custom domains reachable` so wfctl --wait surfaces rollout-time downtime windows in operator logs without changing the gate semantics. (The gate continues to be "deployment Phase = Active + post-Active domain probe.")
- When no custom domains are attached (typical of green clones after §1 sanitization) the in-rollout probe is a no-op.

Implementation: extract the existing probe loop into `appPlatformProbeCustomDomains(ctx, app, probe, path) (reachable int, total int, lastErr error)` and call it from both the post-Active path (where lastErr already becomes the returned error) and the waitingForDeployment path (where the reachable/total is suffixed to the in-progress message).

### 3. Progress signal in deployment-in-progress error

Today `deploymentHealthError` returns:

```
app deploy: "myapp" deployment in progress: DEPLOYING (a1b2-...)
```

Surface `godo.Deployment.Progress` data:

```
app deploy: "myapp" deployment in progress: DEPLOYING (a1b2-...) [3/5 steps; updated 12s ago]
```

`updated Ns ago` is derived from `Deployment.UpdatedAt` (stateless on our side — no counter, no instance-lifetime assumption). The stuck-deploy heuristic from the prior design is dropped (per adversarial D1 + Important finding on per-call instance reconstruction).

Implementation: `deploymentProgressString(dep *godo.Deployment) string` — returns `""` when Progress is nil (early DO phases), `"[N/M steps; updated Xs ago]"` otherwise. Appended to the existing message; **the original error prefix and parenthesized deployment ID are preserved verbatim** so any downstream substring matchers still match.

### 4. Tests pinning the actual semantics

In `internal/drivers/deploy_test.go`:

- `TestAppBlueGreenDriver_CreateGreen_StripsCustomDomainsFromGreenSpec` — seed blue with a non-default `Spec.Domains` entry; assert the `AppCreateRequest.Spec.Domains` passed to `client.Create` is empty after §1.
- `TestAppCanaryDriver_CreateCanary_StripsCustomDomainsFromCanarySpec` — same for canary.
- `TestAppBlueGreenDriver_SwitchTraffic_UpdatesBlueSpec` — assert post-SwitchTraffic, blue's spec image equals the green image (in-place update, not promotion).
- `TestAppBlueGreenDriver_GreenURL_IsDefaultIngress_NotCustomDomain` — assert `GreenEndpoint()` returns the green clone's `*.ondigitalocean.app` URL, not any custom domain attached to blue.
- `TestAppBlueGreenDriver_DestroyBlue_DeletesGreenNotBlue` — explicit assertion about the misleading method name's actual behavior.
- `TestAppBlueGreenDriver_HealthCheck_PostSwitchProbesBlueDeployment` — after SwitchTraffic, HealthCheck must probe blue's new in-progress deployment (existing plumbing; domain-probe assertion split out for clarity).
- `TestAppDeployDriver_HealthCheck_DuringRolloutProbesDomain` — seed blue with a custom domain, set Phase=DEPLOYING, inject a probe that records all calls; assert each HealthCheck call during rollout invokes the probe and surfaces reachable/total in the error message.
- `TestDeploymentProgressString_NilProgressIsEmpty` — explicit nil-Progress safety.
- `TestDeploymentProgressString_UpdatedAtAgeFormatting` — recent UpdatedAt → "Xs ago".

Constructor surface change (required to make the probe-injection tests work): add `NewAppBlueGreenDriverWithDomainProbe(client, regClient, region, blueID, blueName, probe)` and `NewAppCanaryDriverWithDomainProbe(...)`, each wiring the probe into the inner `*AppDeployDriver` constructions via a new package-private setter. The existing `NewAppBlueGreenDriver` + `NewAppBlueGreenDriverWithRegistry` continue to work unchanged (probe is nil → falls back to `defaultAppPlatformDomainProbe`, same as today).

### 5. Documentation — `docs/DEPLOYMENT_STRATEGIES.md` (new)

A single page documenting what each strategy actually does on DO App Platform:

- `AppDeployDriver` — in-place rolling via DO App Platform's own RollingUpdate. Rollout health = deployment phase Active + custom-domain HTTPS readiness. **No availability guarantee if `InstanceCount < 2`** — DO's single-instance services restart by stop-then-start; the in-rollout probe will surface the resulting downtime window in logs.
- `AppBlueGreenDriver` — **prevalidated rolling**, not true traffic-switching. Green is created as a separate App (with custom domains stripped, see §1) for pre-flight image validation; SwitchTraffic discards the green and updates blue in place. Same availability properties as `AppDeployDriver`. Linked to filed follow-up for true DO LB+Droplets front-door.
- `AppCanaryDriver` — `RoutePercent` not supported on App Platform; CreateCanary is otherwise identical safety-wise to CreateGreen after §1.

Linked from `README.md`.

### 6. Follow-ups filed (out of scope for this PR)

- True B/G via DO LB+Droplets or external front-door — separate design.
- Rename `AppBlueGreenDriver` → `AppPrevalidatedRollingDriver` — requires workflow-engine interface rename; coordinated cycle.

## Assumptions

A1. `godo.Deployment.Progress` is populated for in-progress deployments past the very-early phases. The helper returns `""` when nil — tested explicitly per §4.
A2. DO App Platform's own rolling deploy is the actual zero-downtime mechanism **only for `InstanceCount >= 2`**. For single-instance services, DO performs stop-then-start; the in-rollout availability probe (§2) will surface the resulting downtime window in operator logs. This is documented as a non-guarantee in `DEPLOYMENT_STRATEGIES.md`.
A3. The `module.BlueGreenDriver` interface from workflow-engine cannot be renamed in this PR; the misleading "blue/green" name is documented instead and a follow-up is filed.
A4. wfctl `infra apply --wait` polls `HealthCheck` on a backoff. The in-rollout availability probe runs once per poll — no new polling cadence is introduced.
A5. Downstream consumers (wfctl) that pattern-match the in-progress error string match on the prefix `"deployment in progress: "` (substring, not anchored). The original prefix + parenthesized deployment ID are preserved verbatim by §3; only the bracketed suffix is new. **Mitigation if A5 is wrong:** the new fragment is appended *after* the existing parenthesized ID, so any consumer regex anchored on the parens-block continues to match.
A6. `cloneAppSpec` is used by both same-app-Update paths (where Domains pass-through is correct) and cross-app-Create paths (where it is the bug). The §1 helper is invoked *only* at the Create call sites; other call sites are unchanged.

## Rollback

- §1 (Domains-stripping in CreateGreen/CreateCanary) is the only behavior change. It is purely additive — strictly fewer fields populated in the cloned spec — and revertible by a single `git revert`. If, against documented DO behavior, a future build of DO App Platform begins to accept duplicate custom-domain claims on Create, removing the strip is a one-line change with no migration. No state, no plugin contract, no flag.
- §2 + §3 are error-message enrichments + probe-loop extraction; pure additive. Revert is `git revert`.
- §4 + §5 are tests + docs; revert is trivial.
- New constructors `NewAppBlueGreenDriverWithDomainProbe` / `NewAppCanaryDriverWithDomainProbe` are additive surface — existing constructors keep their signatures so no downstream rebuild is needed.

## Test plan

- Unit tests added per §4.
- `go test ./internal/drivers/... -count=1 -race` green.
- Full repo `go test ./... -count=1` green.
- Manual live-validation against gocodealone-multisite is **not** required for this PR — the changes are observation-only (probe, log enrichment) + a strictly-safer spec sanitization. Live validation of true zero-downtime guarantees belongs to the deferred front-door design.

## Adversarial cycle 1 — addressed

- C1 (Domains inherited into green-clone spec): **addressed in scope** — §1 Domains-stripping fix + §4 tests for both BG and Canary.
- C2 (no in-rollout availability probe): **addressed** — §2 fires the probe during `waitingForDeployment`, surfaces reachable/total in the in-progress error.
- I1/I2 (counter state across HealthCheck instance lifetime + StepsCompleted-as-stall-proxy): **addressed** — counter dropped, stateless `Deployment.UpdatedAt`-age formatting (§3) instead.
- I3 (unimplementable probe-injection test): **addressed** — new constructors `NewAppBlueGreenDriverWithDomainProbe` / `NewAppCanaryDriverWithDomainProbe` (§4).
- I4 (wire-format compat for error string): **addressed** — original prefix + parenthesized deployment ID preserved verbatim; new fragment appended after (§3 + A5).
- I5 (single-instance availability assumption): **addressed** — A2 calls it out + `DEPLOYMENT_STRATEGIES.md` (§5) documents it as a non-guarantee.

Minor findings (M1/M2/M3/M4/M5) accepted without further action per "avoid nitpicking" mandate.
