package drivers_test

import (
	"context"
	"encoding/json"
	"errors"
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

// TestSpacesKeyDriver_Read_HappyPath pins the Read contract: with a non-empty
// ProviderID, the driver issues GET /v2/spaces/keys/{access_key} and returns
// the metadata fields ONLY — secret_key MUST NOT appear in Outputs (the DO
// API never re-emits it after Create), and Sensitive[secret_key] MUST be
// unset/false. access_key remains flagged sensitive for masking only.
func TestSpacesKeyDriver_Read_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/spaces/keys/AKIATEST123" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"key": map[string]any{
					"name":       "test-key",
					"access_key": "AKIATEST123",
					"created_at": "2026-05-08T11:00:00Z",
					"grants":     []any{map[string]any{"permission": "fullaccess", "bucket": ""}},
				},
			})
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected path", http.StatusInternalServerError)
	}))
	defer srv.Close()

	driver := drivers.NewSpacesKeyDriver(godoClientForTest(t, srv))
	out, err := driver.Read(context.Background(), interfaces.ResourceRef{
		Name:       "test-key",
		Type:       "infra.spaces_key",
		ProviderID: "AKIATEST123",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.ProviderID != "AKIATEST123" {
		t.Errorf("ProviderID: want AKIATEST123, got %q", out.ProviderID)
	}
	if name, _ := out.Outputs["name"].(string); name != "test-key" {
		t.Errorf("Outputs[name]: want test-key, got %v", out.Outputs["name"])
	}
	if ak, _ := out.Outputs["access_key"].(string); ak != "AKIATEST123" {
		t.Errorf("Outputs[access_key]: want AKIATEST123, got %v", out.Outputs["access_key"])
	}
	if ca, _ := out.Outputs["created_at"].(string); ca != "2026-05-08T11:00:00Z" {
		t.Errorf("Outputs[created_at]: want 2026-05-08T11:00:00Z, got %v", out.Outputs["created_at"])
	}
	if _, ok := out.Outputs["grants"]; !ok {
		t.Errorf("Outputs[grants]: want present, got absent")
	}
	// secret_key MUST NOT appear in Outputs on Read — the API never re-emits it.
	if _, ok := out.Outputs["secret_key"]; ok {
		t.Errorf("Outputs[secret_key]: must be absent on Read; got %v", out.Outputs["secret_key"])
	}
	// Sensitive[secret_key] must be unset/false on Read (only access_key is flagged).
	if out.Sensitive["secret_key"] {
		t.Errorf("Sensitive[secret_key]: must be unset/false on Read, got true")
	}
	if !out.Sensitive["access_key"] {
		t.Errorf("Sensitive[access_key]: want true (display masking), got false")
	}
}

// TestSpacesKeyDriver_Read_NotFound asserts that a 404 from godo Get is
// converted to interfaces.ErrResourceNotFound (via WrapGodoError →
// sentinelForStatus mapping) so callers can errors.Is for typed handling.
func TestSpacesKeyDriver_Read_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/spaces/keys/AK_GONE" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"id":"not_found","message":"key not found"}`))
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected path", http.StatusInternalServerError)
	}))
	defer srv.Close()

	driver := drivers.NewSpacesKeyDriver(godoClientForTest(t, srv))
	_, err := driver.Read(context.Background(), interfaces.ResourceRef{
		Name:       "test-key",
		ProviderID: "AK_GONE",
	})
	if err == nil {
		t.Fatal("Read: expected ErrResourceNotFound, got nil")
	}
	if !errors.Is(err, interfaces.ErrResourceNotFound) {
		t.Errorf("Read: want errors.Is(err, ErrResourceNotFound), got %v", err)
	}
}

