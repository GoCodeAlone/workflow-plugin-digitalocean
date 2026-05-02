package drivers

// White-box tests for Troubleshoot helpers.
// Using package drivers (not drivers_test) so unexported helpers are accessible.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// ── stub AppPlatformClient ──────────────────────────────────────────────────

type stubAppClient struct {
	app        *godo.App
	appErr     error
	deps       []*godo.Deployment
	depsErr    error
	logs       *godo.AppLogs
	logsErr    error
}

func (s *stubAppClient) Create(_ context.Context, _ *godo.AppCreateRequest) (*godo.App, *godo.Response, error) {
	return nil, nil, errors.New("not implemented in stub")
}
func (s *stubAppClient) Get(_ context.Context, _ string) (*godo.App, *godo.Response, error) {
	return s.app, &godo.Response{Response: &http.Response{StatusCode: 200}}, s.appErr
}
func (s *stubAppClient) List(_ context.Context, _ *godo.ListOptions) ([]*godo.App, *godo.Response, error) {
	return nil, nil, errors.New("not implemented in stub")
}
func (s *stubAppClient) Update(_ context.Context, _ string, _ *godo.AppUpdateRequest) (*godo.App, *godo.Response, error) {
	return nil, nil, errors.New("not implemented in stub")
}
func (s *stubAppClient) CreateDeployment(_ context.Context, _ string, _ ...*godo.DeploymentCreateRequest) (*godo.Deployment, *godo.Response, error) {
	return nil, nil, errors.New("not implemented in stub")
}
func (s *stubAppClient) ListDeployments(_ context.Context, _ string, _ *godo.ListOptions) ([]*godo.Deployment, *godo.Response, error) {
	return s.deps, &godo.Response{}, s.depsErr
}
func (s *stubAppClient) Delete(_ context.Context, _ string) (*godo.Response, error) {
	return nil, errors.New("not implemented in stub")
}
func (s *stubAppClient) GetLogs(_ context.Context, _, _, _ string, _ godo.AppLogType, _ bool, _ int) (*godo.AppLogs, *godo.Response, error) {
	return s.logs, nil, s.logsErr
}

// ── Troubleshoot tests ─────────────────────────────────────────────────────

func TestTroubleshoot_EmptyProviderID(t *testing.T) {
	d := NewAppPlatformDriverWithClient(&stubAppClient{}, "nyc3")
	diags, err := d.Troubleshoot(context.Background(), interfaces.ResourceRef{Name: "my-app", ProviderID: ""}, "")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if diags != nil {
		t.Fatalf("expected nil diagnostics, got %v", diags)
	}
}

func TestTroubleshoot_GetAppError(t *testing.T) {
	stub := &stubAppClient{appErr: errors.New("network error")}
	d := NewAppPlatformDriverWithClient(stub, "nyc3")
	_, err := d.Troubleshoot(context.Background(), interfaces.ResourceRef{Name: "my-app", ProviderID: "app-123"}, "")
	if err == nil {
		t.Fatal("expected error from Get, got nil")
	}
}

func TestTroubleshoot_NilApp(t *testing.T) {
	stub := &stubAppClient{app: nil}
	d := NewAppPlatformDriverWithClient(stub, "nyc3")
	diags, err := d.Troubleshoot(context.Background(), interfaces.ResourceRef{Name: "my-app", ProviderID: "app-123"}, "")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if diags != nil {
		t.Fatalf("expected nil diagnostics for nil app, got %v", diags)
	}
}

