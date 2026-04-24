package drivers

import (
	"context"
	"fmt"
	"log"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// KubernetesClient is the godo Kubernetes interface (for mocking).
type KubernetesClient interface {
	Create(ctx context.Context, req *godo.KubernetesClusterCreateRequest) (*godo.KubernetesCluster, *godo.Response, error)
	Get(ctx context.Context, clusterID string) (*godo.KubernetesCluster, *godo.Response, error)
	List(ctx context.Context, opts *godo.ListOptions) ([]*godo.KubernetesCluster, *godo.Response, error)
	Update(ctx context.Context, clusterID string, req *godo.KubernetesClusterUpdateRequest) (*godo.KubernetesCluster, *godo.Response, error)
	Delete(ctx context.Context, clusterID string) (*godo.Response, error)
	UpdateNodePool(ctx context.Context, clusterID, poolID string, req *godo.KubernetesNodePoolUpdateRequest) (*godo.KubernetesNodePool, *godo.Response, error)
}

// KubernetesDriver manages DigitalOcean Kubernetes Service (DOKS) clusters (infra.k8s_cluster).
type KubernetesDriver struct {
	client KubernetesClient
	region string
}

// NewKubernetesDriver creates a KubernetesDriver backed by a real godo client.
func NewKubernetesDriver(c *godo.Client, region string) *KubernetesDriver {
	return &KubernetesDriver{client: c.Kubernetes, region: region}
}

// NewKubernetesDriverWithClient creates a driver with an injected client (for tests).
func NewKubernetesDriverWithClient(c KubernetesClient, region string) *KubernetesDriver {
	return &KubernetesDriver{client: c, region: region}
}

func (d *KubernetesDriver) Create(ctx context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	version := strFromConfig(spec.Config, "version", "latest")
	nodeSize := strFromConfig(spec.Config, "node_size", "s-2vcpu-4gb")
	nodeCount, _ := intFromConfig(spec.Config, "node_count", 3)
	region := strFromConfig(spec.Config, "region", d.region)

	req := &godo.KubernetesClusterCreateRequest{
		Name:        spec.Name,
		RegionSlug:  region,
		VersionSlug: version,
		NodePools: []*godo.KubernetesNodePoolCreateRequest{
			{
				Name:  spec.Name + "-pool",
				Size:  nodeSize,
				Count: nodeCount,
			},
		},
	}

	cluster, _, err := d.client.Create(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("kubernetes create %q: %w", spec.Name, WrapGodoError(err))
	}
	if cluster == nil || cluster.ID == "" {
		return nil, fmt.Errorf("kubernetes create %q: API returned cluster with empty ID", spec.Name)
	}
	return k8sOutput(cluster), nil
}

func (d *KubernetesDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	cluster, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("kubernetes read %q: %w", ref.Name, WrapGodoError(err))
	}
	return k8sOutput(cluster), nil
}

// findClusterByName iterates the paginated cluster list and returns the first
// entry whose Name matches. Returns ErrResourceNotFound if no match.
func (d *KubernetesDriver) findClusterByName(ctx context.Context, name string) (*interfaces.ResourceOutput, error) {
	opts := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		clusters, resp, err := d.client.List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("kubernetes list: %w", WrapGodoError(err))
		}
		for _, c := range clusters {
			if c.Name == name {
				return k8sOutput(c), nil
			}
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opts.Page++
	}
	return nil, fmt.Errorf("k8s_cluster %q: %w", name, ErrResourceNotFound)
}

// resolveProviderID returns a UUID-like ProviderID for the given ref. If
// ref.ProviderID is already UUID-shaped it is returned as-is. Otherwise a
// WARN is logged and a name-based lookup heals stale state transparently.
func (d *KubernetesDriver) resolveProviderID(ctx context.Context, ref interfaces.ResourceRef) (string, error) {
	if isUUIDLike(ref.ProviderID) {
		return ref.ProviderID, nil
	}
	log.Printf("warn: k8s_cluster %q: ProviderID %q is not UUID-like; resolving by name (state-heal)",
		ref.Name, ref.ProviderID)
	out, err := d.findClusterByName(ctx, ref.Name)
	if err != nil {
		return "", fmt.Errorf("k8s_cluster state-heal for %q: %w", ref.Name, err)
	}
	return out.ProviderID, nil
}

func (d *KubernetesDriver) Update(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	providerID, err := d.resolveProviderID(ctx, ref)
	if err != nil {
		return nil, err
	}
	req := &godo.KubernetesClusterUpdateRequest{
		Name: spec.Name,
	}
	cluster, _, err := d.client.Update(ctx, providerID, req)
	if err != nil {
		return nil, fmt.Errorf("kubernetes update %q: %w", ref.Name, WrapGodoError(err))
	}
	return k8sOutput(cluster), nil
}

func (d *KubernetesDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	providerID, err := d.resolveProviderID(ctx, ref)
	if err != nil {
		return err
	}
	_, err = d.client.Delete(ctx, providerID)
	if err != nil {
		return fmt.Errorf("kubernetes delete %q: %w", ref.Name, WrapGodoError(err))
	}
	return nil
}

func (d *KubernetesDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	if current == nil {
		return &interfaces.DiffResult{NeedsUpdate: true}, nil
	}
	return &interfaces.DiffResult{NeedsUpdate: false}, nil
}

func (d *KubernetesDriver) HealthCheck(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	cluster, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	healthy := cluster.Status != nil && cluster.Status.State == godo.KubernetesClusterStatusRunning
	msg := ""
	if !healthy && cluster.Status != nil {
		msg = cluster.Status.Message
	}
	return &interfaces.HealthResult{Healthy: healthy, Message: msg}, nil
}

// Scale resizes the first node pool of the cluster to the given replica count
// using godo.Kubernetes.UpdateNodePool.
func (d *KubernetesDriver) Scale(ctx context.Context, ref interfaces.ResourceRef, replicas int) (*interfaces.ResourceOutput, error) {
	cluster, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("kubernetes scale read %q: %w", ref.Name, WrapGodoError(err))
	}
	if len(cluster.NodePools) == 0 {
		return nil, fmt.Errorf("kubernetes scale %q: no node pools found", ref.Name)
	}
	pool := cluster.NodePools[0]
	count := replicas
	_, _, err = d.client.UpdateNodePool(ctx, ref.ProviderID, pool.ID, &godo.KubernetesNodePoolUpdateRequest{
		Count: &count,
	})
	if err != nil {
		return nil, fmt.Errorf("kubernetes scale %q pool %q: %w", ref.Name, pool.ID, WrapGodoError(err))
	}
	return d.Read(ctx, ref)
}

func k8sOutput(cluster *godo.KubernetesCluster) *interfaces.ResourceOutput {
	status := "provisioning"
	if cluster.Status != nil {
		status = string(cluster.Status.State)
	}
	return &interfaces.ResourceOutput{
		Name:       cluster.Name,
		Type:       "infra.k8s_cluster",
		ProviderID: cluster.ID,
		Outputs: map[string]any{
			"endpoint": cluster.Endpoint,
			"region":   cluster.RegionSlug,
			"version":  cluster.VersionSlug,
		},
		Status: status,
	}
}

func (d *KubernetesDriver) SensitiveKeys() []string { return nil }

func (d *KubernetesDriver) ProviderIDFormat() interfaces.ProviderIDFormat { return interfaces.IDFormatUUID }
