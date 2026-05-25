package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	pb "github.com/GoCodeAlone/workflow/plugin/external/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	collectorModuleName    = "observability-collector"
	collectorImage         = "otel/opentelemetry-collector-contrib:latest"
	collectorRunCommand    = "otelcol-contrib --config=env:OTELCOL_CONFIG"
	collectorRequirementTy = "infra.container_service"
)

// MapRequirements implements the optional derived-IaC mapper contract for
// DigitalOcean. The only derived shape currently emitted is an App Platform
// OpenTelemetry Collector service. App-specific names stay with applications:
// the module name and satisfies keys are generic and overrideable in YAML.
func (s *doIaCServer) MapRequirements(_ context.Context, req *pb.MapRequirementsRequest) (*pb.MapRequirementsResponse, error) {
	if req.GetProvider() != "" && req.GetProvider() != "digitalocean" {
		return nil, status.Errorf(codes.InvalidArgument, "digitalocean mapper cannot satisfy provider %q", req.GetProvider())
	}

	resp := &pb.MapRequirementsResponse{}
	var accepted []*pb.IaCRequirement
	for _, requirement := range req.GetRequirements() {
		switch diag := rejectUnsupportedRequirement(req.GetRuntime(), requirement); {
		case diag != nil:
			resp.Rejected = append(resp.Rejected, diag)
		default:
			accepted = append(accepted, requirement)
			resp.AcceptedKeys = append(resp.AcceptedKeys, requirement.GetKey())
		}
	}
	if len(accepted) == 0 {
		return resp, nil
	}

	cfg, err := collectorModuleConfig(accepted)
	if err != nil {
		return nil, err
	}
	configJSON, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("digitalocean requirement mapper: encode collector config: %w", err)
	}
	resp.Modules = append(resp.Modules, &pb.DerivedModuleSpec{
		Name:       collectorModuleName,
		Type:       collectorRequirementTy,
		Satisfies:  append([]string(nil), resp.GetAcceptedKeys()...),
		ConfigJson: configJSON,
	})
	resp.Notes = append(resp.Notes, &pb.RequirementNote{
		Key:         strings.Join(resp.GetAcceptedKeys(), ","),
		Message:     "DigitalOcean App Platform has no native sidecars; this derives a generic internal OTel Collector service that can be overridden with an explicit module using the same satisfies keys.",
		Interactive: false,
	})
	return resp, nil
}

func rejectUnsupportedRequirement(runtime pb.RequirementRuntime, req *pb.IaCRequirement) *pb.RequirementDiagnostic {
	key := req.GetKey()
	if req.GetKind() != pb.RequirementKind_REQUIREMENT_KIND_OBSERVABILITY {
		return requirementDiagnostic(key, "unsupported_kind", "digitalocean can only derive observability requirements today")
	}
	if hint := req.GetResourceTypeHint(); hint != "" && hint != collectorRequirementTy {
		return requirementDiagnostic(key, "unsupported_resource_type_hint",
			fmt.Sprintf("digitalocean observability derivation emits %s, not %s", collectorRequirementTy, hint))
	}
	if runtime != pb.RequirementRuntime_REQUIREMENT_RUNTIME_DIGITALOCEAN_APP_PLATFORM {
		return requirementDiagnostic(key, "unsupported_runtime", "digitalocean observability derivation currently targets App Platform")
	}
	if !requirementAllowsRuntime(req, runtime) {
		return requirementDiagnostic(key, "unsupported_runtime", "requirement does not allow DigitalOcean App Platform")
	}
	if !requirementAllowsDeploymentMode(req) {
		return requirementDiagnostic(key, "unsupported_deployment_mode",
			"digitalocean App Platform maps sidecar intent to a sibling collector service")
	}
	return nil
}

func requirementAllowsRuntime(req *pb.IaCRequirement, runtime pb.RequirementRuntime) bool {
	if len(req.GetRuntimes()) == 0 {
		return true
	}
	for _, candidate := range req.GetRuntimes() {
		if candidate == runtime {
			return true
		}
	}
	return false
}

func requirementAllowsDeploymentMode(req *pb.IaCRequirement) bool {
	modes := req.GetDeploymentModes()
	if len(modes) == 0 {
		return true
	}
	for _, mode := range modes {
		switch mode {
		case pb.DeploymentMode_DEPLOYMENT_MODE_SIDECAR,
			pb.DeploymentMode_DEPLOYMENT_MODE_SIBLING_SERVICE,
			pb.DeploymentMode_DEPLOYMENT_MODE_MANAGED:
			return true
		}
	}
	return false
}

