package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type appJobAPIFake struct {
	app                 *godo.App
	phase               godo.JobInvocationPhase
	deploymentPhase     godo.DeploymentPhase
	updates             int
	cancels             int
	updateErr           error
	loseUpdateResponse  bool
	ignoreCleanup       bool
	logs                *godo.AppLogs
	lastJobName         string
	noInvocation        bool
	deploymentCancels   int
	encryptSecretValues bool
	getCalls            int
	failGetAt           int
	deployment          *godo.Deployment
	normalizeJobs       bool
	loseCleanupReadback bool
	deploymentListErr   error
	deploymentReadErr   error
	deploymentHistory   []*godo.Deployment
	ignoreDeployCancel  bool
}

func newAppJobAPIFake() *appJobAPIFake {
	return &appJobAPIFake{app: &godo.App{ID: "app-uuid", Spec: &godo.AppSpec{Name: "staging", Services: []*godo.AppServiceSpec{{Name: "server", RunCommand: "/server"}}}}, phase: godo.JOBINVOCATIONPHASE_Running, deploymentPhase: godo.DeploymentPhase_Deploying}
}

func jobClone[T any](t T) T {
	data, err := json.Marshal(t)
	if err != nil {
		panic(err)
	}
	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		panic(err)
	}
	return result
}

func (f *appJobAPIFake) Get(_ context.Context, id string) (*godo.App, *godo.Response, error) {
	f.getCalls++
	if f.getCalls == f.failGetAt {
		return nil, nil, errors.New("temporary readback outage")
	}
	if f.loseCleanupReadback && f.updates >= 2 && len(f.app.Spec.Jobs) == 0 {
		f.loseCleanupReadback = false
		return nil, nil, errors.New("cleanup readback lost after removal")
	}
	if id != f.app.ID {
		return nil, nil, errors.New("wrong app ID")
	}
	return jobClone(f.app), nil, nil
}

func (f *appJobAPIFake) Update(_ context.Context, id string, req *godo.AppUpdateRequest) (*godo.App, *godo.Response, error) {
	f.updates++
	if id != f.app.ID {
		return nil, nil, errors.New("wrong app ID")
	}
	if f.updateErr != nil {
		return nil, nil, f.updateErr
	}
	if !f.ignoreCleanup || len(req.Spec.Jobs) > 0 {
		f.app.Spec = jobClone(req.Spec)
	}
	if len(req.Spec.Jobs) > 0 {
		f.lastJobName = req.Spec.Jobs[len(req.Spec.Jobs)-1].Name
		if f.encryptSecretValues {
			for _, job := range f.app.Spec.Jobs {
				for _, env := range job.Envs {
					if env.Type == godo.AppVariableType_Secret {
						env.Value = "EV[fixture-provider-encrypted-reference]"
					}
				}
			}
		}
		if f.normalizeJobs {
			for _, job := range f.app.Spec.Jobs {
				timeout, err := time.ParseDuration(job.Timeout)
				if err != nil {
					return nil, nil, err
				}
				job.Timeout = timeout.String()
				for i, j := 0, len(job.Envs)-1; i < j; i, j = i+1, j-1 {
					job.Envs[i], job.Envs[j] = job.Envs[j], job.Envs[i]
				}
			}
		}
	}
	f.app.InProgressDeployment = &godo.Deployment{ID: "deployment-uuid", Phase: f.deploymentPhase, Spec: jobClone(f.app.Spec)}
	if len(req.Spec.Jobs) > 0 {
		f.deployment = jobClone(f.app.InProgressDeployment)
	} else if f.deployment != nil {
		f.app.InProgressDeployment.ID = "cleanup-deployment-uuid"
	}
	if f.loseUpdateResponse {
		return nil, nil, errors.New("response lost after write")
	}
	return jobClone(f.app), nil, nil
}

func (f *appJobAPIFake) GetDeployment(_ context.Context, appID, deploymentID string) (*godo.Deployment, *godo.Response, error) {
	if f.deploymentReadErr != nil {
		return nil, nil, f.deploymentReadErr
	}
	for _, deployment := range f.deploymentHistory {
		if appID == f.app.ID && deployment != nil && deployment.ID == deploymentID {
			return jobClone(deployment), nil, nil
		}
	}
	if appID != f.app.ID || deploymentID != "deployment-uuid" {
		return nil, nil, errors.New("wrong deployment")
	}
	deployment := f.deployment
	if deployment == nil {
		deployment = f.app.InProgressDeployment
	}
	if deployment == nil {
		return nil, nil, errors.New("missing deployment")
	}
	result := jobClone(deployment)
	result.Phase = f.deploymentPhase
	return result, nil, nil
}

