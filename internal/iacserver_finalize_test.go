package internal

// TDD regression gate for workflow#695 Phase 2.5: FinalizeApply MUST fire
// FlushDeferredUpdates on every driver that holds queued state, AND MUST
// preserve per-driver error attribution (Resource/Action/Error shape) when
// a driver's flush fails. Mirrors the v1 wrapper coverage in
// provider_deferred_test.go but exercises the v2 dispatch path via the
// gRPC server's FinalizeApply method.
//
// Coverage:
//   - TestDOIaCServer_FinalizeApply_FlushesDeferredUpdates — happy path:
//     queued deferred update flushes through FinalizeApply; resp.errors is
//     empty; UpdateFirewallRules received the full rule set.
//   - TestDOIaCServer_FinalizeApply_PreservesPerDriverErrorAttribution —
//     failure path: driver flush returns error; FinalizeApply itself
//     succeeds (per the wire-status invariant — gRPC OK with per-driver
//     errors in response.errors[]); resp.errors carries the v1 wrapper's
//     ActionError{Resource:"infra.database", Action:"deferred_update",
//     Error: <flushErr.Error()>} attribution shape.
//
// Fixtures reuse fakeAppForDeferred + minimalDBMock from
// provider_deferred_test.go (same package); the new flushErr field on
// minimalDBMock is the only mock-struct extension. Two new setup helpers
// (extracted from the existing TestDOProvider_Apply_FlushesDeferred_*
// fixture patterns) seed a deferred update via direct dbDriver.Create
// call so the test path is independent of DOProvider.Apply (which is the
// v1 path under cleanup post-Phase-2.5).

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	"github.com/digitalocean/godo"
)

const finalizeTestAppUUID = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"

// setupProviderWithQueuedDBDeferredUpdate constructs a minimal DOProvider
// whose database driver has one queued deferred update (trusted_sources
// app-ref → UUID resolution). Extracted from the
// TestDOProvider_Apply_FlushesDeferred_WhenTypeAbsentFromPlan fixture
// pattern to keep the v2-dispatch FinalizeApply tests independent of the
// v1 Apply wrapper path.
func setupProviderWithQueuedDBDeferredUpdate(t *testing.T) (*DOProvider, *minimalDBMock) {
	t.Helper()
	dbMock := &minimalDBMock{db: &godo.Database{
		ID:   "db-abc",
		Name: "coredump-staging-db",
		Connection: &godo.DatabaseConnection{
			Host: "host.db.ondigitalocean.com",
			Port: 5432,
		},
	}}
	// First Apps.List (during seed Create) returns empty → driver queues
	// the deferred update. Subsequent calls (during FinalizeApply's
	// FlushDeferredUpdates) return the app so UUID resolution succeeds.
	appSeq := &sequencedAppsForDeferred{
		apps: []*godo.App{fakeAppForDeferred("coredump-staging", finalizeTestAppUUID)},
	}
	dbDriver := drivers.NewDatabaseDriverWithClients(dbMock, appSeq, "nyc3")

	// Seed via direct Create — first Apps.List returns empty so the
	// driver queues a deferred trusted_sources update for the app ref.
	if _, err := dbDriver.Create(context.Background(), interfaces.ResourceSpec{
		Name: "coredump-staging-db",
		Config: map[string]any{
			"engine": "pg",
			"trusted_sources": []any{
				map[string]any{"type": "app", "value": "coredump-staging"},
			},
		},
	}); err != nil {
		t.Fatalf("seed Create: %v", err)
	}
	if !dbDriver.HasDeferredUpdates() {
		t.Fatal("test fixture invariant: expected deferred update queued after seed Create")
	}
	return &DOProvider{
		drivers: map[string]interfaces.ResourceDriver{
			"infra.database": dbDriver,
		},
	}, dbMock
}

// setupProviderWithFailingDBDeferredUpdate is the failure-injection
// counterpart of setupProviderWithQueuedDBDeferredUpdate. Sets the new
// minimalDBMock.flushErr field so UpdateFirewallRules returns the
// configured error when FinalizeApply triggers FlushDeferredUpdates.
// Returns the sentinel error too so tests can assert it round-trips
// into resp.Errors[0].Error.
func setupProviderWithFailingDBDeferredUpdate(t *testing.T) (*DOProvider, *minimalDBMock, error) {
	t.Helper()
	provider, dbMock := setupProviderWithQueuedDBDeferredUpdate(t)
	dbMock.flushErr = errors.New("update firewall rules failed")
	return provider, dbMock, dbMock.flushErr
}

