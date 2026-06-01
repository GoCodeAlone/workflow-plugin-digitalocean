package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
	"github.com/digitalocean/godo"
	"google.golang.org/grpc"
)

func TestDOProvider_ImplementsOwnershipProvider(t *testing.T) {
	var _ interfaces.OwnershipProvider = (*DOProvider)(nil)
}

func TestDOIaCServer_RegistersOwnershipProvider(t *testing.T) {
	server := grpc.NewServer()
	if err := sdk.RegisterAllIaCProviderServices(server, NewIaCServer()); err != nil {
		t.Fatalf("RegisterAllIaCProviderServices: %v", err)
	}
	if _, ok := server.GetServiceInfo()[pb.IaCProviderOwnership_ServiceDesc.ServiceName]; !ok {
		t.Fatalf("registered services missing %s", pb.IaCProviderOwnership_ServiceDesc.ServiceName)
	}
}

func TestPluginManifestAdvertisesOwnershipProvider(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(testRepoRoot(t), "plugin.json"))
	if err != nil {
		t.Fatalf("read plugin.json: %v", err)
	}
	var manifest struct {
		IaCServices []string `json:"iacServices"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse plugin.json: %v", err)
	}
	if !containsString(manifest.IaCServices, pb.IaCProviderOwnership_ServiceDesc.ServiceName) {
		t.Fatalf("iacServices missing %s: %v", pb.IaCProviderOwnership_ServiceDesc.ServiceName, manifest.IaCServices)
	}
}

func TestDOProvider_GetOwnerFromDropletTags(t *testing.T) {
	api := &ownershipFakeAPI{
		droplets: map[string]ownershipResource{
			"1001": {id: "1001", name: "app", tags: []string{"other", "workflow-owner:team-a"}},
		},
	}
	p, _ := newProviderForOwnershipTest(t, api)

	owner, err := p.GetOwner(context.Background(), interfaces.ResourceRef{
		Name:       "app",
		Type:       "infra.droplet",
		ProviderID: "1001",
	})
	if err != nil {
		t.Fatalf("GetOwner: %v", err)
	}
	if owner.Owner != "team-a" {
		t.Fatalf("owner = %q, want team-a", owner.Owner)
	}
	if owner.Source != "tag:workflow-owner" {
		t.Fatalf("source = %q, want tag:workflow-owner", owner.Source)
	}
}

func TestDOProvider_GetOwnerFromSupportedResourceTags(t *testing.T) {
	encodedOwner := "team a@example.com"
	encodedTag, err := ownerTagName(encodedOwner)
	if err != nil {
		t.Fatalf("ownerTagName: %v", err)
	}
	api := &ownershipFakeAPI{
		droplets: map[string]ownershipResource{
			"1001": {id: "1001", name: "app", tags: []string{"workflow-owner:team-a"}},
		},
		volumes: map[string]ownershipResource{
			"vol-1": {id: "vol-1", name: "data", tags: []string{encodedTag}},
		},
		databases: map[string]ownershipResource{
			"db-1":    {id: "db-1", name: "pg", engine: "pg", tags: []string{"workflow-owner:team-db"}},
			"cache-1": {id: "cache-1", name: "redis", engine: "redis", tags: []string{"workflow-owner:team-cache"}},
		},
	}
	p, _ := newProviderForOwnershipTest(t, api)

	tests := []struct {
		name string
		ref  interfaces.ResourceRef
		want string
	}{
		{
			name: "droplet",
			ref:  interfaces.ResourceRef{Name: "app", Type: "infra.droplet", ProviderID: "1001"},
			want: "team-a",
		},
		{
			name: "volume encoded owner",
			ref:  interfaces.ResourceRef{Name: "data", Type: "infra.volume", ProviderID: "vol-1"},
			want: encodedOwner,
		},
		{
			name: "database",
			ref:  interfaces.ResourceRef{Name: "pg", Type: "infra.database", ProviderID: "db-1"},
			want: "team-db",
		},
		{
			name: "cache",
			ref:  interfaces.ResourceRef{Name: "redis", Type: "infra.cache", ProviderID: "cache-1"},
			want: "team-cache",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, err := p.GetOwner(context.Background(), tt.ref)
			if err != nil {
				t.Fatalf("GetOwner: %v", err)
			}
			if owner.Owner != tt.want {
				t.Fatalf("owner = %q, want %q", owner.Owner, tt.want)
			}
			if owner.Source != "tag:workflow-owner" {
				t.Fatalf("source = %q, want tag:workflow-owner", owner.Source)
			}
		})
	}
}

func TestDOProvider_OwnershipRequiresInitializedClient(t *testing.T) {
	p := NewDOProvider()
	ref := interfaces.ResourceRef{Name: "app", Type: "infra.droplet", ProviderID: "1001"}

	_, err := p.GetOwner(context.Background(), ref)
	if err == nil || !strings.Contains(err.Error(), "call Initialize first") {
		t.Fatalf("GetOwner error = %v, want Initialize hint", err)
	}
	err = p.SetOwner(context.Background(), ref, "team-a")
	if err == nil || !strings.Contains(err.Error(), "call Initialize first") {
		t.Fatalf("SetOwner error = %v, want Initialize hint", err)
	}
	_, err = p.ListOwners(context.Background(), interfaces.OwnerFilter{Owner: "team-a"})
	if err == nil || !strings.Contains(err.Error(), "call Initialize first") {
		t.Fatalf("ListOwners error = %v, want Initialize hint", err)
	}
}

func TestDOProvider_SetOwnerCreatesTagAndReplacesOldOwnerTag(t *testing.T) {
	api := &ownershipFakeAPI{
		droplets: map[string]ownershipResource{
			"1001": {id: "1001", name: "app", tags: []string{"workflow-owner:team-old"}},
		},
	}
	p, _ := newProviderForOwnershipTest(t, api)

	err := p.SetOwner(context.Background(), interfaces.ResourceRef{
		Name:       "app",
		Type:       "infra.droplet",
		ProviderID: "1001",
	}, "team-a")
	if err != nil {
		t.Fatalf("SetOwner: %v", err)
	}

	if !api.createdTags["workflow-owner:team-a"] {
		t.Fatalf("expected workflow-owner:team-a tag to be created")
	}
	if !api.tagged["workflow-owner:team-a|droplet|1001"] {
		t.Fatalf("expected droplet 1001 to be tagged with workflow-owner:team-a; got %+v", api.tagged)
	}
	if !api.untagged["workflow-owner:team-old|droplet|1001"] {
		t.Fatalf("expected old owner tag to be removed; got %+v", api.untagged)
	}
}

func TestDOProvider_ListOwnersByOwnerUsesTaggedResourceEnumeration(t *testing.T) {
	mock := &enumeratorFakeAPI{
		tagExists: map[string]bool{"workflow-owner:team-a": true},
		dropletsByTag: map[string][]godo.Droplet{
			"workflow-owner:team-a": {
				{ID: 1001, Name: "app", Tags: []string{"workflow-owner:team-a"}},
			},
		},
		volumes: []godo.Volume{
			{ID: "vol-1", Name: "data", Tags: []string{"workflow-owner:team-a"}},
		},
		databases: []godo.Database{
			{ID: "db-1", Name: "pg", EngineSlug: "pg", Tags: []string{"workflow-owner:team-a"}},
		},
	}
	p, _ := newProviderForEnumeratorTest(t, mock)

	owners, err := p.ListOwners(context.Background(), interfaces.OwnerFilter{Owner: "team-a"})
	if err != nil {
		t.Fatalf("ListOwners: %v", err)
	}
	if len(owners) != 3 {
		t.Fatalf("owners len = %d, want 3: %+v", len(owners), owners)
	}
	for _, owner := range owners {
		if owner.Owner != "team-a" {
			t.Fatalf("owner = %q, want team-a in %+v", owner.Owner, owner)
		}
		if owner.Source != "tag:workflow-owner" {
			t.Fatalf("source = %q, want tag:workflow-owner", owner.Source)
		}
	}
}

func TestDOProvider_ListOwnersByResourceTypeOnlyEnumeratesThatType(t *testing.T) {
	mock := &enumeratorFakeAPI{
		tagExists: map[string]bool{"workflow-owner:team-a": true},
		dropletsByTag: map[string][]godo.Droplet{
			"workflow-owner:team-a": {
				{ID: 1001, Name: "app", Tags: []string{"workflow-owner:team-a"}},
			},
		},
		volumes: []godo.Volume{
			{ID: "vol-1", Name: "data", Tags: []string{"workflow-owner:team-a"}},
		},
		databases: []godo.Database{
			{ID: "db-1", Name: "pg", EngineSlug: "pg", Tags: []string{"workflow-owner:team-a"}},
			{ID: "cache-1", Name: "redis", EngineSlug: "redis", Tags: []string{"workflow-owner:team-a"}},
		},
	}
	p, _ := newProviderForEnumeratorTest(t, mock)

	owners, err := p.ListOwners(context.Background(), interfaces.OwnerFilter{
		Owner:        "team-a",
		ResourceType: "infra.volume",
	})
	if err != nil {
		t.Fatalf("ListOwners: %v", err)
	}
	if len(owners) != 1 {
		t.Fatalf("owners len = %d, want 1: %+v", len(owners), owners)
	}
	if owners[0].Ref.Type != "infra.volume" || owners[0].Ref.ProviderID != "vol-1" {
		t.Fatalf("owner ref = %+v, want volume vol-1", owners[0].Ref)
	}
	if mock.volumeLists != 1 {
		t.Fatalf("volume list calls = %d, want 1", mock.volumeLists)
	}
	if mock.dropletLists != 0 || mock.databaseLists != 0 {
		t.Fatalf("unexpected unrelated list calls: droplets=%d databases=%d", mock.dropletLists, mock.databaseLists)
	}
}

type ownershipResource struct {
	id     string
	name   string
	engine string
	tags   []string
}

type ownershipFakeAPI struct {
	droplets    map[string]ownershipResource
	volumes     map[string]ownershipResource
	databases   map[string]ownershipResource
	createdTags map[string]bool
	tagged      map[string]bool
	untagged    map[string]bool
}

func (f *ownershipFakeAPI) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	if f.createdTags == nil {
		f.createdTags = map[string]bool{}
	}
	if f.tagged == nil {
		f.tagged = map[string]bool{}
	}
	if f.untagged == nil {
		f.untagged = map[string]bool{}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/v2/droplets/") && r.Method == http.MethodGet:
			id := strings.TrimPrefix(r.URL.Path, "/v2/droplets/")
			writeOwnershipResource(t, w, "droplet", f.droplets[id])
			return
		case strings.HasPrefix(r.URL.Path, "/v2/volumes/") && r.Method == http.MethodGet:
			id := strings.TrimPrefix(r.URL.Path, "/v2/volumes/")
			writeOwnershipResource(t, w, "volume", f.volumes[id])
			return
		case strings.HasPrefix(r.URL.Path, "/v2/databases/") && r.Method == http.MethodGet:
			id := strings.TrimPrefix(r.URL.Path, "/v2/databases/")
			writeOwnershipResource(t, w, "database", f.databases[id])
			return
		case r.URL.Path == "/v2/tags" && r.Method == http.MethodPost:
			var req godo.TagCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode create tag: %v", err)
			}
			f.createdTags[req.Name] = true
			_, _ = fmt.Fprintf(w, `{"tag":{"name":%q}}`, req.Name)
			return
		case strings.HasPrefix(r.URL.Path, "/v2/tags/") && strings.HasSuffix(r.URL.Path, "/resources"):
			tag := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v2/tags/"), "/resources")
			var req godo.TagResourcesRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode tag resources: %v", err)
			}
			for _, resource := range req.Resources {
				key := fmt.Sprintf("%s|%s|%s", tag, resource.Type, resource.ID)
				switch r.Method {
				case http.MethodPost:
					f.tagged[key] = true
				case http.MethodDelete:
					f.untagged[key] = true
				default:
					t.Fatalf("unexpected tag resources method %s", r.Method)
				}
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = fmt.Fprintf(w, `{"id":"not_implemented","message":"%s %s not handled"}`, r.Method, r.URL.Path)
	}
}

func writeOwnershipResource(t *testing.T, w http.ResponseWriter, kind string, resource ownershipResource) {
	t.Helper()
	if resource.id == "" {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"id":"not_found","message":"missing"}`))
		return
	}
	tags := jsonStringArray(resource.tags)
	switch kind {
	case "droplet":
		_, _ = fmt.Fprintf(w, `{"droplet":{"id":%s,"name":%q,"status":"active","size":{"slug":"s-1vcpu-1gb"},"region":{"slug":"nyc3"},"tags":%s}}`, resource.id, resource.name, tags)
	case "volume":
		_, _ = fmt.Fprintf(w, `{"volume":{"id":%q,"name":%q,"tags":%s}}`, resource.id, resource.name, tags)
	case "database":
		engine := resource.engine
		if engine == "" {
			engine = "pg"
		}
		_, _ = fmt.Fprintf(w, `{"database":{"id":%q,"name":%q,"engine":%q,"tags":%s}}`, resource.id, resource.name, engine, tags)
	}
}

func newProviderForOwnershipTest(t *testing.T, api *ownershipFakeAPI) (*DOProvider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(api.handler(t))
	t.Cleanup(srv.Close)

	client := godo.NewClient(srv.Client())
	base, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatalf("parse httptest URL: %v", err)
	}
	client.BaseURL = base
	return &DOProvider{client: client, region: "nyc3", drivers: map[string]interfaces.ResourceDriver{
		"infra.droplet":  drivers.NewDropletDriver(client, "nyc3"),
		"infra.volume":   drivers.NewVolumeDriver(client, "nyc3"),
		"infra.database": drivers.NewDatabaseDriver(client, "nyc3"),
		"infra.cache":    drivers.NewCacheDriver(client, "nyc3"),
	}}, srv
}
