package drivers_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type mockFirewallClient struct {
	fw      *godo.Firewall
	err     error
	listErr error
	// lastReq captures the most recent FirewallRequest sent to Create or
	// Update, allowing tests to assert that DropletIDs / Tags pass through
	// to godo.
	lastReq *godo.FirewallRequest
}

func (m *mockFirewallClient) Create(_ context.Context, req *godo.FirewallRequest) (*godo.Firewall, *godo.Response, error) {
	m.lastReq = req
	return m.fw, nil, m.err
}
func (m *mockFirewallClient) Get(_ context.Context, _ string) (*godo.Firewall, *godo.Response, error) {
	return m.fw, nil, m.err
}
func (m *mockFirewallClient) List(_ context.Context, _ *godo.ListOptions) ([]godo.Firewall, *godo.Response, error) {
	if m.listErr != nil {
		return nil, nil, m.listErr
	}
	if m.fw == nil {
		return nil, nil, nil
	}
	return []godo.Firewall{*m.fw}, nil, nil
}
func (m *mockFirewallClient) Update(_ context.Context, _ string, req *godo.FirewallRequest) (*godo.Firewall, *godo.Response, error) {
	m.lastReq = req
	return m.fw, nil, m.err
}
func (m *mockFirewallClient) Delete(_ context.Context, _ string) (*godo.Response, error) {
	return nil, m.err
}

func testFirewall() *godo.Firewall {
	return &godo.Firewall{
		ID:     "fw-123",
		Name:   "my-fw",
		Status: "succeeded",
	}
}

func TestFirewallDriver_Create(t *testing.T) {
	mock := &mockFirewallClient{fw: testFirewall()}
	d := drivers.NewFirewallDriverWithClient(mock)

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-fw",
		Config: map[string]any{
			"droplet_ids": []any{123},
			"inbound_rules": []any{
				map[string]any{"protocol": "tcp", "ports": "80", "sources": []any{"0.0.0.0/0"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID != "fw-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "fw-123")
	}
}

func TestFirewallDriver_Create_Error(t *testing.T) {
	mock := &mockFirewallClient{err: fmt.Errorf("api failure")}
	d := drivers.NewFirewallDriverWithClient(mock)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-fw",
		// droplet_ids satisfies the targets-required validation so the
		// test exercises the API-error propagation path.
		Config: map[string]any{"droplet_ids": []any{123}},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestFirewallDriver_Read_Success(t *testing.T) {
	mock := &mockFirewallClient{fw: testFirewall()}
	d := drivers.NewFirewallDriverWithClient(mock)

	out, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name: "my-fw", ProviderID: "fw-123",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.ProviderID != "fw-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "fw-123")
	}
}

func TestFirewallDriver_Update_Success(t *testing.T) {
	mock := &mockFirewallClient{fw: testFirewall()}
	d := drivers.NewFirewallDriverWithClient(mock)

	out, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-fw", ProviderID: "fw-123",
	}, interfaces.ResourceSpec{
		Name:   "my-fw",
		Config: map[string]any{"droplet_ids": []any{123}},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.ProviderID != "fw-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "fw-123")
	}
}

func TestFirewallDriver_Update_Error(t *testing.T) {
	mock := &mockFirewallClient{err: fmt.Errorf("update failed")}
	d := drivers.NewFirewallDriverWithClient(mock)

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-fw", ProviderID: "fw-123",
	}, interfaces.ResourceSpec{
		Name:   "my-fw",
		Config: map[string]any{"droplet_ids": []any{123}},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestFirewallDriver_Delete_Success(t *testing.T) {
	mock := &mockFirewallClient{fw: testFirewall()}
	d := drivers.NewFirewallDriverWithClient(mock)

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "my-fw", ProviderID: "fw-123",
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestFirewallDriver_Delete_Error(t *testing.T) {
	mock := &mockFirewallClient{err: fmt.Errorf("delete failed")}
	d := drivers.NewFirewallDriverWithClient(mock)

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "my-fw", ProviderID: "fw-123",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestFirewallDriver_Diff_NilCurrent(t *testing.T) {
	mock := &mockFirewallClient{}
	d := drivers.NewFirewallDriverWithClient(mock)

	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{Name: "my-fw"}, nil)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=true when current is nil")
	}
}

func TestFirewallDriver_Diff_NoChanges(t *testing.T) {
	mock := &mockFirewallClient{}
	d := drivers.NewFirewallDriverWithClient(mock)

	current := &interfaces.ResourceOutput{ProviderID: "fw-123"}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{Name: "my-fw"}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=false when current exists")
	}
}

func TestFirewallDriver_HealthCheck(t *testing.T) {
	mock := &mockFirewallClient{fw: testFirewall()}
	d := drivers.NewFirewallDriverWithClient(mock)

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name:       "my-fw",
		ProviderID: "fw-123",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if !result.Healthy {
		t.Errorf("expected healthy firewall")
	}
}

func TestFirewallDriver_HealthCheck_Unhealthy(t *testing.T) {
	fw := &godo.Firewall{
		ID:     "fw-123",
		Name:   "my-fw",
		Status: "waiting",
	}
	mock := &mockFirewallClient{fw: fw}
	d := drivers.NewFirewallDriverWithClient(mock)

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name: "my-fw", ProviderID: "fw-123",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if result.Healthy {
		t.Errorf("expected unhealthy for firewall with status 'waiting'")
	}
}

func TestFirewallDriver_SupportsUpsert(t *testing.T) {
	d := drivers.NewFirewallDriverWithClient(&mockFirewallClient{})
	if !d.SupportsUpsert() {
		t.Error("FirewallDriver.SupportsUpsert() should return true")
	}
}

func TestFirewallDriver_Read_NameBased(t *testing.T) {
	mock := &mockFirewallClient{fw: testFirewall()}
	d := drivers.NewFirewallDriverWithClient(mock)

	// Read with empty ProviderID triggers name-based lookup.
	out, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name: "my-fw",
	})
	if err != nil {
		t.Fatalf("Read by name: %v", err)
	}
	if out.ProviderID != "fw-123" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "fw-123")
	}
	if out.Name != "my-fw" {
		t.Errorf("Name = %q, want %q", out.Name, "my-fw")
	}
}

