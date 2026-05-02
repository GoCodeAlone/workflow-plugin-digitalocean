# DigitalOcean Container Registry — owned vs shared patterns

DigitalOcean Container Registry (DOCR) is an **account-level** resource:
the [account limits](https://docs.digitalocean.com/products/container-registry/details/limits/) cap a Basic-plan account at **one registry**, and a Professional-plan account at up to ten. A single registry can host **many repositories** (e.g. `myorg-registry/api`, `myorg-registry/worker`), so multi-project consolidation under one registry is supported on every tier.

The plugin's `infra.registry` driver is **account-singleton** today: godo's `Registry.Get()` and `Registry.Delete()` are account-scoped (no name parameter), so the driver can only see and manage one registry per account regardless of plan tier. On Professional accounts with multiple registries, the others have to be managed out-of-band; the plugin can be enhanced later to use the named-registry godo APIs if/when that becomes a real requirement.

Given the driver constraint and DO's design (registries can host many repositories), two `infra.yaml` patterns make sense:

- **Owned** — declare the registry as an `infra.registry` IaC module; the plugin manages the create/update/destroy lifecycle. One registry per account (the driver's limit). Use when this project owns the DO account end-to-end.
- **Shared** — omit the `infra.registry` module declaration; reference only the path under `ci.registries`. Bootstrap the registry once out-of-band. Use when multiple projects share a DO account.

Picking the wrong pattern produces a deploy failure that's awkward to diagnose: two projects' deploys race to create the registry, the second mover hits "registry already exists" or a name-mismatch check, and the deploy aborts mid-pipeline.

## Pattern 1 — Owned (single-project account)

The project's `infra.yaml` declares the registry as an IaC resource. The plugin's `RegistryDriver` is responsible for creating, updating, and tearing down the registry across `wfctl infra plan/apply/destroy`.

```yaml
infra:
  modules:
    - name: myorg-registry
      type: infra.registry
      provider: digitalocean
      config:
        tier: basic        # or "professional", "starter"
        region: nyc3

ci:
  registries:
    - name: docr
      type: do
      path: registry.digitalocean.com/myorg-registry
      auth:
        env: DIGITALOCEAN_TOKEN
```

Image references in App Platform / app specs use the same path:

```yaml
infra:
  modules:
    - name: myproj-app
      type: infra.container_service     # App Platform
      provider: digitalocean
      config:
        services:
          - name: api
            image: "registry.digitalocean.com/myorg-registry/api:${IMAGE_SHA}"
```

`wfctl infra apply` creates the registry on first run; subsequent runs are no-ops once the registry's desired state matches actual state. (DOCR doesn't support in-place updates — the driver's `Update` returns current state without modification.)

**When to use Owned:**
- The DO account hosts only this project (no risk of conflict).
- The project's IaC owns its full deploy surface (e.g. for a self-contained product or per-customer deploy).
- You want `wfctl infra destroy` to actually destroy the registry alongside everything else.

## Pattern 2 — Shared (multi-project account)

The registry is bootstrapped once, manually or by a one-time workflow, and lives outside any single project's IaC. Each project's `infra.yaml` references the registry path but does not declare it as an `infra.registry` module.

```yaml
# infra.yaml — registry is NOT declared as an IaC module.
# Bootstrap once, out-of-band:
#   doctl registry create myorg-registry --subscription-tier basic --region nyc3
infra:
  modules:
    - name: myproj-app
      type: infra.container_service     # App Platform
      provider: digitalocean
      # ...

ci:
  registries:
    - name: docr
      type: do
      path: registry.digitalocean.com/myorg-registry
      auth:
        env: DIGITALOCEAN_TOKEN
```

Image references include the project name as the repository to keep peers' images apart:

```yaml
infra:
  modules:
    - name: myproj-app
      type: infra.container_service
      provider: digitalocean
      config:
        services:
          - name: api
            image: "registry.digitalocean.com/myorg-registry/myproj-api:${IMAGE_SHA}"
```

The deploy workflow should **verify** the shared registry exists before pushing, and fail with a clear bootstrap message if it doesn't:

```yaml
- name: Verify shared DOCR registry exists
  run: |
    set -euo pipefail
    EXPECTED=myorg-registry
    EXISTING=$(doctl registry get --format Name --no-header || echo "")
    if [ -z "$EXISTING" ]; then
      echo "::error::No DO container registry on this account."
      echo "::error::Bootstrap once: doctl registry create $EXPECTED --subscription-tier basic --region nyc3"
      exit 1
    elif [ "$EXISTING" != "$EXPECTED" ]; then
      echo "::error::Account has registry '$EXISTING', expected '$EXPECTED'."
      exit 1
    fi
```

**When to use Shared:**
- Multiple projects deploy to the same DO account.
- The team accepts that one project's deploy depends on the registry being bootstrapped first (a one-time prerequisite, not a per-deploy step).

**Don't put `doctl registry create` in a per-deploy workflow** when sharing the account. The first project's deploy works; the second project's deploy races the create and fails with "registry already exists" or a name-mismatch check.

## Migration: from per-project to shared

If two projects already declared owned registries on the same DO account, the second one's deploy fails (Basic = 1 registry; the driver is account-singleton on every plan). To consolidate:

1. Pick which existing registry name wins (typically the one with the most existing images, to avoid re-pushing).
2. Update the **other** project's `infra.yaml` + workflows to reference that name and to drop the `infra.registry` module declaration.
3. Re-tag and re-push the migrating project's images under the shared registry path.
4. Update App Platform image references to the shared path.
5. Document the shared name in each repo's CLAUDE.md / contributing notes so future contributors don't re-introduce the conflict.

The naming will be uneven (whichever project named first stays as the registry name). DO doesn't expose a registry rename, so changing the name effectively means destroy + recreate + re-push of all images.

## Why the plugin doesn't pick the pattern for you

The Owned/Shared distinction is a **deployment topology** decision, not a plugin capability. The driver supports both: declaring an `infra.registry` module triggers Owned (Create/Update/Destroy lifecycle); referencing only the path under `ci.registries` without a module triggers Shared (consume-only).

The plugin's `RegistryDriver.Create` is idempotent against the DO API for same name + region (DOCR returns the existing registry rather than erroring), so a single owner's repeated applies are safe. But it cannot distinguish "this is intentionally shared" from "this is a misconfiguration where two projects both think they own it" — that has to come from the topology you choose.

When in doubt: multiple projects on one account → Shared. Single-project account → Owned.

## Driver limitations to be aware of

- **Account-singleton.** The driver uses godo's `Registry.Get(ctx)` and `Registry.Delete(ctx)` (no name parameter), so it can only manage one registry per account regardless of plan tier. On Professional accounts that legitimately have multiple registries, the others must be managed out-of-band.
- **No in-place updates.** DOCR doesn't support tier or region changes after creation. The driver's `Update` returns current state without modification; tier/region drift is not reconciled.
- **Idempotent Create**, but only for **same name + region**. Creating a registry with a different name when one already exists fails on the DO API side, not at plan time. Catch this earlier with the deploy-time verification snippet above.
