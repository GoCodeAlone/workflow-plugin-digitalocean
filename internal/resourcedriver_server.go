// Package internal — typed pb.ResourceDriverServer implementation
// (Task 11 of the strict-contracts force-cutover plan,
// docs/plans/2026-05-10-strict-contracts-force-cutover.md, rev5).
//
// The pb.ResourceDriverServer surface is a single gRPC service that
// dispatches per-resource-type CRUD by carrying resource_type on
// every RPC. This file extends *doIaCServer (declared in iacserver.go)
// with the 9 RPC methods Required by the interface; routing happens by
// looking up the per-type interfaces.ResourceDriver via
// *DOProvider.ResourceDriver(resourceType) and forwarding the call.
//
// Once *doIaCServer satisfies pb.ResourceDriverServer at the Go type
// level, sdk.RegisterAllIaCProviderServices auto-registers it
// (iacserver.go: type-assertion gate). No manual Register* call needed.
package internal

import (
	"context"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/GoCodeAlone/workflow/interfaces"
	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
)

// Embed pb.UnimplementedResourceDriverServer on doIaCServer so the
// gRPC forward-compat contract is satisfied AND the SDK type-assert
// in sdk.RegisterAllIaCProviderServices succeeds. Declared in this
// file rather than iacserver.go to keep the optional-service embed
// co-located with the methods that override it — same pattern the
// pb codegen documents on the Unimplemented*Server types.
//
// (The base struct is doIaCServer in iacserver.go; the embed is added
// by Go's struct-extension rules at the field level. Cannot embed
// types in two struct definitions, so this file uses an interface
// satisfaction check + method definitions; the embed lives in
// iacserver.go as part of the doIaCServer composite.)

// Compile-time guard: doIaCServer satisfies pb.ResourceDriverServer.
// A signature drift in iac.proto fails the build at this line rather
// than at first RPC dispatch.
var _ pb.ResourceDriverServer = (*doIaCServer)(nil)

// resolveResourceDriver looks up the per-type driver registered on
// the underlying *DOProvider. Returns a typed gRPC error with
// codes.NotFound when the resource_type is not registered, so
// callers see the same wire-shape regardless of whether the type
// is unknown or the provider has not been initialized.
func (s *doIaCServer) resolveResourceDriver(resourceType string) (interfaces.ResourceDriver, error) {
	if resourceType == "" {
		return nil, status.Error(codes.InvalidArgument, "digitalocean ResourceDriver: resource_type is required")
	}
	d, err := s.provider.ResourceDriver(resourceType)
	if err != nil {
		// Surface as NotFound so the wfctl host can distinguish
		// "no such resource type" from "driver crashed".
		return nil, status.Errorf(codes.NotFound, "digitalocean ResourceDriver: %v", err)
	}
	return d, nil
}

// Create dispatches the typed RPC to the per-type driver's
// interfaces.ResourceDriver.Create. The provider-specific config is
// JSON-encoded in spec.config_json (per iac.proto §Hard invariants);
// specFromPB unmarshals it into the map[string]any the driver expects.
func (s *doIaCServer) Create(ctx context.Context, req *pb.ResourceCreateRequest) (*pb.ResourceCreateResponse, error) {
	driver, err := s.resolveResourceDriver(req.GetResourceType())
	if err != nil {
		return nil, err
	}
	spec, err := specFromPB(req.GetSpec())
	if err != nil {
		return nil, fmt.Errorf("digitalocean ResourceDriver(%s).Create: decode spec: %w", req.GetResourceType(), err)
	}
	out, err := driver.Create(ctx, spec)
	if err != nil {
		return nil, err
	}
	pbOut, err := outputToPB(out)
	if err != nil {
		return nil, fmt.Errorf("digitalocean ResourceDriver(%s).Create: encode response: %w", req.GetResourceType(), err)
	}
	return &pb.ResourceCreateResponse{Output: pbOut}, nil
}

