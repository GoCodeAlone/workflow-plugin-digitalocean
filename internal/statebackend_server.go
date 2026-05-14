// Package internal — typed pb.IaCStateBackendServer implementation.
//
// Per decisions/0035 (one type carries both concerns), doIaCServer ALSO
// serves the typed IaC state-backend contract: it persists IaC state via a
// SpacesIaCStateStore (ported from workflow core) and answers ListBackendNames
// with the single backend name "spaces".
//
// Hard invariants (strict-contracts force-cutover):
//   - NO structpb.Struct on the wire — the free-form Outputs / Config
//     map[string]any fields of IaCState cross as JSON bytes (outputs_json,
//     config_json), converted locally via encoding/json below.
//   - The store is lazily constructed: the host delivers the iac.state module
//     config via the Configure RPC (decisions/0036); until then GetState/etc.
//     return a clear FailedPrecondition error rather than panicking.
package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/statebackend"
	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// doStateBackendName is the single iac.state backend name this plugin serves.
const doStateBackendName = "spaces"

// stateBackend holds the lazily-constructed spaces state store plus the guard
// that builds it once. It is embedded into doIaCServer.
type stateBackend struct {
	mu    sync.Mutex
	store *statebackend.SpacesIaCStateStore
}

// resolveStore returns the configured store, or a codes.FailedPrecondition
// gRPC status if the host has not yet delivered the iac.state config for this
// plugin process via the Configure RPC.
//
// An unset store yields FailedPrecondition — distinct from a generic Internal
// error — so the engine can tell "backend not configured" apart from a real
// storage failure, rather than the server panicking on a nil store.
func (b *stateBackend) resolveStore() (*statebackend.SpacesIaCStateStore, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.store == nil {
		return nil, status.Error(codes.FailedPrecondition,
			"digitalocean state backend: spaces backend is not configured")
	}
	return b.store, nil
}

// setStateStore injects the backing store. Used by the Configure handler and tests.
func (b *stateBackend) setStateStore(s *statebackend.SpacesIaCStateStore) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.store = s
}

// spacesConfig is the iac.state module config the host delivers via the
// Configure RPC. The JSON keys mirror exactly the config keys the in-core
// spaces switch case in workflow core's iac_module.go read.
type spacesConfig struct {
	Region    string `json:"region"`
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix"`
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
	Endpoint  string `json:"endpoint"`
}

// ── pb.IaCStateBackendServer methods (on doIaCServer) ───────────────────────

// Configure decodes the host-delivered iac.state module config and lazily
// constructs the spaces state store, satisfying decisions/0036's host-config
// plumbing. Until Configure runs, the state RPCs return FailedPrecondition
// (see resolveStore). The config_json bytes carry the module config
// map[string]any per the iac.proto JSON-bytes invariant.
func (s *doIaCServer) Configure(_ context.Context, req *pb.ConfigureRequest) (*pb.ConfigureResponse, error) {
	if req.GetBackendName() != doStateBackendName {
		return nil, status.Errorf(codes.InvalidArgument,
			"digitalocean state backend: Configure for backend %q — this plugin serves only %q",
			req.GetBackendName(), doStateBackendName)
	}
	var cfg spacesConfig
	if err := json.Unmarshal(req.GetConfigJson(), &cfg); err != nil {
		return nil, status.Errorf(codes.InvalidArgument,
			"digitalocean state backend: decode Configure config: %v", err)
	}
	if cfg.Bucket == "" {
		return nil, status.Error(codes.InvalidArgument,
			"digitalocean state backend: spaces backend requires 'bucket' config")
	}
	store, err := statebackend.NewSpacesIaCStateStore(
		cfg.Region, cfg.Bucket, cfg.Prefix, cfg.AccessKey, cfg.SecretKey, cfg.Endpoint)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument,
			"digitalocean state backend: construct spaces store: %v", err)
	}
	s.stateBackend.setStateStore(store)
	return &pb.ConfigureResponse{}, nil
}

// GetState retrieves a state record by resource ID.
func (s *doIaCServer) GetState(ctx context.Context, req *pb.GetStateRequest) (*pb.GetStateResponse, error) {
	store, err := s.stateBackend.resolveStore()
	if err != nil {
		return nil, err
	}
	st, err := store.GetState(ctx, req.GetResourceId())
	if err != nil {
		return nil, err
	}
	if st == nil {
		return &pb.GetStateResponse{Exists: false}, nil
	}
	pbState, err := iacStateToPB(st)
	if err != nil {
		return nil, fmt.Errorf("digitalocean state backend: encode GetState response: %w", err)
	}
	return &pb.GetStateResponse{State: pbState, Exists: true}, nil
}

// SaveState inserts or replaces a state record.
func (s *doIaCServer) SaveState(ctx context.Context, req *pb.SaveStateRequest) (*pb.SaveStateResponse, error) {
	store, err := s.stateBackend.resolveStore()
	if err != nil {
		return nil, err
	}
	st, err := iacStateFromPB(req.GetState())
	if err != nil {
		return nil, fmt.Errorf("digitalocean state backend: decode SaveState request: %w", err)
	}
	if err := store.SaveState(ctx, st); err != nil {
		return nil, err
	}
	return &pb.SaveStateResponse{}, nil
}

