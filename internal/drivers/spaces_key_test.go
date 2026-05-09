package drivers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/GoCodeAlone/workflow/secrets"
	"github.com/digitalocean/godo"
)

// TestSpacesKeyDriver_Create_HappyPath is the contract test for
// SpacesKeyDriver.Create. It pins the split-storage pattern that is the whole
// point of the driver:
//
//   - ResourceOutput.ProviderID == access_key (the godo identifier; matches
//     the sister bucket driver in spaces.go).
//   - State outputs contain the metadata needed for downstream resources
//     (name, access_key, created_at) but NEVER secret_key — the secret
//     belongs in the secrets provider, not in IaC state files.
//   - The provided secrets.Provider receives BOTH the access_key and
//     secret_key as `<resource-name>_access_key` / `<resource-name>_secret_key`
//     so consumers (apps, runners) can read them by canonical name.
//   - SensitiveKeys() includes "access_key" so wfctl masks it in plan/diff
//     output. secret_key isn't in Outputs so doesn't need masking there.
//
// This test is the failing-side of the Task 11/12 TDD pair (PR4b). Until
// Task 12 lands NewSpacesKeyDriver in spaces_key.go, the file fails to
// compile with `undefined: NewSpacesKeyDriver`. Per ADR 0020.
func TestSpacesKeyDriver_Create_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/spaces/keys" && r.Method == http.MethodPost {
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
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	client := godoClientForTest(t, srv)
	fakeGHSecrets := &fakeSecretsProvider{stored: map[string]string{}}

	driver := NewSpacesKeyDriver(client, fakeGHSecrets)
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

	// ResourceOutput per workflow interfaces/iac_resource_driver.go.
	// ProviderID == access_key (the godo identifier; matches sister bucket
	// driver pattern at internal/drivers/spaces.go).
	if out.ProviderID != "AKIATEST123" {
		t.Errorf("ProviderID: want AKIATEST123 (access_key as godo ID), got %q", out.ProviderID)
	}

	// State outputs must contain {name, access_key, created_at}, NOT secret_key.
	if got, _ := out.Outputs["access_key"].(string); got != "AKIATEST123" {
		t.Errorf("access_key: want AKIATEST123, got %v", out.Outputs["access_key"])
	}
	if got, _ := out.Outputs["created_at"].(string); got != "2026-05-08T11:00:00Z" {
		t.Errorf("created_at: want 2026-05-08T11:00:00Z, got %v", out.Outputs["created_at"])
	}
	if _, ok := out.Outputs["secret_key"]; ok {
		t.Errorf("secret_key MUST NOT be in state outputs (must live in GH Secrets only)")
	}

	// Both sub-keys must reach the secrets.Provider under the canonical
	// `<resource>_access_key` / `<resource>_secret_key` names.
	if got := fakeGHSecrets.stored["test-key_access_key"]; got != "AKIATEST123" {
		t.Errorf("GH Secret test-key_access_key: want AKIATEST123, got %q", got)
	}
	if got := fakeGHSecrets.stored["test-key_secret_key"]; got != "SK_secret_data_here" {
		t.Errorf("GH Secret test-key_secret_key: want SK_secret_data_here, got %q", got)
	}

	// SensitiveKeys returns access_key (so wfctl masks it in logs).
	// secret_key is NOT in Outputs, so doesn't need state-side masking;
	// the secrets.Provider handles its sensitivity separately.
	sensitive := driver.SensitiveKeys()
	found := false
	for _, k := range sensitive {
		if k == "access_key" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("SensitiveKeys must include access_key; got %v", sensitive)
	}
}

// godoClientForTest builds a godo client whose BaseURL points at the given
// httptest server and whose underlying http.Client is the test server's own
// client. Mirrors `internal/provider_enumerator_test.go::newProviderForEnumeratorTest`
// (line 149: `godo.NewClient(srv.Client())`). Using `srv.Client()` rather than
// `http.DefaultClient` keeps the test hermetic — every request rides the
// test server's transport, so an ambient transport mutation in another test
// can't leak in. Standard httptest discipline.
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

// fakeSecretsProvider is a minimal in-memory secrets.Provider that records
// every Set so tests can assert which sub-keys were stored. Get/Delete/List
// implement the interface contract; the Spaces key driver only uses Set.
type fakeSecretsProvider struct {
	stored map[string]string
}

func (f *fakeSecretsProvider) Name() string { return "fake" }

func (f *fakeSecretsProvider) Get(_ context.Context, key string) (string, error) {
	if v, ok := f.stored[key]; ok {
		return v, nil
	}
	return "", secrets.ErrNotFound
}

func (f *fakeSecretsProvider) Set(_ context.Context, key, value string) error {
	if f.stored == nil {
		f.stored = map[string]string{}
	}
	f.stored[key] = value
	return nil
}

func (f *fakeSecretsProvider) Delete(_ context.Context, key string) error {
	delete(f.stored, key)
	return nil
}

func (f *fakeSecretsProvider) List(_ context.Context) ([]string, error) {
	keys := make([]string, 0, len(f.stored))
	for k := range f.stored {
		keys = append(keys, k)
	}
	return keys, nil
}
