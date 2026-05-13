// Package steps contains pipeline step implementations for the workflow-plugin-digitalocean
// external plugin. Each step provides a named pipeline step type (e.g. step.iac_scale)
// that operators reference in their workflow YAML configs.
package steps

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"

	"github.com/digitalocean/godo"

	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
)

// AppsScaleClient is the subset of the godo Apps service used by IaCScaleStep.
// Injecting this interface allows tests to provide a fake without starting a
// real godo client.
type AppsScaleClient interface {
	Get(ctx context.Context, appID string) (*godo.App, *godo.Response, error)
	Update(ctx context.Context, appID string, req *godo.AppUpdateRequest) (*godo.App, *godo.Response, error)
}

// iacScaleStep implements step.iac_scale — scales a single App Platform service
// component to a target instance_count without a full step.iac_apply rerun.
//
// Config schema (all values may come from pipeline current or config):
//
//	app_id         string (required) — DO App Platform app UUID
//	component_name string (required) — name of the service component to scale
//	instance_count int    (required) — target instance count (>= 0)
//
// Outputs:
//
//	previous_count  int64  — count before this scale operation
//	new_count       int64  — count after this scale operation
//	app_id          string — app UUID
//	deployment_id   string — InProgressDeployment.ID returned by godo after
//	                         the Apps.Update call; empty on no-op (count unchanged)
type iacScaleStep struct {
	name   string
	client AppsScaleClient
}

// NewIaCScaleStep creates a step.iac_scale instance backed by the provided
// AppsScaleClient. The production path injects the real godo Apps client; tests
// inject a fake.
func NewIaCScaleStep(name string, client AppsScaleClient) sdk.StepInstance {
	return &iacScaleStep{name: name, client: client}
}

