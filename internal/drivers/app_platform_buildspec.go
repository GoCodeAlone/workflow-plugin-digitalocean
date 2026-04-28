package drivers

import (
	"fmt"
	"strings"

	"github.com/digitalocean/godo"
)

// containerSizingMap maps canonical abstract size tiers to DO App Platform instance size slugs.
var containerSizingMap = map[string]string{
	"xs": "apps-s-1vcpu-0.5gb",
	"s":  "apps-s-1vcpu-1gb",
	"m":  "apps-s-2vcpu-4gb",
	"l":  "apps-s-4vcpu-8gb",
	"xl": "apps-s-8vcpu-16gb",
}

// buildAppSpec converts a canonical config map into a fully-populated godo.AppSpec.
// It maps every supported canonical IaC key to the corresponding DO App Platform field.
func buildAppSpec(name string, cfg map[string]any, region string) (*godo.AppSpec, error) {
	imgSpec, err := imageSpecFromConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("app platform image config: %w", err)
	}

	httpPort, _ := intFromConfig(cfg, "http_port", 8080)
	instanceCount, _ := intFromConfig(cfg, "instance_count", 1)

	svc := &godo.AppServiceSpec{
		Name:                name,
		Image:               imgSpec,
		HTTPPort:            int64(httpPort),
		InstanceCount:       int64(instanceCount),
		Envs:                envVarsFromConfig(cfg),
		BuildCommand:        strFromConfig(cfg, "build_command", ""),
		RunCommand:          strFromConfig(cfg, "run_command", ""),
		DockerfilePath:      strFromConfig(cfg, "dockerfile_path", ""),
		SourceDir:           strFromConfig(cfg, "source_dir", ""),
		InstanceSizeSlug:    instanceSizeSlugFromConfig(cfg),
		Protocol:            httpPortProtocolFromConfig(cfg),
		InternalPorts:       internalPortsFromConfig(cfg),
		Routes:              routesFromConfig(cfg),
		HealthCheck:         serviceHealthCheckFromConfig(cfg),
		LivenessHealthCheck: livenessHealthCheckFromConfig(cfg),
		CORS:                corsFromConfig(cfg),
		Autoscaling:         autoscalingFromConfig(cfg),
		Termination:         serviceTerminationFromConfig(cfg),
		LogDestinations:     logDestinationsFromConfig(cfg),
		Alerts:              componentAlertsFromConfig(cfg),
	}

	// Extract provider_specific.digitalocean overrides for top-level AppSpec fields.
	var (
		disableEdgeCache        bool
		disableEmailObfuscation bool
		enhancedThreatControl   bool
		features                []string
	)
	if ps, ok := cfg["provider_specific"].(map[string]any); ok {
		if do, ok := ps["digitalocean"].(map[string]any); ok {
			disableEdgeCache, _ = do["disable_edge_cache"].(bool)
			disableEmailObfuscation, _ = do["disable_email_obfuscation"].(bool)
			enhancedThreatControl, _ = do["enhanced_threat_control_enabled"].(bool)
			if ff, ok := do["features"].([]any); ok {
				for _, f := range ff {
					if s, ok := f.(string); ok {
						features = append(features, s)
					}
				}
			}
		}
	}

	// Sidecars run as sibling AppServiceSpec entries (DO App Platform does not have
	// a native sidecar concept; each sidecar becomes an independent service component).
	sidecars, err := sidecarsFromConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("app platform sidecars: %w", err)
	}
	services := append([]*godo.AppServiceSpec{svc}, sidecars...)

	spec := &godo.AppSpec{
		Name:                         name,
		Region:                       region,
		Services:                     services,
		Jobs:                         jobsFromConfig(cfg),
		Workers:                      workersFromConfig(cfg),
		StaticSites:                  staticSitesFromConfig(cfg),
		Domains:                      domainsFromConfig(cfg),
		Alerts:                       appAlertsFromConfig(cfg),
		Ingress:                      ingressFromConfig(cfg),
		Egress:                       egressFromConfig(cfg),
		Maintenance:                  maintenanceFromConfig(cfg),
		Vpc:                          vpcFromConfig(cfg),
		Features:                     features,
		DisableEdgeCache:             disableEdgeCache,
		DisableEmailObfuscation:      disableEmailObfuscation,
		EnhancedThreatControlEnabled: enhancedThreatControl,
	}
	return spec, nil
}

