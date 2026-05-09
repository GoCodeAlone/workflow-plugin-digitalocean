// Package drivers — SpacesKeyDriver provisions DigitalOcean Spaces access
// keys (resource type `infra.spaces_key`) via godo's SpacesKeysService.
//
// Engine-routing pattern (workflow v0.27.0+):
//
//   - Create returns ResourceOutput.Outputs containing every field including
//     access_key and secret_key, and flags both as Sensitive via
//     ResourceOutput.Sensitive[k]==true. The workflow engine's iac/sensitive
//     package routes those flagged values through the configured
//     secrets.Provider (keyed `<resource>_<output>`) and replaces them with
//     `secret_ref://...` placeholders before persisting state. The driver
//     itself NEVER touches secrets.Provider — it stays platform-agnostic.
//
//   - Read returns Outputs WITHOUT secret_key (the DO API doesn't return it
//     on List/Get; the secret value lives in the secrets.Provider after
//     Create routed it). Audit/refresh paths reconcile against access_key,
//     name, created_at, grants — the only fields the API can re-read.
//
//   - Diff compares mutable fields (grants). Spaces keys can't be renamed
//     server-side, so name divergence sets NeedsReplace=true.
//
//   - SensitiveKeys() returns access_key + secret_key for plan/diff display
//     masking; this is independent from the per-call routing trigger above.
//
// ProviderID == access_key — matches the sister bucket driver
// (internal/drivers/spaces.go) and the EnumerateAll path in
// internal/provider.go::enumerateAllSpacesKeys. Per ADR 0015/0020.
package drivers

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// spacesKeysAPI is the subset of godo.SpacesKeysService that this driver
// uses. Extracted as an interface so tests can inject a fake without
// constructing a full *godo.Client.
type spacesKeysAPI interface {
	Create(context.Context, *godo.SpacesKeyCreateRequest) (*godo.SpacesKey, *godo.Response, error)
	Get(context.Context, string) (*godo.SpacesKey, *godo.Response, error)
	List(context.Context, *godo.ListOptions) ([]*godo.SpacesKey, *godo.Response, error)
	Update(context.Context, string, *godo.SpacesKeyUpdateRequest) (*godo.SpacesKey, *godo.Response, error)
	Delete(context.Context, string) (*godo.Response, error)
}

// SpacesKeyDriver manages DigitalOcean Spaces access keys (infra.spaces_key).
// Constructed with a *godo.Client only — the secrets.Provider is the
// engine's concern under the v0.27.0 engine-side routing contract.
type SpacesKeyDriver struct {
	client spacesKeysAPI
}

// NewSpacesKeyDriver returns a driver bound to the given godo client.
func NewSpacesKeyDriver(client *godo.Client) *SpacesKeyDriver {
	return &SpacesKeyDriver{client: client.SpacesKeys}
}

// ResourceType is the canonical resource-type string this driver serves.
func (d *SpacesKeyDriver) ResourceType() string { return "infra.spaces_key" }

// SensitiveKeys returns the output keys whose values should be masked in
// plan/diff display. The engine ALSO consults ResourceOutput.Sensitive
// per-call to decide routing through secrets.Provider; SensitiveKeys is
// strictly the display-side signal.
func (d *SpacesKeyDriver) SensitiveKeys() []string {
	return []string{"access_key", "secret_key"}
}

// ProviderIDFormat declares ProviderIDs are freeform (DO access keys are
// not UUIDs). Disables UUID validation while still enforcing non-empty.
func (d *SpacesKeyDriver) ProviderIDFormat() interfaces.ProviderIDFormat {
	return interfaces.IDFormatFreeform
}

// Create provisions a new Spaces key and returns its full outputs flagged
// for engine-side sensitive routing.
func (d *SpacesKeyDriver) Create(ctx context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	req, err := spacesKeyCreateRequest(spec)
	if err != nil {
		return nil, fmt.Errorf("spaces_key create %q: %w", spec.Name, err)
	}
	key, _, err := d.client.Create(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("spaces_key create %q: %w", spec.Name, WrapGodoError(err))
	}
	if key == nil {
		return nil, fmt.Errorf("spaces_key create %q: godo returned nil key", spec.Name)
	}
	return spacesKeyOutput(key, /*includeSecret*/ true), nil
}

// Read refreshes metadata for an existing Spaces key. secret_key is NOT
// returned (the DO API only exposes it on Create); the cached value lives
// in the secrets.Provider after Create routed it.
func (d *SpacesKeyDriver) Read(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOutput, error) {
	if ref.ProviderID == "" {
		// Name-only fallback for upsert / state-heal: enumerate and match
		// by name. Spaces keys have no native name lookup.
		key, err := d.findByName(ctx, ref.Name)
		if err != nil {
			return nil, fmt.Errorf("spaces_key read %q: %w", ref.Name, err)
		}
		return spacesKeyOutput(key, /*includeSecret*/ false), nil
	}
	key, _, err := d.client.Get(ctx, ref.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("spaces_key read %q: %w", ref.Name, WrapGodoError(err))
	}
	if key == nil {
		return nil, fmt.Errorf("spaces_key read %q: godo returned nil key", ref.Name)
	}
	return spacesKeyOutput(key, /*includeSecret*/ false), nil
}

