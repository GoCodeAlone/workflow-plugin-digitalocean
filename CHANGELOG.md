# Changelog

All notable changes to workflow-plugin-digitalocean are documented here.

## v0.12.0 — 2026-05-08

- **feat(drivers/app_platform)**: image-presence pre-flight for DOCR-typed images. `Diff`, `Create`, and `Update` now call `verifyImagePresentInDOCR(ctx, regClient, imageRef)` before mutating the AppSpec. On absence, returns wrapping `interfaces.ErrImageNotInRegistry`. Conservative behavior: any DOCR API error (rate-limit, 5xx, parse failure) returns nil so the underlying `apps.update` surfaces the real issue.
- **chore(deps)**: bump workflow to v0.24.0; bump godo to v1.189.0.

## [Unreleased]

### Fixed

- **App Platform route drift detection** — `infra.container_service.routes` is
  now recorded in outputs and compared during `Diff`, so adding or clearing
  public ingress routes on an existing app triggers an in-place App Platform
  update instead of silently no-oping.

### Added

- **`DropletDriver.Replace` implements `interfaces.ResourceReplacer`** (workflow v0.23.0+). Replaces use detach-before-create orchestration: read old → DetachByDropletID per attached Block Storage Volume → wait for each detach action completion (60s/2s bounds) → delete old Droplet → create new Droplet with the resolved volume IDs (bypassing the name-resolution race in `resolveDropletVolumes`). Fixes the "422 storage already associated with another droplet" failure class when replacing Droplets with attached Volumes.

- **`ActionsClient` interface** for the godo Actions service subset (Get only). Enables testing of action wait-polling without live API.

- **`waitForActionComplete` helper** — bounded async-action polling with immediate context-cancellation propagation, "errored" status detection, and configurable timeout/poll-interval bounds (parameterized for testability; 60s/2s production defaults).

- **`createWithResolvedVolumes` unexported helper** — shared Create/Replace request builder. When `resolvedIDs` is non-nil (Replace path), volume IDs are used verbatim, bypassing name-lookup. When nil (Create path), existing `resolveDropletVolumes` runs unchanged.

### Changed

- **`DropletDriver` constructors now wire `c.StorageActions` + `c.Actions`** into the driver automatically. `NewDropletDriverWithClient` gains a variadic optional-clients parameter (type-switched) preserving existing test call sites.

- **`StorageActionsClient` now includes `DetachByDropletID`** alongside `Resize`. The godo `StorageActionsService` implements both; one interface covers VolumeDriver (resize) and DropletDriver.Replace (detach).

- **Replace operations on Droplets with attached Block Storage Volumes no longer fail with `422 storage already associated`**. Recovery sequence after partial Replace failure is: retry `wfctl infra apply --refresh-outputs` to sync state from cloud truth before the next plan; without refresh, persisted state may still claim the Volume is attached.

### Fixed

