package internal

import (
	"context"
	"sort"

	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
)

var digitalOceanProviderRegions = []string{
	"ams2", "ams3", "blr1", "fra1", "lon1", "nyc1", "nyc2", "nyc3",
	"sfo1", "sfo2", "sfo3", "sgp1", "syd1", "tor1",
}

func (s *doIaCServer) ListRegions(context.Context, *pb.ListRegionsRequest) (*pb.ListRegionsResponse, error) {
	regions := make([]string, len(digitalOceanProviderRegions))
	copy(regions, digitalOceanProviderRegions)
	sort.Strings(regions)

	out := make([]*pb.ProviderRegion, 0, len(regions))
	for _, name := range regions {
		out = append(out, &pb.ProviderRegion{Name: name, DisplayName: name})
	}
	return &pb.ListRegionsResponse{Regions: out}, nil
}
