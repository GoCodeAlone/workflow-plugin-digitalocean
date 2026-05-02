# `infra.container_service` kind discriminator — design

**Status:** Approved 2026-05-02 (user direction: "If you're having to specify http port, sounds like you're creating a web app rather than a worker (non-public service). Make sure we're creating the correct shape definition for each service being deployed... Investigate accordingly and fix as necessary. Proceed autonomously, brainstorm if you need to.").

## Goal

Allow `infra.container_service` configs to deploy as DigitalOcean App Platform Worker components (no HTTPPort, no HTTP probes) when the workload is non-HTTP (NATS, message queues, background processors). Currently the plugin always emits an `AppServiceSpec`, forcing every workload into the HTTP-Service shape with a default `HTTPPort: 8080` that DO's readiness probe targets — failing immediately for non-HTTP workloads.

## Background

`internal/drivers/app_platform_buildspec.go::buildAppSpec` constructs a top-level `*godo.AppServiceSpec` plus appended sidecars (also Services). The resulting `AppSpec` has `Services=[svc]`. `Workers/Jobs/StaticSites=nil` for the top-level component — those nested arrays only populate from `cfg["workers"]/["jobs"]/["static_sites"]` sub-keys.

DO App Platform supports five component types (per godo `apps.gen.go`):
- **Service** — long-running HTTP server. Requires `HTTPPort`. DO probes HTTP path/port for readiness + liveness.
- **Worker** — long-running non-HTTP process. No `HTTPPort`. DO supervises only via process exit status (no probe spec on `AppWorkerSpec`).
- **Job** — runs to completion (one-shot or scheduled). Has `Kind` for `pre_deploy/post_deploy/failed_deploy/scheduled`.
- **StaticSite** — static file server. Out of scope here.
- **Functions** — serverless. Out of scope here.

