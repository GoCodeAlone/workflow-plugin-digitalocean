package drivers

import (
	"context"
	"fmt"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// StorageClient is the godo Storage interface (for mocking). Subset of
// godo.StorageService — only the methods VolumeDriver and DropletDriver
// (volumes-by-name resolution) need.
type StorageClient interface {
	CreateVolume(ctx context.Context, req *godo.VolumeCreateRequest) (*godo.Volume, *godo.Response, error)
	GetVolume(ctx context.Context, volumeID string) (*godo.Volume, *godo.Response, error)
	DeleteVolume(ctx context.Context, volumeID string) (*godo.Response, error)
	ListVolumes(ctx context.Context, params *godo.ListVolumeParams) ([]godo.Volume, *godo.Response, error)
}

// StorageActionsClient is the subset of godo.StorageActionsService used by
// VolumeDriver to execute resize actions (the only mutation DO supports on
// an existing volume without delete + recreate).
type StorageActionsClient interface {
	Resize(ctx context.Context, volumeID string, sizeGigabytes int, regionSlug string) (*godo.Action, *godo.Response, error)
}

// VolumeDriver manages DigitalOcean Block Storage volumes (infra.volume).
//
// Update semantics: only size GROWTH is supported in-place via
// StorageActions.Resize. Size shrinks are unsupported by DO; any other
// attribute change (region, filesystem_type) forces replace via Diff.
type VolumeDriver struct {
	client  StorageClient
	actions StorageActionsClient
	region  string
}

// NewVolumeDriver creates a VolumeDriver backed by a real godo client.
func NewVolumeDriver(c *godo.Client, region string) *VolumeDriver {
	return &VolumeDriver{client: c.Storage, actions: c.StorageActions, region: region}
}

// NewVolumeDriverWithClient creates a driver with injected clients (for tests).
// The actions client may be nil for tests that do not exercise the resize
// path.
func NewVolumeDriverWithClient(c StorageClient, actions StorageActionsClient, region string) *VolumeDriver {
	return &VolumeDriver{client: c, actions: actions, region: region}
}

func (d *VolumeDriver) Create(ctx context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	region := strFromConfig(spec.Config, "region", d.region)
	sizeGB, _, err := intStrictFromConfig(spec.Config, "size_gb", "volume size_gb", 0)
	if err != nil {
		return nil, fmt.Errorf("volume create %q: %w", spec.Name, err)
	}
	if sizeGB <= 0 {
		return nil, fmt.Errorf("volume create %q: size_gb is required and must be > 0", spec.Name)
	}
	req := &godo.VolumeCreateRequest{
		Region:         region,
		Name:           spec.Name,
		SizeGigaBytes:  int64(sizeGB),
		Description:    strFromConfig(spec.Config, "description", ""),
		FilesystemType: strFromConfig(spec.Config, "filesystem_type", ""),
		Tags:           strSliceFromConfig(spec.Config, "tags"),
	}
	vol, _, err := d.client.CreateVolume(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("volume create %q: %w", spec.Name, WrapGodoError(err))
	}
	if vol == nil || vol.ID == "" {
		return nil, fmt.Errorf("volume create %q: API returned volume with empty ID", spec.Name)
	}
	return volumeOutput(vol), nil
}

func (d *VolumeDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	if ref.ProviderID == "" {
		return nil, fmt.Errorf("volume read %q: empty ProviderID", ref.Name)
	}
	vol, _, err := d.client.GetVolume(ctx, ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("volume read %q: %w", ref.Name, WrapGodoError(err))
	}
	return volumeOutput(vol), nil
}

func (d *VolumeDriver) Update(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	if ref.ProviderID == "" {
		return nil, fmt.Errorf("volume update %q: empty ProviderID", ref.Name)
	}
	desiredSize, _, err := intStrictFromConfig(spec.Config, "size_gb", "volume size_gb", 0)
	if err != nil {
		return nil, fmt.Errorf("volume update %q: %w", ref.Name, err)
	}
	if desiredSize <= 0 {
		return nil, fmt.Errorf("volume update %q: size_gb is required and must be > 0", ref.Name)
	}
	cur, _, err := d.client.GetVolume(ctx, ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("volume update %q: read current: %w", ref.Name, WrapGodoError(err))
	}
	if int64(desiredSize) == cur.SizeGigaBytes {
		// No-op update — return current state.
		return volumeOutput(cur), nil
	}
	if int64(desiredSize) < cur.SizeGigaBytes {
		return nil, fmt.Errorf("volume update %q: shrinking from %d to %d GB is not supported by DO; replace required",
			ref.Name, cur.SizeGigaBytes, desiredSize)
	}
	if d.actions == nil {
		return nil, fmt.Errorf("volume update %q: storage actions client not configured; cannot resize", ref.Name)
	}
	region := cur.Region.Slug
	if _, _, err := d.actions.Resize(ctx, ref.ProviderID, desiredSize, region); err != nil {
		return nil, fmt.Errorf("volume update %q: resize to %d GB: %w", ref.Name, desiredSize, WrapGodoError(err))
	}
	// Re-read to surface the post-resize state.
	updated, _, err := d.client.GetVolume(ctx, ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("volume update %q: read after resize: %w", ref.Name, WrapGodoError(err))
	}
	return volumeOutput(updated), nil
}

func (d *VolumeDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	if ref.ProviderID == "" {
		return fmt.Errorf("volume delete %q: empty ProviderID", ref.Name)
	}
	if _, err := d.client.DeleteVolume(ctx, ref.ProviderID); err != nil {
		return fmt.Errorf("volume delete %q: %w", ref.Name, WrapGodoError(err))
	}
	return nil
}

func (d *VolumeDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	if current == nil {
		return &interfaces.DiffResult{NeedsUpdate: true}, nil
	}
	var changes []interfaces.FieldChange
	var needsReplace bool

	desiredSize, sizePresent, err := intStrictFromConfig(desired.Config, "size_gb", "volume size_gb", 0)
	if err != nil {
		return nil, err
	}
	if sizePresent && desiredSize > 0 {
		curSize := outputsAsInt(current.Outputs["size_gb"])
		if curSize != desiredSize {
			// Growth = in-place resize; shrink = replace (DO has no shrink API).
			fc := interfaces.FieldChange{
				Path: "size_gb",
				Old:  curSize,
				New:  desiredSize,
			}
			if desiredSize < curSize {
				fc.ForceNew = true
				needsReplace = true
			}
			changes = append(changes, fc)
		}
	}

	if region := strFromConfig(desired.Config, "region", ""); region != "" {
		curRegion, _ := current.Outputs["region"].(string)
		if curRegion != region {
			changes = append(changes, interfaces.FieldChange{
				Path: "region", Old: curRegion, New: region, ForceNew: true,
			})
			needsReplace = true
		}
	}

	// filesystem_type: any change forces replace (DO has no in-place
	// reformat). Drop both empty-side guards so transitions raw↔ext4
	// (empty→non-empty or vice versa) surface as drift, not silently
	// ignored. We compare unconditionally when the desired key is present;
	// absent desired means "operator did not opt in" and we leave current
	// alone for backwards compat.
	if _, hasFS := desired.Config["filesystem_type"]; hasFS {
		fs := strFromConfig(desired.Config, "filesystem_type", "")
		curFS, _ := current.Outputs["filesystem_type"].(string)
		if curFS != fs {
			changes = append(changes, interfaces.FieldChange{
				Path: "filesystem_type", Old: curFS, New: fs, ForceNew: true,
			})
			needsReplace = true
		}
	}

	// description and tags: DO Block Storage does NOT expose update endpoints
	// for these (godo.StorageActions only supports Resize; the Volume API
	// itself sets description/tags at creation time only). Treat any change
	// as ForceNew so drift surfaces as a planned replace rather than being
	// silently ignored.
	//
	// Use "key present" rather than "non-empty" so add-from-empty (no desc
	// → "Postgres data volume") and clear-to-empty ("audit log" → "")
	// transitions both surface as drift. Absent key still skips (backwards-
	// compat for YAML predating the field).
	if _, hasDesc := desired.Config["description"]; hasDesc {
		desc := strFromConfig(desired.Config, "description", "")
		curDesc, _ := current.Outputs["description"].(string)
		if curDesc != desc {
			changes = append(changes, interfaces.FieldChange{
				Path: "description", Old: curDesc, New: desc, ForceNew: true,
			})
			needsReplace = true
		}
	}

	// Use "key present in desired" so clearing tags ([prod] -> []) surfaces
	// as drift instead of being silently ignored. Same rationale as the
	// description guard above.
	if _, hasTags := desired.Config["tags"]; hasTags {
		tags := strSliceFromConfig(desired.Config, "tags")
		curTags := outputsAsStringSlice(current.Outputs["tags"])
		// equalStringSet (firewall.go) treats order-irrelevant — DO does not
		// preserve tag order across reads, so reorders must NOT trigger
		// replace.
		if !equalStringSet(curTags, tags) {
			changes = append(changes, interfaces.FieldChange{
				Path: "tags", Old: curTags, New: tags, ForceNew: true,
			})
			needsReplace = true
		}
	}

	return &interfaces.DiffResult{
		NeedsUpdate:  len(changes) > 0,
		NeedsReplace: needsReplace,
		Changes:      changes,
	}, nil
}

func (d *VolumeDriver) HealthCheck(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	if ref.ProviderID == "" {
		return &interfaces.HealthResult{Healthy: false, Message: "empty ProviderID"}, nil
	}
	vol, _, err := d.client.GetVolume(ctx, ref.ProviderID)
	if err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	// godo.Volume has no Status field; a successful Read on a real volume is
	// the strongest health signal the DO API exposes here. Surface size and
	// region in the message so operators can spot a misconfigured pair.
	msg := fmt.Sprintf("available (%d GB, region=%s)", vol.SizeGigaBytes, regionSlug(vol.Region))
	return &interfaces.HealthResult{Healthy: true, Message: msg}, nil
}

func (d *VolumeDriver) Scale(_ context.Context, _ interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return nil, fmt.Errorf("volume does not support scale operation; change size_gb and re-apply")
}

func (d *VolumeDriver) SensitiveKeys() []string { return nil }

// ProviderIDFormat is UUID — DO Block Storage volume IDs are UUIDs.
func (d *VolumeDriver) ProviderIDFormat() interfaces.ProviderIDFormat {
	return interfaces.IDFormatUUID
}

func volumeOutput(vol *godo.Volume) *interfaces.ResourceOutput {
	// tags: copy to a fresh []any slice so structpb round-trips don't
	// mutate godo's internal slice; Diff also reads via outputsAsStringSlice
	// which accepts both []string and []any shapes.
	tags := make([]any, 0, len(vol.Tags))
	for _, t := range vol.Tags {
		tags = append(tags, t)
	}
	return &interfaces.ResourceOutput{
		Name:       vol.Name,
		Type:       "infra.volume",
		ProviderID: vol.ID,
		Outputs: map[string]any{
			"id":              vol.ID,
			"name":            vol.Name,
			"region":          regionSlug(vol.Region),
			"size_gb":         float64(vol.SizeGigaBytes),
			"filesystem_type": vol.FilesystemType,
			"description":     vol.Description,
			"tags":            tags,
		},
		// godo.Volume exposes no Status field; report a stable string so
		// downstream callers don't read empty Status as "unknown failure".
		Status: "available",
	}
}


// regionSlug returns r.Slug for a non-nil region, "" otherwise. Centralised
// so volume helpers don't repeat the nil-guard.
func regionSlug(r *godo.Region) string {
	if r == nil {
		return ""
	}
	return r.Slug
}

// outputsAsInt converts an Outputs value (which may be int, int64, or
// float64 after a structpb round-trip) back to int. Returns 0 for missing
// or unparseable values.
func outputsAsInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	}
	return 0
}
