# NATS-as-Worker via nested `workers:[]` (revised after adversarial review)

**Status:** Approved 2026-05-02 (revised after adversarial-design-review surfaced that the original `kind: worker` discriminator design didn't fix the user's actual connectivity problem). User direction: "If you're having to specify http port, sounds like you're creating a web app rather than a worker (non-public service). Make sure we're creating the correct shape definition for each service being deployed... Investigate accordingly and fix as necessary. Proceed autonomously, brainstorm if you need to."

## Goal

Get NATS deploying as a DO App Platform Worker (non-HTTP, no fake HTTPPort, no HTTP readiness probe) AND keep the game server (`coredump-staging`) able to reach NATS via the existing `nats://coredump-nats-staging.internal:4222` URL the server expects.

## Background

`core-dump-nats` is currently declared as a top-level `infra.container_service` resource in `core-dump/infra.yaml`. The plugin always emits an `AppServiceSpec` with `HTTPPort: 8080` default, which DO probes for HTTP readiness. NATS speaks NATS-protocol on 4222, not HTTP on 8080 → readiness probe fails → deployment ERROR.

This design's first iteration proposed a `kind: worker` discriminator on the plugin's `infra.container_service` config. Adversarial review (cycle 1) surfaced three Critical findings:
1. `deriveImageFromAppSpec`, `Scale()`, and `Diff()` all hard-code Service-shape assumptions
2. The kind-discriminator approach would create core-dump-nats as a *separate* DO App from coredump-staging — DO's `.internal` DNS works only WITHIN an App, so `nats://coredump-nats-staging.internal:4222` would still fail to resolve
3. User intent is "fix the NATS+server connection," not "make the probe pass on a worker that can't be reached"

The plugin already supports the correct architectural shape: `buildAppSpec` (`internal/drivers/app_platform_buildspec.go:88`) calls `workersFromConfig(cfg)` for every container_service, populating `Spec.Workers` from a nested `workers:` array. **Zero plugin changes needed** — the fix is purely a `core-dump/infra.yaml` restructure.

## Approach

Move NATS from a top-level resource into a nested `workers:` array under `core-dump-app.config`. The result is a single DO App with `Spec.Services=[server], Spec.Workers=[nats]` — both components live in the same App's internal mesh, so `.internal:4222` DNS resolves correctly.

```yaml
# BEFORE (two separate Apps)
modules:
  - name: core-dump-nats
    type: infra.container_service
    config:
      provider: do-provider
      env_vars:
        NATS_AUTH_TOKEN: "${NATS_AUTH_TOKEN}"
    environments:
      staging:
        config:
          name: coredump-nats-staging
          image: "registry.digitalocean.com/coredump-registry/core-dump-nats:${IMAGE_SHA}"
          instance_count: 1
          region: nyc3
      prod:
        config:
          name: coredump-nats-prod
          ...

  - name: core-dump-app
    type: infra.container_service
    config:
      http_port: 8080
      provider: do-provider
      env_vars:
        ...
        NATS_AUTH_TOKEN: "${NATS_AUTH_TOKEN}"
    environments:
      staging:
        config:
          name: coredump-staging
          image: "registry.digitalocean.com/coredump-registry/core-dump-server:${IMAGE_SHA}"
          ...
          env_vars:
            NATS_URL: "nats://coredump-nats-staging.internal:4222"
            ...
```

```yaml
# AFTER (one App, server + worker components)
modules:
  - name: core-dump-app
    type: infra.container_service
    config:
      http_port: 8080
      provider: do-provider
      env_vars:
        ...
        NATS_AUTH_TOKEN: "${NATS_AUTH_TOKEN}"
    environments:
      staging:
        config:
          name: coredump-staging
          image: "registry.digitalocean.com/coredump-registry/core-dump-server:${IMAGE_SHA}"
          ...
          env_vars:
            NATS_URL: "nats://coredump-nats-staging.internal:4222"
            ...
          workers:
            # NATS_AUTH_TOKEN appears here AND on the parent service's env_vars
            # by design: the parent server uses the token as the CLIENT auth
            # credential to dial NATS; the worker uses the same token as the
            # SERVER-SIDE expected credential it requires from connecting
            # clients. They must match (single shared secret model).
            - name: coredump-nats-staging
              image: "registry.digitalocean.com/coredump-registry/core-dump-nats:${IMAGE_SHA}"
              instance_count: 1
              env_vars:
                NATS_AUTH_TOKEN: "${NATS_AUTH_TOKEN}"
      prod:
        config:
          name: coredump-prod
          ...
          workers:
            - name: coredump-nats-prod
              ...
  # core-dump-nats top-level module REMOVED
```

