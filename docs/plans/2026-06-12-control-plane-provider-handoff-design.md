# DigitalOcean Control-Plane Provider Handoff Design

## Goal

Close T567 by proving `workflow-plugin-digitalocean` can validate a
product-neutral control-plane provider handoff reference for a DigitalOcean App
Platform dry-run capability, without requiring a DigitalOcean account, invoking
DigitalOcean APIs, or moving host authority into the provider or the public
control-plane package.

## Global Design Guidance

Source: `workflow-compute` `SPEC.md` C66/V769 and
`docs/plans/2026-06-12-control-plane-cp2-public-contracts-design.md`.

| guidance | design response |
|---|---|
| Provider action handoff descriptors are typed references only, not executable payloads, raw URLs, callbacks, or shell commands | The fixture validates only `ProviderHandoffRef` fields: provider id/version, capability id/version, input schema digest, action nonce, and idempotency key. |
| Hosts keep route binding, authz, deployment approval, provider dispatch, credentials, trust roots, private keys, rollout state, and IaC apply policy | The fixture does not authorize, dispatch, apply, approve, issue credentials, or persist rollout state. It is a no-account dry-run contract check. |
| Provider handoff must cover confused-deputy checks, capability skew, idempotency-key binding, input schema digest checks, and credential-required path rejection | Tests cover valid dry-run binding plus negative cases for wrong provider/version/capability, replayed idempotency key, mismatched digest, invalid nonce/key shape, and a credential-required capability path. |
| `workflow-compute` remains a thin product assembly and reusable contracts move to public plugins | Runtime code lands in the public DigitalOcean plugin repo; workflow-compute only records evidence after the plugin PR merges. |

## Approach

The DigitalOcean plugin will add a small internal control-plane handoff fixture
package. The package is not wired into `cmd/plugin`; it exists so provider-side
tests can validate the real public `workflow-plugin-control-plane` descriptor
contracts against DigitalOcean capability metadata.

The fixture describes one no-account capability:

- provider plugin id: `workflow-plugin-digitalocean`
- provider plugin version: `v2.0.15`
- capability id/version: `app-platform-dry-run` / `v1`
- input schema: a small canonical JSON schema for an App Platform dry-run
  request with opaque `environmentRef`, `serviceRef`, and `imageDigest` fields
- credential behavior: explicitly `RequiresCredentials=false`

Validation is intentionally local and deterministic. It computes the schema
digest from the committed schema string, calls
`descriptors.ValidateProviderHandoffRef`, checks exact provider/capability
binding, and tracks idempotency keys in a caller-owned replay set.

## Alternatives Considered

| option | trade-off | decision |
|---|---|---|
| Test-only helper in `_test.go` | smallest change, but future provider fixtures cannot reuse the shape and the fixture is less visible to provider maintainers | rejected |
| Internal fixture package plus tests | exposes a reusable provider-owned fixture without changing plugin runtime loading or public API | selected |
| Production action execution path | closer to eventual T568/T569 flows, but would add dispatch/credential/IaC semantics before the host adapter and scenarios exist | rejected as scope drift |

## Security Review

This phase never reads or requires `DIGITALOCEAN_TOKEN`. The dry-run fixture
does not call `Initialize`, create a `godo` client, or invoke provider drivers.
Negative tests fail closed if a credential-required capability is accidentally
accepted as the no-account fixture. Schema inputs use opaque handles and a digest
ref; no account id, project id, app id, domain, API URL, callback, shell command,
secret reference, or raw provider identifier is accepted by the fixture.

## Infrastructure Impact

No cloud resources, deployments, migrations, secrets, network listeners, CI
secrets, or DigitalOcean API calls are added. The only runtime-adjacent impact is
a module dependency on `workflow-plugin-control-plane` used by an internal
fixture package; `go list -deps ./cmd/plugin` must prove the plugin binary does
not import that package.

## Multi-Component Validation

The smallest real boundary for T567 is provider plugin plus public
control-plane package. Tests import the released control-plane validators and
the DigitalOcean fixture in the same Go module, proving the provider handoff ref
is accepted by the public contract and rejected for provider/capability/schema
skew and replay cases. Workflow host adapter consumption is deliberately T568,
and scenario proof is T569.

## Assumptions

- `workflow-plugin-control-plane v0.1.0` remains the released contract package
  from T565/T566.
- DigitalOcean App Platform dry-run is the correct provider family to fixture
  first because workflow-compute staging already deploys through DigitalOcean
  App Platform.
- `v2.0.15` is the correct provider release identity to record because it is the
  current plugin manifest download version in this repo.
- Replay detection can be represented by a caller-owned in-memory key set for
  this fixture phase; durable idempotency storage belongs to the host adapter or
  scenario phases.

## Self-Challenge

1. A pure `_test.go` fixture would be smaller. The design keeps a tiny internal
   package because T567 is meant to leave provider-maintained fixture code in
   the public plugin repo, not just a one-off test.
2. The schema digest may drift if the fixture schema is reformatted. The
   implementation computes the digest from the committed canonical schema and
   tests exact mismatch behavior.
3. This does not prove workflow-compute can consume the descriptor. That is
   intentional: T568 is the adapter phase and T569 is scenario proof.

## Rollback

Revert the DigitalOcean plugin PR and remove the T567 completion evidence from
workflow-compute. No runtime state, release tag, cloud resource, or migration is
created by this phase.
