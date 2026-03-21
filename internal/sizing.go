package internal

import (
	"fmt"

	"github.com/GoCodeAlone/workflow/interfaces"
)

// doSizingMap maps abstract size tiers to DigitalOcean SKU slugs per resource type.
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
}

// resolveSizing returns the DigitalOcean SKU for a given resource type and size.
// For resource types without an entry, the generic Droplet SKU is returned.
func resolveSizing(resourceType string, size interfaces.Size, _ *interfaces.ResourceHints) (*interfaces.ProviderSizing, error) {
	sizeMap, ok := doSizingMap[resourceType]
	if !ok {
		sizeMap = doSizingMap["infra.droplet"]
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
