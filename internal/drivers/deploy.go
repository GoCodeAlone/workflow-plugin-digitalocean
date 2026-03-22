package drivers

import (
	"context"
	"fmt"

	"github.com/digitalocean/godo"
)

// ─── AppDeployDriver ──────────────────────────────────────────────────────────

// AppDeployDriver implements module.DeployDriver for DigitalOcean App Platform.
// It manages a single App Platform application identified by its app ID.
type AppDeployDriver struct {
	client  AppPlatformClient
	region  string
	appID   string
	appName string
}

// NewAppDeployDriver creates a DeployDriver backed by a DO App Platform app.
func NewAppDeployDriver(c AppPlatformClient, region, appID, appName string) *AppDeployDriver {
	return &AppDeployDriver{client: c, region: region, appID: appID, appName: appName}
}

func (d *AppDeployDriver) Update(ctx context.Context, image string) error {
	app, _, err := d.client.Get(ctx, d.appID)
	if err != nil {
		return fmt.Errorf("app deploy: get %q: %w", d.appName, err)
	}
	spec := app.Spec
	for _, svc := range spec.Services {
		if svc.Image != nil {
			svc.Image.Repository = imageRepo(image)
			svc.Image.Tag = imageTag(image)
		}
	}
	if _, _, err := d.client.Update(ctx, d.appID, &godo.AppUpdateRequest{Spec: spec}); err != nil {
		return fmt.Errorf("app deploy: update %q: %w", d.appName, err)
	}
	return nil
}

func (d *AppDeployDriver) HealthCheck(ctx context.Context, _ string) error {
	app, _, err := d.client.Get(ctx, d.appID)
	if err != nil {
		return fmt.Errorf("app deploy: health check %q: %w", d.appName, err)
	}
	if app.ActiveDeployment == nil || app.ActiveDeployment.Phase != godo.DeploymentPhase_Active {
		phase := ""
		if app.ActiveDeployment != nil {
			phase = string(app.ActiveDeployment.Phase)
		}
		return fmt.Errorf("app deploy: %q not active (phase: %q)", d.appName, phase)
	}
	return nil
}

func (d *AppDeployDriver) CurrentImage(ctx context.Context) (string, error) {
	app, _, err := d.client.Get(ctx, d.appID)
	if err != nil {
		return "", fmt.Errorf("app deploy: current image %q: %w", d.appName, err)
	}
	if app.Spec == nil || len(app.Spec.Services) == 0 {
		return "", fmt.Errorf("app deploy: no services in %q", d.appName)
	}
	svc := app.Spec.Services[0]
	if svc.Image == nil {
		return "", fmt.Errorf("app deploy: service in %q has no image spec", d.appName)
	}
	return svc.Image.Repository + ":" + svc.Image.Tag, nil
}

func (d *AppDeployDriver) ReplicaCount(ctx context.Context) (int, error) {
	app, _, err := d.client.Get(ctx, d.appID)
	if err != nil {
		return 0, fmt.Errorf("app deploy: replica count %q: %w", d.appName, err)
	}
	if app.Spec == nil || len(app.Spec.Services) == 0 {
		return 1, nil
	}
	return int(app.Spec.Services[0].InstanceCount), nil
}

// ─── AppBlueGreenDriver ───────────────────────────────────────────────────────

// AppBlueGreenDriver implements module.BlueGreenDriver for DigitalOcean App Platform.
//
// Blue environment: the existing app identified by blueID.
// Green environment: a new app created with the "-green" name suffix.
//
// SwitchTraffic is implemented by updating the blue app's spec with the green
// image (making blue the new stable), then DestroyBlue removes the green clone.
// The green app's live URL is returned from GreenEndpoint.
type AppBlueGreenDriver struct {
	client    AppPlatformClient
	region    string
	blueID    string
	blueName  string
	greenID   string // set during CreateGreen
	greenURL  string // set during CreateGreen
}

// NewAppBlueGreenDriver creates a BlueGreenDriver for DO App Platform.
func NewAppBlueGreenDriver(c AppPlatformClient, region, blueID, blueName string) *AppBlueGreenDriver {
	return &AppBlueGreenDriver{client: c, region: region, blueID: blueID, blueName: blueName}
}