func requirementDiagnostic(key, code, message string) *pb.RequirementDiagnostic {
	return &pb.RequirementDiagnostic{Key: key, Code: code, Message: message}
}

func collectorModuleConfig(reqs []*pb.IaCRequirement) (map[string]any, error) {
	signals := requestedSignals(reqs)
	backends := requestedBackends(reqs)
	collectorConfig := buildCollectorConfig(signals, backends)

	envVars := map[string]any{
		"OTELCOL_CONFIG": collectorConfig,
	}
	secretVars := make(map[string]any)
	if hasBackend(backends, pb.ObservabilityBackend_OBSERVABILITY_BACKEND_OTEL) {
		envVars["OTEL_EXPORTER_OTLP_ENDPOINT"] = "${vars.otel_exporter_otlp_endpoint}"
	}
	if hasBackend(backends, pb.ObservabilityBackend_OBSERVABILITY_BACKEND_DATADOG) {
		envVars["DD_SITE"] = "${vars.datadog_site}"
		secretVars["DD_API_KEY"] = "${secrets.datadog_api_key}"
	}
	if hasBackend(backends, pb.ObservabilityBackend_OBSERVABILITY_BACKEND_LOKI) {
		envVars["LOKI_ENDPOINT"] = "${vars.loki_endpoint}"
	}
	if hasBackend(backends, pb.ObservabilityBackend_OBSERVABILITY_BACKEND_GRAFANA) {
		envVars["GRAFANA_OTLP_ENDPOINT"] = "${vars.grafana_otlp_endpoint}"
		secretVars["GRAFANA_OTLP_HEADERS"] = "${secrets.grafana_otlp_headers}"
	}

	cfg := map[string]any{
		"image":           collectorImage,
		"run_command":     collectorRunCommand,
		"expose":          "internal",
		"ports":           collectorPorts(backends),
		"env_vars":        envVars,
		"env_vars_secret": secretVars,
	}
	return cfg, nil
}

func requestedSignals(reqs []*pb.IaCRequirement) map[pb.TelemetrySignal]bool {
	out := make(map[pb.TelemetrySignal]bool)
	for _, req := range reqs {
		for _, signal := range req.GetTelemetrySignals() {
			if signal != pb.TelemetrySignal_TELEMETRY_SIGNAL_UNSPECIFIED {
				out[signal] = true
			}
		}
	}
	if len(out) == 0 {
		out[pb.TelemetrySignal_TELEMETRY_SIGNAL_TRACES] = true
		out[pb.TelemetrySignal_TELEMETRY_SIGNAL_METRICS] = true
		out[pb.TelemetrySignal_TELEMETRY_SIGNAL_LOGS] = true
	}
	return out
}

func requestedBackends(reqs []*pb.IaCRequirement) map[pb.ObservabilityBackend]bool {
	out := make(map[pb.ObservabilityBackend]bool)
	for _, req := range reqs {
		for _, backend := range req.GetObservabilityBackends() {
			if backend != pb.ObservabilityBackend_OBSERVABILITY_BACKEND_UNSPECIFIED {
				out[backend] = true
			}
		}
	}
	if len(out) == 0 {
		out[pb.ObservabilityBackend_OBSERVABILITY_BACKEND_OTEL] = true
	}
	return out
}

func collectorPorts(backends map[pb.ObservabilityBackend]bool) []any {
	ports := []any{
		map[string]any{"port": 4317, "public": false},
		map[string]any{"port": 4318, "public": false},
	}
	if hasBackend(backends, pb.ObservabilityBackend_OBSERVABILITY_BACKEND_PROMETHEUS) {
		ports = append(ports, map[string]any{"port": 9464, "public": false})
	}
	return ports
}