// instanceSizeSlugFromConfig resolves the canonical "size" key to a DO instance size slug.
// provider_specific.digitalocean.instance_size_slug takes precedence over the abstract "size" tier.
func instanceSizeSlugFromConfig(cfg map[string]any) string {
	if ps, ok := cfg["provider_specific"].(map[string]any); ok {
		if do, ok := ps["digitalocean"].(map[string]any); ok {
			if slug, ok := do["instance_size_slug"].(string); ok && slug != "" {
				return slug
			}
		}
	}
	size := strFromConfig(cfg, "size", "")
	if slug, ok := containerSizingMap[size]; ok {
		return slug
	}
	return ""
}

// httpPortProtocolFromConfig maps the canonical port protocol to a godo.ServingProtocol
// on godo.AppServiceSpec.Protocol (godo v1.178.0 apps.gen.go:568).
//
// Two canonical keys are accepted:
//
//   - "http_port_protocol" — explicit, mirrors the DO App Platform API field
//     name. Takes precedence when both keys are set.
//   - "protocol" — historic shorthand. Recognized aliases: "grpc" → HTTP2
//     (gRPC requires HTTP/2 with prior knowledge per DO docs).
//
// DO recognizes HTTP and HTTP2; unknown values pass through for forward
// compatibility with future godo releases.
func httpPortProtocolFromConfig(cfg map[string]any) godo.ServingProtocol {
	// Explicit canonical key wins over the shorthand.
	raw := strFromConfig(cfg, "http_port_protocol", "")
	if raw == "" {
		raw = strFromConfig(cfg, "protocol", "")
	}
	switch strings.ToUpper(raw) {
	case "HTTP2", "GRPC":
		// gRPC over App Platform is served as HTTP/2 with prior knowledge.
		return godo.SERVINGPROTOCOL_HTTP2
	case "HTTP", "":
		return "" // omit — DO defaults to HTTP
	default:
		return godo.ServingProtocol(strings.ToUpper(raw))
	}
}

// internalPortsFromConfig converts the canonical "internal_ports" list to []int64.
func internalPortsFromConfig(cfg map[string]any) []int64 {
	raw, ok := cfg["internal_ports"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]int64, 0, len(raw))
	for _, v := range raw {
		switch t := v.(type) {
		case int:
			out = append(out, int64(t))
		case int64:
			out = append(out, t)
		case float64:
			out = append(out, int64(t))
		}
	}
	return out
}

// routesFromConfig converts the canonical "routes" list to []*godo.AppRouteSpec.
func routesFromConfig(cfg map[string]any) []*godo.AppRouteSpec {
	raw, ok := cfg["routes"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	routes := make([]*godo.AppRouteSpec, 0, len(raw))
	for _, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		r := &godo.AppRouteSpec{
			Path: strFromConfig(m, "path", "/"),
		}
		if pp, ok := m["preserve_path_prefix"].(bool); ok {
			r.PreservePathPrefix = pp
		}
		routes = append(routes, r)
	}
	return routes
}

