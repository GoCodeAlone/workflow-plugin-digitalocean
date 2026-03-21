package internal

import (
	"testing"

	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
)

// compile-time interface check
var _ sdk.PluginProvider = (*doPlugin)(nil)

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