- **VPCDriver / DropletDriver / AppPlatformDriver `Diff` now compares `region`** (#70) — previously only `VolumeDriver.Diff` checked region drift; the other three drivers silently no-op'd on region changes despite DO having no in-place region update on those resources. Region change now correctly emits `FieldChange{ForceNew: true}` and sets `NeedsReplace=true`. Surfaced by core-dump's TC2 cutover plan-shape assertion which expected a 5-resource cascade replace but got 1 (Volume only); without the assertion gate the partial cutover would have left VPC + Droplet in nyc3 while the App + Volume moved to nyc1, producing a half-migrated state requiring manual cleanup. Includes upgrade-safe guard: `vpcOutput` and `dropletOutput` already populated `region`, and this PR adds `region` to `appOutput.Outputs` so AppPlatformDriver.Diff has a current-side value to compare; the new check then skips when `current.Outputs["region"]` is empty so state from earlier plugin versions doesn't false-positive — the next Read populates it without spurious drift. Regression tests cover both the change-detection and empty-current-skip paths for all three drivers.

## [v0.10.0]

### Added

- **`iacProvider.computePlanVersion: v2` opt-in** (PR P-DO TP4) — wfctl's
  runtime dispatcher now routes Apply through `wfctlhelpers.ApplyPlan`
  instead of the legacy in-provider switch. The new dispatch path adds:
  Replace decomposition + `ReplaceIDMap` propagation, JIT
  `${MODULE.id}` / `${VAR}` substitution, the input-drift postcondition,
  per-action context cancellation between iterations, and the
  `interfaces.UpsertSupporter` recovery contract.

  Backward compat: wfctl < v0.21.0 ignores the new field; the legacy
  v1 dispatch (`provider.Apply` switch, now wrapping
  `wfctlhelpers.ApplyPlan`) continues to work for all existing callers.

- **`ValidatePlan` (`interfaces.ProviderValidator`)** (PR P-DO TP3) —
  read-only, no-remote-call cross-resource constraint check that runs
  at `wfctl infra align` time before any cloud API call. First pass
  covers three constraint families:
  1. App Platform `infra.container_service` requires a region GROUP
     slug (`nyc`, `ams`, `fra`, `sfo`, `sgp`, `syd`, `tor`, `blr`,
     `lon`); zone slugs (`nyc1`, `sfo3`, …) rejected with
     `PlanDiagnosticError`.
  2. Zone-bound resources (`infra.vpc`, `infra.droplet`, `infra.volume`)
     require a zone slug; bare group slugs rejected with
     `PlanDiagnosticError`.
  3. Cross-resource: App Platform `vpc_ref` must reference a VPC whose
     region zone belongs to the App Platform's region group; database
     `vpc_ref` must resolve to an in-plan VPC. Locks the recurring
     "App Platform in nyc cannot reach VPC in sfo3" production bug
     class (root-cause issue D from the conformance design).

  Severity mapping: Error always fails align; Warning fails only under
  `--strict`; Info never affects exit. ValidatePlan is read-only and
  makes no remote calls per the W-4 contract.

- **Conformance test (`provider_conformance_test.go`)** (PR P-DO TP5)
  — invokes `iac/conformance.Run` against a freshly-constructed
  `DOProvider`. Behind the `conformance` build tag; opt in with
  `go test -tags=conformance ./internal/...`. Six non-cloud scenarios
  run by default; `CONFORMANCE_LIVE_CLOUD=1` (with
  `DIGITALOCEAN_ACCESS_TOKEN`) opts into the cloud-touching probes.

- **`.github/workflows/codemod-report.yml`** (PR P-DO TP1) — per-PR
  workflow runs `iac-codemod refactor-apply -dry-run` against the
  plugin source, uploads the full Markdown report as a 90-day retention
  GitHub Actions artifact, and posts/updates a sticky PR comment with
  the top-30-line summary so drive-by reviewers see findings without
  downloading the artifact.

### Changed

- **`DOProvider.Apply` collapsed to wrap `wfctlhelpers.ApplyPlan`**
  (PR P-DO TP2) — the in-Apply per-action switch
  (create/update/replace/delete + upsert recovery + nil-out diagnostic)
  is replaced with a single dispatch to the helper. The DO-plugin-specific
  deferred-update flush (DatabaseDriver `type=app` `trusted_sources`
  referencing apps created later in the plan; regression-gated by
  `provider_deferred_test.go`) is preserved by wrapping `ApplyPlan` with
  the second-pass loop. The wrapper deviates from the codemod's canonical
  single-statement shape; the deviation is documented and marked with
  `// wfctl:skip-iac-codemod` so `AssertApplyDelegatesToHelper`
  recognises it as intentional.

- **DO drivers `AppPlatformDriver`, `VPCDriver`, `FirewallDriver`,
  `DatabaseDriver`** structurally satisfy the canonical
  `interfaces.UpsertSupporter` (their existing `SupportsUpsert() bool`
  method is bit-identical to the new interface; no driver-side changes
  needed).

- **`DOProvider.Apply` `delete` action with `Current == nil`** — under
  v2 dispatch the contract is "driver is the authority on what an empty
  ProviderID means" (per `wfctlhelpers/apply.go::doUpdate`'s analogous
  comment). The v1-era pre-flight precondition error is retired;
  `provider_apply_test.go::TestDOProvider_Apply_DeleteAction_MissingCurrent`
  was rewritten to lock the new contract.

### Fixed

- **`infra.vpc` exposes `id` output** — wfctl `infra_output: <vpc>.id`
  secrets need an `id` field in the VPC outputs map. Previously the VPC
  UUID lived only on `ProviderID` (metadata), not on Outputs, so
  downstream `vpc_ref` / `vpc_uuid` references failed with `field "id"
  not found in outputs of module`. Mirrors `ProviderID` to
  `Outputs["id"]` so the standard `<vpc>.id` reference works. Surfaced
  by core-dump deploy run 25278900082.

### Bumped

- `github.com/GoCodeAlone/workflow` → `e2c582bece90` (workflow main HEAD
  with W-7 conformance suite + W-8 codemod merged).

## [v0.9.1]

### Fixed

