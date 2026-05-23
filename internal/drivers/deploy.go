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
	client                     AppPlatformClient
	regClient                  RegistryClient
	region                     string
	appID                      string
	appName                    string
	domainProbe                AppPlatformDomainProbe
	targetDeploymentID         string
	previousActiveDeploymentID string
	waitingForDeployment       bool
}

// NewAppDeployDriver creates a DeployDriver backed by a DO App Platform app.
func NewAppDeployDriver(c AppPlatformClient, region, appID, appName string) *AppDeployDriver {
	return &AppDeployDriver{client: c, region: region, appID: appID, appName: appName}
}

// NewAppDeployDriverWithRegistry creates a DeployDriver with a DOCR image
// presence pre-flight. The registry client may be nil to skip the check.
func NewAppDeployDriverWithRegistry(c AppPlatformClient, r RegistryClient, region, appID, appName string) *AppDeployDriver {
	return &AppDeployDriver{client: c, regClient: r, region: region, appID: appID, appName: appName}
}

// NewAppDeployDriverWithDomainProbe creates a DeployDriver with an injected
// custom-domain readiness probe for tests.
func NewAppDeployDriverWithDomainProbe(c AppPlatformClient, region, appID, appName string, probe AppPlatformDomainProbe) *AppDeployDriver {
	return &AppDeployDriver{client: c, region: region, appID: appID, appName: appName, domainProbe: probe}
}

func (d *AppDeployDriver) Update(ctx context.Context, image string) error {
	if d.regClient != nil {
		if err := verifyImagePresentInDOCR(ctx, d.regClient, image); err != nil {
			return err
		}
	}
	app, err := d.getApp(ctx)
	if err != nil {
		return fmt.Errorf("app deploy: get %q: %w", d.appName, err)
	}
	d.previousActiveDeploymentID = deploymentID(app.ActiveDeployment)
	spec, err := cloneAppSpec(app.Spec)
	if err != nil {
		return fmt.Errorf("app deploy: clone app spec %q: %w", d.appName, err)
	}
	if spec == nil {
		return fmt.Errorf("app deploy: %q has no app spec", d.appName)
	}
	for _, svc := range spec.Services {
		if svc.Image != nil {
			svc.Image.Repository = imageRepo(image)
			svc.Image.Tag = imageTag(image)
		}
	}
	updated, _, err := d.client.Update(ctx, d.appID, &godo.AppUpdateRequest{Spec: spec})
	if err != nil {
		return fmt.Errorf("app deploy: update %q: %w", d.appName, err)
	}
	d.waitingForDeployment = true
	d.targetDeploymentID = deploymentID(selectUpdateDeployment(updated, d.previousActiveDeploymentID))
	return nil
}

func (d *AppDeployDriver) HealthCheck(ctx context.Context, path string) error {
	app, err := d.getApp(ctx)
	if err != nil {
		return fmt.Errorf("app deploy: health check %q: %w", d.appName, err)
	}
	if d.waitingForDeployment {
		dep, err := d.currentTargetDeployment(ctx, app)
		if err != nil {
			return err
		}
		if dep == nil {
			return fmt.Errorf("app deploy: %q waiting for deployment after update", d.appName)
		}
		if err := deploymentHealthError(d.appName, dep); err != nil {
			return err
		}
		return appPlatformCustomDomainReadinessError(ctx, d.appName, app, d.domainProbe, path)
	}
	if app.ActiveDeployment == nil || app.ActiveDeployment.Phase != godo.DeploymentPhase_Active {
		phase := ""
		if app.ActiveDeployment != nil {
			phase = string(app.ActiveDeployment.Phase)
		}
		return fmt.Errorf("app deploy: %q not active (phase: %q)", d.appName, phase)
	}
	return appPlatformCustomDomainReadinessError(ctx, d.appName, app, d.domainProbe, path)
}

func (d *AppDeployDriver) currentTargetDeployment(ctx context.Context, app *godo.App) (*godo.Deployment, error) {
	if dep := selectUpdateDeployment(app, d.previousActiveDeploymentID); dep != nil {
		if d.targetDeploymentID == "" || dep.ID == d.targetDeploymentID {
			d.targetDeploymentID = dep.ID
			return dep, nil
		}
		if dep.PreviousDeploymentID == d.targetDeploymentID {
			d.targetDeploymentID = dep.ID
			return dep, nil
		}
	}
	appID, err := d.resolveAppID(ctx)
	if err != nil {
		return nil, err
	}
	deployments, _, err := d.client.ListDeployments(ctx, appID, &godo.ListOptions{Page: 1, PerPage: 20})
	if err != nil {
		return nil, fmt.Errorf("app deploy: list deployments %q: %w", d.appName, err)
	}
	for _, dep := range deployments {
		if dep == nil {
			continue
		}
		if d.targetDeploymentID != "" && dep.ID == d.targetDeploymentID {
			return dep, nil
		}
		if d.targetDeploymentID == "" && isHistoricalUpdateDeployment(dep, d.previousActiveDeploymentID) {
			d.targetDeploymentID = dep.ID
			return dep, nil
		}
	}
	return nil, nil
}

