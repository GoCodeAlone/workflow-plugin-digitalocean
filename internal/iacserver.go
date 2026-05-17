// Package internal — typed pb.IaCProvider*Server implementation.
//
// Per the strict-contracts force-cutover plan (Task 9):
// docs/plans/2026-05-10-strict-contracts-force-cutover.md, rev5.
//
// doIaCServer is the SERVER side of the typed IaC contract. It satisfies
// pb.IaCProviderRequiredServer plus the six optional pb.IaCProvider*Server
// interfaces by delegating each typed RPC to the matching Go-interface
// method on the underlying *DOProvider. Marshalling between pb messages
// and the Go interfaces.* types lives in marshalling helpers below; the
// shape mirrors the inverse direction implemented by
// cmd/wfctl/iac_typed_adapter.go in workflow itself, so a single canonical
// pb<->Go mapping holds across both ends of the gRPC bridge.
//
// Hard invariants (per cycle 4 force-cutover §Acceptance):
//   - NO structpb.Struct, NO Any.UnmarshalTo on the wire — provider-
//     specific config / outputs cross as JSON bytes (config_json,
//     outputs_json).
//   - REQUIRED service methods MUST be implemented; the SDK type-assert
//     in sdk.RegisterAllIaCProviderServices fails at plugin startup
//     otherwise.
//   - OPTIONAL services are auto-registered when satisfied at the Go
//     interface level — no manual Register* call required.
package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/GoCodeAlone/workflow/interfaces"
	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// doIaCServer wraps *DOProvider and exposes the typed pb.IaCProvider*Server
// + ResourceDriverServer surface. The Unimplemented*Server embeds satisfy
// the gRPC forward-compat contract (proto codegen requires them) and let
// the SDK type-assert succeed when only some of the optional methods are
// overridden — though doIaCServer overrides every one for parity with the
// underlying *DOProvider's interface coverage.
//
// doIaCServer also implements pb.PluginServiceServer (step-related methods)
// so the workflow SDK (v0.51.6+) can auto-register it as the PluginService,
// enabling step.iac_logs and any future step types to be served alongside
// the typed IaC contract on the same gRPC connection. See step_router.go.
type doIaCServer struct {
	pb.UnimplementedIaCProviderRequiredServer
	pb.UnimplementedIaCProviderEnumeratorServer
	pb.UnimplementedIaCProviderDriftDetectorServer
	pb.UnimplementedIaCProviderCredentialRevokerServer
	pb.UnimplementedIaCProviderMigrationRepairerServer
	pb.UnimplementedIaCProviderValidatorServer
	pb.UnimplementedIaCProviderDriftConfigDetectorServer
	// pb.UnimplementedIaCProviderFinalizerServer satisfies the
	// mustEmbedUnimplementedIaCProviderFinalizerServer() forward-compat
	// requirement on pb.IaCProviderFinalizerServer (workflow#695 Phase 2.5).
	// The actual FinalizeApply method is overridden below; the embed is
	// required by the gRPC codegen contract.
	pb.UnimplementedIaCProviderFinalizerServer
	// pb.UnimplementedResourceDriverServer satisfies the
	// mustEmbedUnimplementedResourceDriverServer() forward-compat
	// requirement on pb.ResourceDriverServer; the per-type CRUD
	// dispatch methods are declared in resourcedriver_server.go
	// (Task 11 of the strict-contracts force-cutover plan).
	pb.UnimplementedResourceDriverServer
	// pb.UnimplementedPluginServiceServer satisfies the
	// mustEmbedUnimplementedPluginServiceServer() forward-compat
	// requirement on pb.PluginServiceServer. All PluginService methods
	// that doIaCServer actually handles are overridden in step_router.go;
	// remaining methods fall back to the Unimplemented stubs here.
	pb.UnimplementedPluginServiceServer
	pb.UnimplementedIaCStateBackendServer

	provider *DOProvider

	// stepRegistry holds the embedded step management state (instances map,
	// grpcSrv ref, and mu) used by the pb.PluginServiceServer implementation
	// in step_router.go.
	stepRegistry

	// stateBackend serves the typed pb.IaCStateBackendServer surface
	// (spaces backend). Per decisions/0035, this one type carries both the
	// IaC-provider and the IaC-state-backend concerns. The backing store is
	// constructed lazily via the Configure RPC — see internal/statebackend_server.go.
	stateBackend stateBackend
}

// newDOIaCServer constructs a typed-IaC server backed by the given
// *DOProvider. The provider is NOT initialized here; Initialize is the
// first typed RPC the host sends after the gRPC dial completes.
func newDOIaCServer(provider *DOProvider) *doIaCServer {
	return &doIaCServer{provider: provider}
}

// NewIaCServer is the package entrypoint used by cmd/plugin/main.go. It
// constructs a fresh *DOProvider and wraps it in the typed pb.IaCProvider*
// server surface. The returned value is suitable to pass to
// sdk.ServeIaCPlugin; the SDK auto-registers every typed gRPC service
// the server satisfies via Go type-assertion at plugin startup.
func NewIaCServer() *doIaCServer {
	return newDOIaCServer(NewDOProvider())
}

