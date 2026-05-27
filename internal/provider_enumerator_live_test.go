//go:build live_dns

// Live integration test for EnumerateAll("infra.dns"). Gated by the
// `live_dns` build tag AND the INFRA_DNS_ENUMERATE_LIVE=1 env var so it does
// not run in CI by default. Required env:
//
//   - INFRA_DNS_ENUMERATE_LIVE=1
//   - DIGITALOCEAN_TOKEN=<api token with read access to /v2/domains>
//
// Run locally:
//
//	INFRA_DNS_ENUMERATE_LIVE=1 DIGITALOCEAN_TOKEN=$TOKEN \
//	  GOWORK=off go test -tags live_dns \
//	    -run TestDOProvider_EnumerateAll_DNS_live ./internal/...

package internal

import (
	"context"
	"os"
	"testing"
)

func TestDOProvider_EnumerateAll_DNS_live(t *testing.T) {
	if os.Getenv("INFRA_DNS_ENUMERATE_LIVE") != "1" {
		t.Skip("set INFRA_DNS_ENUMERATE_LIVE=1 + DIGITALOCEAN_TOKEN to run")
	}
	token := os.Getenv("DIGITALOCEAN_TOKEN")
	if token == "" {
		t.Skip("DIGITALOCEAN_TOKEN not set; cannot run live test")
	}

	ctx := context.Background()
	p := NewDOProvider()
	if err := p.Initialize(ctx, map[string]any{"token": token}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	outs, err := p.EnumerateAll(ctx, "infra.dns")
	if err != nil {
		t.Fatalf("live EnumerateAll(infra.dns): %v", err)
	}
	if len(outs) == 0 {
		t.Skip("account has zero zones; cannot validate live contract")
	}
	for _, o := range outs {
		if o.ProviderID == "" {
			t.Errorf("empty ProviderID for %+v", o.Outputs)
		}
		if o.Type != "infra.dns" {
			t.Errorf("wrong Type %q", o.Type)
		}
		if got, _ := o.Outputs["zone"].(string); got == "" {
			t.Errorf("Outputs.zone empty for %+v", o)
		}
	}
	t.Logf("enumerated %d zones from live DO account", len(outs))
}
