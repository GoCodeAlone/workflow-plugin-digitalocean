// Package internal implements the DigitalOcean workflow engine plugin.
package internal

import (
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
)

// doPlugin implements sdk.PluginProvider.
type doPlugin struct{}

// NewDOPlugin returns a new DigitalOcean plugin instance.
func NewDOPlugin() sdk.PluginProvider {
	return &doPlugin{}
}

// Manifest returns plugin metadata.
func (p *doPlugin) Manifest() sdk.PluginManifest {
	return sdk.PluginManifest{
		Name:        "workflow-plugin-digitalocean",
		Version:     "0.1.0",
		Author:      "GoCodeAlone",
		Description: "DigitalOcean IaC provider: App Platform, DOKS, databases, load balancers, VPC, firewall, DNS, Spaces, DOCR, certificates, and Droplets",
	}
}
