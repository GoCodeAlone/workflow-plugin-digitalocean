// Package internal implements the DigitalOcean workflow engine plugin.
package internal

import (
	"context"
	"fmt"

	dopb "github.com/GoCodeAlone/workflow-plugin-digitalocean/proto"
	externalPb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
	"google.golang.org/protobuf/reflect/protodesc"
	descriptorpb "google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/anypb"
)

// Version is set at build time via -ldflags
// "-X github.com/GoCodeAlone/workflow-plugin-digitalocean/internal.Version=X.Y.Z"
var Version = "dev"

const iacProviderModuleType = "iac.provider"

// doPlugin implements sdk.PluginProvider, sdk.ModuleProvider, sdk.TypedModuleProvider,
// and sdk.ContractProvider.
type doPlugin struct{}

// Compile-time interface assertions.
var (
	_ sdk.PluginProvider      = (*doPlugin)(nil)
	_ sdk.ModuleProvider      = (*doPlugin)(nil) // legacy map-based fallback path
	_ sdk.TypedModuleProvider = (*doPlugin)(nil)
	_ sdk.ContractProvider    = (*doPlugin)(nil)
)

// NewDOPlugin returns a new DigitalOcean plugin instance.
func NewDOPlugin() sdk.PluginProvider {
	return &doPlugin{}
}

// Manifest returns plugin metadata.
func (p *doPlugin) Manifest() sdk.PluginManifest {
	return sdk.PluginManifest{
		Name:        "workflow-plugin-digitalocean",
		Version:     Version,
		Author:      "GoCodeAlone",
		Description: "DigitalOcean IaC provider: App Platform, DOKS, databases, load balancers, VPC, firewall, DNS, Spaces, DOCR, certificates, and Droplets",
	}
}

// ── sdk.ModuleProvider (legacy map-based path) ────────────────────────────────

// ModuleTypes returns the module types this plugin exposes.
func (p *doPlugin) ModuleTypes() []string {
	return []string{iacProviderModuleType}
}

// CreateModule creates and initialises a module instance of the given type.
// For "iac.provider", a DOProvider is constructed and initialised with config.
// This legacy map-based path is preserved for backward compatibility with hosts
// that do not send typed_config in CreateModuleRequest.
func (p *doPlugin) CreateModule(typeName, _ string, config map[string]any) (sdk.ModuleInstance, error) {
	if typeName != iacProviderModuleType {
		return nil, fmt.Errorf("digitalocean plugin: unknown module type %q (supported: %s)", typeName, iacProviderModuleType)
	}
	provider := NewDOProvider()
	if err := provider.Initialize(context.Background(), config); err != nil {
		return nil, fmt.Errorf("digitalocean: initialize provider: %w", err)
	}
	return &doModuleInstance{provider: provider}, nil
}

// ── sdk.TypedModuleProvider (strict proto-backed path) ────────────────────────

// TypedModuleTypes returns the module types this plugin provides via strict
// proto contracts.
func (p *doPlugin) TypedModuleTypes() []string {
	return []string{iacProviderModuleType}
}

// CreateTypedModule creates a module instance from a proto-typed config payload.
// It returns sdk.ErrTypedContractNotHandled when config is nil so that the host
// can fall back to the legacy CreateModule path for backward compatibility.
func (p *doPlugin) CreateTypedModule(typeName, _ string, config *anypb.Any) (sdk.ModuleInstance, error) {
	if typeName != iacProviderModuleType {
		return nil, fmt.Errorf("%w: module type %q", sdk.ErrTypedContractNotHandled, typeName)
	}
	// nil typed_config means the host is using the legacy struct path; signal
	// the server to fall back to the ModuleProvider.CreateModule call.
	if config == nil {
		return nil, sdk.ErrTypedContractNotHandled
	}
	var cfg dopb.IacProviderConfig
	// UnmarshalTo validates the type URL and rejects message type mismatches.
	if err := config.UnmarshalTo(&cfg); err != nil {
		return nil, fmt.Errorf("digitalocean plugin: typed config: %w", err)
	}
	provider := NewDOProvider()
	if err := provider.Initialize(context.Background(), iacConfigToMap(&cfg)); err != nil {
		return nil, fmt.Errorf("digitalocean: initialize provider: %w", err)
	}
	// Return doModuleInstance directly so ServiceContextInvoker is preserved.
	return &doModuleInstance{provider: provider}, nil
}

// iacConfigToMap converts an IacProviderConfig proto message to the map[string]any
// shape expected by DOProvider.Initialize, propagating only non-empty fields.
func iacConfigToMap(cfg *dopb.IacProviderConfig) map[string]any {
	m := make(map[string]any, 4)
	if cfg.Token != "" {
		m["token"] = cfg.Token
	}
	if cfg.Region != "" {
		m["region"] = cfg.Region
	}
	if cfg.SpacesAccessKey != "" {
		m["spaces_access_key"] = cfg.SpacesAccessKey
	}
	if cfg.SpacesSecretKey != "" {
		m["spaces_secret_key"] = cfg.SpacesSecretKey
	}
	return m
}

// ── sdk.ContractProvider ──────────────────────────────────────────────────────

// ContractRegistry returns the strict contract descriptors for this plugin.
// The embedded FileDescriptorSet lets the host dynamically encode and decode
// IacProviderConfig without a separate proto registration step.
func (p *doPlugin) ContractRegistry() *externalPb.ContractRegistry {
	prototype := &dopb.IacProviderConfig{}
	fd := prototype.ProtoReflect().Descriptor().ParentFile()
	fds := &descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{
			protodesc.ToFileDescriptorProto(fd),
		},
	}
	return &externalPb.ContractRegistry{
		Contracts: []*externalPb.ContractDescriptor{
			{
				Kind:          externalPb.ContractKind_CONTRACT_KIND_MODULE,
				ModuleType:    iacProviderModuleType,
				Mode:          externalPb.ContractMode_CONTRACT_MODE_STRICT_PROTO,
				ConfigMessage: string(prototype.ProtoReflect().Descriptor().FullName()),
			},
		},
		FileDescriptorSet: fds,
	}
}
