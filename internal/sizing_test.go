package internal

import (
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
)

func TestResolveSizing(t *testing.T) {
	cases := []struct {
		resourceType string
		size         interfaces.Size
		wantSKU      string
	}{
		{"infra.container_service", interfaces.SizeXS, "apps-s-1vcpu-0.5gb"},
		{"infra.container_service", interfaces.SizeS, "apps-s-1vcpu-1gb"},
		{"infra.container_service", interfaces.SizeM, "apps-s-2vcpu-4gb"},
		{"infra.container_service", interfaces.SizeL, "apps-s-4vcpu-8gb"},
		{"infra.container_service", interfaces.SizeXL, "apps-s-8vcpu-16gb"},
		{"infra.k8s_cluster", interfaces.SizeXS, "s-1vcpu-512mb"},
		{"infra.k8s_cluster", interfaces.SizeM, "s-2vcpu-4gb"},
		{"infra.database", interfaces.SizeS, "db-s-1vcpu-2gb"},
		{"infra.database", interfaces.SizeL, "db-s-4vcpu-8gb"},
		{"infra.cache", interfaces.SizeS, "db-s-1vcpu-2gb"},
		{"infra.cache", interfaces.SizeM, "db-s-2vcpu-4gb"},
		{"infra.load_balancer", interfaces.SizeXS, "lb-small"},
		{"infra.load_balancer", interfaces.SizeM, "lb-medium"},
		{"infra.api_gateway", interfaces.SizeS, "apps-s-1vcpu-1gb"},
		// Types without variable sizing return "n/a".
		{"infra.vpc", interfaces.SizeM, "n/a"},
		{"infra.firewall", interfaces.SizeL, "n/a"},
		{"infra.dns", interfaces.SizeS, "n/a"},
		{"infra.storage", interfaces.SizeXL, "n/a"},
		{"infra.registry", interfaces.SizeS, "n/a"},
		{"infra.certificate", interfaces.SizeM, "n/a"},
		{"infra.iam_role", interfaces.SizeS, "n/a"},
	}

	for _, tc := range cases {
		t.Run(tc.resourceType+"/"+string(tc.size), func(t *testing.T) {
			result, err := resolveSizing(tc.resourceType, tc.size, nil)
			if err != nil {
				t.Fatalf("resolveSizing: %v", err)
			}
			if result.InstanceType != tc.wantSKU {
				t.Errorf("InstanceType = %q, want %q", result.InstanceType, tc.wantSKU)
			}
		})
	}
}

func TestResolveSizing_UnknownTypeReturnsError(t *testing.T) {
	_, err := resolveSizing("infra.unknown_thing", interfaces.SizeM, nil)
	if err == nil {
		t.Fatal("expected error for unknown resource type, got nil")
	}
}
