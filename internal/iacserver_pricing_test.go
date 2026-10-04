package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestDOIaCServer_DropletPricingCancellation(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			var block atomic.Bool
			var calls atomic.Int32
			started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/v2/sizes" {
					t.Error("unexpected pricing request")
				}
				if block.Load() {
					close(started)
					select {
					case <-r.Context().Done():
						close(canceled)
					case <-release:
					}
					return
				}
				_, _ = fmt.Fprint(w, `{"sizes":[{"slug":"s-2vcpu-4gb","available":true,"regions":["nyc3"],"price_monthly":24}]}`)
			}))
			t.Cleanup(func() { close(release); srv.Close() })
			p := NewDOProvider()
			if err := p.Initialize(contextWithPricingHTTPClient(t, srv), map[string]any{"token": "fake", "region": "nyc3"}); err != nil {
				t.Fatal(err)
			}
			listener := bufconn.Listen(iacServerTestBufSize)
			t.Cleanup(func() { _ = listener.Close() })
			server := grpc.NewServer()
			if err := sdk.RegisterAllIaCProviderServices(server, newDOIaCServer(p)); err != nil {
				t.Fatal(err)
			}
			go func() { _ = server.Serve(listener) }()
			t.Cleanup(server.Stop)
			conn, err := grpc.NewClient("passthrough:///pricing",
				grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }),
				grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			client := pb.NewIaCProviderValidatorClient(conn)
			plan := &interfaces.IaCPlan{Actions: []interfaces.PlanAction{{Action: "create", Resource: interfaces.ResourceSpec{Name: "host", Type: "infra.droplet", Config: map[string]any{"size": "s-2vcpu-4gb", "region": "nyc3", "max_monthly_usd": 24}}}}}
			wirePlan, err := planToPB(plan)
			if err != nil {
				t.Fatal(err)
			}
			warmupCtx, warmupCancel := context.WithTimeout(t.Context(), iacServerTestRPCDeadline)
			defer warmupCancel()
			response, err := client.ValidatePlan(warmupCtx, &pb.ValidatePlanRequest{Plan: wirePlan})
			if err != nil || len(response.GetDiagnostics()) != 1 || response.Diagnostics[0].Severity != pb.PlanDiagnosticSeverity_PLAN_DIAGNOSTIC_INFO {
				t.Fatalf("typed quote RPC failed: %v", err)
			}
			var quote drivers.DropletPriceQuote
			if err := json.Unmarshal([]byte(response.Diagnostics[0].Message), &quote); err != nil || quote.MonthlyUSD != 24 || quote.CheckedAt.IsZero() {
				t.Fatal("RPC did not expose a typed live quote diagnostic")
			}
			delete(plan.Actions[0].Resource.Config, "max_monthly_usd")
			uncapped, err := planToPB(plan)
			if err != nil {
				t.Fatal(err)
			}
			if response, err := client.ValidatePlan(warmupCtx, &pb.ValidatePlanRequest{Plan: uncapped}); err != nil || len(response.GetDiagnostics()) != 0 || calls.Load() != 1 {
				t.Fatal("uncapped RPC performed pricing lookup")
			}
			block.Store(true)
			ctx, cancel := context.WithCancel(t.Context())
			wantCode := codes.Canceled
			if mode == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), time.Second)
				wantCode = codes.DeadlineExceeded
			}
			defer cancel()
			returned := make(chan error, 1)
			go func() { _, err := client.ValidatePlan(ctx, &pb.ValidatePlanRequest{Plan: wirePlan}); returned <- err }()
			select {
			case <-started:
			case <-time.After(iacServerTestRPCDeadline):
				t.Fatal("cancellation fixture never reached pricing API")
			}
			if mode == "cancel" {
				cancel()
			}
			select {
			case err := <-returned:
				if status.Code(err) != wantCode {
					t.Fatalf("RPC cancellation code = %v, want %v", status.Code(err), wantCode)
				}
			case <-time.After(iacServerTestRPCDeadline):
				t.Fatal("RPC did not terminate")
			}
			select {
			case <-canceled:
			case <-time.After(2 * time.Second):
				t.Fatal("gRPC cancellation did not reach pricing HTTP request")
			}
		})
	}
}