// serviceHealthCheckFromConfig converts the canonical "health_check" map to a godo.AppServiceSpecHealthCheck.
func serviceHealthCheckFromConfig(cfg map[string]any) *godo.AppServiceSpecHealthCheck {
	raw, ok := cfg["health_check"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	hc := &godo.AppServiceSpecHealthCheck{
		HTTPPath: strFromConfig(raw, "http_path", ""),
	}
	if v, ok := intFromConfig(raw, "initial_delay_seconds", 0); ok {
		hc.InitialDelaySeconds = int32(v)
	}
	if v, ok := intFromConfig(raw, "period_seconds", 0); ok {
		hc.PeriodSeconds = int32(v)
	}
	if v, ok := intFromConfig(raw, "timeout_seconds", 0); ok {
		hc.TimeoutSeconds = int32(v)
	}
	if v, ok := intFromConfig(raw, "success_threshold", 0); ok {
		hc.SuccessThreshold = int32(v)
	}
	if v, ok := intFromConfig(raw, "failure_threshold", 0); ok {
		hc.FailureThreshold = int32(v)
	}
	if v, ok := intFromConfig(raw, "port", 0); ok {
		hc.Port = int64(v)
	}
	return hc
}

// livenessHealthCheckFromConfig converts the canonical "liveness_check" map to a godo.HealthCheckSpec.
func livenessHealthCheckFromConfig(cfg map[string]any) *godo.HealthCheckSpec {
	raw, ok := cfg["liveness_check"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	hc := &godo.HealthCheckSpec{
		HTTPPath: strFromConfig(raw, "http_path", ""),
	}
	if v, ok := intFromConfig(raw, "initial_delay_seconds", 0); ok {
		hc.InitialDelaySeconds = int32(v)
	}
	if v, ok := intFromConfig(raw, "period_seconds", 0); ok {
		hc.PeriodSeconds = int32(v)
	}
	if v, ok := intFromConfig(raw, "timeout_seconds", 0); ok {
		hc.TimeoutSeconds = int32(v)
	}
	if v, ok := intFromConfig(raw, "success_threshold", 0); ok {
		hc.SuccessThreshold = int32(v)
	}
	if v, ok := intFromConfig(raw, "failure_threshold", 0); ok {
		hc.FailureThreshold = int32(v)
	}
	if v, ok := intFromConfig(raw, "port", 0); ok {
		hc.Port = int64(v)
	}
	return hc
}

// corsFromConfig converts the canonical "cors" map to a godo.AppCORSPolicy.
func corsFromConfig(cfg map[string]any) *godo.AppCORSPolicy {
	raw, ok := cfg["cors"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	policy := &godo.AppCORSPolicy{
		MaxAge: strFromConfig(raw, "max_age", ""),
	}
	if cred, ok := raw["allow_credentials"].(bool); ok {
		policy.AllowCredentials = cred
	}
	policy.AllowOrigins = stringMatchesFromConfig(raw, "allow_origins")
	policy.AllowMethods = stringsFromConfig(raw, "allow_methods")
	policy.AllowHeaders = stringsFromConfig(raw, "allow_headers")
	policy.ExposeHeaders = stringsFromConfig(raw, "expose_headers")
	if len(policy.AllowOrigins) == 0 && len(policy.AllowMethods) == 0 &&
		len(policy.AllowHeaders) == 0 && len(policy.ExposeHeaders) == 0 &&
		!policy.AllowCredentials && policy.MaxAge == "" {
		return nil
	}
	return policy
}

// autoscalingFromConfig converts the canonical "autoscaling" map to a godo.AppAutoscalingSpec.
func autoscalingFromConfig(cfg map[string]any) *godo.AppAutoscalingSpec {
	raw, ok := cfg["autoscaling"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	spec := &godo.AppAutoscalingSpec{}
	if v, ok := intFromConfig(raw, "min", 0); ok {
		spec.MinInstanceCount = int64(v)
	}
	if v, ok := intFromConfig(raw, "max", 0); ok {
		spec.MaxInstanceCount = int64(v)
	}
	if spec.MinInstanceCount == 0 && spec.MaxInstanceCount == 0 {
		return nil
	}
	if v, ok := intFromConfig(raw, "cpu_percent", 0); ok && v > 0 {
		spec.Metrics = &godo.AppAutoscalingSpecMetrics{
			CPU: &godo.AppAutoscalingSpecMetricCPU{Percent: int64(v)},
		}
	}
	return spec
}

// serviceTerminationFromConfig converts canonical "termination" to godo.AppServiceSpecTermination.
func serviceTerminationFromConfig(cfg map[string]any) *godo.AppServiceSpecTermination {
	raw, ok := cfg["termination"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	t := &godo.AppServiceSpecTermination{}
	if v, ok := intFromConfig(raw, "drain_seconds", 0); ok {
		t.DrainSeconds = int32(v)
	}
	if v, ok := intFromConfig(raw, "grace_period_seconds", 0); ok {
		t.GracePeriodSeconds = int32(v)
	}
	if t.DrainSeconds == 0 && t.GracePeriodSeconds == 0 {
		return nil
	}
	return t
}

// componentAlertsFromConfig converts the canonical "alerts" list to []*godo.AppAlertSpec.
// These alerts apply to the service component (CPU, memory, restart counts).
func componentAlertsFromConfig(cfg map[string]any) []*godo.AppAlertSpec {
	raw, ok := cfg["alerts"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]*godo.AppAlertSpec, 0, len(raw))
	for _, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		spec := alertSpecFromMap(m)
		if spec != nil {
			out = append(out, spec)
		}
	}
	return out
}

// appAlertsFromConfig builds app-level alerts (deployment events) from canonical "alerts".
// App-level alerts use a separate list on the AppSpec.
func appAlertsFromConfig(cfg map[string]any) []*godo.AppAlertSpec {
	// App-level alerts (DEPLOYMENT_FAILED, DEPLOYMENT_LIVE, etc.) are not in the canonical
	// schema; they come from provider_specific.digitalocean.app_alerts if present.
	ps, ok := cfg["provider_specific"].(map[string]any)
	if !ok {
		return nil
	}
	do, ok := ps["digitalocean"].(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := do["app_alerts"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]*godo.AppAlertSpec, 0, len(raw))
	for _, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		spec := alertSpecFromMap(m)
		if spec != nil {
			out = append(out, spec)
		}
	}
	return out
}

// alertSpecFromMap converts a canonical alert map to a godo.AppAlertSpec.
func alertSpecFromMap(m map[string]any) *godo.AppAlertSpec {
	rule := strFromConfig(m, "rule", "")
	if rule == "" {
		return nil
	}
	spec := &godo.AppAlertSpec{
		Rule: godo.AppAlertSpecRule(strings.ToUpper(rule)),
	}
	if op := strFromConfig(m, "operator", ""); op != "" {
		spec.Operator = godo.AppAlertSpecOperator(strings.ToUpper(op))
	}
	if win := strFromConfig(m, "window", ""); win != "" {
		spec.Window = godo.AppAlertSpecWindow(strings.ToUpper(win))
	}
	// YAML decode produces float64 for decimals and int for whole numbers;
	// accept all numeric types so alert thresholds work regardless of YAML representation.
	switch v := m["value"].(type) {
	case float64:
		spec.Value = float32(v)
	case float32:
		spec.Value = v
	case int:
		spec.Value = float32(v)
	case int64:
		spec.Value = float32(v)
	}
	if disabled, ok := m["disabled"].(bool); ok {
		spec.Disabled = disabled
	}
	return spec
}

// logDestinationsFromConfig converts the canonical "log_destinations" list to []*godo.AppLogDestinationSpec.
// Currently maps: endpoint (HTTP), papertrail endpoint, datadog api_key/endpoint, logtail token.
func logDestinationsFromConfig(cfg map[string]any) []*godo.AppLogDestinationSpec {
	raw, ok := cfg["log_destinations"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]*godo.AppLogDestinationSpec, 0, len(raw))
	for _, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		name := strFromConfig(m, "name", "")
		if name == "" {
			continue
		}
		dest := &godo.AppLogDestinationSpec{
			Name:     name,
			Endpoint: strFromConfig(m, "endpoint", ""),
		}
		if tls, ok := m["tls_insecure"].(bool); ok {
			dest.TLSInsecure = tls
		}
		// Provider-specific sub-specs.
		if pt, ok := m["papertrail"].(map[string]any); ok {
			dest.Papertrail = &godo.AppLogDestinationSpecPapertrail{
				Endpoint: strFromConfig(pt, "endpoint", ""),
			}
		}
		if dd, ok := m["datadog"].(map[string]any); ok {
			dest.Datadog = &godo.AppLogDestinationSpecDataDog{
				ApiKey:   strFromConfig(dd, "api_key", ""),
				Endpoint: strFromConfig(dd, "endpoint", ""),
			}
		}
		if lt, ok := m["logtail"].(map[string]any); ok {
			dest.Logtail = &godo.AppLogDestinationSpecLogtail{
				Token: strFromConfig(lt, "token", ""),
			}
		}
		out = append(out, dest)
	}
	return out
}

