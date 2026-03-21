package internal

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/digitalocean/godo"
	"golang.org/x/oauth2"
)

// tokenSource implements oauth2.TokenSource for the godo client.
type tokenSource struct{ token string }

func (t *tokenSource) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: t.token}, nil
}

// DOProvider implements interfaces.IaCProvider for DigitalOcean.
type DOProvider struct {
	client  *godo.Client
	region  string
	drivers map[string]interfaces.ResourceDriver
}

var _ interfaces.IaCProvider = (*DOProvider)(nil)

// NewDOProvider creates an uninitialised DOProvider.
func NewDOProvider() *DOProvider {
	return &DOProvider{}
}

func (p *DOProvider) Name() string    { return "digitalocean" }
func (p *DOProvider) Version() string { return "0.1.0" }

// Initialize configures the godo client using the provided config map.
// Required key: "token". Optional: "region" (default: "nyc3").
func (p *DOProvider) Initialize(_ context.Context, config map[string]any) error {
	token, _ := config["token"].(string)
	if token == "" {
		return fmt.Errorf("digitalocean: missing required config key 'token'")
	}
	region, _ := config["region"].(string)
	if region == "" {
		region = "nyc3"
	}
	p.region = region

	oauthClient := oauth2.NewClient(context.Background(), &tokenSource{token: token})
	p.client = godo.NewClient(oauthClient)

	p.drivers = map[string]interfaces.ResourceDriver{
		"infra.container_service": drivers.NewAppPlatformDriver(p.client, p.region),
		"infra.k8s_cluster":       drivers.NewKubernetesDriver(p.client, p.region),
		"infra.database":          drivers.NewDatabaseDriver(p.client, p.region),
		"infra.load_balancer":     drivers.NewLoadBalancerDriver(p.client, p.region),
		"infra.vpc":               drivers.NewVPCDriver(p.client, p.region),
		"infra.firewall":          drivers.NewFirewallDriver(p.client),
		"infra.dns":               drivers.NewDNSDriver(p.client),
		"infra.storage":           drivers.NewSpacesDriver(p.client, p.region),
		"infra.registry":          drivers.NewRegistryDriver(p.client),
		"infra.certificate":       drivers.NewCertificateDriver(p.client),
		"infra.droplet":           drivers.NewDropletDriver(p.client, p.region),
	}
	return nil
}

// Capabilities returns the resource types this provider supports.
func (p *DOProvider) Capabilities() []interfaces.IaCCapabilityDeclaration {
	ops := []string{"create", "read", "update", "delete", "scale"}
	return []interfaces.IaCCapabilityDeclaration{
		{ResourceType: "infra.container_service", Tier: 3, Operations: ops},
		{ResourceType: "infra.k8s_cluster", Tier: 1, Operations: ops},
		{ResourceType: "infra.database", Tier: 1, Operations: ops},
		{ResourceType: "infra.load_balancer", Tier: 1, Operations: ops},
		{ResourceType: "infra.vpc", Tier: 1, Operations: ops},
		{ResourceType: "infra.firewall", Tier: 1, Operations: ops},
		{ResourceType: "infra.dns", Tier: 1, Operations: ops},
		{ResourceType: "infra.storage", Tier: 1, Operations: ops},
		{ResourceType: "infra.registry", Tier: 2, Operations: ops},
		{ResourceType: "infra.certificate", Tier: 1, Operations: ops},
		{ResourceType: "infra.droplet", Tier: 1, Operations: ops},
	}
}

// ResourceDriver returns the driver for the given resource type.
func (p *DOProvider) ResourceDriver(resourceType string) (interfaces.ResourceDriver, error) {
	d, ok := p.drivers[resourceType]
	if !ok {
		return nil, fmt.Errorf("digitalocean: unsupported resource type %q", resourceType)
	}
	return d, nil
}

// ResolveSizing maps abstract size tiers to DigitalOcean SKUs.
func (p *DOProvider) ResolveSizing(resourceType string, size interfaces.Size, hints *interfaces.ResourceHints) (*interfaces.ProviderSizing, error) {
	return resolveSizing(resourceType, size, hints)
}

// Plan computes the set of actions needed to reach the desired state.
func (p *DOProvider) Plan(ctx context.Context, desired []interfaces.ResourceSpec, current []interfaces.ResourceState) (*interfaces.IaCPlan, error) {
	currentByName := make(map[string]interfaces.ResourceState, len(current))
	for _, r := range current {
		currentByName[r.Name] = r
	}

	plan := &interfaces.IaCPlan{
		ID:        fmt.Sprintf("plan-%d", time.Now().UnixNano()),
		CreatedAt: time.Now(),
	}

	for _, spec := range desired {
		cur, exists := currentByName[spec.Name]
		if !exists {
			plan.Actions = append(plan.Actions, interfaces.PlanAction{
				Action:   "create",
				Resource: spec,
			})
			continue
		}
		// Check if config changed.
		curHash := configHash(cur.AppliedConfig)
		newHash := configHash(spec.Config)
		if curHash != newHash {
			plan.Actions = append(plan.Actions, interfaces.PlanAction{
				Action:   "update",
				Resource: spec,
				Current:  &cur,
			})
		}
	}
	return plan, nil
}

