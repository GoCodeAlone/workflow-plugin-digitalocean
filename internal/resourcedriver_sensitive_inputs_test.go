package internal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goplugin "github.com/GoCodeAlone/go-plugin"
	"github.com/GoCodeAlone/workflow/iac/providerclient"
	"github.com/GoCodeAlone/workflow/iac/sensitive"
	"github.com/GoCodeAlone/workflow/iac/wfctlhelpers"
	"github.com/GoCodeAlone/workflow/interfaces"
	pluginpkg "github.com/GoCodeAlone/workflow/plugin"
	"github.com/GoCodeAlone/workflow/plugin/external"
	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	"github.com/GoCodeAlone/workflow/plugin/external/sdk"
	"github.com/GoCodeAlone/workflow/secrets"
	"github.com/hashicorp/go-hclog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type sensitiveInputTestDriver struct {
	interfaces.ResourceDriver
	paths       []string
	err         error
	createCalls atomic.Int32
}

func (d *sensitiveInputTestDriver) SensitiveInputPaths(context.Context) ([]string, error) {
	return append([]string(nil), d.paths...), d.err
}

func (d *sensitiveInputTestDriver) Create(_ context.Context, spec interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	d.createCalls.Add(1)
	return &interfaces.ResourceOutput{Name: spec.Name, Type: spec.Type, ProviderID: "fixture-id"}, nil
}

type stateUpdateTestDriver struct {
	sensitiveInputTestDriver
	legacyCalls atomic.Int32
	stateCalls  atomic.Int32
}

func (d *stateUpdateTestDriver) Update(_ context.Context, ref interfaces.ResourceRef, _ interfaces.ResourceSpec) (*interfaces.ResourceOutput, error) {
	d.legacyCalls.Add(1)
	return &interfaces.ResourceOutput{Name: ref.Name, Type: ref.Type, ProviderID: ref.ProviderID}, nil
}

func (d *stateUpdateTestDriver) UpdateWithState(_ context.Context, ref interfaces.ResourceRef, _ interfaces.ResourceSpec, prior *interfaces.ResourceState) (*interfaces.ResourceOutput, error) {
	d.stateCalls.Add(1)
	encoded, err := json.Marshal(prior)
	if err != nil {
		return nil, err
	}
	return &interfaces.ResourceOutput{Name: ref.Name, Type: ref.Type, ProviderID: ref.ProviderID, Outputs: map[string]any{"prior": string(encoded)}}, nil
}

func TestDOResourceUpdateWithStateSDKBridge(t *testing.T) {
	provider := NewDOProvider()
	driver := &stateUpdateTestDriver{}
	legacy := &stateUpdateTestDriver{}
	// The legacy wrapper intentionally does not expose ResourceStateUpdater.
	provider.drivers = map[string]interfaces.ResourceDriver{"fixture.state": driver, "fixture.legacy": struct{ interfaces.ResourceDriver }{legacy}}
	adapter := providerclient.New(sensitiveInputTestConn(t, provider), map[string]bool{providerclient.IaCServiceResourceDriver: true})
	rd, err := adapter.ResourceDriver("fixture.state")
	if err != nil {
		t.Fatal(err)
	}
	ref := interfaces.ResourceRef{Name: "identity", Type: "fixture.state", ProviderID: "parent/user"}
	spec := interfaces.ResourceSpec{Name: ref.Name, Type: ref.Type, Config: map[string]any{"rotation_epoch": "2"}}
	prior := &interfaces.ResourceState{ID: "state-id", Name: ref.Name, Type: ref.Type, ProviderID: ref.ProviderID,
		Provider: "digitalocean", ProviderRef: "provider-module", ConfigHash: "hash", AppliedConfigSource: "apply",
		Outputs:       map[string]any{"rotation_epoch": "2", "password": "secret://iac/identity/password"},
		AppliedConfig: map[string]any{"rotation_epoch": "2"}, Dependencies: []string{"parent"},
		CreatedAt: time.Unix(1700000000, 0).UTC(), UpdatedAt: time.Unix(1700000001, 0).UTC(), LastDriftCheck: time.Unix(1700000002, 0).UTC()}
	updater := rd.(interfaces.ResourceStateUpdater)
	out, err := updater.UpdateWithState(t.Context(), ref, spec, prior)
	if err != nil {
		t.Fatal(err)
	}
	var forwarded interfaces.ResourceState
	if raw, ok := out.Outputs["prior"].(string); !ok {
		t.Fatal("SDK bridge discarded authoritative prior state")
	} else if err := json.Unmarshal([]byte(raw), &forwarded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&forwarded, prior) || driver.stateCalls.Load() != 1 || driver.legacyCalls.Load() != 0 {
		t.Fatal("SDK bridge did not forward exact public prior state")
	}
	if _, err := rd.Update(t.Context(), ref, spec); err != nil {
		t.Fatal(err)
	}
	if driver.stateCalls.Load() != 2 {
		t.Fatal("nil state did not reach native state updater")
	}
	legacyRD, err := adapter.ResourceDriver("fixture.legacy")
	if err != nil {
		t.Fatal(err)
	}
	ref.Type, spec.Type = "fixture.legacy", "fixture.legacy"
	if _, err := legacyRD.Update(t.Context(), ref, spec); err != nil || legacy.legacyCalls.Load() != 1 {
		t.Fatalf("legacy server fallback changed: %v", err)
	}
}

