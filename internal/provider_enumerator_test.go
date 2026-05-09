package internal

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// TestDOProvider_ImplementsEnumerator is a compile-time assertion that the DO
// plugin satisfies the opt-in interfaces.Enumerator interface added in workflow
// PR #557 (workflow#536). The cleanup subcommand type-asserts against this
// interface and skips providers that do not implement it.
func TestDOProvider_ImplementsEnumerator(t *testing.T) {
	var _ interfaces.Enumerator = (*DOProvider)(nil)
}

// TestDOProvider_EnumerateByTag_NilClient pins the defensive guard: if the
// caller invokes EnumerateByTag on an uninitialised provider (no godo client),
// the method returns a clear error rather than panicking on nil-pointer
// deref.
func TestDOProvider_EnumerateByTag_NilClient(t *testing.T) {
	p := NewDOProvider() // Initialize NOT called → p.client is nil
	_, err := p.EnumerateByTag(context.Background(), "any-tag")
	if err == nil {
		t.Fatal("expected error from EnumerateByTag on uninitialised provider; got nil")
	}
	if !strings.Contains(err.Error(), "not initialized") {
		t.Errorf("error %q should mention not initialized", err.Error())
	}
}

// enumeratorFakeAPI is an httptest-backed mock that implements the subset of
// the DO REST API consumed by EnumerateByTag: GET /v2/tags/{name},
// GET /v2/droplets?tag_name={tag}, GET /v2/volumes, GET /v2/databases.
//
// The mock returns canned JSON for each endpoint based on the maps populated
// by the test. Pagination links are intentionally omitted — the production
// EnumerateByTag handles pagination, but for unit-test scope a single page
// per endpoint is sufficient to exercise the result-shape contract.
type enumeratorFakeAPI struct {
	dropletsByTag map[string][]godo.Droplet
	volumes       []godo.Volume
	databases     []godo.Database
	// tagExists maps tag-name → whether GET /v2/tags/{name} returns 200.
	// When the tag does not exist, the API returns 404; EnumerateByTag must
	// treat 404 as "no resources" (empty result) not an error.
	tagExists map[string]bool
}

// handler returns an http.HandlerFunc that routes the path/method to the
// canned response. Anything not handled returns 501 so the test surfaces the
// gap rather than silently coercing to an empty list.
func (f *enumeratorFakeAPI) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.HasPrefix(r.URL.Path, "/v2/tags/") && r.Method == http.MethodGet:
			tagName := strings.TrimPrefix(r.URL.Path, "/v2/tags/")
			if !f.tagExists[tagName] {
				w.WriteHeader(http.StatusNotFound)
				_, _ = fmt.Fprintf(w, `{"id":"not_found","message":"tag %q not found"}`, tagName)
				return
			}
			// Tags.Get response shape — counts only.
			_, _ = fmt.Fprintf(w, `{"tag":{"name":%q,"resources":{"count":0,"droplets":{"count":%d},"volumes":{"count":%d},"databases":{"count":%d},"images":{"count":0},"volume_snapshots":{"count":0}}}}`,
				tagName, len(f.dropletsByTag[tagName]), len(f.volumes), len(f.databases))
			return

		case r.URL.Path == "/v2/droplets" && r.Method == http.MethodGet:
			tag := r.URL.Query().Get("tag_name")
			droplets := f.dropletsByTag[tag]
			writeDroplets(t, w, droplets)
			return

		case r.URL.Path == "/v2/volumes" && r.Method == http.MethodGet:
			writeVolumes(t, w, f.volumes)
			return

		case r.URL.Path == "/v2/databases" && r.Method == http.MethodGet:
			writeDatabases(t, w, f.databases)
			return
		}

		w.WriteHeader(http.StatusNotImplemented)
		_, _ = fmt.Fprintf(w, `{"id":"not_implemented","message":"%s %s not handled by enumeratorFakeAPI"}`, r.Method, r.URL.Path)
	}
}