- **`IacProviderConfig` accepts `provider` field** — wfctl bootstrap uses
  `provider: digitalocean` as the discriminator to identify which
  iac.provider module owns a given backend (e.g. `backend: spaces`).
  v0.9.0's strict-contracts proto (PR #41) didn't declare `provider`,
  so configs carrying the canonical discriminator (BMW, core-dump, etc.)
  failed typed-config marshal with `protobuf error` (wfctl unwraps to
  the bottom-most error, hiding the field name). Added `string provider
  = 5` to the proto; the field is accepted but ignored by the plugin
  itself (manifest.iacProvider.name = digitalocean is the source of
  truth for provider identity). Surfaced by core-dump's first deploy
  attempt against v0.9.0.

## [v0.9.0]

### Added

- **`infra.volume` driver** — DigitalOcean Block Storage. Create / Read /
  Update (in-place resize via `StorageActions.Resize` for size growth) /
  Delete / Diff (size shrinks, region changes, and filesystem_type changes
  force replace) / HealthCheck (Read succeeds → healthy; godo `Volume`
  exposes no Status field, so a successful API round-trip is the strongest
  available signal).

  Config keys: `name` (from `spec.Name`), `region` (defaults to provider
  region), `size_gb` (required, > 0), `filesystem_type` (`ext4` / `xfs`;
  default empty = raw block device), `description`, `tags`.

  Outputs: `id` (UUID), `name`, `region`, `size_gb`, `filesystem_type`.
  ProviderIDFormat = `IDFormatUUID`.

- **Droplet driver — extended config** for self-hosted services that need
  more than the bare-minimum size/image/region. New optional keys, all
  additive and defaulted-empty (no behaviour change for existing configs):

  - `user_data` (string) — cloud-init payload
  - `vpc_uuid` (string) — VPC the Droplet joins
  - `ssh_keys` ([]string | []int | mixed) — fingerprint strings OR numeric
    SSH-key IDs; element type is detected at runtime (structpb-safe; floats
    must be whole numbers)
  - `tags` ([]string)
  - `enable_backups` (bool) — maps to `Backups`
  - `monitoring` (bool)
  - `ipv6` (bool)
  - `volumes` ([]string of Block Storage volume **names**) — names are
    resolved to IDs at create time via `Storage.ListVolumes(name=,region=)`;
    a name that doesn't resolve in the Droplet's region returns
    `droplet volumes: volume %q not found`

  Droplet outputs gain `private_ip` (`droplet.PrivateIPv4()`) so downstream
  services in the same VPC can be wired directly.

- **Troubleshoot fetches DO deploy/build logs** (PR-E2) — `AppPlatformDriver.Troubleshoot`
  now calls `godo.AppsService.GetLogs` for each component in any deployment
  whose phase is `Error`, `Canceled`, or `Superseded`. The DO API returns
  presigned historic URLs; the plugin HTTP-fetches the most recent URL and
  appends the last 200 lines per component to `Diagnostic.Detail` as a
  delimited block:

  ```
  ---
  Deploy logs — component "web" (last 200 lines):
  <log content>
  ---
  ```

  For build-phase failures (a `SummaryStep` named `"build"` with status
  `Error`), `AppLogTypeBuild` is requested; all other failures use
  `AppLogTypeDeploy`. Multi-component apps produce one block per component
  (Services, Jobs, Workers, Functions enumerated from `dep.Spec`); if `Spec`
  is nil or all arrays are empty, a single aggregate fetch is performed
  (component `""` = DO API aggregate).

  Graceful degradation: `GetLogs` error or HTTP-fetch non-200 are logged to
  stderr; the `Diagnostic` is still produced without the log block. A 10 MB
  cap prevents pathological log responses.

- **`appHealthResult` ListDeployments fallback** (PR-E2) — When all three
  deployment slots (`Active`, `InProgress`, `Pending`) are nil, `HealthCheck`
  now falls back to `ListDeployments(Page:1, PerPage:1)` and surfaces the
  latest deployment's short ID + phase: `"latest deployment f8b6200c: ERROR"`.
  This catches the fast-fail case where DO removes a failed deployment from
  all three slots before the operator's poll loop catches up (reproducer: build
  error in ≤1 second). If `ListDeployments` errors or returns empty, falls
  through to the previous `"no deployment found"` message.

