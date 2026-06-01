package internal

import (
	"context"
	"sort"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	"github.com/digitalocean/godo"
)

func (s *doIaCServer) ListRegions(ctx context.Context, _ *pb.ListRegionsRequest) (*pb.ListRegionsResponse, error) {
	if s != nil && s.provider != nil && s.provider.client != nil {
		regions, err := listDigitalOceanRegions(ctx, s.provider.client)
		if err != nil {
			return nil, err
		}
		return providerRegionsResponse(regions), nil
	}
	return providerRegionsResponse(digitalOceanFallbackRegionNames()), nil
}

func listDigitalOceanRegions(ctx context.Context, client *godo.Client) ([]string, error) {
	opts := &godo.ListOptions{Page: 1, PerPage: 200}
	var regions []string
	for {
		page, resp, err := client.Regions.List(ctx, opts)
		if err != nil {
			return nil, drivers.WrapGodoError(err)
		}
		for _, region := range page {
			if region.Slug != "" && region.Available {
				regions = append(regions, region.Slug)
			}
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		current, err := resp.Links.CurrentPage()
		if err != nil {
			return nil, err
		}
		opts.Page = current + 1
	}
	return regions, nil
}

func digitalOceanFallbackRegionNames() []string {
	regions := make([]string, 0, len(zoneToGroup))
	for name := range zoneToGroup {
		regions = append(regions, name)
	}
	return regions
}

func providerRegionsResponse(regions []string) *pb.ListRegionsResponse {
	regions = append([]string(nil), regions...)
	sort.Strings(regions)

	out := make([]*pb.ProviderRegion, 0, len(regions))
	for _, name := range regions {
		out = append(out, &pb.ProviderRegion{Name: name, DisplayName: name})
	}
	return &pb.ListRegionsResponse{Regions: out}
}