// TestSpacesKeyDriver_Read_FindByName covers the upsert / state-heal fallback:
// when ResourceRef.ProviderID is empty, Read paginates the List endpoint and
// matches by Name. The httptest server serves two pages so the pagination
// loop in findByName is exercised.
func TestSpacesKeyDriver_Read_FindByName(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/spaces/keys" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			page := r.URL.Query().Get("page")
			if page == "" || page == "1" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"keys": []any{
						map[string]any{"name": "key-other", "access_key": "AK_OTHER", "created_at": "2026-05-01T00:00:00Z"},
					},
					"links": map[string]any{
						"pages": map[string]any{"next": srv.URL + "/v2/spaces/keys?page=2"},
					},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"keys": []any{
					map[string]any{
						"name":       "target-key",
						"access_key": "AK_TARGET",
						"created_at": "2026-05-02T00:00:00Z",
						"grants":     []any{map[string]any{"permission": "read", "bucket": "bk"}},
					},
				},
			})
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected path", http.StatusInternalServerError)
	}))
	defer srv.Close()

	driver := drivers.NewSpacesKeyDriver(godoClientForTest(t, srv))
	out, err := driver.Read(context.Background(), interfaces.ResourceRef{
		Name: "target-key",
		// ProviderID intentionally empty — drives findByName fallback.
	})
	if err != nil {
		t.Fatalf("Read (findByName): %v", err)
	}
	if out.ProviderID != "AK_TARGET" {
		t.Errorf("ProviderID: want AK_TARGET, got %q", out.ProviderID)
	}
	if name, _ := out.Outputs["name"].(string); name != "target-key" {
		t.Errorf("Outputs[name]: want target-key, got %v", out.Outputs["name"])
	}
}

// TestSpacesKeyDriver_Read_FindByName_NotFound asserts that when the name
// fallback exhausts pagination without a match, ErrResourceNotFound is
// returned (wrapped). This is the negative-path companion to FindByName.
func TestSpacesKeyDriver_Read_FindByName_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/spaces/keys" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"keys": []any{
					map[string]any{"name": "other-1", "access_key": "AK_O1"},
					map[string]any{"name": "other-2", "access_key": "AK_O2"},
				},
				// No links.pages.next → list ends after this page.
			})
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected path", http.StatusInternalServerError)
	}))
	defer srv.Close()

	driver := drivers.NewSpacesKeyDriver(godoClientForTest(t, srv))
	_, err := driver.Read(context.Background(), interfaces.ResourceRef{Name: "missing-key"})
	if err == nil {
		t.Fatal("Read (findByName): expected ErrResourceNotFound, got nil")
	}
	if !errors.Is(err, interfaces.ErrResourceNotFound) {
		t.Errorf("Read (findByName): want errors.Is(err, ErrResourceNotFound), got %v", err)
	}
}

// TestSpacesKeyDriver_Update_RenameInPlace pins the in-place rename contract:
// godo Update accepts both Name and Grants, so a name change is NeedsUpdate
// (not NeedsReplace). The test asserts the request payload reaches DO with
// both fields, the response is mapped to Outputs, and secret_key is NOT in
// Outputs (Update never re-issues the secret).
func TestSpacesKeyDriver_Update_RenameInPlace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/spaces/keys/AKIATEST123" && r.Method == http.MethodPut {
			var got godo.SpacesKeyUpdateRequest
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
				return
			}
			if got.Name != "renamed-key" {
				http.Error(w, "request name="+got.Name+" want renamed-key", http.StatusBadRequest)
				return
			}
			if len(got.Grants) != 1 || got.Grants[0] == nil ||
				got.Grants[0].Permission != godo.SpacesKeyFullAccess {
				http.Error(w, "request grants mismatch", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"key": map[string]any{
					"name":       "renamed-key",
					"access_key": "AKIATEST123",
					"created_at": "2026-05-08T11:00:00Z",
					"grants":     []any{map[string]any{"permission": "fullaccess", "bucket": ""}},
				},
			})
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected path", http.StatusInternalServerError)
	}))
	defer srv.Close()

	driver := drivers.NewSpacesKeyDriver(godoClientForTest(t, srv))
	out, err := driver.Update(context.Background(),
		interfaces.ResourceRef{Name: "test-key", ProviderID: "AKIATEST123"},
		interfaces.ResourceSpec{
			Name: "renamed-key",
			Type: "infra.spaces_key",
			Config: map[string]any{
				"name":   "renamed-key",
				"grants": []any{map[string]any{"permission": "fullaccess"}},
			},
		},
	)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if name, _ := out.Outputs["name"].(string); name != "renamed-key" {
		t.Errorf("Outputs[name]: want renamed-key, got %v", out.Outputs["name"])
	}
	// secret_key MUST NOT appear in Update Outputs — Update doesn't re-issue.
	if _, ok := out.Outputs["secret_key"]; ok {
		t.Errorf("Outputs[secret_key]: must be absent on Update; got %v", out.Outputs["secret_key"])
	}
	if out.Sensitive["secret_key"] {
		t.Errorf("Sensitive[secret_key]: must be unset/false on Update, got true")
	}
}