// DeployDriver methods delegate to the blue (stable) app.

func (d *AppBlueGreenDriver) Update(ctx context.Context, image string) error {
	stable := NewAppDeployDriver(d.client, d.region, d.blueID, d.blueName)
	return stable.Update(ctx, image)
}

func (d *AppBlueGreenDriver) HealthCheck(ctx context.Context, path string) error {
	target := d.greenID
	name := d.blueName + "-green"
	if target == "" {
		target = d.blueID
		name = d.blueName
	}
	drv := NewAppDeployDriver(d.client, d.region, target, name)
	return drv.HealthCheck(ctx, path)
}

func (d *AppBlueGreenDriver) CurrentImage(ctx context.Context) (string, error) {
	stable := NewAppDeployDriver(d.client, d.region, d.blueID, d.blueName)
	return stable.CurrentImage(ctx)
}

func (d *AppBlueGreenDriver) ReplicaCount(ctx context.Context) (int, error) {
	stable := NewAppDeployDriver(d.client, d.region, d.blueID, d.blueName)
	return stable.ReplicaCount(ctx)
}

// CreateGreen creates a new App Platform app with the "-green" name suffix and
// the given image, recording the green app ID and live URL for later use.
func (d *AppBlueGreenDriver) CreateGreen(ctx context.Context, image string) error {
	blueApp, _, err := d.client.Get(ctx, d.blueID)
	if err != nil {
		return fmt.Errorf("app blue-green: get blue %q: %w", d.blueName, err)
	}

	greenSpec := blueApp.Spec
	greenSpec.Name = d.blueName + "-green"
	for _, svc := range greenSpec.Services {
		if svc.Image != nil {
			svc.Image.Repository = imageRepo(image)
			svc.Image.Tag = imageTag(image)
		}
	}

	greenApp, _, err := d.client.Create(ctx, &godo.AppCreateRequest{Spec: greenSpec})
	if err != nil {
		return fmt.Errorf("app blue-green: create green: %w", err)
	}
	d.greenID = greenApp.ID
	d.greenURL = greenApp.LiveURL
	return nil
}

// SwitchTraffic updates the blue app spec to use the green image, effectively
// promoting the green version as the stable app. DO App Platform does not
// support weighted traffic splitting natively; this performs a full cutover.
func (d *AppBlueGreenDriver) SwitchTraffic(ctx context.Context) error {
	if d.greenID == "" {
		return fmt.Errorf("app blue-green: CreateGreen must be called before SwitchTraffic")
	}
	greenImg, err := NewAppDeployDriver(d.client, d.region, d.greenID, d.blueName+"-green").CurrentImage(ctx)
	if err != nil {
		return fmt.Errorf("app blue-green: get green image: %w", err)
	}
	return NewAppDeployDriver(d.client, d.region, d.blueID, d.blueName).Update(ctx, greenImg)
}

// DestroyBlue deletes the green clone (the temporary environment).
func (d *AppBlueGreenDriver) DestroyBlue(ctx context.Context) error {
	if d.greenID == "" {
		return fmt.Errorf("app blue-green: no green app to destroy")
	}
	if _, err := d.client.Delete(ctx, d.greenID); err != nil {
		return fmt.Errorf("app blue-green: destroy green clone: %w", err)
	}
	return nil
}

// GreenEndpoint returns the live URL of the green App Platform app.
func (d *AppBlueGreenDriver) GreenEndpoint(_ context.Context) (string, error) {
	if d.greenURL == "" {
		return "", fmt.Errorf("app blue-green: green endpoint not available (CreateGreen not called)")
	}
	return d.greenURL, nil
}

// ─── AppCanaryDriver ──────────────────────────────────────────────────────────

// AppCanaryDriver implements module.CanaryDriver for DigitalOcean App Platform.
//
// DigitalOcean App Platform does not support native traffic splitting between
// app instances. RoutePercent returns a clear unsupported error directing users
// to DigitalOcean Load Balancer + Droplets for canary deployments.
//
// CreateCanary, PromoteCanary, and DestroyCanary follow the standard
// create/promote/delete pattern using two separate apps.
type AppCanaryDriver struct {
	client     AppPlatformClient
	region     string
	stableID   string
	stableName string
	canaryID   string // set during CreateCanary
}

