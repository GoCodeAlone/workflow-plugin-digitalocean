package internal

// iacserver_test.go — typed pb.IaCProvider*Server smoke test (Task 8 of the
// strict-contracts force-cutover plan). Stands up an in-process gRPC server
// with sdk.RegisterAllIaCProviderServices, dials it via bufconn, and
// exercises the typed RPCs that DOIaCServer must satisfy:
//
//   - pb.IaCProviderRequiredServer: Name, Version, Capabilities (live
//     against an uninitialized DOProvider — these methods don't need
//     a godo client).
//   - pb.IaCProviderEnumeratorServer: EnumerateAll on an unsupported
//     resource type — exercises the optional service registration
//     surface end-to-end without requiring a live DO API.
//
// The test fails to compile until iacserver.go declares the doIaCServer
// type. Per the TDD contract: red (this file) → green (iacserver.go).

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
)

const (
	iacServerTestBufSize     = 1024 * 1024
	iacServerTestRPCDeadline = 5 * time.Second
)

// TestDOIaCProviderRequiredServer_AllRPCs is the Task 8 smoke test: it
// starts a typed-IaC gRPC server backed by *DOIaCServer wrapping an
// uninitialized *DOProvider, registers every service the provider
// satisfies via sdk.RegisterAllIaCProviderServices, and confirms each
// service is reachable through a typed pb client over a real gRPC
// channel (bufconn).
//
// The assertions cover:
//
//  1. Required service Name + Version (provider Go methods).
//  2. Required service Capabilities (the 17-resource declaration list
//     emitted by *DOProvider.Capabilities — typed-marshal happy path).
//  3. Optional Enumerator service EnumerateAll on an
//     unsupported-by-design resource type ("infra.unsupported_for_test").
//     The provider returns an error, which surfaces as a non-nil err on
//     the typed client. The point of the assertion is that the optional
//     Enumerator service IS registered (an Unimplemented gRPC error
//     here would mean RegisterAllIaCProviderServices skipped it because
//     DOIaCServer doesn't satisfy pb.IaCProviderEnumeratorServer).
//
// EnumerateAll on a SUPPORTED resource type ("infra.spaces_key") would
// require a live godo client; that lives in the Task 11 ResourceDriver
// + integration suite, not this Task-8 smoke test.
func TestDOIaCProviderRequiredServer_AllRPCs(t *testing.T) {
	listener := bufconn.Listen(iacServerTestBufSize)
	t.Cleanup(func() { _ = listener.Close() })

	server := grpc.NewServer()
	srv := newDOIaCServer(NewDOProvider())
	if err := sdk.RegisterAllIaCProviderServices(server, srv); err != nil {
		t.Fatalf("RegisterAllIaCProviderServices: %v", err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

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

	required := pb.NewIaCProviderRequiredClient(conn)

	nameResp, err := required.Name(ctx, &pb.NameRequest{})
	if err != nil {
		t.Fatalf("Name RPC: %v", err)
	}
	if nameResp.GetName() != "digitalocean" {
		t.Errorf("Name = %q, want %q", nameResp.GetName(), "digitalocean")
	}

	versionResp, err := required.Version(ctx, &pb.VersionRequest{})
	if err != nil {
		t.Fatalf("Version RPC: %v", err)
	}
	if versionResp.GetVersion() == "" {
		t.Error("Version returned empty string; want non-empty (build-stamped)")
	}

	capsResp, err := required.Capabilities(ctx, &pb.CapabilitiesRequest{})
	if err != nil {
		t.Fatalf("Capabilities RPC: %v", err)
	}
	got := capsResp.GetCapabilities()
	if len(got) != 17 {
		t.Errorf("Capabilities len = %d, want 17 (one per DO resource type)", len(got))
	}
	hasContainerService := false
	for _, c := range got {
		if c.GetResourceType() == "infra.container_service" {
			hasContainerService = true
			if len(c.GetOperations()) == 0 {
				t.Errorf("infra.container_service capability has no operations")
			}
		}
	}
	if !hasContainerService {
		t.Errorf("Capabilities missing infra.container_service")
	}

	// Optional service registration smoke test: the typed Enumerator
	// client MUST reach a real handler (NOT codes.Unimplemented) — that
	// proves doIaCServer satisfied pb.IaCProviderEnumeratorServer at
	// the RegisterAllIaCProviderServices type-assert. The DOProvider
	// itself returns a clean error for the unknown resource type
	// because client is nil; we accept any err other than the gRPC
	// "service not registered" Unimplemented signal.
	enumClient := pb.NewIaCProviderEnumeratorClient(conn)
	_, enumErr := enumClient.EnumerateAll(ctx, &pb.EnumerateAllRequest{
		ResourceType: "infra.unsupported_for_test",
	})
	if enumErr == nil {
		t.Fatalf("EnumerateAll: expected provider-side error for unsupported resource type, got nil")
	}
	// Sentinel cross-check: the provider returns an error string with
	// "EnumerateAll" or "not initialized" — a transport-level
	// codes.Unimplemented would NOT contain those tokens, so this is a
	// belt-and-braces guard against the optional service silently being
	// dropped at registration.
	msg := enumErr.Error()
	if !containsAny(msg, "EnumerateAll", "not initialized", "not supported") {
		t.Errorf("EnumerateAll error %q does not look like a provider-side error; "+
			"the optional Enumerator service may not be registered", msg)
	}

	logClient := pb.NewIaCProviderLogCaptureClient(conn)
	logStream, logErr := logClient.CaptureLogs(ctx, &pb.CaptureLogsRequest{
		ResourceName: "missing-app",
		LogType:      pb.LogCaptureType_LOG_CAPTURE_TYPE_RUN,
	})
	if logErr == nil {
		_, logErr = logStream.Recv()
	}
	if logErr == nil {
		t.Fatalf("CaptureLogs: expected provider-side error for uninitialized provider, got nil")
	}
	if !containsAny(logErr.Error(), "not initialized", "CaptureLogs") {
		t.Errorf("CaptureLogs error %q does not look like a provider-side error; "+
			"the optional LogCapture service may not be registered", logErr.Error())
	}
}

// containsAny returns true when s contains any of the provided substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub == "" {
			continue
		}
		// strings.Contains avoided to keep the helper standalone — the
		// test file is small enough that an inline scan is clear.
		if indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

// indexOf is a minimal substring search (avoids importing "strings"
// just for this helper; KISS).
func indexOf(s, sub string) int {
	if len(sub) == 0 || len(s) < len(sub) {
		return -1
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
