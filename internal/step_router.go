package internal

// step_router.go wires pipeline step support into doIaCServer so the DO
// plugin can serve step.iac_logs alongside its typed IaC gRPC contract.
//
// Architecture:
//
//  doIaCServer now implements pb.PluginServiceServer (step-related methods +
//  GetContractRegistry). The workflow SDK (v0.51.6+) auto-detects this in
//  registerIaCServicesOnly and registers doIaCServer as the PluginService,
//  bypassing the minimal iacPluginServiceBridge stub. This lets the host
//  (wfctl ExternalPluginAdapter) call GetStepTypes / CreateStep /
//  ExecuteStep / DestroyStep on the same gRPC connection used for IaC RPCs.
//
// GetContractRegistry delegates to sdk.BuildContractRegistry so typed-IaC
// discovery (Enumerator, DriftDetector, etc.) keeps working alongside steps.

import (
	"context"
	"fmt"
	"sync"

	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/steps"
)

// compile-time guard: doIaCServer must satisfy pb.PluginServiceServer.
var _ pb.PluginServiceServer = (*doIaCServer)(nil)

// stepRegistry holds live step instances keyed by their handle ID.
// doIaCServer embeds stepRegistry by value; it is initialised lazily on first
// CreateStep call. mu guards both instances and grpcSrv.
type stepRegistry struct {
	mu        sync.RWMutex
	instances map[string]sdk.StepInstance
	grpcSrv   *grpc.Server // set by SetGRPCServer; used for GetContractRegistry
}

// SetGRPCServer stores the *grpc.Server reference so that GetContractRegistry
// can call sdk.BuildContractRegistry. Called from cmd/plugin/main.go or test
// wiring BEFORE any gRPC call is dispatched. In production the go-plugin
// framework creates the server before calling iacGRPCPlugin.GRPCServer, so
// there is no race: registration (and therefore SetGRPCServer) completes
// before any host RPC arrives.
func (s *doIaCServer) SetGRPCServer(srv *grpc.Server) {
	s.mu.Lock()
	s.grpcSrv = srv
	s.mu.Unlock()
}

// ── pb.PluginServiceServer methods ──────────────────────────────────────────

// GetManifest is intentionally unimplemented — the bridge guard does NOT get
// hit (doIaCServer IS the PluginService), so the engine falls back to the
// disk-loaded plugin.json via the Unimplemented gRPC status. This is the
// same documented behaviour as iacPluginServiceBridge when ManifestProvider
// is nil.
func (s *doIaCServer) GetManifest(_ context.Context, _ *emptypb.Empty) (*pb.Manifest, error) {
	return nil, status.Error(codes.Unimplemented, "manifest not embedded; engine falls back to disk plugin.json")
}

// GetContractRegistry returns the typed-IaC service registrations from the
// underlying gRPC server. This is the critical forward-compat hook: without
// it the host's ExternalPluginAdapter cannot discover IaC services.
// Returns FailedPrecondition when SetGRPCServer has not been called yet, so
// mis-wiring is surfaced immediately rather than silently hiding IaC contracts.
func (s *doIaCServer) GetContractRegistry(_ context.Context, _ *emptypb.Empty) (*pb.ContractRegistry, error) {
	s.mu.RLock()
	srv := s.grpcSrv
	s.mu.RUnlock()
	if srv == nil {
		return nil, status.Error(codes.FailedPrecondition, "GetContractRegistry: SetGRPCServer was not called — plugin wiring incomplete")
	}
	return sdk.BuildContractRegistry(srv), nil
}

// GetStepTypes returns the step type names provided by this plugin.
func (s *doIaCServer) GetStepTypes(_ context.Context, _ *emptypb.Empty) (*pb.TypeList, error) {
	return &pb.TypeList{Types: []string{"step.iac_logs"}}, nil
}

// CreateStep instantiates a step.iac_logs instance for the given config and
// stores it under a newly-allocated handle ID.
func (s *doIaCServer) CreateStep(_ context.Context, req *pb.CreateStepRequest) (*pb.HandleResponse, error) {
	if req.GetType() != "step.iac_logs" {
		return &pb.HandleResponse{Error: fmt.Sprintf("unknown step type %q", req.GetType())}, nil
	}

	cfg := make(map[string]any)
	if req.GetConfig() != nil {
		cfg = req.GetConfig().AsMap()
	}

	factory := steps.NewIaCLogsFactory(s.provider.AppsClient())
	inst, err := factory.CreateStep(req.GetType(), req.GetName(), cfg)
	if err != nil {
		return &pb.HandleResponse{Error: err.Error()}, nil //nolint:nilerr // app error in response field
	}

	handleID := newHandleID()
	s.mu.Lock()
	if s.instances == nil {
		s.instances = make(map[string]sdk.StepInstance)
	}
	s.instances[handleID] = inst
	s.mu.Unlock()

	return &pb.HandleResponse{HandleId: handleID}, nil
}

// ExecuteStep dispatches the step identified by req.HandleId.
func (s *doIaCServer) ExecuteStep(ctx context.Context, req *pb.ExecuteStepRequest) (*pb.ExecuteStepResponse, error) {
	s.mu.RLock()
	inst, ok := s.instances[req.GetHandleId()]
	s.mu.RUnlock()
	if !ok {
		return &pb.ExecuteStepResponse{Error: fmt.Sprintf("unknown step handle: %s", req.GetHandleId())}, nil
	}

	// Convert proto maps to Go maps.
	triggerData := protoStructToMap(req.GetTriggerData())
	stepOutputs := protoStepOutputsToMap(req.GetStepOutputs())
	current := protoStructToMap(req.GetCurrent())
	metadata := protoStructToMap(req.GetMetadata())
	cfg := protoStructToMap(req.GetConfig())

	result, err := inst.Execute(ctx, triggerData, stepOutputs, current, metadata, cfg)
	if err != nil {
		return &pb.ExecuteStepResponse{Error: err.Error()}, nil //nolint:nilerr // app error in response field
	}
	if result == nil {
		return &pb.ExecuteStepResponse{}, nil
	}

	outStruct, encErr := structpb.NewStruct(result.Output)
	if encErr != nil {
		return &pb.ExecuteStepResponse{Error: fmt.Sprintf("encode step output: %v", encErr)}, nil //nolint:nilerr // app error in response field
	}
	return &pb.ExecuteStepResponse{
		Output:       outStruct,
		StopPipeline: result.StopPipeline,
	}, nil
}

