package drivers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const migrationRepairJobPrefix = "wfctl-migration-repair"
const defaultMigrationRepairTimeout = 10 * time.Minute

var _ interfaces.ProviderMigrationRepairer = (*AppPlatformDriver)(nil)

var migrationRepairHTTPClient = http.DefaultClient
var migrationRepairPollInterval = 2 * time.Second

func (d *AppPlatformDriver) RepairDirtyMigration(ctx context.Context, req interfaces.MigrationRepairRequest) (*interfaces.MigrationRepairResult, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	operationCtx := ctx
	timeout := defaultMigrationRepairTimeout
	if req.TimeoutSeconds > 0 {
		timeout = time.Duration(req.TimeoutSeconds) * time.Second
	}
	var cancel context.CancelFunc
	operationCtx, cancel = context.WithTimeout(ctx, timeout)
	defer cancel()
	client, ok := d.client.(appPlatformMigrationRepairClient)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "app platform migration repair: client does not support job invocation APIs")
	}
	app, err := d.findAppObjectByName(operationCtx, req.AppResourceName)
	if err != nil {
		return nil, err
	}
	if app == nil || app.ID == "" || app.Spec == nil {
		return nil, fmt.Errorf("app platform migration repair %q: app is missing ID or spec", req.AppResourceName)
	}

	jobName := migrationRepairJobName()
	job, err := migrationRepairJobSpec(jobName, req)
	if err != nil {
		return nil, err
	}
	if err := preflightMigrationRepairJobInvocations(operationCtx, client, app.ID); err != nil {
		return nil, err
	}
	liveApp, _, err := client.Get(operationCtx, app.ID)
	if err != nil {
		return nil, fmt.Errorf("app platform migration repair read live spec %q: %w", req.AppResourceName, WrapGodoError(err))
	}
	if liveApp == nil || liveApp.Spec == nil {
		return nil, fmt.Errorf("app platform migration repair %q: live app spec missing", req.AppResourceName)
	}
	repairSpec := cloneAppSpec(liveApp.Spec)
	repairSpec.Jobs = append(repairSpec.Jobs, job)
	restore := func(result *interfaces.MigrationRepairResult) (*interfaces.MigrationRepairResult, error) {
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		// App Platform updates are full-spec writes and godo does not expose a
		// compare-and-swap token for AppUpdateRequest. Re-read the live spec and
		// remove only this generated job so unrelated fields are preserved as far
		// as the provider API allows.
		liveApp, _, err := client.Get(restoreCtx, app.ID)
		if err != nil {
			if result == nil {
				result = &interfaces.MigrationRepairResult{Status: interfaces.MigrationRepairStatusFailed}
			}
			result.Status = interfaces.MigrationRepairStatusFailed
			result.Diagnostics = append(result.Diagnostics, interfaces.Diagnostic{
				Phase:  "restore",
				Cause:  "read live app spec failed",
				Detail: err.Error(),
			})
			return result, fmt.Errorf("app platform migration repair restore read %q: %w", req.AppResourceName, WrapGodoError(err))
		}
		if liveApp == nil || liveApp.Spec == nil {
			if result == nil {
				result = &interfaces.MigrationRepairResult{Status: interfaces.MigrationRepairStatusFailed}
			}
			result.Status = interfaces.MigrationRepairStatusFailed
			result.Diagnostics = append(result.Diagnostics, interfaces.Diagnostic{
				Phase: "restore",
				Cause: "live app spec missing",
			})
			return result, fmt.Errorf("app platform migration repair restore %q: live app spec missing", req.AppResourceName)
		}
		restoreSpec := cloneAppSpec(liveApp.Spec)
		restoreSpec.Jobs = withoutMigrationRepairJobName(restoreSpec.Jobs, jobName)
		if len(restoreSpec.Jobs) == len(liveApp.Spec.Jobs) {
			result.Diagnostics = append(result.Diagnostics, interfaces.Diagnostic{
				Phase: "restore",
				Cause: "generated repair job already absent from live spec",
			})
			return result, nil
		}
		if _, _, err := client.Update(restoreCtx, app.ID, &godo.AppUpdateRequest{Spec: restoreSpec}); err != nil {
			if result == nil {
				result = &interfaces.MigrationRepairResult{Status: interfaces.MigrationRepairStatusFailed}
			}
			result.Status = interfaces.MigrationRepairStatusFailed
			result.Diagnostics = append(result.Diagnostics, interfaces.Diagnostic{
				Phase:  "restore",
				Cause:  "restore app spec failed",
				Detail: err.Error(),
			})
			return result, fmt.Errorf("app platform migration repair restore %q: %w", req.AppResourceName, WrapGodoError(err))
		}
		return result, nil
	}
	if _, _, err := client.Update(operationCtx, app.ID, &godo.AppUpdateRequest{Spec: repairSpec}); err != nil {
		result := &interfaces.MigrationRepairResult{
			Status: interfaces.MigrationRepairStatusFailed,
			Diagnostics: []interfaces.Diagnostic{{
				Phase:  "update",
				Cause:  "temporary repair job update failed",
				Detail: err.Error(),
			}},
		}
		restored, restoreErr := restore(result)
		if restoreErr != nil {
			return restored, restoreErr
		}
		return restored, fmt.Errorf("app platform migration repair update %q: %w", req.AppResourceName, WrapGodoError(err))
	}

	result := &interfaces.MigrationRepairResult{
		Status: interfaces.MigrationRepairStatusFailed,
		Diagnostics: []interfaces.Diagnostic{{
			Phase: "job_invocation",
			Cause: "waiting for migration repair job invocation",
		}},
	}
	invocation, err := waitForMigrationRepairInvocation(operationCtx, client, app.ID, jobName, time.Duration(req.TimeoutSeconds)*time.Second)
	if err != nil {
		result.Diagnostics = append(result.Diagnostics, interfaces.Diagnostic{Phase: "job_invocation", Cause: err.Error()})
		if invocation != nil && invocation.ID != "" {
			result.ProviderJobID = invocation.ID
			cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if _, _, cancelErr := client.CancelJobInvocation(cancelCtx, app.ID, invocation.ID, &godo.CancelJobInvocationOptions{JobName: jobName}); cancelErr != nil {
				result.Diagnostics = append(result.Diagnostics, interfaces.Diagnostic{
					ID:     invocation.ID,
					Phase:  "cancel",
					Cause:  "cancel job invocation failed",
					Detail: cancelErr.Error(),
				})
			} else {
				result.Diagnostics = append(result.Diagnostics, interfaces.Diagnostic{
					ID:    invocation.ID,
					Phase: "cancel",
					Cause: "job invocation cancellation requested",
				})
			}
		}
		restored, restoreErr := restore(result)
		if restoreErr != nil {
			return restored, restoreErr
		}
		if invocation != nil && invocation.ID != "" {
			restored.Logs = fetchMigrationRepairInvocationLogs(ctx, client, app.ID, invocation.ID, jobName)
		}
		return restored, err
	}
	result.ProviderJobID = invocation.ID
	result.Status = migrationRepairStatusFromJobInvocation(invocation.Phase)
	result.Diagnostics = append(result.Diagnostics, interfaces.Diagnostic{
		ID:    invocation.DeploymentID,
		Phase: string(invocation.Phase),
		Cause: fmt.Sprintf("job %s", invocation.JobName),
		Detail: fmt.Sprintf("app_id=%s deployment_id=%s job_invocation_id=%s job_name=%s phase=%s",
			app.ID, invocation.DeploymentID, invocation.ID, invocation.JobName, invocation.Phase),
	})
	restored, err := restore(result)
	if err != nil {
		return restored, err
	}
	restored.Logs = fetchMigrationRepairInvocationLogs(ctx, client, app.ID, invocation.ID, jobName)
	return restored, nil
}

