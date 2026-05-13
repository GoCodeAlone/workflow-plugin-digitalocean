// Package steps provides pipeline step factories for the DigitalOcean plugin.
//
// Each step factory implements sdk.StepProvider.CreateStep and returns an
// sdk.StepInstance. Steps are wired into doIaCServer.stepRouter so the gRPC
// PluginService surface (GetStepTypes / CreateStep / ExecuteStep / DestroyStep)
// can dispatch them.
//
// Migration note: step.iac_logs replaces the removed step.do_logs from
// workflow core (deleted in commit 589ef78e, issue #617). Behavioral
// differences from the old step.do_logs:
//
//   - Config key is "module" (infra.container_service name) not "app".
//   - Adds log_type, component_name, and tail_lines knobs.
//   - AppLogs.LiveURL is returned in output but the step does NOT stream
//     live log output — it fetches HistoricURLs only (same limitation as
//     the troubleshoot path in app_platform.go).
//   - No deployment ID routing: step.iac_logs fetches runtime or build
//     logs at the app level (latest deployment). If a specific deployment
//     ID is needed, use a future step.iac_logs_deployment (not yet
//     implemented).
package steps

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/GoCodeAlone/workflow/plugin/external/sdk"
	"github.com/digitalocean/godo"
)

// IaCLogsClient is the minimal godo App API surface consumed by step.iac_logs.
// It is satisfied by *godo.AppsServiceOp (the real godo client) and by test
// fakes. Only List and GetLogs are needed — no Create/Update/Delete.
type IaCLogsClient interface {
	List(ctx context.Context, opts *godo.ListOptions) ([]*godo.App, *godo.Response, error)
	GetLogs(ctx context.Context, appID, deploymentID, component string, logType godo.AppLogType, follow bool, tailLines int) (*godo.AppLogs, *godo.Response, error)
}

// defaultLogTailLines is the number of log lines returned when the step
// config omits tail_lines.
const defaultLogTailLines = 100

// iacLogsStepConfig holds the parsed per-step configuration for step.iac_logs.
type iacLogsStepConfig struct {
	module        string
	logType       godo.AppLogType
	componentName string
	tailLines     int
	follow        bool
}

// iacLogsStep is the sdk.StepInstance for step.iac_logs.
type iacLogsStep struct {
	client IaCLogsClient
	cfg    iacLogsStepConfig
}

// IaCLogsFactory is the sdk.StepProvider factory for "step.iac_logs".
// client is the godo Apps client from the initialized DO provider. When client
// is nil (provider not yet initialized) the step's Execute returns an error.
type IaCLogsFactory struct {
	client IaCLogsClient
}

// NewIaCLogsFactory returns the step factory for step.iac_logs.
// client must be the same IaCLogsClient used by the provider at execution time.
// Pass nil only in tests that want to verify the not-initialized error path.
func NewIaCLogsFactory(client IaCLogsClient) *IaCLogsFactory {
	return &IaCLogsFactory{client: client}
}

// CreateStep implements sdk.StepProvider.
func (f *IaCLogsFactory) CreateStep(typeName, _ string, config map[string]any) (sdk.StepInstance, error) {
	if typeName != "step.iac_logs" {
		return nil, fmt.Errorf("iac_logs factory: unsupported step type %q", typeName)
	}

	moduleName, _ := config["module"].(string)
	if moduleName == "" {
		return nil, fmt.Errorf("step.iac_logs: 'module' is required (name of an infra.container_service resource)")
	}

	logTypeStr, _ := config["log_type"].(string)
	if logTypeStr == "" {
		logTypeStr = "RUN"
	}
	lt, err := parseLogType(logTypeStr)
	if err != nil {
		return nil, fmt.Errorf("step.iac_logs: %w", err)
	}

	componentName, _ := config["component_name"].(string)

	tailLines := defaultLogTailLines
	if tl, ok := intFromConfig(config, "tail_lines"); ok && tl > 0 {
		tailLines = tl
	}

	follow, _ := config["follow"].(bool)

	return &iacLogsStep{
		client: f.client,
		cfg: iacLogsStepConfig{
			module:        moduleName,
			logType:       lt,
			componentName: componentName,
			tailLines:     tailLines,
			follow:        follow,
		},
	}, nil
}

