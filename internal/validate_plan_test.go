package internal

import (
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
)

// TestDOProvider_ValidatePlan_NilAndEmpty asserts the contract that
// ValidatePlan tolerates a nil plan and an empty actions slice without
// panicking; both return zero diagnostics. R-A10 invokes ValidatePlan
// before any align-time enrichment so the early-return cases must hold.
func TestDOProvider_ValidatePlan_NilAndEmpty(t *testing.T) {
	p := NewDOProvider()
	if d := p.ValidatePlan(nil); len(d) != 0 {
		t.Errorf("nil plan: expected 0 diagnostics, got %d: %+v", len(d), d)
	}
	if d := p.ValidatePlan(&interfaces.IaCPlan{}); len(d) != 0 {
		t.Errorf("empty plan: expected 0 diagnostics, got %d: %+v", len(d), d)
	}
}

// TestDOProvider_ValidatePlan_AppPlatformGroupSlugAccepted asserts that
// an App Platform action with a valid region GROUP slug ("nyc") emits no
// diagnostics. This is the canonical happy-path for AP — region groups
// are the App Platform API's required region shape.
func TestDOProvider_ValidatePlan_AppPlatformGroupSlugAccepted(t *testing.T) {
	p := NewDOProvider()
	plan := &interfaces.IaCPlan{Actions: []interfaces.PlanAction{
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "core-app", Type: "infra.container_service",
			Config: map[string]any{"region": "nyc"},
		}},
	}}
	if d := p.ValidatePlan(plan); len(d) != 0 {
		t.Errorf("expected 0 diagnostics for AP region=nyc; got %+v", d)
	}
}

// TestDOProvider_ValidatePlan_AppPlatformZoneSlugRejected asserts that
// an App Platform action with a zone slug ("nyc3") gets a Severity=Error
// diagnostic explaining the AP-region-group requirement. This is the
// concrete root-cause-issue-D defense from the conformance design: an
// operator who copy-pasted nyc3 from a Droplet config gets a clear
// explanation pre-apply, instead of a cryptic DO API rejection at
// `wfctl infra apply`.
func TestDOProvider_ValidatePlan_AppPlatformZoneSlugRejected(t *testing.T) {
	p := NewDOProvider()
	plan := &interfaces.IaCPlan{Actions: []interfaces.PlanAction{
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "core-app", Type: "infra.container_service",
			Config: map[string]any{"region": "nyc3"},
		}},
	}}
	d := p.ValidatePlan(plan)
	if len(d) != 1 {
		t.Fatalf("expected 1 diagnostic for AP region=nyc3; got %d: %+v", len(d), d)
	}
	got := d[0]
	if got.Severity != interfaces.PlanDiagnosticError {
		t.Errorf("severity = %d, want Error (%d)", got.Severity, interfaces.PlanDiagnosticError)
	}
	if got.Resource != "core-app" || got.Field != "region" {
		t.Errorf("diagnostic resource/field = %q/%q, want core-app/region", got.Resource, got.Field)
	}
	if !strings.Contains(got.Message, "region GROUP") {
		t.Errorf("message should explain the group requirement; got %q", got.Message)
	}
	if !strings.Contains(got.Message, "nyc3") {
		t.Errorf("message should cite the offending value nyc3; got %q", got.Message)
	}
}

// TestDOProvider_ValidatePlan_VPCRequiresZone asserts that a VPC with a
// bare group slug ("nyc") gets a Severity=Error diagnostic — the DO VPC
// API requires a specific zone, the inverse of the App Platform
// constraint above.
func TestDOProvider_ValidatePlan_VPCRequiresZone(t *testing.T) {
	p := NewDOProvider()
	plan := &interfaces.IaCPlan{Actions: []interfaces.PlanAction{
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "core-vpc", Type: "infra.vpc",
			Config: map[string]any{"region": "nyc"},
		}},
	}}
	d := p.ValidatePlan(plan)
	if len(d) != 1 || d[0].Severity != interfaces.PlanDiagnosticError {
		t.Fatalf("expected 1 Error diagnostic for VPC region=nyc; got %+v", d)
	}
	if !strings.Contains(d[0].Message, "zone slug") {
		t.Errorf("message should explain the zone-slug requirement; got %q", d[0].Message)
	}
}