- **`appHealthResult` phase-transition handling** (PR #49) — When
  `ActiveDeployment.Phase` is non-Active (transitioning toward Active or
  failed), the function now returns the appropriate in-progress/failed message
  rather than falling through to `"no deployment found"`. Fixes the polling
  loop timeout described in GoCodeAlone/workflow-plugin-digitalocean#48.

- **`DOProvider.DetectDrift`** — real implementation classifying resources as
  `DriftClassGhost` (cloud reports 404), `DriftClassInSync` (cloud Read
  succeeds), or `DriftClassUnknown` (driver lookup fails). Required for
  `wfctl infra apply --refresh` ghost-prune (workflow v0.20.5+).

  - `DriftClassGhost` (`Drifted: true`): state has the resource, but cloud
    `Read` returns `interfaces.ErrResourceNotFound`. Caller should prune state
    via `wfctl infra apply --refresh`.
  - `DriftClassInSync` (`Drifted: false`): cloud Read succeeds — state and
    cloud agree.
  - `DriftClassUnknown` (`Drifted: true`): driver registry lookup failed for
    the ref type (unsupported resource type). Operator must investigate.

  Production-safety invariant: transient API errors (rate-limit, auth,
  network) propagate and do NOT trigger state-prune semantics; only genuine
  `interfaces.ErrResourceNotFound` (HTTP 404) gates the ghost path.

  go.mod bumped to workflow v0.20.5 for `interfaces.DriftClass*` constants.

### Fixed

- **gRPC Diagnostic.Detail field omitted from plugin response** — `internal/module_instance.go::invokeDriverTroubleshoot` serialised `Diagnostic` to `map[string]any` but omitted the `detail` field. wfctl's `remoteResourceDriver.Troubleshoot` reads `m["detail"]` → empty string → `emitDiagnostics` never printed the Detail body. All work done in `attachDeployLogs` (v0.8.3 + v0.8.4) to populate `Diagnostic.Detail` with log content / failure notes was silently dropped at the gRPC boundary. Added `"detail": d.Detail` to the serialisation map. This is the structpb-boundary class of bug previously documented in workspace memory.

- **Troubleshoot deploy/build log fetch** — Fixed two issues that prevented log blocks from appearing in operator-facing Diagnostic output:
  - `deploymentComponents` now reads component names from `dep.Services / .StaticSites / .Workers / .Jobs / .Functions` (deployment-level arrays populated by both ListDeployments and GetDeployment) before falling back to `dep.Spec.*` and finally `[""]` aggregate. Previously only Spec was inspected, which is nil from ListDeployments, so the empty-aggregate fallback was always hit and DO API returned no logs.
  - GetLogs API errors, HTTP-fetch errors, empty HistoricURLs, and empty-body responses now append a brief failure note to `Diagnostic.Detail` (in addition to the existing stderr log, which is captured at hashicorp/go-plugin TRACE level and not surfaced to operators). Operators now see the failure mode in the same Troubleshoot block as the rest of the diagnostic output.

- **DetectDrift Config-drift detection**: out of scope for this release. The
  IaCProvider interface signature receives only refs, not the parsed declared
  config, so per-driver Diff comparisons cannot be performed safely (VPC reads
  `ip_range` from spec.Config; AppPlatform `canonicalExpose` defaults to
  `"public"` on an empty spec — any app with `expose: internal` would report
  false drift back to `"public"`). Use `wfctl infra plan` for full config-drift
  detection — it has access to the spec and surfaces config drift as update
  actions.

- **`drivers.ErrResourceNotFound` aliased to `interfaces.ErrResourceNotFound`**
  — The local sentinel in `app_platform.go` was previously a distinct
  `errors.New(...)` value; cross-package `errors.Is(err, interfaces.ErrResourceNotFound)`
  would silently miss it. Now aliased so all driver list-scan not-found returns
  satisfy the canonical sentinel for the ghost-detection path.

- **Deferred `trusted_sources` update for `infra.database`** — When a
  `trusted_sources` entry of `type: app` references an app that does not yet
  exist (first-deploy ordering), `DatabaseDriver.Create` and `.Update` no
  longer fail with a resource-not-found error. Instead, the driver:

  1. Creates/updates the DB with the resolvable subset of rules (e.g. `ip_addr`)
     applied immediately.
  2. Queues a deferred firewall update for the post-apply second pass.
  3. After all plan `create` actions complete, `DOProvider.Apply` calls
     `FlushDeferredUpdates` on any driver with pending updates, which
     re-resolves all `type=app` names (apps now exist) and calls
     `UpdateFirewallRules` with the full intended rule set.

  Failure during `FlushDeferredUpdates` (e.g. `UpdateFirewallRules` API error)
  is **fatal** — the error surfaces in `ApplyResult.Errors` so the operator
  knows the intended security posture was not fully applied. Failed entries are
  **retained in the pending queue**, so a subsequent `wfctl apply` automatically
  re-attempts the flush without requiring operator intervention (e.g. touching a
  config field to force a Diff update).

  Only "app not yet created" (name absent from `Apps.List`) triggers deferral.
  API-level failures (rate-limit, transient, auth errors) are propagated
  immediately and never silently deferred.

  Fixes GoCodeAlone/core-dump#154 (R4 first-deploy ordering finding).
  See `docs/plans/2026-05-02-staging-deploy-blockers-design.md` (Blocker 2).

- **Apply `"delete"` action** — The `Apply` method now dispatches `"delete"`
  plan actions to `d.Delete(ctx, ref)` using `action.Current` for the
  `ResourceRef` (which carries the `ProviderID`; `action.Resource` is empty for
  deletes). Previously, `"delete"` fell through to the `default` case and
  returned `unknown action "delete"`, blocking any plan that removed a resource
  from config. Reproducer: BMW deploy failed with `delete/bmw-staging-firewall:
  unknown action "delete"` after the firewall was removed from infra.yaml.
- **Apply nil-output guard** — After a successful `"delete"` action, `out`
  is `nil` (deleted resources have no post-apply state). Added a nil check
  before `result.Resources = append(result.Resources, *out)` to prevent a
  nil-pointer panic on mixed delete+create plans.

## [v0.8.0] - 2026-04-28

P-2 staging IaC alignment. Three independent canonical-config additions
(F4 / F5 / F7) plus the surrounding Plan/Diff cascade fixes flagged
during quality review. Bumps `minEngineVersion` to `0.20.1` so plugin
tests + alignment integration can reference align/security-check
fixtures from workflow v0.20.1.

### Added

- **`expose: internal` on `infra.container_service`** (P-2.F4) — App Platform
  services may now declare `expose: internal` to opt out of the public edge
  route. When set, `buildAppSpec` zeroes `HTTPPort`, folds `http_port` into
  `InternalPorts` (so siblings can dial it), and drops `Routes` entirely. The
  service becomes reachable only from sibling components in the same app via
  DO App Platform's internal DNS (`<service-name>.internal:<port>`). Default
  remains `expose: public`.

  Misconfiguration guards (all reject at apply time, before any DO API
  call):
  - `expose` must be a string. Non-string values (accidental YAML bool
    `true`, numbers, maps, etc.) return `expose: must be a string (one of
    [public, internal]), got <type>` rather than silently defaulting to
    public.
  - `expose` must be one of `[public, internal]`. Typos like `intenral` or
    unsupported values like `private` return
    `expose: %q invalid; must be one of [public, internal]`.
  - `expose: internal` requires at least one of `http_port` or
    `internal_ports` to be set. Setting `expose: internal` with no ports
    would produce a service with no listening port — silently unreachable.
    Returns `expose: internal requires http_port or internal_ports to be
    set`.
  - Under `expose: internal`, every port (`http_port` and each entry in
    `internal_ports`) must be in the valid TCP range [1, 65535]. Returns
    `http_port: %d invalid (must be between 1 and 65535)` or
    `internal_ports: %d invalid (must be between 1 and 65535)`. This
    closes the `http_port: 0` landmine where a previous "is the key set"
    check would silently append 0 to InternalPorts → unreachable spec.
  - `http_port` and `internal_ports` set with disjoint values returns
    `internal_ports must include http_port when both are set; use one or
    the other`.

  Plan/Diff: `appOutput` now records `Outputs["expose"]` derived from the
  live `AppSpec` (HTTPPort==0 with InternalPorts populated → "internal";
  otherwise "public") AND `Outputs["image"]` formatted from the first
  service's `ImageSourceSpec` via the new `formatImageSpec` reverse of
  `ParseImageRef`. `Diff` compares `expose` against the canonical desired
  value AND compares `image` structurally
  (RegistryType+Registry+Repository+Tag, with parse-then-compare via
  `imageRefsEqual`) — so in-place public↔internal toggles produce a Plan
  action with a `FieldChange{Path: "expose"}`, GHCR/DockerHub
  registry-org changes are detected, and unchanged image refs no longer
  emit spurious `image` `FieldChange`s on every reconcile. Pre-F4 state
  without recorded `expose` is treated as `public` for the comparison.

  This unblocks core-dump P-1's NATS sidecar and any other backing-service
  component that must not face the open internet.
- **Firewall `droplet_ids` + `tags` target keys (P-2.F7)** — `infra.firewall`
  specs now plumb the canonical `droplet_ids` (list of Droplet IDs) and
  `tags` (list of Droplet/DOKS-pool tag strings) keys into
  `godo.FirewallRequest.DropletIDs` / `Tags`. Tag-based attachment lets
  future Droplets and DOKS pools auto-join the firewall when they receive
  the matching tag.
- **`http_port_protocol` canonical key + `protocol: grpc` alias (P-2.F5)** —
  App Platform services now honor an explicit `http_port_protocol` config key
  that maps to `godo.AppServiceSpec.Protocol` (godo v1.178.0 `apps.gen.go:568`).
  The historic `protocol` shorthand still works and gains a `grpc` alias that
  resolves to `HTTP2` (gRPC requires HTTP/2 with prior knowledge per DO docs).
  When both keys are set, `http_port_protocol` takes precedence.

### Changed

- **Firewall specs without targets now fail at apply time (P-2.F7)** —
  `FirewallDriver.Create` and `FirewallDriver.Update` reject specs that
  declare neither `droplet_ids` nor `tags` BEFORE any DO API call, with
  the error:
  `firewall %q has no targets (specify droplet_ids or tags) — App Platform
  services cannot be firewall-protected; use expose: internal or
  trusted_sources`. The validation runs at the start of every Apply
  reconcile (Create + Update). DO firewalls do **not** attach to App
  Platform apps; for App-Platform-only deployments, omit `infra.firewall`
  and use `expose: internal` services plus `trusted_sources` on managed
  databases.

### Fixed

- **`FirewallDriver.Diff` detects in-place target/rule changes (P-2.F7)** —
  pre-F7 `Diff` was a stub that returned `NeedsUpdate=false` for every
  non-nil current state, silently suppressing in-place toggles of
  `droplet_ids`, `tags`, `inbound_rules`, or `outbound_rules` from `wfctl
  infra plan` output. F7 round 2 extends `Diff` to compare those four
  canonical fields against state recorded by `fwOutput`, surfacing a Plan
  action when any diverges. Set semantics for `droplet_ids` and `tags`
  (reorder is not a change); order-sensitive deep-equal for rules. Pre-F7
  state without recorded fields is treated as having empty fields, so the
  first plan post-upgrade safely over-detects and re-asserts current
  configuration.
- **Empty / non-positive target entries now fail at apply time (P-2.F7)** —
  `tagsFromConfig` filters empty strings and `dropletIDsFromConfig` filters
  IDs ≤ 0. Without these filters, a spec like `tags: [""]` or
  `droplet_ids: [0]` would slip past `validateFirewallTargets` (slice is
  non-empty after parsing) and fail only at the DO API call.
- **Fractional `droplet_ids` rejected, not truncated (P-2.F7)** —
  `dropletIDsFromConfig` now returns an error when a numeric value is not
  integer-valued. Pre-fix, a YAML `droplet_ids: [123.9]` silently truncated
  to `123`, attaching the wrong Droplet.
- **Outputs are gRPC structpb-compatible (P-2.F7)** — `fwOutput` stores
  `droplet_ids` / `tags` / `inbound_rules` / `outbound_rules` in the
  canonical `[]any` shape (numerics as `float64`, rules as
  `[]any`-of-`map[string]any`). The wfctl→plugin gRPC dispatch path
  encodes Outputs through `structpb.NewStruct`, which rejects native typed
  slices (`[]int`, `[]string`, struct slices). Storing canonical-from-the-
  start ensures `Diff` reads symmetric values whether `current.Outputs`
  is consumed in-process or after a structpb round-trip.

### Notes

- **Plugin dispatch mode: legacy compat.** This release ships under
  workflow's legacy plugin dispatch (untyped `map[string]any` arguments
  transported via `structpb`). The F7 firewall Diff fix uses
  canonical-shape Outputs (typed values converted to structpb-compatible
  `[]any` of primitive/map shapes) as a stopgap to survive the gRPC
  structpb boundary. Migration to workflow's strict typed-contract
  dispatch (`v0.19.0-alpha.5+`) is scoped for v0.9.0. Once strict-mode
  is enabled, the canonical-shape conversion becomes obsolete and
  Outputs writers + Diff readers will use generated proto types
  end-to-end.
