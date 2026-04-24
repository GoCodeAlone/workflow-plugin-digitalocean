package drivers_test

// TestAllDrivers_DeclareProviderIDFormat verifies that every ResourceDriver in
// the DO plugin implements interfaces.ProviderIDValidator and returns the
// expected format. This is the exhaustive coverage gate for Task 10 of the
// v0.7.9 plan — any driver added in future releases must be listed here.

import (
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
)

func TestAllDrivers_DeclareProviderIDFormat(t *testing.T) {
	type entry struct {
		name   string
		driver interfaces.ProviderIDValidator
		want   interfaces.ProviderIDFormat
	}

	cases := []entry{
		// ── UUID drivers ──────────────────────────────────────────────────
		{"app_platform", drivers.NewAppPlatformDriverWithClient(&mockAppClient{}, "nyc3"), interfaces.IDFormatUUID},
		{"api_gateway", drivers.NewAPIGatewayDriverWithClient(&mockAPIGatewayClient{}, "nyc3"), interfaces.IDFormatUUID},
		{"cache", drivers.NewCacheDriverWithClient(&mockCacheClient{}, "nyc3"), interfaces.IDFormatUUID},
		{"certificate", drivers.NewCertificateDriverWithClient(&mockCertClient{}), interfaces.IDFormatUUID},
		{"database", drivers.NewDatabaseDriverWithClient(&mockDatabaseClient{}, "nyc3"), interfaces.IDFormatUUID},
		{"firewall", drivers.NewFirewallDriverWithClient(&mockFirewallClient{}), interfaces.IDFormatUUID},
		{"kubernetes", drivers.NewKubernetesDriverWithClient(&mockK8sClient{}, "nyc3"), interfaces.IDFormatUUID},
		{"load_balancer", drivers.NewLoadBalancerDriverWithClient(&mockLBClient{}, "nyc3"), interfaces.IDFormatUUID},
		{"vpc", drivers.NewVPCDriverWithClient(&mockVPCClient{}, "nyc3"), interfaces.IDFormatUUID},

		// ── DomainName drivers ────────────────────────────────────────────
		{"dns", drivers.NewDNSDriverWithClient(&mockDomainsClient{}), interfaces.IDFormatDomainName},

		// ── Freeform drivers ─────────────────────────────────────────────
		// Droplet IDs are integers (not UUIDs); DO Spaces/Registry/IAMRole use
		// name-based IDs. All declared Freeform to allow any non-empty string.
		{"droplet", drivers.NewDropletDriverWithClient(&mockDropletClient{}, "nyc3"), interfaces.IDFormatFreeform},
		{"spaces", drivers.NewSpacesDriverWithClient(&mockSpacesClient{}, "nyc3"), interfaces.IDFormatFreeform},
		{"registry", drivers.NewRegistryDriverWithClient(&mockRegistryClient{}), interfaces.IDFormatFreeform},
		// IAMRoleDriver has no godo client (DO has no IAM API); construct directly.
		{"iam_role", drivers.NewIAMRoleDriver(), interfaces.IDFormatFreeform},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.driver.ProviderIDFormat()
			if got != tc.want {
				t.Errorf("%s.ProviderIDFormat() = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
