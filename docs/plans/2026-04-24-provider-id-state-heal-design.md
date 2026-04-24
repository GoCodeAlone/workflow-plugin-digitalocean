# DO Plugin — ProviderID State-Heal + Integration Test Coverage

**Status:** Approved (autonomous pipeline, 2026-04-24)

**Target release:** workflow-plugin-digitalocean v0.7.8 (bundle into in-flight PR #22)

**Related tasks:** #67 (this work), #58 (v0.7.7 empty-ID guards — prior attempt), #25 (BMW staging deploy unblock), #64 / #72 (wfctl observability).

---

## Problem

BMW staging deploy has failed twice tonight with the same error:

```
update/bmw-staging: PUT https://api.digitalocean.com/v2/apps/bmw-staging: 400 invalid uuid
```

DO's App Platform API requires a UUID in the URL path (`/v2/apps/{uuid}`). BMW's IaC state at `s3://bmw-iac-state/staging/state.json` has `ProviderID = "bmw-staging"` (the resource name) instead of the real UUID (`f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5`). DO rejects the PUT.

v0.7.7 shipped as the fix for this bug class ("empty-ID guards + capture UUID from API response"), but the bug recurred — meaning either (a) v0.7.7 doesn't actually prevent the UUID→name substitution in all code paths, or (b) the state that's been failing was populated by an earlier version and never healed.

User mandate: proper fix, with the test that would have caught this. No teardown. No hand-edit of state files.

---

## What's verified

1. **wfctl is a passthrough for `ProviderID`.** `cmd/wfctl/infra_apply.go:281-299` reads `result.Resources[i].ProviderID` directly into `ResourceState.ProviderID` — if the driver returns the correct UUID, state gets the UUID. So the bug lives in the driver or its call chain, not wfctl.
2. **`AppPlatformDriver.Create` writes `ProviderID: app.ID`** (line 403 via `appOutput(app)`). On the success path, the UUID flows correctly.
3. **State-heal is already drafted** on feat/v0.7.8-troubleshoot (uncommitted): `resolveProviderID(ctx, ref)` + `isUUIDLike` + wired into Update/Delete. The draft matches the design below.
4. **Other DO drivers are vulnerable** to the same class of bug where UUID-format providers are concerned (api_gateway, database, cache, certificate, droplet, load_balancer, vpc, firewall, reserved_ip). DNS and spaces use non-UUID identifiers so are not affected.

---

## Approach

Three layers of defense, each addressable independently, bundled together for v0.7.8:

### Layer 1 — Root-cause audit of `v0.7.7` empty-ID guard

The v0.7.7 "empty-ID guard" (commit `94c9227`) is suspect. Audit it:

- Does any path in `Create` fall back to `ref.Name` when `app.ID == ""`? If yes, that's wrong — it should error out loudly so state never silently persists a name-as-UUID.
- Is there a gRPC-boundary issue where `ResourceOutput.ProviderID` is getting dropped in marshaling, causing wfctl to reuse the previous state's (broken) ProviderID?
- Was the BMW-staging state originally populated pre-v0.7.7, and the v0.7.7 fix only prevents NEW-creation drift but doesn't heal EXISTING drift?

**Deliverable:** a brief root-cause write-up in the PR body naming the specific code path that produced BMW's bad state. Fix it if it still exists (e.g., replace silent fallback with `return nil, fmt.Errorf(...)` at the offending site).

### Layer 2 — State-heal in the driver, per-driver

Each DO driver using UUID-format identifiers validates `ref.ProviderID` shape before hitting the API. On mismatch, fall back to the driver's existing name-based lookup (`findAppByName` / `findDatabaseByName` / etc.) to resolve the real UUID, use it for the API call, and return `ResourceOutput.ProviderID` containing the healed UUID so wfctl rewrites state transparently.

Shape check is per-driver because DO's identifier formats differ:
- App Platform, Database, Cache, Certificate, Droplet, Load Balancer, VPC, Firewall, Reserved IP → UUID
- DNS Domain → domain name (no heal needed; the name IS the ID)
- Spaces → bucket name (same)

A shared `isUUIDLike(s string) bool` helper in `internal/drivers/shared.go` (or equivalent) removes per-driver duplication. Drivers that need heal call it; drivers that don't skip it.

**For v0.7.8 (tonight's scope):** implement state-heal on `AppPlatformDriver` only — that's the unblocker for BMW. Ship.

**For v0.7.9 (follow-up, separate task):** audit + replicate heal across the remaining UUID drivers. Each needs its own `findByName` prerequisite (most have it per task #21 v0.7.3).

### Layer 3 — Integration-test harness

The test infrastructure that would have caught v0.7.7's regression. New file `internal/drivers/app_platform_integration_test.go` plus supporting scaffolding in `internal/drivers/testhelpers_test.go`:

**Harness components:**
- `fakeAppsClient` — embeds `godo.AppsService` interface, tracks calls (which method, with which arguments), returns configurable responses with real UUIDs
- `inMemoryState` — implementing enough of workflow's state-store surface to round-trip `ResourceState` writes and reads
- `apply(driver, spec)` helper that mimics what wfctl's `infra_apply.go` does: call `Create`/`Update`, take returned `ResourceOutput`, persist to in-memory state, return the state snapshot

**Tests:**

| Test | Seed state | Action | Assertion |
|---|---|---|---|
| `TestAppPlatform_Create_PersistsUUIDInState` | empty | Create with mock returning `App{ID:"uuid-abc", Name:"bmw-staging"}` | state has `ProviderID="uuid-abc"` |
| `TestAppPlatform_Update_UsesExistingUUID` | `ProviderID="uuid-abc"` | Update | fakeClient saw `Update(ctx, "uuid-abc", ...)`; no `findByName` call |
| `TestAppPlatform_Update_HealsStaleName` | `ProviderID="bmw-staging"` (stale name) | Update | fakeClient saw `findAppByName` → Update with real UUID; returned `ResourceOutput.ProviderID` is the UUID; warn log captured |
| `TestAppPlatform_Delete_HealsStaleName` | `ProviderID="bmw-staging"` | Delete | Same as Update but for Delete |
| `TestIsUUIDLike_TableDriven` | n/a | pure function | canonical UUIDs pass; names, empty, too-short, missing-hyphens all fail |

**Why these tests pin down the regression:**
- The "happy Create → state has UUID" test fails loudly if anyone ever adds a silent name-fallback in Create. That's the test v0.7.7 didn't have.
- The heal tests cover the defense-in-depth path that matters when state is already corrupt.

### Layer 4 (optional, out of scope) — wfctl-core generic heal hook

Considered and deferred. Reasoning:

**For:** a generic `driver.ValidateProviderID(id) bool` + wfctl calling it before every Update/Delete with Read-by-name fallback means all drivers across all providers benefit uniformly. Less per-driver boilerplate.

**Against:** `ValidateProviderID` shape varies (UUIDs for DO, ARNs for AWS, project/location/resource strings for GCP) — the contract essentially devolves to "let the driver decide." At which point pushing the heal into the driver (where it already has the name-lookup logic) is simpler and more honest. The generic hook buys only marginal deduplication and constrains future providers who may have their own ID conventions.

Verdict: skip the generic hook. Each driver owns its heal.

---

## Data flow (post-v0.7.8, BMW-like recovery)

```
wfctl infra apply --env staging
  → planResourcesForEnv → Plan: 1 action(s): update bmw-staging
  → provider.Apply(plan)
    → AppPlatformDriver.Update(ctx, ref{Name:"bmw-staging", ProviderID:"bmw-staging"}, spec)
      → resolveProviderID(ctx, ref)
        → isUUIDLike("bmw-staging") → false
        → log.Printf("warn: app platform \"bmw-staging\": ProviderID \"bmw-staging\" is not UUID-like; resolving by name (state-heal)")
        → findAppByName(ctx, "bmw-staging") → App{ID:"f8b6200c-...", ...}
        → return "f8b6200c-..."
      → client.Update(ctx, "f8b6200c-...", AppUpdateRequest{Spec:...}) → App{ID:"f8b6200c-...", ...}
      → client.CreateDeployment(ctx, "f8b6200c-...", ...)
      → return ResourceOutput{ProviderID:"f8b6200c-...", ...}
  → wfctl persists ResourceState{ProviderID:"f8b6200c-..."} (healed)
  → Deploy continues: pre_deploy migrations run, app becomes ACTIVE
```

Same flow applies to `Delete` on stale state; user never sees the "invalid uuid" error again.

---

## Rollout

**v0.7.8 (this PR #22):**
1. Root-cause audit write-up in PR description.
2. Shared `isUUIDLike` helper in `internal/drivers/shared.go`.
3. `AppPlatformDriver.resolveProviderID` wired into Update, Delete (already drafted, commit it).
4. Integration-test harness + 5 tests from the table above.
5. CHANGELOG v0.7.8 entry describing the heal, the root-cause finding, and the integration-test harness.
6. Continue to bundle Troubleshoot work from earlier in PR #22.

**v0.7.9 (follow-up task, filed post-merge):**
- Audit + replicate heal across: database, cache, certificate, droplet, load_balancer, vpc, firewall, reserved_ip, api_gateway.
- One commit per driver, same pattern.
- Integration tests for each (parameterize the existing harness).

**BMW consumer bump:**
- Single PR: setup-wfctl v0.18.9 → v0.18.10.1 (after v0.18.10.1 tags) + workflow-plugin-digitalocean v0.7.7 → v0.7.8.
- Retries deploy. State-heal kicks in on the stale `ProviderID="bmw-staging"` during the infra apply step, state gets rewritten with the real UUID, deploy proceeds.

---

## Success criteria

- `internal/drivers/app_platform_integration_test.go` exercises the bug that produced BMW's failure and passes (TDD — write the stale-name test FIRST, watch it fail against main, then apply the heal patch).
- `v0.7.8` merges and tags.
- BMW deploy retry on v0.7.8 against the current (broken-state) staging env completes successfully: `wfctl infra apply` heals state, pre-deploy migration runs, app reaches ACTIVE, /healthz returns 200, auto-promote to prod fires on /healthz green.
- No future regression in the "happy Create produces UUID-state" path — golden test pins it.

---

## Non-goals

- wfctl-core generic `ValidateProviderID` hook (deferred, may never be needed).
- Other DO drivers' heal (v0.7.9).
- AWS / GCP / Azure equivalents (each provider audits + fixes its own drivers in its own repo).
- `wfctl infra heal` command for ad-hoc recovery (nice-to-have, not needed once driver-level heal exists).
- Retroactive state-file repair tool (not needed — first Update on stale state heals it transparently).