// Compile-time guards: every typed server interface this DO plugin
// advertises MUST be satisfied. A signature drift on any of these will
// fail the build at this file rather than at first RPC dispatch.
var (
	_ pb.IaCProviderRequiredServer            = (*doIaCServer)(nil)
	_ pb.IaCProviderEnumeratorServer          = (*doIaCServer)(nil)
	_ pb.IaCProviderDriftDetectorServer       = (*doIaCServer)(nil)
	_ pb.IaCProviderCredentialRevokerServer   = (*doIaCServer)(nil)
	_ pb.IaCProviderMigrationRepairerServer   = (*doIaCServer)(nil)
	_ pb.IaCProviderValidatorServer           = (*doIaCServer)(nil)
	_ pb.IaCProviderDriftConfigDetectorServer = (*doIaCServer)(nil)
	// IaCProviderFinalizer is the workflow#695 Phase 2.5 optional service
	// — DO plugin implements FinalizeApply server-side to host the
	// deferred-flush iteration previously held inline in the v1
	// DOProvider.Apply wrapper. Required by the v2 dispatch declared
	// via ComputePlanVersion="v2" below.
	_ pb.IaCProviderFinalizerServer = (*doIaCServer)(nil)
	// doIaCServer also SERVES the typed IaC state-backend contract (spaces
	// backend). The SDK serve hook auto-registers this via type-assertion at
	// plugin startup — see cmd/plugin/main.go.
	_ pb.IaCStateBackendServer = (*doIaCServer)(nil)
)

// ── Required service methods ────────────────────────────────────────────────

// Initialize unmarshals the JSON-encoded config_json into a map[string]any
// and forwards to *DOProvider.Initialize. Empty config_json yields a nil
// map — which DOProvider rejects with "token required", consistent with
// the legacy pre-cutover dispatch behavior.
func (s *doIaCServer) Initialize(ctx context.Context, req *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	cfg, err := unmarshalJSONMap(req.GetConfigJson())
	if err != nil {
		return nil, fmt.Errorf("digitalocean iacserver: parse Initialize config_json: %w", err)
	}
	if err := s.provider.Initialize(ctx, cfg); err != nil {
		return nil, err
	}
	return &pb.InitializeResponse{}, nil
}

func (s *doIaCServer) Name(_ context.Context, _ *pb.NameRequest) (*pb.NameResponse, error) {
	return &pb.NameResponse{Name: s.provider.Name()}, nil
}

func (s *doIaCServer) Version(_ context.Context, _ *pb.VersionRequest) (*pb.VersionResponse, error) {
	return &pb.VersionResponse{Version: s.provider.Version()}, nil
}

func (s *doIaCServer) Capabilities(_ context.Context, _ *pb.CapabilitiesRequest) (*pb.CapabilitiesResponse, error) {
	caps := s.provider.Capabilities()
	out := make([]*pb.IaCCapabilityDeclaration, 0, len(caps))
	for _, c := range caps {
		// Tier is bounded (1..3) in DOProvider; safe int→int32 conversion.
		// Defensive clamp to int32 range avoids G115 drift if the
		// declaration list grows.
		tier := c.Tier
		if tier < math.MinInt32 {
			tier = math.MinInt32
		} else if tier > math.MaxInt32 {
			tier = math.MaxInt32
		}
		out = append(out, &pb.IaCCapabilityDeclaration{
			ResourceType: c.ResourceType,
			Tier:         int32(tier), //nolint:gosec // G115: clamped above
			Operations:   append([]string(nil), c.Operations...),
		})
	}
	return &pb.CapabilitiesResponse{
		Capabilities: out,
		// ComputePlanVersion="v2" opts this plugin into wfctl's v2 apply
		// dispatch (wfctlhelpers.ApplyPlan + per-action hooks). The
		// deferred-flush iteration previously held inline in the v1
		// DOProvider.Apply wrapper has been hoisted to FinalizeApply (the
		// IaCProviderFinalizer optional service implementation below). Per
		// workflow#695 Phase 2.5 / ADR 0024 / ADR 0040.
		ComputePlanVersion: "v2",
	}, nil
}

// FinalizeApply implements pb.IaCProviderFinalizerServer for the v2
// dispatch path. Inlines the per-driver flush loop from
// DOProvider.Apply (internal/provider.go's post-loop block iterating
// p.drivers and calling deferredUpdater.FlushDeferredUpdates) since
// DOProvider does not expose a public FlushDeferredUpdates method —
// the flush iteration was inline in the v1 Apply wrapper. Per
// workflow#695 Phase 2.5.
//
// Per-driver error attribution is preserved by returning ActionError
// entries on the response, mirroring the v1 wrapper shape that wfctl
// previously read directly from result.Errors via
// ActionError{Resource: resourceType, Action: "deferred_update",
// Error: flushErr.Error()}. wfctl-side OnPlanComplete handler appends
// these entries onward into result.Errors as a "<plan-finalize>" entry,
// preserving the operator-facing diagnostic shape.
//
// Empty errors slice = success (gRPC status OK; wire-status invariant
// per FinalizeApplyResponse godoc).
//
// Best-effort fan-out: per-driver flushes that succeed remain committed
// even if later drivers fail; this method does not coordinate rollback
// across drivers (same semantics as v1 DOProvider.Apply's post-loop
// block). Operators reconcile partial failures from the per-driver
// errors[] entries surfaced by the wfctl-side OnPlanComplete handler.
func (s *doIaCServer) FinalizeApply(ctx context.Context, _ *pb.FinalizeApplyRequest) (*pb.FinalizeApplyResponse, error) {
	var errs []*pb.ActionError
	// Iterate the driver registry directly (not plan.Actions) so that
	// orphaned deferred entries are flushed even when their resource type
	// no longer appears in the current plan (e.g. after a transient flush
	// failure on a prior Apply run). Mirrors v1 wrapper semantics.
	for resourceType, d := range s.provider.drivers {
		du, ok := d.(deferredUpdater)
		if !ok || !du.HasDeferredUpdates() {
			continue
		}
		if flushErr := du.FlushDeferredUpdates(ctx); flushErr != nil {
			errs = append(errs, &pb.ActionError{
				Resource: resourceType,
				Action:   "deferred_update",
				Error:    flushErr.Error(),
			})
		}
	}
	return &pb.FinalizeApplyResponse{Errors: errs}, nil
}