func (f *appJobAPIFake) ListDeployments(_ context.Context, appID string, _ *godo.ListOptions) ([]*godo.Deployment, *godo.Response, error) {
	if appID != f.app.ID {
		return nil, nil, errors.New("wrong app ID")
	}
	if f.deploymentListErr != nil {
		return nil, nil, f.deploymentListErr
	}
	if f.deploymentHistory != nil {
		return jobClone(f.deploymentHistory), nil, nil
	}
	return jobClone([]*godo.Deployment{f.deployment, f.app.InProgressDeployment}), nil, nil
}

func (f *appJobAPIFake) ListJobInvocations(_ context.Context, id string, opts *godo.ListJobInvocationsOptions) ([]*godo.JobInvocation, *godo.Response, error) {
	if id != f.app.ID {
		return nil, nil, errors.New("wrong app ID")
	}
	if f.lastJobName == "" || f.noInvocation || f.deploymentPhase == godo.DeploymentPhase_Error {
		return nil, nil, nil
	}
	if len(opts.JobNames) > 0 && opts.JobNames[0] != f.lastJobName {
		return nil, nil, nil
	}
	return []*godo.JobInvocation{{ID: "invocation-uuid", JobName: f.lastJobName, DeploymentID: "deployment-uuid", Phase: f.phase}}, nil, nil
}

func (f *appJobAPIFake) CancelDeployment(_ context.Context, appID, deploymentID string) (*godo.Deployment, *godo.Response, error) {
	if appID != f.app.ID || deploymentID != "deployment-uuid" {
		return nil, nil, errors.New("wrong deployment cancellation")
	}
	f.deploymentCancels++
	if !f.ignoreDeployCancel {
		f.deploymentPhase = godo.DeploymentPhase_Canceled
	}
	return &godo.Deployment{ID: deploymentID, Phase: f.deploymentPhase}, nil, nil
}

func (f *appJobAPIFake) GetJobInvocation(ctx context.Context, id, invocationID string, opts *godo.GetJobInvocationOptions) (*godo.JobInvocation, *godo.Response, error) {
	if id != f.app.ID || invocationID != "invocation-uuid" {
		return nil, nil, errors.New("wrong invocation")
	}
	return &godo.JobInvocation{ID: invocationID, JobName: opts.JobName, DeploymentID: "deployment-uuid", Phase: f.phase}, nil, ctx.Err()
}

func (f *appJobAPIFake) CancelJobInvocation(_ context.Context, id, invocationID string, opts *godo.CancelJobInvocationOptions) (*godo.JobInvocation, *godo.Response, error) {
	if id != f.app.ID || invocationID != "invocation-uuid" || len(f.app.Spec.Jobs) == 0 || opts.JobName != f.app.Spec.Jobs[len(f.app.Spec.Jobs)-1].Name {
		return nil, nil, errors.New("wrong cancellation target")
	}
	f.cancels++
	f.phase = godo.JOBINVOCATIONPHASE_Canceled
	return &godo.JobInvocation{ID: invocationID, JobName: opts.JobName, DeploymentID: "deployment-uuid", Phase: f.phase}, nil, nil
}

func (f *appJobAPIFake) GetJobInvocationLogs(_ context.Context, id, invocationID string, opts *godo.GetJobInvocationLogsOptions) (*godo.AppLogs, *godo.Response, error) {
	if id != f.app.ID || invocationID != "invocation-uuid" || opts.JobName == "" || opts.TailLines != 200 || opts.Follow {
		return nil, nil, errors.New("wrong logs target")
	}
	return f.logs, nil, nil
}

func validAppJobSpec() interfaces.JobSpec {
	return interfaces.JobSpec{Name: "migrate", Target: &interfaces.ResourceRef{Name: "staging", Type: "infra.container_service", ProviderID: "app-uuid"}, Image: "ghcr.io/org/server@sha256:" + strings.Repeat("a", 64), RunCommand: "/server migrate", TimeoutSeconds: 300, EnvVarsSecret: map[string]string{"DATABASE_URL": "${db.DATABASE_URL}"}}
}

