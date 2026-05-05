package internal

import (
	"fmt"
	"strings"

	"github.com/GoCodeAlone/workflow/interfaces"
)

// ValidatePlan implements interfaces.ProviderValidator (W-4): a read-only,
// no-remote-call cross-resource constraint check that runs at `wfctl infra
// align` time before any cloud API call. Diagnostics surface as
// PlanDiagnosticError|Warning|Info with severity-driven exit-code mapping
// (Error always fails align; Warning fails only under --strict; Info never
// affects exit).
//
// PR P-DO TP3 — first DO region-constraint pass:
//
//  1. App Platform `infra.container_service` MUST use one of DO App
//     Platform's supported region GROUPS (nyc, ams, fra, sfo, sgp, syd,
//     tor, blr, lon). If `region` is set to a Droplet/VPC zone slug
//     (nyc1, nyc3, sfo2, sfo3 …) it is rejected — App Platform configures
//     by group, not zone.
//
//  2. Droplet/VPC/Volume zone-bound resources MUST use a zone slug
//     (nyc1, nyc2, nyc3, sfo2, sfo3, ams2, ams3, fra1, sgp1, lon1, tor1,
//     blr1, syd1) — bare group slugs (nyc, sfo) are rejected for these
//     types because the DO API for these resources requires a specific
//     zone.
//
//  3. App Platform with `vpc_ref` (referencing a VPC by name in the same
//     plan) — the referenced VPC's region zone MUST belong to the App
//     Platform's region group (e.g., AppPlatform region nyc → VPC in
//     nyc1 OR nyc3; AppPlatform region sfo → VPC in sfo2 OR sfo3).
//     Mismatch is the recurring "App Platform in nyc cannot reach VPC in
//     sfo3" production bug (root-cause issue D from the conformance
//     design).
//
// Future extensions (deferred follow-ups): database/cache zone slugs,
// load balancer zone matching against attached droplets, registry
// regional restrictions.
func (p *DOProvider) ValidatePlan(plan *interfaces.IaCPlan) []interfaces.PlanDiagnostic {
	if plan == nil || len(plan.Actions) == 0 {
		return nil
	}

	// Index plan resources by Name for cross-resource lookup. Both desired
	// (from action.Resource) and existing (from action.Current) entries
	// land here so an App Platform that references an existing VPC by
	// name (no plan action of its own) still resolves.
	byName := make(map[string]planResource, len(plan.Actions))
	for _, a := range plan.Actions {
		byName[a.Resource.Name] = planResource{spec: a.Resource, current: a.Current}
	}

	var diags []interfaces.PlanDiagnostic
	for _, a := range plan.Actions {
		// Skip delete actions — they target the existing resource, not
		// the desired spec, so region constraints on the (empty) spec
		// would produce false positives.
		if a.Action == "delete" {
			continue
		}
		region, _ := a.Resource.Config["region"].(string)
		switch a.Resource.Type {
		case "infra.container_service":
			diags = appendAppPlatformDiagnostics(diags, a.Resource, region, byName)
		case "infra.vpc", "infra.droplet", "infra.volume":
			if region != "" && !isZoneSlug(region) {
				diags = append(diags, interfaces.PlanDiagnostic{
					Severity: interfaces.PlanDiagnosticError,
					Resource: a.Resource.Name,
					Field:    "region",
					Message: fmt.Sprintf(
						"DO %s requires a zone slug (e.g. nyc1, sfo3); got %q which is %s. Use a specific zone.",
						a.Resource.Type, region, classifyRegion(region),
					),
				})
			}
		case "infra.database":
			diags = appendDatabaseDiagnostics(diags, a.Resource, byName)
		}
	}
	return diags
}

// appendDatabaseDiagnostics emits cross-resource diagnostics for an
// infra.database action. Currently:
//
//   - vpc_ref MUST resolve to either an in-plan VPC or a name the
//     operator knows is an existing VPC (Warning when missing). A
//     dangling vpc_ref is the conformance scenario's regression-pin
//     case (Scenario_CrossResourceConstraintRejection).
func appendDatabaseDiagnostics(
	diags []interfaces.PlanDiagnostic,
	spec interfaces.ResourceSpec,
	byName map[string]planResource,
) []interfaces.PlanDiagnostic {
	vpcRef, _ := spec.Config["vpc_ref"].(string)
	if vpcRef == "" {
		return diags
	}
	if _, ok := byName[vpcRef]; ok {
		return diags
	}
	// vpc_ref names a resource not in the plan. Surface as Error so the
	// conformance scenario's "at least one Severity=Error" assertion
	// matches; this is the dangling-cross-resource-reference case the
	// W-4 design explicitly calls out as a plan-time validator gap.
	diags = append(diags, interfaces.PlanDiagnostic{
		Severity: interfaces.PlanDiagnosticError,
		Resource: spec.Name,
		Field:    "vpc_ref",
		Message: fmt.Sprintf(
			"DO database %q references vpc_ref %q which is not declared in the same plan; either add the VPC resource or remove the vpc_ref",
			spec.Name, vpcRef,
		),
	})
	return diags
}

// planResource is the cross-resource lookup record used by ValidatePlan.
// Carries both the desired spec and the optional current state so a plan
// where an App Platform references an existing-and-unchanged VPC still
// resolves the VPC's region.
type planResource struct {
	spec    interfaces.ResourceSpec
	current *interfaces.ResourceState
}