func buildCollectorConfig(signals map[pb.TelemetrySignal]bool, backends map[pb.ObservabilityBackend]bool) string {
	var b strings.Builder
	b.WriteString("receivers:\n")
	b.WriteString("  otlp:\n")
	b.WriteString("    protocols:\n")
	b.WriteString("      grpc:\n")
	b.WriteString("        endpoint: 0.0.0.0:4317\n")
	b.WriteString("      http:\n")
	b.WriteString("        endpoint: 0.0.0.0:4318\n")
	b.WriteString("processors:\n")
	b.WriteString("  batch: {}\n")
	b.WriteString("exporters:\n")
	writeExporters(&b, backends)
	b.WriteString("service:\n")
	b.WriteString("  pipelines:\n")
	if signals[pb.TelemetrySignal_TELEMETRY_SIGNAL_TRACES] {
		writePipeline(&b, "traces", exportersForSignal(pb.TelemetrySignal_TELEMETRY_SIGNAL_TRACES, backends))
	}
	if signals[pb.TelemetrySignal_TELEMETRY_SIGNAL_METRICS] {
		writePipeline(&b, "metrics", exportersForSignal(pb.TelemetrySignal_TELEMETRY_SIGNAL_METRICS, backends))
	}
	if signals[pb.TelemetrySignal_TELEMETRY_SIGNAL_LOGS] {
		writePipeline(&b, "logs", exportersForSignal(pb.TelemetrySignal_TELEMETRY_SIGNAL_LOGS, backends))
	}
	return b.String()
}

func writeExporters(b *strings.Builder, backends map[pb.ObservabilityBackend]bool) {
	if hasBackend(backends, pb.ObservabilityBackend_OBSERVABILITY_BACKEND_OTEL) {
		b.WriteString("  otlp:\n")
		b.WriteString("    endpoint: ${env:OTEL_EXPORTER_OTLP_ENDPOINT}\n")
	}
	if hasBackend(backends, pb.ObservabilityBackend_OBSERVABILITY_BACKEND_DATADOG) {
		b.WriteString("  datadog:\n")
		b.WriteString("    api:\n")
		b.WriteString("      key: ${env:DD_API_KEY}\n")
		b.WriteString("      site: ${env:DD_SITE}\n")
	}
	if hasBackend(backends, pb.ObservabilityBackend_OBSERVABILITY_BACKEND_PROMETHEUS) {
		b.WriteString("  prometheus:\n")
		b.WriteString("    endpoint: 0.0.0.0:9464\n")
	}
	if hasBackend(backends, pb.ObservabilityBackend_OBSERVABILITY_BACKEND_LOKI) {
		b.WriteString("  loki:\n")
		b.WriteString("    endpoint: ${env:LOKI_ENDPOINT}\n")
	}
	if hasBackend(backends, pb.ObservabilityBackend_OBSERVABILITY_BACKEND_GRAFANA) {
		b.WriteString("  otlp/grafana_otlp:\n")
		b.WriteString("    endpoint: ${env:GRAFANA_OTLP_ENDPOINT}\n")
		b.WriteString("    headers:\n")
		b.WriteString("      authorization: ${env:GRAFANA_OTLP_HEADERS}\n")
	}
}

func writePipeline(b *strings.Builder, name string, exporters []string) {
	if len(exporters) == 0 {
		return
	}
	b.WriteString("    ")
	b.WriteString(name)
	b.WriteString(":\n")
	b.WriteString("      receivers: [otlp]\n")
	b.WriteString("      processors: [batch]\n")
	b.WriteString("      exporters: [")
	b.WriteString(strings.Join(exporters, ", "))
	b.WriteString("]\n")
}

func exportersForSignal(signal pb.TelemetrySignal, backends map[pb.ObservabilityBackend]bool) []string {
	exporters := make([]string, 0, len(backends))
	if hasBackend(backends, pb.ObservabilityBackend_OBSERVABILITY_BACKEND_OTEL) {
		exporters = append(exporters, "otlp")
	}
	if hasBackend(backends, pb.ObservabilityBackend_OBSERVABILITY_BACKEND_DATADOG) {
		exporters = append(exporters, "datadog")
	}
	if signal == pb.TelemetrySignal_TELEMETRY_SIGNAL_METRICS &&
		hasBackend(backends, pb.ObservabilityBackend_OBSERVABILITY_BACKEND_PROMETHEUS) {
		exporters = append(exporters, "prometheus")
	}
	if signal == pb.TelemetrySignal_TELEMETRY_SIGNAL_LOGS &&
		hasBackend(backends, pb.ObservabilityBackend_OBSERVABILITY_BACKEND_LOKI) {
		exporters = append(exporters, "loki")
	}
	if hasBackend(backends, pb.ObservabilityBackend_OBSERVABILITY_BACKEND_GRAFANA) {
		exporters = append(exporters, "otlp/grafana_otlp")
	}
	sort.Strings(exporters)
	return exporters
}

func hasBackend(backends map[pb.ObservabilityBackend]bool, backend pb.ObservabilityBackend) bool {
	return backends[backend]
}