// SupportsUpsert advertises that Read can locate by Name when ProviderID
// is empty, so wfctl ApplyPlan can recover from ErrResourceAlreadyExists.
func (d *SpacesKeyDriver) SupportsUpsert() bool { return true }

// Update mutates a Spaces key in place. The DO API only allows changing
// grants and name — name change is treated as in-place rename via
// SpacesKeyUpdateRequest.
func (d *SpacesKeyDriver) Update(ctx context.Context, ref interfaces.ResourceRef, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	grants, err := grantsFromConfig(spec.Config)
	if err != nil {
		return nil, fmt.Errorf("spaces_key update %q: %w", ref.Name, err)
	}
	name := strFromConfig(spec.Config, "name", spec.Name)
	if ref.ProviderID == "" {
		return nil, fmt.Errorf("spaces_key update %q: ProviderID is required", ref.Name)
	}
	key, _, err := d.client.Update(ctx, ref.ProviderID, &godo.SpacesKeyUpdateRequest{
		Name:   name,
		Grants: grants,
	})
	if err != nil {
		return nil, fmt.Errorf("spaces_key update %q: %w", ref.Name, WrapGodoError(err))
	}
	if key == nil {
		return nil, fmt.Errorf("spaces_key update %q: godo returned nil key", ref.Name)
	}
	// secret_key is NOT returned by Update — only Create. Mirror Read's shape.
	return spacesKeyOutput(key, /*includeSecret*/ false), nil
}

// Delete removes the Spaces key by access_key. 404 is treated as success
// (idempotent delete) — sister provider RevokeProviderCredential takes
// the same stance.
func (d *SpacesKeyDriver) Delete(ctx context.Context, ref interfaces.ResourceRef) error {
	if ref.ProviderID == "" {
		return fmt.Errorf("spaces_key delete %q: ProviderID is required", ref.Name)
	}
	resp, err := d.client.Delete(ctx, ref.ProviderID)
	if err != nil {
		if resp != nil && resp.StatusCode == 404 {
			return nil
		}
		return fmt.Errorf("spaces_key delete %q: %w", ref.Name, WrapGodoError(err))
	}
	return nil
}

// Diff compares desired spec against current state. Spaces keys support
// in-place rename + grant updates; both go via Update (NeedsUpdate=true),
// no replace path.
func (d *SpacesKeyDriver) Diff(_ context.Context, desired interfaces.ResourceSpec, current *interfaces.ResourceOutput) (*interfaces.DiffResult, error) {
	if current == nil {
		return &interfaces.DiffResult{NeedsUpdate: true}, nil
	}
	desiredGrants, err := grantsFromConfig(desired.Config)
	if err != nil {
		return nil, fmt.Errorf("spaces_key diff %q: %w", desired.Name, err)
	}
	desiredName := strFromConfig(desired.Config, "name", desired.Name)
	curName, _ := current.Outputs["name"].(string)

	var changes []interfaces.FieldChange
	if curName != desiredName {
		changes = append(changes, interfaces.FieldChange{
			Path: "name", Old: curName, New: desiredName,
		})
	}

	desiredGrantsMap := grantsToMaps(desiredGrants)
	curGrants := normalizeGrantsForDiff(current.Outputs["grants"])
	if !reflect.DeepEqual(curGrants, desiredGrantsMap) {
		changes = append(changes, interfaces.FieldChange{
			Path: "grants", Old: curGrants, New: desiredGrantsMap,
		})
	}
	return &interfaces.DiffResult{NeedsUpdate: len(changes) > 0, Changes: changes}, nil
}

// HealthCheck has no provider-side concept for Spaces keys; report healthy
// if the key still exists. Errors here are non-fatal at the call site.
func (d *SpacesKeyDriver) HealthCheck(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.HealthResult, error) {
	if ref.ProviderID == "" {
		return &interfaces.HealthResult{Healthy: false, Message: "ProviderID empty"}, nil
	}
	if _, _, err := d.client.Get(ctx, ref.ProviderID); err != nil {
		return &interfaces.HealthResult{Healthy: false, Message: err.Error()}, nil
	}
	return &interfaces.HealthResult{Healthy: true}, nil
}

// Scale is not supported (access keys have no replica concept).
func (d *SpacesKeyDriver) Scale(_ context.Context, _ interfaces.ResourceRef, _ int) (*interfaces.ResourceOutput, error) {
	return nil, errors.New("spaces_key does not support scale operation")
}

// findByName paginates SpacesKeys.List looking for an entry with the given
// Name. Used by Read when ProviderID is empty (name-only upsert path).
func (d *SpacesKeyDriver) findByName(ctx context.Context, name string) (*godo.SpacesKey, error) {
	opt := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		keys, resp, err := d.client.List(ctx, opt)
		if err != nil {
			return nil, fmt.Errorf("list spaces_keys page=%d: %w", opt.Page, WrapGodoError(err))
		}
		for _, k := range keys {
			if k != nil && k.Name == name {
				return k, nil
			}
		}
		if resp == nil || resp.Links == nil || resp.Links.Pages == nil || resp.Links.Pages.Next == "" {
			break
		}
		page, err := resp.Links.CurrentPage()
		if err != nil {
			return nil, fmt.Errorf("list spaces_keys: parse next page: %w", err)
		}
		opt.Page = page + 1
	}
	return nil, fmt.Errorf("%w: spaces_key %q", interfaces.ErrResourceNotFound, name)
}