// Execute implements sdk.StepInstance.
//
// Inputs are merged from current (step outputs of prior steps) and config (the
// step's static YAML config), with config taking precedence for required fields.
func (s *iacScaleStep) Execute(
	ctx context.Context,
	_ map[string]any,
	_ map[string]map[string]any,
	current map[string]any,
	_ map[string]any,
	config map[string]any,
) (*sdk.StepResult, error) {
	if s.client == nil {
		return nil, fmt.Errorf("step %s: DigitalOcean client not initialized (plugin not yet configured via iac.provider)", s.name)
	}
	merged := mergeInputs(current, config)

	appID, _ := merged["app_id"].(string)
	if appID == "" {
		return nil, fmt.Errorf("step %s: app_id is required", s.name)
	}
	componentName, _ := merged["component_name"].(string)
	if componentName == "" {
		return nil, fmt.Errorf("step %s: component_name is required", s.name)
	}

	// Accept both float64 (JSON/YAML default numeric type) and int variants.
	targetCount, err := intFromAny(merged["instance_count"])
	if err != nil {
		return nil, fmt.Errorf("step %s: instance_count: %w", s.name, err)
	}
	if targetCount < 0 {
		return nil, fmt.Errorf("step %s: instance_count must be >= 0, got %d", s.name, targetCount)
	}

	// Fetch the full current AppSpec — we must round-trip the complete spec to
	// avoid clobbering unrelated services, jobs, workers, domains, etc.
	app, _, err := s.client.Get(ctx, appID)
	if err != nil {
		return nil, fmt.Errorf("step %s: get app %q: %w", s.name, appID, wrapGodoErr(err))
	}
	if app.Spec == nil {
		return nil, fmt.Errorf("step %s: app %q has no spec", s.name, appID)
	}

	// Locate the named service component.
	previousCount, found := findServiceCount(app.Spec, componentName)
	if !found {
		return nil, fmt.Errorf("step %s: component %q not found in app %q (available services: %s)",
			s.name, componentName, appID, serviceNames(app.Spec))
	}

	// No-op path: count is already at target — skip the Update call entirely.
	if previousCount == targetCount {
		return &sdk.StepResult{Output: map[string]any{
			"previous_count": previousCount,
			"new_count":      previousCount,
			"app_id":         appID,
			"deployment_id":  "",
		}}, nil
	}

	// Mutate a copy of the target component only; leave all other services
	// unchanged and avoid mutating the pointer returned by the Get response.
	setServiceCount(app.Spec, componentName, targetCount)

	// Apps.Update with the full spec triggers a new deployment.
	// A 409 means another deployment is in progress — surface it with a
	// clear message so operators know to wait or check for concurrent apply.
	updated, _, err := s.client.Update(ctx, appID, &godo.AppUpdateRequest{Spec: app.Spec})
	if err != nil {
		return nil, fmt.Errorf("step %s: scale app %q component %q to %d: %w",
			s.name, appID, componentName, targetCount, wrapGodoErr(err))
	}

	deploymentID := ""
	if updated != nil && updated.InProgressDeployment != nil {
		deploymentID = updated.InProgressDeployment.ID
	}

	return &sdk.StepResult{Output: map[string]any{
		"previous_count": previousCount,
		"new_count":      targetCount,
		"app_id":         appID,
		"deployment_id":  deploymentID,
	}}, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// mergeInputs returns a merged map where config values take precedence over
// current (step output) values. Both may be nil.
func mergeInputs(current, config map[string]any) map[string]any {
	merged := make(map[string]any, len(current)+len(config))
	for k, v := range current {
		merged[k] = v
	}
	for k, v := range config {
		merged[k] = v
	}
	return merged
}

// intFromAny converts float64 (JSON/YAML default), int, int64 to int64.
// Returns an error when v is nil, a non-numeric type, or a fractional float.
// instance_count values are always small (0–N), so overflow is not a concern
// in practice; the range check is present for defense.
func intFromAny(v any) (int64, error) {
	switch t := v.(type) {
	case float64:
		if math.Trunc(t) != t {
			return 0, fmt.Errorf("expected integer value, got fractional float %v", t)
		}
		if t < 0 || t > math.MaxInt32 {
			return 0, fmt.Errorf("value %v out of valid range [0, %d]", t, math.MaxInt32)
		}
		return int64(t), nil
	case float32:
		tf := float64(t)
		if math.Trunc(tf) != tf {
			return 0, fmt.Errorf("expected integer value, got fractional float %v", t)
		}
		if tf < 0 || tf > math.MaxInt32 {
			return 0, fmt.Errorf("value %v out of valid range [0, %d]", t, math.MaxInt32)
		}
		return int64(t), nil
	case int:
		return int64(t), nil
	case int32:
		return int64(t), nil
	case int64:
		return t, nil
	case nil:
		return 0, fmt.Errorf("value is nil (not provided)")
	default:
		return 0, fmt.Errorf("expected numeric type, got %T", v)
	}
}

// findServiceCount returns the current InstanceCount for the named service
// component. The second return value is false when no service with that name
// exists in the spec.
func findServiceCount(spec *godo.AppSpec, componentName string) (int64, bool) {
	for _, svc := range spec.Services {
		if svc.Name == componentName {
			return svc.InstanceCount, true
		}
	}
	return 0, false
}

// setServiceCount replaces the named service entry in spec.Services with a
// shallow copy whose InstanceCount is set to count. Using a copy avoids
// mutating the pointer owned by the godo Get response in-place, which
// prevents aliasing surprises if the caller reuses the original app object.
func setServiceCount(spec *godo.AppSpec, componentName string, count int64) {
	for i, svc := range spec.Services {
		if svc.Name == componentName {
			// Shallow-copy the spec to avoid mutating the Get-response pointer.
			copied := *svc
			copied.InstanceCount = count
			spec.Services[i] = &copied
			return
		}
	}
}

// serviceNames returns a comma-separated list of service names in the spec for
// error messages.
func serviceNames(spec *godo.AppSpec) string {
	if spec == nil || len(spec.Services) == 0 {
		return "<none>"
	}
	names := make([]string, 0, len(spec.Services))
	for _, svc := range spec.Services {
		names = append(names, svc.Name)
	}
	result := ""
	for i, n := range names {
		if i > 0 {
			result += ", "
		}
		result += n
	}
	return result
}

// wrapGodoErr converts a godo error to a human-readable error with a hint when
// the status is 409 Conflict (concurrent deployment in progress). Uses
// errors.As for robust classification so wrapped errors are handled correctly.
func wrapGodoErr(err error) error {
	if err == nil {
		return nil
	}
	var resp *godo.ErrorResponse
	if errors.As(err, &resp) {
		if resp.Response != nil && resp.Response.StatusCode == http.StatusConflict {
			return fmt.Errorf("concurrent update conflict (another deployment is in progress): %w", err)
		}
	}
	return err
}