func (d *AppPlatformDriver) findAppObjectByName(ctx context.Context, name string) (*godo.App, error) {
	opts := &godo.ListOptions{Page: 1, PerPage: 200}
	for {
		apps, resp, err := d.client.List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("app platform list: %w", WrapGodoError(err))
		}
		for _, app := range apps {
			if app != nil && app.Spec != nil && app.Spec.Name == name {
				return app, nil
			}
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		opts.Page++
	}
	return nil, fmt.Errorf("app %q: %w", name, ErrResourceNotFound)
}

func migrationRepairJobSpec(name string, req interfaces.MigrationRepairRequest) (*godo.AppJobSpec, error) {
	image, err := ParseImageRef(req.JobImage)
	if err != nil {
		return nil, fmt.Errorf("app platform migration repair image: %w", err)
	}
	return &godo.AppJobSpec{
		Name:       name,
		Kind:       godo.AppJobSpecKind_PreDeploy,
		Image:      image,
		RunCommand: migrationRepairRunCommand(req),
		SourceDir:  "",
		Envs:       migrationRepairEnv(req.Env),
		Timeout:    migrationRepairTimeout(req.TimeoutSeconds),
	}, nil
}

func migrationRepairRunCommand(req interfaces.MigrationRepairRequest) string {
	parts := []string{
		"/workflow-migrate repair-dirty",
		"--source-dir " + shellQuote(req.SourceDir),
		"--expected-dirty-version " + shellQuote(req.ExpectedDirtyVersion),
		"--force-version " + shellQuote(req.ForceVersion),
		"--confirm-force " + shellQuote(req.ConfirmForce),
	}
	if req.ThenUp {
		parts = append(parts, "--then-up")
	}
	if req.UpIfClean {
		parts = append(parts, "--up-if-clean")
	}
	return strings.Join(parts, " ")
}