func deploymentHealthError(appName string, dep *godo.Deployment) error {
	switch dep.Phase {
	case godo.DeploymentPhase_Active:
		return nil
	case godo.DeploymentPhase_PendingBuild,
		godo.DeploymentPhase_Building,
		godo.DeploymentPhase_PendingDeploy,
		godo.DeploymentPhase_Deploying:
		return fmt.Errorf("app deploy: %q deployment in progress: %s (%s)", appName, dep.Phase, dep.ID)
	case godo.DeploymentPhase_Error,
		godo.DeploymentPhase_Canceled,
		godo.DeploymentPhase_Superseded:
		cause := dep.Cause
		if cause == "" {
			cause = string(dep.Phase)
		}
		return fmt.Errorf("app deploy: %q deployment failed: %s (%s): %s", appName, dep.Phase, dep.ID, cause)
	default:
		return fmt.Errorf("app deploy: %q deployment phase unknown: %s (%s)", appName, dep.Phase, dep.ID)
	}
}

func selectUpdateDeployment(app *godo.App, previousActiveID string) *godo.Deployment {
	if app == nil {
		return nil
	}
	for _, dep := range []*godo.Deployment{app.InProgressDeployment, app.PendingDeployment, app.ActiveDeployment} {
		if isNewerDeployment(dep, previousActiveID) {
			return dep
		}
	}
	return nil
}

func isNewerDeployment(dep *godo.Deployment, previousActiveID string) bool {
	if dep == nil || dep.ID == "" {
		return false
	}
	return dep.ID != previousActiveID || dep.PreviousDeploymentID == previousActiveID
}

func isHistoricalUpdateDeployment(dep *godo.Deployment, previousActiveID string) bool {
	if dep == nil || dep.ID == "" {
		return false
	}
	if previousActiveID == "" {
		return true
	}
	return dep.PreviousDeploymentID == previousActiveID
}

func deploymentID(dep *godo.Deployment) string {
	if dep == nil {
		return ""
	}
	return dep.ID
}

func (d *AppDeployDriver) CurrentImage(ctx context.Context) (string, error) {
	app, err := d.getApp(ctx)
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
	app, err := d.getApp(ctx)
	if err != nil {
		return 0, fmt.Errorf("app deploy: replica count %q: %w", d.appName, err)
	}
	if app.Spec == nil || len(app.Spec.Services) == 0 {
		return 1, nil
	}
	return int(app.Spec.Services[0].InstanceCount), nil
}

func (d *AppDeployDriver) getApp(ctx context.Context) (*godo.App, error) {
	appID, err := d.resolveAppID(ctx)
	if err != nil {
		return nil, err
	}
	app, _, err := d.client.Get(ctx, appID)
	return app, err
}

func (d *AppDeployDriver) resolveAppID(ctx context.Context) (string, error) {
	if d.appID != "" {
		return d.appID, nil
	}
	if d.appName == "" {
		return "", fmt.Errorf("app deploy: app ID or app name is required")
	}
	opts := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		apps, resp, err := d.client.List(ctx, opts)
		if err != nil {
			return "", err
		}
		for _, app := range apps {
			if app != nil && app.Spec != nil && app.Spec.Name == d.appName {
				d.appID = app.ID
				return d.appID, nil
			}
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opts.Page++
	}
	return "", fmt.Errorf("app %q not found", d.appName)
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
	client      AppPlatformClient
	regClient   RegistryClient
	region      string
	blueID      string
	blueName    string
	greenID     string // set during CreateGreen
	greenURL    string // set during CreateGreen
	stableCheck bool
	blueDeploy  *AppDeployDriver
	greenDeploy *AppDeployDriver
	domainProbe AppPlatformDomainProbe // optional; nil → default HTTPS probe
}

// NewAppBlueGreenDriver creates a BlueGreenDriver for DO App Platform.
func NewAppBlueGreenDriver(c AppPlatformClient, region, blueID, blueName string) *AppBlueGreenDriver {
	return &AppBlueGreenDriver{client: c, region: region, blueID: blueID, blueName: blueName}
}

