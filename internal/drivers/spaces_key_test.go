package drivers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
)

// TestSpacesKeyDriver_Create_HappyPath pins the engine-routing contract for
// SpacesKeyDriver.Create per workflow v0.27.0 + ADR 0015/0020:
//
//   - ResourceOutput.ProviderID == access_key (the godo identifier; matches
//     the sister bucket driver in spaces.go and the EnumerateAll path in
//     internal/provider.go::enumerateAllSpacesKeys).
//   - Outputs contains EVERY field including access_key AND secret_key —
//     the workflow engine's iac/sensitive package routes them through the
//     configured secrets.Provider before persisting state. The driver is
//     platform-agnostic: it does NOT touch secrets.Provider itself.
//   - Sensitive["access_key"]==true and Sensitive["secret_key"]==true —
//     these flags drive the engine's per-call routing.
//   - Sensitive["created_at"] is unset/false — created_at is metadata, not
//     a secret.
//   - SensitiveKeys() returns both "access_key" and "secret_key" — used by
//     wfctl plan/diff display masking, separate from per-call routing.
//
// The test also validates the request payload reaching DO so the spec→godo
// translation (name + grants) is pinned, and returns 500 on any unexpected
// path so godo-level errors surface deterministically.
func TestSpacesKeyDriver_Create_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/spaces/keys" && r.Method == http.MethodPost {
			// Validate the request payload — the driver must translate
			// spec.Config{name, grants} into the godo.SpacesKeyCreateRequest
			// faithfully. Without this assertion, the test would still pass
			// even if Create dropped/garbled the spec.
			var got godo.SpacesKeyCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
				return
			}
			if got.Name != "test-key" {
				http.Error(w, "request name="+got.Name+" want test-key", http.StatusBadRequest)
				return
			}
			if len(got.Grants) != 1 || got.Grants[0] == nil ||
				got.Grants[0].Permission != godo.SpacesKeyFullAccess {
				http.Error(w, "request grants mismatch", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"key": map[string]any{
					"name":       "test-key",
					"access_key": "AKIATEST123",
					"secret_key": "SK_secret_data_here",
					"created_at": "2026-05-08T11:00:00Z",
					"grants":     []any{map[string]any{"permission": "fullaccess"}},
				},
			})
			return
		}
		// Unexpected path — return 500 so godo surfaces it as an HTTP error
		// rather than the test silently waiting for a response that never
		// matches.
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected path", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := godoClientForTest(t, srv)

	driver := drivers.NewSpacesKeyDriver(client)
	spec := interfaces.ResourceSpec{
		Name: "test-key",
		Type: "infra.spaces_key",
		Config: map[string]any{
			"name":   "test-key",
			"grants": []any{map[string]any{"permission": "fullaccess"}},
		},
	}

	out, err := driver.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// ProviderID == access_key (the godo identifier; matches sister bucket
	// driver pattern at internal/drivers/spaces.go).
	if out.ProviderID != "AKIATEST123" {
		t.Errorf("ProviderID: want AKIATEST123 (access_key as godo ID), got %q", out.ProviderID)
	}

	// All four output fields populated — the engine routes Sensitive ones
	// through secrets.Provider after this method returns.
	if got, _ := out.Outputs["access_key"].(string); got != "AKIATEST123" {
		t.Errorf("access_key: want AKIATEST123, got %v", out.Outputs["access_key"])
	}
	if got, _ := out.Outputs["secret_key"].(string); got != "SK_secret_data_here" {
		t.Errorf("secret_key: want SK_secret_data_here (engine routes after return), got %v", out.Outputs["secret_key"])
	}
	if got, _ := out.Outputs["created_at"].(string); got != "2026-05-08T11:00:00Z" {
		t.Errorf("created_at: want 2026-05-08T11:00:00Z, got %v", out.Outputs["created_at"])
	}
	if _, ok := out.Outputs["name"]; !ok {
		t.Errorf("name MUST be in Outputs; got %v", out.Outputs)
	}

	// Sensitive flags MUST mark both access_key and secret_key — the
	// engine's iac/sensitive.Route consults this map per-call to decide
	// what to push through secrets.Provider.
	if !out.Sensitive["access_key"] {
		t.Errorf("Sensitive[access_key]: want true, got %v", out.Sensitive["access_key"])
	}
	if !out.Sensitive["secret_key"] {
		t.Errorf("Sensitive[secret_key]: want true, got %v", out.Sensitive["secret_key"])
	}
	// created_at is metadata, not sensitive — must NOT be flagged.
	if out.Sensitive["created_at"] {
		t.Errorf("Sensitive[created_at]: want false/unset, got true")
	}

	// SensitiveKeys (display masking) must include both keys — distinct
	// from the per-call Sensitive map but parallel in content here.
	wantKeys := map[string]bool{"access_key": true, "secret_key": true}
	gotKeys := map[string]bool{}
	for _, k := range driver.SensitiveKeys() {
		gotKeys[k] = true
	}
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Errorf("SensitiveKeys: want %v, got %v", wantKeys, gotKeys)
	}
}

// godoClientForTest builds a godo client whose BaseURL points at the given
// httptest server and whose underlying http.Client is the test server's own
// client. Mirrors `internal/provider_enumerator_test.go::newProviderForEnumeratorTest`
// (line 149: `godo.NewClient(srv.Client())`). Using `srv.Client()` rather than
// `http.DefaultClient` keeps the test hermetic — every request rides the
// test server's transport, so an ambient transport mutation in another test
// can't leak in.
func godoClientForTest(t *testing.T, srv *httptest.Server) *godo.Client {
	t.Helper()
	client := godo.NewClient(srv.Client())
	base, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatalf("parse httptest URL %q: %v", srv.URL, err)
	}
	client.BaseURL = base
	return client
}