- **Filed follow-up:** issue #37 — use `ref.Name` consistently for
  `FirewallDriver.Update` error formatting (Minor advisory from F7
  review; non-blocking, scoped for v0.8.x).

## [v0.7.9] - 2026-04-24

### Added

- **`ProviderIDValidator` on all 14 drivers** — every driver now implements
  `interfaces.ProviderIDValidator` (workflow v0.18.11) by returning a
  `ProviderIDFormat` declaration. wfctl uses the declaration to validate IDs
  at two boundaries: input (warn-only on Update/Delete) and output (hard
  failure on Apply to prevent state corruption).

  Format assignments:
  - `IDFormatUUID` — AppPlatform, APIGateway, Cache, Certificate, Database,
    Firewall, Kubernetes, LoadBalancer, VPC
  - `IDFormatDomainName` — DNS
  - `IDFormatFreeform` — Droplet, Spaces, Registry, IAMRole

  **Droplet deviation from plan table**: the v0.7.9 design doc listed Droplet
  as `IDFormatUUID`. Droplet IDs are actually integers assigned by the DO API
  (e.g. `"123456789"`), not canonical UUIDs. Corrected to `IDFormatFreeform`;
  `providerIDToInt` performs strict local validation (via `strconv.Atoi`) and
  returns an explicit error for any non-integer ProviderID before any API call
  is made — no UUID-based state-heal is needed for Droplet.

