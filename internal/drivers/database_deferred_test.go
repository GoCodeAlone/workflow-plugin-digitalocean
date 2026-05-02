package drivers_test

// TDD tests for the DatabaseDriver deferred trusted_sources update path.
//
// When a type=app trusted_sources entry references an app that doesn't exist
// yet at create/update time, the driver must:
//   (1) create/update the DB without the unresolvable app rule,
//   (2) queue a deferred firewall update,
//   (3) apply the full rules (including app UUID) when FlushDeferredUpdates
//       is called after the app has been provisioned.
//
// Regression gate for GoCodeAlone/core-dump#154 (R4 finding) +
// GoCodeAlone/core-dump#158 (first-deploy ordering).
// See docs/plans/2026-05-02-staging-deploy-blockers-design.md (Blocker 2).

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// countedAppClient returns an empty app list for the first `emptyFor` calls,
// then returns `apps` on all subsequent calls. Used to simulate the
// first-deploy ordering: app doesn't exist during DB create, exists during
// FlushDeferredUpdates.
type countedAppClient struct {
	apps      []*godo.App
	emptyFor  int
	callCount int
}

func (m *countedAppClient) List(_ context.Context, _ *godo.ListOptions) ([]*godo.App, *godo.Response, error) {
	m.callCount++
	if m.callCount <= m.emptyFor {
		return []*godo.App{}, nil, nil
	}
	return m.apps, nil, nil
}

// --- Create deferral tests ---

