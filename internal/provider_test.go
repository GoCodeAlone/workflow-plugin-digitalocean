package internal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/GoCodeAlone/workflow/module"
	"github.com/digitalocean/godo"
	"golang.org/x/oauth2"
)

// TestMain disables the platform/diffcache filesystem backend so per-test Diff
// dispatch is reproducible. ComputePlan caches Diff results under
// ~/.cache/wfctl/diff/ keyed on (PluginVersion, Type, ProviderID, SHAConfig,
// SHAOutputs); without disabling, prior test runs poison subsequent runs and
// fakes that record Diff invocations observe zero calls. WFCTL_DIFFCACHE is
// resolved by getDiffCache via sync.Once on first cache-eligible Diff call.
func TestMain(m *testing.M) {
	if err := os.Setenv("WFCTL_DIFFCACHE", "disabled"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

// compile-time interface check
var _ interfaces.IaCProvider = (*DOProvider)(nil)
var _ module.DeployDriverProvider = (*DOProvider)(nil)
var _ module.BlueGreenDriverProvider = (*DOProvider)(nil)
var _ module.CanaryDriverProvider = (*DOProvider)(nil)

func TestDOProvider_Name(t *testing.T) {
	p := NewDOProvider()
	if p.Name() != "digitalocean" {
		t.Errorf("Name = %q, want %q", p.Name(), "digitalocean")
	}
}

func TestDOProvider_Capabilities(t *testing.T) {
	p := NewDOProvider()
	caps := p.Capabilities()
	if len(caps) == 0 {
		t.Fatal("expected non-empty capabilities")
	}
	types := make(map[string]bool)
	for _, c := range caps {
		types[c.ResourceType] = true
	}
	required := []string{
		"infra.container_service",
		"infra.app_domain",
		"infra.k8s_cluster",
		"infra.database",
		"infra.cache",
		"infra.load_balancer",
		"infra.vpc",
		"infra.firewall",
		"infra.dns",
		"infra.storage",
		"infra.registry",
		"infra.certificate",
		"infra.droplet",
		"infra.volume",
		"infra.iam_role",
		"infra.api_gateway",
	}
	for _, rt := range required {
		if !types[rt] {
			t.Errorf("missing capability: %s", rt)
		}
	}
}

func TestDOProvider_Initialize_MissingToken(t *testing.T) {
	p := NewDOProvider()
	err := p.Initialize(t.Context(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing token")
	}
}

// TestDOProvider_Initialize_NilCtxRejected pins the nil-ctx guard added in
// workflow-plugin-digitalocean#62. Initialize must reject nil ctx rather than
// silently passing it down to oauth2.NewClient.
func TestDOProvider_Initialize_NilCtxRejected(t *testing.T) {
	p := NewDOProvider()
	//nolint:staticcheck // intentional nil ctx for the guard test
	err := p.Initialize(nil, map[string]any{"token": "fake-token"})
	if err == nil {
		t.Fatal("expected error for nil ctx; got nil")
	}
	if !strings.Contains(err.Error(), "non-nil ctx") {
		t.Errorf("error %q should mention non-nil ctx", err.Error())
	}
}

// httpClientCapturingTransport records the http.Client that issued a request.
// Used to verify Initialize threaded ctx-injected oauth2.HTTPClient into the
// godo client's transport chain.
type httpClientCapturingTransport struct {
	called bool
	resp   *http.Response
}

func (t *httpClientCapturingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.called = true
	if t.resp != nil {
		return t.resp, nil
	}
	// Return a minimal 200 with a valid JSON object body so godo's response
	// decoder doesn't error on EOF / invalid JSON when the caller passes a
	// pointer-to-struct destination. We don't care what the test caller does
	// with the response — only that the transport observed the request, which
	// proves the ctx-injected http.Client made it through Initialize.
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("{}")),
		Header:     header,
		Request:    req,
	}, nil
}

// TestDOProvider_Initialize_ThreadsCtxToGodoClient pins workflow-plugin-digitalocean#62.
// Prior to the fix, Initialize discarded the caller's ctx and constructed the
// oauth2 client with context.Background(), so any oauth2.HTTPClient injected via
// ctx (tests, custom transports, proxy configs) was silently dropped. This test
// injects a capturing transport via oauth2.HTTPClient and verifies it observes a
// real outbound request after Initialize wires the godo client.
func TestDOProvider_Initialize_ThreadsCtxToGodoClient(t *testing.T) {
	transport := &httpClientCapturingTransport{}
	customClient := &http.Client{Transport: transport}
	ctx := context.WithValue(t.Context(), oauth2.HTTPClient, customClient)

	p := NewDOProvider()
	if err := p.Initialize(ctx, map[string]any{"token": "fake-token"}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Issue any godo call that hits the transport. Account.Get is the cheapest.
	_, _, _ = p.client.Account.Get(t.Context())

	if !transport.called {
		t.Fatal("custom transport injected via oauth2.HTTPClient was never called; ctx was not threaded into godo client")
	}
}

func TestDOProvider_ResolveSizing(t *testing.T) {
	p := NewDOProvider()
	result, err := p.ResolveSizing("infra.database", interfaces.SizeM, nil)
	if err != nil {
		t.Fatalf("ResolveSizing: %v", err)
	}
	if result.InstanceType != "db-s-2vcpu-4gb" {
		t.Errorf("InstanceType = %q, want %q", result.InstanceType, "db-s-2vcpu-4gb")
	}
}

func TestDOProvider_ResolveSizing_NoopType(t *testing.T) {
	p := NewDOProvider()
	result, err := p.ResolveSizing("infra.vpc", interfaces.SizeM, nil)
	if err != nil {
		t.Fatalf("ResolveSizing vpc: %v", err)
	}
	if result.InstanceType != "n/a" {
		t.Errorf("InstanceType = %q, want n/a", result.InstanceType)
	}
}

func TestDOProvider_ResolveSizing_UnknownReturnsError(t *testing.T) {
	p := NewDOProvider()
	_, err := p.ResolveSizing("infra.unknown_thing", interfaces.SizeM, nil)
	if err == nil {
		t.Fatal("expected error for unknown resource type")
	}
}

func TestDOProvider_ResourceDriver_Unknown(t *testing.T) {
	p := NewDOProvider()
	_, err := p.ResourceDriver("infra.unknown_thing")
	if err == nil {
		t.Fatal("expected error for unknown resource type")
	}
}

func TestDOProvider_SupportedCanonicalKeys(t *testing.T) {
	p := NewDOProvider()
	keys := p.SupportedCanonicalKeys()
	if len(keys) == 0 {
		t.Fatal("SupportedCanonicalKeys returned empty slice")
	}
	keySet := make(map[string]bool, len(keys))
	for _, k := range keys {
		keySet[k] = true
	}

	// Keys the DO provider actively maps in this release (v0.7.0).
	supported := []string{
		"name", "region", "image", "http_port", "instance_count", "size",
		"env_vars", "env_vars_secret", "autoscaling", "routes", "health_check",
		"liveness_check", "cors", "protocol", "internal_ports", "build_command", "run_command",
		"dockerfile_path", "source_dir", "termination", "domains", "alerts",
		"log_destinations", "ingress", "egress", "maintenance", "vpc_ref",
		"jobs", "workers", "static_sites", "sidecars", "provider_specific",
	}
	for _, k := range supported {
		if !keySet[k] {
			t.Errorf("SupportedCanonicalKeys missing expected key %q", k)
		}
	}
}

func TestConfigHash_Deterministic(t *testing.T) {
	cfg := map[string]any{"b": 2, "a": 1, "c": "three"}
	h1 := configHash(cfg)
	h2 := configHash(cfg)
	if h1 != h2 {
		t.Errorf("configHash not deterministic: %q != %q", h1, h2)
	}
}

func TestConfigHash_DifferentConfigs(t *testing.T) {
	h1 := configHash(map[string]any{"engine": "pg", "size": "db-s-1vcpu-2gb"})
	h2 := configHash(map[string]any{"engine": "pg", "size": "db-s-2vcpu-4gb"})
	if h1 == h2 {
		t.Error("expected different hashes for different configs")
	}
}

func TestConfigHash_Empty(t *testing.T) {
	h := configHash(nil)
	if h != "" {
		t.Errorf("expected empty hash for nil config, got %q", h)
	}
}

// planDiffFakeDriver is a test double whose Diff records each invocation.
// platform.ComputePlan dispatches Diff in parallel via errgroup, so all
// observable state mutations are guarded by mu.
type planDiffFakeDriver struct {
	diffResult *interfaces.DiffResult
	mu         sync.Mutex
	diffCalls  int
	// receivedSpec / receivedCurrent capture the LAST Diff invocation; in
	// multi-resource tests they reflect non-deterministic ordering since
	// dispatch is parallel — assertions that depend on a specific resource
	// should pin the spec by Name in the assertion (or use a multi-shot
	// recorder per name).
	receivedSpec    interfaces.ResourceSpec
	receivedCurrent *interfaces.ResourceOutput
}

func (f *planDiffFakeDriver) Create(_ context.Context, _ interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	return nil, nil
}
func (f *planDiffFakeDriver) Read(_ context.Context, _ interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	return nil, nil
}
func (f *planDiffFakeDriver) Update(_ context.Context, _ interfaces.ResourceRef, _ interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	return nil, nil
}
func (f *planDiffFakeDriver) Delete(_ context.Context, _ interfaces.ResourceRef) error { return nil }
func (f *planDiffFakeDriver) Diff(_ context.Context, spec interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.diffCalls++
	f.receivedSpec = spec
	f.receivedCurrent = current
	return f.diffResult, nil
}
func (f *planDiffFakeDriver) HealthCheck(_ context.Context, _ interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	return nil, nil
}
func (f *planDiffFakeDriver) Scale(_ context.Context, _ interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return nil, nil
}
func (f *planDiffFakeDriver) SensitiveKeys() []string { return nil }

func TestDOProvider_Plan_UsesDriverDiffForExistingResource(t *testing.T) {
	spec := interfaces.ResourceSpec{
		Name: "example-dns",
		Type: "infra.dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "TXT", "name": "@", "data": "expected", "ttl": 300},
			},
		},
	}
	fake := &planDiffFakeDriver{
		diffResult: &interfaces.DiffResult{
			NeedsUpdate: true,
			Changes: []interfaces.FieldChange{
				{Path: "records[TXT/@/expected]", Old: nil, New: "expected"},
			},
		},
	}
	p := &DOProvider{drivers: map[string]interfaces.ResourceDriver{"infra.dns": fake}}

	plan, err := p.Plan(t.Context(), []interfaces.ResourceSpec{spec}, []interfaces.ResourceState{
		{
			Name:          "example-dns",
			Type:          "infra.dns",
			ProviderID:    "example.com",
			AppliedConfig: spec.Config,
			Outputs: map[string]any{
				"records": []map[string]any{
					{"type": "TXT", "name": "@", "data": "other", "ttl": 300},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if fake.diffCalls != 1 {
		t.Fatalf("Diff calls = %d, want 1", fake.diffCalls)
	}
	if fake.receivedCurrent == nil {
		t.Fatal("Diff current = nil, want reconstructed ResourceOutput")
	}
	if fake.receivedCurrent.ProviderID != "example.com" {
		t.Errorf("Diff current ProviderID = %q, want example.com", fake.receivedCurrent.ProviderID)
	}
	if len(plan.Actions) != 1 {
		t.Fatalf("plan actions = %d, want 1", len(plan.Actions))
	}
	action := plan.Actions[0]
	if action.Action != "update" {
		t.Fatalf("plan action = %q, want update", action.Action)
	}
	if len(action.Changes) != 1 || action.Changes[0].Path != "records[TXT/@/expected]" {
		t.Fatalf("plan action changes = %+v, want driver changes", action.Changes)
	}
}

func TestDOProvider_Plan_KeepsDistinctCurrentStatePerAction(t *testing.T) {
	desired := []interfaces.ResourceSpec{
		{
			Name:   "one-dns",
			Type:   "infra.dns",
			Config: map[string]any{"domain": "one.example.com"},
		},
		{
			Name:   "two-dns",
			Type:   "infra.dns",
			Config: map[string]any{"domain": "two.example.com"},
		},
	}
	current := []interfaces.ResourceState{
		{
			Name:          "one-dns",
			Type:          "infra.dns",
			ProviderID:    "one.example.com",
			AppliedConfig: map[string]any{"domain": "old-one.example.com"},
		},
		{
			Name:          "two-dns",
			Type:          "infra.dns",
			ProviderID:    "two.example.com",
			AppliedConfig: map[string]any{"domain": "old-two.example.com"},
		},
	}
	fake := &planDiffFakeDriver{
		diffResult: &interfaces.DiffResult{NeedsUpdate: true},
	}
	p := &DOProvider{drivers: map[string]interfaces.ResourceDriver{"infra.dns": fake}}

	plan, err := p.Plan(t.Context(), desired, current)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Actions) != 2 {
		t.Fatalf("plan actions = %d, want 2", len(plan.Actions))
	}
	if plan.Actions[0].Current == nil || plan.Actions[0].Current.ProviderID != "one.example.com" {
		t.Fatalf("first action current = %+v, want one.example.com", plan.Actions[0].Current)
	}
	if plan.Actions[1].Current == nil || plan.Actions[1].Current.ProviderID != "two.example.com" {
		t.Fatalf("second action current = %+v, want two.example.com", plan.Actions[1].Current)
	}
}

// TestDOProvider_Plan_RejectsUnregisteredDriverType pins the new contract
// after refactoring DOProvider.Plan to platform.ComputePlan
// (workflow-plugin-digitalocean#63). Previously the hand-rolled Plan body
// silently fell back to a configHash compare when ResourceDriver returned an
// error; the canonical helper instead surfaces the missing-driver error so
// operators see a clear failure rather than a stale-shape plan.
//
// Replaces TestDOProvider_Plan_KeepsDistinctCurrentStatePerConfigHashAction —
// the legacy-fallback semantic that test pinned no longer applies.
func TestDOProvider_Plan_RejectsUnregisteredDriverType(t *testing.T) {
	desired := []interfaces.ResourceSpec{
		{
			Name:   "one-dns",
			Type:   "infra.dns",
			Config: map[string]any{"domain": "one.example.com"},
		},
	}
	current := []interfaces.ResourceState{
		{
			Name:          "one-dns",
			Type:          "infra.dns",
			ProviderID:    "one.example.com",
			AppliedConfig: map[string]any{"domain": "old-one.example.com"},
		},
	}
	// DOProvider with no drivers registered — ResourceDriver returns an error
	// for every type. ComputePlan must propagate that error rather than
	// silently emit a configHash-based update.
	p := &DOProvider{}

	_, err := p.Plan(t.Context(), desired, current)
	if err == nil {
		t.Fatal("expected error from Plan when driver is not registered; got nil")
	}
	if !strings.Contains(err.Error(), "infra.dns") {
		t.Errorf("error %q should mention the missing resource type", err.Error())
	}
}

type providerDNSMock struct {
	domain         *godo.Domain
	getErrs        []error
	createErr      error
	records        []godo.DomainRecord
	createCalls    int
	createRecordNs []godo.DomainRecordEditRequest
}

func (m *providerDNSMock) Create(_ context.Context, req *godo.DomainCreateRequest) (*godo.Domain, *godo.Response, error) {
	m.createCalls++
	if m.createErr != nil {
		return nil, nil, m.createErr
	}
	return &godo.Domain{Name: req.Name}, nil, nil
}

func (m *providerDNSMock) Get(_ context.Context, name string) (*godo.Domain, *godo.Response, error) {
	if len(m.getErrs) > 0 {
		err := m.getErrs[0]
		m.getErrs = m.getErrs[1:]
		if err != nil {
			return nil, nil, err
		}
	}
	if m.domain != nil {
		return m.domain, nil, nil
	}
	return &godo.Domain{Name: name}, nil, nil
}

func (m *providerDNSMock) Delete(_ context.Context, _ string) (*godo.Response, error) {
	return nil, nil
}

func (m *providerDNSMock) CreateRecord(_ context.Context, _ string, req *godo.DomainRecordEditRequest) (*godo.DomainRecord, *godo.Response, error) {
	m.createRecordNs = append(m.createRecordNs, *req)
	record := godo.DomainRecord{ID: len(m.records) + 1, Type: req.Type, Name: req.Name, Data: req.Data, TTL: req.TTL, Priority: req.Priority, Port: req.Port, Weight: req.Weight, Flags: req.Flags, Tag: req.Tag}
	m.records = append(m.records, record)
	return &record, nil, nil
}

func (m *providerDNSMock) EditRecord(_ context.Context, _ string, id int, req *godo.DomainRecordEditRequest) (*godo.DomainRecord, *godo.Response, error) {
	record := godo.DomainRecord{ID: id, Type: req.Type, Name: req.Name, Data: req.Data, TTL: req.TTL, Priority: req.Priority, Port: req.Port, Weight: req.Weight, Flags: req.Flags, Tag: req.Tag}
	for i := range m.records {
		if m.records[i].ID == id {
			m.records[i] = record
			return &record, nil, nil
		}
	}
	m.records = append(m.records, record)
	return &record, nil, nil
}

func (m *providerDNSMock) DeleteRecord(_ context.Context, _ string, _ int) (*godo.Response, error) {
	return nil, nil
}

func (m *providerDNSMock) Records(_ context.Context, _ string, _ *godo.ListOptions) ([]godo.DomainRecord, *godo.Response, error) {
	return append([]godo.DomainRecord(nil), m.records...), &godo.Response{Links: &godo.Links{Pages: &godo.Pages{}}}, nil
}

func TestDOProvider_Plan_DNSDriverDiffUsesImportedRecordState(t *testing.T) {
	p := &DOProvider{drivers: map[string]interfaces.ResourceDriver{
		"infra.dns": drivers.NewDNSDriverWithClient(&providerDNSMock{}),
	}}
	spec := interfaces.ResourceSpec{
		Name: "site-dns",
		Type: "infra.dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "TXT", "name": "@", "data": "expected", "ttl": 300},
			},
		},
	}

	plan, err := p.Plan(t.Context(), []interfaces.ResourceSpec{spec}, []interfaces.ResourceState{{
		Name:          "site-dns",
		Type:          "infra.dns",
		ProviderID:    "example.com",
		AppliedConfig: spec.Config,
		Outputs: map[string]any{
			"records": []map[string]any{{"type": "A", "name": "@", "data": "1.2.3.4", "ttl": 300}},
		},
	}})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Actions) != 1 || plan.Actions[0].Action != "update" {
		t.Fatalf("plan actions = %#v, want one DNS update", plan.Actions)
	}
}

func TestDOProvider_Plan_DNSDriverDiffNoopsWhenImportedRecordStateMatches(t *testing.T) {
	p := &DOProvider{drivers: map[string]interfaces.ResourceDriver{
		"infra.dns": drivers.NewDNSDriverWithClient(&providerDNSMock{}),
	}}
	spec := interfaces.ResourceSpec{
		Name: "site-dns",
		Type: "infra.dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "TXT", "name": "@", "data": "expected", "ttl": 300},
			},
		},
	}

	plan, err := p.Plan(t.Context(), []interfaces.ResourceSpec{spec}, []interfaces.ResourceState{{
		Name:          "site-dns",
		Type:          "infra.dns",
		ProviderID:    "example.com",
		AppliedConfig: map[string]any{"domain": "something-stale"},
		Outputs: map[string]any{
			"records": []map[string]any{{"type": "TXT", "name": "@", "data": "expected", "ttl": 300}},
		},
	}})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Actions) != 0 {
		t.Fatalf("plan actions = %#v, want none from matching imported DNS records", plan.Actions)
	}
}