- **State-heal replication across all UUID drivers** (Tasks 11) — the
  `resolveProviderID` → `findXxxByName` pattern introduced for `AppPlatformDriver`
  in v0.7.8 is now replicated to every remaining UUID-shaped driver:

  | Driver | List expansion |
  |--------|---------------|
  | VPC | — (List already in interface) |
  | Firewall | — (List already in interface) |
  | Database | — (List already in interface) |
  | Certificate | — (List already in interface) |
  | Cache | `CacheClient.List` added |
  | APIGateway | `APIGatewayAppClient.List` added |
  | LoadBalancer | `LoadBalancerClient.List` added (value slice `[]godo.LoadBalancer`) |
  | Kubernetes | `KubernetesClient.List` added |

  Each driver gained `findXxxByName` (paginated list → name match) and
  `resolveProviderID` (UUID check → pass-through or WARN log + heal).
  Update/Delete on all 8 drivers now route through `resolveProviderID`.

- **Per-driver state-heal tests** — 5 tests per UUID driver (40 total) in
  `*_stateheal_test.go` files (package `drivers`):
  - `Create_PersistsUUIDInState` — ProviderID comes from API, not spec name
  - `Update_UsesExistingUUID` — no `List` call when ProviderID is valid UUID
  - `Update_HealsStaleName` — `List` fires, Update called with healed UUID
  - `Update_HealFails_WhenListFails` — error propagated when the List API call fails, no silent fallback
  - `Delete_HealsStaleName` — Delete called with healed UUID