func writeDroplets(t *testing.T, w http.ResponseWriter, droplets []godo.Droplet) {
	t.Helper()
	parts := make([]string, 0, len(droplets))
	for _, d := range droplets {
		tags := jsonStringArray(d.Tags)
		parts = append(parts, fmt.Sprintf(`{"id":%d,"name":%q,"tags":%s}`, d.ID, d.Name, tags))
	}
	_, _ = fmt.Fprintf(w, `{"droplets":[%s],"links":{},"meta":{"total":%d}}`, strings.Join(parts, ","), len(droplets))
}

func writeVolumes(t *testing.T, w http.ResponseWriter, volumes []godo.Volume) {
	t.Helper()
	parts := make([]string, 0, len(volumes))
	for _, v := range volumes {
		tags := jsonStringArray(v.Tags)
		parts = append(parts, fmt.Sprintf(`{"id":%q,"name":%q,"tags":%s}`, v.ID, v.Name, tags))
	}
	_, _ = fmt.Fprintf(w, `{"volumes":[%s],"links":{},"meta":{"total":%d}}`, strings.Join(parts, ","), len(volumes))
}

func writeDatabases(t *testing.T, w http.ResponseWriter, databases []godo.Database) {
	t.Helper()
	parts := make([]string, 0, len(databases))
	for _, db := range databases {
		tags := jsonStringArray(db.Tags)
		parts = append(parts, fmt.Sprintf(`{"id":%q,"name":%q,"engine":%q,"tags":%s}`, db.ID, db.Name, db.EngineSlug, tags))
	}
	_, _ = fmt.Fprintf(w, `{"databases":[%s],"links":{},"meta":{"total":%d}}`, strings.Join(parts, ","), len(databases))
}

func jsonStringArray(s []string) string {
	if len(s) == 0 {
		return "[]"
	}
	parts := make([]string, len(s))
	for i, v := range s {
		parts[i] = fmt.Sprintf("%q", v)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// newProviderForEnumeratorTest returns a DOProvider whose godo client is wired
// to a httptest server running the given mock. It does NOT call Initialize
// because Initialize requires a token and would also reset BaseURL. Instead
// the test directly constructs the godo client + sets BaseURL.
func newProviderForEnumeratorTest(t *testing.T, mock *enumeratorFakeAPI) (*DOProvider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(mock.handler(t))
	t.Cleanup(srv.Close)

	client := godo.NewClient(srv.Client())
	base, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatalf("parse httptest URL: %v", err)
	}
	client.BaseURL = base
	return &DOProvider{client: client, region: "nyc3"}, srv
}

func TestDOProvider_EnumerateByTag_ReturnsTaggedResources(t *testing.T) {
	mock := &enumeratorFakeAPI{
		tagExists: map[string]bool{"bmw-prod": true},
		dropletsByTag: map[string][]godo.Droplet{
			"bmw-prod": {
				{ID: 1001, Name: "bmw-app-1", Tags: []string{"bmw-prod"}},
				{ID: 1002, Name: "bmw-app-2", Tags: []string{"bmw-prod", "extra"}},
			},
		},
		volumes: []godo.Volume{
			{ID: "vol-aaa", Name: "bmw-data", Tags: []string{"bmw-prod"}},
			{ID: "vol-bbb", Name: "other-data", Tags: []string{"unrelated"}}, // must be filtered out
		},
		databases: []godo.Database{
			// SQL-style cluster — must surface as infra.database.
			{ID: "db-ccc", Name: "bmw-pg", EngineSlug: "pg", Tags: []string{"bmw-prod"}},
			// Managed Redis on the SAME /v2/databases endpoint — must surface
			// as infra.cache, not infra.database. Pins the engine-split fix
			// from PR review finding #2.
			{ID: "redis-eee", Name: "bmw-cache", EngineSlug: "redis", Tags: []string{"bmw-prod"}},
			// Off-tag — must be filtered out.
			{ID: "db-ddd", Name: "other-pg", EngineSlug: "pg", Tags: []string{}},
		},
	}

	p, _ := newProviderForEnumeratorTest(t, mock)

	refs, err := p.EnumerateByTag(context.Background(), "bmw-prod")
	if err != nil {
		t.Fatalf("EnumerateByTag: %v", err)
	}

	// Sort by Name for stable comparison; provider does not guarantee order.
	sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })

	want := []interfaces.ResourceRef{
		{Name: "bmw-app-1", Type: "infra.droplet", ProviderID: "1001"},
		{Name: "bmw-app-2", Type: "infra.droplet", ProviderID: "1002"},
		{Name: "bmw-cache", Type: "infra.cache", ProviderID: "redis-eee"},
		{Name: "bmw-data", Type: "infra.volume", ProviderID: "vol-aaa"},
		{Name: "bmw-pg", Type: "infra.database", ProviderID: "db-ccc"},
	}
	sort.Slice(want, func(i, j int) bool { return want[i].Name < want[j].Name })

	if len(refs) != len(want) {
		t.Fatalf("EnumerateByTag returned %d refs, want %d: %+v", len(refs), len(want), refs)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Errorf("ref[%d] = %+v, want %+v", i, refs[i], want[i])
		}
	}
}

