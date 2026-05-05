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
		parts = append(parts, fmt.Sprintf(`{"id":%q,"name":%q,"tags":%s}`, db.ID, db.Name, tags))
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
			{ID: "db-ccc", Name: "bmw-pg", Tags: []string{"bmw-prod"}},
			{ID: "db-ddd", Name: "other-pg", Tags: []string{}}, // must be filtered out
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
