package internal

import (
	"fmt"

	"github.com/GoCodeAlone/workflow/interfaces"
)

// doSizingMap maps abstract size tiers to DigitalOcean SKU slugs per resource type.
// Resource types without variable sizing (vpc, firewall, dns, storage, registry,
// certificate, iam_role) use a sentinel value "n/a" — ProviderSizing.InstanceType
// will carry that value so callers know sizing is not applicable.
var doSizingMap = map[string]map[interfaces.Size]string{
	"infra.container_service": {
		interfaces.SizeXS: "apps-s-1vcpu-0.5gb",
		interfaces.SizeS:  "apps-s-1vcpu-1gb",
		interfaces.SizeM:  "apps-s-2vcpu-4gb",
		interfaces.SizeL:  "apps-s-4vcpu-8gb",
		interfaces.SizeXL: "apps-s-8vcpu-16gb",
	},
	"infra.k8s_cluster": {
		interfaces.SizeXS: "s-1vcpu-512mb",
		interfaces.SizeS:  "s-1vcpu-2gb",
		interfaces.SizeM:  "s-2vcpu-4gb",
		interfaces.SizeL:  "s-4vcpu-8gb",
		interfaces.SizeXL: "s-8vcpu-16gb",
	},
	"infra.database": {
		interfaces.SizeXS: "db-s-1vcpu-1gb",
		interfaces.SizeS:  "db-s-1vcpu-2gb",
		interfaces.SizeM:  "db-s-2vcpu-4gb",
		interfaces.SizeL:  "db-s-4vcpu-8gb",
		interfaces.SizeXL: "db-s-8vcpu-16gb",
	},
	"infra.cache": {
		interfaces.SizeXS: "db-s-1vcpu-1gb",
		interfaces.SizeS:  "db-s-1vcpu-2gb",
		interfaces.SizeM:  "db-s-2vcpu-4gb",
		interfaces.SizeL:  "db-s-4vcpu-8gb",
		interfaces.SizeXL: "db-s-8vcpu-16gb",
	},
	"infra.load_balancer": {
		interfaces.SizeXS: "lb-small",
		interfaces.SizeS:  "lb-small",
		interfaces.SizeM:  "lb-medium",
		interfaces.SizeL:  "lb-large",
		interfaces.SizeXL: "lb-large",
	},
	"infra.droplet": {
		interfaces.SizeXS: "s-1vcpu-512mb",
		interfaces.SizeS:  "s-1vcpu-2gb",
		interfaces.SizeM:  "s-2vcpu-4gb",
		interfaces.SizeL:  "s-4vcpu-8gb",
		interfaces.SizeXL: "s-8vcpu-16gb",
	},
	"infra.api_gateway": {
		interfaces.SizeXS: "apps-s-1vcpu-0.5gb",
		interfaces.SizeS:  "apps-s-1vcpu-1gb",
		interfaces.SizeM:  "apps-s-2vcpu-4gb",
		interfaces.SizeL:  "apps-s-4vcpu-8gb",
		interfaces.SizeXL: "apps-s-8vcpu-16gb",
	},
	// Resource types below have no variable sizing — all tiers return "n/a".
	"infra.vpc":         noopSizing(),
	"infra.firewall":    noopSizing(),
	"infra.dns":         noopSizing(),
	"infra.storage":     noopSizing(),
	"infra.registry":    noopSizing(),
	"infra.certificate": noopSizing(),
	"infra.iam_role":    noopSizing(),
}

// noopSizing returns a size map where every tier maps to "n/a".
func noopSizing() map[interfaces.Size]string {
	return map[interfaces.Size]string{
		interfaces.SizeXS: "n/a",
		interfaces.SizeS:  "n/a",
		interfaces.SizeM:  "n/a",
		interfaces.SizeL:  "n/a",
		interfaces.SizeXL: "n/a",
	}
}

// resolveSizing returns the DigitalOcean SKU for a given resource type and size.
// Returns an error for resource types not registered in doSizingMap.
func resolveSizing(resourceType string, size interfaces.Size, _ *interfaces.ResourceHints) (*interfaces.ProviderSizing, error) {
	sizeMap, ok := doSizingMap[resourceType]
	if !ok {
		return nil, fmt.Errorf("digitalocean: no sizing entry for resource type %q", resourceType)
	}
	sku, ok := sizeMap[size]
	if !ok {
		return nil, fmt.Errorf("digitalocean: no SKU for resource type %q size %q", resourceType, size)
	}
	return &interfaces.ProviderSizing{
		InstanceType: sku,
		Specs:        map[string]any{"sku": sku},
	}, nil
}
