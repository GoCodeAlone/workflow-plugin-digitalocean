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
		"infra.load_balancer",
		"infra.vpc",
		"infra.firewall",
		"infra.dns",
		"infra.storage",
		"infra.registry",
		"infra.certificate",
		"infra.droplet",
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

func TestDOProvider_ResourceDriver_Unknown(t *testing.T) {
	p := NewDOProvider()
	_, err := p.ResourceDriver("infra.unknown_thing")
	if err == nil {
		t.Fatal("expected error for unknown resource type")
	}
}
