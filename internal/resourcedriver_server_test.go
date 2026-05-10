package internal

// resourcedriver_server_test.go — typed pb.ResourceDriverServer smoke
// test (Task 11 of the strict-contracts force-cutover plan,
// docs/plans/2026-05-10-strict-contracts-force-cutover.md, rev5).
//
// Stands up an in-process gRPC server with sdk.RegisterAllIaCProviderServices,
// dials it via bufconn, and exercises the typed ResourceDriver RPCs that
// doIaCServer must satisfy. Asserts:
//
//   - SensitiveKeys for a known type ("infra.spaces_key") routes to
//     SpacesKeyDriver and returns the documented sensitive-output list
//     ([access_key, secret_key]).
//   - SensitiveKeys for an unknown type ("infra.unknown_for_test")
//     surfaces a non-Unimplemented error — proving the dispatcher
//     hit the Go-side router rather than missing-service-registration
//     at the gRPC layer.
//   - Troubleshoot for a non-Troubleshooter driver
//     ("infra.spaces_key") returns codes.Unimplemented — preserves the
//     legacy iterate-and-skip semantics callers depend on.
//
// The test fails to compile until resourcedriver_server.go declares the
// ResourceDriver methods on doIaCServer. Per the TDD contract:
// red (this file) → green (resourcedriver_server.go).

import (
	"context"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
)

// TestDOResourceDriverServer_RoutesByResourceType is the Task 11 smoke
// test. The fake-token Initialize populates the per-type driver map
// without issuing any DO API calls; SensitiveKeys + Troubleshoot are
// non-network methods that exercise the routing layer end-to-end
// through a real gRPC channel (bufconn).
func TestDOResourceDriverServer_RoutesByResourceType(t *testing.T) {
	provider := NewDOProvider()
	if err := provider.Initialize(context.Background(), map[string]any{
		"token":  "fake-token-for-test",
		"region": "nyc3",
	}); err != nil {
		t.Fatalf("DOProvider.Initialize: %v", err)
	}

	listener := bufconn.Listen(iacServerTestBufSize)
	t.Cleanup(func() { _ = listener.Close() })

	srv := grpc.NewServer()
	if err := sdk.RegisterAllIaCProviderServices(srv, newDOIaCServer(provider)); err != nil {
		t.Fatalf("RegisterAllIaCProviderServices: %v", err)
	}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), iacServerTestRPCDeadline)
	t.Cleanup(cancel)

	client := pb.NewResourceDriverClient(conn)

	// 1. Known resource type — SensitiveKeys delegates to SpacesKeyDriver.
	keysResp, err := client.SensitiveKeys(ctx, &pb.SensitiveKeysRequest{
		ResourceType: "infra.spaces_key",
	})
	if err != nil {
		t.Fatalf("SensitiveKeys(infra.spaces_key): %v", err)
	}
	keys := keysResp.GetKeys()
	if len(keys) != 2 || keys[0] != "access_key" || keys[1] != "secret_key" {
		t.Errorf("SensitiveKeys = %v, want [access_key secret_key]", keys)
	}

	// 2. Unknown resource type — error surfaces at the dispatcher,
	//    NOT as transport-level Unimplemented.
	if _, err := client.SensitiveKeys(ctx, &pb.SensitiveKeysRequest{
		ResourceType: "infra.unknown_for_test",
	}); err == nil {
		t.Fatalf("SensitiveKeys(infra.unknown_for_test): expected error, got nil")
	} else if status.Code(err) == codes.Unimplemented {
		t.Errorf("SensitiveKeys(unknown): error code = Unimplemented; "+
			"want a per-type dispatcher error (the optional ResourceDriver "+
			"service appears unregistered): %v", err)
	} else if !strings.Contains(err.Error(), "infra.unknown_for_test") &&
		!strings.Contains(err.Error(), "unsupported resource type") {
		t.Errorf("SensitiveKeys(unknown) error %q should mention the unknown type", err.Error())
	}

	// 3. Optional Troubleshoot interface — SpacesKeyDriver does not
	//    implement interfaces.Troubleshooter, so the typed RPC must
	//    return codes.Unimplemented (the legitimate negative signal
	//    callers translate to ErrProviderMethodUnimplemented and
	//    fall back to the original failure message).
	_, err = client.Troubleshoot(ctx, &pb.TroubleshootRequest{
		ResourceType: "infra.spaces_key",
		Ref: &pb.ResourceRef{
			Name:       "test-key",
			Type:       "infra.spaces_key",
			ProviderId: "FAKE",
		},
		FailureMsg: "test failure",
	})
	if err == nil {
		t.Fatalf("Troubleshoot(spaces_key): expected codes.Unimplemented, got nil")
	}
	if got := status.Code(err); got != codes.Unimplemented {
		t.Errorf("Troubleshoot(spaces_key): code = %v, want Unimplemented (driver does not implement Troubleshooter); err=%v", got, err)
	}
}

// TestDOResourceDriverServer_TroubleshootsAppPlatform asserts the
// Troubleshooter optional interface IS reachable through the typed
// RPC for drivers that DO implement it. AppPlatformDriver implements
// Troubleshoot — the typed dispatch must NOT short-circuit to
// codes.Unimplemented for this driver. We don't drive a real DO API
// (no app exists at FAKE provider id), so we accept any non-
// Unimplemented error from the underlying driver as proof the routing
// reached the live Troubleshoot method rather than the optional-
// service-not-registered short-circuit.
func TestDOResourceDriverServer_TroubleshootsAppPlatform(t *testing.T) {
	provider := NewDOProvider()
	if err := provider.Initialize(context.Background(), map[string]any{
		"token":  "fake-token-for-test",
		"region": "nyc3",
	}); err != nil {
		t.Fatalf("DOProvider.Initialize: %v", err)
	}

	listener := bufconn.Listen(iacServerTestBufSize)
	t.Cleanup(func() { _ = listener.Close() })

	srv := grpc.NewServer()
	if err := sdk.RegisterAllIaCProviderServices(srv, newDOIaCServer(provider)); err != nil {
		t.Fatalf("RegisterAllIaCProviderServices: %v", err)
	}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), iacServerTestRPCDeadline)
	t.Cleanup(cancel)

	client := pb.NewResourceDriverClient(conn)

	// AppPlatformDriver DOES implement Troubleshooter — the typed RPC
	// should reach the live driver. Empty providerID returns a
	// driver-side error (per AppPlatformDriver.Troubleshoot's docstring),
	// which is fine — we just need to assert the error code is NOT
	// Unimplemented (which would mean dispatch dropped the call).
	resp, err := client.Troubleshoot(ctx, &pb.TroubleshootRequest{
		ResourceType: "infra.container_service",
		Ref: &pb.ResourceRef{
			Name: "test-app",
			Type: "infra.container_service",
			// ProviderID intentionally empty — driver returns
			// nil + nil per its empty-id guard, exercised below.
		},
		FailureMsg: "test failure",
	})
	if err != nil && status.Code(err) == codes.Unimplemented {
		t.Errorf("Troubleshoot(container_service): code = Unimplemented; "+
			"AppPlatformDriver implements interfaces.Troubleshooter, the typed "+
			"RPC must NOT short-circuit to Unimplemented for it: %v", err)
	}
	// Either way (resp+nil err for the empty-id guard, OR a
	// driver-side error string) is acceptable — both prove the typed
	// dispatch reached the live AppPlatformDriver.Troubleshoot method.
	_ = resp
	_ = err
}
