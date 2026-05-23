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

// ─── Issue #159: in-rollout custom-domain probe ──────────────────────────────

// healthCheckProbeStub is the minimum AppPlatformClient surface the in-rollout
// HealthCheck probe tests need. The full deployMockClient lives in
// package drivers_test and cannot be reached from this internal test file.
type healthCheckProbeStub struct{ app *godo.App }

func (s *healthCheckProbeStub) Create(_ context.Context, _ *godo.AppCreateRequest) (*godo.App, *godo.Response, error) {
	return nil, nil, nil
}
func (s *healthCheckProbeStub) Get(_ context.Context, _ string) (*godo.App, *godo.Response, error) {
	return s.app, nil, nil
}
func (s *healthCheckProbeStub) List(_ context.Context, _ *godo.ListOptions) ([]*godo.App, *godo.Response, error) {
	return []*godo.App{s.app}, nil, nil
}
func (s *healthCheckProbeStub) Update(_ context.Context, _ string, _ *godo.AppUpdateRequest) (*godo.App, *godo.Response, error) {
	return s.app, nil, nil
}
func (s *healthCheckProbeStub) CreateDeployment(_ context.Context, _ string, _ ...*godo.DeploymentCreateRequest) (*godo.Deployment, *godo.Response, error) {
	return nil, nil, nil
}
func (s *healthCheckProbeStub) ListDeployments(_ context.Context, _ string, _ *godo.ListOptions) ([]*godo.Deployment, *godo.Response, error) {
	return nil, nil, nil
}
func (s *healthCheckProbeStub) Delete(_ context.Context, _ string) (*godo.Response, error) {
	return nil, nil
}
func (s *healthCheckProbeStub) GetLogs(_ context.Context, _, _, _ string, _ godo.AppLogType, _ bool, _ int) (*godo.AppLogs, *godo.Response, error) {
	return nil, nil, nil
}

func TestAppDeployDriver_HealthCheck_DuringRolloutProbesDomain(t *testing.T) {
	stub := &healthCheckProbeStub{
		app: &godo.App{
			ID: "blue-id",
			Spec: &godo.AppSpec{
				Domains:  []*godo.AppDomainSpec{{Domain: "blue.example.com"}},
				Services: []*godo.AppServiceSpec{{Name: "web", Image: &godo.ImageSourceSpec{Repository: "r", Tag: "v2"}}},
			},
			InProgressDeployment: &godo.Deployment{
				ID:    "dep-2",
				Phase: godo.DeploymentPhase_Deploying,
			},
			Domains: []*godo.AppDomain{{Spec: &godo.AppDomainSpec{Domain: "blue.example.com"}, Phase: godo.AppJobSpecKindPHASE_Active}},
		},
	}
	var probeCalls int
	probe := func(_ context.Context, _, _ string) error {
		probeCalls++
		return fmt.Errorf("simulated downtime")
	}
	d := NewAppDeployDriverWithDomainProbe(stub, "nyc1", "blue-id", "blue", probe)
	d.waitingForDeployment = true
	d.targetDeploymentID = "dep-2"

	err := d.HealthCheck(context.Background(), "")
	if err == nil {
		t.Fatal("expected in-progress error during DEPLOYING phase")
	}
	if probeCalls == 0 {
		t.Fatal("expected probe to fire during DEPLOYING phase")
	}
	if !strings.Contains(err.Error(), "domain probe: 0/1") {
		t.Errorf("missing reachable/total fragment in error: %v", err)
	}
	if !strings.Contains(err.Error(), "deployment in progress: DEPLOYING") {
		t.Errorf("missing original prefix in error: %v", err)
	}
}

func TestAppDeployDriver_HealthCheck_DuringBuildPhaseSkipsProbe(t *testing.T) {
	stub := &healthCheckProbeStub{
		app: &godo.App{
			ID: "blue-id",
			Spec: &godo.AppSpec{
				Domains:  []*godo.AppDomainSpec{{Domain: "blue.example.com"}},
				Services: []*godo.AppServiceSpec{{Name: "web"}},
			},
			InProgressDeployment: &godo.Deployment{ID: "dep-2", Phase: godo.DeploymentPhase_Building},
		},
	}
	var probeCalls int
	probe := func(_ context.Context, _, _ string) error { probeCalls++; return nil }
	d := NewAppDeployDriverWithDomainProbe(stub, "nyc1", "blue-id", "blue", probe)
	d.waitingForDeployment = true
	d.targetDeploymentID = "dep-2"

	_ = d.HealthCheck(context.Background(), "")
	if probeCalls != 0 {
		t.Fatalf("probe fired during BUILDING phase (%d calls); should be skipped", probeCalls)
	}
}

// silence unused-import warnings in case future patches add more tests below.
var (
	_ = fmt.Sprintf
	_ = strings.Contains
)