// NewAppBlueGreenDriverWithRegistry creates a BlueGreenDriver with DOCR image
// presence pre-flight. The registry client may be nil to skip the check.
func NewAppBlueGreenDriverWithRegistry(c AppPlatformClient, r RegistryClient, region, blueID, blueName string) *AppBlueGreenDriver {
	return &AppBlueGreenDriver{client: c, regClient: r, region: region, blueID: blueID, blueName: blueName}
}

// NewAppBlueGreenDriverWithDomainProbe is like NewAppBlueGreenDriverWithRegistry
// but also injects probe into both inner *AppDeployDriver instances created by
// blueDriver()/greenDriver(). Intended for unit tests that need to substitute
// the HTTPS probe.
func NewAppBlueGreenDriverWithDomainProbe(c AppPlatformClient, r RegistryClient, region, blueID, blueName string, probe AppPlatformDomainProbe) *AppBlueGreenDriver {
	d := NewAppBlueGreenDriverWithRegistry(c, r, region, blueID, blueName)
	d.domainProbe = probe
	return d
}

// DeployDriver methods delegate to the blue (stable) app.

func (d *AppBlueGreenDriver) Update(ctx context.Context, image string) error {
	return d.blueDriver().Update(ctx, image)
}

func (d *AppBlueGreenDriver) HealthCheck(ctx context.Context, path string) error {
	if d.greenID != "" && !d.stableCheck {
		return d.greenDriver().HealthCheck(ctx, path)
	}
	return d.blueDriver().HealthCheck(ctx, path)
}

func (d *AppBlueGreenDriver) CurrentImage(ctx context.Context) (string, error) {
	return d.blueDriver().CurrentImage(ctx)
}

func (d *AppBlueGreenDriver) ReplicaCount(ctx context.Context) (int, error) {
	return d.blueDriver().ReplicaCount(ctx)
}

// CreateGreen creates a new App Platform app with the "-green" name suffix and
// the given image, recording the green app ID and live URL for later use.
func (d *AppBlueGreenDriver) CreateGreen(ctx context.Context, image string) error {
	if d.regClient != nil {
		if err := verifyImagePresentInDOCR(ctx, d.regClient, image); err != nil {
			return err
		}
	}
	blueApp, err := d.blueDriver().getApp(ctx)
	if err != nil {
		return fmt.Errorf("app blue-green: get blue %q: %w", d.blueName, err)
	}

	greenSpec, err := cloneAppSpec(blueApp.Spec)
	if err != nil {
		return fmt.Errorf("app blue-green: clone blue spec %q: %w", d.blueName, err)
	}
	if greenSpec == nil {
		return fmt.Errorf("app blue-green: blue app %q has no app spec", d.blueName)
	}
	greenSpec.Name = d.blueName + "-green"
	sanitizeClonedSpecForCreate(greenSpec)
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
	d.greenDeploy = NewAppDeployDriverWithRegistry(d.client, d.regClient, d.region, d.greenID, d.blueName+"-green")
	d.stableCheck = false
	return nil
}

