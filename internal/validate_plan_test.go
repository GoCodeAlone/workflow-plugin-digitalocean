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

// TestDOProvider_ValidatePlan_DatabaseVPCRefToNonVPCType pins
// Copilot review #6 (round 2): a database vpc_ref pointing to a name
// that resolves in the plan but to a NON-VPC type (e.g., a droplet)
// must Error. Type-checking at plan time avoids confusing 4xx errors
// from the DO database API at apply time.
func TestDOProvider_ValidatePlan_DatabaseVPCRefToNonVPCType(t *testing.T) {
	p := NewDOProvider()
	plan := &interfaces.IaCPlan{Actions: []interfaces.PlanAction{
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "core-droplet", Type: "infra.droplet",
			Config: map[string]any{"region": "nyc1"},
		}},
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "db", Type: "infra.database",
			Config: map[string]any{"vpc_ref": "core-droplet"},
		}},
	}}
	d := p.ValidatePlan(plan)
	var hasTypeError bool
	for _, x := range d {
		if x.Severity == interfaces.PlanDiagnosticError &&
			x.Resource == "db" && x.Field == "vpc_ref" &&
			strings.Contains(x.Message, "infra.droplet") {
			hasTypeError = true
		}
	}
	if !hasTypeError {
		t.Errorf("expected Severity=Error with infra.droplet in message; got %+v", d)
	}
}

// TestDOProvider_ValidatePlan_AppPlatformVPCRefToNonVPCType pins
// Copilot review #7 (round 2): same type-checking for App Platform
// vpc_ref pointing to a non-VPC resource (e.g., another App Platform
// app). Without this check the region-match logic would silently skip
// because target.spec.Config["region"] would be a region GROUP for an
// App Platform target, not a zone.
func TestDOProvider_ValidatePlan_AppPlatformVPCRefToNonVPCType(t *testing.T) {
	p := NewDOProvider()
	plan := &interfaces.IaCPlan{Actions: []interfaces.PlanAction{
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "other-app", Type: "infra.container_service",
			Config: map[string]any{"region": "nyc"},
		}},
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "core-app", Type: "infra.container_service",
			Config: map[string]any{"region": "nyc", "vpc_ref": "other-app"},
		}},
	}}
	d := p.ValidatePlan(plan)
	var hasTypeError bool
	for _, x := range d {
		if x.Severity == interfaces.PlanDiagnosticError &&
			x.Resource == "core-app" && x.Field == "vpc_ref" &&
			strings.Contains(x.Message, "infra.container_service") {
			hasTypeError = true
		}
	}
	if !hasTypeError {
		t.Errorf("expected Severity=Error with infra.container_service in message; got %+v", d)
	}
}

// TestDOProvider_ValidatePlan_VPCRefAsUUIDIsDeferred pins Copilot
// review #8/#9 (round 3): vpc_ref values that look like a VPC UUID
// (the canonical DO Apps/Databases API shape at apply time) MUST NOT
// trigger the in-plan-name-resolution branch — they are deferred to
// apply-time validation by wfctlhelpers.ApplyPlan after JIT
// substitution. The test exercises both the database and the App
// Platform paths with a fixed UUID literal.
func TestDOProvider_ValidatePlan_VPCRefAsUUIDIsDeferred(t *testing.T) {
	const vpcUUID = "a1b2c3d4-e5f6-7890-abcd-ef0123456789"
	p := NewDOProvider()
	plan := &interfaces.IaCPlan{Actions: []interfaces.PlanAction{
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "db", Type: "infra.database",
			Config: map[string]any{"vpc_ref": vpcUUID},
		}},
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "core-app", Type: "infra.container_service",
			Config: map[string]any{"region": "nyc", "vpc_ref": vpcUUID},
		}},
	}}
	if d := p.ValidatePlan(plan); len(d) != 0 {
		t.Errorf("expected 0 diagnostics for UUID vpc_ref values; got %+v", d)
	}
}

// TestDOProvider_ValidatePlan_VPCRefAsUpperCaseUUIDIsDeferred pins
// Copilot review #12 (round 4): UUIDs are case-insensitive; an
// upper-case or mixed-case VPC UUID must also be classified as a
// UUID literal (not as a plain resource name) so it does not trigger
// false dangling-reference diagnostics. Verifies the (?i) flag on
// uuidPattern.
func TestDOProvider_ValidatePlan_VPCRefAsUpperCaseUUIDIsDeferred(t *testing.T) {
	p := NewDOProvider()
	plan := &interfaces.IaCPlan{Actions: []interfaces.PlanAction{
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "db", Type: "infra.database",
			Config: map[string]any{"vpc_ref": "A1B2C3D4-E5F6-7890-ABCD-EF0123456789"},
		}},
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "app", Type: "infra.container_service",
			Config: map[string]any{"region": "nyc", "vpc_ref": "A1b2C3d4-E5f6-7890-AbCd-Ef0123456789"},
		}},
	}}
	if d := p.ValidatePlan(plan); len(d) != 0 {
		t.Errorf("expected 0 diagnostics for upper/mixed-case UUID vpc_ref; got %+v", d)
	}
}

