package drivers_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
	"google.golang.org/protobuf/types/known/structpb"
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

// ── Firewall target enforcement ──────────────────────────────────────────────

// Tests reference the no-targets error format string via
// `drivers.NoTargetsErrFmt`, the exported source-of-truth in firewall.go.
// Re-defining the literal here would risk drift the next time the
// validator's wording is tightened.

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
	want := fmt.Sprintf(drivers.NoTargetsErrFmt, "my-fw")
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
	want := fmt.Sprintf(drivers.NoTargetsErrFmt, "my-fw")
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
// that a mixed slice is filtered to only the non-empty entries. This is a
// regression: empty-string tags must be filtered so target validation
// catches the no-targets condition at validation time rather than letting
// the DO API reject them at apply time — defeating F7's fail-fast contract.
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
		want := fmt.Sprintf(drivers.NoTargetsErrFmt, "my-fw")
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
// empty-string tag filter. Droplet IDs are positive integers assigned by
// the DO API; 0 and negatives are never valid and would be rejected at
// apply time. This is a regression: non-positive droplet IDs must be
// filtered so target validation catches no-targets at validation time
// rather than letting the DO API reject them at apply time.
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
		want := fmt.Sprintf(drivers.NoTargetsErrFmt, "my-fw")
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

// ── Diff cascade ─────────────────────────────────────────────────────────────
//
// FirewallDriver.Diff must detect target/rule changes so that toggling
// droplet_ids, tags, inbound_rules, or outbound_rules between deploys
// produces a Plan action. A stub Diff that always reported NeedsUpdate=false
// for non-nil current would silently no-op the most common firewall lifecycle
// action — target reconfiguration — so the comparison covers all four
// canonical fields against state recorded by `fwOutput`.

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
	// firewall in the structpb-compatible canonical shape that fwOutput
	// produces (no typed slices on Outputs — see the StructpbBoundary
	// tests for why).
	canonicalInbound := func(rules []godo.InboundRule) []any {
		if len(rules) == 0 {
			return nil
		}
		out := make([]any, 0, len(rules))
		for _, r := range rules {
			var addrs []any
			if r.Sources != nil {
				for _, a := range r.Sources.Addresses {
					addrs = append(addrs, a)
				}
			}
			out = append(out, map[string]any{
				"protocol": r.Protocol,
				"ports":    r.PortRange,
				"sources":  addrs,
			})
		}
		return out
	}
	canonicalOutbound := func(rules []godo.OutboundRule) []any {
		if len(rules) == 0 {
			return nil
		}
		out := make([]any, 0, len(rules))
		for _, r := range rules {
			var addrs []any
			if r.Destinations != nil {
				for _, a := range r.Destinations.Addresses {
					addrs = append(addrs, a)
				}
			}
			out = append(out, map[string]any{
				"protocol":     r.Protocol,
				"ports":        r.PortRange,
				"destinations": addrs,
			})
		}
		return out
	}
	idsToAny := func(ids []int) []any {
		if len(ids) == 0 {
			return nil
		}
		out := make([]any, 0, len(ids))
		for _, n := range ids {
			out = append(out, float64(n))
		}
		return out
	}
	tagsToAny := func(tags []string) []any {
		if len(tags) == 0 {
			return nil
		}
		out := make([]any, 0, len(tags))
		for _, t := range tags {
			out = append(out, t)
		}
		return out
	}
	makeCurrent := func(ids []int, tags []string, in []godo.InboundRule, out []godo.OutboundRule) *interfaces.ResourceOutput {
		return &interfaces.ResourceOutput{
			ProviderID: "fw-uuid",
			Outputs: map[string]any{
				"status":         "succeeded",
				"droplet_ids":    idsToAny(ids),
				"tags":           tagsToAny(tags),
				"inbound_rules":  canonicalInbound(in),
				"outbound_rules": canonicalOutbound(out),
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

// ── F7 Round 3 — gRPC structpb boundary regression ──────────────────────────
//
// The wfctl→plugin gRPC dispatch path encodes outputs through
// `structpb.NewStruct` then decodes via `.AsMap()`. structpb rejects native
// typed slices ([]string, []int, []godo.InboundRule, …) with "proto: invalid
// type"; numerics survive only as float64; structs lose their type identity
// and become map[string]any. Round-2 stored typed slices on Outputs, which
// meant the entire Diff cascade fix was a no-op in production gRPC mode —
// every reconcile would either fail at structpb encoding or surface spurious
// FieldChange because the post-roundtrip type-assertions returned ok=false.
//
// These tests pin the structpb-compatible Outputs contract.

// firewallOutputsRoundTrip simulates the wfctl→plugin gRPC dispatch boundary
// by encoding Outputs through structpb.NewStruct then decoding via AsMap().
// Mirrors `internal.grpcRoundTrip` (kept local because that helper lives in
// a different package).
func firewallOutputsRoundTrip(t *testing.T, outputs map[string]any) map[string]any {
	t.Helper()
	if outputs == nil {
		return nil
	}
	s, err := structpb.NewStruct(outputs)
	if err != nil {
		t.Fatalf("structpb.NewStruct rejected Outputs (typed slices on Outputs are a bug): %v", err)
	}
	return s.AsMap()
}

// TestFirewallDriver_StructpbBoundary_FwOutputAcceptedByStructpb pins the
// invariant that Outputs values returned by Create / Read are structpb-
// compatible. Without this, the wfctl→plugin gRPC encoding fails before the
// outputs even reach the wire.
func TestFirewallDriver_StructpbBoundary_FwOutputAcceptedByStructpb(t *testing.T) {
	fw := &godo.Firewall{
		ID: "fw-uuid", Name: "my-fw", Status: "succeeded",
		DropletIDs: []int{123, 456},
		Tags:       []string{"bmw-prod", "edge"},
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
		Config: map[string]any{"droplet_ids": []any{123, 456}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := structpb.NewStruct(out.Outputs); err != nil {
		t.Fatalf("structpb.NewStruct rejected Outputs (Outputs must be structpb-compatible — no typed slices, no struct values): %v", err)
	}
}

// TestFirewallDriver_StructpbBoundary_DiffSurvivesRoundTrip is the canonical
// regression for round-2's bug. It records the live firewall via fwOutput,
// round-trips Outputs through structpb, then asserts Diff against the
// matching desired spec produces NeedsUpdate=false. A failure here means the
// Diff cascade fix is a no-op in production gRPC mode.
func TestFirewallDriver_StructpbBoundary_DiffSurvivesRoundTrip(t *testing.T) {
	fw := &godo.Firewall{
		ID: "fw-uuid", Name: "my-fw", Status: "succeeded",
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

	// Record the firewall via Create (which calls fwOutput internally).
	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-fw",
		Config: map[string]any{"droplet_ids": []any{123, 456}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Round-trip Outputs through the structpb gRPC boundary.
	rtOutputs := firewallOutputsRoundTrip(t, out.Outputs)

	// Build a current state with the round-tripped Outputs.
	current := &interfaces.ResourceOutput{
		ProviderID: out.ProviderID,
		Outputs:    rtOutputs,
	}

	// Same desired spec — Diff must NOT report a change.
	desired := interfaces.ResourceSpec{
		Name: "my-fw",
		Config: map[string]any{
			"droplet_ids": []any{123, 456},
			"tags":        []any{"bmw-prod"},
			"inbound_rules": []any{
				map[string]any{"protocol": "tcp", "ports": "443", "sources": []any{"0.0.0.0/0"}},
			},
			"outbound_rules": []any{
				map[string]any{"protocol": "tcp", "ports": "1-65535", "destinations": []any{"0.0.0.0/0"}},
			},
		},
	}
	got, err := d.Diff(context.Background(), desired, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if got.NeedsUpdate {
		t.Errorf("NeedsUpdate = true after structpb round-trip, want false. Spurious changes: %+v", got.Changes)
	}
}

// TestFirewallDriver_DropletIDs_FractionalFloat_Rejected verifies that
// fractional float values are rejected rather than silently truncated.
// YAML's `123.9` would otherwise become Droplet ID 123 — the wrong
// Droplet attached. structpb represents all numerics as float64, so a
// fractional input survives the gRPC boundary and the int conversion
// must reject it explicitly.
func TestFirewallDriver_DropletIDs_FractionalFloat_Rejected(t *testing.T) {
	t.Run("fractional float rejected", func(t *testing.T) {
		mock := &mockFirewallClient{fw: testFirewall()}
		d := drivers.NewFirewallDriverWithClient(mock)

		_, err := d.Create(context.Background(), interfaces.ResourceSpec{
			Name:   "my-fw",
			Config: map[string]any{"droplet_ids": []any{123.9}},
		})
		if err == nil {
			t.Fatal("expected error for fractional droplet_ids, got nil")
		}
		if mock.lastReq != nil {
			t.Error("FirewallRequest reached godo client despite fractional droplet_ids")
		}
	})

	t.Run("integer-valued float accepted", func(t *testing.T) {
		mock := &mockFirewallClient{fw: testFirewall()}
		d := drivers.NewFirewallDriverWithClient(mock)

		_, err := d.Create(context.Background(), interfaces.ResourceSpec{
			Name:   "my-fw",
			Config: map[string]any{"droplet_ids": []any{123.0}},
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if got, want := mock.lastReq.DropletIDs, []int{123}; !equalIntSlices(got, want) {
			t.Errorf("DropletIDs = %v, want %v", got, want)
		}
	})
}
