package controlplanehandoff

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/GoCodeAlone/workflow-plugin-control-plane/descriptors"
	descriptorspb "github.com/GoCodeAlone/workflow-plugin-control-plane/descriptors/pb"
)

const AppPlatformDryRunInputSchemaJSON = `{"$schema":"https://json-schema.org/draft/2020-12/schema","additionalProperties":false,"properties":{"environmentRef":{"pattern":"^envref://[a-z0-9][a-z0-9._/-]*$","type":"string"},"imageDigest":{"pattern":"^sha256:[0-9a-f]{64}$","type":"string"},"serviceRef":{"pattern":"^serviceref://[a-z0-9][a-z0-9._/-]*$","type":"string"}},"required":["environmentRef","serviceRef","imageDigest"],"type":"object"}`

type Capability struct {
	ProviderPluginID      string
	ProviderPluginVersion string
	CapabilityID          string
	CapabilityVersion     string
	InputSchemaDigest     string
	RequiresCredentials   bool
}

func AppPlatformDryRunCapability() Capability {
	return Capability{
		ProviderPluginID:      "workflow-plugin-digitalocean",
		ProviderPluginVersion: "v2.0.15",
		CapabilityID:          "app-platform-dry-run",
		CapabilityVersion:     "v1",
		InputSchemaDigest:     SchemaDigest(AppPlatformDryRunInputSchemaJSON),
		RequiresCredentials:   false,
	}
}

func SchemaDigest(schema string) string {
	sum := sha256.Sum256([]byte(schema))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func NewAppPlatformDryRunHandoff(actionNonce, idempotencyKey string) *descriptorspb.ProviderHandoffRef {
	capability := AppPlatformDryRunCapability()
	return &descriptorspb.ProviderHandoffRef{
		ProviderPluginId:      capability.ProviderPluginID,
		ProviderPluginVersion: capability.ProviderPluginVersion,
		CapabilityId:          capability.CapabilityID,
		CapabilityVersion:     capability.CapabilityVersion,
		InputSchemaDigest:     capability.InputSchemaDigest,
		ActionNonce:           actionNonce,
		IdempotencyKey:        idempotencyKey,
	}
}

func ValidateAppPlatformDryRunHandoff(ref *descriptorspb.ProviderHandoffRef, seen map[string]struct{}) error {
	return ValidateProviderHandoff(ref, AppPlatformDryRunCapability(), seen)
}

func ValidateProviderHandoff(ref *descriptorspb.ProviderHandoffRef, capability Capability, seen map[string]struct{}) error {
	if err := descriptors.ValidateProviderHandoffRef(ref); err != nil {
		return err
	}
	if capability.RequiresCredentials {
		return fmt.Errorf("requires_credentials is not allowed for no-account dry-run handoff")
	}
	if ref.GetProviderPluginId() != capability.ProviderPluginID {
		return fmt.Errorf("provider_plugin_id = %q, want %q", ref.GetProviderPluginId(), capability.ProviderPluginID)
	}
	if ref.GetProviderPluginVersion() != capability.ProviderPluginVersion {
		return fmt.Errorf("provider_plugin_version = %q, want %q", ref.GetProviderPluginVersion(), capability.ProviderPluginVersion)
	}
	if ref.GetCapabilityId() != capability.CapabilityID {
		return fmt.Errorf("capability_id = %q, want %q", ref.GetCapabilityId(), capability.CapabilityID)
	}
	if ref.GetCapabilityVersion() != capability.CapabilityVersion {
		return fmt.Errorf("capability_version = %q, want %q", ref.GetCapabilityVersion(), capability.CapabilityVersion)
	}
	if ref.GetInputSchemaDigest() != capability.InputSchemaDigest {
		return fmt.Errorf("input_schema_digest = %q, want %q", ref.GetInputSchemaDigest(), capability.InputSchemaDigest)
	}
	if seen != nil {
		key := ref.GetIdempotencyKey()
		if _, ok := seen[key]; ok {
			return fmt.Errorf("idempotency_key %q was already used", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}