// NewAppCanaryDriver creates a CanaryDriver for DO App Platform.
func NewAppCanaryDriver(c AppPlatformClient, region, stableID, stableName string) *AppCanaryDriver {
	return &AppCanaryDriver{client: c, region: region, stableID: stableID, stableName: stableName}
}

// DeployDriver methods delegate to the stable app.

func (d *AppCanaryDriver) Update(ctx context.Context, image string) error {
	return NewAppDeployDriver(d.client, d.region, d.stableID, d.stableName).Update(ctx, image)
}

func (d *AppCanaryDriver) HealthCheck(ctx context.Context, path string) error {
	target, name := d.stableID, d.stableName
	if d.canaryID != "" {
		target = d.canaryID
		name = d.stableName + "-canary"
	}
	return NewAppDeployDriver(d.client, d.region, target, name).HealthCheck(ctx, path)
}

func (d *AppCanaryDriver) CurrentImage(ctx context.Context) (string, error) {
	return NewAppDeployDriver(d.client, d.region, d.stableID, d.stableName).CurrentImage(ctx)
}

func (d *AppCanaryDriver) ReplicaCount(ctx context.Context) (int, error) {
	return NewAppDeployDriver(d.client, d.region, d.stableID, d.stableName).ReplicaCount(ctx)
}

// CreateCanary creates a new App Platform app with the "-canary" name suffix
// and the given image.
func (d *AppCanaryDriver) CreateCanary(ctx context.Context, image string) error {
	stableApp, _, err := d.client.Get(ctx, d.stableID)
	if err != nil {
		return fmt.Errorf("app canary: get stable %q: %w", d.stableName, err)
	}

	canarySpec := stableApp.Spec
	canarySpec.Name = d.stableName + "-canary"
	for _, svc := range canarySpec.Services {
		if svc.Image != nil {
			svc.Image.Repository = imageRepo(image)
			svc.Image.Tag = imageTag(image)
		}
	}

	canaryApp, _, err := d.client.Create(ctx, &godo.AppCreateRequest{Spec: canarySpec})
	if err != nil {
		return fmt.Errorf("app canary: create canary: %w", err)
	}
	d.canaryID = canaryApp.ID
	return nil
}

// RoutePercent is not supported by DigitalOcean App Platform. Use
// DigitalOcean Load Balancer with Droplets for canary traffic splitting.
func (d *AppCanaryDriver) RoutePercent(_ context.Context, percent int) error {
	return fmt.Errorf("app canary: RoutePercent(%d) unsupported — DigitalOcean App Platform does not "+
		"support traffic splitting; use DigitalOcean Load Balancer + Droplets for canary deployments", percent)
}

// CheckMetricGate always passes (no native metric integration).
func (d *AppCanaryDriver) CheckMetricGate(_ context.Context, _ string) error {
	return nil
}

// PromoteCanary updates the stable app with the canary image and deletes the canary.
func (d *AppCanaryDriver) PromoteCanary(ctx context.Context) error {
	if d.canaryID == "" {
		return fmt.Errorf("app canary: CreateCanary must be called before PromoteCanary")
	}
	canaryImg, err := NewAppDeployDriver(d.client, d.region, d.canaryID, d.stableName+"-canary").CurrentImage(ctx)
	if err != nil {
		return fmt.Errorf("app canary: get canary image: %w", err)
	}
	if err := NewAppDeployDriver(d.client, d.region, d.stableID, d.stableName).Update(ctx, canaryImg); err != nil {
		return fmt.Errorf("app canary: promote to stable: %w", err)
	}
	return d.DestroyCanary(ctx)
}

// DestroyCanary deletes the canary App Platform app.
func (d *AppCanaryDriver) DestroyCanary(ctx context.Context) error {
	if d.canaryID == "" {
		return fmt.Errorf("app canary: no canary app to destroy")
	}
	if _, err := d.client.Delete(ctx, d.canaryID); err != nil {
		return fmt.Errorf("app canary: destroy canary: %w", err)
	}
	d.canaryID = ""
	return nil
}