func TestDOProvider_Import_DNSIncludesExistingRecords(t *testing.T) {
	mock := &providerDNSMock{
		domain:  &godo.Domain{Name: "example.com", ZoneFile: "$ORIGIN example.com.\n"},
		records: []godo.DomainRecord{{ID: 10, Type: "TXT", Name: "@", Data: "imported", TTL: 300}},
	}
	p := &DOProvider{drivers: map[string]interfaces.ResourceDriver{
		"infra.dns": drivers.NewDNSDriverWithClient(mock),
	}}

	state, err := p.Import(t.Context(), "example.com", "infra.dns")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	records, ok := state.Outputs["records"].([]map[string]any)
	if !ok {
		t.Fatalf("records output = %T, want []map[string]any", state.Outputs["records"])
	}
	if len(records) != 1 || records[0]["data"] != "imported" {
		t.Fatalf("records = %#v, want imported TXT record", records)
	}
	if records[0]["id"] != 10 {
		t.Fatalf("record id = %#v, want 10", records[0]["id"])
	}
	if state.Outputs["zone_file"] != "$ORIGIN example.com.\n" {
		t.Fatalf("zone_file = %#v, want exported zone", state.Outputs["zone_file"])
	}
	if state.Outputs["record_count"] != 1 {
		t.Fatalf("record_count = %#v, want 1", state.Outputs["record_count"])
	}
}