func TestTroubleshoot_ActiveApp_NoCandidates(t *testing.T) {
	// A cleanly-active app with no InProgress/Pending slot and no
	// historical deps returns an empty slice, not an error.
	app := &godo.App{
		ID: "app-123",
		Spec: &godo.AppSpec{Name: "my-app"},
		ActiveDeployment: &godo.Deployment{
			ID:    "dep-active",
			Phase: godo.DeploymentPhase_Active,
		},
	}
	stub := &stubAppClient{app: app, deps: nil}
	d := NewAppPlatformDriverWithClient(stub, "nyc3")
	diags, err := d.Troubleshoot(context.Background(), interfaces.ResourceRef{Name: "my-app", ProviderID: "app-123"}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Active deployment with no error SummarySteps → buildDiagnosticFor returns nil.
	if len(diags) != 0 {
		t.Fatalf("expected 0 diagnostics for active healthy app, got %d: %+v", len(diags), diags)
	}
}

func TestTroubleshoot_InProgressDeploymentError(t *testing.T) {
	dep := &godo.Deployment{
		ID:        "dep-err",
		Phase:     godo.DeploymentPhase_Error,
		Cause:     "workflow-migrate up: first .: file does not exist",
		CreatedAt: time.Now().UTC(),
	}
	app := &godo.App{
		ID:                   "app-123",
		Spec:                 &godo.AppSpec{Name: "my-app"},
		InProgressDeployment: dep,
	}
	stub := &stubAppClient{app: app, deps: nil}
	d := NewAppPlatformDriverWithClient(stub, "nyc3")
	diags, err := d.Troubleshoot(context.Background(), interfaces.ResourceRef{Name: "my-app", ProviderID: "app-123"}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d: %+v", len(diags), diags)
	}
	if diags[0].ID != "dep-err" {
		t.Errorf("unexpected ID: %s", diags[0].ID)
	}
	if diags[0].Cause == "" {
		t.Error("Cause should not be empty")
	}
}

func TestTroubleshoot_SummaryStepFailure(t *testing.T) {
	dep := &godo.Deployment{
		ID:        "dep-123",
		Phase:     godo.DeploymentPhase_Error,
		CreatedAt: time.Now().UTC(),
		Progress: &godo.DeploymentProgress{
			SummarySteps: []*godo.DeploymentProgressStep{
				{
					Name:   "pre_deploy",
					Status: godo.DeploymentProgressStepStatus_Error,
					Reason: &godo.DeploymentProgressStepReason{
						Message: "exit status 1: workflow-migrate up failed",
					},
				},
			},
		},
	}
	app := &godo.App{
		ID:                   "app-123",
		Spec:                 &godo.AppSpec{Name: "my-app"},
		InProgressDeployment: dep,
	}
	stub := &stubAppClient{app: app, deps: nil}
	d := NewAppPlatformDriverWithClient(stub, "nyc3")
	diags, err := d.Troubleshoot(context.Background(), interfaces.ResourceRef{Name: "my-app", ProviderID: "app-123"}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(diags))
	}
	if diags[0].Phase != "pre_deploy" {
		t.Errorf("expected phase 'pre_deploy', got %q", diags[0].Phase)
	}
	if diags[0].Cause == "" {
		t.Error("Cause should not be empty from SummaryStep reason")
	}
}

func TestTroubleshoot_ListDeploymentsError_BestEffort(t *testing.T) {
	// ListDeployments error should be ignored; we still return diagnostics
	// derived from the app's deployment slots.
	dep := &godo.Deployment{
		ID:    "dep-err",
		Phase: godo.DeploymentPhase_Error,
		Cause: "out of memory",
	}
	app := &godo.App{
		ID:                   "app-123",
		Spec:                 &godo.AppSpec{Name: "my-app"},
		InProgressDeployment: dep,
	}
	stub := &stubAppClient{
		app:     app,
		depsErr: errors.New("API unavailable"),
	}
	d := NewAppPlatformDriverWithClient(stub, "nyc3")
	diags, err := d.Troubleshoot(context.Background(), interfaces.ResourceRef{Name: "my-app", ProviderID: "app-123"}, "")
	if err != nil {
		t.Fatalf("ListDeployments error should not propagate; got: %v", err)
	}
	if len(diags) == 0 {
		t.Error("expected diagnostics from InProgressDeployment despite ListDeployments error")
	}
}