- **`TestAllDrivers_DeclareProviderIDFormat`** — manually maintained registry in
  `providerid_format_test.go`; one entry per driver, fails if any listed driver
  returns the wrong format or the method is missing. New drivers must be added
  to this table manually.

### Changed

- Depends on workflow v0.18.11.

---

## [v0.7.8] - 2026-04-24

### Added

- `AppPlatformDriver.Troubleshoot` implements `interfaces.Troubleshooter` from workflow
  v0.18.10. On deploy health-check failure wfctl automatically fetches the app's
  in-progress/pending/active deployment slots (prioritised in that order) plus up to
  5 recent historical deployments, synthesises `[]Diagnostic` entries with per-phase
  root-cause lines extracted from `Progress.SummarySteps` and `Progress.Steps`, and
  surfaces them in CI output — no DO console trip required to diagnose failures.
- `pickTroubleshootDeployments` helper: priority-ordered candidate selection with dedup.
- `buildDiagnosticFor` helper: structured Diagnostic extraction per deployment.
- `extractCause` helper: scans log tail / reason messages for common error patterns
  (`Error:`, `exit status`, `panic:`, `fatal:`, `failed to`, …) with last-line fallback.
- `ResourceDriver.Troubleshoot` gRPC dispatch in plugin `InvokeMethod`; returns
  `codes.Unimplemented` for drivers that don't implement `Troubleshooter` so wfctl
  silently no-ops without error.

### Fixed

- **State-heal for stale name-as-ProviderID** — `AppPlatformDriver.Update` and `Delete`
  now call `resolveProviderID` before hitting the DO API. When `ref.ProviderID` is not a
  canonical UUID (36 chars, hyphens at positions 8/13/18/23), the driver logs a WARN and
  transparently falls back to `findAppByName` to recover the real UUID. The healed UUID is
  returned in `ResourceOutput.ProviderID` so wfctl rewrites state on the next Apply — no
  manual teardown or state editing required.

  Root-cause: a pre-v0.7.7 code path in `DOProvider.Apply` substituted `spec.Name` as
  `ProviderID` when the godo API returned a zero-ID response. v0.7.7 added an empty-ID
  guard on the Create path but did not heal existing stale state. v0.7.8 heals it at
  Update/Delete time. Triggered by BMW staging deploy `24901939350` where
  `state.json` contained `ProviderID="bmw-staging"` instead of the app UUID.

- New shared helper `isUUIDLike(s string) bool` in `internal/drivers/shared.go` — used
  by `resolveProviderID`; 11-case table-driven unit test in `shared_test.go`.
- A WARN log (`"state-heal"` keyword) is emitted when heal fires so operators can observe
  state drift in CI output without the deploy failing.
