# Changelog

All notable changes to workflow-plugin-digitalocean are documented here.

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
