# Migration: `iac.state` `spaces` backend moves to the DigitalOcean plugin

**Affected release:** `workflow-plugin-digitalocean` v1.1.0
**Related:** workflow cloud-SDK-extraction Phase B (core PR 6); `decisions/0034`, `decisions/0035`, `decisions/0036`

## What changed

The `iac.state` `spaces` backend (an S3-compatible state store, `aws-sdk-go-v2/service/s3`)
was previously implemented **in workflow core**. As of workflow's cloud-SDK-extraction
Phase B core PR, the in-core `spaces` case is **removed** — this is a **clean break**.

`workflow-plugin-digitalocean` v1.1.0 now **serves** the `spaces` backend over the typed
`IaCStateBackend` gRPC contract: it carries the ported S3-compatible state store and a
`Configure` RPC that receives the `iac.state` module config from the engine host.

## Impact

- The YAML you write is **unchanged**. `iac.state` with `backend: spaces` keeps the exact
  same config keys (`region`, `bucket`, `prefix`, `accessKey`, `secretKey`, `endpoint`).
- After workflow's Phase B core PR, `backend: spaces` **requires
  `workflow-plugin-digitalocean >= v1.1.0` loaded**. Without the plugin, the engine has no
  `spaces` backend and `iac.state` initialization fails.
- The `DO_SPACES_ACCESS_KEY` / `DO_SPACES_SECRET_KEY` environment-variable credential
  fallbacks are preserved in the plugin's port — behavior is unchanged.

## Action required

Ensure `workflow-plugin-digitalocean` v1.1.0 (or later) is installed and loaded in any
workflow deployment that uses `iac.state` with `backend: spaces`. Pin the plugin in your
plugin lockfile / manifest as you would any other plugin dependency.

No config changes are needed.

## Version compatibility

| workflow engine | `backend: spaces` source |
|---|---|
| pre-Phase-B | in-core (no plugin needed) |
| Phase-B core PR and later | `workflow-plugin-digitalocean >= v1.1.0` (plugin-served) |

## Rollback

The `spaces` clean break rolls back only as a **matched pair** with workflow's Phase B core
PR — reverting that core PR restores the in-core `spaces` backend. The
`workflow-plugin-digitalocean` v1.1.0 release itself is **additive** (it adds the
`IaCStateBackend` service surface without removing anything); a defect in the plugin port is
addressed with a patch release, not a rollback of v1.1.0.
