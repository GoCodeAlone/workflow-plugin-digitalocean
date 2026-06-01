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
	dropletLists  int
	volumeLists   int
	databaseLists int
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
			f.dropletLists++
			tag := r.URL.Query().Get("tag_name")
			droplets := f.dropletsByTag[tag]
			writeDroplets(t, w, droplets)
			return

		case r.URL.Path == "/v2/volumes" && r.Method == http.MethodGet:
			f.volumeLists++
			writeVolumes(t, w, f.volumes)
			return

		case r.URL.Path == "/v2/databases" && r.Method == http.MethodGet:
			f.databaseLists++
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

// TestDOProvider_EnumerateAll_DNS_paginates pins the EnumerateAll contract for
// resource type "infra.dns": every zone in the DO account is returned with the
// metadata downstream callers (wfctl infra import-all, plus DNS audit/policy
// commands) need to feed back into IaCProvider.Import without re-reading each
// zone individually.
//
// Mirrors the paginated httptest pattern used by TestProvider_EnumerateAll_SpacesKeys
// (provider_test.go:722) — page 1 returns 2 domains with a next-page link;
// page 2 returns 1 domain. Asserts:
//
//   - DOProvider implements interfaces.EnumeratorAll for "infra.dns".
//   - Pagination is handled inside the provider, not punted to the caller —
//     the test asserts 3 outputs across 2 fake pages.
//   - Each *ResourceOutput has Type="infra.dns", ProviderID=zone-name (DO uses
//     the domain name as its cloud ID per the v2/domains API contract), and
//     Outputs.{zone, zone_id, ttl, zone_file} populated so the import-all path
//     can feed them into IaCProvider.Import without a second round-trip.
func TestDOProvider_EnumerateAll_DNS_paginates(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v2/domains" {
			w.Header().Set("Content-Type", "application/json")
			page := r.URL.Query().Get("page")
			if page == "" || page == "1" {
				_, _ = fmt.Fprintf(w, `{"domains":[`+
					`{"name":"alpha.test","ttl":1800,"zone_file":"$ORIGIN alpha.test.\n"},`+
					`{"name":"beta.test","ttl":3600,"zone_file":"$ORIGIN beta.test.\n"}`+
					`],"links":{"pages":{"next":%q,"last":%q}}}`,
					srv.URL+"/v2/domains?page=2", srv.URL+"/v2/domains?page=2")
				return
			}
			_, _ = fmt.Fprintf(w, `{"domains":[`+
				`{"name":"gamma.test","ttl":1800,"zone_file":"$ORIGIN gamma.test.\n"}`+
				`],"links":{}}`)
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected path", http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := newDOProviderForDNSEnumeratorTest(t, srv)
	enumerator, ok := interfaces.IaCProvider(p).(interfaces.EnumeratorAll)
	if !ok {
		t.Fatalf("Provider must implement EnumeratorAll")
	}

	outs, err := enumerator.EnumerateAll(context.Background(), "infra.dns")
	if err != nil {
		t.Fatalf("EnumerateAll(infra.dns): %v", err)
	}
	if len(outs) != 3 {
		t.Fatalf("expected 3 zones (paginated), got %d: %+v", len(outs), outs)
	}

	// Sort by ProviderID for stable comparison; pagination order is documented
	// but the test should not depend on the order godo emits pages in.
	sort.Slice(outs, func(i, j int) bool { return outs[i].ProviderID < outs[j].ProviderID })

	wantNames := []string{"alpha.test", "beta.test", "gamma.test"}
	for i, want := range wantNames {
		o := outs[i]
		if o.Type != "infra.dns" {
			t.Errorf("outs[%d].Type = %q, want %q", i, o.Type, "infra.dns")
		}
		if o.ProviderID != want {
			t.Errorf("outs[%d].ProviderID = %q, want %q", i, o.ProviderID, want)
		}
		if got, _ := o.Outputs["zone"].(string); got != want {
			t.Errorf("outs[%d].Outputs.zone = %v, want %q", i, o.Outputs["zone"], want)
		}
		// DO uses the domain name as its cloud identifier — zone_id mirrors
		// the ProviderID so downstream Import can call back via the same
		// addressable name without parsing ProviderID separately.
		if got, _ := o.Outputs["zone_id"].(string); got != want {
			t.Errorf("outs[%d].Outputs.zone_id = %v, want %q", i, o.Outputs["zone_id"], want)
		}
		// ttl must be the int from the godo Domain struct, not a string —
		// downstream Import code paths assert on int directly.
		if _, ok := o.Outputs["ttl"].(int); !ok {
			t.Errorf("outs[%d].Outputs.ttl = %T, want int", i, o.Outputs["ttl"])
		}
		if got, _ := o.Outputs["zone_file"].(string); got == "" {
			t.Errorf("outs[%d].Outputs.zone_file is empty; want non-empty", i)
		}
	}
}

// TestDOProvider_EnumerateAll_DNS_NilClient pins the defensive guard: if a
// caller invokes EnumerateAll("infra.dns") on an uninitialised provider, the
// method returns a clear error rather than panicking on nil-pointer deref.
// Mirrors the sister guard in TestDOProvider_EnumerateByTag_NilClient.
func TestDOProvider_EnumerateAll_DNS_NilClient(t *testing.T) {
	p := NewDOProvider()
	_, err := p.EnumerateAll(context.Background(), "infra.dns")
	if err == nil {
		t.Fatal("expected error from EnumerateAll on uninitialised provider; got nil")
	}
	if !strings.Contains(err.Error(), "not initialized") {
		t.Errorf("error %q should mention not initialized", err.Error())
	}
}

// newDOProviderForDNSEnumeratorTest mirrors newDOProviderForTest
// (provider_test.go) for tests that drive their own httptest handler. Kept
// local to this file so the enumerator suite stays self-contained.
func newDOProviderForDNSEnumeratorTest(t *testing.T, srv *httptest.Server) *DOProvider {
	t.Helper()
	client := godo.NewClient(srv.Client())
	base, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatalf("parse httptest URL %q: %v", srv.URL, err)
	}
	client.BaseURL = base
	return &DOProvider{client: client, region: "nyc3"}
}
