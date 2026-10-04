package internal

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/GoCodeAlone/workflow/iac/providerclient"
	"github.com/GoCodeAlone/workflow/plugin/external"
	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	"github.com/digitalocean/godo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAppPlatformRunnerNativeOwnershipRecovery(t *testing.T) {
	if os.Getenv("WORKFLOW_IAC_HOST_CONFORMANCE") != "1" {
		t.Skip("set WORKFLOW_IAC_HOST_CONFORMANCE=1 to launch the production plugin against loopback HTTP")
	}
	root := t.TempDir()
	if evidence := os.Getenv("WORKFLOW_DO_TASK8_EVIDENCE"); evidence != "" {
		var err error
		root, err = os.MkdirTemp(evidence, "native-")
		if err != nil {
			t.Fatal(err)
		}
	}
	repo := testRepoRoot(t)
	name := readPluginName(t, filepath.Join(repo, "plugin.json"))
	plugins := filepath.Join(root, "plugins")
	plugin := filepath.Join(plugins, name)
	if err := os.MkdirAll(plugin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"plugin.json", "plugin.contracts.json"} {
		copyFile(t, filepath.Join(repo, file), filepath.Join(plugin, file))
	}
	write := func(file string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, file), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("http_dependency.go", []byte(databaseConsumerHTTPOverlay))
	overlay, err := json.Marshal(map[string]any{"Replace": map[string]string{filepath.Join(repo, "cmd/plugin/task8_http_dependency.go"): filepath.Join(root, "http_dependency.go")}})
	if err != nil {
		t.Fatal(err)
	}
	write("overlay.json", overlay)
	build := exec.Command("go", "build", "-overlay", filepath.Join(root, "overlay.json"), "-o", filepath.Join(plugin, name), "./cmd/plugin")
	build.Dir = repo
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build production plugin with HTTP-only dependency overlay: %v\n%s", err, output)
	}
	t.Setenv("WORKFLOW_DO_TASK7_PIDS", filepath.Join(root, "owned-pids.txt"))
	var cases []map[string]any
	for _, outcome := range []string{"rejected", "deployment-failed", "invocation-succeeded", "interrupted-cancel", "cleanup-original-handle", "cleanup-known-id", "cleanup-unencrypted", "cleanup-live-drift", "cleanup-pending-owned"} {
		api := newAppJobAPIFake()
		api.app.Spec.Databases = []*godo.AppDatabaseSpec{{Name: "db"}}
		durableCancel := strings.HasPrefix(outcome, "cleanup-")
		if outcome == "rejected" {
			api.app.InProgressDeployment = &godo.Deployment{ID: "deployment-uuid", Phase: godo.DeploymentPhase_Deploying, Spec: jobClone(api.app.Spec)}
			api.updateErr = fmt.Errorf("launch rejected before write")
		} else {
			api.encryptSecretValues, api.loseUpdateResponse, api.failGetAt = true, true, 2
		}
		if durableCancel {
			api.noInvocation, api.loseCleanupReadback = true, true
			if outcome == "cleanup-known-id" {
				api.loseUpdateResponse, api.failGetAt = false, 0
			}
			if outcome == "cleanup-unencrypted" {
				api.encryptSecretValues = false
			}
		}
		server := appJobHTTPFake(t, api)
		t.Setenv("WORKFLOW_DO_TASK7_API", server.URL)
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		var handle *pb.JobHandle
		load := func(call func(*external.ExternalPluginAdapter)) {
			t.Helper()
			mgr := external.NewExternalPluginManager(plugins, nil)
			defer mgr.Shutdown()
			adapter, err := mgr.LoadPlugin(name)
			if err != nil {
				t.Fatal(err)
			}
			if err := providerclient.New(adapter.Conn(), map[string]bool{providerclient.IaCServiceResourceDriver: true}).Initialize(ctx, map[string]any{"token": "fixture-token"}); err != nil {
				t.Fatal(err)
			}
			call(adapter)
		}
		load(func(adapter *external.ExternalPluginAdapter) {
			spec := validAppJobSpec()
			var err error
			handle, err = pb.NewIaCProviderRunnerClient(adapter.Conn()).RunJob(ctx, &pb.JobSpec{Name: spec.Name, Target: &pb.ResourceRef{Name: spec.Target.Name, Type: spec.Target.Type, ProviderId: spec.Target.ProviderID}, Image: spec.Image, RunCommand: spec.RunCommand, TimeoutSeconds: int32(spec.TimeoutSeconds), EnvVarsSecret: spec.EnvVarsSecret})
			if err != nil {
				t.Fatal(err)
			}
		})
		encoded, err := json.Marshal(handle)
		if err != nil || strings.Contains(string(encoded), "${db.DATABASE_URL}") || strings.Contains(string(encoded), "EV[") {
			t.Fatal("native handle encoding failed or persisted secret bytes")
		}
		write(outcome+"-handle.json", encoded)
		if durableCancel {
			var metadata appPlatformJobMetadata
			if err := json.Unmarshal([]byte(handle.Metadata["app_platform_job"]), &metadata); err != nil || (metadata.DeploymentID != "") != (outcome == "cleanup-known-id") || metadata.ComponentAcknowledged != (outcome == "cleanup-known-id") {
				t.Fatal("native fixture did not preserve original launch acknowledgement state")
			}
		}
		handle = jobClone(handle)
		api.loseUpdateResponse = false
		switch outcome {
		case "deployment-failed":
			api.noInvocation, api.deploymentPhase = true, godo.DeploymentPhase_Error
		case "invocation-succeeded":
			api.phase = godo.JOBINVOCATIONPHASE_Succeeded
		}
		wantDeploymentCancels := 0
		if durableCancel {
			cancelOriginal := func(wantError bool) {
				t.Helper()
				load(func(adapter *external.ExternalPluginAdapter) {
					persisted, err := os.ReadFile(filepath.Join(root, outcome+"-handle.json"))
					if err != nil {
						t.Fatal(err)
					}
					var original pb.JobHandle
					if err := json.Unmarshal(persisted, &original); err != nil {
						t.Fatal(err)
					}
					_, err = pb.NewIaCProviderJobCancelerClient(adapter.Conn()).CancelJob(ctx, &original)
					if (err != nil) != wantError {
						t.Fatalf("original durable handle CancelRPC %s: error=%v want-error=%t", outcome, err, wantError)
					}
				})
			}
			cancelOriginal(true)
			if api.loseCleanupReadback || api.deploymentCancels != 1 || api.updates != 2 || len(api.app.Spec.Jobs) != 0 || api.app.InProgressDeployment.ID == api.deployment.ID {
				t.Fatal("native fixture did not cancel/remove and advance deployment hint before losing cleanup readback")
			}
			parent := jobClone(api.app.Spec)
			wantDeploymentCancels = 1
			switch outcome {
			case "cleanup-live-drift":
				api.app.Spec.Services[0].RunCommand = "/changed-server"
				cancelOriginal(true)
				api.app.Spec = parent
			case "cleanup-pending-owned":
				api.deploymentPhase, api.ignoreDeployCancel = godo.DeploymentPhase_Deploying, true
				cancelOriginal(true)
				wantDeploymentCancels++
				api.deploymentPhase, api.ignoreDeployCancel = godo.DeploymentPhase_Canceled, false
			}
			cancelOriginal(false)
			cancelOriginal(false)
			persisted, err := os.ReadFile(filepath.Join(root, outcome+"-handle.json"))
			if err != nil || string(persisted) != string(encoded) {
				t.Fatal("CancelRPC recovery changed the original persisted handle")
			}
		} else {
			load(func(adapter *external.ExternalPluginAdapter) {
				client := pb.NewIaCProviderRunnerClient(adapter.Conn())
				if outcome != "interrupted-cancel" {
					reply, err := client.JobStatus(ctx, handle)
					if err != nil || (outcome == "invocation-succeeded" && reply.GetState() != pb.JobState_JOB_STATE_SUCCEEDED) || (outcome != "invocation-succeeded" && reply.GetState() != pb.JobState_JOB_STATE_FAILED) {
						t.Fatalf("native restarted recovery %s: %v", outcome, err)
					}
					handle = jobClone(reply.GetHandle())
				}
				_, err := pb.NewIaCProviderJobCancelerClient(adapter.Conn()).CancelJob(ctx, handle)
				if outcome != "rejected" && err != nil {
					t.Fatalf("native restarted cancellation %s: %v", outcome, err)
				}
			})
		}
		if api.deploymentCancels != wantDeploymentCancels || len(api.app.Spec.Jobs) != 0 || (outcome == "rejected" && (api.updates != 1 || api.cancels != 0)) || (outcome == "interrupted-cancel" && api.cancels != 1) || (durableCancel && (api.updates != 2 || api.cancels != 0)) {
			t.Fatalf("native %s did not preserve ownership/cleanup: updates=%d cancels=%d deployment-cancels=%d jobs=%d", outcome, api.updates, api.cancels, api.deploymentCancels, len(api.app.Spec.Jobs))
		}
		encoded, err = json.Marshal(handle)
		if err != nil || strings.Contains(string(encoded), "${db.DATABASE_URL}") || strings.Contains(string(encoded), "EV[") {
			t.Fatal("native recovered handle persisted secret bytes")
		}
		write(outcome+"-recovered-handle.json", encoded)
		cases = append(cases, map[string]any{"case": outcome, "updates": api.updates, "invocation_cancels": api.cancels, "deployment_cancels": api.deploymentCancels, "remaining_jobs": len(api.app.Spec.Jobs), "original_durable_handle_reloaded_per_cancel_rpc": durableCancel})
		cancel()
		server.Close()
		if conn, err := net.DialTimeout("tcp", strings.TrimPrefix(server.URL, "http://"), time.Second); err == nil {
			_ = conn.Close()
			t.Fatal("native API fixture listener was not reaped")
		}
	}
	pids, err := os.ReadFile(filepath.Join(root, "owned-pids.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range strings.Fields(string(pids)) {
		pid, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatal(err)
		}
		process, err := os.FindProcess(pid)
		if err != nil {
			t.Fatal(err)
		}
		if err := process.Signal(syscall.Signal(0)); err == nil {
			t.Fatalf("owned production plugin child %d retained", pid)
		}
	}
	binary, err := os.ReadFile(filepath.Join(plugin, name))
	if err != nil {
		t.Fatal(err)
	}
	proof, err := json.MarshalIndent(map[string]any{"result": "PASS", "cases": cases, "plugin_sha256": fmt.Sprintf("%x", sha256.Sum256(binary)), "host": "released Workflow v0.86.1 external plugin manager/SDK; actual cmd/plugin entrypoint; HTTP-only loopback dependency overlay", "owned_children": strings.Fields(string(pids)), "owned_children_and_listeners_reaped": true, "secret_bytes_in_handles": false}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	write("runner-proof.json", proof)
	t.Logf("native ownership/recovery proof: %s", filepath.Join(root, "runner-proof.json"))
}

