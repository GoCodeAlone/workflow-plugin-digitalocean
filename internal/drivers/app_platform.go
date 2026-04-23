// Package drivers contains ResourceDriver implementations for each DigitalOcean resource type.
package drivers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// ErrResourceNotFound is returned when a resource cannot be located by name or ID.
// It is intended to be used with errors.Is for sentinel matching.
var ErrResourceNotFound = errors.New("resource not found")

// AppPlatformClient is the godo App interface used by AppPlatformDriver (for mocking).
type AppPlatformClient interface {
	Create(ctx context.Context, req *godo.AppCreateRequest) (*godo.App, *godo.Response, error)
	Get(ctx context.Context, appID string) (*godo.App, *godo.Response, error)
	List(ctx context.Context, opts *godo.ListOptions) ([]*godo.App, *godo.Response, error)
	Update(ctx context.Context, appID string, req *godo.AppUpdateRequest) (*godo.App, *godo.Response, error)
	CreateDeployment(ctx context.Context, appID string, req ...*godo.DeploymentCreateRequest) (*godo.Deployment, *godo.Response, error)
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
	region, _ := spec.Config["region"].(string)
	if region == "" {
		region = d.region
	}
	appSpec, err := buildAppSpec(spec.Name, spec.Config, region)
	if err != nil {
		return nil, fmt.Errorf("app platform build spec: %w", err)
	}

	app, _, err := d.client.Create(ctx, &godo.AppCreateRequest{Spec: appSpec})
	if err != nil {
		return nil, fmt.Errorf("app platform create %q: %w", spec.Name, WrapGodoError(err))
	}
	return appOutput(app), nil
}

func (d *AppPlatformDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	if ref.ProviderID == "" {
		return d.findAppByName(ctx, ref.Name)
	}
	app, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("app platform read %q: %w", ref.Name, WrapGodoError(err))
	}
	return appOutput(app), nil
}

// findAppByName iterates the paginated app list and returns the first app whose
// Spec.Name matches name. Returns ErrResourceNotFound if no match is found.
func (d *AppPlatformDriver) findAppByName(ctx context.Context, name string) (*interfaces.ResourceOutput, error) {
	opts := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		apps, resp, err := d.client.List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("app platform list: %w", WrapGodoError(err))
		}
		for _, app := range apps {
			if app.Spec != nil && app.Spec.Name == name {
				return appOutput(app), nil
			}
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opts.Page++
	}
	return nil, fmt.Errorf("app %q: %w", name, ErrResourceNotFound)
}

func (d *AppPlatformDriver) Update(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	region, _ := spec.Config["region"].(string)
	if region == "" {
		region = d.region
	}
	appSpec, err := buildAppSpec(spec.Name, spec.Config, region)
	if err != nil {
		return nil, fmt.Errorf("app platform build spec: %w", err)
	}

	app, _, err := d.client.Update(ctx, ref.ProviderID, &godo.AppUpdateRequest{Spec: appSpec})
	if err != nil {
		return nil, fmt.Errorf("app platform update %q: %w", ref.Name, WrapGodoError(err))
	}
	// Trigger a new deployment — Update only changes the spec; DO does not auto-deploy.
	dep, _, err := d.client.CreateDeployment(ctx, ref.ProviderID, &godo.DeploymentCreateRequest{ForceBuild: true})
	if err != nil {
		return nil, fmt.Errorf("app platform create deployment %q: %w", ref.Name, WrapGodoError(err))
	}
	fmt.Printf("  app platform deploy %q: triggered deployment %s\n", spec.Name, dep.ID)
	return appOutput(app), nil
}

