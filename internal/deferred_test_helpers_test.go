package internal

// Test-only fixtures for the deferred-update flush path. Used by
// iacserver_finalize_test.go (workflow#695 Phase 2.5 FinalizeApply
// regression suite). Extracted from provider_deferred_test.go in the
// Phase 3 cleanup that deleted the v1-wrapper DOProvider.Apply tests
// (docs/plans/2026-05-17-phase2.5-cleanup-bundle.md Task 5) — the
// fixtures themselves remain in-package so the v2-dispatch tests
// continue to exercise the canonical DatabaseDriver mock surface
// without coupling to the (now-stub) v1 Apply wrapper.

import (
	"context"

	"github.com/digitalocean/godo"
)

// fakeAppForDeferred constructs a minimal *godo.App for deferred-test mocks.
func fakeAppForDeferred(name, id string) *godo.App {
	return &godo.App{ID: id, Spec: &godo.AppSpec{Name: name}}
}

// minimalDBMock satisfies drivers.DatabaseClient for the FinalizeApply
// deferred-flush tests. Stores the last UpdateFirewallRules request so
// the test can assert the flush occurred. The optional flushErr field —
// when non-nil — is returned from UpdateFirewallRules so tests can
// exercise the per-driver error-attribution path (workflow#695 Phase 2.5
// FinalizeApply regression test).
type minimalDBMock struct {
	db              *godo.Database
	lastFirewallReq *godo.DatabaseUpdateFirewallRulesRequest
	flushErr        error
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
	if m.flushErr != nil {
		return nil, m.flushErr
	}
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
