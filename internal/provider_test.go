package internal

import (
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
)

// compile-time interface check
var _ interfaces.IaCProvider = (*DOProvider)(nil)

func TestDOProvider_Name(t *testing.T) {
	p := NewDOProvider()
	if p.Name() != "digitalocean" {
		t.Errorf("Name = %q, want %q", p.Name(), "digitalocean")
	}
}

func TestDOProvider_Capabilities(t *testing.T) {
	p := NewDOProvider()
	caps := p.Capabilities()
	if len(caps) == 0 {
		t.Fatal("expected non-empty capabilities")
	}
	types := make(map[string]bool)
	for _, c := range caps {
		types[c.ResourceType] = true
	}
	required := []string{
		"infra.container_service",
		"infra.k8s_cluster",
		"infra.database",
		"infra.cache",
		"infra.load_balancer",
		"infra.vpc",
		"infra.firewall",
		"infra.dns",
		"infra.storage",
		"infra.registry",
		"infra.certificate",
		"infra.droplet",
		"infra.iam_role",
		"infra.api_gateway",
	}
	for _, rt := range required {
		if !types[rt] {
			t.Errorf("missing capability: %s", rt)
		}
	}
}

func TestDOProvider_Initialize_MissingToken(t *testing.T) {
	p := NewDOProvider()
	err := p.Initialize(t.Context(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing token")
	}
}

func TestDOProvider_ResolveSizing(t *testing.T) {
	p := NewDOProvider()
	result, err := p.ResolveSizing("infra.database", interfaces.SizeM, nil)
	if err != nil {
		t.Fatalf("ResolveSizing: %v", err)
	}
	if result.InstanceType != "db-s-2vcpu-4gb" {
		t.Errorf("InstanceType = %q, want %q", result.InstanceType, "db-s-2vcpu-4gb")
	}
}

func TestDOProvider_ResolveSizing_NoopType(t *testing.T) {
	p := NewDOProvider()
	result, err := p.ResolveSizing("infra.vpc", interfaces.SizeM, nil)
	if err != nil {
		t.Fatalf("ResolveSizing vpc: %v", err)
	}
	if result.InstanceType != "n/a" {
		t.Errorf("InstanceType = %q, want n/a", result.InstanceType)
	}
}

func TestDOProvider_ResolveSizing_UnknownReturnsError(t *testing.T) {
	p := NewDOProvider()
	_, err := p.ResolveSizing("infra.unknown_thing", interfaces.SizeM, nil)
	if err == nil {
		t.Fatal("expected error for unknown resource type")
	}
}

func TestDOProvider_ResourceDriver_Unknown(t *testing.T) {
	p := NewDOProvider()
	_, err := p.ResourceDriver("infra.unknown_thing")
	if err == nil {
		t.Fatal("expected error for unknown resource type")
	}
}

func TestDOProvider_SupportedCanonicalKeys(t *testing.T) {
	p := NewDOProvider()
	keys := p.SupportedCanonicalKeys()
	if len(keys) == 0 {
		t.Fatal("SupportedCanonicalKeys returned empty slice")
	}
	keySet := make(map[string]bool, len(keys))
	for _, k := range keys {
		keySet[k] = true
	}

	// Keys the DO provider actively maps in this release.
	supported := []string{
		"name", "region", "image", "http_port", "instance_count", "size",
		"env_vars", "env_vars_secret", "autoscaling", "routes", "health_check",
		"liveness_check", "cors", "internal_ports", "build_command", "run_command",
		"dockerfile_path", "source_dir", "termination", "domains", "alerts",
		"log_destinations", "ingress", "egress", "maintenance", "vpc_ref",
		"jobs", "workers", "static_sites", "provider_specific",
	}
	for _, k := range supported {
		if !keySet[k] {
			t.Errorf("SupportedCanonicalKeys missing expected key %q", k)
		}
	}

	// "sidecars" is not yet mapped (Task 37); must NOT appear until then.
	if keySet["sidecars"] {
		t.Error("SupportedCanonicalKeys must not include \"sidecars\" until Task 37 lands")
	}
}

func TestConfigHash_Deterministic(t *testing.T) {
	cfg := map[string]any{"b": 2, "a": 1, "c": "three"}
	h1 := configHash(cfg)
	h2 := configHash(cfg)
	if h1 != h2 {
		t.Errorf("configHash not deterministic: %q != %q", h1, h2)
	}
}

func TestConfigHash_DifferentConfigs(t *testing.T) {
	h1 := configHash(map[string]any{"engine": "pg", "size": "db-s-1vcpu-2gb"})
	h2 := configHash(map[string]any{"engine": "pg", "size": "db-s-2vcpu-4gb"})
	if h1 == h2 {
		t.Error("expected different hashes for different configs")
	}
}

func TestConfigHash_Empty(t *testing.T) {
	h := configHash(nil)
	if h != "" {
		t.Errorf("expected empty hash for nil config, got %q", h)
	}
}