// spacesKeyCreateRequest builds godo.SpacesKeyCreateRequest from spec.Config.
func spacesKeyCreateRequest(spec interfaces.ResourceSpec) (*godo.SpacesKeyCreateRequest, error) {
	name := strFromConfig(spec.Config, "name", spec.Name)
	if name == "" {
		return nil, fmt.Errorf("spec.Config[\"name\"] (or spec.Name) is required")
	}
	grants, err := grantsFromConfig(spec.Config)
	if err != nil {
		return nil, err
	}
	return &godo.SpacesKeyCreateRequest{Name: name, Grants: grants}, nil
}

// grantsFromConfig parses spec.Config["grants"] into []*godo.Grant. Accepts
// both raw map shape (from YAML/JSON) and pre-typed []*godo.Grant.
//
// Each grant entry must carry a non-empty `permission`; `bucket` is
// optional (empty bucket means "all buckets" — DO's account-wide grant).
func grantsFromConfig(config map[string]any) ([]*godo.Grant, error) {
	raw, ok := config["grants"]
	if !ok || raw == nil {
		return nil, nil
	}
	switch v := raw.(type) {
	case []*godo.Grant:
		return v, nil
	case []any:
		out := make([]*godo.Grant, 0, len(v))
		for i, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("grants[%d]: expected object, got %T", i, item)
			}
			perm, _ := m["permission"].(string)
			if perm == "" {
				return nil, fmt.Errorf("grants[%d]: permission is required", i)
			}
			bucket, _ := m["bucket"].(string)
			out = append(out, &godo.Grant{
				Bucket:     bucket,
				Permission: godo.SpacesKeyPermission(perm),
			})
		}
		return out, nil
	default:
		return nil, fmt.Errorf("grants: expected array, got %T", raw)
	}
}

// spacesKeyOutput renders the standard ResourceOutput for a *godo.SpacesKey.
// includeSecret controls whether secret_key is populated (true on Create,
// false on Read/Update where the API doesn't return it).
func spacesKeyOutput(k *godo.SpacesKey, includeSecret bool) *interfaces.ResourceOutput {
	outputs := map[string]any{
		"name":       k.Name,
		"access_key": k.AccessKey,
		"created_at": k.CreatedAt,
		"grants":     grantsToMaps(k.Grants),
	}
	sensitive := map[string]bool{
		"access_key": true,
	}
	if includeSecret {
		outputs["secret_key"] = k.SecretKey
		sensitive["secret_key"] = true
	}
	return &interfaces.ResourceOutput{
		Name:       k.Name,
		Type:       "infra.spaces_key",
		ProviderID: k.AccessKey,
		Outputs:    outputs,
		Sensitive:  sensitive,
		Status:     "active",
	}
}

// normalizeGrantsForDiff coerces a stored Outputs["grants"] value (which
// can round-trip as []map[string]any in-process or []any of map[string]any
// after JSON decode) to the canonical []map[string]any shape produced by
// grantsToMaps. Returns nil when the input is nil or empty so DeepEqual
// against grantsToMaps's nil-on-empty result succeeds symmetrically.
func normalizeGrantsForDiff(v any) []map[string]any {
	switch t := v.(type) {
	case nil:
		return nil
	case []map[string]any:
		if len(t) == 0 {
			return nil
		}
		return t
	case []any:
		if len(t) == 0 {
			return nil
		}
		out := make([]map[string]any, 0, len(t))
		for _, item := range t {
			if m, ok := item.(map[string]any); ok {
				out = append(out, map[string]any{
					"bucket":     stringOr(m["bucket"], ""),
					"permission": stringOr(m["permission"], ""),
				})
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return nil
}

// stringOr coerces v to string or returns def. Used in
// normalizeGrantsForDiff so JSON-round-tripped state values match the
// canonical map shape produced by grantsToMaps.
func stringOr(v any, def string) string {
	if s, ok := v.(string); ok {
		return s
	}
	return def
}

// grantsToMaps converts a []*godo.Grant to []map[string]any so it can live
// in ResourceOutput.Outputs (which is map[string]any, not a typed grants
// slice). Returns nil for empty/nil input so the Outputs entry is
// omitted-rather-than-empty. Mirrors the helper of the same name in
// internal/provider.go (kept package-local here to avoid a tight coupling
// from drivers up to the parent internal package).
func grantsToMaps(grants []*godo.Grant) []map[string]any {
	if len(grants) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(grants))
	for _, g := range grants {
		if g == nil {
			continue
		}
		out = append(out, map[string]any{
			"bucket":     g.Bucket,
			"permission": string(g.Permission),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
