package internal

import (
	"context"
	"errors"
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
)

// TestDOProvider_Apply_ReturnsRemovedError pins the Phase 3 cleanup
// invariant: DOProvider.Apply is dead code post-Phase-2.5 v2 cutover;
// reaching it indicates a misconfigured caller. Method returns sentinel
// ErrApplyV1Removed; callers can errors.Is for diagnostic classification.
// Per workflow#695 Phase 3 cleanup + docs/plans/2026-05-17-phase2.5-cleanup-bundle.md.
func TestDOProvider_Apply_ReturnsRemovedError(t *testing.T) {
	p := &DOProvider{}
	_, err := p.Apply(context.Background(), &interfaces.IaCPlan{})
	if !errors.Is(err, ErrApplyV1Removed) {
		t.Errorf("expected ErrApplyV1Removed; got: %v", err)
	}
}
