package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/iac/diffcache"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/GoCodeAlone/workflow/platform"
	"golang.org/x/oauth2"
)

func TestValidatePlan_DropletPricingFreshAndSanitized(t *testing.T) {
	price := 24.0
	status, calls := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/v2/sizes" {
			t.Error("unexpected pricing request")
		}
		if status != 0 {
			w.WriteHeader(status)
			_, _ = fmt.Fprint(w, `{"message":"KNOWN_PRICE_SECRET"}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sizes": []map[string]any{{"slug": "s-2vcpu-4gb", "available": true, "regions": []string{"nyc3"}, "price_monthly": price}}})
	}))
	defer srv.Close()
	// Reuse the provider's ctx-injected HTTP seam; redirect every SDK request
	// to the local handler without changing production client construction.
	ctx := contextWithPricingHTTPClient(t, srv)
	p := NewDOProvider()
	if err := p.Initialize(ctx, map[string]any{"token": "fake", "region": "nyc3"}); err != nil {
		t.Fatal(err)
	}
	plan := &interfaces.IaCPlan{Actions: []interfaces.PlanAction{{Action: "create", Resource: interfaces.ResourceSpec{Name: "host", Type: "infra.droplet", Config: map[string]any{"size": "s-2vcpu-4gb", "region": "nyc3", "max_monthly_usd": 24}}}}}
	diags := p.ValidatePlan(plan)
	if len(diags) != 1 || diags[0].Severity != interfaces.PlanDiagnosticInfo {
		t.Fatalf("live quote diagnostic missing: %v", diags)
	}
	var quote drivers.DropletPriceQuote
	if err := json.Unmarshal([]byte(diags[0].Message), &quote); err != nil || quote.MonthlyUSD != 24 || quote.CheckedAt.IsZero() {
		t.Fatal("diagnostic is not a typed sanitized quote")
	}
	price = 25
	diags = p.ValidatePlan(plan)
	if len(diags) != 1 || diags[0].Severity != interfaces.PlanDiagnosticError || calls != 2 {
		t.Fatal("validation used a stale quote")
	}
	status = http.StatusForbidden
	diags = p.ValidatePlan(plan)
	if len(diags) != 1 || diags[0].Severity != interfaces.PlanDiagnosticError || strings.Contains(diags[0].Message, "KNOWN_PRICE_SECRET") {
		t.Fatal("API failure accepted or exposed")
	}
	plan.Actions[0].Action = "delete"
	if diags := p.ValidatePlan(plan); len(diags) != 0 || calls != 3 {
		t.Fatal("cleanup delete performed pricing lookup")
	}
}

func TestValidatePlan_DropletPricingOptInAndUninitializedClient(t *testing.T) {
	p := NewDOProvider()
	plan := &interfaces.IaCPlan{Actions: []interfaces.PlanAction{{Action: "create", Resource: interfaces.ResourceSpec{Type: "infra.droplet", Config: map[string]any{"size": "s-2vcpu-4gb", "region": "nyc3"}}}}}
	if diags := p.ValidatePlan(plan); len(diags) != 0 {
		t.Fatal("uncapped legacy required live pricing")
	}
	plan.Actions[0].Resource.Config["max_monthly_usd"] = 24
	if diags := p.ValidatePlan(plan); len(diags) != 1 || diags[0].Severity != interfaces.PlanDiagnosticError {
		t.Fatal("missing live client did not fail closed")
	}
}

func TestValidatePlan_DropletPricingLiveCatalogNotStaticRegions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/sizes" {
			t.Error("unexpected pricing request")
		}
		_, _ = fmt.Fprint(w, `{"sizes":[{"slug":"s-2vcpu-4gb","available":true,"regions":["future1"],"price_monthly":24}]}`)
	}))
	defer srv.Close()
	p := NewDOProvider()
	if err := p.Initialize(contextWithPricingHTTPClient(t, srv), map[string]any{"token": "fake", "region": "nyc3"}); err != nil {
		t.Fatal(err)
	}
	plan := &interfaces.IaCPlan{Actions: []interfaces.PlanAction{{Action: "create", Resource: interfaces.ResourceSpec{Type: "infra.droplet", Config: map[string]any{"size": "s-2vcpu-4gb", "region": "future1"}}}}}
	if diags := p.ValidatePlan(plan); len(diags) != 1 || diags[0].Severity != interfaces.PlanDiagnosticWarning {
		t.Fatal("fixture region unexpectedly present in static catalog")
	}
	plan.Actions[0].Resource.Config["max_monthly_usd"] = 24
	if diags := p.ValidatePlan(plan); len(diags) != 1 || diags[0].Severity != interfaces.PlanDiagnosticInfo {
		t.Fatal("static region catalog overrode authoritative live availability")
	}
}

func TestDOProvider_Plan_DropletPricingCacheRefresh(t *testing.T) {
	platform.SetDiffCacheForTest(t, diffcache.NewMemory())
	var price atomic.Uint64
	price.Store(math.Float64bits(24))
	var calls, httpStatus atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/v2/sizes" {
			t.Error("unexpected pricing request")
		}
		if code := httpStatus.Load(); code != 0 {
			w.WriteHeader(int(code))
			_, _ = fmt.Fprint(w, `{"message":"KNOWN_PRICE_SECRET"}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sizes": []map[string]any{{"slug": "s-2vcpu-4gb", "available": true, "regions": []string{"nyc3"}, "price_monthly": math.Float64frombits(price.Load())}}})
	}))
	defer srv.Close()
	p := NewDOProvider()
	if err := p.Initialize(contextWithPricingHTTPClient(t, srv), map[string]any{"token": "fake", "region": "nyc3"}); err != nil {
		t.Fatal(err)
	}
	driver := &pricingCountingDriver{ResourceDriver: p.drivers["infra.droplet"]}
	p.drivers["infra.droplet"] = driver
	desired := []interfaces.ResourceSpec{{Name: "host", Type: "infra.droplet", Config: map[string]any{"size": "s-2vcpu-4gb", "region": "nyc3", "max_monthly_usd": 24}}}
	current := []interfaces.ResourceState{{Name: "host", Type: "infra.droplet", ProviderID: "100", Outputs: map[string]any{"size": "s-2vcpu-4gb"}, AppliedConfig: desired[0].Config}}
	first, err := p.Plan(t.Context(), desired, current)
	if err != nil || len(first.Actions) != 0 || driver.calls.Load() != 1 || calls.Load() == 0 {
		t.Fatalf("initial live Diff fixture failed: %v", err)
	}
	before := calls.Load()
	price.Store(math.Float64bits(23))
	second, err := p.Plan(t.Context(), desired, current)
	if err != nil || len(second.Actions) != 0 || driver.calls.Load() != 1 {
		t.Fatalf("second plan did not hit the real diff cache: %v", err)
	}
	if calls.Load() != before+1 {
		t.Error("cache-hit plan did not fetch a fresh quote")
	}
	price.Store(math.Float64bits(25))
	if _, err := p.Plan(t.Context(), desired, current); err == nil || !errors.Is(err, interfaces.ErrValidation) {
		t.Error("cache-hit plan authorized an over-cap live price")
	}
	httpStatus.Store(http.StatusForbidden)
	if _, err := p.Plan(t.Context(), desired, current); err == nil || !errors.Is(err, interfaces.ErrForbidden) || strings.Contains(err.Error(), "KNOWN_PRICE_SECRET") {
		t.Error("cache-hit plan accepted or exposed a failed lookup")
	}
	if driver.calls.Load() != 1 {
		t.Fatal("fixture did not retain its cached Diff result")
	}
}