func (d *AppPlatformDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	_, err := d.client.Delete(ctx, ref.ProviderID)
	if err != nil {
		return fmt.Errorf("app platform delete %q: %w", ref.Name, WrapGodoError(err))
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
	return appHealthResult(app), nil
}

// appHealthResult evaluates all three DO deployment slots in priority order and
// returns an accurate HealthResult:
//
//   - ActiveDeployment ACTIVE         → Healthy=true
//   - InProgressDeployment (building) → Healthy=false, "deployment in progress: <phase>"
//   - InProgressDeployment (failed)   → Healthy=false, "deployment failed: <phase>"
//   - PendingDeployment               → Healthy=false, "deployment queued"
//   - none of the above               → Healthy=false, "no deployment found"
func appHealthResult(app *godo.App) *interfaces.HealthResult {
	// 1. Active and healthy.
	if app.ActiveDeployment != nil && app.ActiveDeployment.Phase == godo.DeploymentPhase_Active {
		return &interfaces.HealthResult{Healthy: true}
	}

	// 2. Deployment currently in progress — inspect its phase.
	if dep := app.InProgressDeployment; dep != nil {
		switch dep.Phase {
		case godo.DeploymentPhase_PendingBuild,
			godo.DeploymentPhase_Building,
			godo.DeploymentPhase_PendingDeploy,
			godo.DeploymentPhase_Deploying:
			return &interfaces.HealthResult{
				Healthy: false,
				Message: fmt.Sprintf("deployment in progress: %s", dep.Phase),
			}
		case godo.DeploymentPhase_Error,
			godo.DeploymentPhase_Canceled,
			godo.DeploymentPhase_Superseded:
			return &interfaces.HealthResult{
				Healthy: false,
				Message: fmt.Sprintf("deployment failed: %s", dep.Phase),
			}
		default:
			// Forward-compat: a future godo release may add new phases.
			// Report "unknown" rather than "failed" to avoid mislabeling.
			return &interfaces.HealthResult{
				Healthy: false,
				Message: fmt.Sprintf("unknown phase: %s", dep.Phase),
			}
		}
	}

	// 3. Deployment queued but not yet started.
	if app.PendingDeployment != nil {
		return &interfaces.HealthResult{Healthy: false, Message: "deployment queued"}
	}

	// 4. No deployment at all (first-deploy not yet kicked off, or app never deployed).
	return &interfaces.HealthResult{Healthy: false, Message: "no deployment found"}
}

func (d *AppPlatformDriver) Scale(ctx context.Context, ref interfaces.ResourceRef, replicas int) (*interfaces.ResourceOutput, error) {
	app, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("app platform scale read %q: %w", ref.Name, WrapGodoError(err))
	}
	spec := app.Spec
	for _, svc := range spec.Services {
		svc.InstanceCount = int64(replicas)
	}
	updated, _, err := d.client.Update(ctx, ref.ProviderID, &godo.AppUpdateRequest{Spec: spec})
	if err != nil {
		return nil, fmt.Errorf("app platform scale update %q: %w", ref.Name, WrapGodoError(err))
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

// ParseImageRef parses a flat image reference string into a DO App Platform
// ImageSourceSpec. Supports:
//
//   - registry.digitalocean.com/<registry>/<repo>:<tag>  → DOCR (Registry left empty per DO API requirement)
//   - ghcr.io/<org>/<repo>:<tag>                         → GHCR
//   - docker.io/<org>/<repo>:<tag>                       → DOCKER_HUB
//   - <org>/<repo>:<tag>                                 → DOCKER_HUB (bare form)
//
// A missing tag defaults to "latest".
func ParseImageRef(imageStr string) (*godo.ImageSourceSpec, error) {
	if imageStr == "" {
		return nil, fmt.Errorf("image ref is empty")
	}

	// Separate tag from the path reference.
	ref, tag, _ := strings.Cut(imageStr, ":")
	if tag == "" {
		tag = "latest"
	}

	parts := strings.Split(ref, "/")

	switch {
	case strings.HasPrefix(ref, "registry.digitalocean.com/"):
		// registry.digitalocean.com/<registry>/<repo>
		if len(parts) < 3 {
			return nil, fmt.Errorf("invalid DOCR image ref %q: expected registry.digitalocean.com/<registry>/<repo>", imageStr)
		}
		return &godo.ImageSourceSpec{
			RegistryType: godo.ImageSourceSpecRegistryType_DOCR,
			// Registry must be left empty for DOCR per DO API.
			Repository: parts[len(parts)-1],
			Tag:        tag,
		}, nil

	case strings.HasPrefix(ref, "ghcr.io/"):
		// ghcr.io/<org>/<repo>
		if len(parts) < 3 {
			return nil, fmt.Errorf("invalid GHCR image ref %q: expected ghcr.io/<org>/<repo>", imageStr)
		}
		return &godo.ImageSourceSpec{
			RegistryType: godo.ImageSourceSpecRegistryType_Ghcr,
			Registry:     parts[1],
			Repository:   parts[len(parts)-1],
			Tag:          tag,
		}, nil

	case strings.HasPrefix(ref, "docker.io/"):
		// docker.io/<org>/<repo>
		if len(parts) < 3 {
			return nil, fmt.Errorf("invalid Docker Hub image ref %q: expected docker.io/<org>/<repo>", imageStr)
		}
		return &godo.ImageSourceSpec{
			RegistryType: godo.ImageSourceSpecRegistryType_DockerHub,
			Registry:     parts[1],
			Repository:   parts[len(parts)-1],
			Tag:          tag,
		}, nil

	default:
		// Bare <org>/<repo> format treated as Docker Hub.
		if len(parts) < 2 {
			return nil, fmt.Errorf("unsupported image ref %q: expected <org>/<repo>[:tag] or a registry-prefixed form (registry.digitalocean.com, ghcr.io, docker.io)", imageStr)
		}
		return &godo.ImageSourceSpec{
			RegistryType: godo.ImageSourceSpecRegistryType_DockerHub,
			Registry:     parts[0],
			Repository:   parts[len(parts)-1],
			Tag:          tag,
		}, nil
	}
}

// imageSpecFromConfig extracts an ImageSourceSpec from spec.Config["image"].
// Accepts either a flat string (parsed by ParseImageRef) or an already-nested
// map[string]any with keys registry_type, repository, tag, registry.
func imageSpecFromConfig(cfg map[string]any) (*godo.ImageSourceSpec, error) {
	switch v := cfg["image"].(type) {
	case string:
		if v == "" {
			return nil, fmt.Errorf("image config is empty")
		}
		return ParseImageRef(v)
	case map[string]any:
		return imageSpecFromMap(v)
	default:
		return nil, fmt.Errorf("image config must be a string or map[string]any, got %T", cfg["image"])
	}
}

func imageSpecFromMap(m map[string]any) (*godo.ImageSourceSpec, error) {
	repo, _ := m["repository"].(string)
	if repo == "" {
		return nil, fmt.Errorf("image map config missing required field 'repository'")
	}
	regType, _ := m["registry_type"].(string)
	if regType == "" {
		regType = string(godo.ImageSourceSpecRegistryType_DOCR)
	}
	tag, _ := m["tag"].(string)
	if tag == "" {
		tag = "latest"
	}
	registry, _ := m["registry"].(string)
	return &godo.ImageSourceSpec{
		RegistryType: godo.ImageSourceSpecRegistryType(regType),
		Registry:     registry,
		Repository:   repo,
		Tag:          tag,
	}, nil
}

// envVarsFromConfig converts the "env_vars" map in spec config to App Platform
// environment variable definitions. Secret vars use "env_vars_secret" (canonical key).
// "secret_env_vars" is a legacy alias: it is used only when "env_vars_secret" is
// absent from the config entirely (key-presence check, not length check).
func envVarsFromConfig(cfg map[string]any) []*godo.AppVariableDefinition {
	raw, _ := cfg["env_vars"].(map[string]any)
	// Prefer the canonical key; fall back to the legacy alias only when the
	// canonical key is not present at all (not just empty).
	var secrets map[string]any
	if v, ok := cfg["env_vars_secret"]; ok {
		secrets, _ = v.(map[string]any)
	} else {
		secrets, _ = cfg["secret_env_vars"].(map[string]any)
	}
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

func (d *AppPlatformDriver) SensitiveKeys() []string { return nil }