func TestAppPlatformRunnerValidationBeforeMutation(t *testing.T) {
	cases := map[string]func(*interfaces.JobSpec){
		"missing target":      func(s *interfaces.JobSpec) { s.Target = nil },
		"wrong target type":   func(s *interfaces.JobSpec) { s.Target.Type = "infra.droplet" },
		"missing ID":          func(s *interfaces.JobSpec) { s.Target.ProviderID = "" },
		"wrong parent name":   func(s *interfaces.JobSpec) { s.Target.Name = "other" },
		"mutable image":       func(s *interfaces.JobSpec) { s.Image = "ghcr.io/org/server:latest" },
		"bad digest":          func(s *interfaces.JobSpec) { s.Image = "ghcr.io/org/server@sha256:abcd" },
		"empty command":       func(s *interfaces.JobSpec) { s.RunCommand = "  " },
		"no timeout":          func(s *interfaces.JobSpec) { s.TimeoutSeconds = 0 },
		"long timeout":        func(s *interfaces.JobSpec) { s.TimeoutSeconds = 3601 },
		"literal secret":      func(s *interfaces.JobSpec) { s.EnvVarsSecret["DATABASE_URL"] = "known-credential-value" },
		"missing binding":     func(s *interfaces.JobSpec) { s.EnvVarsSecret["DATABASE_URL"] = "${unknown.PASSWORD}" },
		"secret as plain env": func(s *interfaces.JobSpec) { s.EnvVars = map[string]string{"DATABASE_URL": "not-a-ref"} },
		"scheduled":           func(s *interfaces.JobSpec) { s.Cron = "* * * * *" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			api := newAppJobAPIFake()
			api.app.Spec.Databases = []*godo.AppDatabaseSpec{{Name: "db"}}
			spec := validAppJobSpec()
			mutate(&spec)
			_, err := newAppPlatformRunner(api).RunJob(context.Background(), spec)
			if !errors.Is(err, interfaces.ErrValidation) {
				t.Fatalf("want validation error, got %v", err)
			}
			if api.updates != 0 {
				t.Fatalf("invalid job mutated app %d times", api.updates)
			}
			if strings.Contains(err.Error(), "known-credential-value") {
				t.Fatal("secret leaked in error")
			}
		})
	}
}

func launchAppJob(t *testing.T, api *appJobAPIFake) (*appPlatformRunner, interfaces.JobHandle) {
	t.Helper()
	api.app.Spec.Databases = []*godo.AppDatabaseSpec{{Name: "db"}}
	runner := newAppPlatformRunner(api)
	handle, err := runner.RunJob(context.Background(), validAppJobSpec())
	if err != nil {
		t.Fatal(err)
	}
	if handle == nil {
		t.Fatal("missing durable handle")
	}
	return runner, *handle
}

func TestAppPlatformRunnerTerminalCleanupAndRestart(t *testing.T) {
	for _, phase := range []godo.JobInvocationPhase{godo.JOBINVOCATIONPHASE_Succeeded, godo.JOBINVOCATIONPHASE_Failed, godo.JOBINVOCATIONPHASE_Canceled, godo.JOBINVOCATIONPHASE_Skipped} {
		t.Run(string(phase), func(t *testing.T) {
			api := newAppJobAPIFake()
			_, handle := launchAppJob(t, api)
			if len(api.app.Spec.Jobs) != 1 {
				t.Fatal("expected one owned job")
			}
			job := api.app.Spec.Jobs[0]
			if !strings.HasPrefix(job.Name, "wfctl-job-") || job.Name == handle.Name || job.Timeout != "300s" || job.Image.Digest != "sha256:"+strings.Repeat("a", 64) || job.Image.Tag != "" {
				t.Fatalf("unsafe job component: %+v", job)
			}
			if len(job.Envs) != 1 || job.Envs[0].Value != "${db.DATABASE_URL}" || job.Envs[0].Type != godo.AppVariableType_Secret {
				t.Fatal("provider-side reference was not preserved")
			}
			api.phase = phase
			reply, err := newAppPlatformRunner(api).JobStatus(context.Background(), jobClone(handle))
			if err != nil {
				t.Fatal(err)
			}
			if phase == godo.JOBINVOCATIONPHASE_Succeeded && reply.State != interfaces.JobStateSucceeded {
				t.Fatalf("got state %q", reply.State)
			}
			if phase != godo.JOBINVOCATIONPHASE_Succeeded && reply.State == interfaces.JobStateSucceeded {
				t.Fatal("failure reported success")
			}
			if len(api.app.Spec.Jobs) != 0 || api.app.Spec.Services[0].RunCommand != "/server" {
				t.Fatal("cleanup did not restore non-job spec")
			}
			if reply.Handle.Metadata["app_platform_job"] == "" {
				t.Fatal("missing persisted metadata")
			}
			if _, err := newAppPlatformRunner(api).JobStatus(context.Background(), reply.Handle); err != nil {
				t.Fatalf("restart retry: %v", err)
			}
		})
	}
}