// Read dispatches to interfaces.ResourceDriver.Read.
func (s *doIaCServer) Read(ctx context.Context, req *pb.ResourceReadRequest) (*pb.ResourceReadResponse, error) {
	driver, err := s.resolveResourceDriver(req.GetResourceType())
	if err != nil {
		return nil, err
	}
	out, err := driver.Read(ctx, refFromPB(req.GetRef()))
	if err != nil {
		return nil, err
	}
	pbOut, err := outputToPB(out)
	if err != nil {
		return nil, fmt.Errorf("digitalocean ResourceDriver(%s).Read: encode response: %w", req.GetResourceType(), err)
	}
	return &pb.ResourceReadResponse{Output: pbOut}, nil
}

// Update dispatches to interfaces.ResourceDriver.Update.
func (s *doIaCServer) Update(ctx context.Context, req *pb.ResourceUpdateRequest) (*pb.ResourceUpdateResponse, error) {
	driver, err := s.resolveResourceDriver(req.GetResourceType())
	if err != nil {
		return nil, err
	}
	spec, err := specFromPB(req.GetSpec())
	if err != nil {
		return nil, fmt.Errorf("digitalocean ResourceDriver(%s).Update: decode spec: %w", req.GetResourceType(), err)
	}
	out, err := driver.Update(ctx, refFromPB(req.GetRef()), spec)
	if err != nil {
		return nil, err
	}
	pbOut, err := outputToPB(out)
	if err != nil {
		return nil, fmt.Errorf("digitalocean ResourceDriver(%s).Update: encode response: %w", req.GetResourceType(), err)
	}
	return &pb.ResourceUpdateResponse{Output: pbOut}, nil
}

// Delete dispatches to interfaces.ResourceDriver.Delete. The pb
// response is empty (the only reportable signal is the error).
func (s *doIaCServer) Delete(ctx context.Context, req *pb.ResourceDeleteRequest) (*pb.ResourceDeleteResponse, error) {
	driver, err := s.resolveResourceDriver(req.GetResourceType())
	if err != nil {
		return nil, err
	}
	if err := driver.Delete(ctx, refFromPB(req.GetRef())); err != nil {
		return nil, err
	}
	return &pb.ResourceDeleteResponse{}, nil
}

// Diff dispatches to interfaces.ResourceDriver.Diff.
func (s *doIaCServer) Diff(ctx context.Context, req *pb.ResourceDiffRequest) (*pb.ResourceDiffResponse, error) {
	driver, err := s.resolveResourceDriver(req.GetResourceType())
	if err != nil {
		return nil, err
	}
	desired, err := specFromPB(req.GetDesired())
	if err != nil {
		return nil, fmt.Errorf("digitalocean ResourceDriver(%s).Diff: decode desired: %w", req.GetResourceType(), err)
	}
	current, err := outputFromPB(req.GetCurrent())
	if err != nil {
		return nil, fmt.Errorf("digitalocean ResourceDriver(%s).Diff: decode current: %w", req.GetResourceType(), err)
	}
	result, err := driver.Diff(ctx, desired, current)
	if err != nil {
		return nil, err
	}
	pbResult, err := diffResultToPB(result)
	if err != nil {
		return nil, fmt.Errorf("digitalocean ResourceDriver(%s).Diff: encode response: %w", req.GetResourceType(), err)
	}
	return &pb.ResourceDiffResponse{Result: pbResult}, nil
}

// Scale dispatches to interfaces.ResourceDriver.Scale. The replicas
// pb field is int32; the Go interface accepts int. Sign-extension
// is safe because replicas is always non-negative in practice.
func (s *doIaCServer) Scale(ctx context.Context, req *pb.ResourceScaleRequest) (*pb.ResourceScaleResponse, error) {
	driver, err := s.resolveResourceDriver(req.GetResourceType())
	if err != nil {
		return nil, err
	}
	out, err := driver.Scale(ctx, refFromPB(req.GetRef()), int(req.GetReplicas()))
	if err != nil {
		return nil, err
	}
	pbOut, err := outputToPB(out)
	if err != nil {
		return nil, fmt.Errorf("digitalocean ResourceDriver(%s).Scale: encode response: %w", req.GetResourceType(), err)
	}
	return &pb.ResourceScaleResponse{Output: pbOut}, nil
}