func appJobHTTPFake(t *testing.T, fake *appJobAPIFake) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		ctx := r.Context()
		var response any
		var err error
		switch {
		case r.URL.Path == "/v2/apps/app-uuid" && r.Method == http.MethodGet:
			app, _, readErr := fake.Get(ctx, "app-uuid")
			err = readErr
			response = struct {
				App *godo.App `json:"app"`
			}{app}
		case r.URL.Path == "/v2/apps/app-uuid" && r.Method == http.MethodPut:
			var req godo.AppUpdateRequest
			if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
				t.Errorf("decode fake update: %v", decodeErr)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			app, _, writeErr := fake.Update(ctx, "app-uuid", &req)
			err = writeErr
			response = struct {
				App *godo.App `json:"app"`
			}{app}
		case r.URL.Path == "/v2/apps/app-uuid/job-invocations" && r.Method == http.MethodGet:
			opts := &godo.ListJobInvocationsOptions{}
			if names := r.URL.Query().Get("job_names"); names != "" {
				opts.JobNames = strings.Split(names, ",")
			}
			jobs, _, readErr := fake.ListJobInvocations(ctx, "app-uuid", opts)
			err = readErr
			response = struct {
				Jobs []*godo.JobInvocation `json:"job_invocations"`
			}{jobs}
		case r.URL.Path == "/v2/apps/app-uuid/deployments" && r.Method == http.MethodGet:
			deployments, _, readErr := fake.ListDeployments(ctx, "app-uuid", &godo.ListOptions{Page: 1, PerPage: 100})
			err = readErr
			response = struct {
				Deployments []*godo.Deployment `json:"deployments"`
			}{deployments}
		case r.URL.Path == "/v2/apps/app-uuid/job-invocations/invocation-uuid/cancel" && r.Method == http.MethodPost:
			job, _, cancelErr := fake.CancelJobInvocation(ctx, "app-uuid", "invocation-uuid", &godo.CancelJobInvocationOptions{JobName: r.URL.Query().Get("job_name")})
			err = cancelErr
			response = struct {
				Job *godo.JobInvocation `json:"job_invocation"`
			}{job}
		case r.URL.Path == "/v2/apps/app-uuid/job-invocations/invocation-uuid" && r.Method == http.MethodGet:
			job, _, readErr := fake.GetJobInvocation(ctx, "app-uuid", "invocation-uuid", &godo.GetJobInvocationOptions{JobName: r.URL.Query().Get("job_name")})
			err = readErr
			response = struct {
				Job *godo.JobInvocation `json:"job_invocation"`
			}{job}
		case strings.HasPrefix(r.URL.Path, "/v2/apps/app-uuid/jobs/") && strings.HasSuffix(r.URL.Path, "/invocations/invocation-uuid/logs") && r.Method == http.MethodGet:
			parts := strings.Split(r.URL.Path, "/")
			if r.URL.Query().Get("tail_lines") != "200" || r.URL.Query().Get("type") != "JOB_INVOCATION" {
				t.Error("unbounded or wrong log request")
			}
			logs, _, readErr := fake.GetJobInvocationLogs(ctx, "app-uuid", "invocation-uuid", &godo.GetJobInvocationLogsOptions{JobName: parts[5], TailLines: 200})
			err = readErr
			if logs == nil {
				logs = &godo.AppLogs{}
			}
			response = logs
		case r.URL.Path == "/v2/apps/app-uuid/deployments/deployment-uuid" && r.Method == http.MethodGet:
			deployment, _, readErr := fake.GetDeployment(ctx, "app-uuid", "deployment-uuid")
			err = readErr
			response = struct {
				Deployment *godo.Deployment `json:"deployment"`
			}{deployment}
		case r.URL.Path == "/v2/apps/app-uuid/deployments/deployment-uuid/cancel" && r.Method == http.MethodPost:
			deployment, _, cancelErr := fake.CancelDeployment(ctx, "app-uuid", "deployment-uuid")
			err = cancelErr
			response = struct {
				Deployment *godo.Deployment `json:"deployment"`
			}{deployment}
		default:
			t.Errorf("unexpected provider request %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"id":"fixture_failure","message":"provider request failed"}`)
			return
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("encode fake API response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestAppPlatformRunnerGRPCLifecycle(t *testing.T) {
	api := newAppJobAPIFake()
	api.app.Spec.Databases = []*godo.AppDatabaseSpec{{Name: "db"}}
	server := appJobHTTPFake(t, api)
	provider := NewDOProvider()
	provider.client = godo.NewClient(server.Client())
	provider.client.BaseURL, _ = url.Parse(server.URL + "/")
	conn := sensitiveInputTestConn(t, provider)
	spec := validAppJobSpec()
	handle, err := pb.NewIaCProviderRunnerClient(conn).RunJob(t.Context(), &pb.JobSpec{Name: spec.Name, Target: &pb.ResourceRef{Name: spec.Target.Name, Type: spec.Target.Type, ProviderId: spec.Target.ProviderID}, Image: spec.Image, RunCommand: spec.RunCommand, TimeoutSeconds: int32(spec.TimeoutSeconds), EnvVarsSecret: spec.EnvVarsSecret})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pb.NewIaCProviderJobCancelerClient(conn).CancelJob(t.Context(), handle); err != nil {
		t.Fatal(err)
	}
	if api.cancels != 1 || len(api.app.Spec.Jobs) != 0 {
		t.Fatal("native gRPC forwarding did not cancel and clean up")
	}
	stream, err := pb.NewIaCProviderRunnerClient(conn).JobLogs(t.Context(), handle)
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := stream.Recv()
	if err != nil || !chunk.GetEof() {
		t.Fatalf("expected log EOF chunk, got %v %v", chunk, err)
	}
	if _, err := stream.Recv(); err != io.EOF {
		t.Fatalf("expected completed log stream, got %v", err)
	}
}

func TestAppPlatformRunnerGRPCRequiresInitializedProvider(t *testing.T) {
	conn := sensitiveInputTestConn(t, NewDOProvider())
	_, err := pb.NewIaCProviderJobCancelerClient(conn).CancelJob(context.Background(), &pb.JobHandle{})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("uninitialized native canceler: %v", err)
	}
}