Real-world symptom (core-dump deploy run [25263238663](https://github.com/GoCodeAlone/core-dump/actions/runs/25263238663/job/74073925869)): NATS container starts cleanly (`[INF] Listening for client connections on 0.0.0.0:4222 — Server is ready`), but `Readiness probe failed: dial tcp 100.127.22.161:8080: connect: connection refused` for 13 attempts → deployment ERROR. NATS speaks NATS-protocol on 4222, not HTTP on 8080.

## Approaches considered

### (A) Single resource type with `kind:` discriminator (recommended)

`infra.container_service` gains a `kind:` config field. Values: `service` (default = current behavior, full BC), `worker`. When `kind: worker`, `buildAppSpec` dispatches to a worker-shape builder that emits `Spec.Workers=[w]` instead of `Spec.Services=[svc]`. HTTP-only fields are rejected with explicit build-time errors.

**Trade-off:** smallest API surface; one resource = one component; matches K8s/Helm `kind:` convention. The type name `container_service` becomes mildly misleading when `kind: worker` is set, but operators are familiar with discriminator patterns.

### (B) Separate resource type `infra.container_worker`

New driver registration. Per-type schema validation. Pros: type name accurate, no forbidden-field tables. Cons: schema duplication for shared fields (Image, Envs, Autoscaling, Termination), 2× align/security-check rule surface, more registry entries to maintain.

### (C) Status quo — force nested workers via parent service

Operators wrap NATS as a `worker:` array entry inside another `infra.container_service`. The parent must still be a Service with a fake HTTPPort. Inverts ownership; worst mental model.

**Pick:** (A). Discriminator is industry-standard, BC-clean, simplest.

## Design

### Section 1 — Config field

Add `kind:` to `infra.container_service` config:

```yaml
- name: my-nats
  type: infra.container_service
  config:
    kind: worker             # NEW; default "service" preserves current behavior
    provider: do-provider
    env_vars:
      NATS_AUTH_TOKEN: "${NATS_AUTH_TOKEN}"
  environments:
    staging:
      config:
        name: my-nats-staging
        image: "registry.digitalocean.com/my-registry/nats:${IMAGE_SHA}"
        instance_count: 1
        region: nyc3
```

Allowed values: `service` (default), `worker`. `kind: job` is deferred to a follow-up issue (YAGNI for the immediate NATS unblock).

### Section 2 — buildAppSpec dispatch

```go
func buildAppSpec(name string, cfg map[string]any, region string) (*godo.AppSpec, error) {
    kind := strFromConfig(cfg, "kind", "service")
    switch kind {
    case "service", "":
        return buildServiceAppSpec(name, cfg, region)  // refactored existing path
    case "worker":
        return buildWorkerAppSpec(name, cfg, region)   // new
    default:
        return nil, fmt.Errorf("infra.container_service kind %q not supported (allowed: service, worker)", kind)
    }
}
```

`buildServiceAppSpec` is the existing buildAppSpec body, renamed. `buildWorkerAppSpec` is new.

### Section 3 — buildWorkerAppSpec behavior

```go
func buildWorkerAppSpec(name string, cfg map[string]any, region string) (*godo.AppSpec, error) {
    if err := rejectServiceOnlyFieldsForWorker(cfg); err != nil {
        return nil, err
    }
    imgSpec, err := imageSpecFromConfig(cfg)
    if err != nil { return nil, fmt.Errorf("worker image config: %w", err) }
    instanceCount, _ := intFromConfig(cfg, "instance_count", 1)

    w := &godo.AppWorkerSpec{
        Name:             name,
        Image:            imgSpec,
        InstanceCount:    int64(instanceCount),
        Envs:             envVarsFromConfig(cfg),
        BuildCommand:     strFromConfig(cfg, "build_command", ""),
        RunCommand:       strFromConfig(cfg, "run_command", ""),
        DockerfilePath:   strFromConfig(cfg, "dockerfile_path", ""),
        SourceDir:        strFromConfig(cfg, "source_dir", ""),
        InstanceSizeSlug: instanceSizeSlugFromConfig(cfg),
        Autoscaling:      autoscalingFromConfig(cfg),
        Termination:      workerTerminationFromConfig(cfg),
        LogDestinations:  logDestinationsFromConfig(cfg),
        Alerts:           componentAlertsFromConfig(cfg),
    }

    spec := &godo.AppSpec{
        Name:    name,
        Region:  region,
        Workers: []*godo.AppWorkerSpec{w},
        Jobs:    jobsFromConfig(cfg),       // nested jobs still allowed
        Domains: domainsFromConfig(cfg),    // app-level config
        Vpc:     vpcFromConfig(cfg),
        Alerts:  appAlertsFromConfig(cfg),
        // (no top-level Services; no Ingress, Egress, Maintenance, etc. that are HTTP-only)
    }
    return spec, nil
}
```

`workerTerminationFromConfig` is new (Workers' termination uses only `grace_period_seconds`, no `drain_seconds`).

### Section 4 — Forbidden-fields rejection

`rejectServiceOnlyFieldsForWorker` returns an error if any of these are set with `kind: worker`:

- `http_port`
- `routes`
- `health_check`
- `liveness_check`
- `cors`
- `ingress`
- `expose` (the public/internal flag is also Service-only)
- `protocol`
- `internal_ports`
- `sidecars`
- `static_sites` (top-level wouldn't make sense alongside a worker)

Error format: `kind=worker does not support field %q; remove it or change kind to service`. One field at a time (fail fast on the first encountered) keeps the error message focused.

Why ERROR not WARN: silent drops let misconfigurations linger and create deployment surprises later. The user's mental model when setting `kind: worker` is "no HTTP" — any HTTP field is a contradiction.

### Section 5 — align rule

New rule `R-A10`: when `kind: worker` is set, flag any of the forbidden fields with `WARN` (not `--strict`-level FAIL). The build-time error in Section 4 is the strict gate; the align rule helps operators catch issues at plan-time before they reach Apply.

Existing rules (R-A1 through R-A9) are unaffected since they don't make Service-vs-Worker assumptions.

### Section 6 — Health check + Troubleshoot for Workers

Workers don't have HTTP probes; DO supervises via process exit status. The existing `appHealthResult` (v0.8.3) returns Healthy=true when `app.ActiveDeployment.Phase == DeploymentPhase_Active`. For a Worker app, ActiveDeployment is populated normally when the worker process is running — no special handling needed.

Troubleshoot's existing `attachDeployLogs` already enumerates `dep.Workers` (post-v0.8.4) for component names — already correct.

**Verification needed during implementation:** confirm via test that `appHealthResult` returns Healthy=true for an app whose ActiveDeployment has `Phase: Active` and `Spec.Services=nil, Spec.Workers=[w]`. (Existing tests use Service-only fixtures.)

### Section 7 — Validation against core-dump-nats

After PR-G1 ships + plugin v0.8.6 released + core-dump bumps lockfile + adds `kind: worker` to `core-dump-nats` config:

```yaml
- name: core-dump-nats
  type: infra.container_service
  config:
    kind: worker
    provider: do-provider
    env_vars:
      NATS_AUTH_TOKEN: "${NATS_AUTH_TOKEN}"
  environments:
    staging:
      config:
        name: coredump-nats-staging
        image: "..."
        instance_count: 1
        region: nyc3
```

Expected deploy outcome: NATS deploys as Worker; no HTTP readiness probe; DO marks the deployment Active when the process is running. Server-side `coredump-staging` connects via `nats://coredump-nats-staging.internal:4222` (the existing internal-DNS pattern continues to work for cross-component traffic within an App Platform app, even when one component is a Worker).

**Note:** `coredump-nats` and `coredump-staging` are currently separate Apps (each has its own `infra.container_service` resource → its own AppSpec → its own DO App). DO's `<service>.internal:<port>` DNS works **within** an App, not across Apps. If cross-App `.internal` DNS is not supported, the design needs to either (a) merge them into one App via the existing nested-arrays pattern, or (b) use a public route — but this is an existing concern not introduced by the kind discriminator. Worth verifying during validation.

## Out of scope

- `kind: job` — deferred to follow-up. NATS unblock doesn't need it.
- `kind: static_site` / `kind: function` — out of scope; static sites are content-deploy not container-service shaped.
- Restructuring core-dump's two-app architecture into one App with multiple components — separate refactor decision.
- Migration of existing nested `workers:` array config to top-level `kind: worker` — both patterns continue to be supported.
- Cross-resource component composition (one App with one Service + N Workers via separate resources) — would require resource-set semantics that don't fit the one-resource-one-Cloud-resource IaC contract.

## Cross-repo coordination

- PR-G1 in workflow-plugin-digitalocean: add kind discriminator + worker dispatch + tests + R-A10 align rule + CHANGELOG. Tag plugin v0.8.6.
- PR-G2 in core-dump: bump lockfile to v0.8.6 + add `kind: worker` to core-dump-nats config.
- Validate core-dump deploy → NATS as Worker → no HTTP probe failure → server reaches /healthz.

## Acceptance criteria

- `wfctl infra plan` against a config with `kind: worker` succeeds and emits a plan whose Apply would create an App with `Spec.Workers=[w], Spec.Services=nil`.
- `wfctl infra align --strict` flags forbidden fields with `kind: worker` (WARN-level for align; ERROR-level at apply-time).
- Apply succeeds end-to-end against DO API with no HTTPPort field set in the worker.
- Existing `kind: service` (default) configs deploy unchanged. Full BC.
- core-dump-nats deploys as Worker; deployment reaches Active phase without HTTP probe failure.

## Assumptions

1. DO App Platform `Worker` components do NOT require any probe spec; DO supervises via process exit status. (Verified against `godo.AppWorkerSpec` struct — no HealthCheck or LivenessCheck field exists.)
2. The existing `workersFromConfig` + `buildWorkerSpec` (lines 608-668 of `app_platform_buildspec.go`) helpers don't have hidden Service-context dependencies. Reusing them as a base for `buildWorkerAppSpec`'s field mapping. (Verified by reading the source — they take a `map[string]any` and return a `*godo.AppWorkerSpec` with no other side effects.)
3. `appHealthResult` (v0.8.3) handles Worker-only apps correctly. ActiveDeployment.Phase logic doesn't key on Service component existence. **Needs implementation-time test to confirm.**
4. `wfctl infra align` rules don't make Service-vs-Worker assumptions. Adding R-A10 doesn't break existing rules. (Verified via grep — existing rules look at presence/absence of fields, not at component shape.)
5. DO `<component>.internal:<port>` DNS works for Worker components within an App. (Standard App Platform behavior; if not, separate fix needed for core-dump's two-app split — not in scope for this design.)
6. Workers don't use Sidecars (DO model: each component is independent; sidecars are a Service-spec-only sub-array). Confirmed via godo struct.

## Rollback

Affects runtime (plugin loading paths, build configuration). Rollback procedure:

1. Revert core-dump's `kind: worker` setting on `core-dump-nats` config (returns to default `kind: service` behavior).
2. Bump core-dump's `.wfctl-lock.yaml` workflow-plugin-digitalocean entry from `v0.8.6` back to `v0.8.5`.
3. Re-deploy. Pre-PR-G1 behavior restored — NATS deploys as Service with HTTPPort 8080 (and continues to fail readiness probe, but is back to the known-failure mode the user has visibility into via Troubleshoot).
4. (Optional) Open follow-up issue on workflow-plugin-digitalocean to investigate the rollback cause.

The plugin code change itself is BC at v0.8.6 — existing `kind: service` configs unaffected. So rollback only requires reverting the core-dump-side change unless a regression in `kind: service` behavior surfaces (unlikely given the BC discipline).

## System Impact

- **State store:** No change. Worker apps stored same as Service apps under the same resource type entry.
- **Plugin contract:** Backwards-compatible. Default `kind: service` preserves all existing behavior. New `kind: worker` is opt-in.
- **CLI:** No new commands. Existing `wfctl infra plan/apply/drift/refresh` work unchanged.
- **Validation rules:** New R-A10 (additive, WARN-level for `kind: worker` + Service-only fields).
- **Production safety:** New build-time errors prevent silent misconfigurations. Existing dry-run/auto-approve semantics unchanged.
- **Other System Impact Matrix categories** (auth, anti-cheat, malware, sandbox, network, filesystem, process/OS, social, NPC, factions, economy, IoT, media, legal, forensics, VERA, achievements, client desktop, terminal, world history, content, telemetry): None — purely a wfctl/plugin/IaC config-shape change.
