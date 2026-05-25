package internal

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
)

func TestDORequirementMapper_MapsObservabilityToAppPlatformCollector(t *testing.T) {
	conn := newMapperTestConn(t)
	client := pb.NewIaCProviderRequirementMapperClient(conn)

	resp, err := client.MapRequirements(context.Background(), &pb.MapRequirementsRequest{
		Provider:    "digitalocean",
		Runtime:     pb.RequirementRuntime_REQUIREMENT_RUNTIME_DIGITALOCEAN_APP_PLATFORM,
		Environment: "prod",
		Requirements: []*pb.IaCRequirement{{
			Key:  "observability.telemetry",
			Kind: pb.RequirementKind_REQUIREMENT_KIND_OBSERVABILITY,
			Runtimes: []pb.RequirementRuntime{
				pb.RequirementRuntime_REQUIREMENT_RUNTIME_DIGITALOCEAN_APP_PLATFORM,
			},
			TelemetrySignals: []pb.TelemetrySignal{
				pb.TelemetrySignal_TELEMETRY_SIGNAL_TRACES,
				pb.TelemetrySignal_TELEMETRY_SIGNAL_METRICS,
				pb.TelemetrySignal_TELEMETRY_SIGNAL_LOGS,
			},
			ObservabilityBackends: []pb.ObservabilityBackend{
				pb.ObservabilityBackend_OBSERVABILITY_BACKEND_OTEL,
				pb.ObservabilityBackend_OBSERVABILITY_BACKEND_DATADOG,
				pb.ObservabilityBackend_OBSERVABILITY_BACKEND_PROMETHEUS,
				pb.ObservabilityBackend_OBSERVABILITY_BACKEND_LOKI,
				pb.ObservabilityBackend_OBSERVABILITY_BACKEND_GRAFANA,
			},
			DeploymentModes: []pb.DeploymentMode{
				pb.DeploymentMode_DEPLOYMENT_MODE_SIDECAR,
				pb.DeploymentMode_DEPLOYMENT_MODE_SIBLING_SERVICE,
			},
		}},
	})
	if err != nil {
		t.Fatalf("MapRequirements: %v", err)
	}
	if got := resp.GetAcceptedKeys(); len(got) != 1 || got[0] != "observability.telemetry" {
		t.Fatalf("accepted_keys = %v, want [observability.telemetry]", got)
	}
	if len(resp.GetRejected()) != 0 {
		t.Fatalf("rejected = %+v, want none", resp.GetRejected())
	}
	if len(resp.GetModules()) != 1 {
		t.Fatalf("modules len = %d, want 1", len(resp.GetModules()))
	}
	mod := resp.GetModules()[0]
	if mod.GetName() != "observability-collector" {
		t.Errorf("module name = %q, want observability-collector", mod.GetName())
	}
	if mod.GetType() != "infra.container_service" {
		t.Errorf("module type = %q, want infra.container_service", mod.GetType())
	}
	if got := mod.GetSatisfies(); len(got) != 1 || got[0] != "observability.telemetry" {
		t.Errorf("module satisfies = %v, want [observability.telemetry]", got)
	}

	var cfg map[string]any
	if err := json.Unmarshal(mod.GetConfigJson(), &cfg); err != nil {
		t.Fatalf("config_json: %v", err)
	}
	if cfg["image"] != "otel/opentelemetry-collector-contrib:latest" {
		t.Errorf("image = %v", cfg["image"])
	}
	if cfg["run_command"] != "otelcol-contrib --config=env:OTELCOL_CONFIG" {
		t.Errorf("run_command = %v", cfg["run_command"])
	}
	if cfg["expose"] != "internal" {
		t.Errorf("expose = %v, want internal", cfg["expose"])
	}
	envVars, ok := cfg["env_vars"].(map[string]any)
	if !ok {
		t.Fatalf("env_vars missing or wrong type: %#v", cfg["env_vars"])
	}
	collectorConfig, _ := envVars["OTELCOL_CONFIG"].(string)
	for _, want := range []string{
		"otlp:",
		"datadog:",
		"prometheus:",
		"loki:",
		"grafana_otlp:",
		"traces:",
		"metrics:",
		"logs:",
	} {
		if !containsAny(collectorConfig, want) {
			t.Fatalf("collector config missing %q:\n%s", want, collectorConfig)
		}
	}
	secretVars, ok := cfg["env_vars_secret"].(map[string]any)
	if !ok {
		t.Fatalf("env_vars_secret missing or wrong type: %#v", cfg["env_vars_secret"])
	}
	if secretVars["DD_API_KEY"] != "${secrets.datadog_api_key}" {
		t.Errorf("DD_API_KEY = %v", secretVars["DD_API_KEY"])
	}
	if secretVars["GRAFANA_OTLP_HEADERS"] != "${secrets.grafana_otlp_headers}" {
		t.Errorf("GRAFANA_OTLP_HEADERS = %v", secretVars["GRAFANA_OTLP_HEADERS"])
	}
}

