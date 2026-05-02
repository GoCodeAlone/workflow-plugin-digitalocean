# DigitalOcean Container Registry — owned vs shared patterns

DigitalOcean Container Registry (DOCR) is an **account-level** resource:
the [account limits](https://docs.digitalocean.com/products/container-registry/details/limits/) cap a Basic-plan account at **one registry**, and a Professional-plan account at up to ten. A single registry can host **many repositories** (e.g. `myorg-registry/api`, `myorg-registry/worker`), so multi-project consolidation is the intended pattern on Basic.

This shapes how `infra.yaml` should declare the registry. Two patterns are supported:

- **Owned** — the project's IaC manages the registry's lifecycle (create, update, destroy). Use when the project is the sole occupant of the DO account, or on Professional where each project can have its own registry.
- **Shared** — the registry is bootstrapped out-of-band; the project only references it as a string path. Use when multiple projects share a DO account and one registry hosts repositories for all of them.

Picking the wrong pattern produces a deploy failure that's awkward to diagnose: two projects' deploys race to create the registry, the second mover hits "registry already exists" or a name-mismatch check, and the deploy aborts mid-pipeline.

## Pattern 1 — Owned (single-project account, or Professional)

The project's `infra.yaml` declares the registry as an IaC resource. The plugin's `RegistryDriver` is responsible for creating, updating, and tearing down the registry across `wfctl infra plan/apply/destroy`.

```yaml
infra:
  modules:
    - name: myorg-registry
      type: digitalocean.container_registry
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
services:
  - name: api
    image:
      registry_type: DOCR
      repository: api
      tag: ${IMAGE_SHA}
      # Resolves to: registry.digitalocean.com/myorg-registry/api:${IMAGE_SHA}
```

`wfctl infra apply` creates the registry on first run; subsequent runs are no-ops once the registry's desired state matches actual state.

**When to use Owned:**
- The DO account hosts only this project (no risk of conflict).
- The team is on the Professional plan and has decided each project owns its own registry.
- The project's IaC owns its full deploy surface (e.g. for a self-contained product or per-customer deploy).

## Pattern 2 — Shared (multi-project on Basic)

The registry is bootstrapped once, manually or by a one-time workflow, and lives outside the project's IaC. Each project's `infra.yaml` references the registry path but does not declare it as a resource.

```yaml
# infra.yaml — registry is NOT declared as an IaC module.
# Bootstrap once, out-of-band:
#   doctl registry create myorg-registry --subscription-tier basic --region nyc3
infra:
  modules:
    - name: myproj-app
      type: digitalocean.app_platform
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
services:
  - name: api
    image:
      registry_type: DOCR
      repository: myproj-api
      tag: ${IMAGE_SHA}
      # Resolves to: registry.digitalocean.com/myorg-registry/myproj-api:${IMAGE_SHA}
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
- Multiple projects deploy to the same DO account on the Basic plan.
- The team accepts that one project's deploy depends on the registry being bootstrapped first (a one-time prerequisite, not a per-deploy step).

**Don't put `doctl registry create` in a per-deploy workflow** when sharing the account. The first project's deploy works; the second project's deploy races the create and fails with "registry already exists" or a name-mismatch check.

## Migration: from per-project to shared

If two projects already declared owned registries on the same Basic account, the second one's deploy fails. To consolidate:

1. Pick which existing registry name wins (typically the one with the most existing images, to avoid re-pushing).
2. Update the **other** project's `infra.yaml` + workflows to reference that name and to drop the registry-as-IaC-resource declaration.
3. Re-tag and re-push the migrating project's images under the shared registry path.
4. Update App Platform image references to the shared path.
5. Document the shared name in each repo's CLAUDE.md / contributing notes so future contributors don't re-introduce the conflict.

The naming will be uneven (whichever project named first stays as the registry name). This is a one-time historical artifact unless you upgrade to Professional and split back into per-project registries, or coordinate a registry rename (DO doesn't expose rename; effectively destroy + recreate + re-push of all images).

## Why the plugin doesn't pick the pattern for you

The Owned/Shared distinction is a **deployment topology** decision, not a plugin capability. The driver supports both: declaring `digitalocean.container_registry` as a module triggers Owned (Create/Update/Destroy lifecycle); referencing only the path under `ci.registries` without a module triggers Shared (consume-only).

The plugin's `RegistryDriver.Create` is idempotent against the DO API (creating an existing registry is a no-op for the same name + region), so the Owned pattern is safe to retry. But it cannot distinguish "this is intentionally shared" from "this is a misconfiguration where two projects both think they own it" — that has to come from the topology you choose.

When in doubt: Basic plan + multiple projects → Shared. Otherwise → Owned.