// TestDOProvider_EnumerateByTag_TagNotFound verifies that when the tag does
// not exist (DO API returns 404 from GET /v2/tags/{name}), EnumerateByTag
// returns an empty slice rather than an error. Operators running cleanup
// against a tag that has never been used should see "no resources" not "tag
// lookup failed".
func TestDOProvider_EnumerateByTag_TagNotFound(t *testing.T) {
	mock := &enumeratorFakeAPI{
		tagExists: map[string]bool{}, // bmw-prod NOT in the tag set
	}

	p, _ := newProviderForEnumeratorTest(t, mock)

	refs, err := p.EnumerateByTag(context.Background(), "bmw-prod")
	if err != nil {
		t.Fatalf("EnumerateByTag for missing tag: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("EnumerateByTag for missing tag returned %d refs, want 0: %+v", len(refs), refs)
	}
}

// TestDOProvider_EnumerateByTag_EmptyTag rejects empty input early — passing
// "" to godo would query /v2/tags/ which has different semantics (list all
// tags) and pollute results. The contract requires a non-empty tag.
func TestDOProvider_EnumerateByTag_EmptyTag(t *testing.T) {
	mock := &enumeratorFakeAPI{}
	p, _ := newProviderForEnumeratorTest(t, mock)

	_, err := p.EnumerateByTag(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty tag; got nil")
	}
	if !strings.Contains(err.Error(), "tag") {
		t.Errorf("error %q should mention the tag argument", err.Error())
	}
}

// fakeEnumeratorProvider is an IaCProvider that ALSO implements Enumerator,
// used by the dispatch-level test below to verify the doModuleInstance
// InvokeMethod proxy reaches the underlying provider.
//
// Embeds fakeIaCProvider (defined in module_instance_test.go) for the bulk
// of the IaCProvider surface; only EnumerateByTag and call-tracking fields
// are added here.
type fakeEnumeratorProvider struct {
	fakeIaCProvider
	enumerateCalled bool
	enumerateTag    string
	enumerateRefs   []interfaces.ResourceRef
	enumerateErr    error
}

func (f *fakeEnumeratorProvider) EnumerateByTag(_ context.Context, tag string) ([]interfaces.ResourceRef, error) {
	f.enumerateCalled = true
	f.enumerateTag = tag
	return f.enumerateRefs, f.enumerateErr
}

// TestDOModuleInstance_InvokeMethod_EnumerateByTag pins finding #1 of the
// PR 6b Copilot review: the doModuleInstance.InvokeMethod dispatch table
// must route "IaCProvider.EnumerateByTag" through to the underlying
// provider when it implements interfaces.Enumerator. Without this case,
// the host's remoteIaCProvider would type-assert ok=false and the cleanup
// dispatcher would silently skip every DO provider — even though
// DOProvider implements Enumerator directly.
func TestDOModuleInstance_InvokeMethod_EnumerateByTag(t *testing.T) {
	prov := &fakeEnumeratorProvider{
		enumerateRefs: []interfaces.ResourceRef{
			{Name: "bmw-app", Type: "infra.droplet", ProviderID: "12345"},
			{Name: "bmw-cache", Type: "infra.cache", ProviderID: "redis-uuid"},
		},
	}
	mi := &doModuleInstance{provider: prov}

	out, err := mi.InvokeMethod("IaCProvider.EnumerateByTag", map[string]any{
		"tag": "bmw-prod",
	})
	if err != nil {
		t.Fatalf("InvokeMethod: %v", err)
	}
	if !prov.enumerateCalled {
		t.Fatal("provider.EnumerateByTag was not called via dispatch")
	}
	if prov.enumerateTag != "bmw-prod" {
		t.Errorf("enumerateTag = %q, want %q", prov.enumerateTag, "bmw-prod")
	}
	rawRefs, ok := out["refs"].([]any)
	if !ok {
		t.Fatalf(`out["refs"] type = %T, want []any`, out["refs"])
	}
	if len(rawRefs) != 2 {
		t.Fatalf("refs length = %d, want 2: %+v", len(rawRefs), rawRefs)
	}
	// Sample the first ref to confirm structToMap shape — keys match the
	// JSON tags on interfaces.ResourceRef.
	first, ok := rawRefs[0].(map[string]any)
	if !ok {
		t.Fatalf("ref[0] type = %T, want map[string]any", rawRefs[0])
	}
	if first["name"] != "bmw-app" || first["type"] != "infra.droplet" || first["provider_id"] != "12345" {
		t.Errorf("ref[0] = %+v, want name=bmw-app type=infra.droplet provider_id=12345", first)
	}
}

// TestDOModuleInstance_InvokeMethod_EnumerateByTag_NonEnumeratorProvider
// pins the codes.Unimplemented branch. When the underlying provider does
// NOT implement interfaces.Enumerator, the dispatch must surface an
// Unimplemented gRPC error so the host's remoteIaCProvider can interpret
// it as "skip this provider" (same semantic as the in-process type-assert
// returning ok=false). Without this branch, callers couldn't distinguish
// "provider does not support this op" from "provider crashed".
func TestDOModuleInstance_InvokeMethod_EnumerateByTag_NonEnumeratorProvider(t *testing.T) {
	mi := &doModuleInstance{provider: &fakeIaCProvider{}}

	_, err := mi.InvokeMethod("IaCProvider.EnumerateByTag", map[string]any{
		"tag": "bmw-prod",
	})
	if err == nil {
		t.Fatal("expected Unimplemented error from non-Enumerator provider; got nil")
	}
	if !strings.Contains(err.Error(), "Enumerator") {
		t.Errorf("error %q should mention Enumerator", err.Error())
	}
}

// fakeEnumeratorAllProvider is an IaCProvider that ALSO implements
// EnumeratorAll, used by the dispatch-level test to verify the
// doModuleInstance.InvokeMethod proxy reaches the provider's typed
// EnumerateAll(ctx, resourceType) method. Embeds fakeIaCProvider for the
// bulk of the IaCProvider surface.
type fakeEnumeratorAllProvider struct {
	fakeIaCProvider
	enumerateAllCalled       bool
	enumerateAllResourceType string
	enumerateAllOuts         []*interfaces.ResourceOutput
	enumerateAllErr          error
}

func (f *fakeEnumeratorAllProvider) EnumerateAll(_ context.Context, resourceType string) ([]*interfaces.ResourceOutput, error) {
	f.enumerateAllCalled = true
	f.enumerateAllResourceType = resourceType
	return f.enumerateAllOuts, f.enumerateAllErr
}

// TestDOModuleInstance_InvokeMethod_EnumerateAll pins the dispatcher case
// added in v0.14.2 fixing the runtime "unknown method
// IaCProvider.EnumerateAll" failure that surfaced against staging. The
// doModuleInstance.InvokeMethod dispatch table must route
// "IaCProvider.EnumerateAll" through to the underlying provider's typed
// EnumerateAll(ctx, resourceType) when it implements interfaces.EnumeratorAll.
//
// This complements TestDispatcherCoversEveryProviderInterfaceMethod (the
// CI-time strict-coverage test): coverage asserts the case exists; this test
// asserts the case behavior matches the wfctl-side bridge contract
// (deploy_providers.go's remoteIaCProvider.EnumerateAll).
func TestDOModuleInstance_InvokeMethod_EnumerateAll(t *testing.T) {
	prov := &fakeEnumeratorAllProvider{
		enumerateAllOuts: []*interfaces.ResourceOutput{
			{
				Name:       "bmw-deploy-key",
				Type:       "infra.spaces_key",
				ProviderID: "DOACCESS123",
				Status:     "active",
				Outputs: map[string]any{
					"name":       "bmw-deploy-key",
					"access_key": "DOACCESS123",
				},
				Sensitive: map[string]bool{"access_key": true},
			},
			{
				Name:       "bmw-state-key",
				Type:       "infra.spaces_key",
				ProviderID: "DOACCESS456",
				Status:     "active",
			},
		},
	}
	mi := &doModuleInstance{provider: prov}

	out, err := mi.InvokeMethod("IaCProvider.EnumerateAll", map[string]any{
		"resource_type": "infra.spaces_key",
	})
	if err != nil {
		t.Fatalf("InvokeMethod EnumerateAll: %v", err)
	}
	if !prov.enumerateAllCalled {
		t.Fatal("provider.EnumerateAll was not called via dispatch — case missing or routed to wrong impl")
	}
	if prov.enumerateAllResourceType != "infra.spaces_key" {
		t.Errorf("enumerateAllResourceType = %q, want %q",
			prov.enumerateAllResourceType, "infra.spaces_key")
	}
	// Response shape MUST be {"outputs": [<map>, ...]} so wfctl's
	// remoteIaCProvider.EnumerateAll (deploy_providers.go) can decode via
	// res["outputs"] + anyToStruct. Mismatch here is the runtime symptom
	// against staging the v0.14.2 fix closed.
	rawOuts, ok := out["outputs"].([]any)
	if !ok {
		t.Fatalf(`out["outputs"] type = %T, want []any`, out["outputs"])
	}
	if len(rawOuts) != 2 {
		t.Fatalf("outputs length = %d, want 2: %+v", len(rawOuts), rawOuts)
	}
	// Sample the first output — keys match interfaces.ResourceOutput JSON tags.
	first, ok := rawOuts[0].(map[string]any)
	if !ok {
		t.Fatalf("outputs[0] type = %T, want map[string]any", rawOuts[0])
	}
	if first["name"] != "bmw-deploy-key" ||
		first["type"] != "infra.spaces_key" ||
		first["provider_id"] != "DOACCESS123" {
		t.Errorf("outputs[0] = %+v, want name=bmw-deploy-key type=infra.spaces_key provider_id=DOACCESS123",
			first)
	}
}

// TestDOModuleInstance_InvokeMethod_EnumerateAll_NonEnumeratorAllProvider
// pins the codes.Unimplemented branch for EnumerateAll. When the underlying
// provider does NOT implement interfaces.EnumeratorAll, the dispatcher must
// surface an Unimplemented gRPC error so wfctl's remoteIaCProvider.EnumerateAll
// can translate it into interfaces.ErrProviderMethodUnimplemented (preserving
// the iterate-and-skip semantics in cmd/wfctl/infra_audit_keys.go and
// infra_prune.go).
func TestDOModuleInstance_InvokeMethod_EnumerateAll_NonEnumeratorAllProvider(t *testing.T) {
	mi := &doModuleInstance{provider: &fakeIaCProvider{}}

	_, err := mi.InvokeMethod("IaCProvider.EnumerateAll", map[string]any{
		"resource_type": "infra.spaces_key",
	})
	if err == nil {
		t.Fatal("expected Unimplemented error from non-EnumeratorAll provider; got nil")
	}
	if !strings.Contains(err.Error(), "EnumeratorAll") {
		t.Errorf("error %q should mention EnumeratorAll", err.Error())
	}
}