// TestDOIaCServer_FinalizeApply_FlushesDeferredUpdates is the happy-path
// regression gate. Without FinalizeApply firing the per-driver flush
// loop, v2-dispatched plans would silently lose deferred trusted_sources
// resolution — the exact regression workflow#695 Phase 2.5 closes.
func TestDOIaCServer_FinalizeApply_FlushesDeferredUpdates(t *testing.T) {
	provider, dbMock := setupProviderWithQueuedDBDeferredUpdate(t)
	s := newDOIaCServer(provider)

	resp, err := s.FinalizeApply(context.Background(), &pb.FinalizeApplyRequest{PlanId: "test-plan-success"})

	if err != nil {
		t.Fatalf("FinalizeApply returned gRPC error (should be in resp.Errors, not top-level): %v", err)
	}
	if len(resp.GetErrors()) != 0 {
		t.Errorf("expected no per-driver errors; got: %+v", resp.GetErrors())
	}
	if dbMock.lastFirewallReq == nil {
		t.Fatal("UpdateFirewallRules was never called — deferred flush did not run under v2 dispatch via FinalizeApply")
	}
	if len(dbMock.lastFirewallReq.Rules) != 1 {
		t.Fatalf("expected 1 rule in deferred flush, got %d: %+v",
			len(dbMock.lastFirewallReq.Rules), dbMock.lastFirewallReq.Rules)
	}
	rule := dbMock.lastFirewallReq.Rules[0]
	if rule.Type != "app" || rule.Value != finalizeTestAppUUID {
		t.Errorf("deferred flush rule = {%s %s}, want {app %s}", rule.Type, rule.Value, finalizeTestAppUUID)
	}
}

// TestDOIaCServer_FinalizeApply_PreservesPerDriverErrorAttribution
// is the failure-path regression gate. Locks the wire contract that
// per-driver flush failures surface via response.errors[] (gRPC OK
// status), preserving the v1 wrapper's
// ActionError{Resource, Action, Error} attribution shape so the
// wfctl-side OnPlanComplete handler can render the operator-facing
// diagnostic distinctly per driver. Per ADR 0040 invariants 1 + 2.
func TestDOIaCServer_FinalizeApply_PreservesPerDriverErrorAttribution(t *testing.T) {
	provider, dbMock, sentinel := setupProviderWithFailingDBDeferredUpdate(t)
	s := newDOIaCServer(provider)

	resp, err := s.FinalizeApply(context.Background(), &pb.FinalizeApplyRequest{PlanId: "test-plan-fail"})

	// Belt+suspenders — the error round-trip below implicitly proves the
	// flush chain ran, but locking lastFirewallReq != nil directly guards
	// against a future refactor where the mock's UpdateFirewallRules
	// short-circuits before recording the request (e.g., if flushErr is
	// moved to a pre-call gate). The contract under test is "flush was
	// attempted AND failed"; the implicit-only assert would mask a
	// regression that drops the attempt.
	if dbMock.lastFirewallReq == nil {
		t.Fatal("UpdateFirewallRules was not attempted — failure-path coverage degraded to flush-never-fired")
	}

	// Wire-status invariant (FinalizeApplyResponse godoc, mirrors
	// ADR 0040 inv 2): per-driver errors live in response.errors[];
	// gRPC status MUST be OK. A non-OK status would signal transport
	// failure distinct from per-driver finalize errors.
	if err != nil {
		t.Fatalf("FinalizeApply gRPC error (should be in resp.Errors, not top-level): %v", err)
	}
	if len(resp.GetErrors()) != 1 {
		t.Fatalf("expected 1 ActionError in resp; got %d: %+v", len(resp.GetErrors()), resp.GetErrors())
	}
	got := resp.GetErrors()[0]
	if got.GetResource() != "infra.database" {
		t.Errorf("expected Resource=%q; got %q", "infra.database", got.GetResource())
	}
	if got.GetAction() != "deferred_update" {
		t.Errorf("expected Action=%q; got %q", "deferred_update", got.GetAction())
	}
	// Driver wraps flush failures with a "deferred firewall update <name>:"
	// prefix before bubbling out of FlushDeferredUpdates; assert substring
	// presence rather than full equality so the per-driver attribution
	// shape is locked without coupling to the driver-side prefix wording.
	// strings.Contains also survives the response.errors[] map-iteration
	// order non-determinism if multiple drivers fail in a future test.
	if !strings.Contains(got.GetError(), sentinel.Error()) {
		t.Errorf("expected Error to contain %q; got %q", sentinel.Error(), got.GetError())
	}
}

// TestDOIaCServer_Capabilities_DeclaresV2 locks the load-bearing
// ComputePlanVersion="v2" opt-in literal in the Capabilities RPC
// return. wfctl's DispatchVersionFor reads this string to route the
// plugin through the v2 apply path (wfctlhelpers.ApplyPlan + per-action
// hooks); a future refactor accidentally dropping the literal (or a
// struct re-init forgetting to set it) silently reverts the plugin to
// v1 dispatch — and the compile-time assert can't catch a string
// omission. Per workflow#695 Phase 2.5.
func TestDOIaCServer_Capabilities_DeclaresV2(t *testing.T) {
	s := newDOIaCServer(&DOProvider{})
	caps, err := s.Capabilities(context.Background(), &pb.CapabilitiesRequest{})
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if got := caps.GetComputePlanVersion(); got != "v2" {
		t.Errorf("CapabilitiesResponse.ComputePlanVersion = %q; want %q (v2 dispatch opt-in lost)", got, "v2")
	}
}