// appendAppPlatformDiagnostics emits region-constraint diagnostics for an
// infra.container_service action's region/vpc_ref pair.
func appendAppPlatformDiagnostics(
	diags []interfaces.PlanDiagnostic,
	spec interfaces.ResourceSpec,
	region string,
	byName map[string]planResource,
) []interfaces.PlanDiagnostic {
	if region != "" && !isAppPlatformRegionGroup(region) {
		diags = append(diags, interfaces.PlanDiagnostic{
			Severity: interfaces.PlanDiagnosticError,
			Resource: spec.Name,
			Field:    "region",
			Message: fmt.Sprintf(
				"DO App Platform configures by region GROUP (nyc, ams, fra, sfo, sgp, syd, tor, blr, lon); got %q which is %s. Replace with the parent group slug.",
				region, classifyRegion(region),
			),
		})
	}

	vpcRef, _ := spec.Config["vpc_ref"].(string)
	if vpcRef == "" || region == "" || !isAppPlatformRegionGroup(region) {
		return diags
	}
	target, ok := byName[vpcRef]
	if !ok {
		// vpc_ref points to a name not in the plan; cannot validate
		// region match without state. Emit a Warning so an operator
		// configuring a fresh deployment sees the gap; non-strict
		// runs continue.
		diags = append(diags, interfaces.PlanDiagnostic{
			Severity: interfaces.PlanDiagnosticWarning,
			Resource: spec.Name,
			Field:    "vpc_ref",
			Message: fmt.Sprintf(
				"App Platform %q references vpc_ref %q which is not declared in the same plan; region-match check skipped",
				spec.Name, vpcRef,
			),
		})
		return diags
	}
	// Resolve the referenced VPC's region. Prefer the desired spec
	// (config the user is actively declaring) and fall back to the
	// current state's outputs (existing VPC adoption).
	vpcRegion, _ := target.spec.Config["region"].(string)
	if vpcRegion == "" && target.current != nil {
		if v, ok := target.current.Outputs["region"].(string); ok {
			vpcRegion = v
		}
	}
	if vpcRegion == "" {
		// Cannot determine VPC region — silent, not a finding (planning
		// pipeline upstream may not have populated yet).
		return diags
	}
	if appPlatformRegionGroupOf(vpcRegion) != region {
		diags = append(diags, interfaces.PlanDiagnostic{
			Severity: interfaces.PlanDiagnosticError,
			Resource: spec.Name,
			Field:    "vpc_ref",
			Message: fmt.Sprintf(
				"App Platform region %q is incompatible with VPC %q region %q. App Platform region group %q only routes to VPCs in zones %s.",
				region, vpcRef, vpcRegion, region, strings.Join(zonesInGroup(region), ", "),
			),
		})
	}
	return diags
}

// isAppPlatformRegionGroup reports whether s is a DO App Platform region
// group slug (the parent of one or more zone slugs).
func isAppPlatformRegionGroup(s string) bool {
	_, ok := appPlatformGroupZones[s]
	return ok
}

// isZoneSlug reports whether s is a DO zone slug (e.g., nyc1, sfo3).
// Zone slugs are the value DO's API expects for VPC, Droplet, Volume,
// Database, Spaces.
func isZoneSlug(s string) bool {
	_, ok := zoneToGroup[s]
	return ok
}

// appPlatformRegionGroupOf returns the App Platform region group that
// contains a zone slug. Returns "" for unknown zones.
func appPlatformRegionGroupOf(zone string) string {
	return zoneToGroup[zone]
}

// zonesInGroup returns the sorted list of zone slugs inside a group, for
// use in diagnostic messages.
func zonesInGroup(group string) []string {
	return appPlatformGroupZones[group]
}

// classifyRegion returns a short human-readable label for a region slug
// to help diagnostic messages distinguish bare-group from zone-slug
// confusion.
func classifyRegion(s string) string {
	if isAppPlatformRegionGroup(s) {
		return fmt.Sprintf("a region GROUP (zones: %s)", strings.Join(zonesInGroup(s), ", "))
	}
	if isZoneSlug(s) {
		return fmt.Sprintf("a zone slug in group %q", appPlatformRegionGroupOf(s))
	}
	return "not a recognized DO region slug"
}

// appPlatformGroupZones maps each App Platform region group to the
// zone slugs it routes to. Source: DO docs (Apps API Region object) +
// Apps API region listing as of 2026-05.
var appPlatformGroupZones = map[string][]string{
	"nyc": {"nyc1", "nyc3"},
	"ams": {"ams3"},
	"fra": {"fra1"},
	"sfo": {"sfo2", "sfo3"},
	"sgp": {"sgp1"},
	"syd": {"syd1"},
	"tor": {"tor1"},
	"blr": {"blr1"},
	"lon": {"lon1"},
}

// zoneToGroup is the inverse mapping computed at init() so lookups are
// O(1). Kept distinct from appPlatformGroupZones so the source-of-truth
// (group → zones) stays single-direction.
var zoneToGroup = func() map[string]string {
	m := make(map[string]string, 16)
	for group, zones := range appPlatformGroupZones {
		for _, z := range zones {
			m[z] = group
		}
	}
	// Additional zones DO supports that don't currently route to App
	// Platform (legacy nyc2, ams2). Including them lets the zone-slug
	// validation accept these without flagging valid VPC/Droplet/Volume
	// configs — they simply have no App Platform routing.
	m["nyc2"] = ""
	m["ams2"] = ""
	return m
}()

// Compile-time assertion: DOProvider implements ProviderValidator.
var _ interfaces.ProviderValidator = (*DOProvider)(nil)