// TestSpacesKeyDriver_Update_GrantsChange pins the grants-only update path:
// name unchanged, grants list rewritten. Asserts request payload faithfully
// translates spec.Config["grants"] to the godo wire format.
func TestSpacesKeyDriver_Update_GrantsChange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/spaces/keys/AKIATEST123" && r.Method == http.MethodPut {
			var got godo.SpacesKeyUpdateRequest
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
				return
			}
			if got.Name != "test-key" {
				http.Error(w, "request name="+got.Name+" want test-key", http.StatusBadRequest)
				return
			}
			if len(got.Grants) != 2 {
				http.Error(w, "request grants len mismatch", http.StatusBadRequest)
				return
			}
			if got.Grants[0].Bucket != "bk-1" || got.Grants[0].Permission != godo.SpacesKeyRead {
				http.Error(w, "request grants[0] mismatch", http.StatusBadRequest)
				return
			}
			if got.Grants[1].Bucket != "bk-2" || got.Grants[1].Permission != godo.SpacesKeyReadWrite {
				http.Error(w, "request grants[1] mismatch", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"key": map[string]any{
					"name":       "test-key",
					"access_key": "AKIATEST123",
					"created_at": "2026-05-08T11:00:00Z",
					"grants": []any{
						map[string]any{"permission": "read", "bucket": "bk-1"},
						map[string]any{"permission": "readwrite", "bucket": "bk-2"},
					},
				},
			})
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected path", http.StatusInternalServerError)
	}))
	defer srv.Close()

	driver := drivers.NewSpacesKeyDriver(godoClientForTest(t, srv))
	out, err := driver.Update(context.Background(),
		interfaces.ResourceRef{Name: "test-key", ProviderID: "AKIATEST123"},
		interfaces.ResourceSpec{
			Name: "test-key",
			Type: "infra.spaces_key",
			Config: map[string]any{
				"name": "test-key",
				"grants": []any{
					map[string]any{"permission": "read", "bucket": "bk-1"},
					map[string]any{"permission": "readwrite", "bucket": "bk-2"},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	grants, ok := out.Outputs["grants"].([]map[string]any)
	if !ok {
		t.Fatalf("Outputs[grants]: want []map[string]any, got %T", out.Outputs["grants"])
	}
	if len(grants) != 2 {
		t.Fatalf("Outputs[grants] len: want 2, got %d", len(grants))
	}
	if grants[0]["bucket"] != "bk-1" || grants[0]["permission"] != "read" {
		t.Errorf("Outputs[grants][0]: want {bk-1, read}, got %v", grants[0])
	}
}

// TestSpacesKeyDriver_Delete_HappyPath asserts a 204 No Content from godo
// Delete is treated as success (nil error).
func TestSpacesKeyDriver_Delete_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/spaces/keys/AKIATEST123" && r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected path", http.StatusInternalServerError)
	}))
	defer srv.Close()

	driver := drivers.NewSpacesKeyDriver(godoClientForTest(t, srv))
	err := driver.Delete(context.Background(), interfaces.ResourceRef{
		Name: "test-key", ProviderID: "AKIATEST123",
	})
	if err != nil {
		t.Fatalf("Delete: want nil, got %v", err)
	}
}