func migrationRepairEnv(env map[string]string) []*godo.AppVariableDefinition {
	if len(env) == 0 {
		return nil
	}
	out := make([]*godo.AppVariableDefinition, 0, len(env))
	for key, value := range env {
		out = append(out, &godo.AppVariableDefinition{
			Key:   key,
			Value: value,
			Scope: godo.AppVariableScope_RunTime,
			Type:  godo.AppVariableType_Secret,
		})
	}
	return out
}

func migrationRepairTimeout(seconds int) string {
	if seconds <= 0 {
		return ""
	}
	return fmt.Sprintf("%ds", seconds)
}

var errMigrationRepairProviderPoll = errors.New("migration repair provider poll failed")

func withoutMigrationRepairJobName(jobs []*godo.AppJobSpec, name string) []*godo.AppJobSpec {
	out := make([]*godo.AppJobSpec, 0, len(jobs))
	for _, job := range jobs {
		if job != nil && job.Name == name {
			continue
		}
		out = append(out, job)
	}
	return out
}

func migrationRepairJobName() string {
	var token [6]byte
	if _, err := rand.Read(token[:]); err == nil {
		return migrationRepairJobPrefix + "-" + hex.EncodeToString(token[:])
	}
	return fmt.Sprintf("%s-%d", migrationRepairJobPrefix, time.Now().UnixNano())
}

func preflightMigrationRepairJobInvocations(ctx context.Context, client appPlatformMigrationRepairClient, appID string) error {
	_, _, err := client.ListJobInvocations(ctx, appID, &godo.ListJobInvocationsOptions{Page: 1, PerPage: 1})
	if err == nil {
		return nil
	}
	if isUnsupportedJobInvocationError(err) {
		return status.Error(codes.Unimplemented, "app platform migration repair: job invocation APIs are not supported")
	}
	return fmt.Errorf("%w: preflight job invocations: %w", errMigrationRepairProviderPoll, WrapGodoError(err))
}

func waitForMigrationRepairInvocation(ctx context.Context, client appPlatformMigrationRepairClient, appID, jobName string, timeout time.Duration) (*godo.JobInvocation, error) {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(migrationRepairPollInterval)
	defer ticker.Stop()
	var last *godo.JobInvocation
	for {
		invocations, err := listMigrationRepairInvocations(ctx, client, appID, jobName)
		if err != nil {
			if isUnsupportedJobInvocationError(err) {
				return last, status.Error(codes.Unimplemented, "app platform migration repair: job invocation APIs are not supported")
			}
			return last, fmt.Errorf("%w: list job invocations: %w", errMigrationRepairProviderPoll, WrapGodoError(err))
		}
		for _, invocation := range invocations {
			if invocation == nil || invocation.JobName != jobName {
				continue
			}
			last = invocation
			switch invocation.Phase {
			case godo.JOBINVOCATIONPHASE_Succeeded, godo.JOBINVOCATIONPHASE_Failed, godo.JOBINVOCATIONPHASE_Canceled, godo.JOBINVOCATIONPHASE_Skipped:
				return invocation, nil
			}
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-deadline.C:
			return last, fmt.Errorf("timed out waiting for migration repair job %q", jobName)
		case <-ticker.C:
		}
	}
}

