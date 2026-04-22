// Package internal implements the DigitalOcean workflow engine plugin.
package internal

import (
	"context"
	"fmt"

	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
)

// doPlugin implements sdk.PluginProvider and sdk.ModuleProvider.
type doPlugin struct{}

// NewDOPlugin returns a new DigitalOcean plugin instance.
func NewDOPlugin() sdk.PluginProvider {
	return &doPlugin{}
}

// Manifest returns plugin metadata.
func (p *doPlugin) Manifest() sdk.PluginManifest {
	return sdk.PluginManifest{
		Name:        "workflow-plugin-digitalocean",
		Version:     "0.5.2",
		Author:      "GoCodeAlone",
		Description: "DigitalOcean IaC provider: App Platform, DOKS, databases, load balancers, VPC, firewall, DNS, Spaces, DOCR, certificates, and Droplets",
	}
}

// ModuleTypes returns the module types this plugin exposes.
func (p *doPlugin) ModuleTypes() []string {
	return []string{"iac.provider"}
}

// CreateModule creates and initialises a module instance of the given type.
// For "iac.provider", a DOProvider is constructed and initialised with config.
func (p *doPlugin) CreateModule(typeName, _ string, config map[string]any) (sdk.ModuleInstance, error) {
	if typeName != "iac.provider" {
		return nil, fmt.Errorf("digitalocean plugin: unknown module type %q (supported: iac.provider)", typeName)
	}
	provider := NewDOProvider()
	if err := provider.Initialize(context.Background(), config); err != nil {
		return nil, fmt.Errorf("digitalocean: initialize provider: %w", err)
	}
	return &doModuleInstance{provider: provider}, nil
}