// Execute implements sdk.StepInstance.
//
// Workflow:
//  1. List DO apps and find the one whose Spec.Name matches cfg.module.
//  2. Call godo Apps.GetLogs for the found app ID.
//  3. Fetch the first HistoricURL (newest first) to retrieve log text.
//  4. Return logs, log_url (LiveURL), and component_count.
//
// The step does NOT follow live logs (cfg.follow is not honoured beyond
// passing it to GetLogs): streaming is not feasible inside a pipeline step.
func (s *iacLogsStep) Execute(ctx context.Context, _ map[string]any, _ map[string]map[string]any, _ map[string]any, _ map[string]any, _ map[string]any) (*sdk.StepResult, error) {
	if s.client == nil {
		return nil, fmt.Errorf("step.iac_logs: DO provider not initialized (client is nil)")
	}

	// 1. Resolve app ID from app name.
	appID, err := resolveAppID(ctx, s.client, s.cfg.module)
	if err != nil {
		return nil, fmt.Errorf("step.iac_logs: %w", err)
	}

	// 2. Fetch log metadata from DO API.
	// deploymentID = "" → latest deployment (DO API default).
	appLogs, _, err := s.client.GetLogs(ctx, appID, "", s.cfg.componentName, s.cfg.logType, s.cfg.follow, s.cfg.tailLines)
	if err != nil {
		return nil, fmt.Errorf("step.iac_logs: GetLogs app=%s: %w", s.cfg.module, err)
	}

	// Component count: 1 when a specific component is requested, 0 means "all".
	// Emitted as float64 so the value round-trips correctly through structpb.NewStruct
	// (which encodes all numbers as float64 in JSON-derived proto Struct values).
	var componentCount float64 = 1
	if s.cfg.componentName == "" {
		componentCount = 0 // all components aggregated
	}

	liveURL := ""
	if appLogs != nil {
		liveURL = appLogs.LiveURL
	}

	// 3. Fetch actual log text from the first historic URL.
	var logText string
	if appLogs != nil && len(appLogs.HistoricURLs) > 0 {
		text, fetchErr := fetchLogContent(ctx, appLogs.HistoricURLs[0], s.cfg.tailLines)
		if fetchErr != nil {
			// Non-fatal: return partial output with empty logs rather than
			// failing the whole step. The caller can inspect log_url to
			// retrieve logs manually.
			logText = ""
		} else {
			logText = text
		}
	}

	return &sdk.StepResult{
		Output: map[string]any{
			"logs":            logText,
			"log_url":         liveURL,
			"component_count": componentCount,
		},
	}, nil
}

// resolveAppID lists DO apps (paginated) and returns the ID of the app whose
// Spec.Name matches appName. Returns an error if not found or if any List
// call fails. Each page fetches up to 200 apps; iteration stops when the
// returned page is shorter than PerPage (last page) or the app is found.
func resolveAppID(ctx context.Context, client IaCLogsClient, appName string) (string, error) {
	const perPage = 200
	for page := 1; ; page++ {
		apps, _, err := client.List(ctx, &godo.ListOptions{Page: page, PerPage: perPage})
		if err != nil {
			return "", err
		}
		for _, app := range apps {
			if app.Spec != nil && app.Spec.Name == appName {
				return app.ID, nil
			}
		}
		if len(apps) < perPage {
			break // last page
		}
	}
	return "", fmt.Errorf("app %q not found in DigitalOcean App Platform", appName)
}

// logFetchClient is an http.Client with a 30 s per-request timeout used when
// fetching log content from DO's presigned HistoricURLs. 30 s provides generous
// headroom for slow S3 presigned URL responses without hanging pipelines.
var logFetchClient = &http.Client{Timeout: 30 * time.Second}

// fetchLogContent fetches the first log URL and returns the last tailLines
// lines of the response body. A 10 MB cap prevents pathological responses.
// URL is a presigned URL and MUST NOT be logged verbatim (to avoid leaking
// signed AWS-style credentials). Errors reference the failure class only.
func fetchLogContent(ctx context.Context, url string, tailLines int) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("log request: invalid URL shape")
	}
	resp, err := logFetchClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("log fetch: network error")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("log fetch: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return "", fmt.Errorf("log fetch: read body: %w", err)
	}
	return tailString(string(body), tailLines), nil
}

// tailString returns the last n lines of s. When s has fewer than n lines the
// entire string is returned unchanged. Empty lines are preserved (not filtered).
func tailString(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// parseLogType validates and converts a log_type string to godo.AppLogType.
func parseLogType(s string) (godo.AppLogType, error) {
	switch strings.ToUpper(s) {
	case "BUILD":
		return godo.AppLogTypeBuild, nil
	case "DEPLOY":
		return godo.AppLogTypeDeploy, nil
	case "RUN":
		return godo.AppLogTypeRun, nil
	case "RUN_RESTARTED":
		return godo.AppLogTypeRunRestarted, nil
	default:
		return "", fmt.Errorf("unsupported log_type %q; valid values: BUILD, DEPLOY, RUN, RUN_RESTARTED", s)
	}
}

// intFromConfig extracts an int from a map[string]any config value. Handles
// int, int64, and float64 (JSON numbers are decoded as float64 by encoding/json).
func intFromConfig(config map[string]any, key string) (int, bool) {
	v, ok := config[key]
	if !ok {
		return 0, false
	}
	switch tv := v.(type) {
	case int:
		return tv, true
	case int64:
		return int(tv), true
	case float64:
		return int(tv), true
	}
	return 0, false
}
