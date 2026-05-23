package drivers

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/digitalocean/godo"
)

// White-box tests for unexported helpers and driver state. Black-box public-
// API tests live in deploy_test.go (package drivers_test).

// ─── Issue #159: sanitizeClonedSpecForCreate scope boundaries ────────────────

func TestSanitizeClonedSpecForCreate_PreservesIngress(t *testing.T) {
	spec := &godo.AppSpec{
		Name: "blue",
		Domains: []*godo.AppDomainSpec{
			{Domain: "example.com", Type: godo.AppDomainSpecType_Primary},
		},
		Ingress: &godo.AppIngressSpec{
			Rules: []*godo.AppIngressSpecRule{
				{Match: &godo.AppIngressSpecRuleMatch{Path: &godo.AppIngressSpecRuleStringMatch{Prefix: godo.PtrTo("/api")}}},
				{Match: &godo.AppIngressSpecRuleMatch{Path: &godo.AppIngressSpecRuleStringMatch{Prefix: godo.PtrTo("/")}}},
			},
		},
		Services: []*godo.AppServiceSpec{{Name: "web"}},
	}
	sanitizeClonedSpecForCreate(spec)
	if len(spec.Domains) != 0 {
		t.Fatalf("Domains not cleared, got %d entries", len(spec.Domains))
	}
	if spec.Ingress == nil || len(spec.Ingress.Rules) != 2 {
		t.Fatalf("Ingress.Rules altered, got %#v", spec.Ingress)
	}
	if len(spec.Services) != 1 {
		t.Fatalf("Services altered, got %d entries", len(spec.Services))
	}
}

func TestSanitizeClonedSpecForCreate_NilSpec(t *testing.T) {
	sanitizeClonedSpecForCreate(nil) // must not panic
}

// ─── Issue #159: WithDomainProbe constructor propagation ─────────────────────

func TestNewAppBlueGreenDriverWithDomainProbe_InjectsIntoInnerDrivers(t *testing.T) {
	probe := func(_ context.Context, _, _ string) error { return nil }
	d := NewAppBlueGreenDriverWithDomainProbe(nil, nil, "nyc1", "blue-id", "blue", probe)
	if d.blueDriver().domainProbe == nil {
		t.Fatal("blue driver missing injected probe")
	}
	d.greenID = "green-id"
	if d.greenDriver().domainProbe == nil {
		t.Fatal("green driver missing injected probe")
	}
}

func TestNewAppCanaryDriverWithDomainProbe_InjectsIntoInnerDrivers(t *testing.T) {
	probe := func(_ context.Context, _, _ string) error { return nil }
	d := NewAppCanaryDriverWithDomainProbe(nil, nil, "nyc1", "stable-id", "stable", probe)
	if d.stableDriver().domainProbe == nil {
		t.Fatal("stable driver missing injected probe")
	}
	d.canaryID = "canary-id"
	if d.canaryDriver().domainProbe == nil {
		t.Fatal("canary driver missing injected probe")
	}
}

// ─── Issue #159: appPlatformProbeCustomDomains helper ───────────────────────

func TestAppPlatformProbeCustomDomains_CountsReachable(t *testing.T) {
	app := &godo.App{
		Spec: &godo.AppSpec{
			Domains: []*godo.AppDomainSpec{
				{Domain: "a.example.com"},
				{Domain: "b.example.com"},
				{Domain: "c.example.com", Wildcard: true}, // excluded
			},
		},
	}
	var calls []string
	probe := func(_ context.Context, domain, _ string) error {
		calls = append(calls, domain)
		if domain == "b.example.com" {
			return fmt.Errorf("simulated 503")
		}
		return nil
	}
	reachable, total := appPlatformProbeCustomDomains(context.Background(), app, probe, "/healthz")
	if total != 2 {
		t.Fatalf("total = %d, want 2 (wildcard excluded)", total)
	}
	if reachable != 1 {
		t.Fatalf("reachable = %d, want 1", reachable)
	}
	if len(calls) != 2 {
		t.Fatalf("probe called %d times, want 2", len(calls))
	}
}

func TestAppPlatformProbeCustomDomains_EmptyApp(t *testing.T) {
	r, total := appPlatformProbeCustomDomains(context.Background(), &godo.App{}, nil, "")
	if total != 0 || r != 0 {
		t.Fatalf("expected (0,0) for app with no domains, got (%d, %d)", r, total)
	}
}

// silence unused-import warnings in case future patches add more tests below.
var (
	_ = fmt.Sprintf
	_ = strings.Contains
)
