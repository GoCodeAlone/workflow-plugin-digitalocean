package drivers_test

import (
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// buildSpecViaCreate is a test helper that invokes Create on a mock client and
// returns the AppSpec that was sent in the create request.
func buildSpecViaCreate(t *testing.T, cfg map[string]any) *godo.AppSpec {
	t.Helper()
	mock := &mockAppClient{app: testApp()}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")
	_, err := d.Create(t.Context(), interfaces.ResourceSpec{
		Name:   "test-app",
		Config: cfg,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if mock.lastCreateReq == nil {
		t.Fatal("no create request captured")
	}
	return mock.lastCreateReq.Spec
}

// buildSpecViaUpdate is a test helper that invokes Update and returns the AppSpec used.
func buildSpecViaUpdate(t *testing.T, cfg map[string]any) *godo.AppSpec {
	t.Helper()
	mock := &mockAppClient{app: testApp()}
	d := drivers.NewAppPlatformDriverWithClient(mock, "nyc3")
	_, err := d.Update(t.Context(), interfaces.ResourceRef{Name: "test-app", ProviderID: "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"},
		interfaces.ResourceSpec{Name: "test-app", Config: cfg})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if mock.lastUpdateReq == nil {
		t.Fatal("no update request captured")
	}
	return mock.lastUpdateReq.Spec
}

// ── Representative canonical config (buildAppSpec builder coverage) ──────────

// TestBuildAppSpec_RepresentativeCanonicalConfig verifies that buildAppSpec accepts
// all currently-supported canonical keys without error and wires them to the correct
// AppSpec fields. The DOProvider.SupportedCanonicalKeys() key-list assertion is in
// internal/provider_test.go#TestDOProvider_SupportedCanonicalKeys.
func TestBuildAppSpec_RepresentativeCanonicalConfig(t *testing.T) {
	cfg := map[string]any{
		"image":          "registry.digitalocean.com/myrepo/myapp:v1",
		"http_port":      8080,
		"instance_count": 1,
		"size":           "m",
		"env_vars":       map[string]any{"FOO": "bar"},
		"autoscaling":    map[string]any{"min": 1, "max": 3},
		"routes":         []any{map[string]any{"path": "/"}},
		"health_check":   map[string]any{"http_path": "/healthz"},
		"domains":        []any{map[string]any{"domain": "x.example.com"}},
		"jobs":           []any{map[string]any{"name": "j", "kind": "pre_deploy", "run_command": "/j"}},
		"workers":        []any{map[string]any{"name": "w", "run_command": "/w"}},
		"static_sites":   []any{map[string]any{"name": "s", "build_command": "npm run build", "output_dir": "dist"}},
		// "sidecars" is excluded from SupportedCanonicalKeys (doUnsupportedCanonicalKeys) until Task 37 lands.
		"provider_specific": map[string]any{"digitalocean": map[string]any{"features": []any{"buildpack-stack=ubuntu-22"}}},
	}
	spec := buildSpecViaCreate(t, cfg)
	if spec == nil {
		t.Fatal("expected non-nil AppSpec")
	}
	if len(spec.Services) != 1 {
		t.Errorf("expected 1 service, got %d", len(spec.Services))
	}
	if len(spec.Jobs) != 1 {
		t.Errorf("expected 1 job, got %d", len(spec.Jobs))
	}
	if len(spec.Workers) != 1 {
		t.Errorf("expected 1 worker, got %d", len(spec.Workers))
	}
	if len(spec.StaticSites) != 1 {
		t.Errorf("expected 1 static site, got %d", len(spec.StaticSites))
	}
	if len(spec.Domains) != 1 {
		t.Errorf("expected 1 domain, got %d", len(spec.Domains))
	}
}

// ── instanceSizeSlug ────────────────────────────────────────────────────────

func TestBuildAppSpec_InstanceSizeSlug_AbstractSize(t *testing.T) {
	cases := []struct {
		size string
		want string
	}{
		{"xs", "apps-s-1vcpu-0.5gb"},
		{"s", "apps-s-1vcpu-1gb"},
		{"m", "apps-s-2vcpu-4gb"},
		{"l", "apps-s-4vcpu-8gb"},
		{"xl", "apps-s-8vcpu-16gb"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run("size_"+tc.size, func(t *testing.T) {
			cfg := map[string]any{
				"image": "registry.digitalocean.com/myrepo/myapp:v1",
				"size":  tc.size,
			}
			spec := buildSpecViaCreate(t, cfg)
			got := spec.Services[0].InstanceSizeSlug
			if got != tc.want {
				t.Errorf("InstanceSizeSlug = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildAppSpec_InstanceSizeSlug_ProviderSpecificOverride(t *testing.T) {
	cfg := map[string]any{
		"image": "registry.digitalocean.com/myrepo/myapp:v1",
		"size":  "m", // would normally resolve to apps-s-2vcpu-4gb
		"provider_specific": map[string]any{
			"digitalocean": map[string]any{
				"instance_size_slug": "apps-d-8vcpu-32gb", // override
			},
		},
	}
	spec := buildSpecViaCreate(t, cfg)
	got := spec.Services[0].InstanceSizeSlug
	if got != "apps-d-8vcpu-32gb" {
		t.Errorf("InstanceSizeSlug = %q, want provider-specific override %q", got, "apps-d-8vcpu-32gb")
	}
}

// ── Protocol ─────────────────────────────────────────────────────────────────

func TestBuildAppSpec_Protocol_HTTP2(t *testing.T) {
	cfg := map[string]any{
		"image":    "registry.digitalocean.com/myrepo/myapp:v1",
		"protocol": "HTTP2",
	}
	spec := buildSpecViaCreate(t, cfg)
	if spec.Services[0].Protocol != godo.SERVINGPROTOCOL_HTTP2 {
		t.Errorf("Protocol = %q, want HTTP2", spec.Services[0].Protocol)
	}
}

func TestBuildAppSpec_Protocol_Default(t *testing.T) {
	cfg := map[string]any{"image": "registry.digitalocean.com/myrepo/myapp:v1"}
	spec := buildSpecViaCreate(t, cfg)
	// Default is empty (DO uses HTTP internally).
	if spec.Services[0].Protocol != "" {
		t.Errorf("Protocol = %q, want empty (default HTTP)", spec.Services[0].Protocol)
	}
}

// ── BuildCommand / RunCommand / DockerfilePath / SourceDir ───────────────────

func TestBuildAppSpec_SourceFields(t *testing.T) {
	cfg := map[string]any{
		"image":           "registry.digitalocean.com/myrepo/myapp:v1",
		"build_command":   "go build -o /app/server .",
		"run_command":     "/app/server -config /config/app.yaml",
		"dockerfile_path": "Dockerfile.prebuilt",
		"source_dir":      "./cmd/server",
	}
	spec := buildSpecViaCreate(t, cfg)
	svc := spec.Services[0]
	if svc.BuildCommand != "go build -o /app/server ." {
		t.Errorf("BuildCommand = %q", svc.BuildCommand)
	}
	if svc.RunCommand != "/app/server -config /config/app.yaml" {
		t.Errorf("RunCommand = %q", svc.RunCommand)
	}
	if svc.DockerfilePath != "Dockerfile.prebuilt" {
		t.Errorf("DockerfilePath = %q", svc.DockerfilePath)
	}
	if svc.SourceDir != "./cmd/server" {
		t.Errorf("SourceDir = %q", svc.SourceDir)
	}
}

// ── InternalPorts ────────────────────────────────────────────────────────────

func TestBuildAppSpec_InternalPorts(t *testing.T) {
	cfg := map[string]any{
		"image":          "registry.digitalocean.com/myrepo/myapp:v1",
		"internal_ports": []any{9090, 9091},
	}
	spec := buildSpecViaCreate(t, cfg)
	ports := spec.Services[0].InternalPorts
	if len(ports) != 2 {
		t.Fatalf("expected 2 internal ports, got %d", len(ports))
	}
	if ports[0] != 9090 || ports[1] != 9091 {
		t.Errorf("InternalPorts = %v, want [9090, 9091]", ports)
	}
}

// ── Routes ───────────────────────────────────────────────────────────────────

func TestBuildAppSpec_Routes(t *testing.T) {
	cfg := map[string]any{
		"image": "registry.digitalocean.com/myrepo/myapp:v1",
		"routes": []any{
			map[string]any{"path": "/api", "preserve_path_prefix": true},
			map[string]any{"path": "/health"},
		},
	}
	spec := buildSpecViaCreate(t, cfg)
	routes := spec.Services[0].Routes
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(routes))
	}
	if routes[0].Path != "/api" || !routes[0].PreservePathPrefix {
		t.Errorf("route[0] = %+v, want path=/api preserve=true", routes[0])
	}
	if routes[1].Path != "/health" {
		t.Errorf("route[1].Path = %q, want /health", routes[1].Path)
	}
}

// ── HealthCheck ──────────────────────────────────────────────────────────────

func TestBuildAppSpec_HealthCheck(t *testing.T) {
	cfg := map[string]any{
		"image": "registry.digitalocean.com/myrepo/myapp:v1",
		"health_check": map[string]any{
			"http_path":             "/healthz",
			"initial_delay_seconds": 5,
			"period_seconds":        10,
			"timeout_seconds":       3,
			"success_threshold":     1,
			"failure_threshold":     3,
			"port":                  8080,
		},
	}
	spec := buildSpecViaCreate(t, cfg)
	hc := spec.Services[0].HealthCheck
	if hc == nil {
		t.Fatal("HealthCheck is nil")
	}
	if hc.HTTPPath != "/healthz" {
		t.Errorf("HTTPPath = %q, want /healthz", hc.HTTPPath)
	}
	if hc.InitialDelaySeconds != 5 {
		t.Errorf("InitialDelaySeconds = %d, want 5", hc.InitialDelaySeconds)
	}
	if hc.PeriodSeconds != 10 {
		t.Errorf("PeriodSeconds = %d, want 10", hc.PeriodSeconds)
	}
	if hc.FailureThreshold != 3 {
		t.Errorf("FailureThreshold = %d, want 3", hc.FailureThreshold)
	}
}

func TestBuildAppSpec_HealthCheck_Empty(t *testing.T) {
	cfg := map[string]any{"image": "registry.digitalocean.com/myrepo/myapp:v1"}
	spec := buildSpecViaCreate(t, cfg)
	if spec.Services[0].HealthCheck != nil {
		t.Error("expected nil HealthCheck when not specified")
	}
}

// ── LivenessHealthCheck ──────────────────────────────────────────────────────

func TestBuildAppSpec_LivenessHealthCheck(t *testing.T) {
	cfg := map[string]any{
		"image": "registry.digitalocean.com/myrepo/myapp:v1",
		"liveness_check": map[string]any{
			"http_path":      "/livez",
			"period_seconds": 15,
		},
	}
	spec := buildSpecViaCreate(t, cfg)
	lhc := spec.Services[0].LivenessHealthCheck
	if lhc == nil {
		t.Fatal("LivenessHealthCheck is nil")
	}
	if lhc.HTTPPath != "/livez" {
		t.Errorf("HTTPPath = %q, want /livez", lhc.HTTPPath)
	}
	if lhc.PeriodSeconds != 15 {
		t.Errorf("PeriodSeconds = %d, want 15", lhc.PeriodSeconds)
	}
}

// ── Autoscaling ──────────────────────────────────────────────────────────────

func TestBuildAppSpec_Autoscaling(t *testing.T) {
	cfg := map[string]any{
		"image": "registry.digitalocean.com/myrepo/myapp:v1",
		"autoscaling": map[string]any{
			"min":         2,
			"max":         10,
			"cpu_percent": 70,
		},
	}
	spec := buildSpecViaCreate(t, cfg)
	as := spec.Services[0].Autoscaling
	if as == nil {
		t.Fatal("Autoscaling is nil")
	}
	if as.MinInstanceCount != 2 {
		t.Errorf("MinInstanceCount = %d, want 2", as.MinInstanceCount)
	}
	if as.MaxInstanceCount != 10 {
		t.Errorf("MaxInstanceCount = %d, want 10", as.MaxInstanceCount)
	}
	if as.Metrics == nil || as.Metrics.CPU == nil {
		t.Fatal("Metrics.CPU is nil")
	}
	if as.Metrics.CPU.Percent != 70 {
		t.Errorf("CPU.Percent = %d, want 70", as.Metrics.CPU.Percent)
	}
}

func TestBuildAppSpec_Autoscaling_Empty(t *testing.T) {
	cfg := map[string]any{"image": "registry.digitalocean.com/myrepo/myapp:v1"}
	spec := buildSpecViaCreate(t, cfg)
	if spec.Services[0].Autoscaling != nil {
		t.Error("expected nil Autoscaling when not specified")
	}
}

// ── CORS ─────────────────────────────────────────────────────────────────────

func TestBuildAppSpec_CORS(t *testing.T) {
	cfg := map[string]any{
		"image": "registry.digitalocean.com/myrepo/myapp:v1",
		"cors": map[string]any{
			"allow_origins":     []any{"https://example.com"},
			"allow_methods":     []any{"GET", "POST", "OPTIONS"},
			"allow_headers":     []any{"Content-Type", "Authorization"},
			"allow_credentials": true,
			"max_age":           "24h",
		},
	}
	spec := buildSpecViaCreate(t, cfg)
	cors := spec.Services[0].CORS
	if cors == nil {
		t.Fatal("CORS is nil")
	}
	if len(cors.AllowOrigins) != 1 || cors.AllowOrigins[0].Exact != "https://example.com" {
		t.Errorf("AllowOrigins = %+v, want [{Exact: https://example.com}]", cors.AllowOrigins)
	}
	if len(cors.AllowMethods) != 3 {
		t.Errorf("AllowMethods count = %d, want 3", len(cors.AllowMethods))
	}
	if !cors.AllowCredentials {
		t.Error("AllowCredentials should be true")
	}
	if cors.MaxAge != "24h" {
		t.Errorf("MaxAge = %q, want 24h", cors.MaxAge)
	}
}

// ── Termination ──────────────────────────────────────────────────────────────

func TestBuildAppSpec_Termination(t *testing.T) {
	cfg := map[string]any{
		"image": "registry.digitalocean.com/myrepo/myapp:v1",
		"termination": map[string]any{
			"drain_seconds":        30,
			"grace_period_seconds": 60,
		},
	}
	spec := buildSpecViaCreate(t, cfg)
	term := spec.Services[0].Termination
	if term == nil {
		t.Fatal("Termination is nil")
	}
	if term.DrainSeconds != 30 {
		t.Errorf("DrainSeconds = %d, want 30", term.DrainSeconds)
	}
	if term.GracePeriodSeconds != 60 {
		t.Errorf("GracePeriodSeconds = %d, want 60", term.GracePeriodSeconds)
	}
}

// ── Alerts — numeric value coercion ─────────────────────────────────────────

func TestBuildAppSpec_Alerts_IntValue(t *testing.T) {
	// YAML decodes whole numbers as int; ensure alertSpecFromMap handles it.
	cfg := map[string]any{
		"image": "registry.digitalocean.com/myrepo/myapp:v1",
		"alerts": []any{
			map[string]any{
				"rule":     "CPU_UTILIZATION",
				"operator": "GREATER_THAN",
				"window":   "FIVE_MINUTES",
				"value":    80, // int, not float64
			},
		},
	}
	spec := buildSpecViaCreate(t, cfg)
	if len(spec.Services[0].Alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(spec.Services[0].Alerts))
	}
	a := spec.Services[0].Alerts[0]
	if a.Value != 80.0 {
		t.Errorf("Alert.Value = %v, want 80.0 (coerced from int)", a.Value)
	}
}

func TestBuildAppSpec_Alerts_Float64Value(t *testing.T) {
	cfg := map[string]any{
		"image": "registry.digitalocean.com/myrepo/myapp:v1",
		"alerts": []any{
			map[string]any{
				"rule":  "MEM_UTILIZATION",
				"value": 75.5, // float64
			},
		},
	}
	spec := buildSpecViaCreate(t, cfg)
	if len(spec.Services[0].Alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(spec.Services[0].Alerts))
	}
	if spec.Services[0].Alerts[0].Value != 75.5 {
		t.Errorf("Alert.Value = %v, want 75.5", spec.Services[0].Alerts[0].Value)
	}
}

// ── Domains ──────────────────────────────────────────────────────────────────

func TestBuildAppSpec_Domains(t *testing.T) {
	cfg := map[string]any{
		"image": "registry.digitalocean.com/myrepo/myapp:v1",
		"domains": []any{
			map[string]any{
				"domain": "app.example.com",
				"type":   "PRIMARY",
				"zone":   "example.com",
			},
			map[string]any{
				"domain":   "alias.example.com",
				"type":     "ALIAS",
				"wildcard": true,
			},
		},
	}
	spec := buildSpecViaCreate(t, cfg)
	domains := spec.Domains
	if len(domains) != 2 {
		t.Fatalf("expected 2 domains, got %d", len(domains))
	}
	if domains[0].Domain != "app.example.com" {
		t.Errorf("domain[0].Domain = %q, want app.example.com", domains[0].Domain)
	}
	if domains[0].Type != godo.AppDomainSpecType_Primary {
		t.Errorf("domain[0].Type = %q, want PRIMARY", domains[0].Type)
	}
	if domains[0].Zone != "example.com" {
		t.Errorf("domain[0].Zone = %q, want example.com", domains[0].Zone)
	}
	if !domains[1].Wildcard {
		t.Error("domain[1].Wildcard should be true")
	}
}

// ── Egress ───────────────────────────────────────────────────────────────────

func TestBuildAppSpec_Egress(t *testing.T) {
	cfg := map[string]any{
		"image":  "registry.digitalocean.com/myrepo/myapp:v1",
		"egress": map[string]any{"type": "DEDICATED_IP"},
	}
	spec := buildSpecViaCreate(t, cfg)
	if spec.Egress == nil {
		t.Fatal("Egress is nil")
	}
	if spec.Egress.Type != godo.APPEGRESSSPECTYPE_DedicatedIp {
		t.Errorf("Egress.Type = %q, want DEDICATED_IP", spec.Egress.Type)
	}
}

// ── Maintenance ──────────────────────────────────────────────────────────────

func TestBuildAppSpec_Maintenance(t *testing.T) {
	cfg := map[string]any{
		"image": "registry.digitalocean.com/myrepo/myapp:v1",
		"maintenance": map[string]any{
			"enabled":          true,
			"offline_page_url": "https://example.com/maintenance",
		},
	}
	spec := buildSpecViaCreate(t, cfg)
	if spec.Maintenance == nil {
		t.Fatal("Maintenance is nil")
	}
	if !spec.Maintenance.Enabled {
		t.Error("Maintenance.Enabled should be true")
	}
	if spec.Maintenance.OfflinePageURL != "https://example.com/maintenance" {
		t.Errorf("OfflinePageURL = %q", spec.Maintenance.OfflinePageURL)
	}
}

// ── VPC ──────────────────────────────────────────────────────────────────────

func TestBuildAppSpec_VPC(t *testing.T) {
	cfg := map[string]any{
		"image":   "registry.digitalocean.com/myrepo/myapp:v1",
		"vpc_ref": "vpc-abc123",
	}
	spec := buildSpecViaCreate(t, cfg)
	if spec.Vpc == nil {
		t.Fatal("Vpc is nil")
	}
	if spec.Vpc.ID != "vpc-abc123" {
		t.Errorf("Vpc.ID = %q, want vpc-abc123", spec.Vpc.ID)
	}
}

// ── ProviderSpecific features ─────────────────────────────────────────────────

func TestBuildAppSpec_ProviderSpecific_Features(t *testing.T) {
	cfg := map[string]any{
		"image": "registry.digitalocean.com/myrepo/myapp:v1",
		"provider_specific": map[string]any{
			"digitalocean": map[string]any{
				"features":                        []any{"buildpack-stack=ubuntu-22"},
				"disable_edge_cache":              true,
				"disable_email_obfuscation":       true,
				"enhanced_threat_control_enabled": false,
			},
		},
	}
	spec := buildSpecViaCreate(t, cfg)
	if len(spec.Features) != 1 || spec.Features[0] != "buildpack-stack=ubuntu-22" {
		t.Errorf("Features = %v, want [buildpack-stack=ubuntu-22]", spec.Features)
	}
	if !spec.DisableEdgeCache {
		t.Error("DisableEdgeCache should be true")
	}
	if !spec.DisableEmailObfuscation {
		t.Error("DisableEmailObfuscation should be true")
	}
}

// ── Jobs (PRE_DEPLOY / POST_DEPLOY / SCHEDULED) ─────────────────────────────

func TestBuildAppSpec_Jobs_PreDeploy(t *testing.T) {
	cfg := map[string]any{
		"image": "registry.digitalocean.com/myrepo/myapp:v1",
		"jobs": []any{
			map[string]any{
				"name":        "migrate",
				"kind":        "pre_deploy",
				"image":       "registry.digitalocean.com/bmw/workflow-migrate:v1",
				"run_command": "/workflow-migrate up",
				"env_vars_secret": map[string]any{
					"DATABASE_URL": "staging-db-url",
				},
			},
		},
	}
	spec := buildSpecViaCreate(t, cfg)
	if len(spec.Jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(spec.Jobs))
	}
	job := spec.Jobs[0]
	if job.Name != "migrate" {
		t.Errorf("Name = %q, want migrate", job.Name)
	}
	if job.Kind != godo.AppJobSpecKind_PreDeploy {
		t.Errorf("Kind = %q, want PRE_DEPLOY", job.Kind)
	}
	if job.RunCommand != "/workflow-migrate up" {
		t.Errorf("RunCommand = %q", job.RunCommand)
	}
	if job.Image == nil || job.Image.Repository != "workflow-migrate" {
		t.Errorf("Image.Repository = %q, want workflow-migrate", func() string {
			if job.Image == nil {
				return "<nil>"
			}
			return job.Image.Repository
		}())
	}
	// Secret env var.
	found := false
	for _, e := range job.Envs {
		if e.Key == "DATABASE_URL" && e.Type == godo.AppVariableType_Secret {
			found = true
		}
	}
	if !found {
		t.Error("expected DATABASE_URL secret env var in job")
	}
}

func TestBuildAppSpec_Jobs_PostDeploy(t *testing.T) {
	cfg := map[string]any{
		"image": "registry.digitalocean.com/myrepo/myapp:v1",
		"jobs": []any{
			map[string]any{
				"name":        "smoke-test",
				"kind":        "post_deploy",
				"run_command": "/app/smoke-test",
			},
		},
	}
	spec := buildSpecViaCreate(t, cfg)
	if spec.Jobs[0].Kind != godo.AppJobSpecKind_PostDeploy {
		t.Errorf("Kind = %q, want POST_DEPLOY", spec.Jobs[0].Kind)
	}
}

func TestBuildAppSpec_Jobs_Scheduled(t *testing.T) {
	cfg := map[string]any{
		"image": "registry.digitalocean.com/myrepo/myapp:v1",
		"jobs": []any{
			map[string]any{
				"name":        "nightly-report",
				"kind":        "scheduled",
				"cron":        "0 2 * * *",
				"run_command": "/app/report",
			},
		},
	}
	spec := buildSpecViaCreate(t, cfg)
	job := spec.Jobs[0]
	if job.Kind != godo.AppJobSpecKind_Scheduled {
		t.Errorf("Kind = %q, want SCHEDULED", job.Kind)
	}
	if job.Schedule == nil || job.Schedule.Cron != "0 2 * * *" {
		t.Errorf("Schedule.Cron = %q, want '0 2 * * *'", func() string {
			if job.Schedule == nil {
				return "<nil>"
			}
			return job.Schedule.Cron
		}())
	}
}

func TestBuildAppSpec_Jobs_FailedDeploy(t *testing.T) {
	cfg := map[string]any{
		"image": "registry.digitalocean.com/myrepo/myapp:v1",
		"jobs": []any{
			map[string]any{
				"name":        "rollback",
				"kind":        "failed_deploy",
				"run_command": "/app/rollback",
			},
		},
	}
	spec := buildSpecViaCreate(t, cfg)
	if spec.Jobs[0].Kind != godo.AppJobSpecKind_FailedDeploy {
		t.Errorf("Kind = %q, want FAILED_DEPLOY", spec.Jobs[0].Kind)
	}
}

func TestBuildAppSpec_Jobs_Termination(t *testing.T) {
	cfg := map[string]any{
		"image": "registry.digitalocean.com/myrepo/myapp:v1",
		"jobs": []any{
			map[string]any{
				"name":        "cleanup",
				"kind":        "post_deploy",
				"run_command": "/app/cleanup",
				"termination": map[string]any{"grace_period_seconds": 120},
			},
		},
	}
	spec := buildSpecViaCreate(t, cfg)
	term := spec.Jobs[0].Termination
	if term == nil {
		t.Fatal("Job.Termination is nil")
	}
	if term.GracePeriodSeconds != 120 {
		t.Errorf("GracePeriodSeconds = %d, want 120", term.GracePeriodSeconds)
	}
}

// ── Workers ──────────────────────────────────────────────────────────────────

func TestBuildAppSpec_Workers(t *testing.T) {
	cfg := map[string]any{
		"image": "registry.digitalocean.com/myrepo/myapp:v1",
		"workers": []any{
			map[string]any{
				"name":           "queue-processor",
				"run_command":    "/app/worker",
				"instance_count": 3,
				"size":           "m",
				"env_vars": map[string]any{
					"QUEUE_URL": "amqp://localhost",
				},
			},
		},
	}
	spec := buildSpecViaCreate(t, cfg)
	if len(spec.Workers) != 1 {
		t.Fatalf("expected 1 worker, got %d", len(spec.Workers))
	}
	w := spec.Workers[0]
	if w.Name != "queue-processor" {
		t.Errorf("Name = %q, want queue-processor", w.Name)
	}
	if w.RunCommand != "/app/worker" {
		t.Errorf("RunCommand = %q", w.RunCommand)
	}
	if w.InstanceCount != 3 {
		t.Errorf("InstanceCount = %d, want 3", w.InstanceCount)
	}
	if w.InstanceSizeSlug != "apps-s-2vcpu-4gb" {
		t.Errorf("InstanceSizeSlug = %q, want apps-s-2vcpu-4gb (m)", w.InstanceSizeSlug)
	}
	found := false
	for _, e := range w.Envs {
		if e.Key == "QUEUE_URL" {
			found = true
		}
	}
	if !found {
		t.Error("expected QUEUE_URL env var in worker")
	}
}

func TestBuildAppSpec_Workers_Autoscaling(t *testing.T) {
	cfg := map[string]any{
		"image": "registry.digitalocean.com/myrepo/myapp:v1",
		"workers": []any{
			map[string]any{
				"name":        "autoscaled-worker",
				"run_command": "/app/worker",
				"autoscaling": map[string]any{
					"min": 1,
					"max": 5,
				},
			},
		},
	}
	spec := buildSpecViaCreate(t, cfg)
	w := spec.Workers[0]
	if w.Autoscaling == nil {
		t.Fatal("Worker.Autoscaling is nil")
	}
	if w.Autoscaling.MinInstanceCount != 1 || w.Autoscaling.MaxInstanceCount != 5 {
		t.Errorf("Autoscaling = min=%d max=%d, want min=1 max=5",
			w.Autoscaling.MinInstanceCount, w.Autoscaling.MaxInstanceCount)
	}
}

func TestBuildAppSpec_Workers_Termination(t *testing.T) {
	cfg := map[string]any{
		"image": "registry.digitalocean.com/myrepo/myapp:v1",
		"workers": []any{
			map[string]any{
				"name":        "drain-worker",
				"run_command": "/app/worker",
				"termination": map[string]any{"grace_period_seconds": 90},
			},
		},
	}
	spec := buildSpecViaCreate(t, cfg)
	term := spec.Workers[0].Termination
	if term == nil {
		t.Fatal("Worker.Termination is nil")
	}
	if term.GracePeriodSeconds != 90 {
		t.Errorf("GracePeriodSeconds = %d, want 90", term.GracePeriodSeconds)
	}
}

// ── StaticSites ──────────────────────────────────────────────────────────────

func TestBuildAppSpec_StaticSites(t *testing.T) {
	cfg := map[string]any{
		"image": "registry.digitalocean.com/myrepo/myapp:v1",
		"static_sites": []any{
			map[string]any{
				"name":          "frontend",
				"build_command": "npm run build",
				"output_dir":    "dist",
				"routes":        []any{map[string]any{"path": "/"}},
				"env_vars": map[string]any{
					"VITE_API_BASE": "https://api.example.com",
				},
			},
		},
	}
	spec := buildSpecViaCreate(t, cfg)
	if len(spec.StaticSites) != 1 {
		t.Fatalf("expected 1 static site, got %d", len(spec.StaticSites))
	}
	ss := spec.StaticSites[0]
	if ss.Name != "frontend" {
		t.Errorf("Name = %q, want frontend", ss.Name)
	}
	if ss.BuildCommand != "npm run build" {
		t.Errorf("BuildCommand = %q", ss.BuildCommand)
	}
	if ss.OutputDir != "dist" {
		t.Errorf("OutputDir = %q", ss.OutputDir)
	}
	if len(ss.Routes) != 1 || ss.Routes[0].Path != "/" {
		t.Errorf("Routes = %+v", ss.Routes)
	}
	found := false
	for _, e := range ss.Envs {
		if e.Key == "VITE_API_BASE" {
			found = true
		}
	}
	if !found {
		t.Error("expected VITE_API_BASE env var in static site")
	}
}

func TestBuildAppSpec_StaticSites_FallbackDoc(t *testing.T) {
	cfg := map[string]any{
		"image": "registry.digitalocean.com/myrepo/myapp:v1",
		"static_sites": []any{
			map[string]any{
				"name":              "spa",
				"build_command":     "npm run build",
				"output_dir":        "dist",
				"catchall_document": "index.html",
				"error_document":    "404.html",
			},
		},
	}
	spec := buildSpecViaCreate(t, cfg)
	ss := spec.StaticSites[0]
	if ss.CatchallDocument != "index.html" {
		t.Errorf("CatchallDocument = %q, want index.html", ss.CatchallDocument)
	}
	if ss.ErrorDocument != "404.html" {
		t.Errorf("ErrorDocument = %q, want 404.html", ss.ErrorDocument)
	}
}

// ── Ingress ──────────────────────────────────────────────────────────────────

func TestBuildAppSpec_Ingress_LoadBalancer(t *testing.T) {
	cfg := map[string]any{
		"image":   "registry.digitalocean.com/myrepo/myapp:v1",
		"ingress": map[string]any{"load_balancer": "DIGITALOCEAN"},
	}
	spec := buildSpecViaCreate(t, cfg)
	if spec.Ingress == nil {
		t.Fatal("Ingress is nil")
	}
	if spec.Ingress.LoadBalancer != godo.AppIngressSpecLoadBalancer_DigitalOcean {
		t.Errorf("Ingress.LoadBalancer = %q, want DIGITALOCEAN", spec.Ingress.LoadBalancer)
	}
}

func TestBuildAppSpec_Ingress_EmptyIsNil(t *testing.T) {
	// An ingress map with no recognised fields must not produce a non-nil AppIngressSpec.
	cfg := map[string]any{
		"image":   "registry.digitalocean.com/myrepo/myapp:v1",
		"ingress": map[string]any{},
	}
	spec := buildSpecViaCreate(t, cfg)
	if spec.Ingress != nil {
		t.Errorf("expected nil Ingress for empty ingress map, got %+v", spec.Ingress)
	}
}

// ── LogDestinations ──────────────────────────────────────────────────────────

func TestBuildAppSpec_LogDestinations(t *testing.T) {
	cfg := map[string]any{
		"image": "registry.digitalocean.com/myrepo/myapp:v1",
		"log_destinations": []any{
			// HTTP endpoint destination with tls_insecure.
			map[string]any{
				"name":         "http-sink",
				"endpoint":     "https://logs.example.com/ingest",
				"tls_insecure": true,
			},
			// Papertrail destination (endpoint field).
			map[string]any{
				"name": "pt",
				"papertrail": map[string]any{
					"endpoint": "logs.papertrailapp.com:12345",
				},
			},
			// Datadog destination (api_key + endpoint).
			map[string]any{
				"name": "dd",
				"datadog": map[string]any{
					"api_key":  "dd-api-key",
					"endpoint": "https://http-intake.logs.datadoghq.com",
				},
			},
			// Logtail destination (token).
			map[string]any{
				"name": "lt",
				"logtail": map[string]any{
					"token": "lt-source-token",
				},
			},
		},
	}
	spec := buildSpecViaCreate(t, cfg)
	lds := spec.Services[0].LogDestinations
	if len(lds) != 4 {
		t.Fatalf("expected 4 log destinations, got %d", len(lds))
	}

	// HTTP endpoint + tls_insecure.
	http := lds[0]
	if http.Name != "http-sink" {
		t.Errorf("ld[0].Name = %q, want http-sink", http.Name)
	}
	if http.Endpoint != "https://logs.example.com/ingest" {
		t.Errorf("ld[0].Endpoint = %q, want https://logs.example.com/ingest", http.Endpoint)
	}
	if !http.TLSInsecure {
		t.Error("ld[0].TLSInsecure should be true")
	}

	// Papertrail endpoint.
	pt := lds[1]
	if pt.Papertrail == nil {
		t.Fatal("ld[1].Papertrail is nil")
	}
	if pt.Papertrail.Endpoint != "logs.papertrailapp.com:12345" {
		t.Errorf("ld[1].Papertrail.Endpoint = %q", pt.Papertrail.Endpoint)
	}

	// Datadog api_key + endpoint.
	dd := lds[2]
	if dd.Datadog == nil {
		t.Fatal("ld[2].Datadog is nil")
	}
	if dd.Datadog.ApiKey != "dd-api-key" {
		t.Errorf("ld[2].Datadog.ApiKey = %q", dd.Datadog.ApiKey)
	}
	if dd.Datadog.Endpoint != "https://http-intake.logs.datadoghq.com" {
		t.Errorf("ld[2].Datadog.Endpoint = %q", dd.Datadog.Endpoint)
	}

	// Logtail token.
	lt := lds[3]
	if lt.Logtail == nil {
		t.Fatal("ld[3].Logtail is nil")
	}
	if lt.Logtail.Token != "lt-source-token" {
		t.Errorf("ld[3].Logtail.Token = %q", lt.Logtail.Token)
	}
}

// ── Sidecars ─────────────────────────────────────────────────────────────────

func TestBuildAppSpec_Sidecars(t *testing.T) {
	cfg := map[string]any{
		"image": "registry.digitalocean.com/myrepo/myapp:v1",
		"sidecars": []any{
			map[string]any{
				"name":        "tailscale",
				"image":       "docker.io/tailscale/tailscale:latest",
				"run_command": "/usr/local/bin/containerboot",
				"env_vars_secret": map[string]any{
					"TS_AUTH_KEY": "ts-secret",
				},
			},
			map[string]any{
				"name":        "envoy-proxy",
				"image":       "docker.io/envoyproxy/envoy:v1.29",
				"run_command": "/usr/local/bin/envoy -c /config/envoy.yaml",
				"ports":       []any{map[string]any{"port": 9901, "public": true}},
			},
		},
	}
	spec := buildSpecViaCreate(t, cfg)
	// Main service + 2 sidecars = 3 services total.
	if len(spec.Services) != 3 {
		t.Fatalf("expected 3 services (main + 2 sidecars), got %d", len(spec.Services))
	}
	if spec.Services[0].Name != "test-app" {
		t.Errorf("services[0].Name = %q, want test-app", spec.Services[0].Name)
	}
	ts := spec.Services[1]
	if ts.Name != "tailscale" {
		t.Errorf("sidecar[0].Name = %q, want tailscale", ts.Name)
	}
	if ts.RunCommand != "/usr/local/bin/containerboot" {
		t.Errorf("sidecar[0].RunCommand = %q", ts.RunCommand)
	}
	if ts.Image == nil || ts.Image.Repository != "tailscale" {
		t.Errorf("sidecar[0].Image.Repository = %q, want tailscale", func() string {
			if ts.Image == nil {
				return "<nil>"
			}
			return ts.Image.Repository
		}())
	}
	// Secret env var forwarded.
	foundSecret := false
	for _, e := range ts.Envs {
		if e.Key == "TS_AUTH_KEY" && e.Type == godo.AppVariableType_Secret {
			foundSecret = true
		}
	}
	if !foundSecret {
		t.Error("expected TS_AUTH_KEY secret env var in tailscale sidecar")
	}

	// Envoy sidecar: public port mapped to HTTPPort.
	envoy := spec.Services[2]
	if envoy.Name != "envoy-proxy" {
		t.Errorf("sidecar[1].Name = %q, want envoy-proxy", envoy.Name)
	}
	if envoy.HTTPPort != 9901 {
		t.Errorf("sidecar[1].HTTPPort = %d, want 9901", envoy.HTTPPort)
	}
}

func TestBuildAppSpec_Sidecars_Empty(t *testing.T) {
	cfg := map[string]any{"image": "registry.digitalocean.com/myrepo/myapp:v1"}
	spec := buildSpecViaCreate(t, cfg)
	// Only the main service, no sidecars.
	if len(spec.Services) != 1 {
		t.Errorf("expected 1 service (no sidecars), got %d", len(spec.Services))
	}
}

// ── Update propagates buildAppSpec ──────────────────────────────────────────

func TestBuildAppSpec_UpdateUsesBuildSpec(t *testing.T) {
	cfg := map[string]any{
		"image": "registry.digitalocean.com/myrepo/myapp:v2",
		"jobs": []any{
			map[string]any{
				"name":        "migrate",
				"kind":        "pre_deploy",
				"run_command": "/workflow-migrate up",
			},
		},
	}
	spec := buildSpecViaUpdate(t, cfg)
	if len(spec.Jobs) != 1 || spec.Jobs[0].Kind != godo.AppJobSpecKind_PreDeploy {
		t.Errorf("Update did not propagate jobs correctly")
	}
}

// ── BMW pre-deploy scenario ──────────────────────────────────────────────────

func TestBuildAppSpec_BMWPreDeployScenario(t *testing.T) {
	cfg := map[string]any{
		"image":          "registry.digitalocean.com/bmw-registry/buymywishlist:abc123",
		"http_port":      8080,
		"instance_count": 2,
		"size":           "s",
		"env_vars": map[string]any{
			"SESSION_STORE": "pg",
		},
		"env_vars_secret": map[string]any{
			"DATABASE_URL": "staging-db-url",
		},
		"health_check": map[string]any{
			"http_path":             "/healthz",
			"initial_delay_seconds": 10,
		},
		"domains": []any{
			map[string]any{"domain": "bmw.example.com", "type": "PRIMARY"},
		},
		"jobs": []any{
			map[string]any{
				"name":        "migrate",
				"kind":        "pre_deploy",
				"image":       "registry.digitalocean.com/bmw-registry/workflow-migrate:v1",
				"run_command": "/workflow-migrate up",
				"env_vars_secret": map[string]any{
					"DATABASE_URL": "staging-db-url",
				},
			},
			map[string]any{
				"name":        "tenant-ensure",
				"kind":        "pre_deploy",
				"image":       "registry.digitalocean.com/bmw-registry/workflow-migrate:v1",
				"run_command": "/workflow-migrate tenant-ensure",
				"env_vars_secret": map[string]any{
					"DATABASE_URL":    "staging-db-url",
					"BMW_TENANT_SLUG": "bmw",
				},
			},
		},
	}

	spec := buildSpecViaCreate(t, cfg)

	// Service checks.
	if len(spec.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(spec.Services))
	}
	svc := spec.Services[0]
	if svc.InstanceCount != 2 {
		t.Errorf("InstanceCount = %d, want 2", svc.InstanceCount)
	}
	if svc.InstanceSizeSlug != "apps-s-1vcpu-1gb" {
		t.Errorf("InstanceSizeSlug = %q, want apps-s-1vcpu-1gb (s)", svc.InstanceSizeSlug)
	}
	if svc.HealthCheck == nil || svc.HealthCheck.HTTPPath != "/healthz" {
		t.Error("HealthCheck not set correctly")
	}

	// Jobs.
	if len(spec.Jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(spec.Jobs))
	}
	jobNames := map[string]bool{}
	for _, j := range spec.Jobs {
		jobNames[j.Name] = true
		if j.Kind != godo.AppJobSpecKind_PreDeploy {
			t.Errorf("job %q: Kind = %q, want PRE_DEPLOY", j.Name, j.Kind)
		}
	}
	if !jobNames["migrate"] || !jobNames["tenant-ensure"] {
		t.Errorf("job names = %v, want [migrate, tenant-ensure]", jobNames)
	}

	// Domains.
	if len(spec.Domains) != 1 || spec.Domains[0].Domain != "bmw.example.com" {
		t.Errorf("Domains = %+v", spec.Domains)
	}
}
