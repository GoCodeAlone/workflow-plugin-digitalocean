//go:build conformance

// Package internal — DO provider conformance test.
//
// This file lives behind the `conformance` build tag so it is excluded
// from the default `go test ./...` (which runs the existing per-package
// unit tests). Opt in with:
//
//	GOWORK=off go test -tags=conformance ./internal/... -run TestConformance
//
// The conformance suite (github.com/GoCodeAlone/workflow/iac/conformance)
// dispatches a fixed set of provider-portable scenarios; each scenario
// independently decides whether it can run against a fresh provider
// (some skip when an opt-in driver type like infra.compute is absent —
// DO does not expose infra.compute, so the upsert-on-already-exists
// scenario will skip on DO).
//
// Two opt-in env vars gate the heavier scenarios:
//
//   - CONFORMANCE_LIVE_CLOUD=1   — runs RequiresCloud=true scenarios
//     against the real DO API. Requires DIGITALOCEAN_ACCESS_TOKEN.
//   - testing.Short()=true       — limits to Smoke=true scenarios only
//     (the per-PR smoke gate's narrow contract).
//
// Without either env var, the suite runs only the non-cloud,
// non-smoke-only scenarios (the default for unit-style invocation).
//
// PR P-DO TP5.

package internal

import (
	"os"
	"testing"

	"github.com/GoCodeAlone/workflow/iac/conformance"
	"github.com/GoCodeAlone/workflow/interfaces"
)

// TestConformance dispatches the workflow conformance suite against a
// freshly-constructed DOProvider. Each scenario receives a new provider
// via the Provider closure; this guarantees per-scenario isolation
// (state from one scenario does not bleed into the next).
//
// The provider is initialized with the DIGITALOCEAN_ACCESS_TOKEN env var
// when LiveCloud is enabled. Without the token the LiveCloud scenarios
// would fail at the first cloud call; with it absent + LiveCloud=true
// the suite returns a clear error instead of hanging on a malformed
// network request.
func TestConformance(t *testing.T) {
	liveCloud := os.Getenv("CONFORMANCE_LIVE_CLOUD") == "1"
	cfg := conformance.Config{
		Provider: func() interfaces.IaCProvider {
			p := NewDOProvider()
			// Always initialize the provider exactly once with a
			// single token, picked at runtime: the real
			// DIGITALOCEAN_ACCESS_TOKEN when LiveCloud is enabled,
			// or a stub placeholder otherwise. The non-cloud
			// scenarios (cross-resource constraint rejection,
			// plan-stale diagnostic, structpb-roundtrip,
			// infra-output cross-module resolution,
			// protected-replace with/without override) need the
			// driver registry populated to dispatch their probes
			// but do NOT require a real DO token because the
			// driver methods they exercise are read-only or
			// pure-data transformations.
			//
			// Copilot review #4: the prior comment described a
			// "stub-then-real swap" with two Initialize calls;
			// the implementation has always made one call with
			// the right token chosen up-front, so the comment was
			// incorrect.
			token := "stub-token-for-non-cloud-conformance"
			if liveCloud {
				token = os.Getenv("DIGITALOCEAN_ACCESS_TOKEN")
				if token == "" {
					t.Fatal("CONFORMANCE_LIVE_CLOUD=1 but DIGITALOCEAN_ACCESS_TOKEN is not set")
				}
			}
			// Copilot review #13 (round 4) → #15 (round 5):
			// pass t.Context() through Initialize so callers that
			// honor context (and any future rev of Initialize that
			// does) get the test-scoped cancellation/deadline path.
			// Note: today's DOProvider.Initialize implementation
			// constructs its godo client with its own
			// context.Background() and ignores the passed-in ctx,
			// so passing t.Context() is forward-prep rather than an
			// immediate behavior change. Tracked as a follow-up to
			// thread the context through the godo construction so
			// LiveCloud cancellation reaches the HTTP transport.
			if err := p.Initialize(t.Context(), map[string]any{
				"token":  token,
				"region": "nyc3",
			}); err != nil {
				t.Fatalf("provider Initialize: %v", err)
			}
			return p
		},
		SmokeOnly: testing.Short(),
		LiveCloud: liveCloud,
	}
	conformance.Run(t, cfg)
}
