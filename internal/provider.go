package internal

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
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
// Required: "token".
// Optional: "region" (default "nyc3"), "spaces_access_key", "spaces_secret_key".
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

	spacesAccessKey, _ := config["spaces_access_key"].(string)
	spacesSecretKey, _ := config["spaces_secret_key"].(string)

	oauthClient := oauth2.NewClient(context.Background(), &tokenSource{token: token})
	p.client = godo.NewClient(oauthClient)

	p.drivers = map[string]interfaces.ResourceDriver{
		"infra.container_service": drivers.NewAppPlatformDriver(p.client, p.region),
		"infra.k8s_cluster":       drivers.NewKubernetesDriver(p.client, p.region),
		"infra.database":          drivers.NewDatabaseDriver(p.client, p.region),
		"infra.cache":             drivers.NewCacheDriver(p.client, p.region),
		"infra.load_balancer":     drivers.NewLoadBalancerDriver(p.client, p.region),
		"infra.vpc":               drivers.NewVPCDriver(p.client, p.region),
		"infra.firewall":          drivers.NewFirewallDriver(p.client),
		"infra.dns":               drivers.NewDNSDriver(p.client),
		"infra.storage":           drivers.NewSpacesDriver(p.client, p.region, spacesAccessKey, spacesSecretKey),
		"infra.registry":          drivers.NewRegistryDriver(p.client),
		"infra.certificate":       drivers.NewCertificateDriver(p.client),
		"infra.droplet":           drivers.NewDropletDriver(p.client, p.region),
		"infra.iam_role":          drivers.NewIAMRoleDriver(),
		"infra.api_gateway":       drivers.NewAPIGatewayDriver(p.client, p.region),
	}
	return nil
}

// Capabilities returns the resource types this provider supports.
func (p *DOProvider) Capabilities() []interfaces.IaCCapabilityDeclaration {
	ops := []string{"create", "read", "update", "delete", "scale"}
	noScale := []string{"create", "read", "update", "delete"}
	return []interfaces.IaCCapabilityDeclaration{
		{ResourceType: "infra.container_service", Tier: 3, Operations: ops},
		{ResourceType: "infra.k8s_cluster", Tier: 1, Operations: ops},
		{ResourceType: "infra.database", Tier: 1, Operations: ops},
		{ResourceType: "infra.cache", Tier: 1, Operations: ops},
		{ResourceType: "infra.load_balancer", Tier: 1, Operations: ops},
		{ResourceType: "infra.vpc", Tier: 1, Operations: ops},
		{ResourceType: "infra.firewall", Tier: 1, Operations: ops},
		{ResourceType: "infra.dns", Tier: 1, Operations: ops},
		{ResourceType: "infra.storage", Tier: 1, Operations: ops},
		{ResourceType: "infra.registry", Tier: 2, Operations: ops},
		{ResourceType: "infra.certificate", Tier: 1, Operations: ops},
		{ResourceType: "infra.droplet", Tier: 1, Operations: ops},
		{ResourceType: "infra.iam_role", Tier: 1, Operations: noScale},
		{ResourceType: "infra.api_gateway", Tier: 3, Operations: noScale},
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
func (p *DOProvider) Plan(_ context.Context, desired []interfaces.ResourceSpec, current []interfaces.ResourceState) (*interfaces.IaCPlan, error) {
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
		if configHash(cur.AppliedConfig) != configHash(spec.Config) {
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
				Resource: action.Resource.Name, Action: action.Action, Error: err.Error(),
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
				Resource: action.Resource.Name, Action: action.Action, Error: err.Error(),
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
				Resource: ref.Name, Action: "delete", Error: err.Error(),
			})
			continue
		}
		if err := d.Delete(ctx, ref); err != nil {
			result.Errors = append(result.Errors, interfaces.ActionError{
				Resource: ref.Name, Action: "delete", Error: err.Error(),
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
				Name: ref.Name, Type: ref.Type, ProviderID: ref.ProviderID, Status: "unknown",
			})
			continue
		}
		out, err := d.Read(ctx, ref)
		if err != nil {
			statuses = append(statuses, interfaces.ResourceStatus{
				Name: ref.Name, Type: ref.Type, ProviderID: ref.ProviderID, Status: "unknown",
			})
			continue
		}
		statuses = append(statuses, interfaces.ResourceStatus{
			Name: out.Name, Type: out.Type, ProviderID: out.ProviderID,
			Status: out.Status, Outputs: out.Outputs,
		})
	}
	return statuses, nil
}

// DetectDrift checks for drift between declared and actual resource state.
func (p *DOProvider) DetectDrift(_ context.Context, resources []interfaces.ResourceRef) ([]interfaces.DriftResult, error) {
	var results []interfaces.DriftResult
	for _, ref := range resources {
		results = append(results, interfaces.DriftResult{Name: ref.Name, Type: ref.Type, Drifted: false})
	}
	return results, nil
}

// Import brings an existing cloud resource under management.
func (p *DOProvider) Import(ctx context.Context, cloudID string, resourceType string) (*interfaces.ResourceState, error) {
	d, err := p.ResourceDriver(resourceType)
	if err != nil {
		return nil, err
	}
	ref := interfaces.ResourceRef{Name: cloudID, Type: resourceType, ProviderID: cloudID}
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

// configHash returns a stable, deterministic hash of a config map.
// Keys are sorted before JSON serialisation so map iteration order does not affect the result.
func configHash(cfg map[string]any) string {
	if len(cfg) == 0 {
		return ""
	}
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	ordered := make([]any, 0, len(keys)*2)
	for _, k := range keys {
		ordered = append(ordered, k, cfg[k])
	}
	data, _ := json.Marshal(ordered)
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)
}
