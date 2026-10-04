package internal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

var errAppJobCleanupPending = errors.New("app platform job cleanup pending; retry with the same handle")

var appJobComponentPattern = regexp.MustCompile(`^wfctl-job-[0-9a-f]{20}$`)
var appJobSecretRefPattern = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_-]*)(?:\.([A-Za-z_][A-Za-z0-9_]*))?\}$`)
var appJobEnvKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type appPlatformJobClient interface {
	Get(context.Context, string) (*godo.App, *godo.Response, error)
	Update(context.Context, string, *godo.AppUpdateRequest) (*godo.App, *godo.Response, error)
	GetDeployment(context.Context, string, string) (*godo.Deployment, *godo.Response, error)
	ListDeployments(context.Context, string, *godo.ListOptions) ([]*godo.Deployment, *godo.Response, error)
	CancelDeployment(context.Context, string, string) (*godo.Deployment, *godo.Response, error)
	ListJobInvocations(context.Context, string, *godo.ListJobInvocationsOptions) ([]*godo.JobInvocation, *godo.Response, error)
	GetJobInvocation(context.Context, string, string, *godo.GetJobInvocationOptions) (*godo.JobInvocation, *godo.Response, error)
	CancelJobInvocation(context.Context, string, string, *godo.CancelJobInvocationOptions) (*godo.JobInvocation, *godo.Response, error)
	GetJobInvocationLogs(context.Context, string, string, *godo.GetJobInvocationLogsOptions) (*godo.AppLogs, *godo.Response, error)
}

type appPlatformRunner struct {
	client appPlatformJobClient
	now    func() time.Time
}

func newAppPlatformRunner(client appPlatformJobClient) *appPlatformRunner {
	return &appPlatformRunner{client: client, now: time.Now}
}

// Handles carry only identities, digests and deadlines, never an app snapshot or credentials.
type appPlatformJobMetadata struct {
	AppID                 string    `json:"app_id"`
	AppName               string    `json:"app_name"`
	Component             string    `json:"component"`
	PriorSpecDigest       string    `json:"prior_spec_digest"`
	ComponentDigest       string    `json:"component_digest"`
	IntentDigest          string    `json:"intent_digest,omitempty"`
	ComponentAcknowledged bool      `json:"component_acknowledged,omitempty"`
	DeploymentID          string    `json:"deployment_id,omitempty"`
	JobID                 string    `json:"job_id,omitempty"`
	Deadline              time.Time `json:"deadline"`
	LaunchFailed          bool      `json:"launch_failed,omitempty"`
}

func (r *appPlatformRunner) RunJob(ctx context.Context, spec interfaces.JobSpec) (*interfaces.JobHandle, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if spec.Target == nil || spec.Target.Type != "infra.container_service" || strings.TrimSpace(spec.RunCommand) == "" || spec.Cron != "" || (spec.Kind != "" && spec.Kind != "PRE_DEPLOY") {
		return nil, appJobValidation("targeted one-off job requires an App Platform parent, command and timeout")
	}
	if len(spec.Alerts) > 0 || len(spec.LogDestinations) > 0 || spec.Termination != nil {
		return nil, appJobValidation("unsupported one-off job options")
	}
	base, digest, ok := strings.Cut(spec.Image, "@")
	if !ok || !validAppJobDigest(digest) {
		return nil, appJobValidation("job image must use a canonical sha256 digest")
	}
	image, err := drivers.ParseImageRef(base)
	if err != nil {
		return nil, appJobValidation("job image registry reference is invalid")
	}
	image.Tag = ""
	image.Digest = digest
	app, _, err := r.client.Get(ctx, spec.Target.ProviderID)
	if err != nil {
		return nil, appJobFailure("read target", err)
	}
	if app == nil || app.ID != spec.Target.ProviderID || app.Spec == nil || app.Spec.Name != spec.Target.Name {
		return nil, appJobValidation("job target does not match the managed app identity")
	}
	envs, err := appJobEnvs(spec, app.Spec)
	if err != nil {
		return nil, err
	}
	if _, _, err := r.client.ListJobInvocations(ctx, app.ID, &godo.ListJobInvocationsOptions{Page: 1, PerPage: 1}); err != nil {
		return nil, appJobFailure("preflight job invocations", err)
	}
	var token [10]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, appJobFailure("generate ownership marker", err)
	}
	component := "wfctl-job-" + hex.EncodeToString(token[:])
	if appJobComponentExists(app.Spec, component) {
		return nil, appJobValidation("job ownership marker conflicts with an existing component")
	}
	updated, err := cloneAppJobSpec(app.Spec)
	if err != nil {
		return nil, err
	}
	job := &godo.AppJobSpec{Name: component, Kind: godo.AppJobSpecKind_PreDeploy, Image: image, RunCommand: spec.RunCommand, Timeout: fmt.Sprintf("%ds", spec.TimeoutSeconds), Envs: envs, InstanceCount: 1}
	updated.Jobs = append(updated.Jobs, job)
	metadata := appPlatformJobMetadata{AppID: app.ID, AppName: app.Spec.Name, Component: component, PriorSpecDigest: appJobDigest(app.Spec), ComponentDigest: appJobDigest(job), IntentDigest: appJobIntentDigest(job), Deadline: r.now().Add(time.Duration(spec.TimeoutSeconds) * time.Second)}
	handle := interfaces.JobHandle{ID: component, Name: spec.Name, Provider: "digitalocean"}
	result, _, err := r.client.Update(ctx, app.ID, &godo.AppUpdateRequest{Spec: updated})
	if err != nil {
		// A failed response can follow an accepted write. Return a recoverable handle,
		// rather than retrying a mutation or discarding its cleanup identity.
		metadata.LaunchFailed = true
		if readback, _, readErr := r.client.Get(ctx, app.ID); readErr == nil {
			result = readback
		}
	}
	if result != nil && result.ID == metadata.AppID && result.Spec != nil && result.Spec.Name == metadata.AppName {
		if accepted := ownedAppJobComponent(result.Spec, metadata); accepted != nil {
			// SECRET values are encrypted by the API on first submission. Persist
			// the acknowledged component digest, not the pre-encryption request hash.
			metadata.ComponentDigest = appJobDigest(accepted)
			metadata.ComponentAcknowledged = true
		}
		for _, deployment := range []*godo.Deployment{result.InProgressDeployment, result.ActiveDeployment} {
			if bindAppJobDeployment(&metadata, deployment) {
				break
			}
		}
	}
	return appJobHandle(handle, metadata), nil
}

// Intent is stable across SECRET encryption, env ordering and duration spelling.
// The exact acknowledged digest remains the authority for subsequent drift checks.
func appJobIntentDigest(job *godo.AppJobSpec) string {
	if job == nil {
		return ""
	}
	timeout, err := time.ParseDuration(job.Timeout)
	if err != nil {
		return ""
	}
	normalized := *job
	normalized.Timeout = timeout.String()
	normalized.Envs = nil
	keys := make(map[string]bool, len(job.Envs))
	for _, env := range job.Envs {
		if env == nil || keys[env.Key] {
			return ""
		}
		keys[env.Key] = true
		copy := *env
		if copy.Type == godo.AppVariableType_Secret {
			copy.Value = ""
		}
		normalized.Envs = append(normalized.Envs, &copy)
	}
	sort.Slice(normalized.Envs, func(i, j int) bool { return normalized.Envs[i].Key < normalized.Envs[j].Key })
	return appJobDigest(&normalized)
}

func ownedAppJobComponent(spec *godo.AppSpec, m appPlatformJobMetadata) *godo.AppJobSpec {
	if spec == nil || spec.Name != m.AppName {
		return nil
	}
	nonJobs := *spec
	nonJobs.Jobs = nil
	if appJobComponentExists(&nonJobs, m.Component) {
		return nil
	}
	var accepted *godo.AppJobSpec
	for _, job := range spec.Jobs {
		if job == nil || job.Name != m.Component {
			continue
		}
		if accepted != nil {
			return nil
		}
		if m.IntentDigest != "" && appJobIntentDigest(job) != m.IntentDigest {
			return nil
		}
		// Legacy handles have no normalization-stable intent, so retain their
		// existing exact-digest authority and never infer a new acknowledgement.
		if (m.IntentDigest == "" || m.ComponentAcknowledged) && appJobDigest(job) != m.ComponentDigest {
			return nil
		}
		accepted = job
	}
	return accepted
}

func bindAppJobDeployment(m *appPlatformJobMetadata, deployment *godo.Deployment) bool {
	if deployment == nil || deployment.ID == "" || (m.DeploymentID != "" && deployment.ID != m.DeploymentID) {
		return false
	}
	job := ownedAppJobComponent(deployment.Spec, *m)
	if job == nil {
		return false
	}
	parent, err := cloneAppJobSpec(deployment.Spec)
	if err != nil {
		return false
	}
	var jobs []*godo.AppJobSpec
	for _, candidate := range parent.Jobs {
		if candidate != nil && candidate.Name == m.Component {
			continue
		}
		jobs = append(jobs, candidate)
	}
	parent.Jobs = jobs
	if appJobDigest(parent) != m.PriorSpecDigest {
		return false
	}
	m.DeploymentID = deployment.ID
	if m.IntentDigest != "" {
		m.ComponentDigest = appJobDigest(job)
		m.ComponentAcknowledged = true
	}
	return true
}

func (r *appPlatformRunner) JobStatus(ctx context.Context, handle interfaces.JobHandle) (*interfaces.JobStatusReply, error) {
	m, err := decodeAppJobHandle(handle)
	if err != nil {
		return nil, err
	}
	reply := &interfaces.JobStatusReply{Handle: handle, State: interfaces.JobStatePending, ExitCode: -1}
	invocation, err := r.appJobInvocation(ctx, &m)
	reply.Handle = *appJobHandle(handle, m)
	if err != nil {
		return reply, err
	}
	if invocation != nil {
		switch invocation.Phase {
		case godo.JOBINVOCATIONPHASE_Succeeded:
			reply.State = interfaces.JobStateSucceeded
			reply.ExitCode = 0
		case godo.JOBINVOCATIONPHASE_Failed, godo.JOBINVOCATIONPHASE_Skipped:
			reply.State = interfaces.JobStateFailed
		case godo.JOBINVOCATIONPHASE_Canceled:
			reply.State = interfaces.JobStateCancelled
		case godo.JOBINVOCATIONPHASE_Running:
			reply.State = interfaces.JobStateRunning
		}
	} else if m.DeploymentID != "" {
		deployment, _, readErr := r.client.GetDeployment(ctx, m.AppID, m.DeploymentID)
		if readErr != nil {
			return reply, appJobFailure("read deployment", readErr)
		}
		if !bindAppJobDeployment(&m, deployment) {
			return reply, appJobValidation("deployment identity mismatch")
		}
		if deployment.Phase == godo.DeploymentPhase_Error {
			reply.State = interfaces.JobStateFailed
		}
		if deployment.Phase == godo.DeploymentPhase_Canceled {
			reply.State = interfaces.JobStateCancelled
		}
	} else if m.LaunchFailed {
		app, _, readErr := r.client.Get(ctx, m.AppID)
		if readErr != nil {
			return reply, appJobFailure("read ambiguous launch", readErr)
		}
		if app == nil || app.Spec == nil {
			return reply, appJobValidation("missing app after ambiguous launch")
		}
		if !appJobComponentExists(app.Spec, m.Component) {
			reply.State = interfaces.JobStateFailed
		}
	}
	if !appJobTerminal(reply.State) && !r.now().Before(m.Deadline) {
		if err := r.cancelInvocation(ctx, &m, invocation); err != nil {
			return reply, err
		}
		reply.State = interfaces.JobStateCancelled
		reply.Message = "job timeout exceeded"
	}
	reply.Handle = *appJobHandle(handle, m)
	if appJobTerminal(reply.State) {
		if err := r.cleanup(ctx, m); err != nil {
			return reply, err
		}
	}
	return reply, nil
}

func (r *appPlatformRunner) CancelJob(ctx context.Context, handle interfaces.JobHandle) error {
	m, err := decodeAppJobHandle(handle)
	if err != nil {
		return err
	}
	invocation, err := r.appJobInvocation(ctx, &m)
	if err != nil {
		return err
	}
	if err := r.cancelInvocation(ctx, &m, invocation); err != nil {
		return err
	}
	return r.cleanup(ctx, m)
}

func (r *appPlatformRunner) cancelInvocation(ctx context.Context, m *appPlatformJobMetadata, invocation *godo.JobInvocation) error {
	if invocation == nil {
		// Removing a not-yet-discovered component does not prove its deployment
		// cannot launch it. Keep cleanup debt until invocation or terminal deployment readback.
		if m.DeploymentID == "" {
			return errAppJobCleanupPending
		}
		deployment, _, err := r.client.GetDeployment(ctx, m.AppID, m.DeploymentID)
		if err != nil {
			return appJobFailure("read cancellation deployment", err)
		}
		if !bindAppJobDeployment(m, deployment) {
			return appJobValidation("cancellation deployment ownership mismatch")
		}
		if deployment.Phase != godo.DeploymentPhase_Error && deployment.Phase != godo.DeploymentPhase_Canceled && deployment.Phase != godo.DeploymentPhase_Superseded {
			if _, _, err := r.client.CancelDeployment(ctx, m.AppID, m.DeploymentID); err != nil {
				return appJobFailure("cancel pending deployment", err)
			}
			readback, _, err := r.client.GetDeployment(ctx, m.AppID, m.DeploymentID)
			if err != nil || !bindAppJobDeployment(m, readback) || (readback.Phase != godo.DeploymentPhase_Canceled && readback.Phase != godo.DeploymentPhase_Error && readback.Phase != godo.DeploymentPhase_Superseded) {
				return errAppJobCleanupPending
			}
		}
		return nil
	}
	if invocation.Phase == godo.JOBINVOCATIONPHASE_Succeeded || invocation.Phase == godo.JOBINVOCATIONPHASE_Failed || invocation.Phase == godo.JOBINVOCATIONPHASE_Canceled || invocation.Phase == godo.JOBINVOCATIONPHASE_Skipped {
		return nil
	}
	deployment, _, err := r.client.GetDeployment(ctx, m.AppID, m.DeploymentID)
	if err != nil {
		return appJobFailure("read invocation deployment", err)
	}
	if !bindAppJobDeployment(m, deployment) {
		return appJobValidation("invocation deployment ownership mismatch")
	}
	if _, _, err := r.client.CancelJobInvocation(ctx, m.AppID, invocation.ID, &godo.CancelJobInvocationOptions{JobName: m.Component}); err != nil {
		return appJobFailure("cancel invocation", err)
	}
	readback, _, err := r.client.GetJobInvocation(ctx, m.AppID, invocation.ID, &godo.GetJobInvocationOptions{JobName: m.Component})
	if err != nil {
		return appJobFailure("read cancellation", err)
	}
	if readback == nil || readback.ID != invocation.ID || readback.JobName != m.Component || (readback.Phase != godo.JOBINVOCATIONPHASE_Canceled && readback.Phase != godo.JOBINVOCATIONPHASE_Succeeded && readback.Phase != godo.JOBINVOCATIONPHASE_Failed && readback.Phase != godo.JOBINVOCATIONPHASE_Skipped) {
		return errAppJobCleanupPending
	}
	return nil
}

func (r *appPlatformRunner) appJobInvocation(ctx context.Context, m *appPlatformJobMetadata) (*godo.JobInvocation, error) {
	if m.DeploymentID == "" {
		app, _, err := r.client.Get(ctx, m.AppID)
		if err != nil {
			return nil, appJobFailure("recover launch deployment", err)
		}
		if app == nil || app.ID != m.AppID || app.Spec == nil || app.Spec.Name != m.AppName {
			return nil, appJobValidation("ambiguous launch app identity mismatch")
		}
		for _, deployment := range []*godo.Deployment{app.InProgressDeployment, app.ActiveDeployment} {
			if deployment == nil || deployment.ID == "" || deployment.Spec == nil {
				continue
			}
			candidate := *m
			if !bindAppJobDeployment(&candidate, deployment) {
				continue
			}
			readback, _, err := r.client.GetDeployment(ctx, m.AppID, candidate.DeploymentID)
			if err != nil {
				return nil, appJobFailure("recover owned deployment", err)
			}
			candidate = *m
			candidate.DeploymentID = deployment.ID
			if !bindAppJobDeployment(&candidate, readback) {
				continue
			}
			*m = candidate
			if m.DeploymentID != "" {
				break
			}
		}
		if m.DeploymentID == "" && !appJobComponentExists(app.Spec, m.Component) && appJobDigest(app.Spec) == m.PriorSpecDigest {
			// Cleanup can advance app deployment hints before its readback is lost.
			// Absence alone cannot prove the historical deployment stopped executing.
			if err := r.recoverAppJobHistoricalDeployment(ctx, m); err != nil {
				return nil, err
			}
		}
	}
	if m.JobID != "" {
		invocation, _, err := r.client.GetJobInvocation(ctx, m.AppID, m.JobID, &godo.GetJobInvocationOptions{JobName: m.Component})
		if err != nil {
			return nil, appJobFailure("read invocation", err)
		}
		if err := r.bindAppJobInvocation(ctx, m, invocation); err != nil {
			return nil, err
		}
		return invocation, nil
	}
	opts := &godo.ListJobInvocationsOptions{Page: 1, PerPage: 100, JobNames: []string{m.Component}, DeploymentID: m.DeploymentID}
	var found *godo.JobInvocation
	for {
		invocations, response, err := r.client.ListJobInvocations(ctx, m.AppID, opts)
		if err != nil {
			return nil, appJobFailure("list invocations", err)
		}
		for _, invocation := range invocations {
			if invocation == nil || invocation.JobName != m.Component {
				continue
			}
			if m.DeploymentID != "" && invocation.DeploymentID != m.DeploymentID {
				continue
			}
			if found != nil && found.ID != invocation.ID {
				return nil, appJobValidation("multiple invocations for one job handle")
			}
			found = invocation
		}
		if response == nil || response.Links == nil || response.Links.IsLastPage() {
			break
		}
		opts.Page++
	}
	if found != nil {
		if err := r.bindAppJobInvocation(ctx, m, found); err != nil {
			return nil, err
		}
	}
	return found, nil
}

func (r *appPlatformRunner) recoverAppJobHistoricalDeployment(ctx context.Context, m *appPlatformJobMetadata) error {
	var found *appPlatformJobMetadata
	opts := &godo.ListOptions{Page: 1, PerPage: 100}
	for ; opts.Page <= 100; opts.Page++ {
		deployments, response, err := r.client.ListDeployments(ctx, m.AppID, opts)
		if err != nil {
			return appJobFailure("list historical deployments", err)
		}
		for _, deployment := range deployments {
			if deployment == nil || deployment.ID == "" {
				continue
			}
			if deployment.Spec != nil && !appJobComponentExists(deployment.Spec, m.Component) {
				continue
			}
			readback, _, err := r.client.GetDeployment(ctx, m.AppID, deployment.ID)
			if err != nil {
				return appJobFailure("read historical deployment", err)
			}
			candidate := *m
			candidate.DeploymentID = deployment.ID
			if !bindAppJobDeployment(&candidate, readback) {
				if readback != nil && appJobComponentExists(readback.Spec, m.Component) {
					return appJobValidation("historical deployment ownership mismatch")
				}
				continue
			}
			if found != nil && found.DeploymentID != candidate.DeploymentID {
				return appJobValidation("multiple historical deployments for one job handle")
			}
			found = &candidate
		}
		if response == nil || response.Links == nil || response.Links.IsLastPage() {
			if found != nil {
				*m = *found
			}
			return nil
		}
	}
	return errAppJobCleanupPending
}

func (r *appPlatformRunner) bindAppJobInvocation(ctx context.Context, m *appPlatformJobMetadata, invocation *godo.JobInvocation) error {
	if invocation == nil || invocation.ID == "" || invocation.JobName != m.Component || invocation.DeploymentID == "" || (m.JobID != "" && invocation.ID != m.JobID) || (m.DeploymentID != "" && invocation.DeploymentID != m.DeploymentID) {
		return appJobValidation("job invocation identity mismatch")
	}
	if m.DeploymentID == "" {
		deployment, _, err := r.client.GetDeployment(ctx, m.AppID, invocation.DeploymentID)
		if err != nil {
			return appJobFailure("bind invocation deployment", err)
		}
		candidate := *m
		candidate.DeploymentID = invocation.DeploymentID
		if !bindAppJobDeployment(&candidate, deployment) {
			return appJobValidation("invocation deployment ownership mismatch")
		}
		*m = candidate
	}
	m.JobID = invocation.ID
	m.DeploymentID = invocation.DeploymentID
	return nil
}

func (r *appPlatformRunner) cleanup(ctx context.Context, m appPlatformJobMetadata) error {
	app, _, err := r.client.Get(ctx, m.AppID)
	if err != nil {
		return fmt.Errorf("%w: target read failed", errAppJobCleanupPending)
	}
	if app == nil || app.ID != m.AppID || app.Spec == nil || app.Spec.Name != m.AppName {
		return fmt.Errorf("%w: app identity changed", errAppJobCleanupPending)
	}
	restored, err := cloneAppJobSpec(app.Spec)
	if err != nil {
		return err
	}
	var jobs []*godo.AppJobSpec
	owned := 0
	for _, job := range restored.Jobs {
		if job != nil && job.Name == m.Component {
			if appJobDigest(job) != m.ComponentDigest {
				return fmt.Errorf("%w: owned component changed", errAppJobCleanupPending)
			}
			owned++
			continue
		}
		jobs = append(jobs, job)
	}
	if owned > 1 {
		return fmt.Errorf("%w: duplicate ownership marker", errAppJobCleanupPending)
	}
	restored.Jobs = jobs
	if appJobDigest(restored) != m.PriorSpecDigest {
		return fmt.Errorf("%w: concurrent app spec drift", errAppJobCleanupPending)
	}
	if owned == 0 {
		return nil
	}
	// App Platform's full-spec Update has no CAS. Never restore a saved snapshot;
	// remove only our unchanged component from a freshly verified live spec.
	_, _, writeErr := r.client.Update(ctx, m.AppID, &godo.AppUpdateRequest{Spec: restored})
	readback, _, readErr := r.client.Get(ctx, m.AppID)
	if readErr != nil || readback == nil || readback.Spec == nil || readback.ID != m.AppID || appJobComponentExists(readback.Spec, m.Component) || appJobDigest(readback.Spec) != m.PriorSpecDigest {
		return fmt.Errorf("%w: absence and spec equivalence not verified", errAppJobCleanupPending)
	}
	if writeErr != nil {
		return nil
	} // Readback resolves a response lost after the write.
	return nil
}

func (r *appPlatformRunner) JobLogs(ctx context.Context, handle interfaces.JobHandle, sink interfaces.LogCaptureSink) error {
	if sink == nil {
		return appJobValidation("job log sink is required")
	}
	m, err := decodeAppJobHandle(handle)
	if err != nil {
		return err
	}
	invocation, err := r.appJobInvocation(ctx, &m)
	if err != nil {
		return err
	}
	if invocation == nil {
		return appJobValidation("job invocation is not available yet")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	logs, _, err := r.client.GetJobInvocationLogs(ctx, m.AppID, invocation.ID, &godo.GetJobInvocationLogsOptions{JobName: m.Component, TailLines: 200})
	if err != nil {
		return appJobFailure("get job logs", err)
	}
	if logs != nil {
		remaining := maxLogCaptureBytes
		for _, rawURL := range logs.HistoricURLs {
			if remaining == 0 {
				break
			}
			data, err := fetchCaptureLogContent(ctx, rawURL, 200)
			if err != nil {
				return err
			}
			if len(data) > remaining {
				data = data[:remaining]
			}
			if err := sink.WriteLogChunk(interfaces.LogChunk{Data: data, Source: "historic"}); err != nil {
				return err
			}
			remaining -= len(data)
		}
	}
	return sink.WriteLogChunk(interfaces.LogChunk{EOF: true})
}

func appJobEnvs(spec interfaces.JobSpec, parent *godo.AppSpec) ([]*godo.AppVariableDefinition, error) {
	var envs []*godo.AppVariableDefinition
	for key, value := range spec.EnvVars {
		if !appJobEnvKeyPattern.MatchString(key) {
			return nil, appJobValidation("invalid job environment key")
		}
		if _, exists := spec.EnvVarsSecret[key]; exists {
			return nil, appJobValidation("job environment key is both plain and secret")
		}
		envs = append(envs, &godo.AppVariableDefinition{Key: key, Value: value, Type: godo.AppVariableType_General, Scope: godo.AppVariableScope_RunTime})
	}
	for key, ref := range spec.EnvVarsSecret {
		parts := appJobSecretRefPattern.FindStringSubmatch(ref)
		if !appJobEnvKeyPattern.MatchString(key) || len(parts) != 3 {
			return nil, appJobValidation("job secret environment requires an App Platform reference")
		}
		if parts[2] != "" {
			if !appJobComponentExists(parent, parts[1]) {
				return nil, appJobValidation("job secret reference component does not exist")
			}
		} else {
			found := false
			for _, env := range parent.Envs {
				if env != nil && env.Key == parts[1] && env.Type == godo.AppVariableType_Secret {
					found = true
					break
				}
			}
			if !found {
				return nil, appJobValidation("job secret reference variable does not exist")
			}
		}
		envs = append(envs, &godo.AppVariableDefinition{Key: key, Value: ref, Type: godo.AppVariableType_Secret, Scope: godo.AppVariableScope_RunTime})
	}
	sort.Slice(envs, func(i, j int) bool { return envs[i].Key < envs[j].Key })
	return envs, nil
}

func appJobComponentExists(spec *godo.AppSpec, name string) bool {
	if spec == nil {
		return false
	}
	for _, c := range spec.Services {
		if c != nil && c.Name == name {
			return true
		}
	}
	for _, c := range spec.Workers {
		if c != nil && c.Name == name {
			return true
		}
	}
	for _, c := range spec.Jobs {
		if c != nil && c.Name == name {
			return true
		}
	}
	for _, c := range spec.StaticSites {
		if c != nil && c.Name == name {
			return true
		}
	}
	for _, c := range spec.Functions {
		if c != nil && c.Name == name {
			return true
		}
	}
	for _, c := range spec.Databases {
		if c != nil && c.Name == name {
			return true
		}
	}
	return false
}

func cloneAppJobSpec(spec *godo.AppSpec) (*godo.AppSpec, error) {
	data, err := json.Marshal(spec)
	if err != nil {
		return nil, appJobFailure("encode app spec", err)
	}
	var result godo.AppSpec
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, appJobFailure("decode app spec", err)
	}
	return &result, nil
}

func appJobDigest(value any) string {
	data, _ := json.Marshal(value)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func validAppJobDigest(digest string) bool {
	if !strings.HasPrefix(digest, "sha256:") {
		return false
	}
	value := strings.TrimPrefix(digest, "sha256:")
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

func appJobHandle(handle interfaces.JobHandle, m appPlatformJobMetadata) *interfaces.JobHandle {
	data, _ := json.Marshal(m)
	handle.Metadata = map[string]string{"app_platform_job": string(data)}
	return &handle
}

func decodeAppJobHandle(handle interfaces.JobHandle) (appPlatformJobMetadata, error) {
	var m appPlatformJobMetadata
	data := handle.Metadata["app_platform_job"]
	if len(data) > 8192 {
		return m, appJobValidation("job handle metadata exceeds the size limit")
	}
	decoder := json.NewDecoder(strings.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return m, appJobValidation("invalid job handle metadata")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return m, appJobValidation("invalid trailing job handle metadata")
	}
	if handle.Provider != "digitalocean" || handle.ID != m.Component || !appJobComponentPattern.MatchString(m.Component) || m.AppID == "" || m.AppName == "" || m.Deadline.IsZero() || !validAppJobDigest("sha256:"+m.PriorSpecDigest) || !validAppJobDigest("sha256:"+m.ComponentDigest) {
		return m, appJobValidation("job handle identity is incomplete")
	}
	if m.IntentDigest != "" && !validAppJobDigest("sha256:"+m.IntentDigest) {
		return m, appJobValidation("job handle intent is invalid")
	}
	return m, nil
}

func appJobTerminal(state interfaces.JobState) bool {
	return state == interfaces.JobStateSucceeded || state == interfaces.JobStateFailed || state == interfaces.JobStateCancelled
}

func appJobValidation(message string) error {
	return fmt.Errorf("app platform job: %s: %w", message, interfaces.ErrValidation)
}
func appJobFailure(operation string, err error) error {
	return &appPlatformJobFailure{operation: operation, cause: err}
}

type appPlatformJobFailure struct {
	operation string
	cause     error
}

func (e *appPlatformJobFailure) Error() string { return "app platform job " + e.operation + " failed" }
func (e *appPlatformJobFailure) Unwrap() error { return e.cause }
