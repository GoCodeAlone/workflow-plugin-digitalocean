package internal

import (
	"context"

	"github.com/GoCodeAlone/workflow/interfaces"
	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
)

func (s *doIaCServer) GetOwner(ctx context.Context, req *pb.GetOwnerRequest) (*pb.GetOwnerResponse, error) {
	owner, err := s.provider.GetOwner(ctx, refFromPB(req.GetRef()))
	if err != nil {
		return nil, err
	}
	return &pb.GetOwnerResponse{Owner: owner.Owner, Source: owner.Source}, nil
}

func (s *doIaCServer) SetOwner(ctx context.Context, req *pb.SetOwnerRequest) (*pb.SetOwnerResponse, error) {
	if err := s.provider.SetOwner(ctx, refFromPB(req.GetRef()), req.GetOwner()); err != nil {
		return nil, err
	}
	return &pb.SetOwnerResponse{}, nil
}

func (s *doIaCServer) ListOwners(ctx context.Context, req *pb.ListOwnersRequest) (*pb.ListOwnersResponse, error) {
	owners, err := s.provider.ListOwners(ctx, interfaces.OwnerFilter{
		Owner:        req.GetOwner(),
		ResourceType: req.GetResourceType(),
	})
	if err != nil {
		return nil, err
	}
	out := make([]*pb.OwnedResource, 0, len(owners))
	for _, owner := range owners {
		out = append(out, &pb.OwnedResource{
			Ref:    refToPB(owner.Ref),
			Owner:  owner.Owner,
			Source: owner.Source,
		})
	}
	return &pb.ListOwnersResponse{Resources: out}, nil
}