// TestDatabaseDriver_Create_AppNotYetExisting_DefersAndSucceeds verifies that
// when a type=app trusted_source can't be resolved (app not yet created),
// Create succeeds, the DB is created without the app rule, and a deferred
// firewall update is queued.
func TestDatabaseDriver_Create_AppNotYetExisting_DefersAndSucceeds(t *testing.T) {
	dbMock := &mockDatabaseClient{db: testDatabase()}
	// App not found — empty list on first call.
	appMock := &mockAppClient{listApps: []*godo.App{}}
	d := drivers.NewDatabaseDriverWithClients(dbMock, appMock, "nyc3")

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-db",
		Config: map[string]any{
			"engine": "pg",
			"trusted_sources": []any{
				map[string]any{"type": "app", "value": "coredump-staging"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create should succeed even when app ref is not yet resolvable; got: %v", err)
	}
	if out == nil || out.ProviderID != "db-123" {
		t.Fatalf("Create should return DB output; got %+v", out)
	}
	if dbMock.lastCreateReq == nil {
		t.Fatal("no create request captured")
	}
	if len(dbMock.lastCreateReq.Rules) != 0 {
		t.Fatalf("create request should have no rules when app ref is deferred; got %d rules: %+v",
			len(dbMock.lastCreateReq.Rules), dbMock.lastCreateReq.Rules)
	}
	if !d.HasDeferredUpdates() {
		t.Error("HasDeferredUpdates() should be true after deferred create")
	}
}

// TestDatabaseDriver_Create_AppNotYetExisting_MixedRules verifies that when
// a spec has both ip_addr and type=app rules and the app is not found, the
// DB is created with the resolvable ip_addr rule and the app rule is deferred.
func TestDatabaseDriver_Create_AppNotYetExisting_MixedRules(t *testing.T) {
	dbMock := &mockDatabaseClient{db: testDatabase()}
	appMock := &mockAppClient{listApps: []*godo.App{}} // app not found
	d := drivers.NewDatabaseDriverWithClients(dbMock, appMock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-db",
		Config: map[string]any{
			"engine": "pg",
			"trusted_sources": []any{
				map[string]any{"type": "ip_addr", "value": "10.20.0.0/16"},
				map[string]any{"type": "app", "value": "coredump-staging"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create with mixed rules should succeed even when app not found; got: %v", err)
	}
	if dbMock.lastCreateReq == nil {
		t.Fatal("no create request captured")
	}
	// Only the ip_addr rule should be in the create request.
	if len(dbMock.lastCreateReq.Rules) != 1 {
		t.Fatalf("expected 1 rule in create request (ip_addr only), got %d: %+v",
			len(dbMock.lastCreateReq.Rules), dbMock.lastCreateReq.Rules)
	}
	if dbMock.lastCreateReq.Rules[0].Type != "ip_addr" || dbMock.lastCreateReq.Rules[0].Value != "10.20.0.0/16" {
		t.Errorf("rule[0] = {%s %s}, want {ip_addr 10.20.0.0/16}",
			dbMock.lastCreateReq.Rules[0].Type, dbMock.lastCreateReq.Rules[0].Value)
	}
	if !d.HasDeferredUpdates() {
		t.Error("HasDeferredUpdates() should be true after deferred create")
	}
}

// TestDatabaseDriver_Create_AppAPIFailure_NotDeferred verifies that when the
// DO Apps.List call itself fails (transient, auth, rate-limit), Create fails
// with the original error and does NOT silently defer. The caller must know
// that the DB was not created under its intended security constraints.
func TestDatabaseDriver_Create_AppAPIFailure_NotDeferred(t *testing.T) {
	dbMock := &mockDatabaseClient{db: testDatabase()}
	// Apps.List returns a hard API error — must NOT be treated as deferral.
	appMock := &mockAppClient{listErr: fmt.Errorf("DO API: connection reset")}
	d := drivers.NewDatabaseDriverWithClients(dbMock, appMock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-db",
		Config: map[string]any{
			"engine": "pg",
			"trusted_sources": []any{
				map[string]any{"type": "app", "value": "coredump-staging"},
			},
		},
	})
	if err == nil {
		t.Fatal("Create should fail when Apps.List returns an API error")
	}
	if d.HasDeferredUpdates() {
		t.Error("HasDeferredUpdates() must be false when failure is an API error, not app-not-found")
	}
	// API failure error must NOT wrap ErrAppNotFound — only app-not-found errors
	// are deferral-eligible.
	if errors.Is(err, drivers.ErrAppNotFound) {
		t.Errorf("error from API failure must not wrap ErrAppNotFound; got: %v", err)
	}
}

// --- Update deferral tests ---

// TestDatabaseDriver_Update_AppNotYetExisting_DefersAndAppliesPartialRules
// verifies the same deferral behaviour on the Update path: when a type=app
// trusted_source can't be resolved, Update applies the resolvable rules
// immediately and queues a deferred update for the full rule set.
func TestDatabaseDriver_Update_AppNotYetExisting_DefersAndAppliesPartialRules(t *testing.T) {
	dbMock := &mockDatabaseClient{db: testDatabase()}
	appMock := &mockAppClient{listApps: []*godo.App{}} // app not found
	d := drivers.NewDatabaseDriverWithClients(dbMock, appMock, "nyc3")

	_, err := d.Update(context.Background(),
		interfaces.ResourceRef{Name: "my-db", ProviderID: "db-123"},
		interfaces.ResourceSpec{
			Name: "my-db",
			Config: map[string]any{
				"size": "db-s-1vcpu-1gb",
				"trusted_sources": []any{
					map[string]any{"type": "ip_addr", "value": "10.20.0.0/16"},
					map[string]any{"type": "app", "value": "coredump-staging"},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("Update should succeed even when app ref is not yet resolvable; got: %v", err)
	}
	// Partial rules (ip_addr only) should have been applied immediately.
	if dbMock.lastFirewallReq == nil {
		t.Fatal("UpdateFirewallRules should be called for partial rules")
	}
	if len(dbMock.lastFirewallReq.Rules) != 1 {
		t.Fatalf("expected 1 partial rule (ip_addr only), got %d: %+v",
			len(dbMock.lastFirewallReq.Rules), dbMock.lastFirewallReq.Rules)
	}
	if dbMock.lastFirewallReq.Rules[0].Type != "ip_addr" {
		t.Errorf("partial rule should be ip_addr, got %q", dbMock.lastFirewallReq.Rules[0].Type)
	}
	if !d.HasDeferredUpdates() {
		t.Error("HasDeferredUpdates() should be true after deferred update")
	}
}

// --- FlushDeferredUpdates tests ---

// TestDatabaseDriver_FlushDeferredUpdates_FullRulesApplied verifies that after
// a deferred create, FlushDeferredUpdates calls UpdateFirewallRules with the
// full rule set (including app UUID) once the app is available.
func TestDatabaseDriver_FlushDeferredUpdates_FullRulesApplied(t *testing.T) {
	const appUUID = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	dbMock := &mockDatabaseClient{db: testDatabase()}
	// First call returns empty (create time); second+ returns app (flush time).
	appMock := &countedAppClient{
		apps:     []*godo.App{makeAppForTrustedSource("coredump-staging", appUUID)},
		emptyFor: 1, // first call (during Create) returns empty
	}
	d := drivers.NewDatabaseDriverWithClients(dbMock, appMock, "nyc3")

	// Create triggers deferred path.
	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-db",
		Config: map[string]any{
			"engine": "pg",
			"trusted_sources": []any{
				map[string]any{"type": "app", "value": "coredump-staging"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !d.HasDeferredUpdates() {
		t.Fatal("expected deferred update queued after create")
	}

	// Flush — app now exists.
	if err := d.FlushDeferredUpdates(context.Background()); err != nil {
		t.Fatalf("FlushDeferredUpdates: %v", err)
	}

	// UpdateFirewallRules should have been called with the app UUID.
	if dbMock.lastFirewallReq == nil {
		t.Fatal("UpdateFirewallRules was not called during flush")
	}
	if len(dbMock.lastFirewallReq.Rules) != 1 {
		t.Fatalf("expected 1 rule after flush, got %d: %+v",
			len(dbMock.lastFirewallReq.Rules), dbMock.lastFirewallReq.Rules)
	}
	rule := dbMock.lastFirewallReq.Rules[0]
	if rule.Type != "app" || rule.Value != appUUID {
		t.Errorf("flush rule = {%s %s}, want {app %s}", rule.Type, rule.Value, appUUID)
	}
	// After flush, deferred queue is cleared.
	if d.HasDeferredUpdates() {
		t.Error("HasDeferredUpdates() should be false after successful flush")
	}
}

// TestDatabaseDriver_FlushDeferredUpdates_APIError_RetainsQueue verifies that
// when UpdateFirewallRules fails during flush, FlushDeferredUpdates returns an
// error AND retains the failed entry in the pending queue so a subsequent
// Apply (once the API is reachable) will automatically re-attempt the flush
// without requiring operator intervention (e.g. touching a config field to
// force a Diff update).
//
// This is the correct behaviour: silently clearing the queue on failure would
// leave the database permanently stuck with partial rules because DatabaseDriver.Diff
// does not compare trusted_sources, so a retry Apply would produce no update
// action and the second-pass flush would never fire.
func TestDatabaseDriver_FlushDeferredUpdates_APIError_RetainsQueue(t *testing.T) {
	const appUUID = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	dbMock := &mockDatabaseClient{
		db:          testDatabase(),
		firewallErr: fmt.Errorf("UpdateFirewallRules: service unavailable"),
	}
	appMock := &countedAppClient{
		apps:     []*godo.App{makeAppForTrustedSource("coredump-staging", appUUID)},
		emptyFor: 1,
	}
	d := drivers.NewDatabaseDriverWithClients(dbMock, appMock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-db",
		Config: map[string]any{
			"engine": "pg",
			"trusted_sources": []any{
				map[string]any{"type": "app", "value": "coredump-staging"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := d.FlushDeferredUpdates(context.Background()); err == nil {
		t.Fatal("FlushDeferredUpdates should return error when UpdateFirewallRules fails")
	}
	// Failed entry must be retained so a retry Apply can re-attempt the flush.
	if !d.HasDeferredUpdates() {
		t.Error("HasDeferredUpdates() must be true after failed flush — retry Apply must re-attempt without operator intervention")
	}
}

// TestDatabaseDriver_HasDeferredUpdates_FalseWhenNone verifies baseline:
// a freshly-created driver with no deferred state reports false.
func TestDatabaseDriver_HasDeferredUpdates_FalseWhenNone(t *testing.T) {
	d := drivers.NewDatabaseDriverWithClient(&mockDatabaseClient{}, "nyc3")
	if d.HasDeferredUpdates() {
		t.Error("HasDeferredUpdates() should be false for a fresh driver")
	}
}

// TestDatabaseDriver_FlushDeferredUpdates_NoopWhenEmpty verifies that calling
// FlushDeferredUpdates on a driver with no pending updates returns nil.
func TestDatabaseDriver_FlushDeferredUpdates_NoopWhenEmpty(t *testing.T) {
	d := drivers.NewDatabaseDriverWithClient(&mockDatabaseClient{}, "nyc3")
	if err := d.FlushDeferredUpdates(context.Background()); err != nil {
		t.Fatalf("FlushDeferredUpdates on empty driver should return nil; got: %v", err)
	}
}