func TestDOResourceUpdateWithStateSDKRejectsUnboundPrior(t *testing.T) {
	provider := NewDOProvider()
	driver := &stateUpdateTestDriver{}
	provider.drivers = map[string]interfaces.ResourceDriver{"fixture.state": driver}
	client := pb.NewResourceDriverClient(sensitiveInputTestConn(t, provider))
	for _, invalid := range []string{"name", "type", "provider-id", "resource-type", "outputs-json"} {
		t.Run(invalid, func(t *testing.T) {
			req := &pb.ResourceUpdateRequest{ResourceType: "fixture.state", Ref: &pb.ResourceRef{Name: "identity", Type: "fixture.state", ProviderId: "id"},
				Spec:       &pb.ResourceSpec{Name: "identity", Type: "fixture.state", ConfigJson: []byte(`{}`)},
				PriorState: &pb.ResourceState{Name: "identity", Type: "fixture.state", ProviderId: "id", OutputsJson: []byte(`{"rotation_epoch":"1"}`)}}
			switch invalid {
			case "name":
				req.PriorState.Name = "other"
			case "type":
				req.PriorState.Type = "other"
			case "provider-id":
				req.PriorState.ProviderId = "other"
			case "resource-type":
				req.ResourceType = "other"
			case "outputs-json":
				req.PriorState.OutputsJson = []byte(`{"password":"known-literal-do-credential"`)
			}
			_, err := client.Update(t.Context(), req)
			if status.Code(err) != codes.InvalidArgument || strings.Contains(err.Error(), "known-literal-do-credential") {
				t.Fatalf("invalid prior wire state not safely rejected: %v", err)
			}
			if driver.stateCalls.Load()+driver.legacyCalls.Load() != 0 {
				t.Fatal("invalid state reached driver")
			}
		})
	}
}

// This init-only overlay substitutes HTTP, not the production plugin entrypoint,
// SDK, provider, drivers, state store, plan producer, or released wfctl host.
const databaseConsumerHTTPOverlay = `package main
import (
 "fmt"
 "net/http"
 "net/url"
 "os"
)
type task7Transport struct { target *url.URL; base http.RoundTripper }
func (t task7Transport) RoundTrip(req *http.Request) (*http.Response, error) {
 if req.URL.Host != "api.digitalocean.com" || req.Header.Get("Authorization") != "Bearer fixture-token" {
  return nil, fmt.Errorf("fixture denies non-DigitalOcean request or nonfixture credential")
 }
 r := req.Clone(req.Context())
 r.URL.Scheme, r.URL.Host, r.Host = t.target.Scheme, t.target.Host, ""
 r.Header.Set("X-Task7-Fixture-Pid", fmt.Sprint(os.Getpid()))
 return t.base.RoundTrip(r)
}
func init() {
 target, err := url.Parse(os.Getenv("WORKFLOW_DO_TASK7_API"))
 if err != nil || target.Scheme != "http" || target.Hostname() != "127.0.0.1" { panic("fixture requires loopback HTTP") }
 http.DefaultClient = &http.Client{Transport: task7Transport{target, http.DefaultTransport}}
 f, err := os.OpenFile(os.Getenv("WORKFLOW_DO_TASK7_PIDS"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
 if err != nil { panic("fixture cannot record owned child") }
 if _, err := fmt.Fprintln(f, os.Getpid()); err != nil { panic("fixture cannot record owned child") }
 if err := f.Close(); err != nil { panic("fixture cannot close child ledger") }
}
`

