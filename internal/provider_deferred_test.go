package internal

// TDD test for DOProvider.Apply second-pass deferred flush.
//
// Regression gate: Apply must call FlushDeferredUpdates on any driver that
// accumulated pending updates during the main action loop. Without this, a
// DB created with deferred app-ref trusted_sources never gets the full rules
// applied, even after the app is provisioned in the same plan run.
//
// See docs/plans/2026-05-02-staging-deploy-blockers-design.md (Blocker 2).

import (
	"context"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// fakeAppForDeferred constructs a minimal *godo.App for deferred-test mocks.
func fakeAppForDeferred(name, id string) *godo.App {
	return &godo.App{ID: id, Spec: &godo.AppSpec{Name: name}}
}

// minimalDBMock satisfies drivers.DatabaseClient for the provider-level
// deferred flush test. Stores the last UpdateFirewallRules request so the
// test can assert the flush occurred.
type minimalDBMock struct {
	db              *godo.Database
	lastFirewallReq *godo.DatabaseUpdateFirewallRulesRequest
}

func (m *minimalDBMock) Create(_ context.Context, _ *godo.DatabaseCreateRequest) (*godo.Database, *godo.Response, error) {
	return m.db, nil, nil
}
func (m *minimalDBMock) Get(_ context.Context, _ string) (*godo.Database, *godo.Response, error) {
	return m.db, nil, nil
}
func (m *minimalDBMock) List(_ context.Context, _ *godo.ListOptions) ([]godo.Database, *godo.Response, error) {
	if m.db == nil {
		return nil, nil, nil
	}
	return []godo.Database{*m.db}, nil, nil
}
func (m *minimalDBMock) Resize(_ context.Context, _ string, _ *godo.DatabaseResizeRequest) (*godo.Response, error) {
	return nil, nil
}
func (m *minimalDBMock) Delete(_ context.Context, _ string) (*godo.Response, error) {
	return nil, nil
}
func (m *minimalDBMock) UpdateFirewallRules(_ context.Context, _ string, req *godo.DatabaseUpdateFirewallRulesRequest) (*godo.Response, error) {
	m.lastFirewallReq = req
	return nil, nil
}

// sequencedAppsForDeferred returns empty on the first call, then the app list.
// Simulates: DB create call (app absent) → flush call (app present).
type sequencedAppsForDeferred struct {
	apps      []*godo.App
	callCount int
}

func (m *sequencedAppsForDeferred) List(_ context.Context, _ *godo.ListOptions) ([]*godo.App, *godo.Response, error) {
	m.callCount++
	if m.callCount == 1 {
		return []*godo.App{}, nil, nil // first call: app doesn't exist yet
	}
	return m.apps, nil, nil
}

// TestDOProvider_Apply_FlushesDeferred_AfterAllCreates verifies the end-to-end
// deferred-update flow through DOProvider.Apply:
//
//  1. Plan has one "create" action for an infra.database with a type=app
//     trusted_sources ref.
//  2. During Create the app doesn't exist yet — driver defers.
//  3. After the main action loop, Apply calls FlushDeferredUpdates.
//  4. The flush calls UpdateFirewallRules with the full rule set (app UUID).
func TestDOProvider_Apply_FlushesDeferred_AfterAllCreates(t *testing.T) {
	const appUUID = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"

	dbMock := &minimalDBMock{db: &godo.Database{
		ID:   "db-abc",
		Name: "coredump-staging-db",
		Connection: &godo.DatabaseConnection{
			Host: "host.db.ondigitalocean.com",
			Port: 5432,
		},
	}}
	appsSeq := &sequencedAppsForDeferred{
		apps: []*godo.App{fakeAppForDeferred("coredump-staging", appUUID)},
	}
	dbDriver := drivers.NewDatabaseDriverWithClients(dbMock, appsSeq, "nyc3")

	p := &DOProvider{
		drivers: map[string]interfaces.ResourceDriver{
			"infra.database": dbDriver,
		},
	}

	plan := &interfaces.IaCPlan{
		ID: "test-plan",
		Actions: []interfaces.PlanAction{
			{
				Action: "create",
				Resource: interfaces.ResourceSpec{
					Name: "coredump-staging-db",
					Type: "infra.database",
					Config: map[string]any{
						"engine": "pg",
						"trusted_sources": []any{
							map[string]any{"type": "app", "value": "coredump-staging"},
						},
					},
				},
			},
		},
	}

	result, err := p.Apply(context.Background(), plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(result.Errors) > 0 {
		t.Fatalf("Apply result errors: %+v", result.Errors)
	}
	if len(result.Resources) != 1 {
		t.Fatalf("expected 1 created resource, got %d", len(result.Resources))
	}

	// The deferred flush should have called UpdateFirewallRules with app UUID.
	if dbMock.lastFirewallReq == nil {
		t.Fatal("UpdateFirewallRules was never called — deferred flush did not run")
	}
	if len(dbMock.lastFirewallReq.Rules) != 1 {
		t.Fatalf("expected 1 rule in deferred flush, got %d: %+v",
			len(dbMock.lastFirewallReq.Rules), dbMock.lastFirewallReq.Rules)
	}
	rule := dbMock.lastFirewallReq.Rules[0]
	if rule.Type != "app" || rule.Value != appUUID {
		t.Errorf("deferred flush rule = {%s %s}, want {app %s}", rule.Type, rule.Value, appUUID)
	}
}
