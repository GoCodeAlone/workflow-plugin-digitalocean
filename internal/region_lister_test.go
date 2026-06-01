package internal

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
	"google.golang.org/grpc"
)

func TestDOIaCServer_ListRegions(t *testing.T) {
	resp, err := NewIaCServer().ListRegions(context.Background(), &pb.ListRegionsRequest{EnvName: "prod"})
	if err != nil {
		t.Fatalf("ListRegions: %v", err)
	}
	got := regionNames(resp.GetRegions())
	want := []string{"ams2", "ams3", "blr1", "fra1", "lon1", "nyc1", "nyc2", "nyc3", "sfo1", "sfo2", "sfo3", "sgp1", "syd1", "tor1"}
	if !sameStrings(got, want) {
		t.Fatalf("regions = %v, want %v", got, want)
	}
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