// SwitchTraffic updates the blue app spec to use the green image, effectively
// promoting the green version as the stable app. DO App Platform does not
// support weighted traffic splitting natively; this performs a full cutover.
func (d *AppBlueGreenDriver) SwitchTraffic(ctx context.Context) error {
	if d.greenID == "" {
		return fmt.Errorf("app blue-green: CreateGreen must be called before SwitchTraffic")
	}
	greenImg, err := d.greenDriver().CurrentImage(ctx)
	if err != nil {
		return fmt.Errorf("app blue-green: get green image: %w", err)
	}
	if err := d.blueDriver().Update(ctx, greenImg); err != nil {
		return err
	}
	d.stableCheck = true
	return nil
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

func (d *AppBlueGreenDriver) blueDriver() *AppDeployDriver {
	if d.blueDeploy == nil {
		d.blueDeploy = NewAppDeployDriverWithRegistry(d.client, d.regClient, d.region, d.blueID, d.blueName)
		d.blueDeploy.domainProbe = d.domainProbe
	}
	return d.blueDeploy
}

func (d *AppBlueGreenDriver) greenDriver() *AppDeployDriver {
	if d.greenDeploy == nil {
		d.greenDeploy = NewAppDeployDriverWithRegistry(d.client, d.regClient, d.region, d.greenID, d.blueName+"-green")
		d.greenDeploy.domainProbe = d.domainProbe
	}
	return d.greenDeploy
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
	client       AppPlatformClient
	regClient    RegistryClient
	region       string
	stableID     string
	stableName   string
	canaryID     string // set during CreateCanary
	stableDeploy *AppDeployDriver
	canaryDeploy *AppDeployDriver
	domainProbe  AppPlatformDomainProbe // optional; nil → default HTTPS probe
}

// NewAppCanaryDriver creates a CanaryDriver for DO App Platform.
func NewAppCanaryDriver(c AppPlatformClient, region, stableID, stableName string) *AppCanaryDriver {
	return &AppCanaryDriver{client: c, region: region, stableID: stableID, stableName: stableName}
}

// NewAppCanaryDriverWithRegistry creates a CanaryDriver with DOCR image
// presence pre-flight. The registry client may be nil to skip the check.
func NewAppCanaryDriverWithRegistry(c AppPlatformClient, r RegistryClient, region, stableID, stableName string) *AppCanaryDriver {
	return &AppCanaryDriver{client: c, regClient: r, region: region, stableID: stableID, stableName: stableName}
}

// NewAppCanaryDriverWithDomainProbe is like NewAppCanaryDriverWithRegistry but
// also injects probe into the inner *AppDeployDriver instances created by
// stableDriver()/canaryDriver(). Intended for unit tests.
func NewAppCanaryDriverWithDomainProbe(c AppPlatformClient, r RegistryClient, region, stableID, stableName string, probe AppPlatformDomainProbe) *AppCanaryDriver {
	d := NewAppCanaryDriverWithRegistry(c, r, region, stableID, stableName)
	d.domainProbe = probe
	return d
}

// DeployDriver methods delegate to the stable app.

func (d *AppCanaryDriver) Update(ctx context.Context, image string) error {
	return d.stableDriver().Update(ctx, image)
}

func (d *AppCanaryDriver) HealthCheck(ctx context.Context, path string) error {
	if d.canaryID != "" {
		return d.canaryDriver().HealthCheck(ctx, path)
	}
	return d.stableDriver().HealthCheck(ctx, path)
}

func (d *AppCanaryDriver) CurrentImage(ctx context.Context) (string, error) {
	return d.stableDriver().CurrentImage(ctx)
}

func (d *AppCanaryDriver) ReplicaCount(ctx context.Context) (int, error) {
	return d.stableDriver().ReplicaCount(ctx)
}

// CreateCanary creates a new App Platform app with the "-canary" name suffix
// and the given image.
func (d *AppCanaryDriver) CreateCanary(ctx context.Context, image string) error {
	if d.regClient != nil {
		if err := verifyImagePresentInDOCR(ctx, d.regClient, image); err != nil {
			return err
		}
	}
	stableApp, err := d.stableDriver().getApp(ctx)
	if err != nil {
		return fmt.Errorf("app canary: get stable %q: %w", d.stableName, err)
	}

	canarySpec, err := cloneAppSpec(stableApp.Spec)
	if err != nil {
		return fmt.Errorf("app canary: clone stable spec %q: %w", d.stableName, err)
	}
	if canarySpec == nil {
		return fmt.Errorf("app canary: stable app %q has no app spec", d.stableName)
	}
	canarySpec.Name = d.stableName + "-canary"
	sanitizeClonedSpecForCreate(canarySpec)
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
	d.canaryDeploy = NewAppDeployDriverWithRegistry(d.client, d.regClient, d.region, d.canaryID, d.stableName+"-canary")
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
	canaryImg, err := d.canaryDriver().CurrentImage(ctx)
	if err != nil {
		return fmt.Errorf("app canary: get canary image: %w", err)
	}
	if err := d.stableDriver().Update(ctx, canaryImg); err != nil {
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
	d.canaryDeploy = nil
	return nil
}

func (d *AppCanaryDriver) stableDriver() *AppDeployDriver {
	if d.stableDeploy == nil {
		d.stableDeploy = NewAppDeployDriverWithRegistry(d.client, d.regClient, d.region, d.stableID, d.stableName)
		d.stableDeploy.domainProbe = d.domainProbe
	}
	return d.stableDeploy
}

func (d *AppCanaryDriver) canaryDriver() *AppDeployDriver {
	if d.canaryDeploy == nil {
		d.canaryDeploy = NewAppDeployDriverWithRegistry(d.client, d.regClient, d.region, d.canaryID, d.stableName+"-canary")
		d.canaryDeploy.domainProbe = d.domainProbe
	}
	return d.canaryDeploy
}

// sanitizeClonedSpecForCreate prepares a spec that was deep-copied from another
// app for use in an Apps.Create call. It clears only the custom-domain claim
// field that would collide with the source app on DO App Platform; everything
// else (Services / Workers / Jobs / Functions / StaticSites / Ingress) is left
// untouched so the new app is a faithful image-swapped clone.
func sanitizeClonedSpecForCreate(spec *godo.AppSpec) {
	if spec == nil {
		return
	}
	spec.Domains = nil
}