## Why this works (and why the kind-discriminator design didn't)

DO App Platform's component mesh: every component inside a single App can dial sibling components via `<component-name>.internal:<port>`. This DNS is **intra-App only**. Two separate Apps don't share the mesh — there is no cross-App `.internal` DNS.

The existing `core-dump-nats` resource is a separate App. The server's `NATS_URL` of `coredump-nats-staging.internal:4222` was never going to resolve, regardless of how the NATS workload was shaped (Service or Worker). The probe failure was the visible symptom; the connectivity gap was the deeper problem.

By collapsing NATS into the parent App as a Worker component, both problems vanish at once: NATS deploys without HTTPPort/probe, and the server's hardcoded URL resolves correctly via the App's internal mesh.

## Plugin behavior verification (no code change required)

`workersFromConfig` (`internal/drivers/app_platform_buildspec.go:608-625`) and `buildWorkerSpec` (lines 628-668) already construct `AppWorkerSpec` correctly when fed a nested `workers:` array. Fields supported: `name`, `image`, `run_command`, `build_command`, `dockerfile_path`, `source_dir`, `instance_size_slug`, `instance_count`, `env_vars`, `autoscaling`, `size`, `termination` (`grace_period_seconds` only).

NATS doesn't need any of the worker-only escape hatches (no autoscaling needed for a single-instance broker, no termination tuning); the plain config above suffices.

## State-drift impact

The state store currently has two App entries: one for `coredump-staging` (server) and one for `coredump-nats-staging` (NATS). After this PR merges + deploy.yml runs:

1. `wfctl infra plan` will diff config against state. The `core-dump-nats` module is gone from config → plan emits a `delete` action for the standalone `coredump-nats-staging` App.
2. The `core-dump-app` config now declares nested `workers: [coredump-nats-staging]` under each environment's config → plan emits an `update` action for `coredump-staging` adding the Worker component (the configHash on the parent module changes because the merged config now contains a `workers:` array).
3. Apply executes in `ComputePlan`'s sort order: **creates and updates first, deletes last** (`workflow/platform/differ.go:83`). So the server App is updated to include the Worker before the standalone NATS App is deleted. Both NATS instances briefly coexist during the DO deployment lag (a few minutes); the server can dial either via DNS during this window. There is no NATS unavailability gap at the model level — only the normal DO deployment latency.

**Pre-deploy state-key verification (REQUIRED):** before merging the PR, run `wfctl infra state list -c infra.yaml --env staging` and confirm the standalone NATS App is keyed as `coredump-nats-staging` (the env-resolved name), not `core-dump-nats` (the module name). If the state key is the module name (older state from before `ResolveForEnv` was added), `ComputePlan` will not match the removed module against the state entry, will emit no delete action, and both NATS instances will run forever. In that case, manually prune via `wfctl infra state delete coredump-nats-staging --env staging` before deploy.

If state drift surfaces (the standalone NATS App entry is stale or missing from cloud), `wfctl infra apply --refresh --auto-approve` (shipped in v0.20.5) prunes ghosts before applying.

## Build pipeline preservation (REQUIRED note)

The `core-dump-nats` entry in `ci.build.containers` (currently at `core-dump/infra.yaml:174-177`) MUST be retained even after the `core-dump-nats` **module** is removed from `modules:`. The `ci.build.containers` block is processed independently of `modules:` by `wfctl build` — it drives the NATS image build + push regardless of where the image is consumed. If a future cleanup PR removes the `ci.build.containers` entry thinking it's dead code, the nested Worker will fail to pull its image on next deploy.