// DestroyStep removes the step instance identified by req.HandleId.
func (s *doIaCServer) DestroyStep(_ context.Context, req *pb.HandleRequest) (*pb.ErrorResponse, error) {
	s.mu.Lock()
	_, ok := s.instances[req.GetHandleId()]
	if ok {
		delete(s.instances, req.GetHandleId())
	}
	s.mu.Unlock()
	if !ok {
		return &pb.ErrorResponse{Error: fmt.Sprintf("unknown step handle: %s", req.GetHandleId())}, nil
	}
	return &pb.ErrorResponse{}, nil
}

// ── Remaining pb.PluginServiceServer stubs ──────────────────────────────────
// These are required for the interface but are not used by this plugin.

func (s *doIaCServer) GetModuleTypes(_ context.Context, _ *emptypb.Empty) (*pb.TypeList, error) {
	// Return Unimplemented so the engine falls back to the disk plugin.json for
	// module-type discovery (iac.provider). An empty TypeList would hide the
	// advertised module type from gRPC-based capability discovery.
	return nil, status.Error(codes.Unimplemented, "module types served from disk plugin.json")
}

func (s *doIaCServer) GetTriggerTypes(_ context.Context, _ *emptypb.Empty) (*pb.TypeList, error) {
	return &pb.TypeList{}, nil
}

func (s *doIaCServer) GetModuleSchemas(_ context.Context, _ *emptypb.Empty) (*pb.ModuleSchemaList, error) {
	return &pb.ModuleSchemaList{}, nil
}

func (s *doIaCServer) CreateModule(_ context.Context, _ *pb.CreateModuleRequest) (*pb.HandleResponse, error) {
	return &pb.HandleResponse{Error: "this plugin does not provide module types"}, nil
}

func (s *doIaCServer) InitModule(_ context.Context, _ *pb.HandleRequest) (*pb.ErrorResponse, error) {
	return &pb.ErrorResponse{Error: "no module instances"}, nil
}

func (s *doIaCServer) StartModule(_ context.Context, _ *pb.HandleRequest) (*pb.ErrorResponse, error) {
	return &pb.ErrorResponse{Error: "no module instances"}, nil
}

func (s *doIaCServer) StopModule(_ context.Context, _ *pb.HandleRequest) (*pb.ErrorResponse, error) {
	return &pb.ErrorResponse{Error: "no module instances"}, nil
}

func (s *doIaCServer) DestroyModule(_ context.Context, _ *pb.HandleRequest) (*pb.ErrorResponse, error) {
	return &pb.ErrorResponse{Error: "no module instances"}, nil
}

func (s *doIaCServer) InvokeService(_ context.Context, _ *pb.InvokeServiceRequest) (*pb.InvokeServiceResponse, error) {
	return nil, status.Error(codes.Unimplemented, "string-dispatch InvokeService is not supported")
}

func (s *doIaCServer) DeliverMessage(_ context.Context, _ *pb.DeliverMessageRequest) (*pb.DeliverMessageResponse, error) {
	return nil, status.Error(codes.Unimplemented, "message delivery is not supported")
}

func (s *doIaCServer) GetConfigFragment(_ context.Context, _ *emptypb.Empty) (*pb.ConfigFragmentResponse, error) {
	return &pb.ConfigFragmentResponse{}, nil
}

func (s *doIaCServer) GetAsset(_ context.Context, _ *pb.GetAssetRequest) (*pb.GetAssetResponse, error) {
	return &pb.GetAssetResponse{Error: "assets not provided"}, nil
}

func (s *doIaCServer) ConfigureCallback(_ context.Context, _ *pb.ConfigureCallbackRequest) (*pb.ErrorResponse, error) {
	return &pb.ErrorResponse{Error: "callback not supported"}, nil
}

func (s *doIaCServer) CreateTrigger(_ context.Context, _ *pb.CreateTriggerRequest) (*pb.HandleResponse, error) {
	return &pb.HandleResponse{Error: "this plugin does not provide trigger types"}, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

// protoStructToMap converts a *structpb.Struct to map[string]any.
// Returns nil when s is nil.
func protoStructToMap(s *structpb.Struct) map[string]any {
	if s == nil {
		return nil
	}
	return s.AsMap()
}

// protoStepOutputsToMap converts the step_outputs field from a gRPC execute
// request to the Go map form expected by sdk.StepInstance.Execute.
func protoStepOutputsToMap(m map[string]*structpb.Struct) map[string]map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]map[string]any, len(m))
	for k, v := range m {
		out[k] = protoStructToMap(v)
	}
	return out
}

// newHandleID returns a unique string handle for a step instance.
// Uses a simple incrementing counter rather than a UUID to avoid pulling in
// github.com/google/uuid as a direct dependency.
var (
	handleMu      sync.Mutex
	handleCounter uint64
)

func newHandleID() string {
	handleMu.Lock()
	handleCounter++
	id := handleCounter
	handleMu.Unlock()
	return fmt.Sprintf("do-step-%d", id)
}