// domainsFromConfig converts the canonical "domains" list to []*godo.AppDomainSpec.
func domainsFromConfig(cfg map[string]any) []*godo.AppDomainSpec {
	raw, ok := cfg["domains"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]*godo.AppDomainSpec, 0, len(raw))
	for _, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		domain := strFromConfig(m, "domain", "")
		if domain == "" {
			continue
		}
		d := &godo.AppDomainSpec{
			Domain:            domain,
			Zone:              strFromConfig(m, "zone", ""),
			Certificate:       strFromConfig(m, "certificate", ""),
			MinimumTLSVersion: strFromConfig(m, "minimum_tls_version", ""),
		}
		if wc, ok := m["wildcard"].(bool); ok {
			d.Wildcard = wc
		}
		if t := strFromConfig(m, "type", ""); t != "" {
			d.Type = godo.AppDomainSpecType(strings.ToUpper(t))
		}
		out = append(out, d)
	}
	return out
}

// ingressFromConfig converts the canonical "ingress" map to a *godo.AppIngressSpec.
// The canonical ingress spec is minimal; complex routing should use provider_specific.
// Returns nil when no supported fields are present (consistent with CORS/autoscaling/maintenance).
func ingressFromConfig(cfg map[string]any) *godo.AppIngressSpec {
	raw, ok := cfg["ingress"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	spec := &godo.AppIngressSpec{}
	if lb := strFromConfig(raw, "load_balancer", ""); lb != "" {
		spec.LoadBalancer = godo.AppIngressSpecLoadBalancer(strings.ToUpper(lb))
	}
	// Rules are complex; skip for now unless coming from provider_specific.
	// Return nil when no supported field was set to avoid sending an empty ingress block.
	if spec.LoadBalancer == "" && len(spec.Rules) == 0 {
		return nil
	}
	return spec
}

// egressFromConfig converts the canonical "egress" map to a *godo.AppEgressSpec.
func egressFromConfig(cfg map[string]any) *godo.AppEgressSpec {
	raw, ok := cfg["egress"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	t := strFromConfig(raw, "type", "")
	if t == "" {
		return nil
	}
	return &godo.AppEgressSpec{
		Type: godo.AppEgressSpecType(strings.ToUpper(t)),
	}
}

// maintenanceFromConfig converts the canonical "maintenance" map to a *godo.AppMaintenanceSpec.
func maintenanceFromConfig(cfg map[string]any) *godo.AppMaintenanceSpec {
	raw, ok := cfg["maintenance"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	spec := &godo.AppMaintenanceSpec{
		OfflinePageURL: strFromConfig(raw, "offline_page_url", ""),
	}
	if enabled, ok := raw["enabled"].(bool); ok {
		spec.Enabled = enabled
	}
	if archive, ok := raw["archive"].(bool); ok {
		spec.Archive = archive
	}
	if !spec.Enabled && !spec.Archive && spec.OfflinePageURL == "" {
		return nil
	}
	return spec
}

// vpcFromConfig converts the canonical "vpc_ref" string to a *godo.AppVpcSpec.
func vpcFromConfig(cfg map[string]any) *godo.AppVpcSpec {
	vpcID := strFromConfig(cfg, "vpc_ref", "")
	if vpcID == "" {
		return nil
	}
	return &godo.AppVpcSpec{ID: vpcID}
}

// jobsFromConfig converts canonical "jobs" to []*godo.AppJobSpec.
func jobsFromConfig(cfg map[string]any) []*godo.AppJobSpec {
	raw, ok := cfg["jobs"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]*godo.AppJobSpec, 0, len(raw))
	for _, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		job := buildJobSpec(m)
		if job != nil {
			out = append(out, job)
		}
	}
	return out
}

// buildJobSpec converts a single canonical job map to a godo.AppJobSpec.
func buildJobSpec(m map[string]any) *godo.AppJobSpec {
	name := strFromConfig(m, "name", "")
	if name == "" {
		return nil
	}
	kind := mapJobKind(strFromConfig(m, "kind", ""))
	job := &godo.AppJobSpec{
		Name:             name,
		Kind:             kind,
		RunCommand:       strFromConfig(m, "run_command", ""),
		BuildCommand:     strFromConfig(m, "build_command", ""),
		DockerfilePath:   strFromConfig(m, "dockerfile_path", ""),
		SourceDir:        strFromConfig(m, "source_dir", ""),
		InstanceSizeSlug: strFromConfig(m, "instance_size_slug", ""),
		Envs:             envVarsFromJobConfig(m),
	}
	// Image from "image" field.
	if imgStr := strFromConfig(m, "image", ""); imgStr != "" {
		img, err := ParseImageRef(imgStr)
		if err == nil {
			job.Image = img
		}
	}
	// Scheduled jobs have a cron expression.
	if cron := strFromConfig(m, "cron", ""); cron != "" {
		job.Schedule = &godo.AppJobSpecSchedule{Cron: cron}
	}
	// Termination.
	if t, ok := m["termination"].(map[string]any); ok {
		if v, ok := intFromConfig(t, "grace_period_seconds", 0); ok {
			job.Termination = &godo.AppJobSpecTermination{GracePeriodSeconds: int32(v)}
		}
	}
	return job
}

// mapJobKind converts a canonical job kind string to a godo.AppJobSpecKind.
func mapJobKind(kind string) godo.AppJobSpecKind {
	switch strings.ToLower(kind) {
	case "pre_deploy":
		return godo.AppJobSpecKind_PreDeploy
	case "post_deploy":
		return godo.AppJobSpecKind_PostDeploy
	case "failed_deploy":
		return godo.AppJobSpecKind_FailedDeploy
	case "scheduled":
		return godo.AppJobSpecKind_Scheduled
	default:
		return godo.AppJobSpecKind_Unspecified
	}
}

// workersFromConfig converts canonical "workers" to []*godo.AppWorkerSpec.
func workersFromConfig(cfg map[string]any) []*godo.AppWorkerSpec {
	raw, ok := cfg["workers"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]*godo.AppWorkerSpec, 0, len(raw))
	for _, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		w := buildWorkerSpec(m)
		if w != nil {
			out = append(out, w)
		}
	}
	return out
}

