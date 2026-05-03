package drivers

import (
	"context"
	"fmt"
	"strconv"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// DropletsClient is the godo Droplets interface (for mocking).
type DropletsClient interface {
	Create(ctx context.Context, req *godo.DropletCreateRequest) (*godo.Droplet, *godo.Response, error)
	Get(ctx context.Context, dropletID int) (*godo.Droplet, *godo.Response, error)
	Delete(ctx context.Context, dropletID int) (*godo.Response, error)
}

// DropletDriver manages DigitalOcean Droplets (infra.droplet).
type DropletDriver struct {
	client  DropletsClient
	storage StorageClient // optional; required only when spec.Config["volumes"] is non-empty
	region  string
}

// NewDropletDriver creates a DropletDriver backed by a real godo client.
// The Storage client is wired so volumes-by-name resolution works.
func NewDropletDriver(c *godo.Client, region string) *DropletDriver {
	return &DropletDriver{client: c.Droplets, storage: c.Storage, region: region}
}

// NewDropletDriverWithClient creates a driver with an injected client (for tests).
// The optional storage argument is used only when a spec references volumes by
// name; pass nil if your test does not exercise that path.
func NewDropletDriverWithClient(c DropletsClient, region string, storage ...StorageClient) *DropletDriver {
	d := &DropletDriver{client: c, region: region}
	if len(storage) > 0 {
		d.storage = storage[0]
	}
	return d
}

func (d *DropletDriver) Create(ctx context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	size := strFromConfig(spec.Config, "size", "s-1vcpu-2gb")
	image := strFromConfig(spec.Config, "image", "ubuntu-24-04-x64")
	region := strFromConfig(spec.Config, "region", d.region)

	req := &godo.DropletCreateRequest{
		Name:       spec.Name,
		Region:     region,
		Size:       size,
		Image:      godo.DropletCreateImage{Slug: image},
		UserData:   strFromConfig(spec.Config, "user_data", ""),
		VPCUUID:    strFromConfig(spec.Config, "vpc_uuid", ""),
		Tags:       strSliceFromConfig(spec.Config, "tags"),
		Backups:    boolFromConfig(spec.Config, "enable_backups", false),
		Monitoring: boolFromConfig(spec.Config, "monitoring", false),
		IPv6:       boolFromConfig(spec.Config, "ipv6", false),
	}

	sshKeys, err := dropletSSHKeysFromConfig(spec.Config)
	if err != nil {
		return nil, fmt.Errorf("droplet create %q: %w", spec.Name, err)
	}
	req.SSHKeys = sshKeys

	volumes, err := d.resolveDropletVolumes(ctx, spec.Config, region)
	if err != nil {
		return nil, fmt.Errorf("droplet create %q: %w", spec.Name, err)
	}
	req.Volumes = volumes

	droplet, _, err := d.client.Create(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("droplet create %q: %w", spec.Name, WrapGodoError(err))
	}
	if droplet == nil || droplet.ID == 0 {
		return nil, fmt.Errorf("droplet create %q: API returned droplet with empty ID", spec.Name)
	}
	return dropletOutput(droplet), nil
}

func (d *DropletDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	id, err := providerIDToInt(ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("droplet read %q: invalid ProviderID %q: %w", ref.Name, ref.ProviderID, err)
	}
	droplet, _, err := d.client.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("droplet read %q: %w", ref.Name, WrapGodoError(err))
	}
	return dropletOutput(droplet), nil
}

func (d *DropletDriver) Update(_ context.Context, _ interfaces.ResourceRef, _ interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	return nil, fmt.Errorf("droplet: use resize action for size changes; delete and recreate for other changes")
}

func (d *DropletDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	id, err := providerIDToInt(ref.ProviderID)
	if err != nil {
		return fmt.Errorf("droplet delete %q: invalid ProviderID %q: %w", ref.Name, ref.ProviderID, err)
	}
	_, err = d.client.Delete(ctx, id)
	if err != nil {
		return fmt.Errorf("droplet delete %q: %w", ref.Name, WrapGodoError(err))
	}
	return nil
}

