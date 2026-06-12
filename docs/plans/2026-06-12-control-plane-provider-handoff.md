# DigitalOcean Control-Plane Provider Handoff Implementation Plan

> **For the implementing agent:** REQUIRED SUB-SKILL: Use autodev:executing-plans to implement this plan task-by-task.

**Goal:** Complete T567 by adding a DigitalOcean App Platform no-account dry-run provider handoff fixture against the released public control-plane contracts, then record the completion in workflow-compute.

**Architecture:** `workflow-plugin-digitalocean` gets a small internal fixture package plus tests that import `workflow-plugin-control-plane v0.1.0`, validate provider/capability/schema/idempotency binding, and prove the plugin binary dependency graph remains free of control-plane imports. `workflow-compute` then records merge/CI evidence and leaves T568/T569 queued.

**Tech Stack:** Go 1.26, `workflow-plugin-control-plane v0.1.0`, DigitalOcean plugin manifest/capability metadata, GitHub Actions, autodev scope lock.

**Base branch:** main

---

## Scope Manifest

**PR Count:** 2
**Tasks:** 6
**Estimated Lines of Change:** ~520

**Out of scope:**
- Live DigitalOcean API calls, account credentials, provider initialization, or cloud resources.
- workflow-compute adapter consumption; T568 owns it.
- workflow-scenarios or workflow-compute-scenarios proof; T569 owns it.
- Release tagging, production/staging deploys, migrations, new CLI commands, provider action dispatch, approval flows, credential issuance, rollout state, or IaC apply policy.

**PR Grouping:**

| PR # | Title | Tasks | Branch |
|------|-------|-------|--------|
| 1 | test(control-plane): add DigitalOcean provider handoff fixture | Task 1, Task 2, Task 3, Task 4 | workflow-plugin-digitalocean:feat/control-plane-provider-handoff |
| 2 | docs: close DigitalOcean control-plane provider handoff phase | Task 5, Task 6 | workflow-compute:docs/control-plane-provider-handoff |

**Status:** Complete 2026-06-12T18:37:16Z

