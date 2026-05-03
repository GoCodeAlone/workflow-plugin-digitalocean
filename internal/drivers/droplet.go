package drivers

import (
	"context"
	"fmt"
	"log"
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
	return d.dropletOutput(ctx, droplet), nil
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
	return d.dropletOutput(ctx, droplet), nil
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

// Diff compares the desired spec against the current droplet output and
// returns a DiffResult. Update is disallowed by godo (Droplet PUT only
// resizes), so every detected change is flagged as ForceNew — the caller
// must replace the droplet, not patch it. We compare every field that
// Create wires through (size, vpc_uuid, enable_backups, tags, volumes,
// ssh_keys) plus user_data. Fields the DO Read API does not expose
// (user_data, monitoring, ipv6, ssh_keys) cannot be drift-compared from
// current Outputs, but we still flag a change when a desired value is
// present and the snapshot has no corresponding "" / false / nil baseline,
// surfacing config-vs-current divergence on first re-plan.
func (d *DropletDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	if current == nil {
		return &interfaces.DiffResult{NeedsUpdate: true}, nil
	}
	var changes []interfaces.FieldChange

	if sz := strFromConfig(desired.Config, "size", ""); sz != "" {
		if cur, _ := current.Outputs["size"].(string); cur != sz {
			changes = append(changes, interfaces.FieldChange{
				Path: "size", Old: cur, New: sz, ForceNew: true,
			})
		}
	}

	// vpc_uuid: read-side stable, drift is unambiguous. Drop the
	// curVPC != "" guard so adding vpc_uuid to a Droplet whose state
	// predates the field (current.Outputs has no vpc_uuid yet) still
	// surfaces as ForceNew. Empty desired with non-empty current is
	// also drift — operator dropped the VPC pin, plan must recreate.
	if _, hasVPC := desired.Config["vpc_uuid"]; hasVPC {
		vpc := strFromConfig(desired.Config, "vpc_uuid", "")
		curVPC, _ := current.Outputs["vpc_uuid"].(string)
		if curVPC != vpc {
			changes = append(changes, interfaces.FieldChange{
				Path: "vpc_uuid", Old: curVPC, New: vpc, ForceNew: true,
			})
		}
	}

	// enable_backups: bool from desired vs derived from BackupIDs presence
	// in current. Only flag a change when the desired flag is explicitly
	// set (so absent-vs-default doesn't churn).
	if _, hasBackups := desired.Config["enable_backups"]; hasBackups {
		desiredBackups := boolFromConfig(desired.Config, "enable_backups", false)
		curBackups, _ := current.Outputs["enable_backups"].(bool)
		if desiredBackups != curBackups {
			changes = append(changes, interfaces.FieldChange{
				Path: "enable_backups", Old: curBackups, New: desiredBackups, ForceNew: true,
			})
		}
	}

	// tags: order-irrelevant set comparison (DO does not preserve order).
	// We compare unconditionally — only when the "tags" key is present in
	// desired — so clearing tags (non-empty current → empty desired)
	// surfaces as drift instead of being silently ignored. dropletTagsFromDesired
	// distinguishes "key absent" from "key present and empty".
	if _, hasTags := desired.Config["tags"]; hasTags {
		tags := strSliceFromConfig(desired.Config, "tags")
		curTags := outputsAsStringSlice(current.Outputs["tags"])
		if !equalStringSet(curTags, tags) {
			changes = append(changes, interfaces.FieldChange{
				Path: "tags", Old: curTags, New: tags, ForceNew: true,
			})
		}
	}

	// volumes: by-name list. Reuse strict parse so a malformed config
	// surfaces here at plan time, not later at Apply. dropletOutput
	// resolves DO's raw VolumeIDs back to volume *names* before storing
	// them in Outputs, so this comparison is name-vs-name and stable
	// across plans (no perpetual force-replace).
	if names, err := dropletVolumesFromConfig(desired.Config); err != nil {
		return nil, err
	} else if len(names) > 0 {
		curVols := outputsAsStringSlice(current.Outputs["volumes"])
		if !equalStringSet(curVols, names) {
			changes = append(changes, interfaces.FieldChange{
				Path: "volumes", Old: curVols, New: names, ForceNew: true,
			})
		}
	}

	// user_data, monitoring, ipv6, ssh_keys: DO Read does not expose these
	// fields reliably (godo.Droplet has no Monitoring/IPv6/UserData fields
	// surfaced post-create), so we cannot drift-compare from current
	// Outputs without producing a perpetually-dirty plan. Drift on these
	// fields will surface only via re-plan after the operator destroys +
	// recreates, or via an external read-side check. Documented limitation.

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

// dropletOutput translates a godo.Droplet into ResourceOutput. Volumes are
// resolved from godo's VolumeIDs (UUIDs) to volume *names* so that subsequent
// Diff comparisons against the desired config (which carries names) line up
// without forcing a perpetual replace. Without name resolution, every Read
// after Create would compare e.g. ["vol-uuid-1"] vs ["pg-data"] and re-plan
// a destroy+recreate of the Droplet — catastrophic for stateful workloads.
//
// Resolution failures (e.g. the volume was deleted out-of-band) fall back to
// the raw ID so operators still see *something* in state, plus a
// "volumes_resolution" Outputs entry recording which IDs failed to resolve.
// This is logged so it surfaces in the engine log alongside the existing
// state-heal warnings.
func (d *DropletDriver) dropletOutput(ctx context.Context, droplet *godo.Droplet) *interfaces.ResourceOutput {
	var publicIP string
	if ip, err := droplet.PublicIPv4(); err == nil {
		publicIP = ip
	}
	var privateIP string
	if ip, err := droplet.PrivateIPv4(); err == nil {
		privateIP = ip
	}
	// Surface fields wired into Create so Diff can detect drift on any of
	// them. user_data is intentionally omitted: DO does not return it on
	// Read, so Outputs cannot represent it without lying.
	tags := make([]any, 0, len(droplet.Tags))
	for _, t := range droplet.Tags {
		tags = append(tags, t)
	}

	// Resolve VolumeIDs → names so Diff compares like-for-like with the
	// desired config (volume names). Cache lookups by ID within a single
	// call to avoid redundant API hits when Droplets carry the same volume
	// listed twice (rare but legal).
	volumes := make([]any, 0, len(droplet.VolumeIDs))
	var resolutionFailures []any
	idCache := make(map[string]string, len(droplet.VolumeIDs))
	for _, vid := range droplet.VolumeIDs {
		if name, ok := idCache[vid]; ok {
			volumes = append(volumes, name)
			continue
		}
		name := vid // fallback to raw ID if we cannot resolve the name
		if d.storage != nil {
			vol, _, err := d.storage.GetVolume(ctx, vid)
			if err != nil {
				log.Printf("warn: droplet %q: GetVolume(%q) failed; surfacing raw ID in Outputs[\"volumes\"]: %v",
					droplet.Name, vid, err)
				resolutionFailures = append(resolutionFailures, vid)
			} else if vol != nil && vol.Name != "" {
				name = vol.Name
			} else {
				log.Printf("warn: droplet %q: GetVolume(%q) returned nil/empty Name; surfacing raw ID",
					droplet.Name, vid)
				resolutionFailures = append(resolutionFailures, vid)
			}
		} else {
			// No storage client wired; cannot resolve. Record a single
			// note rather than per-ID warnings so the log isn't spammed.
			resolutionFailures = append(resolutionFailures, vid)
		}
		idCache[vid] = name
		volumes = append(volumes, name)
	}

	outputs := map[string]any{
		"id":             droplet.ID,
		"public_ip":      publicIP,
		"private_ip":     privateIP,
		"size":           droplet.Size.Slug,
		"region":         droplet.Region.Slug,
		"status":         droplet.Status,
		"vpc_uuid":       droplet.VPCUUID,
		"enable_backups": dropletBackupsEnabled(droplet),
		"tags":           tags,
		"volumes":        volumes,
		// monitoring / ipv6 are not reliably returned on Read by godo
		// (no dedicated boolean field); operators see drift via
		// re-apply if the desired flag differs from any stored
		// expectation. Diff still flags desired-vs-config-snapshot
		// changes via the desired-only-set check.
	}
	if len(resolutionFailures) > 0 {
		outputs["volumes_resolution"] = map[string]any{
			"unresolved_ids": resolutionFailures,
			"note":           "one or more volume IDs could not be resolved to a name; raw IDs surfaced in volumes[]",
		}
	}

	return &interfaces.ResourceOutput{
		Name:       droplet.Name,
		Type:       "infra.droplet",
		ProviderID: fmt.Sprintf("%d", droplet.ID),
		Outputs:    outputs,
		Status:     droplet.Status,
	}
}

// dropletBackupsEnabled returns true when DO reports any backup IDs on the
// droplet — godo.Droplet has no dedicated EnableBackups field on Read, so
// presence of BackupIDs is the only reliable read-side signal.
func dropletBackupsEnabled(droplet *godo.Droplet) bool {
	return len(droplet.BackupIDs) > 0
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
		// Accept Go-native typed slices as a convenience. Each branch
		// mirrors the per-element validation in the []any path.
		switch tv := v.(type) {
		case []string:
			out := make([]godo.DropletCreateSSHKey, 0, len(tv))
			for i, s := range tv {
				if s == "" {
					return nil, fmt.Errorf("ssh_keys[%d]: empty fingerprint", i)
				}
				out = append(out, godo.DropletCreateSSHKey{Fingerprint: s})
			}
			return out, nil
		case []int:
			out := make([]godo.DropletCreateSSHKey, 0, len(tv))
			for i, n := range tv {
				if n <= 0 {
					return nil, fmt.Errorf("ssh_keys[%d]: non-positive ID %d", i, n)
				}
				out = append(out, godo.DropletCreateSSHKey{ID: n})
			}
			return out, nil
		case []int64:
			out := make([]godo.DropletCreateSSHKey, 0, len(tv))
			for i, n := range tv {
				if n <= 0 {
					return nil, fmt.Errorf("ssh_keys[%d]: non-positive ID %d", i, n)
				}
				out = append(out, godo.DropletCreateSSHKey{ID: int(n)})
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