func (d *DropletDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	if current == nil {
		return &interfaces.DiffResult{NeedsUpdate: true}, nil
	}
	var changes []interfaces.FieldChange
	if sz := strFromConfig(desired.Config, "size", ""); sz != "" {
		if cur, _ := current.Outputs["size"].(string); cur != sz {
			changes = append(changes, interfaces.FieldChange{
				Path:     "size",
				Old:      cur,
				New:      sz,
				ForceNew: true,
			})
		}
	}
	return &interfaces.DiffResult{
		NeedsUpdate:  len(changes) > 0,
		NeedsReplace: len(changes) > 0,
		Changes:      changes,
	}, nil
}

func (d *DropletDriver) HealthCheck(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	id, err := providerIDToInt(ref.ProviderID)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	droplet, _, err := d.client.Get(ctx, id)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	healthy := droplet.Status == "active"
	return &interfaces.HealthResult{Healthy: healthy, Message: droplet.Status}, nil
}

func (d *DropletDriver) Scale(_ context.Context, _ interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return nil, fmt.Errorf("droplet does not support scale operation")
}

func dropletOutput(droplet *godo.Droplet) *interfaces.ResourceOutput {
	var publicIP string
	if ip, err := droplet.PublicIPv4(); err == nil {
		publicIP = ip
	}
	var privateIP string
	if ip, err := droplet.PrivateIPv4(); err == nil {
		privateIP = ip
	}
	return &interfaces.ResourceOutput{
		Name:       droplet.Name,
		Type:       "infra.droplet",
		ProviderID: fmt.Sprintf("%d", droplet.ID),
		Outputs: map[string]any{
			"id":         droplet.ID,
			"public_ip":  publicIP,
			"private_ip": privateIP,
			"size":       droplet.Size.Slug,
			"region":     droplet.Region.Slug,
			"status":     droplet.Status,
		},
		Status: droplet.Status,
	}
}

// providerIDToInt converts a string provider ID to int for godo Droplet API
// calls. Uses strconv.Atoi for strict whole-string parsing — partial matches
// like "123abc" are rejected. Returns an error for any non-positive-integer
// value, preventing silent API calls with droplet ID 0 or a wrong ID.
func providerIDToInt(id string) (int, error) {
	n, err := strconv.Atoi(id)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("ProviderID %q is not a valid droplet integer ID", id)
	}
	return n, nil
}

// dropletSSHKeysFromConfig converts the heterogeneous "ssh_keys" YAML value
// into a typed []godo.DropletCreateSSHKey. Each element may be either a
// fingerprint string (most common) or a numeric ID (int / int64 / float64;
// structpb collapses all numerics to float64). Mixed lists are accepted.
// Empty strings, non-positive IDs, and unsupported element types return an
// explicit error so a typo cannot silently drop an SSH key the operator
// expected to be installed.
func dropletSSHKeysFromConfig(cfg map[string]any) ([]godo.DropletCreateSSHKey, error) {
	v, ok := cfg["ssh_keys"]
	if !ok || v == nil {
		return nil, nil
	}
	raw, ok := v.([]any)
	if !ok {
		// Accept a typed []string for Go-native callers as a convenience.
		if ss, ok := v.([]string); ok {
			out := make([]godo.DropletCreateSSHKey, 0, len(ss))
			for _, s := range ss {
				if s == "" {
					return nil, fmt.Errorf("ssh_keys: empty fingerprint")
				}
				out = append(out, godo.DropletCreateSSHKey{Fingerprint: s})
			}
			return out, nil
		}
		return nil, fmt.Errorf("ssh_keys: expected list, got %T", v)
	}
	out := make([]godo.DropletCreateSSHKey, 0, len(raw))
	for i, e := range raw {
		switch t := e.(type) {
		case string:
			if t == "" {
				return nil, fmt.Errorf("ssh_keys[%d]: empty fingerprint", i)
			}
			out = append(out, godo.DropletCreateSSHKey{Fingerprint: t})
		case int:
			if t <= 0 {
				return nil, fmt.Errorf("ssh_keys[%d]: non-positive ID %d", i, t)
			}
			out = append(out, godo.DropletCreateSSHKey{ID: t})
		case int64:
			if t <= 0 {
				return nil, fmt.Errorf("ssh_keys[%d]: non-positive ID %d", i, t)
			}
			out = append(out, godo.DropletCreateSSHKey{ID: int(t)})
		case float64:
			if t != float64(int64(t)) {
				return nil, fmt.Errorf("ssh_keys[%d]: %v is not an integer", i, t)
			}
			if t <= 0 {
				return nil, fmt.Errorf("ssh_keys[%d]: non-positive ID %v", i, t)
			}
			out = append(out, godo.DropletCreateSSHKey{ID: int(t)})
		default:
			return nil, fmt.Errorf("ssh_keys[%d]: unsupported element type %T (want string fingerprint or numeric ID)", i, e)
		}
	}
	return out, nil
}

