package controlplanehandoff

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-control-plane/descriptors"
	descriptorspb "github.com/GoCodeAlone/workflow-plugin-control-plane/descriptors/pb"
	"google.golang.org/protobuf/proto"
)

func TestAppPlatformDryRunHandoffValidatesAgainstControlPlaneContract(t *testing.T) {
	capability := AppPlatformDryRunCapability()
	if capability.ProviderPluginID != "workflow-plugin-digitalocean" {
		t.Fatalf("provider id = %q", capability.ProviderPluginID)
	}
	if capability.ProviderPluginVersion != "v2.0.15" {
		t.Fatalf("provider version = %q", capability.ProviderPluginVersion)
	}
	if capability.CapabilityID != "app-platform-dry-run" {
		t.Fatalf("capability id = %q", capability.CapabilityID)
	}
	if capability.CapabilityVersion != "v1" {
		t.Fatalf("capability version = %q", capability.CapabilityVersion)
	}
	if capability.RequiresCredentials {
		t.Fatal("dry-run fixture must not require DigitalOcean credentials")
	}
	if capability.InputSchemaDigest != SchemaDigest(AppPlatformDryRunInputSchemaJSON) {
		t.Fatalf("schema digest = %q, want digest of canonical schema", capability.InputSchemaDigest)
	}

	ref := NewAppPlatformDryRunHandoff("nonce-001", "idem-001")
	if err := descriptors.ValidateProviderHandoffRef(ref); err != nil {
		t.Fatalf("control-plane handoff ref invalid: %v", err)
	}
	if err := ValidateAppPlatformDryRunHandoff(ref, map[string]struct{}{}); err != nil {
		t.Fatalf("digitalocean handoff fixture invalid: %v", err)
	}

	manifest, err := os.ReadFile("../../plugin.json")
	if err != nil {
		t.Fatalf("read plugin manifest: %v", err)
	}
	if !strings.Contains(string(manifest), "/v2.0.15/") {
		t.Fatal("plugin manifest downloads must carry v2.0.15 evidence for the fixture provider version")
	}
}

func TestAppPlatformDryRunHandoffRejectsInvalidBindings(t *testing.T) {
	tests := map[string]struct {
		mutate func(*descriptorspb.ProviderHandoffRef)
		want   string
	}{
		"provider id skew": {
			mutate: func(ref *descriptorspb.ProviderHandoffRef) { ref.ProviderPluginId = "workflow-plugin-aws" },
			want:   "provider_plugin_id",
		},
		"provider version skew": {
			mutate: func(ref *descriptorspb.ProviderHandoffRef) { ref.ProviderPluginVersion = "v9.9.9" },
			want:   "provider_plugin_version",
		},
		"capability id skew": {
			mutate: func(ref *descriptorspb.ProviderHandoffRef) { ref.CapabilityId = "app-platform-apply" },
			want:   "capability_id",
		},
		"capability version skew": {
			mutate: func(ref *descriptorspb.ProviderHandoffRef) { ref.CapabilityVersion = "v2" },
			want:   "capability_version",
		},
		"schema digest mismatch": {
			mutate: func(ref *descriptorspb.ProviderHandoffRef) { ref.InputSchemaDigest = SchemaDigest(`{"type":"object"}`) },
			want:   "input_schema_digest",
		},
		"invalid nonce shape": {
			mutate: func(ref *descriptorspb.ProviderHandoffRef) { ref.ActionNonce = "bad nonce" },
			want:   "action_nonce",
		},
		"invalid idempotency key shape": {
			mutate: func(ref *descriptorspb.ProviderHandoffRef) { ref.IdempotencyKey = "bad key" },
			want:   "idempotency_key",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			ref := proto.Clone(NewAppPlatformDryRunHandoff("nonce-001", "idem-001")).(*descriptorspb.ProviderHandoffRef)
			tt.mutate(ref)
			err := ValidateAppPlatformDryRunHandoff(ref, map[string]struct{}{})
			if err == nil {
				t.Fatal("expected validation to fail")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q missing %q", err, tt.want)
			}
		})
	}
}

func TestAppPlatformDryRunHandoffRejectsReplayAndCredentialRequiredPath(t *testing.T) {
	seen := map[string]struct{}{}
	ref := NewAppPlatformDryRunHandoff("nonce-001", "idem-001")
	if err := ValidateAppPlatformDryRunHandoff(ref, seen); err != nil {
		t.Fatalf("first validation: %v", err)
	}
	err := ValidateAppPlatformDryRunHandoff(ref, seen)
	if err == nil || !strings.Contains(err.Error(), "idempotency_key") {
		t.Fatalf("replay error = %v, want idempotency_key failure", err)
	}

	credentialRequired := AppPlatformDryRunCapability()
	credentialRequired.RequiresCredentials = true
	err = ValidateProviderHandoff(ref, credentialRequired, map[string]struct{}{})
	if err == nil || !strings.Contains(err.Error(), "requires_credentials") {
		t.Fatalf("credential-required error = %v, want requires_credentials failure", err)
	}
}

func TestCommandPluginDoesNotDependOnControlPlanePackage(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "./cmd/plugin")
	cmd.Dir = filepath.Clean(filepath.Join("..", ".."))
	cmd.Env = append(envWithoutGOWORK(), "GOWORK=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps ./cmd/plugin: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "github.com/GoCodeAlone/workflow-plugin-control-plane") {
		t.Fatalf("cmd/plugin runtime dependencies include workflow-plugin-control-plane:\n%s", out)
	}
}

func envWithoutGOWORK() []string {
	env := os.Environ()
	filtered := env[:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "GOWORK=") {
			continue
		}
		filtered = append(filtered, kv)
	}
	return filtered
}