func TestTroubleshoot_HistoricalDeploymentUsed(t *testing.T) {
	// No deployment slots on the app, but ListDeployments has an errored one.
	hist := []*godo.Deployment{
		{ID: "dep-hist", Phase: godo.DeploymentPhase_Error, Cause: "build failed"},
	}
	app := &godo.App{ID: "app-123", Spec: &godo.AppSpec{Name: "my-app"}}
	stub := &stubAppClient{app: app, deps: hist}
	d := NewAppPlatformDriverWithClient(stub, "nyc3")
	diags, err := d.Troubleshoot(context.Background(), interfaces.ResourceRef{Name: "my-app", ProviderID: "app-123"}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(diags) != 1 || diags[0].ID != "dep-hist" {
		t.Fatalf("expected dep-hist from history, got %+v", diags)
	}
}

// ── pickTroubleshootDeployments tests ──────────────────────────────────────

func TestPickTroubleshootDeployments_PrioritizesInProgress(t *testing.T) {
	inProgress := &godo.Deployment{ID: "in-progress"}
	pending := &godo.Deployment{ID: "pending"}
	active := &godo.Deployment{ID: "active"}
	hist := []*godo.Deployment{{ID: "hist-1"}}

	app := &godo.App{
		InProgressDeployment: inProgress,
		PendingDeployment:    pending,
		ActiveDeployment:     active,
	}
	result := pickTroubleshootDeployments(app, hist)
	if len(result) == 0 || result[0].ID != "in-progress" {
		t.Errorf("expected InProgress first, got %+v", result)
	}
	if result[1].ID != "pending" {
		t.Errorf("expected Pending second, got %+v", result[1])
	}
	if result[2].ID != "active" {
		t.Errorf("expected Active third, got %+v", result[2])
	}
	// hist-1 should not appear: already at 3 items
	if len(result) > 3 {
		t.Errorf("expected max 3, got %d", len(result))
	}
}

func TestPickTroubleshootDeployments_DeduplicatesIDs(t *testing.T) {
	dep := &godo.Deployment{ID: "dup"}
	app := &godo.App{
		InProgressDeployment: dep,
		PendingDeployment:    dep, // same pointer / ID
	}
	hist := []*godo.Deployment{dep}
	result := pickTroubleshootDeployments(app, hist)
	if len(result) != 1 {
		t.Errorf("expected 1 (deduped), got %d: %+v", len(result), result)
	}
}

func TestPickTroubleshootDeployments_NilSlotsUsesHistory(t *testing.T) {
	app := &godo.App{ID: "app-123"}
	hist := []*godo.Deployment{
		{ID: "h1"}, {ID: "h2"}, {ID: "h3"}, {ID: "h4"},
	}
	result := pickTroubleshootDeployments(app, hist)
	if len(result) != 3 {
		t.Errorf("expected 3 from history, got %d", len(result))
	}
}

// ── buildDiagnosticFor tests ──────────────────────────────────────────────

func TestBuildDiagnosticFor_ErrorPhase_ReturnsDiagnostic(t *testing.T) {
	dep := &godo.Deployment{
		ID:        "dep-1",
		Phase:     godo.DeploymentPhase_Error,
		Cause:     "build failed",
		CreatedAt: time.Now().UTC(),
	}
	diag := buildDiagnosticFor(dep)
	if diag == nil {
		t.Fatal("expected non-nil diagnostic for ERROR phase")
	}
	if diag.ID != "dep-1" {
		t.Errorf("expected ID dep-1, got %s", diag.ID)
	}
	if diag.Cause != "build failed" {
		t.Errorf("unexpected Cause: %q", diag.Cause)
	}
}

func TestBuildDiagnosticFor_ActivePhase_ReturnsNil(t *testing.T) {
	dep := &godo.Deployment{
		ID:    "dep-ok",
		Phase: godo.DeploymentPhase_Active,
	}
	if diag := buildDiagnosticFor(dep); diag != nil {
		t.Errorf("expected nil for ACTIVE phase, got %+v", diag)
	}
}

func TestBuildDiagnosticFor_SummaryStepPhaseNameUsed(t *testing.T) {
	dep := &godo.Deployment{
		ID:    "dep-2",
		Phase: godo.DeploymentPhase_Error,
		Progress: &godo.DeploymentProgress{
			SummarySteps: []*godo.DeploymentProgressStep{
				{
					Name:   "build",
					Status: godo.DeploymentProgressStepStatus_Error,
					Reason: &godo.DeploymentProgressStepReason{Message: "image pull failed"},
				},
			},
		},
	}
	diag := buildDiagnosticFor(dep)
	if diag == nil {
		t.Fatal("expected non-nil diagnostic")
	}
	if diag.Phase != "build" {
		t.Errorf("expected phase 'build' from SummaryStep, got %q", diag.Phase)
	}
}

func TestBuildDiagnosticFor_LeafStepFallback(t *testing.T) {
	dep := &godo.Deployment{
		ID:    "dep-3",
		Phase: godo.DeploymentPhase_Error,
		Progress: &godo.DeploymentProgress{
			SummarySteps: nil, // no summary steps
			Steps: []*godo.DeploymentProgressStep{
				{
					Status: godo.DeploymentProgressStepStatus_Error,
					Reason: &godo.DeploymentProgressStepReason{Message: "exit status 2"},
				},
			},
		},
	}
	diag := buildDiagnosticFor(dep)
	if diag == nil {
		t.Fatal("expected non-nil diagnostic from leaf step")
	}
	if diag.Cause == "" {
		t.Error("expected non-empty Cause from leaf step")
	}
}

func TestBuildDiagnosticFor_CanceledPhase_ReturnsDiagnostic(t *testing.T) {
	dep := &godo.Deployment{
		ID:    "dep-canceled",
		Phase: godo.DeploymentPhase_Canceled,
	}
	if diag := buildDiagnosticFor(dep); diag == nil {
		t.Error("expected non-nil diagnostic for CANCELED phase")
	}
}

// ── extractCause tests ──────────────────────────────────────────────────────

func TestExtractCause_TableDriven(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			name: "go panic",
			in:   "some output\npanic: runtime error: index out of range\ngoroutine 1",
			want: "panic: runtime error: index out of range",
		},
		{
			name: "exit status line",
			in:   "running migrations\nError: exit status 1\ndone",
			want: "Error: exit status 1",
		},
		{
			name: "failed to pattern",
			in:   "starting\nfailed to connect to database\n",
			want: "failed to connect to database",
		},
		{
			name: "fatal uppercase",
			in:   "FATAL: configuration file error",
			want: "FATAL: configuration file error",
		},
		{
			name: "no pattern fallback to last line",
			in:   "line one\nline two\ngoodbye world",
			want: "goodbye world",
		},
		{
			name: "empty input",
			in:   "",
			want: "",
		},
		{
			name: "whitespace only",
			in:   "   \n  \n",
			want: "",
		},
		{
			name: "single error line",
			in:   "error: something went wrong",
			want: "error: something went wrong",
		},
		{
			name: "exit code variant",
			in:   "process exited with exit code 127",
			want: "process exited with exit code 127",
		},
		{
			name: "trimmed whitespace",
			in:   "  Error: too many connections  ",
			want: "Error: too many connections",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractCause(c.in)
			if got != c.want {
				t.Errorf("extractCause(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// ── deploymentCauseAndPhase tests ──────────────────────────────────────────

func TestDeploymentCauseAndPhase_NoProgress(t *testing.T) {
	dep := &godo.Deployment{
		ID:    "dep-1",
		Phase: godo.DeploymentPhase_Error,
		Cause: "image not found",
	}
	cause, phase := deploymentCauseAndPhase(dep)
	if cause != "image not found" {
		t.Errorf("expected cause %q, got %q", "image not found", cause)
	}
	if phase != string(godo.DeploymentPhase_Error) {
		t.Errorf("expected phase %q, got %q", string(godo.DeploymentPhase_Error), phase)
	}
}

func TestDeploymentCauseAndPhase_SummaryStepNoReason(t *testing.T) {
	// SummaryStep error with no Reason.Message; cause falls back to dep.Cause
	// but the step's Name is still used as phase since the SummaryStep matched.
	dep := &godo.Deployment{
		ID:    "dep-1",
		Phase: godo.DeploymentPhase_Error,
		Cause: "deployment timed out",
		Progress: &godo.DeploymentProgress{
			SummarySteps: []*godo.DeploymentProgressStep{
				{Name: "deploy", Status: godo.DeploymentProgressStepStatus_Error, Reason: nil},
			},
		},
	}
	cause, phase := deploymentCauseAndPhase(dep)
	if cause != "deployment timed out" {
		t.Errorf("expected fallback to dep.Cause, got %q", cause)
	}
	// Phase comes from the matching SummaryStep name, not dep.Phase.
	if phase != "deploy" {
		t.Errorf("expected phase %q from SummaryStep name, got %q", "deploy", phase)
	}
}

func TestDeploymentCauseAndPhase_ActivePhase_EmptyCause(t *testing.T) {
	dep := &godo.Deployment{
		ID:    "dep-ok",
		Phase: godo.DeploymentPhase_Active,
	}
	cause, _ := deploymentCauseAndPhase(dep)
	if cause != "" {
		t.Errorf("expected empty cause for ACTIVE phase, got %q", cause)
	}
}

func TestDeploymentCauseAndPhase_LeafStepFallback_NoCause(t *testing.T) {
	// dep.Cause is empty and SummarySteps has no error; cause must come from
	// Progress.Steps leaf step Reason.Message.
	dep := &godo.Deployment{
		ID:    "dep-leaf",
		Phase: godo.DeploymentPhase_Error,
		Cause: "", // explicitly empty
		Progress: &godo.DeploymentProgress{
			SummarySteps: nil,
			Steps: []*godo.DeploymentProgressStep{
				{
					Status: godo.DeploymentProgressStepStatus_Error,
					Reason: &godo.DeploymentProgressStepReason{Message: "exit status 2"},
				},
			},
		},
	}
	cause, phase := deploymentCauseAndPhase(dep)
	if cause == "" {
		t.Fatal("expected cause from Progress.Steps, got empty")
	}
	if cause != "exit status 2" {
		t.Errorf("expected cause %q, got %q", "exit status 2", cause)
	}
	if phase != string(godo.DeploymentPhase_Error) {
		t.Errorf("expected phase from dep.Phase, got %q", phase)
	}
}

// ── deploymentComponents tests ─────────────────────────────────────────────

func TestDeploymentComponents_PrefersDeploymentLevelOverSpec(t *testing.T) {
	dep := &godo.Deployment{
		// Deployment-level arrays (populated by ListDeployments).
		Services: []*godo.DeploymentService{{Name: "svc1"}},
		// Spec-level arrays (populated by richer GetDeployment).
		Spec: &godo.AppSpec{
			Services: []*godo.AppServiceSpec{{Name: "spec-svc1"}},
		},
	}
	got := deploymentComponents(dep)
	if len(got) != 1 || got[0] != "svc1" {
		t.Errorf("expected [\"svc1\"] (deployment-level wins), got %v", got)
	}
}

func TestDeploymentComponents_FallsBackToSpecWhenDeploymentLevelEmpty(t *testing.T) {
	dep := &godo.Deployment{
		// No deployment-level arrays.
		Spec: &godo.AppSpec{
			Services: []*godo.AppServiceSpec{{Name: "spec-svc"}},
		},
	}
	got := deploymentComponents(dep)
	if len(got) != 1 || got[0] != "spec-svc" {
		t.Errorf("expected [\"spec-svc\"] (spec fallback), got %v", got)
	}
}

func TestDeploymentComponents_FallsBackToAggregateWhenAllEmpty(t *testing.T) {
	dep := &godo.Deployment{}
	got := deploymentComponents(dep)
	if len(got) != 1 || got[0] != "" {
		t.Errorf("expected [\"\"] (aggregate fallback), got %v", got)
	}
}

func TestDeploymentComponents_IncludesStaticSitesDeploymentLevel(t *testing.T) {
	dep := &godo.Deployment{
		StaticSites: []*godo.DeploymentStaticSite{{Name: "static1"}},
	}
	got := deploymentComponents(dep)
	if len(got) != 1 || got[0] != "static1" {
		t.Errorf("expected [\"static1\"] from StaticSites, got %v", got)
	}
}

// ── attachDeployLogs visibility tests ─────────────────────────────────────

// errorDeployment is a helper that builds a minimal ERROR-phase deployment
// with one service component (deployment-level) so attachDeployLogs runs.
func errorDeployment(serviceName string) *godo.Deployment {
	return &godo.Deployment{
		ID:    "dep-test",
		Phase: godo.DeploymentPhase_Error,
		Services: []*godo.DeploymentService{
			{Name: serviceName},
		},
	}
}

func TestAttachDeployLogs_VisibleErrorWhenGetLogsErrors(t *testing.T) {
	dep := errorDeployment("web")
	diag := &interfaces.Diagnostic{ID: dep.ID}

	stub := &stubAppClient{logsErr: errors.New("DO API 503")}
	d := NewAppPlatformDriverWithClient(stub, "nyc3")
	d.attachDeployLogs(context.Background(), "app-id", dep, diag)

	if !strings.Contains(diag.Detail, "log fetch unavailable") {
		t.Errorf("Detail should contain 'log fetch unavailable'; got: %q", diag.Detail)
	}
	if !strings.Contains(diag.Detail, "GetLogs API error") {
		t.Errorf("Detail should contain 'GetLogs API error'; got: %q", diag.Detail)
	}
}

func TestAttachDeployLogs_VisibleNoteWhenEmptyHistoricURLs(t *testing.T) {
	dep := errorDeployment("web")
	diag := &interfaces.Diagnostic{ID: dep.ID}

	stub := &stubAppClient{logs: &godo.AppLogs{HistoricURLs: nil}}
	d := NewAppPlatformDriverWithClient(stub, "nyc3")
	d.attachDeployLogs(context.Background(), "app-id", dep, diag)

	if !strings.Contains(diag.Detail, "no historic logs returned") {
		t.Errorf("Detail should contain 'no historic logs returned'; got: %q", diag.Detail)
	}
}

func TestAttachDeployLogs_VisibleErrorWhenHTTPFetchErrors(t *testing.T) {
	// Serve a 500 to trigger fetchLogTail error path.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	dep := errorDeployment("web")
	diag := &interfaces.Diagnostic{ID: dep.ID}

	stub := &stubAppClient{logs: &godo.AppLogs{HistoricURLs: []string{srv.URL}}}
	d := NewAppPlatformDriverWithClient(stub, "nyc3")
	d.attachDeployLogs(context.Background(), "app-id", dep, diag)

	if !strings.Contains(diag.Detail, "log fetch failed") {
		t.Errorf("Detail should contain 'log fetch failed'; got: %q", diag.Detail)
	}
}

func TestAttachDeployLogs_VisibleNoteWhenEmptyBody(t *testing.T) {
	// Return 200 OK with empty body.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dep := errorDeployment("web")
	diag := &interfaces.Diagnostic{ID: dep.ID}

	stub := &stubAppClient{logs: &godo.AppLogs{HistoricURLs: []string{srv.URL}}}
	d := NewAppPlatformDriverWithClient(stub, "nyc3")
	d.attachDeployLogs(context.Background(), "app-id", dep, diag)

	if !strings.Contains(diag.Detail, "empty body") {
		t.Errorf("Detail should contain 'empty body'; got: %q", diag.Detail)
	}
}

func TestAttachDeployLogs_HappyPath_AppendsLogBlock(t *testing.T) {
	const logBody = "line1\nline2\nError: something broke\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(logBody))
	}))
	defer srv.Close()

	dep := errorDeployment("web")
	diag := &interfaces.Diagnostic{ID: dep.ID}

	stub := &stubAppClient{logs: &godo.AppLogs{HistoricURLs: []string{srv.URL}}}
	d := NewAppPlatformDriverWithClient(stub, "nyc3")
	d.attachDeployLogs(context.Background(), "app-id", dep, diag)

	if !strings.Contains(diag.Detail, "Deploy logs") {
		t.Errorf("Detail should contain 'Deploy logs' header; got: %q", diag.Detail)
	}
	if !strings.Contains(diag.Detail, "Error: something broke") {
		t.Errorf("Detail should contain log content; got: %q", diag.Detail)
	}
}
