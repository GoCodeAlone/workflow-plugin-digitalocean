package drivers

import (
	"context"
	"fmt"
	"strings"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// AppDomainDriver manages one App Platform domain entry without rebuilding the
// rest of the app spec.
type AppDomainDriver struct {
	client AppPlatformClient
}

// NewAppDomainDriver creates an AppDomainDriver backed by a real godo client.
func NewAppDomainDriver(c *godo.Client) *AppDomainDriver {
	return &AppDomainDriver{client: c.Apps}
}

// NewAppDomainDriverWithClient creates a driver with an injected apps client.
func NewAppDomainDriverWithClient(c AppPlatformClient) *AppDomainDriver {
	return &AppDomainDriver{client: c}
}

func (d *AppDomainDriver) Create(ctx context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	return d.upsert(ctx, interfaces.ResourceRef{Name: spec.Name, Type: spec.Type}, spec)
}

func (d *AppDomainDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	appID, domain, err := parseAppDomainProviderID(ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("app domain read %q: %w", ref.Name, err)
	}
	app, err := d.getApp(ctx, appID)
	if err != nil {
		return nil, fmt.Errorf("app domain read %q: %w", ref.Name, err)
	}
	found := findAppDomain(app, domain)
	if found == nil {
		return nil, fmt.Errorf("app domain %q on app %q: %w", domain, appID, ErrResourceNotFound)
	}
	return appDomainOutput(ref.Name, app, found), nil
}

func (d *AppDomainDriver) Update(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	return d.upsert(ctx, ref, spec)
}

func (d *AppDomainDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	appID, domain, err := parseAppDomainProviderID(ref.ProviderID)
	if err != nil {
		return fmt.Errorf("app domain delete %q: %w", ref.Name, err)
	}
	app, err := d.getApp(ctx, appID)
	if err != nil {
		return fmt.Errorf("app domain delete %q: %w", ref.Name, err)
	}
	if app.Spec == nil {
		return fmt.Errorf("app domain delete %q: app %q has nil spec", ref.Name, appID)
	}
	domains := make([]*godo.AppDomainSpec, 0, len(app.Spec.Domains))
	for _, existing := range app.Spec.Domains {
		if existing == nil || !strings.EqualFold(existing.Domain, domain) {
			domains = append(domains, existing)
		}
	}
	if len(domains) == len(app.Spec.Domains) {
		return nil
	}
	app.Spec.Domains = domains
	_, _, err = d.client.Update(ctx, app.ID, &godo.AppUpdateRequest{Spec: app.Spec})
	if err != nil {
		return fmt.Errorf("app domain delete %q: %w", ref.Name, WrapGodoError(err))
	}
	return nil
}

func (d *AppDomainDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	domain, err := requiredString(desired.Config, "domain")
	if err != nil {
		return nil, fmt.Errorf("app domain diff %q: %w", desired.Name, err)
	}
	if current == nil {
		return &interfaces.DiffResult{NeedsUpdate: true}, nil
	}
	var changes []interfaces.FieldChange
	if curDomain, _ := current.Outputs["domain"].(string); !strings.EqualFold(curDomain, domain) {
		changes = append(changes, interfaces.FieldChange{Path: "domain", Old: curDomain, New: domain, ForceNew: true})
	}
	if desiredAppID, _ := desired.Config["app_id"].(string); desiredAppID != "" {
		currentAppID, _ := current.Outputs["app_id"].(string)
		if currentAppID != desiredAppID {
			changes = append(changes, interfaces.FieldChange{Path: "app_id", Old: currentAppID, New: desiredAppID, ForceNew: true})
		}
	}
	if desiredApp, _ := desired.Config["app"].(string); desiredApp != "" {
		currentApp, _ := current.Outputs["app"].(string)
		if currentApp != desiredApp {
			changes = append(changes, interfaces.FieldChange{Path: "app", Old: currentApp, New: desiredApp, ForceNew: true})
		}
	}
	if desiredType := desiredDomainType(desired.Config); desiredType != "" {
		curType, _ := current.Outputs["type"].(string)
		if strings.ToUpper(curType) != desiredType {
			changes = append(changes, interfaces.FieldChange{Path: "type", Old: curType, New: desiredType})
		}
	}
	for _, key := range []string{"zone", "certificate", "minimum_tls_version"} {
		desiredValue, _ := desired.Config[key].(string)
		if desiredValue == "" {
			continue
		}
		currentValue, _ := current.Outputs[key].(string)
		if currentValue != desiredValue {
			changes = append(changes, interfaces.FieldChange{Path: key, Old: currentValue, New: desiredValue})
		}
	}
	if desiredWildcard, ok := desired.Config["wildcard"].(bool); ok {
		currentWildcard, _ := current.Outputs["wildcard"].(bool)
		if currentWildcard != desiredWildcard {
			changes = append(changes, interfaces.FieldChange{Path: "wildcard", Old: currentWildcard, New: desiredWildcard})
		}
	}
	return &interfaces.DiffResult{NeedsUpdate: len(changes) > 0, NeedsReplace: hasForceNewChange(changes), Changes: changes}, nil
}

func (d *AppDomainDriver) HealthCheck(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	if _, err := d.Read(ctx, ref); err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	return &interfaces.HealthResult{Healthy: true, Message: "domain configured"}, nil
}

func (d *AppDomainDriver) Scale(_ context.Context, _ interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return nil, fmt.Errorf("app domain scale: unsupported")
}

func (d *AppDomainDriver) SensitiveKeys() []string { return nil }

func (d *AppDomainDriver) ProviderIDFormat() interfaces.ProviderIDFormat {
	return interfaces.IDFormatFreeform
}

func (d *AppDomainDriver) upsert(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	domain, err := requiredString(spec.Config, "domain")
	if err != nil {
		return nil, fmt.Errorf("app domain upsert %q: %w", spec.Name, err)
	}
	app, err := d.resolveApp(ctx, ref, spec)
	if err != nil {
		return nil, fmt.Errorf("app domain upsert %q: %w", spec.Name, err)
	}
	if app.Spec == nil {
		return nil, fmt.Errorf("app domain upsert %q: app %q has nil spec", spec.Name, app.ID)
	}
	desired := appDomainSpecFromConfig(domain, spec.Config)
	app.Spec.Domains = mergeAppDomain(app.Spec.Domains, desired)
	updated, _, err := d.client.Update(ctx, app.ID, &godo.AppUpdateRequest{Spec: app.Spec})
	if err != nil {
		return nil, fmt.Errorf("app domain upsert %q: %w", spec.Name, WrapGodoError(err))
	}
	if updated == nil {
		updated = app
	}
	found := findAppDomain(updated, domain)
	if found == nil {
		found = desired
	}
	return appDomainOutput(spec.Name, updated, found), nil
}

func (d *AppDomainDriver) resolveApp(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*godo.App, error) {
	if appID, _, err := parseAppDomainProviderID(ref.ProviderID); err == nil {
		return d.getApp(ctx, appID)
	}
	if appID, _ := spec.Config["app_id"].(string); appID != "" {
		return d.getApp(ctx, appID)
	}
	appName, _ := spec.Config["app"].(string)
	if appName == "" {
		return nil, fmt.Errorf("config requires app or app_id")
	}
	return d.findAppObjectByName(ctx, appName)
}

func (d *AppDomainDriver) getApp(ctx context.Context, appID string) (*godo.App, error) {
	app, _, err := d.client.Get(ctx, appID)
	if err != nil {
		return nil, WrapGodoError(err)
	}
	if app == nil || app.ID == "" {
		return nil, fmt.Errorf("app %q: %w", appID, ErrResourceNotFound)
	}
	return app, nil
}

func (d *AppDomainDriver) findAppObjectByName(ctx context.Context, name string) (*godo.App, error) {
	opts := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		apps, resp, err := d.client.List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("app platform list: %w", WrapGodoError(err))
		}
		for _, app := range apps {
			if app != nil && app.Spec != nil && app.Spec.Name == name {
				return app, nil
			}
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opts.Page++
	}
	return nil, fmt.Errorf("app %q: %w", name, ErrResourceNotFound)
}

