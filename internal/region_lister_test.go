package internal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"testing"

	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
	"github.com/digitalocean/godo"
	"google.golang.org/grpc"
)

func TestDOIaCServer_ListRegions(t *testing.T) {
	resp, err := NewIaCServer().ListRegions(context.Background(), &pb.ListRegionsRequest{EnvName: "prod"})
	if err != nil {
		t.Fatalf("ListRegions: %v", err)
	}
	got := regionNames(resp.GetRegions())
	want := append([]string(nil), digitalOceanFallbackRegionNames()...)
	sort.Strings(want)
	if !sameStrings(got, want) {
		t.Fatalf("regions = %v, want %v", got, want)
	}
}

func TestDOIaCServer_ListRegions_ProviderBacked(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/regions" {
			t.Fatalf("path = %q, want /v2/regions", r.URL.Path)
		}
		pages = append(pages, r.URL.Query().Get("page"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "1", "":
			_, _ = w.Write([]byte(`{
				"regions": [
					{"slug": "nyc1", "name": "New York 1", "available": true},
					{"slug": "legacy1", "name": "Legacy", "available": false}
				],
				"links": {"pages": {"next": "` + srvURL(t, r, "2") + `", "last": "` + srvURL(t, r, "2") + `"}}
			}`))
		case "2":
			_, _ = w.Write([]byte(`{
				"regions": [
					{"slug": "sfo3", "name": "San Francisco 3", "available": true}
				],
				"links": {"pages": {"last": "` + srvURL(t, r, "2") + `"}}
			}`))
		default:
			t.Fatalf("unexpected page %q", r.URL.Query().Get("page"))
		}
	}))
	t.Cleanup(srv.Close)

	client := godo.NewClient(srv.Client())
	base, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	client.BaseURL = base

	resp, err := newDOIaCServer(&DOProvider{client: client}).ListRegions(context.Background(), &pb.ListRegionsRequest{})
	if err != nil {
		t.Fatalf("ListRegions: %v", err)
	}
	got := regionNames(resp.GetRegions())
	want := []string{"nyc1", "sfo3"}
	if !sameStrings(got, want) {
		t.Fatalf("regions = %v, want %v", got, want)
	}
	if !sameStrings(pages, []string{"1", "2"}) {
		t.Fatalf("requested pages = %v, want [1 2]", pages)
	}
}

func srvURL(t *testing.T, r *http.Request, page string) string {
	t.Helper()
	u := url.URL{Scheme: "http", Host: r.Host, Path: "/v2/regions"}
	q := u.Query()
	q.Set("page", page)
	u.RawQuery = q.Encode()
	return u.String()
}

func TestDOIaCServer_RegistersRegionLister(t *testing.T) {
	server := grpc.NewServer()
	if err := sdk.RegisterAllIaCProviderServices(server, NewIaCServer()); err != nil {
		t.Fatalf("RegisterAllIaCProviderServices: %v", err)
	}
	if _, ok := server.GetServiceInfo()[pb.IaCProviderRegionLister_ServiceDesc.ServiceName]; !ok {
		t.Fatalf("registered services missing %s", pb.IaCProviderRegionLister_ServiceDesc.ServiceName)
	}
}

func TestPluginManifestAdvertisesRegionLister(t *testing.T) {
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
	if !containsString(manifest.IaCServices, pb.IaCProviderRegionLister_ServiceDesc.ServiceName) {
		t.Fatalf("iacServices missing %s: %v", pb.IaCProviderRegionLister_ServiceDesc.ServiceName, manifest.IaCServices)
	}
}

func regionNames(regions []*pb.ProviderRegion) []string {
	out := make([]string, 0, len(regions))
	for _, region := range regions {
		out = append(out, region.GetName())
		if region.GetDisplayName() == "" {
			out = append(out, "<empty-display>")
		}
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