func TestDOProvider_Plan_DropletPricingNewResourceAndUncapped(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = fmt.Fprint(w, `{"sizes":[{"slug":"s-2vcpu-4gb","available":true,"regions":["nyc3"],"price_monthly":25}]}`)
	}))
	defer srv.Close()
	p := NewDOProvider()
	if err := p.Initialize(contextWithPricingHTTPClient(t, srv), map[string]any{"token": "fake", "region": "nyc3"}); err != nil {
		t.Fatal(err)
	}
	desired := []interfaces.ResourceSpec{{Name: "host", Type: "infra.droplet", Config: map[string]any{"size": "s-2vcpu-4gb", "region": "nyc3"}}}
	if plan, err := p.Plan(t.Context(), desired, nil); err != nil || len(plan.Actions) != 1 || calls.Load() != 0 {
		t.Fatal("uncapped new-resource plan performed pricing lookup or changed behavior")
	}
	desired[0].Config["max_monthly_usd"] = 24
	if _, err := p.Plan(t.Context(), desired, nil); err == nil || !errors.Is(err, interfaces.ErrValidation) || calls.Load() != 1 {
		t.Fatal("new-resource plan bypassed live pricing")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := p.Plan(ctx, desired, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled plan lost the pricing cancellation cause")
	}
	desired[0].Type = "infra.vpc"
	before := calls.Load()
	if _, err := p.Plan(t.Context(), desired, nil); err != nil || calls.Load() != before {
		t.Fatal("droplet price cap became a cross-resource public cost contract")
	}
}

type pricingCountingDriver struct {
	interfaces.ResourceDriver
	calls atomic.Int32
}

func (d *pricingCountingDriver) Diff(ctx context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	d.calls.Add(1)
	return d.ResourceDriver.Diff(ctx, desired, current)
}

type pricingRedirectTransport struct {
	target *http.Client
	server *httptest.Server
}

func (r pricingRedirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	u := *clone.URL
	u.Scheme, u.Host = "http", r.server.Listener.Addr().String()
	clone.URL = &u
	clone.Host = u.Host
	return r.target.Transport.RoundTrip(clone)
}

func contextWithPricingHTTPClient(t *testing.T, srv *httptest.Server) context.Context {
	t.Helper()
	return context.WithValue(t.Context(), oauth2.HTTPClient, &http.Client{Transport: pricingRedirectTransport{target: srv.Client(), server: srv}})
}