### Task 1: Pin Control-Plane Contract And Add Fixture Package

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/controlplanehandoff/fixture.go`

Steps:

1. Add `github.com/GoCodeAlone/workflow-plugin-control-plane v0.1.0`.
2. Create `internal/controlplanehandoff` with:
   - canonical App Platform dry-run input schema JSON using opaque `environmentRef`, `serviceRef`, and `imageDigest`;
   - `AppPlatformDryRunCapability` metadata for provider id `workflow-plugin-digitalocean`, provider version `v2.0.15`, capability `app-platform-dry-run`, capability version `v1`, computed schema digest, and `RequiresCredentials=false`;
   - `NewAppPlatformDryRunHandoff(actionNonce, idempotencyKey string) *descriptorspb.ProviderHandoffRef`.
3. Keep the package independent of `internal/provider`, godo clients, and `DIGITALOCEAN_TOKEN`.
4. Verify:

   ```bash
   GOWORK=off go test ./internal/controlplanehandoff -run TestDoesNotExist -count=1
   ```

   Expected: package compiles or reports no test files; no DigitalOcean token is required.

Rollback: revert the commit; no runtime state exists.

### Task 2: Add Positive Fixture Validation Tests

**Files:**
- Create: `internal/controlplanehandoff/fixture_test.go`

Steps:

1. Write `TestAppPlatformDryRunHandoffValidatesAgainstControlPlaneContract`.
2. Assert:
   - `descriptors.ValidateProviderHandoffRef` accepts the fixture;
   - provider id/version and capability id/version match the fixture;
   - input schema digest equals the digest computed from the canonical schema;
   - `RequiresCredentials=false`;
   - the manifest download URLs still contain `v2.0.15`, so the provider version constant has evidence in repo metadata.
3. Run:

   ```bash
   GOWORK=off go test ./internal/controlplanehandoff -count=1
   ```

   Expected: `ok github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/controlplanehandoff`.

Rollback: revert the test/fixture commit.

### Task 3: Add Negative Provider Handoff Cases

**Files:**
- Modify: `internal/controlplanehandoff/fixture.go`
- Modify: `internal/controlplanehandoff/fixture_test.go`

Steps:

1. Add `ValidateAppPlatformDryRunHandoff(ref *descriptorspb.ProviderHandoffRef, seen map[string]struct{}) error`.
2. Validate:
   - public control-plane shape with `descriptors.ValidateProviderHandoffRef`;
   - exact provider id/version;
   - exact capability id/version;
   - exact input schema digest;
   - idempotency key is not already in `seen`, then bind it;
   - selected fixture does not require credentials.
3. Add table tests for:
   - provider id/version skew;
   - capability id/version skew;
   - mismatched input schema digest;
   - replayed idempotency key;
   - invalid nonce/key shape;
   - credential-required fixture path.
4. Run:

   ```bash
   GOWORK=off go test ./internal/controlplanehandoff -count=1
   ```

   Expected: all negative cases fail closed with errors naming the mismatched field.

Rollback: revert the fixture validation commit.

### Task 4: Verify Plugin Boundary And Open DigitalOcean PR

**Files:**
- Modify: `docs/plans/2026-06-12-control-plane-provider-handoff-design.md`
- Modify: `docs/plans/2026-06-12-control-plane-provider-handoff.md`

Steps:

1. Run:

   ```bash
   GOWORK=off go test ./internal/controlplanehandoff ./... -count=1
   GOWORK=off go list -deps ./cmd/plugin | rg 'github.com/GoCodeAlone/workflow-plugin-control-plane'
   git diff --check
   rg -n '/Users/[[:alnum:]_.-]+' docs/plans/2026-06-12-control-plane-provider-handoff-design.md docs/plans/2026-06-12-control-plane-provider-handoff.md internal/controlplanehandoff
   ```

   Expected: Go tests exit 0; dependency scan exits 1 with no matches; diff check exits 0; machine-path scan exits 1 with no matches.
2. Push the branch, create the DigitalOcean plugin PR, run `gh --version` immediately before and after `gh pr create`, add `copilot-pull-request-reviewer`, monitor CI/reviews, fix findings, and admin-squash merge only when green.

Rollback: revert the DigitalOcean plugin PR; no release tag or cloud state is created.

### Task 5: Close T567 In Workflow-Compute

**Files:**
- Modify in `workflow-compute`: `SPEC.md`
- Modify in `workflow-compute`: `docs/plans/deferred.md`
- Modify in `workflow-compute`: `provider_catalog_boundary_test.go`
- Create in `workflow-compute`: `docs/plans/2026-06-12-control-plane-provider-handoff-design.md`
- Create in `workflow-compute`: `docs/plans/2026-06-12-control-plane-provider-handoff.md`

Steps:

1. Create `workflow-compute` branch `docs/control-plane-provider-handoff` from current `origin/main`.
2. Add closure docs citing:
   - DigitalOcean plugin PR number and merge commit;
   - PR and post-merge main CI evidence;
   - `workflow-plugin-control-plane v0.1.0` dependency;
   - no-account dry-run fixture evidence;
   - no `cmd/plugin` runtime dependency edge.
3. Update SPEC/deferred rows:
   - mark T567 complete;
   - keep T568/T569 queued;
   - do not claim adapter consumption or scenario proof.
4. Add guard tests requiring all evidence strings and rejecting host-authority-transfer wording.
5. Run:

   ```bash
   GOWORK=off go test . -run 'TestSpecRecordsControlPlaneProviderHandoffPhase|TestSpecRecordsControlPlaneDescriptorValidationPhase|TestControlPlaneAuthorityBoundaryRejectsAuthorityTransferLanguage' -count=1
   GOWORK=off go test ./... -count=1
   git diff --check
   rg -n '/Users/[[:alnum:]_.-]+' SPEC.md docs/plans/deferred.md provider_catalog_boundary_test.go docs/plans/2026-06-12-control-plane-provider-handoff-design.md docs/plans/2026-06-12-control-plane-provider-handoff.md
   ```

   Expected: tests pass; diff check exits 0; machine-path scan exits 1 with no matches.

Rollback: revert the workflow-compute closure PR; no runtime state exists.

### Task 6: Close Scope Lock And Continue

**Files:**
- Modify: `docs/plans/2026-06-12-control-plane-provider-handoff.md`
- Delete: `docs/plans/2026-06-12-control-plane-provider-handoff.md.scope-lock`
- Append: `/Users/<name>/workspace/.autodev/state/phase-progress.jsonl`

Steps:

1. After both PRs merge and post-merge main CI is green, run:

   ```bash
   bash /Users/<name>/.codex/plugins/cache/autodev-marketplace/autodev/6.5.0/hooks/scope-lock-complete docs/plans/2026-06-12-control-plane-provider-handoff.md --evidence "<merged PR and CI evidence>"
   ```

2. If the plan has already merged before completion, use the same tiny closure-PR pattern as T566.
3. Append a compact JSONL phase-progress row with `nx` set to `T568 workflow-compute control-plane descriptor adapter phase`.
4. Continue into T568.

Rollback: if completion evidence is wrong, revert the closure commit and restore the scope-lock file from the prior commit.