type releasedHostConsumerProof struct {
	Result            string         `json:"result"`
	WFCTLVersion      string         `json:"wfctl_version"`
	WFCTLSHA256       string         `json:"wfctl_sha256"`
	PluginSHA256      string         `json:"plugin_sha256"`
	Dependency        string         `json:"dependency"`
	SavedPlanProducer string         `json:"saved_plan_producer"`
	LatestEpoch       string         `json:"saved_plan_latest_epoch"`
	RotatedEpoch      string         `json:"rotated_epoch"`
	FailedResetEpoch  string         `json:"failed_reset_retained_epoch"`
	RetryEpoch        string         `json:"retry_epoch"`
	APICalls          map[string]int `json:"api_calls"`
	APIEvents         []string       `json:"api_events"`
	OwnedChildren     []string       `json:"owned_children"`
	ChildrenReaped    bool           `json:"owned_children_reaped"`
	ListenerReaped    bool           `json:"owned_listener_reaped"`
	Plaintext         bool           `json:"plaintext_in_state_plan_logs"`
}

func TestDODatabaseRotationNativeReleasedWFCTL(t *testing.T) {
	required, enabled, err := releasedHostMode(os.Getenv, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skip("set WORKFLOW_DO_TASK7_WFCTL to the downloaded native released wfctl")
	}
	_ = os.Getenv("GITHUB_RUN_ID")
	_ = os.Getenv("GITHUB_RUN_ATTEMPT")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	t.Cleanup(cancel)
	root := t.TempDir()
	if evidence := os.Getenv("WORKFLOW_DO_TASK7_EVIDENCE"); evidence != "" {
		var err error
		root, err = os.MkdirTemp(evidence, "native-")
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{"home", "config", "tmp"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal("cannot create owned native environment")
		}
	}
	wfctl := os.Getenv("WORKFLOW_DO_TASK7_WFCTL")
	hostDigest := releasedHostSHA256
	if required {
		transport := releasedHostTransport()
		defer transport.CloseIdleConnections()
		wfctl, err = downloadReleasedHost(ctx, root, transport, releasedHostSize, releasedHostSHA256)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Remove(wfctl); err != nil && !os.IsNotExist(err) {
				t.Error("cannot remove owned released host")
			}
		})
	} else {
		size := releasedHostSize
		if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
			size, hostDigest = 112815442, "64bfdf1a3f99bd634dd2a0c8118a5145cc322149bf34eb1ea30a64836a3adf20"
		} else if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
			t.Fatal("local released host platform unsupported")
		}
		if err := verifyReleasedHostFile(wfctl, size, hostDigest); err != nil {
			t.Fatal(err)
		}
	}
	repoRoot := testRepoRoot(t)
	if buildRoot := os.Getenv("WORKFLOW_DO_TASK7_BUILD_ROOT"); buildRoot != "" {
		repoRoot = buildRoot
	}
	pluginName := readPluginName(t, filepath.Join(repoRoot, "plugin.json"))
	pluginsDir := filepath.Join(root, "plugins")
	pluginDir := filepath.Join(pluginsDir, pluginName)
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plugin.json", "plugin.contracts.json"} {
		copyFile(t, filepath.Join(repoRoot, name), filepath.Join(pluginDir, name))
	}
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("http_dependency.go", []byte(databaseConsumerHTTPOverlay))
	overlay, err := json.Marshal(map[string]any{"Replace": map[string]string{
		filepath.Join(repoRoot, "cmd/plugin/task7_http_dependency.go"): filepath.Join(root, "http_dependency.go"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	write("overlay.json", overlay)
	childEnv := releasedHostEnvironment(root)
	buildEnv := append([]string(nil), childEnv...)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal("cannot resolve toolchain module directory")
	}
	moduleCache := os.Getenv("GOMODCACHE")
	if moduleCache == "" {
		moduleCache = filepath.Join(home, "go", "pkg", "mod")
	}
	buildCache := os.Getenv("GOCACHE")
	if buildCache == "" {
		buildCache = filepath.Join(root, "build-cache")
	}
	buildEnv = append(buildEnv, "GOTOOLCHAIN=go1.27.2", "GOMODCACHE="+moduleCache, "GOCACHE="+buildCache, "CGO_ENABLED=0")
	buildCtx, buildCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer buildCancel()
	goTool, err := releasedHostGoTool()
	if err != nil {
		t.Fatal(err)
	}
	build := releasedHostCommand(buildCtx, buildEnv, repoRoot, goTool, "build", "-overlay", filepath.Join(root, "overlay.json"), "-o", filepath.Join(pluginDir, pluginName), "./cmd/plugin")
	if output, err := build.CombinedOutput(); err != nil {
		_ = output
		t.Fatal("production entrypoint build failed")
	}
	buildInfo, err := buildinfo.ReadFile(filepath.Join(pluginDir, pluginName))
	if err != nil || buildInfo.GoVersion != "go1.27.2" {
		t.Fatal("production entrypoint was not built with Go1.27.2")
	}
	const parent = "9cc10173-e9ea-4176-9dbc-a4cee4c4ff30"
	const sentinel = "NATIVE_TASK7_KNOWN_SECRET:/?#@%+"
	var mu sync.Mutex
	calls := map[string]int{}
	var events []string
	failReset := false
	apiCtx, apiCancel := context.WithCancel(ctx)
	api := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		key := r.Method + " " + r.URL.EscapedPath()
		calls[key]++
		events = append(events, r.Header.Get("X-Task7-Fixture-Pid")+" "+key)
		w.Header().Set("Content-Type", "application/json")
		base := "/v2/databases/" + parent
		switch {
		case r.Method == http.MethodGet && r.URL.Path == base:
			_ = json.NewEncoder(w).Encode(map[string]any{"database": map[string]any{
				"id": parent, "name": "db", "engine": "pg", "region": "nyc3", "version": "16", "status": "online", "size": "db-s-1vcpu-1gb", "num_nodes": 1,
				"connection": map[string]any{"host": "db.fixture.test", "port": 25060, "database": "defaultdb", "user": "doadmin", "password": sentinel, "uri": "DO_NOT_COPY_ADMIN_URI", "ssl": true},
			}})
		case r.Method == http.MethodPut && (r.URL.Path == base+"/resize" || r.URL.Path == base+"/firewall"):
			w.WriteHeader(http.StatusNoContent)
		case (r.Method == http.MethodGet && r.URL.Path == base+"/users/app-user") || (r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reset_auth")):
			if r.Method == http.MethodPost && failReset {
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]any{"message": sentinel})
				return
			}
			username := "app-user"
			if strings.Contains(r.URL.Path, "/doadmin/") {
				username = "doadmin"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"name": username, "role": "normal", "password": sentinel}})
		default:
			t.Errorf("unexpected external API fixture request %s", key)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	api.Config.BaseContext = func(net.Listener) context.Context { return apiCtx }
	api.Start()
	closeAPI := func() { releasedHostCloseAPI(api, apiCancel) }
	t.Cleanup(closeAPI)
	pidFile := filepath.Join(root, "owned-pids.txt")
	childEnv = append(childEnv, "WORKFLOW_DO_TASK7_API="+api.URL, "WORKFLOW_DO_TASK7_PIDS="+pidFile)
	t.Cleanup(func() {
		if err := reapReleasedHostChildren(pidFile, true); err != nil {
			t.Error(err)
		}
		closeAPI()
		if probe, err := net.DialTimeout("tcp", strings.TrimPrefix(api.URL, "http://"), time.Second); err == nil {
			_ = probe.Close()
			t.Error("owned fixture listener survived cleanup")
		}
	})
	secretDir := filepath.Join(root, "secrets")
	t.Cleanup(func() { _ = os.RemoveAll(secretDir) })
	if err := os.MkdirAll(secretDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configuration := fmt.Sprintf("secrets:\n  provider: file\n  config:\n    path: %q\nmodules:\n  - name: do-provider\n    type: iac.provider\n    config:\n      provider: digitalocean\n      token: fixture-token\n  - name: state\n    type: iac.state\n    config:\n      backend: filesystem\n      directory: %q\n", secretDir, filepath.Join(root, "state"))
	write("config.yaml", []byte(configuration))
	store, err := wfctlhelpers.ResolveStateStore(filepath.Join(root, "config.yaml"), "", pluginsDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	states := []interfaces.ResourceState{
		{ID: "app-user", Name: "app-user", Type: "digitalocean.database_user", Provider: "digitalocean", ProviderRef: "do-provider", ProviderID: parent + "/app-user", Outputs: map[string]any{"rotation_epoch": "1"}},
		{ID: "db", Name: "db", Type: "infra.database", Provider: "digitalocean", ProviderRef: "do-provider", ProviderID: parent, Outputs: map[string]any{"admin_rotation_epoch": "1", "engine": "pg", "version": "16", "region": "nyc3", "size": "db-s-1vcpu-1gb", "num_nodes": float64(1)}},
	}
	secretStore := secrets.NewFileProvider(secretDir)
	for i := range states {
		for _, key := range []string{"password", "uri"} {
			states[i].Outputs[key] = sensitive.Placeholder(states[i].Name, key)
			if err := secretStore.Set(ctx, sensitive.SecretKey(states[i].Name, key), sentinel); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.SaveResource(ctx, states[i]); err != nil {
			t.Fatal(err)
		}
	}
	producePlan := func(epoch, filename string) {
		t.Helper()
		rpcCtx, rpcCancel := context.WithTimeout(ctx, 30*time.Second)
		defer rpcCancel()
		manifest, err := pluginpkg.LoadManifest(filepath.Join(pluginDir, "plugin.json"))
		if err != nil {
			t.Fatal("cannot load real plugin manifest")
		}
		if err := manifest.Validate(); err != nil {
			t.Fatal("real plugin manifest invalid")
		}
		pluginLog, err := os.CreateTemp(root, "plan-rpc-*.log")
		if err != nil {
			t.Fatal("cannot create owned Plan RPC log")
		}
		cmd := releasedHostCommand(rpcCtx, childEnv, pluginDir, filepath.Join(pluginDir, pluginName))
		client := goplugin.NewClient(&goplugin.ClientConfig{HandshakeConfig: external.Handshake, Plugins: goplugin.PluginSet{"plugin": &external.GRPCPlugin{}}, Cmd: cmd, SkipHostEnv: true, StartTimeout: 10 * time.Second, Logger: hclog.NewNullLogger(), Stderr: pluginLog, SyncStderr: pluginLog, SyncStdout: pluginLog, AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC}})
		defer func() {
			client.Kill()
			_ = pluginLog.Close()
			data, err := os.ReadFile(pluginLog.Name())
			if err != nil {
				t.Error("owned Plan RPC log unavailable")
			}
			if strings.Contains(string(data), sentinel) {
				_ = os.Remove(pluginLog.Name())
				t.Error("Plan RPC plugin logged plaintext")
			}
		}()
		rpcClient, err := client.Client()
		if err != nil {
			t.Fatal("real plugin handshake failed")
		}
		raw, err := rpcClient.Dispense("plugin")
		if err != nil {
			t.Fatal("real public SDK plugin dispense failed")
		}
		pluginClient, ok := raw.(*external.PluginClient)
		if !ok {
			t.Fatal("real plugin did not expose public SDK client")
		}
		adapter, err := external.NewExternalPluginAdapter(pluginName, pluginClient, manifest)
		if err != nil {
			t.Fatal("real public SDK adapter initialization failed")
		}
		if !registryHasService(adapter.ContractRegistry(), pb.ResourceDriver_ServiceDesc.ServiceName) {
			t.Fatal("actual built plugin omitted ResourceDriver service")
		}
		provider := providerclient.New(adapter.Conn(), map[string]bool{providerclient.IaCServiceResourceDriver: true, providerclient.IaCServiceSensitiveInputDeclarer: true})
		if err := provider.Initialize(rpcCtx, map[string]any{"token": "fixture-token"}); err != nil {
			t.Fatal("real provider initialization failed")
		}
		current, err := store.ListResources(rpcCtx)
		if err != nil {
			t.Fatal(err)
		}
		desired := []interfaces.ResourceSpec{
			{Name: "app-user", Type: "digitalocean.database_user", Config: map[string]any{"provider": "do-provider", "database_id": parent, "username": "app-user", "rotation_epoch": epoch}},
			{Name: "db", Type: "infra.database", Config: map[string]any{"provider": "do-provider", "admin_rotation_epoch": epoch, "version": "16", "trusted_sources": []any{}}},
		}
		plan, err := provider.Plan(rpcCtx, desired, current)
		if err != nil {
			t.Fatal("real provider Plan RPC failed")
		}
		if len(plan.Actions) != 2 || plan.Actions[0].Action != "update" || plan.Actions[1].Action != "update" {
			t.Fatalf("real provider did not produce two update actions: %+v", plan.Actions)
		}
		// The config declares provider/state only; resource specs come from the
		// real Plan RPC. Use the public saved-plan hash helper for that config.
		plan.DesiredHash = wfctlhelpers.DesiredStateHash(nil, nil, nil, "")
		encoded, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		write(filename, encoded)
	}
	var commandNumber int
	run := func(expectSuccess bool, args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		cmd := releasedHostCommand(ctx, childEnv, root, wfctl, args...)
		output, err := cmd.CombinedOutput()
		commandNumber++
		if strings.Contains(string(output), sentinel) {
			t.Fatal("released wfctl printed credential bytes")
		}
		write(fmt.Sprintf("wfctl-%02d.log", commandNumber), output)
		if (err == nil) != expectSuccess {
			t.Fatalf("released wfctl command %d returned unexpected status", commandNumber)
		}
		return output
	}
	versionResult := captureReleasedHostVersion(ctx, childEnv, root, wfctl, "version")
	versionRedactor := secrets.NewRedactor()
	versionRedactor.AddValue("fixture-secret", sentinel)
	versionRedactor.AddValue("fixture-token", "fixture-token")
	versionRedactor.AddValue("owned-root", root)
	versionRedactor.AddValue("released-host-path", wfctl)
	versionEvidence := versionResult.diagnostic(versionRedactor)
	// Go retains test logs on failure even when the temporary proof root is gone.
	t.Logf("%s", versionEvidence)
	commandNumber++
	write(fmt.Sprintf("wfctl-%02d.log", commandNumber), []byte(versionEvidence+"\n"))
	if strings.Contains(string(versionResult.Combined), sentinel) {
		t.Fatal("released wfctl printed credential bytes")
	}
	if versionResult.Status != releasedHostVersionCompleted {
		t.Fatalf("released wfctl command %d returned unexpected status", commandNumber)
	}
	version := versionResult.Combined
	if strings.TrimSpace(string(version)) != "v0.86.1" {
		t.Fatal("proof did not run the required released wfctl")
	}
	producePlan("2", "stale-plan.json")
	for i := range states {
		key := "rotation_epoch"
		if states[i].Type == "infra.database" {
			key = "admin_rotation_epoch"
		}
		states[i].Outputs[key] = "2"
		if err := store.SaveResource(ctx, states[i]); err != nil {
			t.Fatal(err)
		}
	}
	apply := func(plan string, ok bool) {
		run(ok, "infra", "apply", "--config", filepath.Join(root, "config.yaml"), "--plan", filepath.Join(root, plan), "--plugin-dir", pluginsDir, "--auto-approve", "--skip-bootstrap")
	}
	resetCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls["POST /v2/databases/"+parent+"/users/app-user/reset_auth"] + calls["POST /v2/databases/"+parent+"/users/doadmin/reset_auth"]
	}
	assertState := func(epoch string) {
		t.Helper()
		for _, name := range []string{"app-user", "db"} {
			state, err := store.GetResource(ctx, name)
			if err != nil || state == nil {
				t.Fatalf("durable state missing after real apply: %v", err)
			}
			key := "rotation_epoch"
			if name == "db" {
				key = "admin_rotation_epoch"
			}
			if state.Outputs[key] != epoch || !sensitive.IsPlaceholder(state.Outputs["password"]) || !sensitive.IsPlaceholder(state.Outputs["uri"]) {
				t.Fatal("released host did not preserve epoch and routed secret references")
			}
			encoded, err := json.MarshalIndent(state, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			write(fmt.Sprintf("epoch-%s-%s.json", epoch, name), encoded)
		}
		list := run(true, "infra", "state", "list", "--config", filepath.Join(root, "config.yaml"))
		if !strings.Contains(string(list), "digitalocean.database_user") || !strings.Contains(string(list), "infra.database") || !strings.Contains(string(list), "2 resource(s) tracked") {
			t.Fatal("released wfctl did not list both durable identities")
		}
	}
	apply("stale-plan.json", true)
	if resetCount() != 0 {
		t.Fatal("saved plan used stale generation instead of latest persisted outputs")
	}
	assertState("2")
	producePlan("3", "rotation-plan.json")
	apply("rotation-plan.json", true)
	assertState("3")
	apply("rotation-plan.json", true)
	assertState("3")
	if resetCount() != 2 {
		t.Fatal("restart/saved-plan replay did not rotate each identity exactly once")
	}
	producePlan("4", "failure-plan.json")
	mu.Lock()
	failReset = true
	mu.Unlock()
	apply("failure-plan.json", false)
	assertState("3")
	mu.Lock()
	failReset = false
	mu.Unlock()
	apply("failure-plan.json", true)
	assertState("4")
	if resetCount() != 6 {
		t.Fatal("failed-reset/retry did not execute each identity exactly once per attempt")
	}
	for _, filename := range []string{"stale-plan.json", "rotation-plan.json", "failure-plan.json", "state/app-user.json", "state/db.json"} {
		data, err := os.ReadFile(filepath.Join(root, filename))
		if err != nil || strings.Contains(string(data), sentinel) {
			t.Fatalf("state/plan evidence missing or contains plaintext: %s", filename)
		}
	}
	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.Fields(string(pidBytes))) == 0 {
		t.Fatal("real consumer did not record native children")
	}
	if err := reapReleasedHostChildren(pidFile, false); err != nil {
		t.Fatal(err)
	}
	closeAPI()
	probe, err := net.DialTimeout("tcp", strings.TrimPrefix(api.URL, "http://"), time.Second)
	if err == nil {
		_ = probe.Close()
		t.Fatal("owned HTTP fixture listener was not reaped")
	}
	pluginBytes, err := os.ReadFile(filepath.Join(pluginDir, pluginName))
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	report, err := json.MarshalIndent(releasedHostConsumerProof{Result: "PASS", WFCTLVersion: strings.TrimSpace(string(version)),
		WFCTLSHA256: hostDigest, PluginSHA256: fmt.Sprintf("%x", sha256.Sum256(pluginBytes)),
		Dependency:        "loopback DigitalOcean HTTP fixture via init-only Go build overlay; all host/SDK/provider/driver logic real",
		SavedPlanProducer: "actual plugin Plan RPC via public providerclient; public DesiredStateHash for provider/state-only config; actual downloaded wfctl owns apply and state list",
		LatestEpoch:       "2", RotatedEpoch: "3", FailedResetEpoch: "3", RetryEpoch: "4", APICalls: calls, APIEvents: events, OwnedChildren: strings.Fields(string(pidBytes)), ChildrenReaped: true, ListenerReaped: true}, "", "  ")
	mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if t.Failed() || ctx.Err() != nil {
		t.Fatal("native consumer cannot report PASS after a failed assertion or deadline")
	}
	write("consumer-proof.json", report)
	proofFiles, err := filepath.Glob(filepath.Join(root, "*consumer-proof*.json"))
	if err != nil || len(proofFiles) != 1 {
		t.Fatal("native consumer must produce exactly one genuine proof")
	}
	var proof releasedHostConsumerProof
	if err := json.Unmarshal(report, &proof); err != nil || proof.Result != "PASS" || proof.WFCTLVersion != "v0.86.1" || proof.WFCTLSHA256 != hostDigest || !proof.ChildrenReaped || !proof.ListenerReaped || proof.Plaintext || proof.LatestEpoch != "2" || proof.RotatedEpoch != "3" || proof.FailedResetEpoch != "3" || proof.RetryEpoch != "4" {
		t.Fatal("native consumer proof is incomplete")
	}
	t.Logf("real released-wfctl consumer proof: %s", filepath.Join(root, "consumer-proof.json"))
}

func sensitiveInputTestConn(t *testing.T, provider *DOProvider) *grpc.ClientConn {
	t.Helper()
	listener := bufconn.Listen(iacServerTestBufSize)
	t.Cleanup(func() { _ = listener.Close() })
	server := grpc.NewServer()
	if err := sdk.RegisterAllIaCProviderServices(server, newDOIaCServer(provider)); err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestDOResourceSensitiveInputsGRPC(t *testing.T) {
	provider := NewDOProvider()
	if err := provider.Initialize(t.Context(), map[string]any{"token": "fixture-token"}); err != nil {
		t.Fatal(err)
	}
	paths := []string{"/password", "/connection/uri"}
	provider.drivers["fixture.credential"] = &sensitiveInputTestDriver{paths: paths}
	provider.drivers["fixture.denied"] = &sensitiveInputTestDriver{err: status.Error(codes.PermissionDenied, "discovery denied")}
	client := pb.NewResourceSensitiveInputDeclarerClient(sensitiveInputTestConn(t, provider))
	ctx, cancel := context.WithTimeout(t.Context(), iacServerTestRPCDeadline)
	defer cancel()
	resp, err := client.SensitiveInputPaths(ctx, &pb.ResourceSensitiveInputPathsRequest{ResourceType: "fixture.credential"})
	if err != nil || !reflect.DeepEqual(resp.GetPaths(), paths) {
		t.Fatalf("sensitive paths = %v, err = %v; want %v", resp.GetPaths(), err, paths)
	}
	for _, tc := range []struct {
		resourceType string
		code         codes.Code
	}{
		{"infra.spaces_key", codes.Unimplemented},
		{"infra.unknown", codes.NotFound},
		{"", codes.InvalidArgument},
		{"fixture.denied", codes.PermissionDenied},
	} {
		t.Run(tc.resourceType, func(t *testing.T) {
			_, err := client.SensitiveInputPaths(ctx, &pb.ResourceSensitiveInputPathsRequest{ResourceType: tc.resourceType})
			if status.Code(err) != tc.code {
				t.Fatalf("got %v, want code %v", err, tc.code)
			}
		})
	}
}

func TestDOSensitiveInputLiteralRejectedBeforeDispatch(t *testing.T) {
	provider := NewDOProvider()
	driver := &sensitiveInputTestDriver{paths: []string{"/password"}}
	provider.drivers = map[string]interfaces.ResourceDriver{"fixture.credential": driver}
	adapter := providerclient.New(sensitiveInputTestConn(t, provider), map[string]bool{
		providerclient.IaCServiceResourceDriver:         true,
		providerclient.IaCServiceSensitiveInputDeclarer: true,
	})
	const credential = "known-literal-do-credential"
	plan := &interfaces.IaCPlan{Actions: []interfaces.PlanAction{{
		Action: "create", Resource: interfaces.ResourceSpec{
			Name: "fixture", Type: "fixture.credential", Config: map[string]any{"password": credential},
		},
	}}}
	result, err := wfctlhelpers.ApplyPlanWithHooks(t.Context(), adapter, plan, wfctlhelpers.ApplyPlanHooks{})
	if err != nil || result == nil || len(result.Errors) != 1 || !strings.Contains(result.Errors[0].Error, interfaces.ErrValidation.Error()) {
		t.Fatalf("literal credential error = %v, result = %+v; want one action validation error", err, result)
	}
	if calls := driver.createCalls.Load(); calls != 0 {
		t.Fatalf("literal reached provider Create %d times", calls)
	}
	evidence, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(evidence), credential) {
		t.Fatal("validation diagnostic exposed credential bytes")
	}
}

func TestDOProviderDatabaseUserRegistration(t *testing.T) {
	provider := NewDOProvider()
	if err := provider.Initialize(t.Context(), map[string]any{"token": "fixture-token"}); err != nil {
		t.Fatal(err)
	}
	var declared bool
	for _, capability := range provider.Capabilities() {
		declared = declared || capability.ResourceType == "digitalocean.database_user"
	}
	if !declared {
		t.Error("database_user capability absent")
	}
	driver, err := provider.ResourceDriver("digitalocean.database_user")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := driver.(interfaces.ResourceSensitiveInputDeclarer); !ok {
		t.Fatal("database_user sensitive-input declarations absent")
	}
	for _, required := range []string{"uri", "password"} {
		var present bool
		for _, key := range driver.SensitiveKeys() {
			present = present || key == required
		}
		if !present {
			t.Errorf("sensitive output %s absent", required)
		}
	}
}

func TestDODatabaseUserManifest(t *testing.T) {
	root, err := os.ReadFile("../plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := os.ReadFile("../cmd/plugin/plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(root, embedded) {
		t.Fatal("root and embedded plugin manifests differ")
	}
	var manifest struct {
		MinEngineVersion string   `json:"minEngineVersion"`
		IaCServices      []string `json:"iacServices"`
		Capabilities     struct {
			IaCProvider struct {
				ResourceTypes []string `json:"resourceTypes"`
			} `json:"iacProvider"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(root, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.MinEngineVersion != "0.86.1" {
		t.Errorf("min engine = %q; authoritative update state requires 0.86.1", manifest.MinEngineVersion)
	}
	var declaredService, declaredResource bool
	for _, service := range manifest.IaCServices {
		declaredService = declaredService || service == providerclient.IaCServiceSensitiveInputDeclarer
	}
	for _, resourceType := range manifest.Capabilities.IaCProvider.ResourceTypes {
		declaredResource = declaredResource || resourceType == "digitalocean.database_user"
	}
	if !declaredService || !declaredResource {
		t.Errorf("sensitive-input service declared=%t, database_user declared=%t", declaredService, declaredResource)
	}
	for _, name := range []string{pb.IaCProviderRunner_ServiceDesc.ServiceName, pb.IaCProviderJobCanceler_ServiceDesc.ServiceName} {
		present := false
		for _, service := range manifest.IaCServices {
			present = present || service == name
		}
		if !present {
			t.Errorf("targeted-job service %s absent from manifest", name)
		}
	}
}
