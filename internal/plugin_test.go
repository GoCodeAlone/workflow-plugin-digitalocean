package internal

import (
	"testing"

	dopb "github.com/GoCodeAlone/workflow-plugin-digitalocean/proto"
	externalPb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
	"google.golang.org/protobuf/types/known/anypb"
)

// compile-time interface checks
var (
	_ sdk.PluginProvider      = (*doPlugin)(nil)
	_ sdk.TypedModuleProvider = (*doPlugin)(nil)
	_ sdk.ContractProvider    = (*doPlugin)(nil)
)

func TestPlugin_Manifest(t *testing.T) {
	p := NewDOPlugin()
	m := p.Manifest()
	if m.Name != "workflow-plugin-digitalocean" {
		t.Errorf("Name = %q, want %q", m.Name, "workflow-plugin-digitalocean")
	}
	if m.Version == "" {
		t.Error("expected non-empty Version")
	}
}

// TestPlugin_ContractRegistry verifies that ContractRegistry returns a strict
// module descriptor for "iac.provider" with the correct config message name
// and an embedded FileDescriptorSet.
func TestPlugin_ContractRegistry(t *testing.T) {
	p := NewDOPlugin().(*doPlugin)
	reg := p.ContractRegistry()

	if reg == nil {
		t.Fatal("ContractRegistry returned nil")
	}
	if len(reg.Contracts) != 1 {
		t.Fatalf("expected 1 contract descriptor, got %d", len(reg.Contracts))
	}

	d := reg.Contracts[0]
	if d.Kind != externalPb.ContractKind_CONTRACT_KIND_MODULE {
		t.Errorf("Kind = %v, want CONTRACT_KIND_MODULE", d.Kind)
	}
	if d.ModuleType != "iac.provider" {
		t.Errorf("ModuleType = %q, want %q", d.ModuleType, "iac.provider")
	}
	if d.Mode != externalPb.ContractMode_CONTRACT_MODE_STRICT_PROTO {
		t.Errorf("Mode = %v, want CONTRACT_MODE_STRICT_PROTO", d.Mode)
	}
	wantConfig := string((&dopb.IacProviderConfig{}).ProtoReflect().Descriptor().FullName())
	if d.ConfigMessage != wantConfig {
		t.Errorf("ConfigMessage = %q, want %q", d.ConfigMessage, wantConfig)
	}
	if reg.FileDescriptorSet == nil {
		t.Error("FileDescriptorSet is nil; expected embedded proto descriptors")
	}
	if len(reg.FileDescriptorSet.File) == 0 {
		t.Error("FileDescriptorSet has no files")
	}
}

// TestPlugin_TypedModuleTypes verifies the typed module type list.
func TestPlugin_TypedModuleTypes(t *testing.T) {
	p := NewDOPlugin().(*doPlugin)
	types := p.TypedModuleTypes()
	if len(types) != 1 || types[0] != "iac.provider" {
		t.Errorf("TypedModuleTypes = %v, want [\"iac.provider\"]", types)
	}
}

// TestPlugin_CreateTypedModule_NilConfigFallsBack verifies that a nil typed
// config causes CreateTypedModule to return ErrTypedContractNotHandled so
// the gRPC server falls back to the legacy ModuleProvider path.
func TestPlugin_CreateTypedModule_NilConfigFallsBack(t *testing.T) {
	p := NewDOPlugin().(*doPlugin)
	_, err := p.CreateTypedModule("iac.provider", "mymodule", nil)
	if err == nil {
		t.Fatal("expected ErrTypedContractNotHandled, got nil")
	}
	if !isErrTypedContractNotHandled(err) {
		t.Errorf("expected ErrTypedContractNotHandled, got %v", err)
	}
}

// TestPlugin_CreateTypedModule_UnknownType verifies that unknown types are
// rejected with ErrTypedContractNotHandled.
func TestPlugin_CreateTypedModule_UnknownType(t *testing.T) {
	p := NewDOPlugin().(*doPlugin)
	packed, err := anypb.New(&dopb.IacProviderConfig{Token: "tok"})
	if err != nil {
		t.Fatalf("anypb.New: %v", err)
	}
	_, err = p.CreateTypedModule("unknown.type", "m", packed)
	if err == nil {
		t.Fatal("expected error for unknown type, got nil")
	}
	if !isErrTypedContractNotHandled(err) {
		t.Errorf("expected ErrTypedContractNotHandled, got %v", err)
	}
}

// TestPlugin_CreateTypedModule_TypeMismatch verifies that a type mismatch in
// the Any payload is caught before calling Initialize.
func TestPlugin_CreateTypedModule_TypeMismatch(t *testing.T) {
	p := NewDOPlugin().(*doPlugin)
	// Pack a different message type.
	wrongMsg := &externalPb.ContractDescriptor{}
	packed, err := anypb.New(wrongMsg)
	if err != nil {
		t.Fatalf("anypb.New: %v", err)
	}
	_, err = p.CreateTypedModule("iac.provider", "m", packed)
	if err == nil {
		t.Fatal("expected type mismatch error, got nil")
	}
}

// TestPlugin_CreateTypedModule_MissingToken verifies that an IacProviderConfig
// without a token causes initialization to fail with a descriptive error.
func TestPlugin_CreateTypedModule_MissingToken(t *testing.T) {
	p := NewDOPlugin().(*doPlugin)
	packed, err := anypb.New(&dopb.IacProviderConfig{})
	if err != nil {
		t.Fatalf("anypb.New: %v", err)
	}
	_, err = p.CreateTypedModule("iac.provider", "m", packed)
	if err == nil {
		t.Fatal("expected error for missing token, got nil")
	}
}

// TestPlugin_CreateTypedModule_ValidConfig verifies that a correctly populated
// IacProviderConfig results in a non-nil ModuleInstance.
func TestPlugin_CreateTypedModule_ValidConfig(t *testing.T) {
	p := NewDOPlugin().(*doPlugin)
	packed, err := anypb.New(&dopb.IacProviderConfig{Token: "fake-token-for-test"})
	if err != nil {
		t.Fatalf("anypb.New: %v", err)
	}
	inst, err := p.CreateTypedModule("iac.provider", "m", packed)
	if err != nil {
		t.Fatalf("CreateTypedModule returned error: %v", err)
	}
	if inst == nil {
		t.Fatal("expected non-nil ModuleInstance")
	}
}

// isErrTypedContractNotHandled checks whether the error (possibly wrapped) is
// or wraps sdk.ErrTypedContractNotHandled.
func isErrTypedContractNotHandled(err error) bool {
	target := sdk.ErrTypedContractNotHandled
	for err != nil {
		if err == target {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