func appDomainSpecFromConfig(domain string, cfg map[string]any) *godo.AppDomainSpec {
	out := &godo.AppDomainSpec{Domain: domain}
	if t := desiredDomainType(cfg); t != "" {
		out.Type = godo.AppDomainSpecType(t)
	}
	if zone, _ := cfg["zone"].(string); zone != "" {
		out.Zone = zone
	}
	if certificate, _ := cfg["certificate"].(string); certificate != "" {
		out.Certificate = certificate
	}
	if minimumTLSVersion, _ := cfg["minimum_tls_version"].(string); minimumTLSVersion != "" {
		out.MinimumTLSVersion = minimumTLSVersion
	}
	if wildcard, ok := cfg["wildcard"].(bool); ok {
		out.Wildcard = wildcard
	}
	return out
}

func desiredDomainType(cfg map[string]any) string {
	t, _ := cfg["type"].(string)
	return strings.ToUpper(strings.TrimSpace(t))
}

func mergeAppDomain(existing []*godo.AppDomainSpec, desired *godo.AppDomainSpec) []*godo.AppDomainSpec {
	out := make([]*godo.AppDomainSpec, 0, len(existing)+1)
	replaced := false
	for _, domain := range existing {
		if domain != nil && strings.EqualFold(domain.Domain, desired.Domain) {
			out = append(out, desired)
			replaced = true
			continue
		}
		out = append(out, domain)
	}
	if !replaced {
		out = append(out, desired)
	}
	return out
}

func findAppDomain(app *godo.App, domain string) *godo.AppDomainSpec {
	if app == nil || app.Spec == nil {
		return nil
	}
	for _, existing := range app.Spec.Domains {
		if existing != nil && strings.EqualFold(existing.Domain, domain) {
			return existing
		}
	}
	return nil
}

func appDomainOutput(name string, app *godo.App, domain *godo.AppDomainSpec) *interfaces.ResourceOutput {
	appName := ""
	if app.Spec != nil {
		appName = app.Spec.Name
	}
	return &interfaces.ResourceOutput{
		Name:       name,
		Type:       "infra.app_domain",
		ProviderID: app.ID + "/" + domain.Domain,
		Outputs: map[string]any{
			"app_id":              app.ID,
			"app":                 appName,
			"domain":              domain.Domain,
			"type":                string(domain.Type),
			"zone":                domain.Zone,
			"certificate":         domain.Certificate,
			"minimum_tls_version": domain.MinimumTLSVersion,
			"wildcard":            domain.Wildcard,
		},
		Status: "active",
	}
}

func parseAppDomainProviderID(providerID string) (string, string, error) {
	appID, domain, ok := strings.Cut(providerID, "/")
	if !ok || strings.TrimSpace(appID) == "" || strings.TrimSpace(domain) == "" {
		return "", "", fmt.Errorf("provider_id must be <app-id>/<domain>")
	}
	return appID, domain, nil
}

func requiredString(cfg map[string]any, key string) (string, error) {
	value, _ := cfg[key].(string)
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("config requires %s", key)
	}
	return value, nil
}

func hasForceNewChange(changes []interfaces.FieldChange) bool {
	for _, change := range changes {
		if change.ForceNew {
			return true
		}
	}
	return false
}