func TestFirewallDriver_Read_NameBased_NotFound(t *testing.T) {
	mock := &mockFirewallClient{fw: nil}
	d := drivers.NewFirewallDriverWithClient(mock)

	_, err := d.Read(context.Background(), interfaces.ResourceRef{Name: "missing-fw"})
	if !errors.Is(err, drivers.ErrResourceNotFound) {
		t.Fatalf("expected ErrResourceNotFound, got: %v", err)
	}
}

// TestFirewallDriver_Read_NilClientReturn_NoPanic is a regression test for the
// nil-pointer dereference in fwOutput that would occur if the godo client
// returns (nil, nil, nil) for a Get call. The nil guard added to Read ensures
// a descriptive error is returned instead of a panic.
func TestFirewallDriver_Read_NilClientReturn_NoPanic(t *testing.T) {
	mock := &mockFirewallClient{fw: nil, err: nil} // client returns nil, nil, nil
	d := drivers.NewFirewallDriverWithClient(mock)

	_, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name: "my-fw", ProviderID: "fw-123",
	})
	if err == nil {
		t.Fatal("expected error for nil firewall returned by client, got nil")
	}
}

// TestFirewallDriver_HealthCheck_NilClientReturn verifies that HealthCheck does
// not panic when the godo client returns (nil, nil, nil). The nil guard ensures
// a non-healthy result with a descriptive message is returned instead.
func TestFirewallDriver_HealthCheck_NilClientReturn(t *testing.T) {
	mock := &mockFirewallClient{fw: nil, err: nil}
	d := drivers.NewFirewallDriverWithClient(mock)

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name: "my-fw", ProviderID: "fw-123",
	})
	if err != nil {
		t.Fatalf("HealthCheck: unexpected error: %v", err)
	}
	if result.Healthy {
		t.Error("expected Healthy=false for nil firewall")
	}
	if result.Message == "" {
		t.Error("expected non-empty Message for nil firewall")
	}
}