Add a comment to infra.yaml directly above the `ci.build.containers` `core-dump-nats` entry: `# Builds the NATS image consumed by core-dump-app's nested workers[] — keep even though core-dump-nats is no longer a top-level module.`

## Out of scope

- Plugin-side `kind: worker` discriminator on `infra.container_service` — deferred. The nested-workers pattern is sufficient for the common case (worker co-located with a service). A standalone-worker resource type would be useful for future workloads (e.g., background processors that don't need a sibling service), but YAGNI until that need is concrete.
- Plugin's `Diff()` / `Scale()` / `deriveImageFromAppSpec` / `deriveExposeFromAppSpec` Worker handling — these have latent bugs (per adversarial review findings) but they don't manifest under the nested-workers shape because `Spec.Services` is non-empty (the server provides the Service). Filing as follow-up plugin issue.
- Multi-component scale: the current `Scale()` only iterates `Spec.Services` and updates Service.InstanceCount. NATS is a Worker so its `instance_count: 1` is set at apply-time only — `wfctl infra scale` won't change NATS replica count. For NATS this is correct (you don't scale a single-broker stage instance); for prod a separate decision applies.
- Migrating any other resource — only `core-dump-nats` is being collapsed.

## Cross-repo coordination

- One PR in core-dump: infra.yaml restructure. NO plugin or workflow changes.
- Post-merge: deploy.yml fires, plan diffs (delete NATS App + update server App), apply executes, server reaches /healthz once NATS Worker is running and the server can dial `coredump-nats-staging.internal:4222`.

## Acceptance criteria

- `wfctl infra plan` against the revised infra.yaml emits exactly two staging actions: delete `coredump-nats-staging` (standalone App) + update `coredump-staging` (add Worker).
- Apply executes both successfully. State store ends with one App entry for `coredump-staging` (containing the NATS Worker as a sub-component) and no entry for the standalone NATS App.
- The deployed `coredump-staging` App has `Spec.Services=[server-service-named-coredump-staging], Spec.Workers=[worker-named-coredump-nats-staging]` (verifiable via `doctl apps spec get` post-deploy or via the new wfctl drift output).
- The server starts cleanly and connects to NATS via `nats://coredump-nats-staging.internal:4222`.
- `/healthz` on `coredump-staging` returns 200.
- No HTTP readiness probe configured for the NATS Worker (DO supervises Workers via process exit status only).

## Assumptions

1. DO App Platform's intra-App `<component>.internal:<port>` DNS resolves between Service and Worker components in the same App. (Standard App Platform behavior; documented in DO's app-spec reference. If it doesn't work, separate fallback to public route + DNS would be needed.)
2. NATS, run as a Worker with `instance_count: 1`, will keep its process alive indefinitely. DO's Worker supervision restarts on exit; NATS is a stable long-running process in production. (Verified by NATS's documented operational profile.)
3. Coexistence window: during the deploy, the standalone NATS App and the new nested Worker briefly run side-by-side (update-runs-before-delete per `differ.go:83`). Component-name `coredump-nats-staging` is identical in both, but DO's `.internal` DNS is per-App scoped — components within the server App resolve `coredump-nats-staging.internal` to the in-App Worker, not the standalone App. No cross-App DNS collision.
4. The `core-dump-nats-staging` standalone App's deletion via wfctl Apply doesn't leave dangling DO state (e.g., a deployment in flight, a managed cert tied only to it). If found, manual cleanup via DO console pre-deploy.
5. The plugin's existing `workersFromConfig` correctly emits `Spec.Workers` even when the parent service has env_vars + sidecars + other Service-only fields set. (Verified by reading the source — `Workers: workersFromConfig(cfg)` is set independently of Services on the AppSpec.)
6. State store entries for the `core-dump-nats` module are keyed by the env-resolved name (`coredump-nats-staging`), not the module name (`core-dump-nats`). This is `ResolveForEnv`'s standard behavior (`workflow/config/module_resolve_env.go:64-67`). The pre-deploy verification step in §State-drift impact is the safety check.
7. `workers:` arrays at env-level config completely replace any base-level `workers:` arrays (`deepMergeMap` does not merge `[]any`). The proposed YAML places `workers:` only at env level, so this is consistent — but operators must NOT split the workers list between base and env config or entries will silently drop.
8. NATS_AUTH_TOKEN appears in both the parent service env_vars (server uses as client auth credential) AND the nested worker env_vars (NATS server uses as the server-side expected credential it requires from clients). Same value, two roles. Mismatched values would be silent NATS auth failure visible only in the NATS process logs.