func (s *doIaCServer) Plan(ctx context.Context, req *pb.PlanRequest) (*pb.PlanResponse, error) {
	desired, err := specsFromPB(req.GetDesired())
	if err != nil {
		return nil, fmt.Errorf("digitalocean iacserver: decode Plan desired: %w", err)
	}
	current, err := statesFromPB(req.GetCurrent())
	if err != nil {
		return nil, fmt.Errorf("digitalocean iacserver: decode Plan current: %w", err)
	}
	plan, err := s.provider.Plan(ctx, desired, current)
	if err != nil {
		return nil, err
	}
	pbPlan, err := planToPB(plan)
	if err != nil {
		return nil, fmt.Errorf("digitalocean iacserver: encode Plan response: %w", err)
	}
	return &pb.PlanResponse{Plan: pbPlan}, nil
}

func (s *doIaCServer) Apply(ctx context.Context, req *pb.ApplyRequest) (*pb.ApplyResponse, error) {
	plan, err := planFromPB(req.GetPlan())
	if err != nil {
		return nil, fmt.Errorf("digitalocean iacserver: decode Apply plan: %w", err)
	}
	result, err := s.provider.Apply(ctx, plan)
	if err != nil {
		return nil, err
	}
	pbResult, err := applyResultToPB(result)
	if err != nil {
		return nil, fmt.Errorf("digitalocean iacserver: encode Apply response: %w", err)
	}
	return &pb.ApplyResponse{Result: pbResult}, nil
}

func (s *doIaCServer) Destroy(ctx context.Context, req *pb.DestroyRequest) (*pb.DestroyResponse, error) {
	refs := refsFromPB(req.GetRefs())
	result, err := s.provider.Destroy(ctx, refs)
	if err != nil {
		return nil, err
	}
	return &pb.DestroyResponse{Result: destroyResultToPB(result)}, nil
}

func (s *doIaCServer) Status(ctx context.Context, req *pb.StatusRequest) (*pb.StatusResponse, error) {
	refs := refsFromPB(req.GetRefs())
	statuses, err := s.provider.Status(ctx, refs)
	if err != nil {
		return nil, err
	}
	pbStatuses, err := statusesToPB(statuses)
	if err != nil {
		return nil, fmt.Errorf("digitalocean iacserver: encode Status response: %w", err)
	}
	return &pb.StatusResponse{Statuses: pbStatuses}, nil
}

func (s *doIaCServer) Import(ctx context.Context, req *pb.ImportRequest) (*pb.ImportResponse, error) {
	state, err := s.provider.Import(ctx, req.GetProviderId(), req.GetResourceType())
	if err != nil {
		return nil, err
	}
	pbState, err := stateToPB(state)
	if err != nil {
		return nil, fmt.Errorf("digitalocean iacserver: encode Import response: %w", err)
	}
	return &pb.ImportResponse{State: pbState}, nil
}

func (s *doIaCServer) ResolveSizing(_ context.Context, req *pb.ResolveSizingRequest) (*pb.ResolveSizingResponse, error) {
	sizing, err := s.provider.ResolveSizing(
		req.GetResourceType(),
		interfaces.Size(req.GetSize()),
		hintsFromPB(req.GetHints()),
	)
	if err != nil {
		return nil, err
	}
	pbSizing, err := sizingToPB(sizing)
	if err != nil {
		return nil, fmt.Errorf("digitalocean iacserver: encode ResolveSizing response: %w", err)
	}
	return &pb.ResolveSizingResponse{Sizing: pbSizing}, nil
}

