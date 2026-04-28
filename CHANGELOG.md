# Changelog

All notable changes to workflow-plugin-digitalocean are documented here.

## [Unreleased]

### Added

- **`expose: internal` on `infra.container_service`** (P-2.F4) — App Platform
  services may now declare `expose: internal` to opt out of the public edge
  route. When set, `buildAppSpec` zeroes `HTTPPort`, folds `http_port` into
  `InternalPorts` (so siblings can dial it), and drops `Routes` entirely. The
  service becomes reachable only from sibling components in the same app via
  DO App Platform's internal DNS (`<service-name>.internal:<port>`). Default
  remains `expose: public`.

  Misconfiguration guard: declaring both `http_port` and `internal_ports` with
  conflicting values under `expose: internal` returns the explicit error
  `internal_ports must include http_port when both are set; use one or the
  other` at plan time, rather than silently dropping the http port.

  This unblocks core-dump P-1's NATS sidecar and any other backing-service
  component that must not face the open internet.

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