// TestSpacesKeyDriver_Delete_Idempotent_404 pins the idempotent-delete contract:
// a 404 response from DO (key already absent) is treated as success — matches
// sister provider RevokeProviderCredential and ADR 0015.
func TestSpacesKeyDriver_Delete_Idempotent_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/spaces/keys/AK_GONE" && r.Method == http.MethodDelete {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"id":"not_found","message":"key not found"}`))
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected path", http.StatusInternalServerError)
	}))
	defer srv.Close()

	driver := drivers.NewSpacesKeyDriver(godoClientForTest(t, srv))
	err := driver.Delete(context.Background(), interfaces.ResourceRef{
		Name: "test-key", ProviderID: "AK_GONE",
	})
	if err != nil {
		t.Fatalf("Delete (404 idempotent): want nil, got %v", err)
	}
}

// TestSpacesKeyDriver_Delete_OtherError_Propagates asserts that non-404
// errors (e.g. 500) propagate as errors rather than being silently swallowed.
// Without this guard, transient API failures on Delete could leak resources
// (the engine would believe the delete succeeded).
func TestSpacesKeyDriver_Delete_OtherError_Propagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/spaces/keys/AKIATEST123" && r.Method == http.MethodDelete {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"id":"server_error","message":"upstream failure"}`))
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected path", http.StatusInternalServerError)
	}))
	defer srv.Close()

	driver := drivers.NewSpacesKeyDriver(godoClientForTest(t, srv))
	err := driver.Delete(context.Background(), interfaces.ResourceRef{
		Name: "test-key", ProviderID: "AKIATEST123",
	})
	if err == nil {
		t.Fatal("Delete (500): expected non-nil error, got nil")
	}
	// Per WrapGodoError: 5xx maps to ErrTransient.
	if !errors.Is(err, interfaces.ErrTransient) {
		t.Errorf("Delete (500): want errors.Is(err, ErrTransient), got %v", err)
	}
}

// TestSpacesKeyDriver_Diff_NilCurrent_NeedsUpdate covers the resource-not-yet-
// created path: Diff(spec, nil) must return NeedsUpdate=true and not panic.
// Per the impl docstring at internal/drivers/spaces_key.go:179, nil current
// means "treat as initial create".
func TestSpacesKeyDriver_Diff_NilCurrent_NeedsUpdate(t *testing.T) {
	driver := drivers.NewSpacesKeyDriver(godo.NewClient(nil))
	result, err := driver.Diff(context.Background(),
		interfaces.ResourceSpec{
			Name: "test-key",
			Type: "infra.spaces_key",
			Config: map[string]any{
				"name":   "test-key",
				"grants": []any{map[string]any{"permission": "fullaccess"}},
			},
		},
		nil,
	)
	if err != nil {
		t.Fatalf("Diff(nil current): %v", err)
	}
	if !result.NeedsUpdate {
		t.Errorf("Diff(nil current): want NeedsUpdate=true, got false")
	}
}

