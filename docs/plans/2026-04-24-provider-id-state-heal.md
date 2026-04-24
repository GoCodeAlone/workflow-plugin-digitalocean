# DO Plugin ProviderID State-Heal Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Ship workflow-plugin-digitalocean v0.7.8 with per-driver state-heal (Update/Delete), shared `isUUIDLike` helper, root-cause audit + fix, and an integration-test harness that pins down the "BMW invalid-uuid" regression so it cannot silently return.

**Architecture:** `AppPlatformDriver.Update` and `Delete` call a new `resolveProviderID(ctx, ref)` helper that validates `ref.ProviderID` shape via a shared `isUUIDLike`. Stale name-as-ProviderID state falls back to `findAppByName` to resolve the real UUID; returned `ResourceOutput.ProviderID` contains the healed UUID so wfctl rewrites state transparently. Root-cause is audited + fixed or documented as historic. Integration-test harness exercises the full Apply/Update loop against a fake `godo.AppsService` + in-memory state, including the specific "stale name heals to UUID" scenario.

**Tech Stack:** Go 1.26, `github.com/digitalocean/godo`, `github.com/GoCodeAlone/workflow` v0.18.10 (v0.18.10.1 after impl-migrations' hotfix tags).

---

## Repo + branch

**Repo:** `/Users/jon/workspace/workflow-plugin-digitalocean`
**Branch:** `feat/v0.7.8-troubleshoot` (already open as PR #22)

impl-digitalocean-2 has uncommitted local work that implements Layer 2 (state-heal wiring in Update/Delete + `resolveProviderID` + `isUUIDLike`). Task 1 commits that in its current form; Task 2 extracts `isUUIDLike` to a shared location.

## Prereqs

- Workflow v0.18.10.1 tagged (impl-migrations' in-flight PR #478). go.mod on this branch is already bumped to v0.18.10 (commit 5bb52f4); Task 0 below re-bumps to v0.18.10.1 when it ships.

---

### Task 0: Bump go.mod to v0.18.10.1 (blocked on impl-migrations tag)

**Files:**
- Modify: `go.mod`, `go.sum`

**Step 1: Wait for `v0.18.10.1` tag on GoCodeAlone/workflow**

Check via: `gh release view v0.18.10.1 --repo GoCodeAlone/workflow --json tagName`.
If not yet tagged, keep working on Tasks 1-6 which don't depend on the bump.

**Step 2: Bump**

```bash
cd /Users/jon/workspace/workflow-plugin-digitalocean
go get github.com/GoCodeAlone/workflow@v0.18.10.1
go mod tidy
```

**Step 3: Verify**

Run: `GOWORK=off go build ./... && GOWORK=off go test -race -short ./...`
Expected: all packages green.

**Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "deps: bump workflow v0.18.10 → v0.18.10.1 (Troubleshoot hotfix)"
```

---

### Task 1: Commit the already-drafted state-heal in AppPlatformDriver

**Files:**
- Modify: `internal/drivers/app_platform.go` (uncommitted local changes)

**Step 1: Verify the working-tree diff matches the design**

```bash
git diff internal/drivers/app_platform.go
```

Expected content:
- New `resolveProviderID(ctx, ref) (string, error)` method that returns `ref.ProviderID` when `isUUIDLike` is true, else logs a WARN and falls back to `findAppByName(ctx, ref.Name)` for the real UUID
- New `isUUIDLike(s string) bool` helper (36 chars, hyphens at 8/13/18/23)
- `Update` calls `resolveProviderID` first, then uses the resolved ID for both `client.Update` and `client.CreateDeployment`
- `Delete` calls `resolveProviderID` first, then uses the resolved ID for `client.Delete`

If the diff deviates (e.g., missing Delete wiring), adjust so it matches before committing.

**Step 2: Stage + commit — just the driver change, not the design doc (committed separately)**

```bash
git add internal/drivers/app_platform.go
git commit -m "$(cat <<'EOF'
fix(app-platform): state-heal stale non-UUID ProviderID in Update/Delete

When state has a ProviderID that is not a canonical UUID (e.g., legacy state
that stored the resource name), Update/Delete fall back to findAppByName to
resolve the real UUID. The returned ResourceOutput carries the healed
ProviderID so wfctl rewrites state transparently on the next Apply.

Heal fires silently with a WARN log so operators can observe drift without
the deploy failing. Unit + integration test coverage in follow-up tasks.
EOF
)"
```

---

### Task 2: Extract `isUUIDLike` to shared helper

**Files:**
- Create: `internal/drivers/shared.go`
- Create: `internal/drivers/shared_test.go`
- Modify: `internal/drivers/app_platform.go` (remove local declaration)

**Step 1: Write the failing test**

Create `internal/drivers/shared_test.go`:

```go
package drivers

import "testing"

func TestIsUUIDLike_TableDriven(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"canonical uuid", "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5", true},
		{"lowercase letters", "abcdef01-2345-6789-abcd-ef0123456789", true},
		{"uppercase letters", "ABCDEF01-2345-6789-ABCD-EF0123456789", true},
		{"resource name", "bmw-staging", false},
		{"empty string", "", false},
		{"too short", "f8b6200c-3bba-48a7-8bf1", false},
		{"too long", "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5-extra", false},
		{"missing first hyphen", "f8b6200c03bba-48a7-8bf1-7a3e3a885eb5", false},
		{"wrong hyphen positions", "f8b6200-c3bba-48a7-8bf1-7a3e3a885eb5X", false},
		{"36 chars but no hyphens", "f8b6200c3bba48a78bf17a3e3a885eb5foo12", false},
		{"spaces", "f8b6200c 3bba 48a7 8bf1 7a3e3a885eb5 ", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isUUIDLike(c.in); got != c.want {
				t.Errorf("isUUIDLike(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
```

**Step 2: Run — expect compile error (isUUIDLike not in shared.go yet)**

```bash
GOWORK=off go test ./internal/drivers/... -run TestIsUUIDLike_TableDriven -v
```
Expected: test runs against the one in `app_platform.go` and passes (duplicate not yet present). OK, then go to step 3.

**Step 3: Create `internal/drivers/shared.go`**

```go
// Package drivers implements per-resource DigitalOcean drivers used by the
// plugin. shared.go holds helpers that are used by more than one driver.
package drivers

// isUUIDLike returns true when s has the canonical UUID shape:
// 36 characters with hyphens at positions 8, 13, 18, and 23.
//
// Used by drivers whose DO API requires a UUID in the URL path (app platform,
// databases, certificates, load balancers, VPCs, firewalls, droplets, etc.)
// to detect stale state that stored a resource name instead of the UUID,
// so callers can fall back to a name-based lookup instead of sending a
// malformed path parameter to the DO API.
func isUUIDLike(s string) bool {
	return len(s) == 36 && s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-'
}
```

**Step 4: Remove the duplicate from `app_platform.go`**

Delete the `isUUIDLike` function and its doc comment from `app_platform.go`. Leave `resolveProviderID` alone — it uses the shared helper.

**Step 5: Run tests**

```bash
GOWORK=off go build ./... && GOWORK=off go test -race -short ./internal/drivers/...
```
Expected: all green.

**Step 6: Commit**

```bash
git add internal/drivers/shared.go internal/drivers/shared_test.go internal/drivers/app_platform.go
git commit -m "refactor(drivers): extract isUUIDLike to shared helper with tests"
```

---

### Task 3: Root-cause audit + PR-body write-up

**Files:**
- Read: `internal/drivers/app_platform.go`
- Read: any commit `94c9227` diff (v0.7.7 "empty-ID guard")
- Modify: PR #22 description (via `gh pr edit`)
- Possibly modify: `internal/drivers/app_platform.go` if root-cause is still-present

**Step 1: Audit v0.7.7's empty-ID guard**

```bash
git show 94c9227 -- internal/drivers/app_platform.go | head -100
```

Look for: does any code path in `Create` or downstream helpers silently substitute `ref.Name` for `ProviderID` when `app.ID == ""`? v0.7.7's intent was to *guard against* an empty ID by enabling the upsert path, but the guard might accidentally produce a name-as-ProviderID.

Also audit `appOutput(app *godo.App)` (the helper at ~line 403):
- Verify it uses `app.ID` for the ProviderID field
- Verify there is no fallback to `app.Spec.Name` or similar

**Step 2: Audit the gRPC marshaling path**

Check `internal/plugin.go` (plugin's gRPC dispatcher) for:
- Does `ResourceOutput` round-trip correctly through `structpb.NewStruct` / back? Specifically, the `ProviderID` string field — does it survive marshaling?
- Is there any default-on-empty logic that would substitute a name?

**Step 3: Write the write-up**

Draft a ~200-word explanation covering:
- **What happened in BMW:** state has `ProviderID="bmw-staging"` (name) despite v0.7.7 being deployed. When wfctl called Update, DO rejected the PUT because "bmw-staging" isn't a UUID.
- **Where the name came from:** most likely a pre-v0.7.7 Create that never had the empty-ID guard. Once BMW's state had the bad shape, every subsequent Update re-used it without healing (v0.7.7 only guards NEW creates, not existing state).
- **Why v0.7.7 didn't catch it:** unit-test coverage but no integration test that round-trips state through Apply/Update. Tonight's integration test harness (Task 4) covers that gap.
- **What changes in v0.7.8:** state-heal in Update/Delete plus integration tests that would have caught tonight's regression. If the Create path *does* still have a silent name-fallback, add a commit that replaces it with an explicit error so state can never be populated in the bad shape going forward.

**Step 4: Push write-up to PR body**

```bash
gh pr edit 22 --repo GoCodeAlone/workflow-plugin-digitalocean --body "$(cat <<'EOF'
<existing PR body>

## Root-cause (v0.7.8 addendum)

<write-up from step 3>
EOF
)"
```

(Preserve the existing PR body; append the root-cause section.)

**Step 5: If a fix in Create is needed, commit separately**

```bash
git add internal/drivers/app_platform.go
git commit -m "fix(app-platform): error explicitly on empty app.ID in Create, never substitute name"
```

---

### Task 4: Integration-test harness

**Files:**
- Create: `internal/drivers/integration_test_helpers_test.go`

**Step 1: Write the fake godo.AppsService + in-memory state harness**

Create `internal/drivers/integration_test_helpers_test.go`:

```go
package drivers

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// fakeAppsClient implements AppsClient for integration tests. It tracks every
// call so tests can assert which DO API methods were invoked and with which
// identifiers, and it returns configurable responses seeded per test.
type fakeAppsClient struct {
	// Seeded responses.
	createResp *godo.App // returned by Create
	getByID    map[string]*godo.App // keyed by app ID
	listApps   []godo.App // returned by List
	updateResp map[string]*godo.App // keyed by app ID (what Update returns)
	deleteErrs map[string]error // keyed by app ID (nil ⇒ 204)

	// Call log.
	createCalls        []string // app names created
	getCalls           []string // app IDs or names passed to Get
	updateCalls        []string // app IDs passed to Update
	createDeployCalls  []string
	deleteCalls        []string
	listCalls          int
	listDeploymentsFor []string
}

func newFakeAppsClient() *fakeAppsClient {
	return &fakeAppsClient{
		getByID:    map[string]*godo.App{},
		updateResp: map[string]*godo.App{},
		deleteErrs: map[string]error{},
	}
}

// --- AppsClient interface implementation ---

func (f *fakeAppsClient) Create(_ context.Context, req *godo.AppCreateRequest) (*godo.App, *godo.Response, error) {
	f.createCalls = append(f.createCalls, req.Spec.Name)
	if f.createResp != nil {
		f.getByID[f.createResp.ID] = f.createResp
		return f.createResp, resp(http.StatusCreated), nil
	}
	// Default: return an App with a deterministic UUID.
	app := &godo.App{
		ID:   fmt.Sprintf("uuid-%s", req.Spec.Name),
		Spec: req.Spec,
		CreatedAt: time.Now(),
	}
	f.getByID[app.ID] = app
	return app, resp(http.StatusCreated), nil
}

func (f *fakeAppsClient) Get(_ context.Context, id string) (*godo.App, *godo.Response, error) {
	f.getCalls = append(f.getCalls, id)
	if app, ok := f.getByID[id]; ok {
		return app, resp(http.StatusOK), nil
	}
	return nil, resp(http.StatusNotFound), fmt.Errorf("not found")
}

func (f *fakeAppsClient) List(_ context.Context, _ *godo.ListOptions) ([]godo.App, *godo.Response, error) {
	f.listCalls++
	// Synthesize list from seeded apps if listApps is nil.
	if f.listApps != nil {
		return f.listApps, resp(http.StatusOK), nil
	}
	out := make([]godo.App, 0, len(f.getByID))
	for _, a := range f.getByID {
		out = append(out, *a)
	}
	return out, resp(http.StatusOK), nil
}

func (f *fakeAppsClient) Update(_ context.Context, id string, _ *godo.AppUpdateRequest) (*godo.App, *godo.Response, error) {
	f.updateCalls = append(f.updateCalls, id)
	if app, ok := f.updateResp[id]; ok {
		return app, resp(http.StatusOK), nil
	}
	if app, ok := f.getByID[id]; ok {
		return app, resp(http.StatusOK), nil
	}
	return nil, resp(http.StatusNotFound), fmt.Errorf("update: not found: %s", id)
}

func (f *fakeAppsClient) CreateDeployment(_ context.Context, id string, _ *godo.DeploymentCreateRequest) (*godo.Deployment, *godo.Response, error) {
	f.createDeployCalls = append(f.createDeployCalls, id)
	return &godo.Deployment{ID: fmt.Sprintf("dep-%s", id)}, resp(http.StatusOK), nil
}

func (f *fakeAppsClient) Delete(_ context.Context, id string) (*godo.Response, error) {
	f.deleteCalls = append(f.deleteCalls, id)
	if err, ok := f.deleteErrs[id]; ok {
		return resp(http.StatusConflict), err
	}
	delete(f.getByID, id)
	return resp(http.StatusNoContent), nil
}

func (f *fakeAppsClient) ListDeployments(_ context.Context, id string, _ *godo.ListOptions) ([]*godo.Deployment, *godo.Response, error) {
	f.listDeploymentsFor = append(f.listDeploymentsFor, id)
	return nil, resp(http.StatusOK), nil
}

func (f *fakeAppsClient) GetLogs(_ context.Context, _ string, _ string, _ string, _ godo.AppLogType, _ bool, _ int) (*godo.AppLogs, *godo.Response, error) {
	return &godo.AppLogs{}, resp(http.StatusOK), nil
}

// Helper: canned godo.Response.
func resp(status int) *godo.Response {
	return &godo.Response{Response: &http.Response{StatusCode: status}}
}

// --- inMemoryState: minimal state-round-trip store ---

type inMemoryState struct {
	resources map[string]interfaces.ResourceState // keyed by Name
}

func newInMemoryState() *inMemoryState {
	return &inMemoryState{resources: map[string]interfaces.ResourceState{}}
}

func (s *inMemoryState) get(name string) (interfaces.ResourceState, bool) {
	r, ok := s.resources[name]
	return r, ok
}

func (s *inMemoryState) put(r interfaces.ResourceState) {
	s.resources[r.Name] = r
}

// --- applySim: mimic wfctl's apply→persist flow for tests ---

// applySim simulates the wfctl Apply pattern: for a spec, either Create (if
// no state) or Update (if state has ProviderID), then persist the returned
// ResourceOutput to the in-memory state.
func applySim(t *testing.T, ctx context.Context, driver *AppPlatformDriver, state *inMemoryState, spec interfaces.ResourceSpec) interfaces.ResourceState {
	t.Helper()

	existing, have := state.get(spec.Name)
	var out *interfaces.ResourceOutput
	var err error
	if !have {
		out, err = driver.Create(ctx, spec)
	} else {
		ref := interfaces.ResourceRef{
			Name:       existing.Name,
			Type:       existing.Type,
			ProviderID: existing.ProviderID,
		}
		out, err = driver.Update(ctx, ref, spec)
	}
	if err != nil {
		t.Fatalf("applySim: %v", err)
	}

	rs := interfaces.ResourceState{
		ID:         spec.Name,
		Name:       spec.Name,
		Type:       spec.Type,
		ProviderID: out.ProviderID, // ← the field under test
		Outputs:    out.Outputs,
		UpdatedAt:  time.Now().UTC(),
	}
	state.put(rs)
	return rs
}

// newTestDriver returns an AppPlatformDriver wired to a fakeAppsClient + default region.
func newTestDriver(t *testing.T) (*AppPlatformDriver, *fakeAppsClient) {
	t.Helper()
	client := newFakeAppsClient()
	return &AppPlatformDriver{client: client, region: "nyc3"}, client
}

// helpful: tests can use `strings.Contains(logCapture.String(), "state-heal")`
// once we add a log capture — see next task.
var _ = strings.Contains
```

**Step 2: Run (should compile but no tests yet)**

```bash
GOWORK=off go build ./internal/drivers/... && GOWORK=off go test -race -short ./internal/drivers/...
```
Expected: existing tests still pass. Harness compiles.

**Step 3: Commit**

```bash
git add internal/drivers/integration_test_helpers_test.go
git commit -m "test: integration harness for AppPlatformDriver (fakeAppsClient + inMemoryState)"
```

---

### Task 5: Integration tests for the state-heal behavior

**Files:**
- Create: `internal/drivers/app_platform_integration_test.go`

**Step 1: Write the tests — ALL MUST FAIL or cover edge cases matching the design**

Create `internal/drivers/app_platform_integration_test.go`:

```go
package drivers

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// TestAppPlatform_Create_PersistsUUIDInState asserts that Create returns a
// ResourceOutput with the UUID from the API response (never the name), and
// that the resulting ResourceState stored the UUID.
func TestAppPlatform_Create_PersistsUUIDInState(t *testing.T) {
	driver, client := newTestDriver(t)
	client.createResp = &godo.App{
		ID:   "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
		Spec: &godo.AppSpec{Name: "bmw-staging"},
	}
	state := newInMemoryState()

	rs := applySim(t, context.Background(), driver, state, interfaces.ResourceSpec{
		Name: "bmw-staging",
		Type: "infra.app_platform",
		Config: map[string]any{
			"region": "nyc3",
			"name":   "bmw-staging",
		},
	})

	if rs.ProviderID != "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5" {
		t.Errorf("state.ProviderID = %q, want UUID %q", rs.ProviderID, "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5")
	}
	if rs.ProviderID == "bmw-staging" {
		t.Errorf("state.ProviderID is the NAME %q — regression of the v0.7.8 bug class", rs.ProviderID)
	}
}

// TestAppPlatform_Update_UsesExistingUUID asserts that when state already
// has a valid UUID, Update uses it directly and does NOT invoke findAppByName.
func TestAppPlatform_Update_UsesExistingUUID(t *testing.T) {
	driver, client := newTestDriver(t)
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	existing := &godo.App{ID: uuid, Spec: &godo.AppSpec{Name: "bmw-staging"}}
	client.getByID[uuid] = existing
	client.updateResp[uuid] = existing

	state := newInMemoryState()
	state.put(interfaces.ResourceState{
		Name:       "bmw-staging",
		Type:       "infra.app_platform",
		ProviderID: uuid,
	})

	rs := applySim(t, context.Background(), driver, state, interfaces.ResourceSpec{
		Name: "bmw-staging",
		Type: "infra.app_platform",
		Config: map[string]any{"region": "nyc3", "name": "bmw-staging"},
	})

	// Update called with UUID exactly once.
	if len(client.updateCalls) != 1 || client.updateCalls[0] != uuid {
		t.Errorf("updateCalls = %v, want exactly [%q]", client.updateCalls, uuid)
	}
	// List (used by findAppByName) never called.
	if client.listCalls != 0 {
		t.Errorf("listCalls = %d, want 0 (heal should not fire for valid UUID)", client.listCalls)
	}
	if rs.ProviderID != uuid {
		t.Errorf("state.ProviderID = %q, want %q", rs.ProviderID, uuid)
	}
}

// TestAppPlatform_Update_HealsStaleName asserts that when state has a stale
// name-as-ProviderID, Update heals it: calls findAppByName to resolve UUID,
// uses UUID for the API call, returns ResourceOutput with the healed UUID
// so wfctl rewrites state.
func TestAppPlatform_Update_HealsStaleName(t *testing.T) {
	driver, client := newTestDriver(t)
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	const name = "bmw-staging"

	// Seed: fakeAppsClient has the real app (UUID id). State has the NAME.
	client.getByID[uuid] = &godo.App{ID: uuid, Spec: &godo.AppSpec{Name: name}}
	client.updateResp[uuid] = &godo.App{ID: uuid, Spec: &godo.AppSpec{Name: name}}

	state := newInMemoryState()
	state.put(interfaces.ResourceState{
		Name:       name,
		Type:       "infra.app_platform",
		ProviderID: name, // ← STALE: name substituted for UUID
	})

	// Capture WARN log so we can assert the heal fired.
	var logBuf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(oldOut)

	rs := applySim(t, context.Background(), driver, state, interfaces.ResourceSpec{
		Name: name,
		Type: "infra.app_platform",
		Config: map[string]any{"region": "nyc3", "name": name},
	})

	// List (via findAppByName) called at least once during heal.
	if client.listCalls < 1 {
		t.Errorf("expected findAppByName to be called (listCalls ≥ 1), got %d", client.listCalls)
	}
	// Update called with the real UUID, not the name.
	if len(client.updateCalls) != 1 || client.updateCalls[0] != uuid {
		t.Errorf("updateCalls = %v, want exactly [%q]", client.updateCalls, uuid)
	}
	// State was rewritten with the healed UUID.
	if rs.ProviderID != uuid {
		t.Errorf("state.ProviderID = %q after heal, want %q", rs.ProviderID, uuid)
	}
	// WARN log captured.
	if !strings.Contains(logBuf.String(), "state-heal") {
		t.Errorf("expected WARN log containing 'state-heal', got: %q", logBuf.String())
	}
}

// TestAppPlatform_Delete_HealsStaleName asserts the same heal on the Delete path.
func TestAppPlatform_Delete_HealsStaleName(t *testing.T) {
	driver, client := newTestDriver(t)
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	const name = "bmw-staging"

	client.getByID[uuid] = &godo.App{ID: uuid, Spec: &godo.AppSpec{Name: name}}

	err := driver.Delete(context.Background(), interfaces.ResourceRef{
		Name:       name,
		Type:       "infra.app_platform",
		ProviderID: name, // stale
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Delete called with the real UUID.
	if len(client.deleteCalls) != 1 || client.deleteCalls[0] != uuid {
		t.Errorf("deleteCalls = %v, want exactly [%q]", client.deleteCalls, uuid)
	}
	// List (via findAppByName) called during heal.
	if client.listCalls < 1 {
		t.Errorf("expected findAppByName to be called (listCalls ≥ 1), got %d", client.listCalls)
	}
}

// TestAppPlatform_Update_HealFails_WhenAppNotFound asserts that when the app
// genuinely doesn't exist by that name either, the heal returns a clear error
// instead of silently substituting the wrong ID.
func TestAppPlatform_Update_HealFails_WhenAppNotFound(t *testing.T) {
	driver, _ := newTestDriver(t)

	_, err := driver.Update(context.Background(), interfaces.ResourceRef{
		Name:       "ghost-app",
		Type:       "infra.app_platform",
		ProviderID: "ghost-app",
	}, interfaces.ResourceSpec{
		Name: "ghost-app",
		Type: "infra.app_platform",
		Config: map[string]any{"region": "nyc3", "name": "ghost-app"},
	})
	if err == nil {
		t.Fatal("expected error when heal can't find app by name, got nil")
	}
	if !strings.Contains(err.Error(), "state-heal") {
		t.Errorf("expected error mentioning 'state-heal', got: %v", err)
	}
}
```

**Step 2: Run — test for regression first**

```bash
GOWORK=off go test -race -short ./internal/drivers/... -run TestAppPlatform_ -v
```

Expected: all 5 tests pass against the state-heal committed in Task 1. If any test fails, adjust the tested code (not the test) until they all pass.

To be confident the test *would* have caught the regression, temporarily revert the heal (e.g., comment out `resolveProviderID` call in Update) and confirm `TestAppPlatform_Update_HealsStaleName` fails loudly. Restore and re-run — all green.

**Step 3: Commit**

```bash
git add internal/drivers/app_platform_integration_test.go
git commit -m "test: integration tests for app platform state-heal (bug would be caught)"
```

---

### Task 6: CHANGELOG v0.7.8 update

**Files:**
- Modify: `CHANGELOG.md`

**Step 1: Open existing CHANGELOG entry for v0.7.8 and extend**

Current v0.7.8 entry covers Troubleshoot. Add a "State-heal" subsection:

```markdown
## v0.7.8 — 2026-04-24

<existing Troubleshoot content>

### State-heal for stale ProviderIDs

- `AppPlatformDriver.Update` and `Delete` now validate the caller's
  `ResourceRef.ProviderID` shape before hitting the DO API. When the value
  is not a canonical UUID (e.g., legacy state that accidentally stored the
  resource name), the driver transparently falls back to `findAppByName` to
  resolve the real UUID, uses it for the API call, and returns
  `ResourceOutput.ProviderID` with the healed value so `wfctl` rewrites
  state on the next Apply.
- A WARN log is emitted when heal fires so operators can observe drift in
  CI output without the deploy failing.
- New shared helper `isUUIDLike(s string) bool` in `internal/drivers/shared.go`.
- New integration-test harness (`internal/drivers/integration_test_helpers_test.go`)
  exercises the full Create/Update loop against a fake `godo.AppsService`
  and in-memory state store. Pins down the "invalid uuid" regression class:
  if the Create path ever starts persisting a name instead of a UUID, or if
  Update stops healing stale names, the new tests fail loudly.
- Root-cause of tonight's BMW deploy failure documented in PR #22's description.

### Known follow-up (v0.7.9)

- Replicate state-heal across other UUID-ID drivers (database, cache,
  certificate, droplet, load balancer, VPC, firewall, reserved IP, API
  gateway). Same pattern, one commit per driver, parameterized over the
  integration-test harness.
```

**Step 2: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs: CHANGELOG v0.7.8 — add state-heal + integration test section"
```

---

### Task 7: Review + push + Copilot + merge approval

**Files:** none (process).

**Step 1: DM code-reviewer for LOCAL diff review**

Send via team SendMessage:
> PR #22 ready for LOCAL review — 6 commits on top of prior Troubleshoot work: isUUIDLike extract, state-heal commit, (optional) root-cause Create fix, integration harness, integration tests, CHANGELOG. Branch `feat/v0.7.8-troubleshoot`. Please verify: (a) tests actually would catch regression (try reverting heal to confirm), (b) no hidden scope creep.

Wait for approval.

**Step 2: Push**

```bash
git push origin feat/v0.7.8-troubleshoot
```

(force-push may be needed if rebase was done during Task 0; use `--force-with-lease`).

**Step 3: Re-request Copilot**

```bash
gh pr edit 22 --repo GoCodeAlone/workflow-plugin-digitalocean --add-reviewer copilot-pull-request-reviewer
```

**Step 4: Wait 15+ min for Copilot; address any comments via new commits + reply-on-thread**

If Copilot flags issues, address with additional commits. Never `@copilot` in comment bodies — team rule.

**Step 5: DM team-lead when ready for merge**

Send via team SendMessage:
> PR #22 ready for merge — all Copilot threads addressed, CI green, v0.7.8 scope complete (Troubleshoot + state-heal + integration tests).

---

### Task 8: Tag v0.7.8 (team-lead action)

**Files:** none.

**Step 1: After PR merges, team-lead:**

```bash
cd /Users/jon/workspace/workflow-plugin-digitalocean
git checkout main && git pull
git log --oneline -3 # verify merge commit
git tag -a v0.7.8 -m "v0.7.8: Troubleshoot interface + AppPlatform state-heal + integration tests"
git push origin v0.7.8
```

**Step 2: Verify release workflow fires**

```bash
gh run list --repo GoCodeAlone/workflow-plugin-digitalocean --workflow=Release --limit 1
```

**Step 3: After release assets publish, file the v0.7.9 follow-up task**

TaskCreate:
```
subject: DO plugin v0.7.9 — replicate state-heal across remaining UUID drivers
description: Generalize v0.7.8's AppPlatformDriver state-heal pattern to database, cache, certificate, droplet, load_balancer, vpc, firewall, reserved_ip, and api_gateway drivers. Each needs its own findByName-style helper (most have one from task #21 v0.7.3). Parameterize the integration-test harness to cover each driver with the same 5-test matrix (Create persists UUID, Update uses valid UUID, Update heals stale name, Delete heals stale name, Update fails clearly when app not found). Non-goals: DNS driver (uses domain name, not UUID).
```

---

## Dependency summary

```
Task 1, 2 (shared helper + commit heal)  ─────┐
Task 3 (root-cause)                       ────┼──► Task 7 (PR review cycle) ──► Task 8 (tag)
Task 4, 5 (harness + integration tests)   ────┤
Task 6 (CHANGELOG)                        ────┘

Task 0 (go.mod bump to v0.18.10.1) — parallel to Tasks 1-6, committed before push.
```

## Success criteria

- All 5 integration tests pass on the final HEAD.
- Temporarily reverting the heal makes `TestAppPlatform_Update_HealsStaleName` fail — proving the test catches the regression.
- `go test -race -short ./...` clean at merge time.
- CHANGELOG v0.7.8 reflects both Troubleshoot and state-heal deliverables.
- After v0.7.8 tags + BMW bumps pins, BMW deploy retries on the stale-state env, heal fires silently, deploy completes.

## Non-goals (explicit)

- wfctl-core generic `ValidateProviderID` hook — deferred; each driver owns its heal since ID format varies per provider.
- Other DO drivers' heal — task #69 follow-up for v0.7.9.
- `wfctl infra heal` command — not needed once driver-level heal exists.
- Retroactive state-file repair tool — first Update on stale state heals transparently; no offline tool needed.
- AWS / GCP / Azure replication — each provider's plugin audits its own drivers in its own repo.
