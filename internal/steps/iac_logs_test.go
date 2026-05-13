package steps_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow/plugin/external/sdk"
	"github.com/digitalocean/godo"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/steps"
)

// ── fake app client ──────────────────────────────────────────────────────────

type fakeAppsClient struct {
	apps    []*godo.App
	logs    *godo.AppLogs
	logsErr error
	listErr error
}

func (f *fakeAppsClient) List(_ context.Context, _ *godo.ListOptions) ([]*godo.App, *godo.Response, error) {
	return f.apps, nil, f.listErr
}

func (f *fakeAppsClient) GetLogs(_ context.Context, _, _, _ string, _ godo.AppLogType, _ bool, _ int) (*godo.AppLogs, *godo.Response, error) {
	return f.logs, nil, f.logsErr
}

// ── factory tests ─────────────────────────────────────────────────────────────

func TestNewIaCLogsFactory_RegistersType(t *testing.T) {
	factory := steps.NewIaCLogsFactory(nil)
	if factory == nil {
		t.Fatal("factory must not be nil")
	}
}

func TestIaCLogsFactory_CreateStep_MissingModule(t *testing.T) {
	factory := steps.NewIaCLogsFactory(nil)
	_, err := factory.CreateStep("step.iac_logs", "my-step", map[string]any{})
	if err == nil {
		t.Fatal("expected error when 'module' config is missing")
	}
	if !strings.Contains(err.Error(), "module") {
		t.Errorf("error should mention 'module', got: %v", err)
	}
}

func TestIaCLogsFactory_CreateStep_EmptyModule(t *testing.T) {
	factory := steps.NewIaCLogsFactory(nil)
	_, err := factory.CreateStep("step.iac_logs", "my-step", map[string]any{"module": ""})
	if err == nil {
		t.Fatal("expected error when 'module' config is empty string")
	}
}

