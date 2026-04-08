package drivers

import (
	"context"
	"fmt"

	"github.com/GoCodeAlone/workflow/interfaces"
)

// IAMRoleDriver provides a ResourceDriver for infra.iam_role on DigitalOcean.
//
// # Limitation
//
// DigitalOcean does not expose fine-grained IAM role management via the godo API.
// Personal Access Tokens and OAuth applications must be created through the DO
// control panel (https://cloud.digitalocean.com/account/api/tokens).
//
// This driver is entirely declarative: Create/Update return the declared config as
// outputs with a limitation notice; Read/HealthCheck return a healthy declared state;
// Delete and Scale are no-ops. No godo API calls are made.
type IAMRoleDriver struct{}

// NewIAMRoleDriver creates an IAMRoleDriver.
func NewIAMRoleDriver() *IAMRoleDriver {
	return &IAMRoleDriver{}
}

// Create records an IAM role declaration. Since DO has no native role API the
// driver returns the declared metadata as outputs with a clear limitation notice.
func (d *IAMRoleDriver) Create(_ context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	return iamOutput(spec.Name, spec.Config), nil
}

func (d *IAMRoleDriver) Read(_ context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	return &interfaces.ResourceOutput{
		Name:       ref.Name,
		Type:       "infra.iam_role",
		ProviderID: ref.ProviderID,
		Outputs: map[string]any{
			"limitation": "DigitalOcean does not provide a programmatic IAM role API via godo. Manage tokens via the DO control panel.",
		},
		Status: "declared",
	}, nil
}

func (d *IAMRoleDriver) Update(_ context.Context, _ interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	return iamOutput(spec.Name, spec.Config), nil
}

func (d *IAMRoleDriver) Delete(_ context.Context, _ interfaces.ResourceRef) error {
	// No API resource to delete.
	return nil
}

func (d *IAMRoleDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	if current == nil {
		return &interfaces.DiffResult{NeedsUpdate: true}, nil
	}
	return &interfaces.DiffResult{NeedsUpdate: false}, nil
}

func (d *IAMRoleDriver) HealthCheck(_ context.Context, ref interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	return &interfaces.HealthResult{
		Healthy: true,
		Message: fmt.Sprintf("IAM role %q is declared (no live DO resource backing)", ref.Name),
	}, nil
}

func (d *IAMRoleDriver) Scale(_ context.Context, _ interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return nil, fmt.Errorf("iam_role does not support scale operation")
}

func iamOutput(name string, config map[string]any) *interfaces.ResourceOutput {
	outputs := map[string]any{
		"limitation": "DigitalOcean does not provide a programmatic IAM role API via godo. Manage tokens via the DO control panel.",
	}
	for k, v := range config {
		outputs[k] = v
	}
	return &interfaces.ResourceOutput{
		Name:       name,
		Type:       "infra.iam_role",
		ProviderID: name,
		Outputs:    outputs,
		Status:     "declared",
	}
}

func (d *IAMRoleDriver) SensitiveKeys() []string { return nil }