// buildWorkerSpec converts a single canonical worker map to a godo.AppWorkerSpec.
func buildWorkerSpec(m map[string]any) *godo.AppWorkerSpec {
	name := strFromConfig(m, "name", "")
	if name == "" {
		return nil
	}
	instanceCount, _ := intFromConfig(m, "instance_count", 1)
	w := &godo.AppWorkerSpec{
		Name:             name,
		RunCommand:       strFromConfig(m, "run_command", ""),
		BuildCommand:     strFromConfig(m, "build_command", ""),
		DockerfilePath:   strFromConfig(m, "dockerfile_path", ""),
		SourceDir:        strFromConfig(m, "source_dir", ""),
		InstanceSizeSlug: strFromConfig(m, "instance_size_slug", ""),
		InstanceCount:    int64(instanceCount),
		Envs:             envVarsFromJobConfig(m),
		Autoscaling:      autoscalingFromConfig(m),
	}
	if imgStr := strFromConfig(m, "image", ""); imgStr != "" {
		img, err := ParseImageRef(imgStr)
		if err == nil {
			w.Image = img
		}
	}
	// size tier override.
	if size := strFromConfig(m, "size", ""); size != "" {
		if slug, ok := containerSizingMap[size]; ok {
			w.InstanceSizeSlug = slug
		}
	}
	// Termination (workers only have grace_period_seconds, not drain_seconds).
	if t, ok := m["termination"].(map[string]any); ok {
		wt := &godo.AppWorkerSpecTermination{}
		if v, ok := intFromConfig(t, "grace_period_seconds", 0); ok {
			wt.GracePeriodSeconds = int32(v)
		}
		if wt.GracePeriodSeconds != 0 {
			w.Termination = wt
		}
	}
	return w
}