// TestDOProvider_ValidatePlan_DropletAndVolumeAcceptZone asserts the
// happy path for the inverse direction — Droplet/Volume with valid zone
// slugs emit no diagnostics.
func TestDOProvider_ValidatePlan_DropletAndVolumeAcceptZone(t *testing.T) {
	p := NewDOProvider()
	plan := &interfaces.IaCPlan{Actions: []interfaces.PlanAction{
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "core-droplet", Type: "infra.droplet",
			Config: map[string]any{"region": "nyc1"},
		}},
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "core-vol", Type: "infra.volume",
			Config: map[string]any{"region": "nyc1"},
		}},
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "core-vpc", Type: "infra.vpc",
			Config: map[string]any{"region": "nyc1"},
		}},
	}}
	if d := p.ValidatePlan(plan); len(d) != 0 {
		t.Errorf("expected 0 diagnostics for zone-slug-armed plan; got %+v", d)
	}
}

// TestDOProvider_ValidatePlan_AppPlatformVPCRefRegionMismatch is the
// flagship test: App Platform group "nyc" with a vpc_ref pointing to a
// VPC declared in zone "sfo3" must produce an Error diagnostic with the
// vpc_ref field path. This locks the recurring "App Platform in nyc
// cannot reach VPC in sfo3" production bug class (root-cause issue D).
func TestDOProvider_ValidatePlan_AppPlatformVPCRefRegionMismatch(t *testing.T) {
	p := NewDOProvider()
	plan := &interfaces.IaCPlan{Actions: []interfaces.PlanAction{
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "core-vpc", Type: "infra.vpc",
			Config: map[string]any{"region": "sfo3"},
		}},
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "core-app", Type: "infra.container_service",
			Config: map[string]any{"region": "nyc", "vpc_ref": "core-vpc"},
		}},
	}}
	d := p.ValidatePlan(plan)
	if len(d) != 1 {
		t.Fatalf("expected 1 diagnostic for AP/VPC region mismatch; got %d: %+v", len(d), d)
	}
	if d[0].Severity != interfaces.PlanDiagnosticError {
		t.Errorf("severity = %d, want Error", d[0].Severity)
	}
	if d[0].Resource != "core-app" || d[0].Field != "vpc_ref" {
		t.Errorf("resource/field = %q/%q, want core-app/vpc_ref", d[0].Resource, d[0].Field)
	}
	for _, want := range []string{"nyc", "sfo3", "core-vpc"} {
		if !strings.Contains(d[0].Message, want) {
			t.Errorf("message should cite %q; got %q", want, d[0].Message)
		}
	}
}

// TestDOProvider_ValidatePlan_AppPlatformVPCRefRegionMatchHappyPath
// covers the inverse: AP group "nyc" + VPC zone "nyc1" → no diagnostic.
// Locks the contract that the cross-resource check is restricted to
// genuine mismatches.
func TestDOProvider_ValidatePlan_AppPlatformVPCRefRegionMatchHappyPath(t *testing.T) {
	p := NewDOProvider()
	plan := &interfaces.IaCPlan{Actions: []interfaces.PlanAction{
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "core-vpc", Type: "infra.vpc",
			Config: map[string]any{"region": "nyc1"},
		}},
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "core-app", Type: "infra.container_service",
			Config: map[string]any{"region": "nyc", "vpc_ref": "core-vpc"},
		}},
	}}
	if d := p.ValidatePlan(plan); len(d) != 0 {
		t.Errorf("expected 0 diagnostics for AP nyc + VPC nyc1; got %+v", d)
	}
}