func TestFirewallDriver_Create_EmptyIDFromAPI(t *testing.T) {
	// API returns success but firewall has empty ID — guard must reject it.
	mock := &mockFirewallClient{fw: &godo.Firewall{Name: "my-fw"}}
	d := drivers.NewFirewallDriverWithClient(mock)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-fw",
		// droplet_ids satisfies the targets-required validation so this
		// test exercises the empty-ID guard path, not the targets path.
		Config: map[string]any{"droplet_ids": []any{123}},
	})
	if err == nil {
		t.Fatal("expected error for empty ProviderID, got nil")
	}
}

func TestFirewallDriver_Create_ProviderIDIsAPIAssigned(t *testing.T) {
	// ProviderID must be the API-assigned UUID, not the resource name.
	mock := &mockFirewallClient{fw: testFirewall()}
	d := drivers.NewFirewallDriverWithClient(mock)

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-fw",
		Config: map[string]any{"droplet_ids": []any{123}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID == "my-fw" {
		t.Errorf("ProviderID must not equal spec.Name %q; got %q", "my-fw", out.ProviderID)
	}
	if out.ProviderID == "" {
		t.Errorf("ProviderID must not be empty")
	}
}

// ── F7: Firewall target enforcement ──────────────────────────────────────────

// noTargetsErrFmt is the verbatim error string the spec requires when a
// firewall declares no targets. Character-for-character, including the em
// dash (U+2014) and the App Platform clause. Format-arg %q quotes the
// firewall name. See plan P-2.F7 step 3.
const noTargetsErrFmt = `firewall %q has no targets (specify droplet_ids or tags) — App Platform services cannot be firewall-protected; use expose: internal or trusted_sources`

// TestFirewallDriver_Create_DropletIDs_PassThrough verifies droplet_ids reach
// godo.FirewallRequest.DropletIDs.
func TestFirewallDriver_Create_DropletIDs_PassThrough(t *testing.T) {
	mock := &mockFirewallClient{fw: testFirewall()}
	d := drivers.NewFirewallDriverWithClient(mock)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-fw",
		Config: map[string]any{
			"droplet_ids": []any{123, 456},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if mock.lastReq == nil {
		t.Fatal("Create did not capture a FirewallRequest")
	}
	want := []int{123, 456}
	if got := mock.lastReq.DropletIDs; !equalIntSlices(got, want) {
		t.Errorf("DropletIDs = %v, want %v", got, want)
	}
}

// TestFirewallDriver_Create_Tags_PassThrough verifies tags reach
// godo.FirewallRequest.Tags.
func TestFirewallDriver_Create_Tags_PassThrough(t *testing.T) {
	mock := &mockFirewallClient{fw: testFirewall()}
	d := drivers.NewFirewallDriverWithClient(mock)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-fw",
		Config: map[string]any{
			"tags": []any{"bmw-prod"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if mock.lastReq == nil {
		t.Fatal("Create did not capture a FirewallRequest")
	}
	want := []string{"bmw-prod"}
	if got := mock.lastReq.Tags; !equalStringSlices(got, want) {
		t.Errorf("Tags = %v, want %v", got, want)
	}
}

// TestFirewallDriver_Create_BothTargets verifies droplet_ids AND tags both
// flow through when set together.
func TestFirewallDriver_Create_BothTargets(t *testing.T) {
	mock := &mockFirewallClient{fw: testFirewall()}
	d := drivers.NewFirewallDriverWithClient(mock)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-fw",
		Config: map[string]any{
			"droplet_ids": []any{123},
			"tags":        []any{"bmw-prod", "edge"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if mock.lastReq == nil {
		t.Fatal("Create did not capture a FirewallRequest")
	}
	if got, want := mock.lastReq.DropletIDs, []int{123}; !equalIntSlices(got, want) {
		t.Errorf("DropletIDs = %v, want %v", got, want)
	}
	if got, want := mock.lastReq.Tags, []string{"bmw-prod", "edge"}; !equalStringSlices(got, want) {
		t.Errorf("Tags = %v, want %v", got, want)
	}
}

// TestFirewallDriver_Update_Targets_PassThrough verifies droplet_ids and tags
// flow through Update.
func TestFirewallDriver_Update_Targets_PassThrough(t *testing.T) {
	mock := &mockFirewallClient{fw: testFirewall()}
	d := drivers.NewFirewallDriverWithClient(mock)

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-fw", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
	}, interfaces.ResourceSpec{
		Name: "my-fw",
		Config: map[string]any{
			"droplet_ids": []any{789},
			"tags":        []any{"bmw-prod"},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if mock.lastReq == nil {
		t.Fatal("Update did not capture a FirewallRequest")
	}
	if got, want := mock.lastReq.DropletIDs, []int{789}; !equalIntSlices(got, want) {
		t.Errorf("DropletIDs = %v, want %v", got, want)
	}
	if got, want := mock.lastReq.Tags, []string{"bmw-prod"}; !equalStringSlices(got, want) {
		t.Errorf("Tags = %v, want %v", got, want)
	}
}

// TestFirewallDriver_Create_NoTargets_Errors verifies that a firewall spec
// with neither droplet_ids nor tags fails Create with the exact spec error.
func TestFirewallDriver_Create_NoTargets_Errors(t *testing.T) {
	mock := &mockFirewallClient{fw: testFirewall()}
	d := drivers.NewFirewallDriverWithClient(mock)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-fw",
		// inbound_rules without targets is an App Platform footgun: there
		// is nothing for the rule to apply to.
		Config: map[string]any{
			"inbound_rules": []any{
				map[string]any{"protocol": "tcp", "ports": "80"},
			},
		},
	})
	if err == nil {
		t.Fatal("expected error for empty targets, got nil")
	}
	want := fmt.Sprintf(noTargetsErrFmt, "my-fw")
	if got := err.Error(); got != want {
		t.Errorf("error mismatch:\n got: %q\nwant: %q", got, want)
	}
	// Validation must short-circuit BEFORE the API call.
	if mock.lastReq != nil {
		t.Error("FirewallRequest reached godo client despite empty-targets validation")
	}
}

// TestFirewallDriver_Update_NoTargets_Errors verifies the same exact-string
// validation also fires on Update.
func TestFirewallDriver_Update_NoTargets_Errors(t *testing.T) {
	mock := &mockFirewallClient{fw: testFirewall()}
	d := drivers.NewFirewallDriverWithClient(mock)

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "my-fw", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5",
	}, interfaces.ResourceSpec{
		Name:   "my-fw",
		Config: map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error for empty targets, got nil")
	}
	want := fmt.Sprintf(noTargetsErrFmt, "my-fw")
	if got := err.Error(); got != want {
		t.Errorf("error mismatch:\n got: %q\nwant: %q", got, want)
	}
	if mock.lastReq != nil {
		t.Error("FirewallRequest reached godo client despite empty-targets validation")
	}
}

// TestFirewallDriver_Create_DropletIDs_AcceptsMixedNumeric verifies the
// helper accepts the YAML-decoded numeric variants (int, int64, float64) the
// modular YAML loader can produce.
func TestFirewallDriver_Create_DropletIDs_AcceptsMixedNumeric(t *testing.T) {
	mock := &mockFirewallClient{fw: testFirewall()}
	d := drivers.NewFirewallDriverWithClient(mock)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-fw",
		Config: map[string]any{
			"droplet_ids": []any{int(1), int64(2), float64(3)},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got, want := mock.lastReq.DropletIDs, []int{1, 2, 3}; !equalIntSlices(got, want) {
		t.Errorf("DropletIDs = %v, want %v", got, want)
	}
}

// TestFirewallDriver_Create_EmptyStringTagsRejected verifies that an
// all-empty-string tags slice fails the targets-required validation, and
// that a mixed slice is filtered to only the non-empty entries.
//
// Without filtering, `tagsFromConfig` appends "" to its output, making
// `len(req.Tags) > 0` falsely succeed; the DO API then rejects the empty
// tag at apply time — defeating F7's plan-time-fail contract. Fix is in
// `tagsFromConfig` (filter `s != ""`). (Code-review Finding 2, F7 round 2.)
func TestFirewallDriver_Create_EmptyStringTagsRejected(t *testing.T) {
	t.Run("all empty strings → no targets error", func(t *testing.T) {
		mock := &mockFirewallClient{fw: testFirewall()}
		d := drivers.NewFirewallDriverWithClient(mock)

		_, err := d.Create(context.Background(), interfaces.ResourceSpec{
			Name:   "my-fw",
			Config: map[string]any{"tags": []any{""}},
		})
		if err == nil {
			t.Fatal("expected no-targets error for tags: [\"\"]; got nil")
		}
		want := fmt.Sprintf(noTargetsErrFmt, "my-fw")
		if got := err.Error(); got != want {
			t.Errorf("error mismatch:\n got: %q\nwant: %q", got, want)
		}
		if mock.lastReq != nil {
			t.Error("FirewallRequest reached godo client despite all-empty tags")
		}
	})

	t.Run("mixed slice → empty entries filtered, non-empty kept", func(t *testing.T) {
		mock := &mockFirewallClient{fw: testFirewall()}
		d := drivers.NewFirewallDriverWithClient(mock)

		_, err := d.Create(context.Background(), interfaces.ResourceSpec{
			Name:   "my-fw",
			Config: map[string]any{"tags": []any{"", "bmw-prod", ""}},
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if got, want := mock.lastReq.Tags, []string{"bmw-prod"}; !equalStringSlices(got, want) {
			t.Errorf("Tags = %v, want %v (empty entries should be filtered)", got, want)
		}
	})
}

// TestFirewallDriver_Create_ZeroOrNegativeDropletIDsFiltered verifies that
// non-positive Droplet IDs are filtered out, by symmetry with the
// empty-string tag filter (Finding 2). Droplet IDs are positive integers
// assigned by the DO API; 0 and negatives are never valid and would be
// rejected at apply time.
//
// Without filtering, `dropletIDsFromConfig` appends every numeric to its
// output, so `droplet_ids: [0]` slips past validation and the DO API rejects
// it at runtime. (Code-review Finding 3, F7 round 2.)
func TestFirewallDriver_Create_ZeroOrNegativeDropletIDsFiltered(t *testing.T) {
	t.Run("only non-positives → no targets error", func(t *testing.T) {
		mock := &mockFirewallClient{fw: testFirewall()}
		d := drivers.NewFirewallDriverWithClient(mock)

		_, err := d.Create(context.Background(), interfaces.ResourceSpec{
			Name:   "my-fw",
			Config: map[string]any{"droplet_ids": []any{0, -1, int64(-2), float64(0)}},
		})
		if err == nil {
			t.Fatal("expected no-targets error for non-positive droplet IDs; got nil")
		}
		want := fmt.Sprintf(noTargetsErrFmt, "my-fw")
		if got := err.Error(); got != want {
			t.Errorf("error mismatch:\n got: %q\nwant: %q", got, want)
		}
		if mock.lastReq != nil {
			t.Error("FirewallRequest reached godo client despite all-non-positive droplet IDs")
		}
	})

	t.Run("mixed slice → non-positives filtered, positives kept", func(t *testing.T) {
		mock := &mockFirewallClient{fw: testFirewall()}
		d := drivers.NewFirewallDriverWithClient(mock)

		_, err := d.Create(context.Background(), interfaces.ResourceSpec{
			Name:   "my-fw",
			Config: map[string]any{"droplet_ids": []any{0, 123, int64(-1), float64(456)}},
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if got, want := mock.lastReq.DropletIDs, []int{123, 456}; !equalIntSlices(got, want) {
			t.Errorf("DropletIDs = %v, want %v (non-positives should be filtered)", got, want)
		}
	})
}

// ── F7 Finding 1 — Diff cascade ──────────────────────────────────────────────
//
// Pre-F7, FirewallDriver.Diff was a stub: it returned NeedsUpdate=true for nil
// current and NeedsUpdate=false otherwise, which made every in-place toggle of
// targets, tags, or rules a silent no-op at plan time. F7 makes target
// reconfiguration the most common firewall lifecycle action, so Diff must
// detect changes to droplet_ids, tags, inbound_rules, and outbound_rules.
// (Code-review Finding 1, F7 round 2.)

// TestFirewallDriver_FwOutput_RecordsTargetsAndRules verifies that Create
// returns a ResourceOutput whose Outputs map carries the four target/rule
// fields recovered from the godo.Firewall API response. Without these, Diff
// has nothing to compare against.
func TestFirewallDriver_FwOutput_RecordsTargetsAndRules(t *testing.T) {
	fw := &godo.Firewall{
		ID:         "fw-uuid",
		Name:       "my-fw",
		Status:     "succeeded",
		DropletIDs: []int{123, 456},
		Tags:       []string{"bmw-prod"},
		InboundRules: []godo.InboundRule{
			{Protocol: "tcp", PortRange: "443", Sources: &godo.Sources{Addresses: []string{"0.0.0.0/0"}}},
		},
		OutboundRules: []godo.OutboundRule{
			{Protocol: "tcp", PortRange: "1-65535", Destinations: &godo.Destinations{Addresses: []string{"0.0.0.0/0"}}},
		},
	}
	mock := &mockFirewallClient{fw: fw}
	d := drivers.NewFirewallDriverWithClient(mock)

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-fw",
		Config: map[string]any{"droplet_ids": []any{123, 456}, "tags": []any{"bmw-prod"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := out.Outputs["droplet_ids"]; !equalIntSlices(toInts(got), []int{123, 456}) {
		t.Errorf("Outputs[droplet_ids] = %v, want [123 456]", got)
	}
	if got := out.Outputs["tags"]; !equalStringSlices(toStrings(got), []string{"bmw-prod"}) {
		t.Errorf("Outputs[tags] = %v, want [bmw-prod]", got)
	}
	if _, ok := out.Outputs["inbound_rules"]; !ok {
		t.Error("Outputs[inbound_rules] missing")
	}
	if _, ok := out.Outputs["outbound_rules"]; !ok {
		t.Error("Outputs[outbound_rules] missing")
	}
}

// TestFirewallDriver_Diff_DetectsTargetsChange parametrically verifies that
// changes to each of the four canonical firewall fields produce a Plan
// action, and that no-op cases (and reorder for set fields) don't.
func TestFirewallDriver_Diff_DetectsTargetsChange(t *testing.T) {
	d := drivers.NewFirewallDriverWithClient(&mockFirewallClient{fw: testFirewall()})
	ctx := context.Background()

	// Helper: build a current ResourceOutput whose Outputs reflects a live
	// firewall (the way fwOutput will populate it after F7 round 2).
	makeCurrent := func(ids []int, tags []string, in []godo.InboundRule, out []godo.OutboundRule) *interfaces.ResourceOutput {
		return &interfaces.ResourceOutput{
			ProviderID: "fw-uuid",
			Outputs: map[string]any{
				"status":         "succeeded",
				"droplet_ids":    ids,
				"tags":           tags,
				"inbound_rules":  in,
				"outbound_rules": out,
			},
		}
	}

	cases := []struct {
		name       string
		desiredCfg map[string]any
		current    *interfaces.ResourceOutput
		wantUpdate bool
		wantPath   string // expected FieldChange.Path on first change; "" if no change
	}{
		{
			name: "droplet_ids change",
			desiredCfg: map[string]any{
				"droplet_ids": []any{789},
			},
			current:    makeCurrent([]int{123}, nil, nil, nil),
			wantUpdate: true,
			wantPath:   "droplet_ids",
		},
		{
			name: "tags change",
			desiredCfg: map[string]any{
				"tags": []any{"new-tag"},
			},
			current:    makeCurrent(nil, []string{"old-tag"}, nil, nil),
			wantUpdate: true,
			wantPath:   "tags",
		},
		{
			name: "inbound_rules change",
			desiredCfg: map[string]any{
				"droplet_ids": []any{123},
				"inbound_rules": []any{
					map[string]any{"protocol": "tcp", "ports": "443", "sources": []any{"0.0.0.0/0"}},
				},
			},
			current: makeCurrent(
				[]int{123}, nil,
				[]godo.InboundRule{{Protocol: "tcp", PortRange: "80", Sources: &godo.Sources{Addresses: []string{"0.0.0.0/0"}}}},
				nil,
			),
			wantUpdate: true,
			wantPath:   "inbound_rules",
		},
		{
			name: "outbound_rules change",
			desiredCfg: map[string]any{
				"droplet_ids": []any{123},
				"outbound_rules": []any{
					map[string]any{"protocol": "tcp", "ports": "1-65535", "destinations": []any{"0.0.0.0/0"}},
				},
			},
			current: makeCurrent(
				[]int{123}, nil, nil,
				[]godo.OutboundRule{{Protocol: "tcp", PortRange: "53", Destinations: &godo.Destinations{Addresses: []string{"0.0.0.0/0"}}}},
			),
			wantUpdate: true,
			wantPath:   "outbound_rules",
		},
		{
			name: "no change — droplet_ids and tags identical",
			desiredCfg: map[string]any{
				"droplet_ids": []any{123, 456},
				"tags":        []any{"bmw-prod"},
			},
			current:    makeCurrent([]int{123, 456}, []string{"bmw-prod"}, nil, nil),
			wantUpdate: false,
		},
		{
			name: "droplet_ids reorder is NOT a change (set semantics)",
			desiredCfg: map[string]any{
				"droplet_ids": []any{456, 123},
			},
			current:    makeCurrent([]int{123, 456}, nil, nil, nil),
			wantUpdate: false,
		},
		{
			name: "tags reorder is NOT a change (set semantics)",
			desiredCfg: map[string]any{
				"tags": []any{"b", "a"},
			},
			current:    makeCurrent(nil, []string{"a", "b"}, nil, nil),
			wantUpdate: false,
		},
		{
			name: "pre-F7 state — Outputs lacks target keys, desired has targets",
			desiredCfg: map[string]any{
				"droplet_ids": []any{123},
			},
			current:    &interfaces.ResourceOutput{ProviderID: "fw-uuid", Outputs: map[string]any{"status": "succeeded"}},
			wantUpdate: true,
			wantPath:   "droplet_ids",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := d.Diff(ctx, interfaces.ResourceSpec{Name: "my-fw", Config: tc.desiredCfg}, tc.current)
			if err != nil {
				t.Fatalf("Diff: %v", err)
			}
			if got.NeedsUpdate != tc.wantUpdate {
				t.Errorf("NeedsUpdate = %v, want %v (changes=%v)", got.NeedsUpdate, tc.wantUpdate, got.Changes)
			}
			if tc.wantPath != "" {
				found := false
				for _, c := range got.Changes {
					if c.Path == tc.wantPath {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected FieldChange with Path=%q in Changes=%+v", tc.wantPath, got.Changes)
				}
			}
		})
	}
}

// toInts is a tolerant Outputs-coercer for tests that may receive []int or
// []any-of-numerics depending on whether state was round-tripped through
// JSON/YAML state encoding.
func toInts(v any) []int {
	switch t := v.(type) {
	case []int:
		return append([]int(nil), t...)
	case []any:
		out := make([]int, 0, len(t))
		for _, x := range t {
			switch n := x.(type) {
			case int:
				out = append(out, n)
			case int64:
				out = append(out, int(n))
			case float64:
				out = append(out, int(n))
			}
		}
		return out
	}
	return nil
}

// toStrings is the analogous coercer for []string Outputs.
func toStrings(v any) []string {
	switch t := v.(type) {
	case []string:
		return append([]string(nil), t...)
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func equalIntSlices(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