// HealthCheck dispatches to interfaces.ResourceDriver.HealthCheck.
func (s *doIaCServer) HealthCheck(ctx context.Context, req *pb.ResourceHealthCheckRequest) (*pb.ResourceHealthCheckResponse, error) {
	driver, err := s.resolveResourceDriver(req.GetResourceType())
	if err != nil {
		return nil, err
	}
	result, err := driver.HealthCheck(ctx, refFromPB(req.GetRef()))
	if err != nil {
		return nil, err
	}
	pbResult := healthResultToPB(result)
	return &pb.ResourceHealthCheckResponse{Result: pbResult}, nil
}

// SensitiveKeys dispatches to interfaces.ResourceDriver.SensitiveKeys.
// The Go interface signature returns []string — no error path; any
// per-type lookup failure surfaces here at the resolveResourceDriver
// gate.
func (s *doIaCServer) SensitiveKeys(_ context.Context, req *pb.SensitiveKeysRequest) (*pb.SensitiveKeysResponse, error) {
	driver, err := s.resolveResourceDriver(req.GetResourceType())
	if err != nil {
		return nil, err
	}
	keys := driver.SensitiveKeys()
	return &pb.SensitiveKeysResponse{Keys: append([]string(nil), keys...)}, nil
}

// Troubleshoot dispatches to interfaces.Troubleshooter.Troubleshoot
// when the per-type driver implements that optional interface.
// Drivers that don't satisfy Troubleshooter surface a typed gRPC
// codes.Unimplemented — the legitimate negative signal callers
// translate into interfaces.ErrProviderMethodUnimplemented (per
// cmd/wfctl/iac_typed_adapter.go's translateRPCErr) so the original
// failure message is preserved.
func (s *doIaCServer) Troubleshoot(ctx context.Context, req *pb.TroubleshootRequest) (*pb.TroubleshootResponse, error) {
	driver, err := s.resolveResourceDriver(req.GetResourceType())
	if err != nil {
		return nil, err
	}
	tr, ok := driver.(interfaces.Troubleshooter)
	if !ok {
		return nil, status.Errorf(codes.Unimplemented,
			"digitalocean ResourceDriver(%s).Troubleshoot: driver does not implement interfaces.Troubleshooter",
			req.GetResourceType())
	}
	diags, err := tr.Troubleshoot(ctx, refFromPB(req.GetRef()), req.GetFailureMsg())
	if err != nil {
		return nil, err
	}
	out := make([]*pb.Diagnostic, 0, len(diags))
	for _, d := range diags {
		out = append(out, &pb.Diagnostic{
			Id:     d.ID,
			Phase:  d.Phase,
			Cause:  d.Cause,
			At:     timeToPB(d.At),
			Detail: d.Detail,
		})
	}
	return &pb.TroubleshootResponse{Diagnostics: out}, nil
}

// ── Marshalling helpers specific to ResourceDriver responses ────────────────

func diffResultToPB(r *interfaces.DiffResult) (*pb.DiffResult, error) {
	if r == nil {
		return nil, nil
	}
	pbChanges, err := changesToPB(r.Changes)
	if err != nil {
		return nil, err
	}
	return &pb.DiffResult{
		NeedsUpdate:  r.NeedsUpdate,
		NeedsReplace: r.NeedsReplace,
		Changes:      pbChanges,
	}, nil
}

func healthResultToPB(r *interfaces.HealthResult) *pb.HealthResult {
	if r == nil {
		return nil
	}
	return &pb.HealthResult{
		Healthy: r.Healthy,
		Message: r.Message,
	}
}

// outputFromPB is the inverse of outputToPB (which lives in iacserver.go).
// Used by the Diff RPC to decode the typed pb.ResourceOutput.current
// into the Go-side *interfaces.ResourceOutput the driver expects.
func outputFromPB(o *pb.ResourceOutput) (*interfaces.ResourceOutput, error) {
	if o == nil {
		return nil, nil
	}
	outputs, err := unmarshalJSONMap(o.GetOutputsJson())
	if err != nil {
		return nil, err
	}
	sensitive := make(map[string]bool, len(o.GetSensitive()))
	for k, v := range o.GetSensitive() {
		sensitive[k] = v
	}
	return &interfaces.ResourceOutput{
		Name:       o.GetName(),
		Type:       o.GetType(),
		ProviderID: o.GetProviderId(),
		Outputs:    outputs,
		Sensitive:  sensitive,
		Status:     o.GetStatus(),
	}, nil
}