// TestDOProvider_ValidatePlan_AppPlatformVPCRefUnknownEmitsWarning
// asserts that vpc_ref pointing to a name not in the plan emits a
// Severity=Warning (not Error) — the operator may have an existing VPC
// whose region is not yet resolved, so the strict mode escalates while
// non-strict tolerates.
func TestDOProvider_ValidatePlan_AppPlatformVPCRefUnknownEmitsWarning(t *testing.T) {
	p := NewDOProvider()
	plan := &interfaces.IaCPlan{Actions: []interfaces.PlanAction{
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "core-app", Type: "infra.container_service",
			Config: map[string]any{"region": "nyc", "vpc_ref": "missing-vpc"},
		}},
	}}
	d := p.ValidatePlan(plan)
	if len(d) != 1 || d[0].Severity != interfaces.PlanDiagnosticWarning {
		t.Fatalf("expected 1 Warning diagnostic for unknown vpc_ref; got %+v", d)
	}
	if d[0].Field != "vpc_ref" || !strings.Contains(d[0].Message, "missing-vpc") {
		t.Errorf("message should cite the unknown vpc_ref name; got %+v", d[0])
	}
}

// TestDOProvider_ValidatePlan_AppPlatformVPCRefResolvesCurrentState
// asserts that the cross-resource region check looks at action.Current's
// outputs when no desired region is set on the VPC's spec — the typical
// shape when an existing VPC is unchanged but referenced by a new
// AppPlatform action.
func TestDOProvider_ValidatePlan_AppPlatformVPCRefResolvesCurrentState(t *testing.T) {
	p := NewDOProvider()
	plan := &interfaces.IaCPlan{Actions: []interfaces.PlanAction{
		{
			Action: "update",
			Resource: interfaces.ResourceSpec{
				Name: "existing-vpc", Type: "infra.vpc",
				// No region in the desired spec — must fall back to current state.
				Config: map[string]any{},
			},
			Current: &interfaces.ResourceState{
				Name: "existing-vpc", Type: "infra.vpc",
				Outputs: map[string]any{"region": "sfo3"},
			},
		},
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "core-app", Type: "infra.container_service",
			Config: map[string]any{"region": "nyc", "vpc_ref": "existing-vpc"},
		}},
	}}
	d := p.ValidatePlan(plan)
	if len(d) != 1 || d[0].Severity != interfaces.PlanDiagnosticError {
		t.Fatalf("expected 1 Error for existing-VPC region mismatch via current state; got %+v", d)
	}
	if !strings.Contains(d[0].Message, "sfo3") {
		t.Errorf("message should cite the current-state region sfo3; got %q", d[0].Message)
	}
}

// TestDOProvider_ValidatePlan_DeleteActionsSkipped asserts that delete
// actions with empty Config don't trigger false positives. A delete plan
// targets the existing resource for removal — the "desired" spec carries
// no region (user is asking for absence), so region constraints would
// always fire on every delete if not skipped.
func TestDOProvider_ValidatePlan_DeleteActionsSkipped(t *testing.T) {
	p := NewDOProvider()
	plan := &interfaces.IaCPlan{Actions: []interfaces.PlanAction{
		{Action: "delete", Resource: interfaces.ResourceSpec{
			Name: "old-app", Type: "infra.container_service", Config: map[string]any{},
		}, Current: &interfaces.ResourceState{Name: "old-app", Type: "infra.container_service", ProviderID: "app-1"}},
		{Action: "delete", Resource: interfaces.ResourceSpec{
			Name: "old-vpc", Type: "infra.vpc", Config: map[string]any{},
		}, Current: &interfaces.ResourceState{Name: "old-vpc", Type: "infra.vpc", ProviderID: "vpc-1"}},
	}}
	if d := p.ValidatePlan(plan); len(d) != 0 {
		t.Errorf("expected 0 diagnostics for delete-only plan; got %+v", d)
	}
}