## Rollback

Affects runtime (App Platform deployment shape change). Rollback procedure depends on environment:

**Staging (no users):**
1. Revert the core-dump infra.yaml change (re-add `core-dump-nats` as a top-level module, remove the nested `workers:` from `core-dump-app`).
2. Re-deploy. Plan will emit: create `coredump-nats-staging` (standalone App, Service-shape, will fail probe again) + update `coredump-staging` (remove Worker).
3. NATS goes back to deploying as a separate App with the readiness-probe failure.
4. Investigate why this fix didn't work; the rollback destination is the known-failure mode.

**Prod (zero-degradation rollback):**
The naive revert (above) returns prod to the broken Service-shape NATS, which is unacceptable for users.

For prod, before deploying:
1. Snapshot current state: `wfctl infra state list -c infra.yaml --env prod -o json > /tmp/prod-state-pre-cutover.json`
2. Confirm both `coredump-nats-prod` and `coredump-prod` entries are present.
3. If post-deploy validation fails, the rollback is NOT to the original infra.yaml. Instead:
   a. Hold the deploy in the failed state (don't re-deploy yet).
   b. Manually verify whether the Worker is running by inspecting the server App's deployment via DO console / `wfctl infra status -c infra.yaml --env prod`.
   c. If the Worker is running but the connectivity is broken, the issue is likely server-side config (NATS_URL pointing somewhere unexpected) — fix forward, don't roll back.
   d. If the Worker is NOT running and the standalone NATS App is also gone, manually re-create the standalone NATS App via `doctl apps create` from the snapshot, while debugging the Worker shape. This is the panic-button path; it's manual but recoverable.
4. Investigate root cause separately; do not auto-roll-back.

The change is BC at the plugin level (no plugin code changed). Rollback for staging is purely a core-dump-side revert; rollback for prod is a manual recovery procedure.

## Follow-ups (filed separately, not blocking)

- **Plugin issue**: Add `kind: worker` discriminator on `infra.container_service` for future standalone-Worker use cases. Per adversarial review's Important findings, also fix `Diff()` to detect kind changes, `Scale()` to iterate Workers, `deriveImageFromAppSpec` to fall back to Workers[0].Image, and `deriveExposeFromAppSpec` to handle Worker apps. Bundle these together as a v0.9.0 minor release when the use case becomes concrete.
- **Plugin issue**: Document the nested-workers pattern explicitly in plugin README — operators need to know that "worker co-located with service" is the canonical pattern, not "worker as separate container_service."

## System Impact

- **State store**: One App entry deleted (`coredump-nats-staging`), one App entry updated (`coredump-staging` gains Worker sub-component). Net state shrinkage of one resource.
- **Plugin contract**: No change — the existing `workersFromConfig` path is exercised. Nothing new to test on the plugin side.
- **CLI**: No new commands. Existing `wfctl infra plan/apply/drift/refresh` work unchanged.
- **Production safety**: NATS unavailability window during the App-delete + App-update phase. Mitigated for staging (no users). For prod cutover, operators schedule a maintenance window.
- **All other System Impact Matrix categories** (auth, anti-cheat, malware, sandbox, network, filesystem, process/OS, social, NPC, factions, economy, IoT, media, legal, forensics, VERA, achievements, client desktop, terminal, world history, content, telemetry): None — purely an infra config restructure.