// Apply executes the plan.
func (p *DOProvider) Apply(ctx context.Context, plan *interfaces.IaCPlan) (*interfaces.ApplyResult, error) {
	result := &interfaces.ApplyResult{PlanID: plan.ID}
	for _, action := range plan.Actions {
		d, err := p.ResourceDriver(action.Resource.Type)
		if err != nil {
			result.Errors = append(result.Errors, interfaces.ActionError{
				Resource: action.Resource.Name,
				Action:   action.Action,
				Error:    err.Error(),
			})
			continue
		}
		var out *interfaces.ResourceOutput
		switch action.Action {
		case "create":
			out, err = d.Create(ctx, action.Resource)
		case "update":
			ref := interfaces.ResourceRef{
				Name:       action.Resource.Name,
				Type:       action.Resource.Type,
				ProviderID: action.Current.ProviderID,
			}
			out, err = d.Update(ctx, ref, action.Resource)
		default:
			err = fmt.Errorf("unknown action %q", action.Action)
		}
		if err != nil {
			result.Errors = append(result.Errors, interfaces.ActionError{
				Resource: action.Resource.Name,
				Action:   action.Action,
				Error:    err.Error(),
			})
			continue
		}
		result.Resources = append(result.Resources, *out)
	}
	return result, nil
}

// Destroy deletes the given resources.
func (p *DOProvider) Destroy(ctx context.Context, resources []interfaces.ResourceRef) (*interfaces.DestroyResult, error) {
	result := &interfaces.DestroyResult{}
	for _, ref := range resources {
		d, err := p.ResourceDriver(ref.Type)
		if err != nil {
			result.Errors = append(result.Errors, interfaces.ActionError{
				Resource: ref.Name,
				Action:   "delete",
				Error:    err.Error(),
			})
			continue
		}
		if err := d.Delete(ctx, ref); err != nil {
			result.Errors = append(result.Errors, interfaces.ActionError{
				Resource: ref.Name,
				Action:   "delete",
				Error:    err.Error(),
			})
			continue
		}
		result.Destroyed = append(result.Destroyed, ref.Name)
	}
	return result, nil
}

// Status returns the live status of the given resources.
func (p *DOProvider) Status(ctx context.Context, resources []interfaces.ResourceRef) ([]interfaces.ResourceStatus, error) {
	var statuses []interfaces.ResourceStatus
	for _, ref := range resources {
		d, err := p.ResourceDriver(ref.Type)
		if err != nil {
			statuses = append(statuses, interfaces.ResourceStatus{
				Name:       ref.Name,
				Type:       ref.Type,
				ProviderID: ref.ProviderID,
				Status:     "unknown",
			})
			continue
		}
		out, err := d.Read(ctx, ref)
		if err != nil {
			statuses = append(statuses, interfaces.ResourceStatus{
				Name:       ref.Name,
				Type:       ref.Type,
				ProviderID: ref.ProviderID,
				Status:     "unknown",
			})
			continue
		}
		statuses = append(statuses, interfaces.ResourceStatus{
			Name:       out.Name,
			Type:       out.Type,
			ProviderID: out.ProviderID,
			Status:     out.Status,
			Outputs:    out.Outputs,
		})
	}
	return statuses, nil
}

// DetectDrift checks for drift between declared and actual resource state.
func (p *DOProvider) DetectDrift(ctx context.Context, resources []interfaces.ResourceRef) ([]interfaces.DriftResult, error) {
	var results []interfaces.DriftResult
	for _, ref := range resources {
		results = append(results, interfaces.DriftResult{
			Name:    ref.Name,
			Type:    ref.Type,
			Drifted: false,
		})
	}
	return results, nil
}

// Import brings an existing cloud resource under management.
func (p *DOProvider) Import(ctx context.Context, cloudID string, resourceType string) (*interfaces.ResourceState, error) {
	d, err := p.ResourceDriver(resourceType)
	if err != nil {
		return nil, err
	}
	ref := interfaces.ResourceRef{
		Name:       cloudID,
		Type:       resourceType,
		ProviderID: cloudID,
	}
	out, err := d.Read(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("digitalocean import: %w", err)
	}
	now := time.Now()
	return &interfaces.ResourceState{
		ID:         cloudID,
		Name:       out.Name,
		Type:       out.Type,
		Provider:   "digitalocean",
		ProviderID: out.ProviderID,
		Outputs:    out.Outputs,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

// Close is a no-op; the godo client has no persistent connection to close.
func (p *DOProvider) Close() error { return nil }

// configHash returns a stable hash of a config map for change detection.
func configHash(cfg map[string]any) string {
	h := sha256.New()
	fmt.Fprintf(h, "%v", cfg)
	return fmt.Sprintf("%x", h.Sum(nil))
}
