package drivers

import (
	"context"
	"fmt"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// verifyImagePresentInDOCR is a conservative pre-flight that returns an
// interfaces.ErrImageNotInRegistry-wrapping error when imageRef is a DOCR
// reference whose tag is absent from the registry. For non-DOCR refs,
// unparseable refs, or DOCR API errors, it returns nil — the apply path
// will surface any real issue via godo apps.update's own error.
//
// The conservative behavior is intentional: false-negatives (mistakenly
// flagging a present image as missing) would block valid deploys; better
// to let the real apply error surface.
func verifyImagePresentInDOCR(ctx context.Context, regClient RegistryClient, imageRef string) error {
	// ParseImageRef handles the DOCR convention: for
	// "registry.digitalocean.com/<reg>/<repo>:<tag>" the middle path segment
	// (registry name) is discarded and spec.Repository is the FINAL path
	// segment (e.g. "core-dump-server"), which is exactly what
	// RegistryService.ListRepositoryTags expects as its repository arg.
	// Registry name is fetched separately via RegistryService.Get below.
	spec, err := ParseImageRef(imageRef)
	if err != nil || spec == nil {
		return nil // unparseable: skip
	}
	if spec.RegistryType != godo.ImageSourceSpecRegistryType_DOCR {
		return nil // only DOCR is checked
	}
	reg, _, err := regClient.Get(ctx)
	if err != nil || reg == nil || reg.Name == "" {
		return nil // conservative: API failure -> let apply surface it
	}
	// Paginate through tag list. DOCR returns at most ~200 tags per page.
	opts := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		tags, resp, err := regClient.ListRepositoryTags(ctx, reg.Name, spec.Repository, opts)
		if err != nil {
			return nil // conservative
		}
		for _, t := range tags {
			if t != nil && t.Tag == spec.Tag {
				return nil
			}
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opts.Page++
		if opts.Page > 10 {
			// Safety cap: 10 pages * 200/page = 2k tags. Beyond this, fail-open
			// (any real DOCR repo holds far fewer; cap protects against an
			// accidentally infinite loop from a misbehaving Links header).
			return nil
		}
	}
	return fmt.Errorf("image %q not found in DOCR repo %q: %w",
		imageRef, spec.Repository, interfaces.ErrImageNotInRegistry)
}