func TestIaCLogsFactory_CreateStep_ValidConfig(t *testing.T) {
	factory := steps.NewIaCLogsFactory(nil)
	inst, err := factory.CreateStep("step.iac_logs", "my-step", map[string]any{
		"module":   "my-app",
		"log_type": "BUILD",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inst == nil {
		t.Fatal("expected non-nil StepInstance")
	}
}

func TestIaCLogsFactory_CreateStep_Defaults(t *testing.T) {
	factory := steps.NewIaCLogsFactory(nil)
	inst, err := factory.CreateStep("step.iac_logs", "my-step", map[string]any{
		"module": "my-app",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Defaults should be accepted without error.
	if inst == nil {
		t.Fatal("expected non-nil StepInstance")
	}
}

// ── Execute tests ─────────────────────────────────────────────────────────────

// newTestStep constructs a step instance via the factory and returns it.
func newTestStep(t *testing.T, client steps.IaCLogsClient, cfg map[string]any) sdk.StepInstance {
	t.Helper()
	factory := steps.NewIaCLogsFactory(client)
	if _, ok := cfg["module"]; !ok {
		cfg["module"] = "my-app"
	}
	inst, err := factory.CreateStep("step.iac_logs", "my-step", cfg)
	if err != nil {
		t.Fatalf("CreateStep: %v", err)
	}
	return inst
}

func TestIaCLogsStep_Execute_NotInitialized(t *testing.T) {
	// nil client means the provider was not initialized yet.
	factory := steps.NewIaCLogsFactory(nil)
	inst, err := factory.CreateStep("step.iac_logs", "my-step", map[string]any{"module": "my-app"})
	if err != nil {
		t.Fatalf("CreateStep: %v", err)
	}
	_, execErr := inst.Execute(context.Background(), nil, nil, nil, nil, nil)
	if execErr == nil {
		t.Fatal("expected error when client is nil (provider not initialized)")
	}
}

func TestIaCLogsStep_Execute_AppNotFound(t *testing.T) {
	client := &fakeAppsClient{apps: []*godo.App{}} // no apps
	inst := newTestStep(t, client, map[string]any{"module": "missing-app"})
	_, err := inst.Execute(context.Background(), nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error when app not found")
	}
	if !strings.Contains(err.Error(), "missing-app") {
		t.Errorf("error should mention app name, got: %v", err)
	}
}

func TestIaCLogsStep_Execute_ListError(t *testing.T) {
	listErr := errors.New("API error: unauthorized")
	client := &fakeAppsClient{listErr: listErr}
	inst := newTestStep(t, client, map[string]any{"module": "my-app"})
	_, err := inst.Execute(context.Background(), nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error propagation from List")
	}
	if !errors.Is(err, listErr) {
		t.Errorf("expected wrapped listErr, got: %v", err)
	}
}

func TestIaCLogsStep_Execute_GetLogsError(t *testing.T) {
	logsErr := errors.New("godo: 503 service unavailable")
	client := &fakeAppsClient{
		apps:    []*godo.App{{ID: "abc-123", Spec: &godo.AppSpec{Name: "my-app"}}},
		logsErr: logsErr,
	}
	inst := newTestStep(t, client, map[string]any{"module": "my-app"})
	_, err := inst.Execute(context.Background(), nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error propagation from GetLogs")
	}
	if !errors.Is(err, logsErr) {
		t.Errorf("expected wrapped logsErr, got: %v", err)
	}
}

func TestIaCLogsStep_Execute_NoHistoricURLs(t *testing.T) {
	// GetLogs returns an AppLogs with only a LiveURL (no HistoricURLs).
	client := &fakeAppsClient{
		apps: []*godo.App{{ID: "abc-123", Spec: &godo.AppSpec{Name: "my-app"}}},
		logs: &godo.AppLogs{LiveURL: "wss://example.com/logs"},
	}
	inst := newTestStep(t, client, map[string]any{"module": "my-app"})
	result, err := inst.Execute(context.Background(), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	output := result.Output
	if output["log_url"] != "wss://example.com/logs" {
		t.Errorf("log_url should be the LiveURL, got: %v", output["log_url"])
	}
	if output["logs"] != "" {
		t.Errorf("logs should be empty when no HistoricURLs, got: %v", output["logs"])
	}
}

func TestIaCLogsStep_Execute_WithHistoricURL(t *testing.T) {
	// Start a test HTTP server that serves fake log content.
	logBody := "line1\nline2\nline3\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(logBody))
	}))
	defer srv.Close()

	client := &fakeAppsClient{
		apps: []*godo.App{{ID: "abc-123", Spec: &godo.AppSpec{Name: "my-app"}}},
		logs: &godo.AppLogs{
			LiveURL:      "wss://example.com/logs",
			HistoricURLs: []string{srv.URL},
		},
	}
	inst := newTestStep(t, client, map[string]any{
		"module":     "my-app",
		"tail_lines": 100,
	})
	result, err := inst.Execute(context.Background(), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := result.Output
	if !strings.Contains(output["logs"].(string), "line1") {
		t.Errorf("logs should contain fetched content, got: %v", output["logs"])
	}
	if output["log_url"] != "wss://example.com/logs" {
		t.Errorf("log_url should be LiveURL, got: %v", output["log_url"])
	}
	// No component_name was specified → 0 means "all components (aggregate)".
	// component_count is emitted as float64 to survive structpb round-trips.
	if output["component_count"].(float64) != 0 {
		t.Errorf("component_count should be 0 (all components) when no component_name, got: %v", output["component_count"])
	}
}

func TestIaCLogsStep_Execute_WithComponentName(t *testing.T) {
	// component_name is forwarded to GetLogs.
	var capturedComponent string
	capturedClient := &captureLogsClient{
		app:       &godo.App{ID: "abc-123", Spec: &godo.AppSpec{Name: "my-app"}},
		captureComponent: func(component string) {
			capturedComponent = component
		},
	}
	inst := newTestStep(t, capturedClient, map[string]any{
		"module":         "my-app",
		"component_name": "web",
	})
	_, err := inst.Execute(context.Background(), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected Execute error: %v", err)
	}
	if capturedComponent != "web" {
		t.Errorf("expected component_name 'web' forwarded to GetLogs, got %q", capturedComponent)
	}
}

func TestIaCLogsStep_Execute_LogTypeDefault(t *testing.T) {
	// Default log_type is RUN.
	var capturedLogType godo.AppLogType
	capturedClient := &captureLogsClient{
		app: &godo.App{ID: "abc-123", Spec: &godo.AppSpec{Name: "my-app"}},
		captureLogType: func(lt godo.AppLogType) {
			capturedLogType = lt
		},
	}
	inst := newTestStep(t, capturedClient, map[string]any{"module": "my-app"})
	_, err := inst.Execute(context.Background(), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected Execute error: %v", err)
	}
	if capturedLogType != godo.AppLogTypeRun {
		t.Errorf("default log_type should be RUN, got %q", capturedLogType)
	}
}

func TestIaCLogsStep_Execute_LogTypeBuild(t *testing.T) {
	var capturedLogType godo.AppLogType
	capturedClient := &captureLogsClient{
		app: &godo.App{ID: "abc-123", Spec: &godo.AppSpec{Name: "my-app"}},
		captureLogType: func(lt godo.AppLogType) {
			capturedLogType = lt
		},
	}
	inst := newTestStep(t, capturedClient, map[string]any{
		"module":   "my-app",
		"log_type": "BUILD",
	})
	_, err := inst.Execute(context.Background(), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected Execute error: %v", err)
	}
	if capturedLogType != godo.AppLogTypeBuild {
		t.Errorf("expected log_type BUILD, got %q", capturedLogType)
	}
}

// OutputStructure validates that all documented output keys are present.
func TestIaCLogsStep_Execute_OutputStructure(t *testing.T) {
	client := &fakeAppsClient{
		apps: []*godo.App{{ID: "abc-123", Spec: &godo.AppSpec{Name: "my-app"}}},
		logs: &godo.AppLogs{LiveURL: "wss://example.com/logs"},
	}
	inst := newTestStep(t, client, map[string]any{"module": "my-app"})
	result, err := inst.Execute(context.Background(), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, key := range []string{"logs", "log_url", "component_count"} {
		if _, ok := result.Output[key]; !ok {
			t.Errorf("output missing key %q", key)
		}
	}
}

// ── captureLogsClient ─────────────────────────────────────────────────────────

// captureLogsClient is a test client that records arguments passed to GetLogs.
type captureLogsClient struct {
	app              *godo.App
	captureComponent func(string)
	captureLogType   func(godo.AppLogType)
}

func (c *captureLogsClient) List(_ context.Context, _ *godo.ListOptions) ([]*godo.App, *godo.Response, error) {
	return []*godo.App{c.app}, nil, nil
}

func (c *captureLogsClient) GetLogs(_ context.Context, _, _, component string, logType godo.AppLogType, _ bool, _ int) (*godo.AppLogs, *godo.Response, error) {
	if c.captureComponent != nil {
		c.captureComponent(component)
	}
	if c.captureLogType != nil {
		c.captureLogType(logType)
	}
	return &godo.AppLogs{}, nil, nil
}