// staticSitesFromConfig converts canonical "static_sites" to []*godo.AppStaticSiteSpec.
func staticSitesFromConfig(cfg map[string]any) []*godo.AppStaticSiteSpec {
	raw, ok := cfg["static_sites"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]*godo.AppStaticSiteSpec, 0, len(raw))
	for _, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		s := buildStaticSiteSpec(m)
		if s != nil {
			out = append(out, s)
		}
	}
	return out
}

// buildStaticSiteSpec converts a single canonical static site map to a godo.AppStaticSiteSpec.
func buildStaticSiteSpec(m map[string]any) *godo.AppStaticSiteSpec {
	name := strFromConfig(m, "name", "")
	if name == "" {
		return nil
	}
	return &godo.AppStaticSiteSpec{
		Name:             name,
		BuildCommand:     strFromConfig(m, "build_command", ""),
		OutputDir:        strFromConfig(m, "output_dir", ""),
		SourceDir:        strFromConfig(m, "source_dir", ""),
		DockerfilePath:   strFromConfig(m, "dockerfile_path", ""),
		IndexDocument:    strFromConfig(m, "index_document", ""),
		ErrorDocument:    strFromConfig(m, "error_document", ""),
		CatchallDocument: strFromConfig(m, "catchall_document", ""),
		Envs:             envVarsFromJobConfig(m),
		Routes:           routesFromConfig(m),
		CORS:             corsFromConfig(m),
	}
}