func TestDORequirementMapper_RejectsUnsupportedRuntime(t *testing.T) {
	conn := newMapperTestConn(t)
	client := pb.NewIaCProviderRequirementMapperClient(conn)

	resp, err := client.MapRequirements(context.Background(), &pb.MapRequirementsRequest{
		Provider: "digitalocean",
		Runtime:  pb.RequirementRuntime_REQUIREMENT_RUNTIME_KUBERNETES,
		Requirements: []*pb.IaCRequirement{{
			Key:  "observability.telemetry",
			Kind: pb.RequirementKind_REQUIREMENT_KIND_OBSERVABILITY,
			DeploymentModes: []pb.DeploymentMode{
				pb.DeploymentMode_DEPLOYMENT_MODE_DAEMONSET,
			},
		}},
	})
	if err != nil {
		t.Fatalf("MapRequirements: %v", err)
	}
	if len(resp.GetModules()) != 0 {
		t.Fatalf("modules = %+v, want none", resp.GetModules())
	}
	if len(resp.GetAcceptedKeys()) != 0 {
		t.Fatalf("accepted_keys = %v, want none", resp.GetAcceptedKeys())
	}
	if got := resp.GetRejected(); len(got) != 1 || got[0].GetCode() != "unsupported_runtime" {
		t.Fatalf("rejected = %+v, want unsupported_runtime", got)
	}
}

func TestDORequirementMapper_UnregisteredProviderName(t *testing.T) {
	conn := newMapperTestConn(t)
	client := pb.NewIaCProviderRequirementMapperClient(conn)

	_, err := client.MapRequirements(context.Background(), &pb.MapRequirementsRequest{
		Provider: "not-digitalocean",
		Requirements: []*pb.IaCRequirement{{
			Key:  "observability.telemetry",
			Kind: pb.RequirementKind_REQUIREMENT_KIND_OBSERVABILITY,
		}},
	})
	if err == nil {
		t.Fatal("MapRequirements: expected provider mismatch error")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("MapRequirements code = %v, want InvalidArgument; err=%v", status.Code(err), err)
	}
}

func TestPluginManifestAdvertisesRequirementMapper(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(testRepoRoot(t), "plugin.json"))
	if err != nil {
		t.Fatalf("read plugin.json: %v", err)
	}
	var manifest struct {
		MinEngineVersion string   `json:"minEngineVersion"`
		IaCServices      []string `json:"iacServices"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse plugin.json: %v", err)
	}
	if manifest.MinEngineVersion != "0.64.3" {
		t.Fatalf("minEngineVersion = %q, want 0.64.3", manifest.MinEngineVersion)
	}
	const mapperService = "workflow.plugin.external.iac.IaCProviderRequirementMapper"
	for _, svc := range manifest.IaCServices {
		if svc == mapperService {
			return
		}
	}
	t.Fatalf("iacServices missing %s: %v", mapperService, manifest.IaCServices)
}

func newMapperTestConn(t *testing.T) *grpc.ClientConn {
	t.Helper()

	listener := bufconn.Listen(iacServerTestBufSize)
	t.Cleanup(func() { _ = listener.Close() })

	server := grpc.NewServer()
	if err := sdk.RegisterAllIaCProviderServices(server, newDOIaCServer(NewDOProvider())); err != nil {
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
	return conn
}
