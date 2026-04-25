//go:build integration

package drivers

// Live integration test for trusted_sources app name→UUID resolution.
//
// Requirements:
//   DIGITALOCEAN_TOKEN  — personal access token with Apps read scope
//   DO_TEST_APP_NAME    — name of a pre-existing App Platform app in the account
//
// Both variables must be set or the test skips with t.Skip.
// The test is read-only: it never creates, modifies, or deletes any resource.
//
// Run manually:
//   DIGITALOCEAN_TOKEN=<tok> DO_TEST_APP_NAME=<name> \
//     GOWORK=off go test -v -tags integration ./internal/drivers/... \
//     -run TestDatabaseDriver_TrustedSources_AppNameResolution_Live

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/digitalocean/godo"
	"golang.org/x/oauth2"
)

// TestDatabaseDriver_TrustedSources_AppNameResolution_Live calls
// resolveAppNamesMap against the live DO Apps API and verifies that:
//  1. The function resolves DO_TEST_APP_NAME to a UUID without error.
//  2. The returned UUID is UUID-shaped (passes isUUIDLike).
//  3. The UUID matches what an independent Apps.List call returns for the same
//     app, confirming there is no off-by-one or pagination bug.
func TestDatabaseDriver_TrustedSources_AppNameResolution_Live(t *testing.T) {
	token := os.Getenv("DIGITALOCEAN_TOKEN")
	if token == "" {
		t.Skip("DIGITALOCEAN_TOKEN not set — skipping live integration test")
	}
	appName := os.Getenv("DO_TEST_APP_NAME")
	if appName == "" {
		t.Skip("DO_TEST_APP_NAME not set — skipping live integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	httpClient := oauth2.NewClient(ctx, ts)
	godoClient := godo.NewClient(httpClient)

	d := &DatabaseDriver{
		appsClient: godoClient.Apps,
		region:     "nyc3",
	}

	// ── 1. Resolve via the function under test ────────────────────────────────
	raw := []any{
		map[string]any{"type": "app", "value": appName},
	}
	resolved, err := d.resolveAppNamesMap(ctx, raw)
	if err != nil {
		t.Fatalf("resolveAppNamesMap(%q): %v", appName, err)
	}

	gotUUID, ok := resolved[appName]
	if !ok {
		t.Fatalf("resolveAppNamesMap result missing key %q; map: %v", appName, resolved)
	}
	if !isUUIDLike(gotUUID) {
		t.Errorf("resolved value %q for app %q does not look like a UUID", gotUUID, appName)
	}

	// ── 2. Independent cross-check via Apps.List ──────────────────────────────
	wantUUID := ""
	opts := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		apps, resp, listErr := godoClient.Apps.List(ctx, opts)
		if listErr != nil {
			t.Fatalf("Apps.List (cross-check): %v", listErr)
		}
		for _, app := range apps {
			if app.Spec != nil && app.Spec.Name == appName {
				wantUUID = app.ID
				break
			}
		}
		if wantUUID != "" || resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opts.Page++
	}
	if wantUUID == "" {
		t.Fatalf("app %q not found in Apps.List; verify DO_TEST_APP_NAME is correct", appName)
	}

	// ── 3. Assert resolved UUID == cross-checked UUID ─────────────────────────
	if gotUUID != wantUUID {
		t.Errorf("UUID mismatch for app %q:\n  resolveAppNamesMap → %q\n  Apps.List         → %q",
			appName, gotUUID, wantUUID)
	}
	t.Logf("✓ app %q resolved to UUID %q", appName, gotUUID)
}