func (s *doIaCServer) BootstrapStateBackend(ctx context.Context, req *pb.BootstrapStateBackendRequest) (*pb.BootstrapStateBackendResponse, error) {
	cfg, err := unmarshalJSONMap(req.GetConfigJson())
	if err != nil {
		return nil, fmt.Errorf("digitalocean iacserver: parse BootstrapStateBackend config_json: %w", err)
	}
	result, err := s.provider.BootstrapStateBackend(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &pb.BootstrapStateBackendResponse{Result: bootstrapResultToPB(result)}, nil
}

// ── Optional services ───────────────────────────────────────────────────────

// EnumerateAll satisfies pb.IaCProviderEnumeratorServer.EnumerateAll.
// Mirrors interfaces.EnumeratorAll on *DOProvider.
func (s *doIaCServer) EnumerateAll(ctx context.Context, req *pb.EnumerateAllRequest) (*pb.EnumerateAllResponse, error) {
	outs, err := s.provider.EnumerateAll(ctx, req.GetResourceType())
	if err != nil {
		return nil, err
	}
	pbOuts := make([]*pb.ResourceOutput, 0, len(outs))
	for _, o := range outs {
		po, err := outputToPB(o)
		if err != nil {
			return nil, fmt.Errorf("digitalocean iacserver: encode EnumerateAll output: %w", err)
		}
		if po != nil {
			pbOuts = append(pbOuts, po)
		}
	}
	return &pb.EnumerateAllResponse{Outputs: pbOuts}, nil
}

// EnumerateByTag satisfies pb.IaCProviderEnumeratorServer.EnumerateByTag.
// Mirrors interfaces.Enumerator on *DOProvider.
func (s *doIaCServer) EnumerateByTag(ctx context.Context, req *pb.EnumerateByTagRequest) (*pb.EnumerateByTagResponse, error) {
	refs, err := s.provider.EnumerateByTag(ctx, req.GetTag())
	if err != nil {
		return nil, err
	}
	return &pb.EnumerateByTagResponse{Refs: refsToPB(refs)}, nil
}

// DetectDrift satisfies pb.IaCProviderDriftDetectorServer. Mirrors
// interfaces.IaCProvider.DetectDrift on *DOProvider.
func (s *doIaCServer) DetectDrift(ctx context.Context, req *pb.DetectDriftRequest) (*pb.DetectDriftResponse, error) {
	refs := refsFromPB(req.GetRefs())
	drifts, err := s.provider.DetectDrift(ctx, refs)
	if err != nil {
		return nil, err
	}
	pbDrifts, err := driftsToPB(drifts)
	if err != nil {
		return nil, fmt.Errorf("digitalocean iacserver: encode DetectDrift response: %w", err)
	}
	return &pb.DetectDriftResponse{Drifts: pbDrifts}, nil
}

// DetectDriftWithSpecs satisfies pb.IaCProviderDriftDetectorServer.
// (Note: the IaCProviderDriftDetector service in iac.proto carries BOTH
// DetectDrift and DetectDriftWithSpecs RPCs; the latter is also exposed
// by the IaCProviderDriftConfigDetector service via DetectDriftConfig.
// Both routes delegate to *DOProvider.DetectDriftWithSpecs.)
func (s *doIaCServer) DetectDriftWithSpecs(ctx context.Context, req *pb.DetectDriftWithSpecsRequest) (*pb.DetectDriftWithSpecsResponse, error) {
	specs, err := specMapFromPB(req.GetSpecs())
	if err != nil {
		return nil, fmt.Errorf("digitalocean iacserver: decode DetectDriftWithSpecs specs: %w", err)
	}
	drifts, err := s.provider.DetectDriftWithSpecs(ctx, refsFromPB(req.GetRefs()), specs)
	if err != nil {
		return nil, err
	}
	pbDrifts, err := driftsToPB(drifts)
	if err != nil {
		return nil, fmt.Errorf("digitalocean iacserver: encode DetectDriftWithSpecs response: %w", err)
	}
	return &pb.DetectDriftWithSpecsResponse{Drifts: pbDrifts}, nil
}

// DetectDriftConfig satisfies pb.IaCProviderDriftConfigDetectorServer.
// Routes to *DOProvider.DetectDriftWithSpecs (the Go interface name
// is DriftConfigDetector.DetectDriftWithSpecs; the typed proto names
// the RPC DetectDriftConfig to keep the wire surface unambiguous).
func (s *doIaCServer) DetectDriftConfig(ctx context.Context, req *pb.DetectDriftConfigRequest) (*pb.DetectDriftConfigResponse, error) {
	specs, err := specMapFromPB(req.GetSpecs())
	if err != nil {
		return nil, fmt.Errorf("digitalocean iacserver: decode DetectDriftConfig specs: %w", err)
	}
	drifts, err := s.provider.DetectDriftWithSpecs(ctx, refsFromPB(req.GetRefs()), specs)
	if err != nil {
		return nil, err
	}
	pbDrifts, err := driftsToPB(drifts)
	if err != nil {
		return nil, fmt.Errorf("digitalocean iacserver: encode DetectDriftConfig response: %w", err)
	}
	return &pb.DetectDriftConfigResponse{Drifts: pbDrifts}, nil
}

// RevokeProviderCredential satisfies pb.IaCProviderCredentialRevokerServer.
// Mirrors interfaces.ProviderCredentialRevoker on *DOProvider.
func (s *doIaCServer) RevokeProviderCredential(ctx context.Context, req *pb.RevokeProviderCredentialRequest) (*pb.RevokeProviderCredentialResponse, error) {
	if err := s.provider.RevokeProviderCredential(ctx, req.GetSource(), req.GetCredentialId()); err != nil {
		return nil, err
	}
	return &pb.RevokeProviderCredentialResponse{}, nil
}

// RepairDirtyMigration satisfies pb.IaCProviderMigrationRepairerServer.
// confirm_force MUST equal interfaces.MigrationRepairConfirmation
// ("FORCE_MIGRATION_METADATA"); the provider re-validates, but we
// surface a typed error here too so callers get a stable wire-shape
// for the safety check.
func (s *doIaCServer) RepairDirtyMigration(ctx context.Context, req *pb.RepairDirtyMigrationRequest) (*pb.RepairDirtyMigrationResponse, error) {
	inner := req.GetRequest()
	if inner == nil {
		return nil, fmt.Errorf("digitalocean iacserver: RepairDirtyMigration request body is nil")
	}
	repairReq := migrationRepairRequestFromPB(inner)
	result, err := s.provider.RepairDirtyMigration(ctx, repairReq)
	if err != nil {
		return nil, err
	}
	return &pb.RepairDirtyMigrationResponse{Result: migrationRepairResultToPB(result)}, nil
}

// ValidatePlan satisfies pb.IaCProviderValidatorServer. Note the Go
// interface returns []PlanDiagnostic only (no error); this method
// therefore never errors at the gRPC layer beyond marshalling failures.
func (s *doIaCServer) ValidatePlan(_ context.Context, req *pb.ValidatePlanRequest) (*pb.ValidatePlanResponse, error) {
	plan, err := planFromPB(req.GetPlan())
	if err != nil {
		return nil, fmt.Errorf("digitalocean iacserver: decode ValidatePlan plan: %w", err)
	}
	diags := s.provider.ValidatePlan(plan)
	out := make([]*pb.PlanDiagnostic, 0, len(diags))
	for _, d := range diags {
		out = append(out, &pb.PlanDiagnostic{
			Severity: planDiagnosticSeverityToPB(d.Severity),
			Resource: d.Resource,
			Field:    d.Field,
			Message:  d.Message,
		})
	}
	return &pb.ValidatePlanResponse{Diagnostics: out}, nil
}

// ── Marshalling helpers (pb ↔ Go) ───────────────────────────────────────────
//
// These mirror the inverse-direction helpers in
// cmd/wfctl/iac_typed_adapter.go (workflow). The pb→Go path is used for
// inbound RPCs (request decode); the Go→pb path is used for outbound
// responses. Keeping the two sides symmetric in shape is deliberate —
// any drift in struct mapping shows up as a test failure on either end.

func unmarshalJSONMap(b []byte) (map[string]any, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func marshalJSONMap(m map[string]any) ([]byte, error) {
	if m == nil {
		return nil, nil
	}
	return json.Marshal(m)
}

func unmarshalJSONAny(b []byte) (any, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func marshalJSONAny(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}

func refToPB(r interfaces.ResourceRef) *pb.ResourceRef {
	return &pb.ResourceRef{Name: r.Name, Type: r.Type, ProviderId: r.ProviderID}
}

func refFromPB(r *pb.ResourceRef) interfaces.ResourceRef {
	if r == nil {
		return interfaces.ResourceRef{}
	}
	return interfaces.ResourceRef{Name: r.GetName(), Type: r.GetType(), ProviderID: r.GetProviderId()}
}

func refsToPB(refs []interfaces.ResourceRef) []*pb.ResourceRef {
	out := make([]*pb.ResourceRef, 0, len(refs))
	for _, r := range refs {
		out = append(out, refToPB(r))
	}
	return out
}

func refsFromPB(refs []*pb.ResourceRef) []interfaces.ResourceRef {
	out := make([]interfaces.ResourceRef, 0, len(refs))
	for _, r := range refs {
		out = append(out, refFromPB(r))
	}
	return out
}

func hintsToPB(h *interfaces.ResourceHints) *pb.ResourceHints {
	if h == nil {
		return nil
	}
	return &pb.ResourceHints{Cpu: h.CPU, Memory: h.Memory, Storage: h.Storage}
}

func hintsFromPB(h *pb.ResourceHints) *interfaces.ResourceHints {
	if h == nil {
		return nil
	}
	return &interfaces.ResourceHints{CPU: h.GetCpu(), Memory: h.GetMemory(), Storage: h.GetStorage()}
}

func specToPB(s interfaces.ResourceSpec) (*pb.ResourceSpec, error) {
	cfgJSON, err := marshalJSONMap(s.Config)
	if err != nil {
		return nil, err
	}
	return &pb.ResourceSpec{
		Name:       s.Name,
		Type:       s.Type,
		ConfigJson: cfgJSON,
		Size:       string(s.Size),
		Hints:      hintsToPB(s.Hints),
		DependsOn:  append([]string(nil), s.DependsOn...),
	}, nil
}

func specFromPB(s *pb.ResourceSpec) (interfaces.ResourceSpec, error) {
	if s == nil {
		return interfaces.ResourceSpec{}, nil
	}
	cfg, err := unmarshalJSONMap(s.GetConfigJson())
	if err != nil {
		return interfaces.ResourceSpec{}, err
	}
	return interfaces.ResourceSpec{
		Name:      s.GetName(),
		Type:      s.GetType(),
		Config:    cfg,
		Size:      interfaces.Size(s.GetSize()),
		Hints:     hintsFromPB(s.GetHints()),
		DependsOn: append([]string(nil), s.GetDependsOn()...),
	}, nil
}

func specsFromPB(specs []*pb.ResourceSpec) ([]interfaces.ResourceSpec, error) {
	out := make([]interfaces.ResourceSpec, 0, len(specs))
	for _, s := range specs {
		gs, err := specFromPB(s)
		if err != nil {
			return nil, err
		}
		out = append(out, gs)
	}
	return out, nil
}

func specMapFromPB(m map[string]*pb.ResourceSpec) (map[string]interfaces.ResourceSpec, error) {
	if len(m) == 0 {
		return nil, nil
	}
	out := make(map[string]interfaces.ResourceSpec, len(m))
	for k, v := range m {
		gs, err := specFromPB(v)
		if err != nil {
			return nil, fmt.Errorf("specs[%s]: %w", k, err)
		}
		out[k] = gs
	}
	return out, nil
}

func stateToPB(st *interfaces.ResourceState) (*pb.ResourceState, error) {
	if st == nil {
		return nil, nil
	}
	appliedJSON, err := marshalJSONMap(st.AppliedConfig)
	if err != nil {
		return nil, err
	}
	outputsJSON, err := marshalJSONMap(st.Outputs)
	if err != nil {
		return nil, err
	}
	return &pb.ResourceState{
		Id:                  st.ID,
		Name:                st.Name,
		Type:                st.Type,
		Provider:            st.Provider,
		ProviderRef:         st.ProviderRef,
		ProviderId:          st.ProviderID,
		ConfigHash:          st.ConfigHash,
		AppliedConfigJson:   appliedJSON,
		AppliedConfigSource: st.AppliedConfigSource,
		OutputsJson:         outputsJSON,
		Dependencies:        append([]string(nil), st.Dependencies...),
		CreatedAt:           timeToPB(st.CreatedAt),
		UpdatedAt:           timeToPB(st.UpdatedAt),
		LastDriftCheck:      timeToPB(st.LastDriftCheck),
	}, nil
}

func stateFromPB(s *pb.ResourceState) (*interfaces.ResourceState, error) {
	if s == nil {
		return nil, nil
	}
	applied, err := unmarshalJSONMap(s.GetAppliedConfigJson())
	if err != nil {
		return nil, err
	}
	outputs, err := unmarshalJSONMap(s.GetOutputsJson())
	if err != nil {
		return nil, err
	}
	return &interfaces.ResourceState{
		ID:                  s.GetId(),
		Name:                s.GetName(),
		Type:                s.GetType(),
		Provider:            s.GetProvider(),
		ProviderRef:         s.GetProviderRef(),
		ProviderID:          s.GetProviderId(),
		ConfigHash:          s.GetConfigHash(),
		AppliedConfig:       applied,
		AppliedConfigSource: s.GetAppliedConfigSource(),
		Outputs:             outputs,
		Dependencies:        append([]string(nil), s.GetDependencies()...),
		CreatedAt:           timeFromPB(s.GetCreatedAt()),
		UpdatedAt:           timeFromPB(s.GetUpdatedAt()),
		LastDriftCheck:      timeFromPB(s.GetLastDriftCheck()),
	}, nil
}

func statesFromPB(states []*pb.ResourceState) ([]interfaces.ResourceState, error) {
	out := make([]interfaces.ResourceState, 0, len(states))
	for _, s := range states {
		gs, err := stateFromPB(s)
		if err != nil {
			return nil, err
		}
		if gs != nil {
			out = append(out, *gs)
		}
	}
	return out, nil
}

func outputToPB(o *interfaces.ResourceOutput) (*pb.ResourceOutput, error) {
	if o == nil {
		return nil, nil
	}
	outputsJSON, err := marshalJSONMap(o.Outputs)
	if err != nil {
		return nil, err
	}
	sensitive := make(map[string]bool, len(o.Sensitive))
	for k, v := range o.Sensitive {
		sensitive[k] = v
	}
	return &pb.ResourceOutput{
		Name:        o.Name,
		Type:        o.Type,
		ProviderId:  o.ProviderID,
		OutputsJson: outputsJSON,
		Sensitive:   sensitive,
		Status:      o.Status,
	}, nil
}

func statusesToPB(ss []interfaces.ResourceStatus) ([]*pb.ResourceStatus, error) {
	out := make([]*pb.ResourceStatus, 0, len(ss))
	for i := range ss {
		o, err := marshalJSONMap(ss[i].Outputs)
		if err != nil {
			return nil, err
		}
		out = append(out, &pb.ResourceStatus{
			Name:        ss[i].Name,
			Type:        ss[i].Type,
			ProviderId:  ss[i].ProviderID,
			Status:      ss[i].Status,
			OutputsJson: o,
		})
	}
	return out, nil
}

func driftClassToPB(c interfaces.DriftClass) pb.DriftClass {
	switch c {
	case interfaces.DriftClassInSync:
		return pb.DriftClass_DRIFT_CLASS_IN_SYNC
	case interfaces.DriftClassGhost:
		return pb.DriftClass_DRIFT_CLASS_GHOST
	case interfaces.DriftClassConfig:
		return pb.DriftClass_DRIFT_CLASS_CONFIG
	default:
		return pb.DriftClass_DRIFT_CLASS_UNKNOWN
	}
}

func driftsToPB(drifts []interfaces.DriftResult) ([]*pb.DriftResult, error) {
	out := make([]*pb.DriftResult, 0, len(drifts))
	for _, d := range drifts {
		expectedJSON, err := marshalJSONMap(d.Expected)
		if err != nil {
			return nil, err
		}
		actualJSON, err := marshalJSONMap(d.Actual)
		if err != nil {
			return nil, err
		}
		out = append(out, &pb.DriftResult{
			Name:         d.Name,
			Type:         d.Type,
			Drifted:      d.Drifted,
			Class:        driftClassToPB(d.Class),
			ExpectedJson: expectedJSON,
			ActualJson:   actualJSON,
			Fields:       append([]string(nil), d.Fields...),
		})
	}
	return out, nil
}

func planActionToPB(a interfaces.PlanAction) (*pb.PlanAction, error) {
	pbSpec, err := specToPB(a.Resource)
	if err != nil {
		return nil, err
	}
	var pbCurrent *pb.ResourceState
	if a.Current != nil {
		pbCurrent, err = stateToPB(a.Current)
		if err != nil {
			return nil, err
		}
	}
	pbChanges, err := changesToPB(a.Changes)
	if err != nil {
		return nil, err
	}
	return &pb.PlanAction{
		Action:             a.Action,
		Resource:           pbSpec,
		Current:            pbCurrent,
		Changes:            pbChanges,
		ResolvedConfigHash: a.ResolvedConfigHash,
	}, nil
}

func planActionFromPB(a *pb.PlanAction) (interfaces.PlanAction, error) {
	if a == nil {
		return interfaces.PlanAction{}, nil
	}
	spec, err := specFromPB(a.GetResource())
	if err != nil {
		return interfaces.PlanAction{}, err
	}
	var current *interfaces.ResourceState
	if a.GetCurrent() != nil {
		current, err = stateFromPB(a.GetCurrent())
		if err != nil {
			return interfaces.PlanAction{}, err
		}
	}
	changes, err := changesFromPB(a.GetChanges())
	if err != nil {
		return interfaces.PlanAction{}, err
	}
	return interfaces.PlanAction{
		Action:             a.GetAction(),
		Resource:           spec,
		Current:            current,
		Changes:            changes,
		ResolvedConfigHash: a.GetResolvedConfigHash(),
	}, nil
}

func changesToPB(changes []interfaces.FieldChange) ([]*pb.FieldChange, error) {
	out := make([]*pb.FieldChange, 0, len(changes))
	for _, c := range changes {
		oldJSON, err := marshalJSONAny(c.Old)
		if err != nil {
			return nil, err
		}
		newJSON, err := marshalJSONAny(c.New)
		if err != nil {
			return nil, err
		}
		out = append(out, &pb.FieldChange{
			Path:     c.Path,
			OldJson:  oldJSON,
			NewJson:  newJSON,
			ForceNew: c.ForceNew,
		})
	}
	return out, nil
}

func changesFromPB(changes []*pb.FieldChange) ([]interfaces.FieldChange, error) {
	out := make([]interfaces.FieldChange, 0, len(changes))
	for _, c := range changes {
		oldVal, err := unmarshalJSONAny(c.GetOldJson())
		if err != nil {
			return nil, err
		}
		newVal, err := unmarshalJSONAny(c.GetNewJson())
		if err != nil {
			return nil, err
		}
		out = append(out, interfaces.FieldChange{
			Path:     c.GetPath(),
			Old:      oldVal,
			New:      newVal,
			ForceNew: c.GetForceNew(),
		})
	}
	return out, nil
}

func planToPB(p *interfaces.IaCPlan) (*pb.IaCPlan, error) {
	if p == nil {
		return nil, nil
	}
	pbActions := make([]*pb.PlanAction, 0, len(p.Actions))
	for i := range p.Actions {
		pa, err := planActionToPB(p.Actions[i])
		if err != nil {
			return nil, err
		}
		pbActions = append(pbActions, pa)
	}
	if p.SchemaVersion < math.MinInt32 || p.SchemaVersion > math.MaxInt32 {
		return nil, fmt.Errorf("digitalocean iacserver: plan SchemaVersion %d out of int32 range", p.SchemaVersion)
	}
	return &pb.IaCPlan{
		Id:            p.ID,
		Actions:       pbActions,
		CreatedAt:     timeToPB(p.CreatedAt),
		DesiredHash:   p.DesiredHash,
		SchemaVersion: int32(p.SchemaVersion), //nolint:gosec // G115: range-checked above
		InputSnapshot: copyStringMap(p.InputSnapshot),
	}, nil
}

func planFromPB(p *pb.IaCPlan) (*interfaces.IaCPlan, error) {
	if p == nil {
		return nil, nil
	}
	actions := make([]interfaces.PlanAction, 0, len(p.GetActions()))
	for _, a := range p.GetActions() {
		pa, err := planActionFromPB(a)
		if err != nil {
			return nil, err
		}
		actions = append(actions, pa)
	}
	return &interfaces.IaCPlan{
		ID:            p.GetId(),
		Actions:       actions,
		CreatedAt:     timeFromPB(p.GetCreatedAt()),
		DesiredHash:   p.GetDesiredHash(),
		SchemaVersion: int(p.GetSchemaVersion()),
		InputSnapshot: copyStringMap(p.GetInputSnapshot()),
	}, nil
}

func applyResultToPB(r *interfaces.ApplyResult) (*pb.ApplyResult, error) {
	if r == nil {
		return nil, nil
	}
	resources := make([]*pb.ResourceOutput, 0, len(r.Resources))
	for i := range r.Resources {
		ro, err := outputToPB(&r.Resources[i])
		if err != nil {
			return nil, err
		}
		if ro != nil {
			resources = append(resources, ro)
		}
	}
	errs := make([]*pb.ActionError, 0, len(r.Errors))
	for _, e := range r.Errors {
		errs = append(errs, &pb.ActionError{Resource: e.Resource, Action: e.Action, Error: e.Error})
	}
	driftReport := make([]*pb.DriftEntry, 0, len(r.InputDriftReport))
	for _, d := range r.InputDriftReport {
		driftReport = append(driftReport, &pb.DriftEntry{
			Name:             d.Name,
			PlanFingerprint:  d.PlanFingerprint,
			ApplyFingerprint: d.ApplyFingerprint,
		})
	}
	return &pb.ApplyResult{
		PlanId:               r.PlanID,
		Resources:            resources,
		Errors:               errs,
		InitialInputSnapshot: copyStringMap(r.InitialInputSnapshot),
		InputDriftReport:     driftReport,
		ReplaceIdMap:         copyStringMap(r.ReplaceIDMap),
	}, nil
}

func destroyResultToPB(r *interfaces.DestroyResult) *pb.DestroyResult {
	if r == nil {
		return nil
	}
	errs := make([]*pb.ActionError, 0, len(r.Errors))
	for _, e := range r.Errors {
		errs = append(errs, &pb.ActionError{Resource: e.Resource, Action: e.Action, Error: e.Error})
	}
	return &pb.DestroyResult{Destroyed: append([]string(nil), r.Destroyed...), Errors: errs}
}

func bootstrapResultToPB(r *interfaces.BootstrapResult) *pb.BootstrapResult {
	if r == nil {
		return nil
	}
	return &pb.BootstrapResult{
		Bucket:   r.Bucket,
		Region:   r.Region,
		Endpoint: r.Endpoint,
		EnvVars:  copyStringMap(r.EnvVars),
	}
}

func sizingToPB(s *interfaces.ProviderSizing) (*pb.ProviderSizing, error) {
	if s == nil {
		return nil, nil
	}
	specsJSON, err := marshalJSONMap(s.Specs)
	if err != nil {
		return nil, err
	}
	return &pb.ProviderSizing{InstanceType: s.InstanceType, SpecsJson: specsJSON}, nil
}

func planDiagnosticSeverityToPB(s interfaces.PlanDiagnosticSeverity) pb.PlanDiagnosticSeverity {
	switch s {
	case interfaces.PlanDiagnosticWarning:
		return pb.PlanDiagnosticSeverity_PLAN_DIAGNOSTIC_WARNING
	case interfaces.PlanDiagnosticError:
		return pb.PlanDiagnosticSeverity_PLAN_DIAGNOSTIC_ERROR
	default:
		return pb.PlanDiagnosticSeverity_PLAN_DIAGNOSTIC_INFO
	}
}

func migrationRepairRequestFromPB(r *pb.MigrationRepairRequest) interfaces.MigrationRepairRequest {
	if r == nil {
		return interfaces.MigrationRepairRequest{}
	}
	timeout := r.GetTimeoutSeconds()
	return interfaces.MigrationRepairRequest{
		AppResourceName:      r.GetAppResourceName(),
		DatabaseResourceName: r.GetDatabaseResourceName(),
		JobImage:             r.GetJobImage(),
		SourceDir:            r.GetSourceDir(),
		ExpectedDirtyVersion: r.GetExpectedDirtyVersion(),
		ForceVersion:         r.GetForceVersion(),
		ThenUp:               r.GetThenUp(),
		UpIfClean:            r.GetUpIfClean(),
		ConfirmForce:         r.GetConfirmForce(),
		Env:                  copyStringMap(r.GetEnv()),
		TimeoutSeconds:       int(timeout),
	}
}

func migrationRepairResultToPB(r *interfaces.MigrationRepairResult) *pb.MigrationRepairResult {
	if r == nil {
		return nil
	}
	diags := make([]*pb.Diagnostic, 0, len(r.Diagnostics))
	for _, d := range r.Diagnostics {
		diags = append(diags, &pb.Diagnostic{
			Id:     d.ID,
			Phase:  d.Phase,
			Cause:  d.Cause,
			At:     timeToPB(d.At),
			Detail: d.Detail,
		})
	}
	return &pb.MigrationRepairResult{
		ProviderJobId: r.ProviderJobID,
		Status:        r.Status,
		Applied:       append([]string(nil), r.Applied...),
		Logs:          r.Logs,
		Diagnostics:   diags,
	}
}

func timeToPB(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func timeFromPB(t *timestamppb.Timestamp) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.AsTime()
}

func copyStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