// ListStates returns all state records matching the provided key=value filter.
func (s *doIaCServer) ListStates(ctx context.Context, req *pb.ListStatesRequest) (*pb.ListStatesResponse, error) {
	store, err := s.stateBackend.resolveStore()
	if err != nil {
		return nil, err
	}
	states, err := store.ListStates(ctx, req.GetFilter())
	if err != nil {
		return nil, err
	}
	pbStates := make([]*pb.IaCState, 0, len(states))
	for _, st := range states {
		pbState, err := iacStateToPB(st)
		if err != nil {
			return nil, fmt.Errorf("digitalocean state backend: encode ListStates response: %w", err)
		}
		pbStates = append(pbStates, pbState)
	}
	return &pb.ListStatesResponse{States: pbStates}, nil
}

// DeleteState removes a state record by resource ID.
func (s *doIaCServer) DeleteState(ctx context.Context, req *pb.DeleteStateRequest) (*pb.DeleteStateResponse, error) {
	store, err := s.stateBackend.resolveStore()
	if err != nil {
		return nil, err
	}
	if err := store.DeleteState(ctx, req.GetResourceId()); err != nil {
		return nil, err
	}
	return &pb.DeleteStateResponse{}, nil
}

// Lock acquires an exclusive lock for the given resource ID.
func (s *doIaCServer) Lock(ctx context.Context, req *pb.LockRequest) (*pb.LockResponse, error) {
	store, err := s.stateBackend.resolveStore()
	if err != nil {
		return nil, err
	}
	if err := store.Lock(ctx, req.GetResourceId()); err != nil {
		return nil, err
	}
	return &pb.LockResponse{}, nil
}

// Unlock releases the lock for the given resource ID.
func (s *doIaCServer) Unlock(ctx context.Context, req *pb.UnlockRequest) (*pb.UnlockResponse, error) {
	store, err := s.stateBackend.resolveStore()
	if err != nil {
		return nil, err
	}
	if err := store.Unlock(ctx, req.GetResourceId()); err != nil {
		return nil, err
	}
	return &pb.UnlockResponse{}, nil
}

// ListBackendNames reports the iac.state backend names this plugin serves.
func (s *doIaCServer) ListBackendNames(_ context.Context, _ *pb.ListBackendNamesRequest) (*pb.ListBackendNamesResponse, error) {
	return &pb.ListBackendNamesResponse{BackendNames: []string{doStateBackendName}}, nil
}

// ── IaCState ⇄ pb.IaCState converters ───────────────────────────────────────
//
// Local re-implementation of workflow core's unexported iacStateToProto /
// iacStateFromProto. The Outputs / Config map[string]any fields cross the wire
// as JSON bytes (outputs_json / config_json) per the iac.proto invariant — NO
// structpb.

func iacStateToPB(st *statebackend.IaCState) (*pb.IaCState, error) {
	if st == nil {
		return nil, nil
	}
	outputsJSON, err := marshalIaCMap(st.Outputs)
	if err != nil {
		return nil, fmt.Errorf("marshal outputs: %w", err)
	}
	configJSON, err := marshalIaCMap(st.Config)
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	return &pb.IaCState{
		ResourceId:   st.ResourceID,
		ResourceType: st.ResourceType,
		Provider:     st.Provider,
		ProviderRef:  st.ProviderRef,
		ProviderId:   st.ProviderID,
		ConfigHash:   st.ConfigHash,
		Status:       st.Status,
		OutputsJson:  outputsJSON,
		ConfigJson:   configJSON,
		Dependencies: append([]string(nil), st.Dependencies...),
		CreatedAt:    st.CreatedAt,
		UpdatedAt:    st.UpdatedAt,
		Error:        st.Error,
	}, nil
}

func iacStateFromPB(s *pb.IaCState) (*statebackend.IaCState, error) {
	if s == nil {
		return nil, fmt.Errorf("iac state must not be nil")
	}
	outputs, err := unmarshalIaCMap(s.GetOutputsJson())
	if err != nil {
		return nil, fmt.Errorf("unmarshal outputs: %w", err)
	}
	config, err := unmarshalIaCMap(s.GetConfigJson())
	if err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	return &statebackend.IaCState{
		ResourceID:   s.GetResourceId(),
		ResourceType: s.GetResourceType(),
		Provider:     s.GetProvider(),
		ProviderRef:  s.GetProviderRef(),
		ProviderID:   s.GetProviderId(),
		ConfigHash:   s.GetConfigHash(),
		Status:       s.GetStatus(),
		Outputs:      outputs,
		Config:       config,
		Dependencies: append([]string(nil), s.GetDependencies()...),
		CreatedAt:    s.GetCreatedAt(),
		UpdatedAt:    s.GetUpdatedAt(),
		Error:        s.GetError(),
	}, nil
}

func marshalIaCMap(m map[string]any) ([]byte, error) {
	if m == nil {
		return nil, nil
	}
	return json.Marshal(m)
}

func unmarshalIaCMap(b []byte) (map[string]any, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}