func TestAppPlatformRunnerJobCleanupDriftAndRetry(t *testing.T) {
	api := newAppJobAPIFake()
	runner, handle := launchAppJob(t, api)
	api.phase = godo.JOBINVOCATIONPHASE_Succeeded
	api.app.Spec.Services[0].RunCommand = "/new-server"
	reply, err := runner.JobStatus(context.Background(), handle)
	if !errors.Is(err, errAppJobCleanupPending) || reply == nil {
		t.Fatalf("want cleanup debt and handle, got %+v %v", reply, err)
	}
	if api.updates != 1 || len(api.app.Spec.Jobs) != 1 {
		t.Fatal("blindly overwrote concurrent change")
	}
	api.app.Spec.Services[0].RunCommand = "/server"
	if _, err := newAppPlatformRunner(api).JobStatus(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	if len(api.app.Spec.Jobs) != 0 {
		t.Fatal("retry left temporary job")
	}
}

func TestAppPlatformRunnerJobCleanupReadbackRequired(t *testing.T) {
	api := newAppJobAPIFake()
	runner, handle := launchAppJob(t, api)
	api.phase = godo.JOBINVOCATIONPHASE_Succeeded
	api.ignoreCleanup = true
	if _, err := runner.JobStatus(context.Background(), handle); !errors.Is(err, errAppJobCleanupPending) {
		t.Fatalf("want cleanup debt on successful PUT with retained component, got %v", err)
	}
	api.ignoreCleanup = false
	if _, err := newAppPlatformRunner(api).JobStatus(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
}

func TestAppPlatformRunnerJobCancelAndTimeout(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprint(timeout), func(t *testing.T) {
			api := newAppJobAPIFake()
			runner, handle := launchAppJob(t, api)
			if timeout {
				runner.now = func() time.Time { return time.Now().Add(time.Hour) }
				reply, err := runner.JobStatus(context.Background(), handle)
				if err != nil || reply.State != interfaces.JobStateCancelled {
					t.Fatalf("timeout: %+v %v", reply, err)
				}
			} else if err := runner.CancelJob(context.Background(), handle); err != nil {
				t.Fatal(err)
			}
			if api.cancels != 1 || len(api.app.Spec.Jobs) != 0 {
				t.Fatal("cancel did not target invocation and remove job")
			}
		})
	}
}

func TestAppPlatformRunnerDeploymentFailureCleanup(t *testing.T) {
	api := newAppJobAPIFake()
	runner, handle := launchAppJob(t, api)
	api.deploymentPhase = godo.DeploymentPhase_Error
	reply, err := runner.JobStatus(context.Background(), handle)
	if err != nil || reply.State != interfaces.JobStateFailed || len(api.app.Spec.Jobs) != 0 {
		t.Fatalf("deploy failure: %+v %v", reply, err)
	}
}