// TestDOProvider_ValidatePlan_VPCRefAsJITTemplateIsDeferred pins
// Copilot review #8/#9 (round 3): vpc_ref values that contain the
// wfctl JIT substitution syntax (${MODULE.field} or $(...)) MUST NOT
// trigger name-based resolution at plan time — they are resolved by
// wfctlhelpers.ApplyPlan's jitsubst.ResolveSpec at apply time. The
// test exercises both the database and the App Platform paths with
// canonical ${vpc.id} templates.
func TestDOProvider_ValidatePlan_VPCRefAsJITTemplateIsDeferred(t *testing.T) {
	p := NewDOProvider()
	plan := &interfaces.IaCPlan{Actions: []interfaces.PlanAction{
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "db", Type: "infra.database",
			Config: map[string]any{"vpc_ref": "${some-vpc.id}"},
		}},
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "core-app", Type: "infra.container_service",
			Config: map[string]any{"region": "nyc", "vpc_ref": "$(other-vpc.id)"},
		}},
	}}
	if d := p.ValidatePlan(plan); len(d) != 0 {
		t.Errorf("expected 0 diagnostics for JIT-template vpc_ref values; got %+v", d)
	}
}

// TestDOProvider_ValidatePlan_ClassifyRegionEmptyGroup pins Copilot
// review #10 (round 3): a Droplet/VPC region set to a zone whose
// zoneToGroup mapping is the empty string (e.g., legacy nyc2/ams2)
// must produce a clear human-readable diagnostic, not 'a zone slug in
// group ""'. This test indirectly verifies via the message content of
// the bare-group rejection on a Droplet using nyc2.
//
// Note: nyc2 IS a valid zone slug (passes isZoneSlug), so a Droplet
// with region=nyc2 alone would pass validation. We exercise the
// empty-group branch via the App Platform group-vs-zone mismatch path
// where classifyRegion is called on a non-AP-routed zone.
func TestDOProvider_ValidatePlan_ClassifyRegionEmptyGroup(t *testing.T) {
	p := NewDOProvider()
	plan := &interfaces.IaCPlan{Actions: []interfaces.PlanAction{
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "core-app", Type: "infra.container_service",
			// nyc2 is a valid zone slug but not in any AP group.
			Config: map[string]any{"region": "nyc2"},
		}},
	}}
	d := p.ValidatePlan(plan)
	if len(d) != 1 {
		t.Fatalf("expected 1 diagnostic for AP region=nyc2; got %d: %+v", len(d), d)
	}
	if strings.Contains(d[0].Message, `group ""`) {
		t.Errorf("diagnostic still emits empty-group label; got %q", d[0].Message)
	}
	if !strings.Contains(d[0].Message, "not in any App Platform region group") {
		t.Errorf("expected 'not in any App Platform region group' phrasing; got %q", d[0].Message)
	}
}

// TestDOProvider_ValidatePlan_UnknownRegionSlugWarnsNotErrors pins
// Copilot review #16 (round 5): a region slug that is neither a known
// App Platform group nor a known zone (e.g., a brand-new DO region
// 'atl1' the plugin's hardcoded allowlist hasn't caught up to) MUST
// downgrade to Severity=Warning so non-strict align lets operators
// proceed without a plugin bump. The documented misconfig cases
// (group-where-zone-required, zone-where-group-required) keep
// Severity=Error.
func TestDOProvider_ValidatePlan_UnknownRegionSlugWarnsNotErrors(t *testing.T) {
	p := NewDOProvider()
	plan := &interfaces.IaCPlan{Actions: []interfaces.PlanAction{
		// VPC with unknown region — should Warning (forward-compat).
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "fwd-vpc", Type: "infra.vpc",
			Config: map[string]any{"region": "atl1"},
		}},
		// AP with unknown region — should Warning (forward-compat).
		{Action: "create", Resource: interfaces.ResourceSpec{
			Name: "fwd-app", Type: "infra.container_service",
			Config: map[string]any{"region": "atl"},
		}},
	}}
	d := p.ValidatePlan(plan)
	if len(d) != 2 {
		t.Fatalf("expected 2 diagnostics for unknown slugs; got %d: %+v", len(d), d)
	}
	for _, x := range d {
		if x.Severity != interfaces.PlanDiagnosticWarning {
			t.Errorf("expected Severity=Warning for unknown slug; got %v in %+v", x.Severity, x)
		}
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