// envVarsFromJobConfig builds env var definitions from "env_vars" and "env_vars_secret" keys.
// It is used for jobs, workers, and static sites (which take the same envs format as services).
func envVarsFromJobConfig(cfg map[string]any) []*godo.AppVariableDefinition {
	return envVarsFromConfig(cfg)
}

// stringsFromConfig extracts a []string from a config key whose value is a []any of strings.
func stringsFromConfig(cfg map[string]any, key string) []string {
	raw, ok := cfg[key].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// sidecarsFromConfig converts canonical "sidecars" list to sibling []*godo.AppServiceSpec.
// DO App Platform has no native sidecar concept; each sidecar becomes an independent
// service component in the same app. Components communicate via the app's internal
// networking (platform-managed DNS/routing), not via a shared Linux network namespace.
// An invalid image ref in any sidecar is returned as an error so misconfiguration fails fast.
func sidecarsFromConfig(cfg map[string]any) ([]*godo.AppServiceSpec, error) {
	raw, ok := cfg["sidecars"].([]any)
	if !ok || len(raw) == 0 {
		return nil, nil
	}
	out := make([]*godo.AppServiceSpec, 0, len(raw))
	for _, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		name := strFromConfig(m, "name", "")
		if name == "" {
			continue
		}
		svc := &godo.AppServiceSpec{
			Name:       name,
			RunCommand: strFromConfig(m, "run_command", ""),
			Envs:       envVarsFromConfig(m),
		}
		if imgStr := strFromConfig(m, "image", ""); imgStr != "" {
			img, err := ParseImageRef(imgStr)
			if err != nil {
				return nil, fmt.Errorf("sidecar %q: %w", name, err)
			}
			svc.Image = img
		}
		// Map the first public port to HTTPPort if specified.
		if ports, ok := m["ports"].([]any); ok {
			for _, p := range ports {
				pm, ok := p.(map[string]any)
				if !ok {
					continue
				}
				pub, _ := pm["public"].(bool)
				if pub {
					if port, ok2 := intFromConfig(pm, "port", 0); ok2 && port > 0 {
						svc.HTTPPort = int64(port)
						break
					}
				}
			}
		}
		out = append(out, svc)
	}
	return out, nil
}

// stringMatchesFromConfig converts a []any of strings to []*godo.AppStringMatch using exact matching.
func stringMatchesFromConfig(cfg map[string]any, key string) []*godo.AppStringMatch {
	raw, ok := cfg[key].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make([]*godo.AppStringMatch, 0, len(raw))
	for _, v := range raw {
		switch t := v.(type) {
		case string:
			if t == "*" {
				out = append(out, &godo.AppStringMatch{Prefix: ""})
			} else {
				out = append(out, &godo.AppStringMatch{Exact: t})
			}
		case map[string]any:
			m := &godo.AppStringMatch{
				Exact:  strFromConfig(t, "exact", ""),
				Prefix: strFromConfig(t, "prefix", ""),
				Regex:  strFromConfig(t, "regex", ""),
			}
			out = append(out, m)
		}
	}
	return out
}