// TestSpacesKeyDriver_Diff_NoChanges asserts that when desired matches current
// (same name, same grants), Diff reports no changes, no update, no replace.
func TestSpacesKeyDriver_Diff_NoChanges(t *testing.T) {
	driver := drivers.NewSpacesKeyDriver(godo.NewClient(nil))
	result, err := driver.Diff(context.Background(),
		interfaces.ResourceSpec{
			Name: "test-key",
			Type: "infra.spaces_key",
			Config: map[string]any{
				"name":   "test-key",
				"grants": []any{map[string]any{"permission": "fullaccess", "bucket": ""}},
			},
		},
		&interfaces.ResourceOutput{
			Name:       "test-key",
			ProviderID: "AKIATEST123",
			Outputs: map[string]any{
				"name":       "test-key",
				"access_key": "AKIATEST123",
				"grants": []map[string]any{
					{"permission": "fullaccess", "bucket": ""},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsUpdate {
		t.Errorf("Diff: want NeedsUpdate=false, got true (changes=%v)", result.Changes)
	}
	if result.NeedsReplace {
		t.Errorf("Diff: want NeedsReplace=false, got true")
	}
	if len(result.Changes) != 0 {
		t.Errorf("Diff: want 0 changes, got %d (%v)", len(result.Changes), result.Changes)
	}
}

// TestSpacesKeyDriver_Diff_NameChange asserts that a name divergence yields
// NeedsUpdate=true (NOT NeedsReplace), since godo Update accepts Name → the
// driver does an in-place rename. Pins ADR 0015 + spaces_key.go:25-28
// docstring contract.
func TestSpacesKeyDriver_Diff_NameChange(t *testing.T) {
	driver := drivers.NewSpacesKeyDriver(godo.NewClient(nil))
	result, err := driver.Diff(context.Background(),
		interfaces.ResourceSpec{
			Name: "renamed-key",
			Type: "infra.spaces_key",
			Config: map[string]any{
				"name":   "renamed-key",
				"grants": []any{map[string]any{"permission": "fullaccess", "bucket": ""}},
			},
		},
		&interfaces.ResourceOutput{
			Name:       "test-key",
			ProviderID: "AKIATEST123",
			Outputs: map[string]any{
				"name":       "test-key",
				"access_key": "AKIATEST123",
				"grants": []map[string]any{
					{"permission": "fullaccess", "bucket": ""},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Errorf("Diff: want NeedsUpdate=true (name change), got false")
	}
	if result.NeedsReplace {
		t.Errorf("Diff: want NeedsReplace=false (rename is in-place), got true")
	}
	// Find the name FieldChange.
	var nameChange *interfaces.FieldChange
	for i := range result.Changes {
		if result.Changes[i].Path == "name" {
			nameChange = &result.Changes[i]
			break
		}
	}
	if nameChange == nil {
		t.Fatalf("Diff: want FieldChange{Path: name}, got %v", result.Changes)
	}
	if nameChange.Old != "test-key" || nameChange.New != "renamed-key" {
		t.Errorf("Diff: name FieldChange Old=%v New=%v, want test-key→renamed-key",
			nameChange.Old, nameChange.New)
	}
	if nameChange.ForceNew {
		t.Errorf("Diff: name FieldChange.ForceNew: want false, got true")
	}
}

// TestSpacesKeyDriver_Diff_GrantsChange asserts that grants-list divergence
// yields NeedsUpdate=true with a grants FieldChange (Old/New populated with
// the normalized canonical map shape).
func TestSpacesKeyDriver_Diff_GrantsChange(t *testing.T) {
	driver := drivers.NewSpacesKeyDriver(godo.NewClient(nil))
	result, err := driver.Diff(context.Background(),
		interfaces.ResourceSpec{
			Name: "test-key",
			Type: "infra.spaces_key",
			Config: map[string]any{
				"name": "test-key",
				"grants": []any{
					map[string]any{"permission": "readwrite", "bucket": "bk-1"},
				},
			},
		},
		&interfaces.ResourceOutput{
			Name:       "test-key",
			ProviderID: "AKIATEST123",
			Outputs: map[string]any{
				"name":       "test-key",
				"access_key": "AKIATEST123",
				"grants": []map[string]any{
					{"permission": "read", "bucket": "bk-1"},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Errorf("Diff: want NeedsUpdate=true (grants change), got false")
	}
	var grantsChange *interfaces.FieldChange
	for i := range result.Changes {
		if result.Changes[i].Path == "grants" {
			grantsChange = &result.Changes[i]
			break
		}
	}
	if grantsChange == nil {
		t.Fatalf("Diff: want FieldChange{Path: grants}, got %v", result.Changes)
	}
	// Old/New must be the canonical []map[string]any shape (post-normalize).
	wantOld := []map[string]any{{"permission": "read", "bucket": "bk-1"}}
	wantNew := []map[string]any{{"permission": "readwrite", "bucket": "bk-1"}}
	if !reflect.DeepEqual(grantsChange.Old, wantOld) {
		t.Errorf("Diff: grants Old: want %v, got %v", wantOld, grantsChange.Old)
	}
	if !reflect.DeepEqual(grantsChange.New, wantNew) {
		t.Errorf("Diff: grants New: want %v, got %v", wantNew, grantsChange.New)
	}
}

// TestSpacesKeyDriver_Diff_StructpbRoundtripResilience pins the gRPC plugin
// boundary contract: when Outputs round-trips through structpb, typed slices
// are decoded as []any with each element being map[string]any. The driver's
// normalizeGrantsForDiff helper MUST coerce that shape to the canonical
// []map[string]any so DeepEqual against grantsToMaps's output succeeds and
// Diff does NOT false-positive on a no-op refresh.
//
// Per workspace memory feedback_workflow_plugin_structpb_boundary —
// without this, drivers running over the gRPC plugin boundary erroneously
// report "drift" on every plan even when nothing changed.
func TestSpacesKeyDriver_Diff_StructpbRoundtripResilience(t *testing.T) {
	driver := drivers.NewSpacesKeyDriver(godo.NewClient(nil))
	// Construct current.Outputs["grants"] as the post-structpb-roundtrip shape:
	// []any of map[string]any (NOT []map[string]any). This is what comes back
	// from structpb.NewStruct → AsMap on the consumer side.
	result, err := driver.Diff(context.Background(),
		interfaces.ResourceSpec{
			Name: "test-key",
			Type: "infra.spaces_key",
			Config: map[string]any{
				"name":   "test-key",
				"grants": []any{map[string]any{"permission": "fullaccess", "bucket": ""}},
			},
		},
		&interfaces.ResourceOutput{
			Name:       "test-key",
			ProviderID: "AKIATEST123",
			Outputs: map[string]any{
				"name":       "test-key",
				"access_key": "AKIATEST123",
				// Note: []any (not []map[string]any) — the structpb-roundtripped shape.
				"grants": []any{
					map[string]any{"permission": "fullaccess", "bucket": ""},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsUpdate {
		t.Errorf("Diff (structpb roundtrip): want NeedsUpdate=false (no real drift), got true; changes=%v",
			result.Changes)
	}
	if len(result.Changes) != 0 {
		t.Errorf("Diff (structpb roundtrip): want 0 changes, got %d (%v)",
			len(result.Changes), result.Changes)
	}
}

// TestSpacesKeyDriver_HealthCheck asserts the HealthCheck contract: with a
// non-empty ProviderID and a successful godo Get, it returns Healthy=true.
// Spaces keys have no provider-side health concept, so existence == healthy.
func TestSpacesKeyDriver_HealthCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/spaces/keys/AKIATEST123" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"key": map[string]any{
					"name":       "test-key",
					"access_key": "AKIATEST123",
					"created_at": "2026-05-08T11:00:00Z",
				},
			})
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected path", http.StatusInternalServerError)
	}))
	defer srv.Close()

	driver := drivers.NewSpacesKeyDriver(godoClientForTest(t, srv))
	result, err := driver.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name: "test-key", ProviderID: "AKIATEST123",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if result == nil {
		t.Fatal("HealthCheck: result is nil")
	}
	if !result.Healthy {
		t.Errorf("HealthCheck: want Healthy=true, got false (msg=%q)", result.Message)
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
