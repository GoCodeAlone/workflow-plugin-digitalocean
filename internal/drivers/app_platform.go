// Package drivers contains ResourceDriver implementations for each DigitalOcean resource type.
package drivers

import (
	"context"
	"fmt"
	"strings"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// AppPlatformClient is the godo App interface used by AppPlatformDriver (for mocking).
type AppPlatformClient interface {
	Create(ctx context.Context, req *godo.AppCreateRequest) (*godo.App, *godo.Response, error)
	Get(ctx context.Context, appID string) (*godo.App, *godo.Response, error)
	Update(ctx context.Context, appID string, req *godo.AppUpdateRequest) (*godo.App, *godo.Response, error)
	Delete(ctx context.Context, appID string) (*godo.Response, error)
}

// AppPlatformDriver manages DigitalOcean App Platform (infra.container_service).
type AppPlatformDriver struct {
	client AppPlatformClient
	region string
}

// NewAppPlatformDriver creates an AppPlatformDriver backed by a real godo client.
func NewAppPlatformDriver(c *godo.Client, region string) *AppPlatformDriver {
	return &AppPlatformDriver{client: c.Apps, region: region}
}

// NewAppPlatformDriverWithClient creates a driver with an injected client (for tests).
func NewAppPlatformDriverWithClient(c AppPlatformClient, region string) *AppPlatformDriver {
	return &AppPlatformDriver{client: c, region: region}
}

func (d *AppPlatformDriver) Create(ctx context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	image, _ := spec.Config["image"].(string)
	region, _ := spec.Config["region"].(string)
	if region == "" {
		region = d.region
	}
	httpPort, _ := intFromConfig(spec.Config, "http_port", 8080)
	instanceCount, _ := intFromConfig(spec.Config, "instance_count", 1)

	req := &godo.AppCreateRequest{
		Spec: &godo.AppSpec{
			Name:   spec.Name,
			Region: region,
			Services: []*godo.AppServiceSpec{
				{
					Name:          spec.Name,
					InstanceCount: int64(instanceCount),
					HTTPPort:      int64(httpPort),
					Envs:          envVarsFromConfig(spec.Config),
					Image: &godo.ImageSourceSpec{
						RegistryType: godo.ImageSourceSpecRegistryType_DOCR,
						Repository:   imageRepo(image),
						Tag:          imageTag(image),
					},
				},
			},
		},
	}

	app, _, err := d.client.Create(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("app platform create %q: %w", spec.Name, err)
	}
	return appOutput(app), nil
}

func (d *AppPlatformDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	app, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("app platform read %q: %w", ref.Name, err)
	}
	return appOutput(app), nil
}

func (d *AppPlatformDriver) Update(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	image, _ := spec.Config["image"].(string)
	region, _ := spec.Config["region"].(string)
	if region == "" {
		region = d.region
	}
	httpPort, _ := intFromConfig(spec.Config, "http_port", 8080)
	instanceCount, _ := intFromConfig(spec.Config, "instance_count", 1)

	req := &godo.AppUpdateRequest{
		Spec: &godo.AppSpec{
			Name:   spec.Name,
			Region: region,
			Services: []*godo.AppServiceSpec{
				{
					Name:          spec.Name,
					InstanceCount: int64(instanceCount),
					HTTPPort:      int64(httpPort),
					Envs:          envVarsFromConfig(spec.Config),
					Image: &godo.ImageSourceSpec{
						RegistryType: godo.ImageSourceSpecRegistryType_DOCR,
						Repository:   imageRepo(image),
						Tag:          imageTag(image),
					},
				},
			},
		},
	}

	app, _, err := d.client.Update(ctx, ref.ProviderID, req)
	if err != nil {
		return nil, fmt.Errorf("app platform update %q: %w", ref.Name, err)
	}
	return appOutput(app), nil
}

func (d *AppPlatformDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	_, err := d.client.Delete(ctx, ref.ProviderID)
	if err != nil {
		return fmt.Errorf("app platform delete %q: %w", ref.Name, err)
	}
	return nil
}

func (d *AppPlatformDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	if current == nil {
		return &interfaces.DiffResult{NeedsUpdate: true}, nil
	}
	var changes []interfaces.FieldChange
	if img, _ := desired.Config["image"].(string); img != "" {
		curImg, _ := current.Outputs["image"].(string)
		if img != curImg {
			changes = append(changes, interfaces.FieldChange{Path: "image", Old: curImg, New: img})
		}
	}
	return &interfaces.DiffResult{NeedsUpdate: len(changes) > 0, Changes: changes}, nil
}

func (d *AppPlatformDriver) HealthCheck(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	app, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	healthy := app.ActiveDeployment != nil && app.ActiveDeployment.Phase == godo.DeploymentPhase_Active
	msg := ""
	if !healthy {
		msg = fmt.Sprintf("phase: %v", app.ActiveDeployment)
	}
	return &interfaces.HealthResult{Healthy: healthy, Message: msg}, nil
}

func (d *AppPlatformDriver) Scale(ctx context.Context, ref interfaces.ResourceRef, replicas int) (*interfaces.ResourceOutput, error) {
	app, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("app platform scale read %q: %w", ref.Name, err)
	}
	spec := app.Spec
	for _, svc := range spec.Services {
		svc.InstanceCount = int64(replicas)
	}
	updated, _, err := d.client.Update(ctx, ref.ProviderID, &godo.AppUpdateRequest{Spec: spec})
	if err != nil {
		return nil, fmt.Errorf("app platform scale update %q: %w", ref.Name, err)
	}
	return appOutput(updated), nil
}

func appOutput(app *godo.App) *interfaces.ResourceOutput {
	out := &interfaces.ResourceOutput{
		Name:       app.Spec.Name,
		Type:       "infra.container_service",
		ProviderID: app.ID,
		Outputs: map[string]any{
			"live_url": app.LiveURL,
		},
		Status: "running",
	}
	if app.ActiveDeployment == nil {
		out.Status = "pending"
	}
	return out
}

func imageRepo(image string) string {
	parts := strings.SplitN(image, ":", 2)
	return parts[0]
}

func imageTag(image string) string {
	parts := strings.SplitN(image, ":", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return "latest"
}

// envVarsFromConfig converts the "env_vars" map in spec config to App Platform
// environment variable definitions. Values listed under "secret_env_vars" are
// marked as SECRET so DigitalOcean stores them encrypted.
func envVarsFromConfig(cfg map[string]any) []*godo.AppVariableDefinition {
	raw, _ := cfg["env_vars"].(map[string]any)
	secrets, _ := cfg["secret_env_vars"].(map[string]any)
	if len(raw) == 0 && len(secrets) == 0 {
		return nil
	}
	envs := make([]*godo.AppVariableDefinition, 0, len(raw)+len(secrets))
	for k, v := range raw {
		envs = append(envs, &godo.AppVariableDefinition{
			Key:   k,
			Value: fmt.Sprintf("%v", v),
			Type:  godo.AppVariableType_General,
			Scope: godo.AppVariableScope_RunAndBuildTime,
		})
	}
	for k, v := range secrets {
		envs = append(envs, &godo.AppVariableDefinition{
			Key:   k,
			Value: fmt.Sprintf("%v", v),
			Type:  godo.AppVariableType_Secret,
			Scope: godo.AppVariableScope_RunAndBuildTime,
		})
	}
	return envs
}