func listMigrationRepairInvocations(ctx context.Context, client appPlatformMigrationRepairClient, appID, jobName string) ([]*godo.JobInvocation, error) {
	opts := &godo.ListJobInvocationsOptions{
		JobNames: []string{jobName},
		Page:     1,
		PerPage:  20,
	}
	var out []*godo.JobInvocation
	for {
		invocations, resp, err := client.ListJobInvocations(ctx, appID, opts)
		if err != nil {
			return nil, err
		}
		out = append(out, invocations...)
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			return out, nil
		}
		opts.Page++
	}
}

func isUnsupportedJobInvocationError(err error) bool {
	if err == nil {
		return false
	}
	var godoErr *godo.ErrorResponse
	if errors.As(err, &godoErr) && godoErr.Response != nil {
		return godoErr.Response.StatusCode == http.StatusNotFound || godoErr.Response.StatusCode == http.StatusMethodNotAllowed
	}
	type grpcStatusError interface {
		GRPCStatus() *status.Status
	}
	var grpcErr grpcStatusError
	if errors.As(err, &grpcErr) && grpcErr.GRPCStatus() != nil {
		return grpcErr.GRPCStatus().Code() == codes.Unimplemented
	}
	return false
}

func fetchMigrationRepairInvocationLogs(ctx context.Context, client appPlatformMigrationRepairClient, appID, invocationID, jobName string) string {
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	logs, _, err := client.GetJobInvocationLogs(logCtx, appID, invocationID, &godo.GetJobInvocationLogsOptions{JobName: jobName, TailLines: 200})
	if err != nil {
		return ""
	}
	return formatAppLogs(logCtx, logs)
}

func migrationRepairStatusFromJobInvocation(phase godo.JobInvocationPhase) string {
	switch phase {
	case godo.JOBINVOCATIONPHASE_Succeeded:
		return interfaces.MigrationRepairStatusSucceeded
	case godo.JOBINVOCATIONPHASE_Failed, godo.JOBINVOCATIONPHASE_Canceled, godo.JOBINVOCATIONPHASE_Skipped:
		return interfaces.MigrationRepairStatusFailed
	default:
		return interfaces.MigrationRepairStatusFailed
	}
}

func formatAppLogs(ctx context.Context, logs *godo.AppLogs) string {
	if logs == nil {
		return ""
	}
	var parts []string
	if logs.LiveURL != "" {
		parts = append(parts, fetchMigrationRepairLogURL(ctx, logs.LiveURL))
	}
	for _, url := range logs.HistoricURLs {
		if url != "" {
			parts = append(parts, fetchMigrationRepairLogURL(ctx, url))
		}
	}
	return strings.Join(parts, "\n")
}

func fetchMigrationRepairLogURL(ctx context.Context, url string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "log url: " + url
	}
	resp, err := migrationRepairHTTPClient.Do(req)
	if err != nil {
		return "log url: " + url
	}
	defer resp.Body.Close() //nolint:errcheck // best-effort close for log body fetch
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "log url: " + url
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "log url: " + url
	}
	if strings.TrimSpace(string(data)) == "" {
		return "log url: " + url
	}
	return string(data)
}

func cloneAppSpec(spec *godo.AppSpec) *godo.AppSpec {
	if spec == nil {
		return nil
	}
	data, _ := json.Marshal(spec)
	var out godo.AppSpec
	_ = json.Unmarshal(data, &out)
	return &out
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if strings.ContainsAny(value, " \t\n'\"$`\\") {
		return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
	}
	return value
}