// TestDOProvider_ValidatePlan_DatabaseDanglingVPCRef asserts the
// regression-pin case the conformance suite locks
// (Scenario_CrossResourceConstraintRejection): a database with a
// vpc_ref pointing to a name not present in the plan emits at least
// one Severity=Error diagnostic. This is the in-tree mirror of the
// portable conformance assertion.
func TestDOProvider_ValidatePlan_DatabaseDanglingVPCRef(t *testing.T) {
	p := NewDOProvider()
	plan := &interfaces.IaCPlan{Actions: []interfaces.PlanAction{
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "db", Type: "infra.database",
			Config: map[string]any{"vpc_ref": "missing-vpc"},
		}},
	}}
	d := p.ValidatePlan(plan)
	if len(d) == 0 {
		t.Fatal("expected at least one diagnostic for dangling vpc_ref")
	}
	var hasError bool
	for _, x := range d {
		if x.Message == "" {
			t.Errorf("diagnostic message must be non-empty: %+v", x)
		}
		if x.Severity == interfaces.PlanDiagnosticError {
			hasError = true
		}
	}
	if !hasError {
		t.Errorf("expected at least one Severity=Error diagnostic; got %+v", d)
	}
}

// TestDOProvider_ValidatePlan_DatabaseVPCRefToDeleteTargetIsDangling
// pins Copilot review #1: a vpc_ref pointing to a VPC that is being
// deleted in the same plan must be treated as a dangling reference
// (Severity=Error), NOT silently accepted as if the VPC were live.
// The byName index must exclude delete-action resources for
// cross-resource resolution to work correctly.
func TestDOProvider_ValidatePlan_DatabaseVPCRefToDeleteTargetIsDangling(t *testing.T) {
	p := NewDOProvider()
	plan := &interfaces.IaCPlan{Actions: []interfaces.PlanAction{
		// VPC scheduled for deletion in this plan.
		{
			Action: "delete",
			Resource: interfaces.ResourceSpec{Name: "old-vpc", Type: "infra.vpc", Config: map[string]any{}},
			Current: &interfaces.ResourceState{
				Name: "old-vpc", Type: "infra.vpc", ProviderID: "vpc-old",
				Outputs: map[string]any{"region": "nyc1"},
			},
		},
		// Database referencing the soon-to-be-deleted VPC.
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "db", Type: "infra.database",
			Config: map[string]any{"vpc_ref": "old-vpc"},
		}},
	}}
	d := p.ValidatePlan(plan)
	if len(d) == 0 {
		t.Fatal("expected at least one diagnostic for vpc_ref to delete-target")
	}
	var hasDanglingError bool
	for _, x := range d {
		if x.Severity == interfaces.PlanDiagnosticError && x.Field == "vpc_ref" && x.Resource == "db" {
			hasDanglingError = true
		}
	}
	if !hasDanglingError {
		t.Errorf("expected Severity=Error vpc_ref diagnostic on db resource; got %+v", d)
	}
}

// TestDOProvider_ValidatePlan_DatabaseVPCRefInPlanNoFinding asserts
// the inverse — when a database vpc_ref resolves to an in-plan VPC,
// no diagnostic is emitted (well-formed plan, happy path).
func TestDOProvider_ValidatePlan_DatabaseVPCRefInPlanNoFinding(t *testing.T) {
	p := NewDOProvider()
	plan := &interfaces.IaCPlan{Actions: []interfaces.PlanAction{
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "core-vpc", Type: "infra.vpc",
			Config: map[string]any{"region": "nyc1"},
		}},
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "db", Type: "infra.database",
			Config: map[string]any{"vpc_ref": "core-vpc"},
		}},
	}}
	if d := p.ValidatePlan(plan); len(d) != 0 {
		t.Errorf("expected 0 diagnostics for in-plan vpc_ref; got %+v", d)
	}
}

// TestDOProvider_ValidatePlan_CompileTimeAssertion documents that the
// compile-time interface assertion in validate_plan.go locks
// DOProvider's ProviderValidator implementation. If the interface
// signature changes upstream, the compile-time check catches the drift
// before any test runs. This test exists to make the assertion visible
// in test output and to fail the build if ValidatePlan goes missing.
func TestDOProvider_ValidatePlan_CompileTimeAssertion(t *testing.T) {
	var _ interfaces.ProviderValidator = NewDOProvider()
}