// dropletVolumesFromConfig extracts the "volumes" config entry as a strict
// []string of volume names. Unlike strSliceFromConfig (which silently drops
// non-string and empty entries), this returns an explicit error for any
// invalid element. Volume attachments are load-bearing: a typo or wrong type
// must NOT silently leave the Droplet running without an expected disk.
// Mirrors the error-message style of dropletSSHKeysFromConfig.
func dropletVolumesFromConfig(cfg map[string]any) ([]string, error) {
	v, ok := cfg["volumes"]
	if !ok || v == nil {
		return nil, nil
	}
	switch raw := v.(type) {
	case []string:
		out := make([]string, 0, len(raw))
		for i, s := range raw {
			if s == "" {
				return nil, fmt.Errorf("droplet volumes: invalid entry at index %d: expected non-empty string, got empty string", i)
			}
			out = append(out, s)
		}
		return out, nil
	case []any:
		out := make([]string, 0, len(raw))
		for i, e := range raw {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("droplet volumes: invalid entry at index %d: expected non-empty string, got %T", i, e)
			}
			if s == "" {
				return nil, fmt.Errorf("droplet volumes: invalid entry at index %d: expected non-empty string, got empty string", i)
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("droplet volumes: expected list, got %T", v)
}

// resolveDropletVolumes turns the YAML "volumes" list of NAMES into the typed
// []godo.DropletCreateVolume {ID:...} shape that godo serialises. The DO API
// requires IDs (Name is deprecated server-side per godo doc-comment), so we
// look each name up via Storage.ListVolumes and error if no match exists in
// the droplet's region. region is the droplet's resolved region; volume
// matches outside that region are rejected since DO Block Storage cannot
// cross regions.
func (d *DropletDriver) resolveDropletVolumes(ctx context.Context, cfg map[string]any, region string) ([]godo.DropletCreateVolume, error) {
	names, err := dropletVolumesFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, nil
	}
	if d.storage == nil {
		return nil, fmt.Errorf("droplet volumes: storage client not configured; cannot resolve volume names")
	}
	out := make([]godo.DropletCreateVolume, 0, len(names))
	for _, name := range names {
		params := &godo.ListVolumeParams{Name: name, Region: region}
		vols, _, err := d.storage.ListVolumes(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("droplet volumes: list %q: %w", name, WrapGodoError(err))
		}
		var matchID string
		for _, v := range vols {
			if v.Name == name {
				matchID = v.ID
				break
			}
		}
		if matchID == "" {
			return nil, fmt.Errorf("droplet volumes: volume %q not found", name)
		}
		out = append(out, godo.DropletCreateVolume{ID: matchID})
	}
	return out, nil
}

func (d *DropletDriver) SensitiveKeys() []string { return nil }

// ProviderIDFormat returns Freeform because DO Droplet IDs are integers, not
// UUIDs. We declare Freeform; providerIDToInt performs strict local validation
// and rejects any non-integer ProviderID with an explicit error before any
// API call is made — no UUID-based state-heal needed for Droplet.
func (d *DropletDriver) ProviderIDFormat() interfaces.ProviderIDFormat { return interfaces.IDFormatFreeform }