- New integration-test harness in `internal/drivers/integration_test_helpers_test.go`:
  `fakeAppsClient` (full `AppPlatformClient` stub with per-method call tracking),
  `inMemoryState` (minimal state round-trip store), and `applySim` (mimics wfctl's
  Apply→persist loop). Five integration tests in `app_platform_integration_test.go`
  exercise the full Create → state persist → Update flow including:
  - UUID stored (not spec name) after Create
  - No heal for valid UUID on Update
  - Stale name healed on Update (core BMW regression test)
  - Stale name healed on Delete
  - Clear error when heal can't resolve the name

### Changed

- Depends on workflow v0.18.10.1 (was v0.18.6).
- `AppPlatformDriver.Troubleshoot`: empty `ProviderID` now returns `(nil, nil)` instead
  of an error; `ListDeployments` errors are best-effort (swallowed, slot-based data used).
- Test ProviderIDs updated from `"app-123"` to proper UUID format throughout driver tests
  (required because `"app-123"` is not UUID-like and would trigger the heal path).

### Known follow-up (v0.7.9)

- Replicate state-heal (`resolveProviderID` equivalent) across the other UUID-based
  drivers (`vpc`, `firewall`, `database`, `cache`, `load_balancer`, `certificate`,
  `api_gateway`, `kubernetes`, `droplet`) — the same class of stale state is theoretically
  possible for any driver that was deployed before v0.7.7's empty-ID guard.

## [v0.7.7] - 2026-04-24

### Fixed

- **UUID capture on Create (all UUID-based drivers)** — Added a nil/empty-ID guard to the `Create` path of all ten UUID-based resource drivers: `app_platform`, `vpc`, `firewall`, `database`, `cache`, `load_balancer`, `certificate`, `api_gateway`, `kubernetes`, and `droplet`. Previously, if the godo API returned a nil object or an object with an empty ID after a successful HTTP create, the driver would silently propagate an empty string `ProviderID` into state. On the next `wfctl infra apply`, the `Update` call would send that empty/wrong value as the UUID path parameter, causing a DO API `400 invalid uuid` rejection. The guard returns a clear error instead, preventing corrupted state. Two tests per driver verify the guard (`_EmptyIDFromAPI`) and that the happy path stores the real UUID (`_ProviderIDIsUUID`).
- **`BootstrapStateBackend` test injection** — Added `bootstrapClientFactory` field to `DOProvider` so integration tests can inject a fake S3 client without patching globals.
- **`invokeProviderBootstrapStateBackend` args unwrap** — Fixed gRPC dispatch: `args` is the cfg map directly (matching the `Initialize` convention); removed the now-unnecessary `args["cfg"]` unwrap that caused bootstrap args to be silently dropped.

### Notes

- Four drivers intentionally use the resource name (not a UUID) as `ProviderID`: `dns` (domain name), `storage`/Spaces (bucket name), `registry` (registry name), and `iam_role` (declarative stub). These are correct by design — the DO API identifies those resources by name.

## [v0.7.6] - 2026-04-24

### Fixed

- `BootstrapStateBackend` dispatch args decode: `args` is the cfg map (not a wrapper with a `cfg` key). Removed the intermediate unwrap added in v0.7.5 that caused the bootstrap bucket/region config to be silently dropped.

## [v0.7.5] - 2026-04-22

### Fixed

- Wired `IaCProvider.BootstrapStateBackend` in `InvokeMethod` gRPC dispatch so `wfctl infra bootstrap` calls reach the provider.

## [v0.7.4] - 2026-04-20

### Added

- `DOProvider.BootstrapStateBackend` — provisions a DigitalOcean Spaces bucket for remote `wfctl` state storage and exports `WFCTL_STATE_BUCKET` and `SPACES_BUCKET` env vars.

## [v0.7.3] - 2026-04-18

### Added

- Name-based `Read` + `SupportsUpsert` for VPC, Firewall, and Database drivers, enabling `ErrResourceAlreadyExists → upsert` in `DOProvider.Apply`.

## [v0.7.2] - 2026-04-16

### Fixed

- Gate upsert path on `SupportsUpsert` capability check to prevent name-based Read on drivers that require a `ProviderID`.
- Validate that upsert Read returns a non-empty `ProviderID` before attempting Update.

## [v0.7.1] - 2026-04-14

### Fixed

- `DOProvider.Apply` now attempts an upsert (Read + Update) when Create returns `ErrResourceAlreadyExists`, avoiding duplicate-resource errors on re-runs.

## [v0.7.0] - 2026-04-10

### Added

- App Platform sidecar support.
- `DatabaseDriver.trusted_sources` firewall rules.
- Full `AppSpec` field fill: services, jobs, workers, static sites, environment variables, image spec, health checks.
- `SupportedCanonicalKeys` coverage for all resource types.
