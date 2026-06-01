package internal

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

const (
	ownershipTagPrefix = "workflow-owner:"
	ownershipTagSource = "tag:workflow-owner"
	ownershipTagMaxLen = 255
)

var _ interfaces.OwnershipProvider = (*DOProvider)(nil)

func (p *DOProvider) GetOwner(ctx context.Context, ref interfaces.ResourceRef) (*interfaces.ResourceOwner, error) {
	if p.client == nil {
		return nil, fmt.Errorf("digitalocean: GetOwner called on provider that is not initialized — call Initialize first")
	}
	tags, err := p.resourceTags(ctx, ref)
	if err != nil {
		return nil, err
	}
	for _, tag := range tags {
		owner, ok := ownerFromTag(tag)
		if ok {
			return &interfaces.ResourceOwner{Ref: ref, Owner: owner, Source: ownershipTagSource}, nil
		}
	}
	return &interfaces.ResourceOwner{Ref: ref, Source: ownershipTagSource}, nil
}

func (p *DOProvider) SetOwner(ctx context.Context, ref interfaces.ResourceRef, owner string) error {
	if p.client == nil {
		return fmt.Errorf("digitalocean: SetOwner called on provider that is not initialized — call Initialize first")
	}
	resource, err := ownershipTagResource(ref)
	if err != nil {
		return err
	}
	tag, err := ownerTagName(owner)
	if err != nil {
		return err
	}

	tags, err := p.resourceTags(ctx, ref)
	if err != nil {
		return err
	}
	for _, existing := range tags {
		if _, ok := ownerFromTag(existing); !ok || existing == tag {
			continue
		}
		if _, err := p.client.Tags.UntagResources(ctx, existing, &godo.UntagResourcesRequest{Resources: []godo.Resource{resource}}); err != nil {
			return fmt.Errorf("digitalocean: remove owner tag %q from %s/%s: %w", existing, ref.Type, ref.Name, drivers.WrapGodoError(err))
		}
	}
	if _, _, err := p.client.Tags.Create(ctx, &godo.TagCreateRequest{Name: tag}); err != nil && !isTagAlreadyExists(err) {
		return fmt.Errorf("digitalocean: create owner tag %q: %w", tag, drivers.WrapGodoError(err))
	}
	if _, err := p.client.Tags.TagResources(ctx, tag, &godo.TagResourcesRequest{Resources: []godo.Resource{resource}}); err != nil {
		return fmt.Errorf("digitalocean: tag %s/%s with owner %q: %w", ref.Type, ref.Name, owner, drivers.WrapGodoError(err))
	}
	return nil
}

func (p *DOProvider) ListOwners(ctx context.Context, filter interfaces.OwnerFilter) ([]interfaces.ResourceOwner, error) {
	if p.client == nil {
		return nil, fmt.Errorf("digitalocean: ListOwners called on provider that is not initialized — call Initialize first")
	}
	if filter.Owner != "" {
		return p.listOwnersForTag(ctx, filter)
	}

	var out []interfaces.ResourceOwner
	page := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		tags, resp, err := p.client.Tags.List(ctx, page)
		if err != nil {
			return nil, fmt.Errorf("digitalocean: list owner tags: %w", drivers.WrapGodoError(err))
		}
		for _, tag := range tags {
			owner, ok := ownerFromTag(tag.Name)
			if !ok {
				continue
			}
			owners, err := p.listOwnersForTag(ctx, interfaces.OwnerFilter{Owner: owner, ResourceType: filter.ResourceType})
			if err != nil {
				return nil, err
			}
			out = append(out, owners...)
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		nextPage, err := resp.Links.CurrentPage()
		if err != nil {
			return nil, fmt.Errorf("digitalocean: paginate owner tags: %w", err)
		}
		page.Page = nextPage + 1
	}
	return out, nil
}

func (p *DOProvider) listOwnersForTag(ctx context.Context, filter interfaces.OwnerFilter) ([]interfaces.ResourceOwner, error) {
	tag, err := ownerTagName(filter.Owner)
	if err != nil {
		return nil, err
	}
	refs, err := p.EnumerateByTag(ctx, tag)
	if err != nil {
		return nil, err
	}
	out := make([]interfaces.ResourceOwner, 0, len(refs))
	for _, ref := range refs {
		if filter.ResourceType != "" && ref.Type != filter.ResourceType {
			continue
		}
		out = append(out, interfaces.ResourceOwner{Ref: ref, Owner: filter.Owner, Source: ownershipTagSource})
	}
	return out, nil
}

func (p *DOProvider) resourceTags(ctx context.Context, ref interfaces.ResourceRef) ([]string, error) {
	if _, err := ownershipTagResource(ref); err != nil {
		return nil, err
	}
	driver, err := p.ResourceDriver(ref.Type)
	if err != nil {
		return nil, err
	}
	out, err := driver.Read(ctx, ref)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, nil
	}
	return stringSliceFromAny(out.Outputs["tags"]), nil
}

func ownershipTagResource(ref interfaces.ResourceRef) (godo.Resource, error) {
	if ref.ProviderID == "" {
		return godo.Resource{}, fmt.Errorf("digitalocean: ownership requires provider id for %s/%s", ref.Type, ref.Name)
	}
	switch ref.Type {
	case "infra.droplet":
		return godo.Resource{ID: ref.ProviderID, Type: godo.DropletResourceType}, nil
	case "infra.volume":
		return godo.Resource{ID: ref.ProviderID, Type: godo.VolumeResourceType}, nil
	case "infra.database", "infra.cache":
		return godo.Resource{ID: ref.ProviderID, Type: godo.DatabaseResourceType}, nil
	default:
		return godo.Resource{}, fmt.Errorf("digitalocean: ownership unsupported for %s: %w", ref.Type, interfaces.ErrProviderMethodUnimplemented)
	}
}

func ownerTagName(owner string) (string, error) {
	if owner == "" {
		return "", fmt.Errorf("digitalocean: owner must be non-empty")
	}
	suffix := owner
	if !isPlainOwnerSuffix(owner) {
		suffix = "b64:" + base64.RawURLEncoding.EncodeToString([]byte(owner))
	}
	tag := ownershipTagPrefix + suffix
	if len(tag) > ownershipTagMaxLen {
		return "", fmt.Errorf("digitalocean: owner tag exceeds %d characters", ownershipTagMaxLen)
	}
	return tag, nil
}

func ownerFromTag(tag string) (string, bool) {
	suffix, ok := strings.CutPrefix(tag, ownershipTagPrefix)
	if !ok || suffix == "" {
		return "", false
	}
	if encoded, ok := strings.CutPrefix(suffix, "b64:"); ok {
		raw, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			return "", false
		}
		return string(raw), true
	}
	return suffix, true
}

func isPlainOwnerSuffix(owner string) bool {
	if strings.HasPrefix(owner, "b64:") {
		return false
	}
	for _, r := range owner {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == ':' || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func isTagAlreadyExists(err error) bool {
	var doErr *godo.ErrorResponse
	return errors.As(err, &doErr) && doErr.Response != nil && doErr.Response.StatusCode == 422
}

func stringSliceFromAny(v any) []string {
	switch tags := v.(type) {
	case []string:
		return append([]string(nil), tags...)
	case []any:
		out := make([]string, 0, len(tags))
		for _, tag := range tags {
			if s, ok := tag.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
