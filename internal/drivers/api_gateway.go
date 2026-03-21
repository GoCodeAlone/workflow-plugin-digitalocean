package drivers

import (
	"context"
	"fmt"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// APIGatewayAppClient is the godo App interface used for API gateway management (for mocking).
type APIGatewayAppClient interface {
	Create(ctx context.Context, req *godo.AppCreateRequest) (*godo.App, *godo.Response, error)
	Get(ctx context.Context, appID string) (*godo.App, *godo.Response, error)
	Update(ctx context.Context, appID string, req *godo.AppUpdateRequest) (*godo.App, *godo.Response, error)
	Delete(ctx context.Context, appID string) (*godo.Response, error)
}

// APIGatewayDriver manages API routing via DigitalOcean App Platform (infra.api_gateway).
// It creates an App Platform application with HTTP route rules mapping paths to backend services.
//
// Config keys:
//
//	region  string            — DO region slug (default: driver region)
//	routes  []map[string]any  — each: {path, component, preserve_path_prefix}
//	                            component maps to an App Platform service component name
type APIGatewayDriver struct {
	client APIGatewayAppClient
	region string
}

// NewAPIGatewayDriver creates an APIGatewayDriver backed by a real godo client.
func NewAPIGatewayDriver(c *godo.Client, region string) *APIGatewayDriver {
	return &APIGatewayDriver{client: c.Apps, region: region}
}

// NewAPIGatewayDriverWithClient creates a driver with an injected client (for tests).
func NewAPIGatewayDriverWithClient(c APIGatewayAppClient, region string) *APIGatewayDriver {
	return &APIGatewayDriver{client: c, region: region}
}

func (d *APIGatewayDriver) Create(ctx context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	region := strFromConfig(spec.Config, "region", d.region)
	appSpec := buildAPIGatewaySpec(spec.Name, region, spec.Config)

	app, _, err := d.client.Create(ctx, &godo.AppCreateRequest{Spec: appSpec})
	if err != nil {
		return nil, fmt.Errorf("api_gateway create %q: %w", spec.Name, err)
	}
	return apiGatewayOutput(app), nil
}

func (d *APIGatewayDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	app, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("api_gateway read %q: %w", ref.Name, err)
	}
	return apiGatewayOutput(app), nil
}

func (d *APIGatewayDriver) Update(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	region := strFromConfig(spec.Config, "region", d.region)
	appSpec := buildAPIGatewaySpec(spec.Name, region, spec.Config)

	app, _, err := d.client.Update(ctx, ref.ProviderID, &godo.AppUpdateRequest{Spec: appSpec})
	if err != nil {
		return nil, fmt.Errorf("api_gateway update %q: %w", ref.Name, err)
	}
	return apiGatewayOutput(app), nil
}

func (d *APIGatewayDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	_, err := d.client.Delete(ctx, ref.ProviderID)
	if err != nil {
		return fmt.Errorf("api_gateway delete %q: %w", ref.Name, err)
	}
	return nil
}

func (d *APIGatewayDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	if current == nil {
		return &interfaces.DiffResult{NeedsUpdate: true}, nil
	}
	return &interfaces.DiffResult{NeedsUpdate: false}, nil
}

func (d *APIGatewayDriver) HealthCheck(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	app, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	healthy := app.ActiveDeployment != nil && app.ActiveDeployment.Phase == godo.DeploymentPhase_Active
	return &interfaces.HealthResult{Healthy: healthy}, nil
}

func (d *APIGatewayDriver) Scale(_ context.Context, _ interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return nil, fmt.Errorf("api_gateway does not support scale operation")
}

// buildAPIGatewaySpec builds an AppSpec with HTTP route rules from config.
func buildAPIGatewaySpec(name, region string, config map[string]any) *godo.AppSpec {
	spec := &godo.AppSpec{
		Name:   name,
		Region: region,
	}

	routes, _ := config["routes"].([]any)
	for _, r := range routes {
		m, _ := r.(map[string]any)
		if m == nil {
			continue
		}
		path := strFromConfig(m, "path", "/")
		component := strFromConfig(m, "component", "")
		preservePrefix := false
		if v, ok := m["preserve_path_prefix"].(bool); ok {
			preservePrefix = v
		}

		spec.Services = append(spec.Services, &godo.AppServiceSpec{
			Name: component,
			Routes: []*godo.AppRouteSpec{
				{
					Path:               path,
					PreservePathPrefix: preservePrefix,
				},
			},
		})
	}

	return spec
}

func apiGatewayOutput(app *godo.App) *interfaces.ResourceOutput {
	status := "pending"
	if app.ActiveDeployment != nil && app.ActiveDeployment.Phase == godo.DeploymentPhase_Active {
		status = "running"
	}
	return &interfaces.ResourceOutput{
		Name:       app.Spec.Name,
		Type:       "infra.api_gateway",
		ProviderID: app.ID,
		Outputs: map[string]any{
			"live_url": app.LiveURL,
		},
		Status: status,
	}
}