func TestAppPlatformRunnerLostUpdateResponseRetainsHandle(t *testing.T) {
	api := newAppJobAPIFake()
	api.loseUpdateResponse = true
	runner, handle := launchAppJob(t, api)
	if api.updates != 1 {
		t.Fatal("ambiguous write was repeated")
	}
	api.loseUpdateResponse = false
	api.phase = godo.JOBINVOCATIONPHASE_Succeeded
	if _, err := runner.JobStatus(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	if len(api.app.Spec.Jobs) != 0 {
		t.Fatal("ambiguous launch left temporary job")
	}
}

func TestAppPlatformRunnerJobCancelBeforeInvocation(t *testing.T) {
	api := newAppJobAPIFake()
	api.noInvocation = true
	runner, handle := launchAppJob(t, api)
	if err := runner.CancelJob(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	if api.deploymentCancels != 1 || api.cancels != 0 || len(api.app.Spec.Jobs) != 0 {
		t.Fatal("pending deployment was not cancelled before cleanup")
	}
}

func appJobCancelCleanupReadbackLoss(t *testing.T, knownID, encrypted bool) (*appJobAPIFake, interfaces.JobHandle) {
	t.Helper()
	api := newAppJobAPIFake()
	api.app.Spec.Databases = []*godo.AppDatabaseSpec{{Name: "db"}}
	api.encryptSecretValues, api.noInvocation, api.loseCleanupReadback = encrypted, true, true
	if !knownID {
		api.loseUpdateResponse, api.failGetAt = true, 2
	}
	_, original := launchAppJob(t, api)
	m, err := decodeAppJobHandle(original)
	if err != nil || (m.DeploymentID != "") != knownID || m.ComponentAcknowledged != knownID {
		t.Fatal("fixture did not preserve original launch acknowledgement state")
	}
	assertAppJobHandleSecretFree(t, original)
	api.loseUpdateResponse = false
	if err := newAppPlatformRunner(api).CancelJob(t.Context(), jobClone(original)); err == nil {
		t.Fatal("fixture did not lose cleanup readback")
	}
	if api.loseCleanupReadback || len(api.app.Spec.Jobs) != 0 || api.deploymentCancels != 1 || api.updates != 2 || api.app.InProgressDeployment.ID == api.deployment.ID {
		t.Fatal("fixture did not remove owned job, advance app deployment hint and lose cleanup readback")
	}
	return api, jobClone(original)
}

func TestAppPlatformRunnerCancelCleanupReadbackLossOriginalDurableHandle(t *testing.T) {
	for _, fixture := range []struct {
		name               string
		knownID, encrypted bool
	}{{"encrypted-ambiguous", false, true}, {"known-id", true, true}, {"unencrypted-ambiguous", false, false}} {
		t.Run(fixture.name, func(t *testing.T) {
			api, original := appJobCancelCleanupReadbackLoss(t, fixture.knownID, fixture.encrypted)
			encoded, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			for retry := 0; retry < 3; retry++ {
				if err := newAppPlatformRunner(api).CancelJob(t.Context(), jobClone(original)); err != nil {
					t.Fatalf("restart retry %d with original durable handle did not converge: %v", retry, err)
				}
			}
			after, err := json.Marshal(original)
			if err != nil || string(after) != string(encoded) || api.deploymentCancels != 1 || api.cancels != 0 || api.updates != 2 || len(api.app.Spec.Jobs) != 0 {
				t.Fatal("retry changed the durable handle or repeated cancellation/cleanup mutation")
			}
		})
	}
}

func TestAppPlatformRunnerCancelHistoricalAbsenceRecoveryGuards(t *testing.T) {
	for _, change := range []string{"app-id", "app-name", "live-parent", "list-error", "read-error", "unrelated-history", "missing-spec", "intent", "historical-parent", "wrong-readback-id", "multiple-owned", "conflicting-owned", "pending-owned"} {
		t.Run(change, func(t *testing.T) {
			api, original := appJobCancelCleanupReadbackLoss(t, false, true)
			app, deployment := jobClone(api.app), jobClone(api.deployment)
			switch change {
			case "app-id":
				api.app.ID = "unrelated-app"
			case "app-name":
				api.app.Spec.Name = "unrelated-app"
			case "live-parent":
				api.app.Spec.Services[0].RunCommand = "/changed-server"
			case "list-error":
				api.deploymentListErr = errors.New("history unavailable")
			case "read-error":
				api.deploymentReadErr = errors.New("historical deployment unavailable")
			case "unrelated-history":
				api.deployment.Spec.Jobs[0].Name = "unrelated-job"
			case "missing-spec":
				api.deployment.Spec = nil
			case "intent":
				api.deployment.Spec.Jobs[0].RunCommand = "/unrelated-job"
			case "historical-parent":
				api.deployment.Spec.Services[0].RunCommand = "/changed-server"
			case "wrong-readback-id":
				api.deployment.ID = "wrong-readback-id"
			case "multiple-owned", "conflicting-owned":
				second := jobClone(deployment)
				second.ID, second.Phase = "second-owned-deployment", godo.DeploymentPhase_Deploying
				if change == "conflicting-owned" {
					second.Spec.Jobs[0].RunCommand = "/changed-job"
				}
				first := jobClone(deployment)
				first.Phase = godo.DeploymentPhase_Canceled
				api.deploymentHistory = []*godo.Deployment{first, second}
			case "pending-owned":
				api.deploymentPhase, api.ignoreDeployCancel = godo.DeploymentPhase_Deploying, true
			}
			if err := newAppPlatformRunner(api).CancelJob(t.Context(), jobClone(original)); err == nil {
				t.Fatal("absence acknowledged without authoritative identity, unchanged spec and terminal owned deployment")
			}
			wantCancels := 1
			if change == "pending-owned" {
				wantCancels++
			}
			if api.updates != 2 || api.cancels != 0 || api.deploymentCancels != wantCancels || len(api.app.Spec.Jobs) != 0 {
				t.Fatal("unproven historical recovery mutated the live app or unrelated work")
			}
			api.app, api.deployment = app, deployment
			api.deploymentHistory, api.deploymentListErr, api.deploymentReadErr = nil, nil, nil
			api.deploymentPhase, api.ignoreDeployCancel = godo.DeploymentPhase_Canceled, false
			if err := newAppPlatformRunner(api).CancelJob(t.Context(), jobClone(original)); err != nil {
				t.Fatalf("restored authoritative historical cancellation did not converge: %v", err)
			}
			if api.updates != 2 || api.deploymentCancels != wantCancels {
				t.Fatal("restored readback repeated cleanup/cancellation")
			}
		})
	}
}

type appJobDeploymentPages struct {
	*appJobAPIFake
	pages   int
	endless bool
}

func (f *appJobDeploymentPages) ListDeployments(ctx context.Context, appID string, opts *godo.ListOptions) ([]*godo.Deployment, *godo.Response, error) {
	if appID != f.app.ID || opts.PerPage != 100 || opts.Page < 1 || (!f.endless && opts.Page > 2) {
		return nil, nil, errors.New("invalid history page request")
	}
	f.pages++
	if opts.Page == 1 || f.endless {
		return []*godo.Deployment{jobClone(f.app.InProgressDeployment)}, &godo.Response{Links: &godo.Links{Pages: &godo.Pages{Next: "https://api.digitalocean.com/v2/apps/app-uuid/deployments?page=2"}}}, nil
	}
	return []*godo.Deployment{{ID: f.deployment.ID}}, nil, ctx.Err()
}

func TestAppPlatformRunnerCancelHistoricalRecoveryReadsAllPagesAndExactDeployment(t *testing.T) {
	api, original := appJobCancelCleanupReadbackLoss(t, false, true)
	client := &appJobDeploymentPages{appJobAPIFake: api}
	if err := newAppPlatformRunner(client).CancelJob(t.Context(), jobClone(original)); err != nil {
		t.Fatalf("original handle did not recover through paginated ID-only history: %v", err)
	}
	if client.pages != 2 || api.updates != 2 || api.deploymentCancels != 1 {
		t.Fatal("historical readback skipped a page or repeated mutation")
	}
}

func TestAppPlatformRunnerCancelHistoricalRecoveryPaginationBound(t *testing.T) {
	api, original := appJobCancelCleanupReadbackLoss(t, false, true)
	client := &appJobDeploymentPages{appJobAPIFake: api, endless: true}
	if err := newAppPlatformRunner(client).CancelJob(t.Context(), jobClone(original)); err == nil {
		t.Fatal("incomplete unbounded history acknowledged cancellation")
	}
	if client.pages != 100 || api.updates != 2 || api.deploymentCancels != 1 {
		t.Fatal("history pagination was not bounded or repeated mutation")
	}
}

func TestAppPlatformRunnerAmbiguousLaunchDeploymentFailure(t *testing.T) {
	api := newAppJobAPIFake()
	api.noInvocation = true
	api.loseUpdateResponse = true
	runner, handle := launchAppJob(t, api)
	api.loseUpdateResponse = false
	api.deploymentPhase = godo.DeploymentPhase_Error
	reply, err := runner.JobStatus(context.Background(), handle)
	if err != nil || reply.State != interfaces.JobStateFailed || len(api.app.Spec.Jobs) != 0 {
		t.Fatalf("lost response then failed deployment did not converge: %+v %v", reply, err)
	}
}

func TestAppPlatformRunnerJobLogsAfterCleanupPreservePayload(t *testing.T) {
	api := newAppJobAPIFake()
	runner, handle := launchAppJob(t, api)
	const payload = "consumer data: AWS_SECRET_ACCESS_KEY=payload-value\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, payload) }))
	defer server.Close()
	api.logs = &godo.AppLogs{HistoricURLs: []string{server.URL}}
	api.phase = godo.JOBINVOCATIONPHASE_Succeeded
	reply, err := runner.JobStatus(t.Context(), handle)
	if err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	if err := newAppPlatformRunner(api).JobLogs(t.Context(), reply.Handle, sink); err != nil {
		t.Fatal(err)
	}
	if sink.String() != payload {
		t.Fatal("job log consumer payload was changed or lost")
	}
}

func TestAppPlatformRunnerJobCleanupProviderEncryptedReferences(t *testing.T) {
	for _, lostResponse := range []bool{false, true} {
		t.Run(fmt.Sprint(lostResponse), func(t *testing.T) {
			api := newAppJobAPIFake()
			api.encryptSecretValues = true
			api.loseUpdateResponse = lostResponse
			_, handle := launchAppJob(t, api)
			api.loseUpdateResponse = false
			api.phase = godo.JOBINVOCATIONPHASE_Succeeded
			if _, err := newAppPlatformRunner(api).JobStatus(t.Context(), handle); err != nil {
				t.Fatal(err)
			}
			if len(api.app.Spec.Jobs) != 0 {
				t.Fatal("encrypted reference normalization left an owned job")
			}
		})
	}
}

func TestAppPlatformRunnerRejectedLaunchCannotAdoptOrCancelUnrelatedDeployment(t *testing.T) {
	api := newAppJobAPIFake()
	api.app.InProgressDeployment = &godo.Deployment{ID: "deployment-uuid", Phase: godo.DeploymentPhase_Deploying, Spec: jobClone(api.app.Spec)}
	api.updateErr = errors.New("launch rejected before write")
	_, handle := launchAppJob(t, api)
	m, err := decodeAppJobHandle(handle)
	if err != nil {
		t.Fatal(err)
	}
	if m.DeploymentID != "" || !m.LaunchFailed || len(api.app.Spec.Jobs) != 0 {
		t.Fatal("rejected launch adopted an unrelated deployment")
	}
	_ = newAppPlatformRunner(api).CancelJob(t.Context(), jobClone(handle))
	if api.deploymentCancels != 0 || api.cancels != 0 || api.updates != 1 {
		t.Fatal("rejected launch mutated unrelated app work")
	}
}

func TestAppPlatformRunnerCancellationRechecksExactDeploymentBinding(t *testing.T) {
	for _, change := range []string{"marker", "command", "running-command", "secret", "parent", "duplicate", "missing-spec", "identity"} {
		t.Run(change, func(t *testing.T) {
			api := newAppJobAPIFake()
			api.noInvocation = change != "running-command"
			_, handle := launchAppJob(t, api)
			original := jobClone(api.deployment)
			switch change {
			case "marker":
				api.deployment.Spec.Jobs[0].Name = "unrelated"
			case "command", "running-command":
				api.deployment.Spec.Jobs[0].RunCommand = "/unrelated"
			case "secret":
				api.deployment.Spec.Jobs[0].Envs[0].Value = "EV[changed-secret]"
			case "parent":
				api.deployment.Spec.Services[0].RunCommand = "/unrelated-server"
			case "duplicate":
				api.deployment.Spec.Jobs = append(api.deployment.Spec.Jobs, jobClone(api.deployment.Spec.Jobs[0]))
			case "missing-spec":
				api.deployment.Spec = nil
			case "identity":
				api.deployment.ID = "other-deployment"
			}
			if err := newAppPlatformRunner(api).CancelJob(t.Context(), jobClone(handle)); err == nil {
				t.Fatal("unbound deployment cancellation was accepted")
			}
			if api.deploymentCancels != 0 || api.cancels != 0 || api.updates != 1 || len(api.app.Spec.Jobs) != 1 {
				t.Fatal("unbound deployment was cancelled or cleaned up")
			}
			api.deployment = original
			if err := newAppPlatformRunner(api).CancelJob(t.Context(), jobClone(handle)); err != nil {
				t.Fatal(err)
			}
			if api.deploymentCancels+api.cancels != 1 || len(api.app.Spec.Jobs) != 0 {
				t.Fatal("restored exact deployment did not converge")
			}
		})
	}
}

func TestAppPlatformRunnerAmbiguousEncryptedNormalizedIntent(t *testing.T) {
	api := newAppJobAPIFake()
	api.app.Spec.Databases = []*godo.AppDatabaseSpec{{Name: "db"}}
	api.encryptSecretValues, api.loseUpdateResponse, api.normalizeJobs, api.failGetAt = true, true, true, 2
	spec := validAppJobSpec()
	spec.EnvVars = map[string]string{"MODE": "migration"}
	handle, err := newAppPlatformRunner(api).RunJob(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	api.loseUpdateResponse = false
	api.phase = godo.JOBINVOCATIONPHASE_Succeeded
	reply, err := newAppPlatformRunner(api).JobStatus(t.Context(), jobClone(*handle))
	if err != nil || reply.State != interfaces.JobStateSucceeded || len(api.app.Spec.Jobs) != 0 {
		t.Fatalf("equivalent duration/env ordering prevented encrypted recovery: %v", err)
	}
	assertAppJobHandleSecretFree(t, reply.Handle)
}

func ambiguousEncryptedAppJob(t *testing.T) (*appJobAPIFake, interfaces.JobHandle) {
	t.Helper()
	api := newAppJobAPIFake()
	api.encryptSecretValues, api.loseUpdateResponse, api.failGetAt = true, true, 2
	_, handle := launchAppJob(t, api)
	api.loseUpdateResponse = false
	assertAppJobHandleSecretFree(t, handle)
	return api, jobClone(handle)
}

func assertAppJobHandleSecretFree(t *testing.T, handle interfaces.JobHandle) {
	t.Helper()
	data, err := json.Marshal(handle)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"${db.DATABASE_URL}", "EV[fixture-provider-encrypted-reference]", "EV[changed-secret]"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("job handle persisted secret/reference bytes")
		}
	}
}

func TestAppPlatformRunnerAmbiguousEncryptedLaunchRecoversAfterRestart(t *testing.T) {
	for _, outcome := range []string{"deployment-failed", "invocation-succeeded", "invocation-failed", "interrupted-cancel", "interrupted-timeout"} {
		t.Run(outcome, func(t *testing.T) {
			api, handle := ambiguousEncryptedAppJob(t)
			original, err := decodeAppJobHandle(handle)
			if err != nil {
				t.Fatal(err)
			}
			acceptedDigest := appJobDigest(api.app.Spec.Jobs[0])
			if original.DeploymentID != "" || original.ComponentDigest == acceptedDigest {
				t.Fatal("fixture did not lose both encrypted acknowledgement and readback")
			}
			var reply *interfaces.JobStatusReply
			switch outcome {
			case "deployment-failed":
				api.noInvocation = true
				api.deploymentPhase = godo.DeploymentPhase_Error
			case "invocation-succeeded":
				api.phase = godo.JOBINVOCATIONPHASE_Succeeded
			case "invocation-failed":
				api.phase = godo.JOBINVOCATIONPHASE_Failed
			}
			if outcome == "interrupted-cancel" {
				err = newAppPlatformRunner(api).CancelJob(t.Context(), handle)
			} else {
				runner := newAppPlatformRunner(api)
				if outcome == "interrupted-timeout" {
					runner.now = func() time.Time { return original.Deadline.Add(time.Second) }
				}
				reply, err = runner.JobStatus(t.Context(), handle)
			}
			if err != nil || len(api.app.Spec.Jobs) != 0 {
				t.Fatalf("encrypted ambiguous launch did not converge: %v; components=%d", err, len(api.app.Spec.Jobs))
			}
			if reply != nil {
				recovered, err := decodeAppJobHandle(reply.Handle)
				if err != nil || recovered.ComponentDigest != acceptedDigest || recovered.DeploymentID != "deployment-uuid" {
					t.Fatal("acknowledged digest/deployment was not persisted after recovery")
				}
				assertAppJobHandleSecretFree(t, reply.Handle)
				if _, err := newAppPlatformRunner(api).JobStatus(t.Context(), jobClone(reply.Handle)); err != nil {
					t.Fatalf("recovered handle retry failed: %v", err)
				}
			}
			if reply != nil {
				handle = reply.Handle
			}
			if err := newAppPlatformRunner(api).CancelJob(t.Context(), jobClone(handle)); err != nil {
				t.Fatalf("durable same-identity handle retry failed: %v", err)
			}
		})
	}
}

func TestAppPlatformRunnerEncryptedRecoveryAcknowledgesImmutableDeploymentNotAppHint(t *testing.T) {
	api, handle := ambiguousEncryptedAppJob(t)
	api.noInvocation, api.deploymentPhase = true, godo.DeploymentPhase_Error
	// The app's embedded hint may lag normalization; only the exact deployment
	// readback can supply the digest persisted for subsequent cleanup checks.
	api.app.InProgressDeployment.Spec.Jobs[0].Envs[0].Value = "${db.DATABASE_URL}"
	reply, err := newAppPlatformRunner(api).JobStatus(t.Context(), handle)
	if err != nil || reply.State != interfaces.JobStateFailed || len(api.app.Spec.Jobs) != 0 {
		t.Fatalf("immutable encrypted acknowledgement did not recover: %v", err)
	}
	assertAppJobHandleSecretFree(t, reply.Handle)
}

func TestAppPlatformRunnerAmbiguousEncryptedRecoveryDoesNotWaiveDrift(t *testing.T) {
	for _, change := range []string{"live-secret", "command", "image", "timeout", "env-key", "env-type", "env-scope", "parent", "duplicate", "extra-source"} {
		t.Run(change, func(t *testing.T) {
			api, handle := ambiguousEncryptedAppJob(t)
			api.noInvocation = true
			api.deploymentPhase = godo.DeploymentPhase_Error
			originalApp, originalDeployment := jobClone(api.app.Spec), jobClone(api.deployment)
			job := api.deployment.Spec.Jobs[0]
			switch change {
			case "live-secret":
				api.app.Spec.Jobs[0].Envs[0].Value = "EV[changed-secret]"
			case "command":
				job.RunCommand = "/unrelated"
			case "image":
				job.Image.Digest = "sha256:" + strings.Repeat("b", 64)
			case "timeout":
				job.Timeout = "1s"
			case "env-key":
				job.Envs[0].Key = "OTHER"
			case "env-type":
				job.Envs[0].Type = godo.AppVariableType_General
			case "env-scope":
				job.Envs[0].Scope = godo.AppVariableScope_BuildTime
			case "parent":
				api.app.Spec.Services[0].RunCommand = "/new-server"
			case "duplicate":
				api.deployment.Spec.Jobs = append(api.deployment.Spec.Jobs, jobClone(job))
			case "extra-source":
				job.GitHub = &godo.GitHubSourceSpec{Repo: "other/source"}
			}
			_, statusErr := newAppPlatformRunner(api).JobStatus(t.Context(), handle)
			cancelErr := newAppPlatformRunner(api).CancelJob(t.Context(), handle)
			if api.updates != 1 || api.cancels != 0 || api.deploymentCancels != 0 || len(api.app.Spec.Jobs) != 1 {
				t.Fatal("ambiguous recovery waived ownership or drift checks")
			}
			for _, err := range []error{statusErr, cancelErr} {
				if err != nil && strings.Contains(err.Error(), "EV[") {
					t.Fatal("recovery error exposed encrypted secret bytes")
				}
			}
			api.app.Spec, api.deployment = originalApp, originalDeployment
			api.app.InProgressDeployment = jobClone(originalDeployment)
			reply, err := newAppPlatformRunner(api).JobStatus(t.Context(), jobClone(handle))
			if err != nil || reply.State != interfaces.JobStateFailed || len(api.app.Spec.Jobs) != 0 {
				t.Fatalf("restored unchanged intent did not converge: %v", err)
			}
		})
	}
}

func TestAppPlatformRunnerLegacyOpaqueHandleRetainsExactBinding(t *testing.T) {
	api := newAppJobAPIFake()
	api.noInvocation = true
	_, handle := launchAppJob(t, api)
	var metadata map[string]any
	if err := json.Unmarshal([]byte(handle.Metadata["app_platform_job"]), &metadata); err != nil {
		t.Fatal(err)
	}
	delete(metadata, "intent_digest")
	delete(metadata, "component_acknowledged")
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	handle.Metadata["app_platform_job"] = string(data)
	if err := newAppPlatformRunner(api).CancelJob(t.Context(), jobClone(handle)); err != nil {
		t.Fatal(err)
	}
	if api.deploymentCancels != 1 || len(api.app.Spec.Jobs) != 0 {
		t.Fatal("legacy acknowledged handle did not clean up its exact deployment")
	}
}