func TestDOProvider_Plan_UsesReplaceForDriverNeedsReplace(t *testing.T) {
	spec := interfaces.ResourceSpec{
		Name:   "example-vpc",
		Type:   "infra.vpc",
		Config: map[string]any{"ip_range": "10.20.0.0/16"},
	}
	fake := &planDiffFakeDriver{
		diffResult: &interfaces.DiffResult{
			NeedsReplace: true,
			Changes: []interfaces.FieldChange{
				{Path: "ip_range", Old: "10.10.0.0/16", New: "10.20.0.0/16", ForceNew: true},
			},
		},
	}
	p := &DOProvider{drivers: map[string]interfaces.ResourceDriver{"infra.vpc": fake}}

	plan, err := p.Plan(t.Context(), []interfaces.ResourceSpec{spec}, []interfaces.ResourceState{
		{
			Name:          "example-vpc",
			Type:          "infra.vpc",
			ProviderID:    "vpc-123",
			AppliedConfig: map[string]any{"ip_range": "10.10.0.0/16"},
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Actions) != 1 {
		t.Fatalf("plan actions = %d, want 1", len(plan.Actions))
	}
	action := plan.Actions[0]
	if action.Action != "replace" {
		t.Fatalf("plan action = %q, want replace", action.Action)
	}
	if action.Current == nil || action.Current.ProviderID != "vpc-123" {
		t.Fatalf("plan current = %+v, want provider ID vpc-123", action.Current)
	}
	if len(action.Changes) != 1 || !action.Changes[0].ForceNew {
		t.Fatalf("plan action changes = %+v, want ForceNew change", action.Changes)
	}
}

func TestDOProvider_Plan_TreatsNoopDriverDiffAsAuthoritative(t *testing.T) {
	spec := interfaces.ResourceSpec{
		Name: "example-dns",
		Type: "infra.dns",
		Config: map[string]any{
			"domain": "example.com",
			"records": []any{
				map[string]any{"type": "TXT", "name": "@", "data": "expected", "ttl": 300},
			},
		},
	}
	fake := &planDiffFakeDriver{
		diffResult: &interfaces.DiffResult{NeedsUpdate: false, NeedsReplace: false},
	}
	p := &DOProvider{drivers: map[string]interfaces.ResourceDriver{"infra.dns": fake}}

	plan, err := p.Plan(t.Context(), []interfaces.ResourceSpec{spec}, []interfaces.ResourceState{
		{
			Name:       "example-dns",
			Type:       "infra.dns",
			ProviderID: "example.com",
			AppliedConfig: map[string]any{
				"domain": "example.com",
				"records": []any{
					map[string]any{"type": "TXT", "name": "@", "data": "stale", "ttl": 300},
				},
			},
			Outputs: map[string]any{
				"records": []map[string]any{
					{"type": "TXT", "name": "@", "data": "expected", "ttl": 300},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if fake.diffCalls != 1 {
		t.Fatalf("Diff calls = %d, want 1", fake.diffCalls)
	}
	if len(plan.Actions) != 0 {
		t.Fatalf("plan actions = %+v, want none from authoritative driver diff", plan.Actions)
	}
}

// newDOProviderForTest builds a *DOProvider whose godo client points at the
// given httptest server. Mirrors newProviderForEnumeratorTest in
// provider_enumerator_test.go but takes the server (not just a URL) so the
// EnumeratorAll spaces_key test can drive its own paginated handler while
// still using the server's hermetic http client.
//
// Why srv.Client() and not http.DefaultClient: matches the sister helpers
// at provider_enumerator_test.go:149 and internal/drivers/spaces_key_test.go
// (post-f203b15). srv.Client() trusts the server's TLS cert if/when the
// test moves to TLS, and never mutates the global http.DefaultClient state.
func newDOProviderForTest(t *testing.T, srv *httptest.Server) *DOProvider {
	t.Helper()
	client := godo.NewClient(srv.Client())
	base, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatalf("parse httptest URL %q: %v", srv.URL, err)
	}
	client.BaseURL = base
	return &DOProvider{client: client, region: "nyc3"}
}

// TestProvider_EnumerateAll_SpacesKeys is the contract test for the
// EnumeratorAll interface implementation on the DO provider, scoped to the
// "infra.spaces_key" resource type. Spaces keys live outside the DO tag
// system so the existing EnumerateByTag path cannot reach them; the audit /
// prune CLIs (Tasks 17, 19, 21) need a tag-free enumeration path that
// returns every key in the account, paginated transparently.
//
// The test fakes a paginated DO API response (page 1 returns 2 keys with a
// next-page link; page 2 returns 1 key) and asserts:
//
//   - DOProvider implements interfaces.EnumeratorAll (added in workflow
//     v0.26.0; see iac_provider.go).
//   - EnumerateAll(ctx, "infra.spaces_key") returns 3 *ResourceOutput
//     entries — pagination must be handled inside the provider, not punted
//     to the caller.
//   - Each output has Type="infra.spaces_key", non-empty ProviderID
//     (= access_key, matching the SpacesKeyDriver.Create contract), and
//     Outputs.access_key + Outputs.created_at populated so audit-keys / prune
//     can filter by age without re-reading every key.
//
// Pins the contract post Tasks 14+15: DOProvider implements EnumeratorAll
// and enumerateAllSpacesKeys returns the full paginated set with the same
// ProviderID convention (access_key) and Outputs shape that
// SpacesKeyDriver.Read produces.
func TestProvider_EnumerateAll_SpacesKeys(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v2/spaces/keys" {
			w.Header().Set("Content-Type", "application/json")
			page := r.URL.Query().Get("page")
			if page == "" || page == "1" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"keys": []any{
						map[string]any{"name": "key-a", "access_key": "AK_A", "created_at": "2026-05-01T00:00:00Z"},
						map[string]any{"name": "key-b", "access_key": "AK_B", "created_at": "2026-05-02T00:00:00Z"},
					},
					"links": map[string]any{
						"pages": map[string]any{"next": srv.URL + "/v2/spaces/keys?page=2"},
					},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"keys": []any{
					map[string]any{"name": "key-c", "access_key": "AK_C", "created_at": "2026-05-03T00:00:00Z"},
				},
			})
			return
		}
		// Unexpected path — return 500 so godo surfaces it as an HTTP error
		// rather than the test silently observing an empty 200 body. Mirrors
		// the same hermetic-handler pattern in
		// internal/drivers/spaces_key_test.go.
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected path", http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := newDOProviderForTest(t, srv)
	enumerator, ok := interfaces.IaCProvider(p).(interfaces.EnumeratorAll)
	if !ok {
		t.Fatalf("Provider must implement EnumeratorAll")
	}

	outs, err := enumerator.EnumerateAll(context.Background(), "infra.spaces_key")
	if err != nil {
		t.Fatalf("EnumerateAll: %v", err)
	}
	if len(outs) != 3 {
		t.Fatalf("expected 3 keys (paginated), got %d: %+v", len(outs), outs)
	}
	// Each *ResourceOutput must have Name + Type + ProviderID populated, plus
	// the full Outputs map (name, access_key, created_at, ...) so downstream
	// filter use cases (wfctl infra audit-keys, prune) don't have to re-read.
	//
	// access_key + created_at are asserted as non-empty STRINGS rather than
	// just non-nil interface{}: ResourceOutput.Outputs is map[string]any but
	// the gRPC-side proto roundtrip (structpb) only accepts string/number/
	// bool/list/map values. A regression that stored time.Time would pass a
	// bare nil-check but fail the proto marshal — type-assert to string here
	// to lock the structpb-safe shape.
	for _, o := range outs {
		if o.Type != "infra.spaces_key" {
			t.Errorf("expected Type=infra.spaces_key, got %q", o.Type)
		}
		if o.ProviderID == "" {
			t.Errorf("ProviderID must be populated (= access_key); got empty for %q", o.Name)
		}
		if got, _ := o.Outputs["access_key"].(string); got == "" {
			t.Errorf("Outputs.access_key must be populated as non-empty string for %q; got %T %v", o.Name, o.Outputs["access_key"], o.Outputs["access_key"])
		}
		if got, _ := o.Outputs["created_at"].(string); got == "" {
			t.Errorf("Outputs.created_at must be populated as non-empty string for %q; got %T %v", o.Name, o.Outputs["created_at"], o.Outputs["created_at"])
		}
	}
}
