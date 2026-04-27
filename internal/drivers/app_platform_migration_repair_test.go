package drivers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var migrationRepairNameHookMu sync.Mutex

func TestAppPlatformRepairDirtyMigrationRunsTemporaryPreDeployJobAndRestoresSpec(t *testing.T) {
	logServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("migration repair log tail"))
	}))
	defer logServer.Close()
	client := &migrationRepairAppClient{
		app: &godo.App{
			ID: "app-123",
			Spec: &godo.AppSpec{
				Name:   "bmw-staging",
				Region: "nyc3",
				Services: []*godo.AppServiceSpec{{
					Name: "bmw-staging",
					Image: &godo.ImageSourceSpec{
						RegistryType: godo.ImageSourceSpecRegistryType_DOCR,
						Registry:     "bmw-registry",
						Repository:   "buymywishlist",
						Tag:          "sha",
					},
				}},
			},
		},
		deployment: &godo.Deployment{ID: "dep-123", Phase: godo.DeploymentPhase_Active},
		invocations: [][]*godo.JobInvocation{{
			{ID: "job-123", JobName: "$repair", DeploymentID: "dep-123", Phase: godo.JOBINVOCATIONPHASE_Succeeded},
		}},
		logs: &godo.AppLogs{HistoricURLs: []string{logServer.URL + "/job-123"}},
	}
	driver := NewAppPlatformDriverWithClient(client, "nyc3")

	result, err := driver.RepairDirtyMigration(context.Background(), interfaces.MigrationRepairRequest{
		AppResourceName:      "bmw-staging",
		DatabaseResourceName: "bmw-staging-db",
		JobImage:             "registry.digitalocean.com/bmw-registry/workflow-migrate:sha",
		SourceDir:            "/migrations",
		ExpectedDirtyVersion: "20260426000005",
		ForceVersion:         "20260422000001",
		ThenUp:               true,
		ConfirmForce:         interfaces.MigrationRepairConfirmation,
		Env:                  map[string]string{"DATABASE_URL": "postgres://secret"},
		TimeoutSeconds:       60,
	})
	if err != nil {
		t.Fatalf("RepairDirtyMigration: %v", err)
	}
	if result.Status != interfaces.MigrationRepairStatusSucceeded {
		t.Fatalf("status = %q", result.Status)
	}
	if result.ProviderJobID != "job-123" {
		t.Fatalf("ProviderJobID = %q", result.ProviderJobID)
	}
	if len(client.updates) != 2 {
		t.Fatalf("updates = %d, want temporary update + restore", len(client.updates))
	}
	tempSpec := client.updates[0].Spec
	if len(tempSpec.Jobs) != 1 {
		t.Fatalf("temporary jobs = %d, want 1", len(tempSpec.Jobs))
	}
	job := tempSpec.Jobs[0]
	if !strings.HasPrefix(job.Name, migrationRepairJobPrefix+"-") {
		t.Fatalf("job name = %q", job.Name)
	}
	if len(job.Name) > 32 {
		t.Fatalf("job name length = %d, want <= 32 for DigitalOcean App Platform", len(job.Name))
	}
	if job.Image == nil || job.Image.Repository != "workflow-migrate" || job.Image.Tag != "sha" {
		t.Fatalf("job image = %+v", job.Image)
	}
	for _, want := range []string{
		"/workflow-migrate repair-dirty",
		"--source-dir /migrations",
		"--expected-dirty-version 20260426000005",
		"--force-version 20260422000001",
		"--confirm-force FORCE_MIGRATION_METADATA",
		"--then-up",
	} {
		if !strings.Contains(job.RunCommand, want) {
			t.Fatalf("run command %q missing %q", job.RunCommand, want)
		}
	}
	if len(job.Envs) != 1 || job.Envs[0].Key != "DATABASE_URL" || job.Envs[0].Type != godo.AppVariableType_Secret {
		t.Fatalf("job envs = %+v", job.Envs)
	}
	if len(client.deploymentsCreated) != 0 {
		t.Fatalf("deploymentsCreated = %v, want no explicit deployment after app update", client.deploymentsCreated)
	}
	if restored := client.updates[1].Spec; len(restored.Jobs) != 0 {
		t.Fatalf("restored jobs = %d, want original jobs restored", len(restored.Jobs))
	}
	if len(result.Diagnostics) == 0 || result.Diagnostics[len(result.Diagnostics)-1].ID != "dep-123" {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
	if detail := result.Diagnostics[len(result.Diagnostics)-1].Detail; !strings.Contains(detail, "app_id=app-123") || !strings.Contains(detail, "job_invocation_id=job-123") {
		t.Fatalf("terminal diagnostic detail = %q, want provider identifiers", detail)
	}
	if !strings.Contains(result.Logs, "migration repair log tail") {
		t.Fatalf("logs = %q, want fetched log tail", result.Logs)
	}
}

func TestFetchMigrationRepairLogURLDoesNotLeakSignedURLOnFailure(t *testing.T) {
	logServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer logServer.Close()

	logs := formatAppLogs(context.Background(), &godo.AppLogs{
		HistoricURLs: []string{logServer.URL + "/job-123?token=secret"},
	})
	if strings.Contains(logs, "token=secret") || strings.Contains(logs, logServer.URL) {
		t.Fatalf("logs = %q, want no signed URL leak", logs)
	}
	if logs != "log unavailable" {
		t.Fatalf("logs = %q, want generic placeholder", logs)
	}
}

func TestAppPlatformRepairDirtyMigrationRejectsInvalidJobImageBeforeMutation(t *testing.T) {
	client := &migrationRepairAppClient{
		app: &godo.App{ID: "app-123", Spec: &godo.AppSpec{Name: "bmw-staging"}},
	}
	driver := NewAppPlatformDriverWithClient(client, "nyc3")

	_, err := driver.RepairDirtyMigration(context.Background(), interfaces.MigrationRepairRequest{
		AppResourceName:      "bmw-staging",
		DatabaseResourceName: "bmw-staging-db",
		JobImage:             "not a valid image reference with spaces",
		SourceDir:            "/migrations",
		ExpectedDirtyVersion: "20260426000005",
		ForceVersion:         "20260422000001",
		ThenUp:               true,
		ConfirmForce:         interfaces.MigrationRepairConfirmation,
		TimeoutSeconds:       1,
	})
	if err == nil {
		t.Fatal("expected invalid image error")
	}
	if len(client.updates) != 0 {
		t.Fatalf("updates = %d, want no mutation before image validation", len(client.updates))
	}
}

func TestAppPlatformRepairDirtyMigrationDefaultTimeoutCoversPreflight(t *testing.T) {
	client := &migrationRepairAppClient{
		app:                      &godo.App{ID: "app-123", Spec: &godo.AppSpec{Name: "bmw-staging"}},
		deployment:               &godo.Deployment{ID: "dep-123", Phase: godo.DeploymentPhase_Deploying},
		requirePreflightDeadline: true,
		invocations:              [][]*godo.JobInvocation{{{ID: "job-123", JobName: "$repair", DeploymentID: "dep-123", Phase: godo.JOBINVOCATIONPHASE_Succeeded}}},
	}
	driver := NewAppPlatformDriverWithClient(client, "nyc3")

	if _, err := driver.RepairDirtyMigration(context.Background(), interfaces.MigrationRepairRequest{
		AppResourceName:      "bmw-staging",
		DatabaseResourceName: "bmw-staging-db",
		JobImage:             "registry.digitalocean.com/bmw-registry/workflow-migrate:sha",
		SourceDir:            "/migrations",
		ExpectedDirtyVersion: "20260426000005",
		ForceVersion:         "20260422000001",
		ThenUp:               true,
		ConfirmForce:         interfaces.MigrationRepairConfirmation,
	}); err != nil {
		t.Fatalf("RepairDirtyMigration: %v", err)
	}
}

func TestAppPlatformRepairDirtyMigrationCleansUpAfterAmbiguousUpdateError(t *testing.T) {
	client := &migrationRepairAppClient{
		app:                 &godo.App{ID: "app-123", Spec: &godo.AppSpec{Name: "bmw-staging"}},
		deployment:          &godo.Deployment{ID: "dep-123", Phase: godo.DeploymentPhase_Deploying},
		updateErrAfterApply: true,
	}
	driver := NewAppPlatformDriverWithClient(client, "nyc3")

	result, err := driver.RepairDirtyMigration(context.Background(), interfaces.MigrationRepairRequest{
		AppResourceName:      "bmw-staging",
		DatabaseResourceName: "bmw-staging-db",
		JobImage:             "registry.digitalocean.com/bmw-registry/workflow-migrate:sha",
		SourceDir:            "/migrations",
		ExpectedDirtyVersion: "20260426000005",
		ForceVersion:         "20260422000001",
		ThenUp:               true,
		ConfirmForce:         interfaces.MigrationRepairConfirmation,
	})
	if err == nil {
		t.Fatal("expected update error")
	}
	if result == nil || result.Status != interfaces.MigrationRepairStatusFailed {
		t.Fatalf("result = %+v, want failed result", result)
	}
	if len(client.updates) != 2 {
		t.Fatalf("updates = %d, want attempted update plus cleanup update", len(client.updates))
	}
	if jobs := client.updates[1].Spec.Jobs; len(jobs) != 0 {
		t.Fatalf("cleanup jobs = %+v, want generated repair job removed", jobs)
	}
}

func TestAppPlatformRepairDirtyMigrationCancelsRunningJobOnPollingErrorAndRestores(t *testing.T) {
	prevInterval := migrationRepairPollInterval
	migrationRepairPollInterval = time.Millisecond
	t.Cleanup(func() { migrationRepairPollInterval = prevInterval })
	logServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("running job log tail"))
	}))
	defer logServer.Close()
	client := &migrationRepairAppClient{
		app: &godo.App{ID: "app-123", Spec: &godo.AppSpec{Name: "bmw-staging"}},
		deployment: &godo.Deployment{
			ID:    "dep-123",
			Phase: godo.DeploymentPhase_Deploying,
		},
		invocations: [][]*godo.JobInvocation{{
			{ID: "job-running", JobName: "$repair", DeploymentID: "dep-123", Phase: godo.JOBINVOCATIONPHASE_Running},
		}},
		listInvocationErr: context.DeadlineExceeded,
		logs:              &godo.AppLogs{HistoricURLs: []string{logServer.URL + "/job-running"}},
	}
	driver := NewAppPlatformDriverWithClient(client, "nyc3")

	result, err := driver.RepairDirtyMigration(context.Background(), interfaces.MigrationRepairRequest{
		AppResourceName:      "bmw-staging",
		DatabaseResourceName: "bmw-staging-db",
		JobImage:             "registry.digitalocean.com/bmw-registry/workflow-migrate:sha",
		SourceDir:            "/migrations",
		ExpectedDirtyVersion: "20260426000005",
		ForceVersion:         "20260422000001",
		ThenUp:               true,
		ConfirmForce:         interfaces.MigrationRepairConfirmation,
	})
	if err == nil {
		t.Fatal("expected provider polling error")
	}
	if result == nil || result.Status != interfaces.MigrationRepairStatusFailed {
		t.Fatalf("result = %+v, want failed result", result)
	}
	if len(client.cancelledJobs) != 1 || client.cancelledJobs[0] != "job-running" {
		t.Fatalf("cancelledJobs = %v, want job-running canceled", client.cancelledJobs)
	}
	if !strings.Contains(result.Logs, "running job log tail") {
		t.Fatalf("logs = %q, want failed invocation logs", result.Logs)
	}
	if len(client.updates) != 2 {
		t.Fatalf("updates = %d, want temporary update + restore", len(client.updates))
	}
}

func TestAppPlatformRepairDirtyMigrationCancelsRunningJobOnRequestTimeoutAndRestores(t *testing.T) {
	prevInterval := migrationRepairPollInterval
	migrationRepairPollInterval = time.Millisecond
	t.Cleanup(func() { migrationRepairPollInterval = prevInterval })
	client := &migrationRepairAppClient{
		app:        &godo.App{ID: "app-123", Spec: &godo.AppSpec{Name: "bmw-staging"}},
		deployment: &godo.Deployment{ID: "dep-123", Phase: godo.DeploymentPhase_Deploying},
		invocations: [][]*godo.JobInvocation{{
			{ID: "job-running", JobName: "$repair", DeploymentID: "dep-123", Phase: godo.JOBINVOCATIONPHASE_Running},
		}},
	}
	driver := NewAppPlatformDriverWithClient(client, "nyc3")

	result, err := driver.RepairDirtyMigration(context.Background(), interfaces.MigrationRepairRequest{
		AppResourceName:      "bmw-staging",
		DatabaseResourceName: "bmw-staging-db",
		JobImage:             "registry.digitalocean.com/bmw-registry/workflow-migrate:sha",
		SourceDir:            "/migrations",
		ExpectedDirtyVersion: "20260426000005",
		ForceVersion:         "20260422000001",
		ThenUp:               true,
		ConfirmForce:         interfaces.MigrationRepairConfirmation,
		TimeoutSeconds:       1,
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if result == nil || result.Status != interfaces.MigrationRepairStatusFailed {
		t.Fatalf("result = %+v, want failed result", result)
	}
	if len(client.cancelledJobs) != 1 || client.cancelledJobs[0] != "job-running" {
		t.Fatalf("cancelledJobs = %v, want job-running canceled", client.cancelledJobs)
	}
}

func TestAppPlatformRepairDirtyMigrationUsesRequestTimeoutForPolling(t *testing.T) {
	client := &migrationRepairAppClient{
		app:        &godo.App{ID: "app-123", Spec: &godo.AppSpec{Name: "bmw-staging"}},
		deployment: &godo.Deployment{ID: "dep-123", Phase: godo.DeploymentPhase_Deploying},
	}
	driver := NewAppPlatformDriverWithClient(client, "nyc3")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()

	result, err := driver.RepairDirtyMigration(ctx, interfaces.MigrationRepairRequest{
		AppResourceName:      "bmw-staging",
		DatabaseResourceName: "bmw-staging-db",
		JobImage:             "registry.digitalocean.com/bmw-registry/workflow-migrate:sha",
		SourceDir:            "/migrations",
		ExpectedDirtyVersion: "20260426000005",
		ForceVersion:         "20260422000001",
		ThenUp:               true,
		ConfirmForce:         interfaces.MigrationRepairConfirmation,
		TimeoutSeconds:       1,
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if result == nil || result.Status != interfaces.MigrationRepairStatusFailed {
		t.Fatalf("result = %+v, want failed result", result)
	}
	if elapsed := time.Since(start); elapsed > 2500*time.Millisecond {
		t.Fatalf("repair elapsed %s, want request timeout to bound polling near 1s", elapsed)
	}
}

func TestAppPlatformRepairDirtyMigrationDoesNotPollPreviousActiveDeployment(t *testing.T) {
	client := &migrationRepairAppClient{
		app: &godo.App{
			ID:               "app-123",
			Spec:             &godo.AppSpec{Name: "bmw-staging"},
			ActiveDeployment: &godo.Deployment{ID: "active-old", Phase: godo.DeploymentPhase_Active},
		},
		useActiveDeploymentOnly: true,
	}
	driver := NewAppPlatformDriverWithClient(client, "nyc3")

	result, err := driver.RepairDirtyMigration(context.Background(), interfaces.MigrationRepairRequest{
		AppResourceName:      "bmw-staging",
		DatabaseResourceName: "bmw-staging-db",
		JobImage:             "registry.digitalocean.com/bmw-registry/workflow-migrate:sha",
		SourceDir:            "/migrations",
		ExpectedDirtyVersion: "20260426000005",
		ForceVersion:         "20260422000001",
		ThenUp:               true,
		ConfirmForce:         interfaces.MigrationRepairConfirmation,
		TimeoutSeconds:       1,
	})
	if err == nil {
		t.Fatal("expected missing update deployment error")
	}
	if result == nil || len(result.Diagnostics) == 0 || !strings.Contains(result.Diagnostics[len(result.Diagnostics)-1].Cause, "timed out waiting for migration repair job") {
		t.Fatalf("result diagnostics = %+v, want job polling timeout diagnostic", result)
	}
	if len(client.listInvocationRequests) < 2 {
		t.Fatalf("listInvocationRequests = %d, want preflight and polling requests", len(client.listInvocationRequests))
	}
	if got := client.listInvocationRequests[1].DeploymentID; got != "" {
		t.Fatalf("DeploymentID filter = %q, want no binding to previous active deployment", got)
	}
}

func TestAppPlatformRepairDirtyMigrationPrefersPendingDeploymentOverPreviousInProgress(t *testing.T) {
	client := &migrationRepairAppClient{
		app: &godo.App{
			ID:                   "app-123",
			Spec:                 &godo.AppSpec{Name: "bmw-staging"},
			InProgressDeployment: &godo.Deployment{ID: "in-progress-old", Phase: godo.DeploymentPhase_Building},
		},
		deployment: &godo.Deployment{ID: "pending-new", Phase: godo.DeploymentPhase_PendingBuild},
		invocations: [][]*godo.JobInvocation{{
			{ID: "job-new", JobName: "$repair", DeploymentID: "pending-new", Phase: godo.JOBINVOCATIONPHASE_Succeeded},
		}},
		usePendingDeployment: true,
	}
	driver := NewAppPlatformDriverWithClient(client, "nyc3")

	result, err := driver.RepairDirtyMigration(context.Background(), interfaces.MigrationRepairRequest{
		AppResourceName:      "bmw-staging",
		DatabaseResourceName: "bmw-staging-db",
		JobImage:             "registry.digitalocean.com/bmw-registry/workflow-migrate:sha",
		SourceDir:            "/migrations",
		ExpectedDirtyVersion: "20260426000005",
		ForceVersion:         "20260422000001",
		ThenUp:               true,
		ConfirmForce:         interfaces.MigrationRepairConfirmation,
		TimeoutSeconds:       1,
	})
	if err != nil {
		t.Fatalf("RepairDirtyMigration: %v", err)
	}
	if result.ProviderJobID != "job-new" {
		t.Fatalf("ProviderJobID = %q, want job-new", result.ProviderJobID)
	}
	if got := client.listInvocationRequests[1].DeploymentID; got != "" {
		t.Fatalf("DeploymentID filter = %q, want no deployment binding", got)
	}
}

func TestAppPlatformRepairDirtyMigrationIgnoresPreExistingPendingDeployment(t *testing.T) {
	client := &migrationRepairAppClient{
		app: &godo.App{
			ID:                "app-123",
			Spec:              &godo.AppSpec{Name: "bmw-staging"},
			PendingDeployment: &godo.Deployment{ID: "pending-old", Phase: godo.DeploymentPhase_PendingBuild},
		},
		deployment: &godo.Deployment{ID: "in-progress-new", Phase: godo.DeploymentPhase_Building},
		invocations: [][]*godo.JobInvocation{{
			{ID: "job-new", JobName: "$repair", DeploymentID: "in-progress-new", Phase: godo.JOBINVOCATIONPHASE_Succeeded},
		}},
		listInvocationErr: context.DeadlineExceeded,
	}
	driver := NewAppPlatformDriverWithClient(client, "nyc3")

	result, err := driver.RepairDirtyMigration(context.Background(), interfaces.MigrationRepairRequest{
		AppResourceName:      "bmw-staging",
		DatabaseResourceName: "bmw-staging-db",
		JobImage:             "registry.digitalocean.com/bmw-registry/workflow-migrate:sha",
		SourceDir:            "/migrations",
		ExpectedDirtyVersion: "20260426000005",
		ForceVersion:         "20260422000001",
		ThenUp:               true,
		ConfirmForce:         interfaces.MigrationRepairConfirmation,
	})
	if err != nil {
		t.Fatalf("RepairDirtyMigration: %v", err)
	}
	if result.ProviderJobID != "job-new" {
		t.Fatalf("ProviderJobID = %q, want job-new", result.ProviderJobID)
	}
	if got := client.listInvocationRequests[1].DeploymentID; got != "" {
		t.Fatalf("DeploymentID filter = %q, want no deployment binding", got)
	}
}

func TestAppPlatformRepairDirtyMigrationPollsForEventuallyConsistentNewDeployment(t *testing.T) {
	prevInterval := migrationRepairPollInterval
	migrationRepairPollInterval = time.Millisecond
	t.Cleanup(func() { migrationRepairPollInterval = prevInterval })
	client := &migrationRepairAppClient{
		app: &godo.App{ID: "app-123", Spec: &godo.AppSpec{Name: "bmw-staging"}},
		deployment: &godo.Deployment{
			ID:    "dep-eventual",
			Phase: godo.DeploymentPhase_Deploying,
		},
		suppressUpdateDeployment: true,
		deploymentOnGet:          &godo.Deployment{ID: "dep-eventual", Phase: godo.DeploymentPhase_Deploying},
		invocations: [][]*godo.JobInvocation{{
			{ID: "job-eventual", JobName: "$repair", DeploymentID: "dep-eventual", Phase: godo.JOBINVOCATIONPHASE_Succeeded},
		}},
	}
	driver := NewAppPlatformDriverWithClient(client, "nyc3")

	result, err := driver.RepairDirtyMigration(context.Background(), interfaces.MigrationRepairRequest{
		AppResourceName:      "bmw-staging",
		DatabaseResourceName: "bmw-staging-db",
		JobImage:             "registry.digitalocean.com/bmw-registry/workflow-migrate:sha",
		SourceDir:            "/migrations",
		ExpectedDirtyVersion: "20260426000005",
		ForceVersion:         "20260422000001",
		ThenUp:               true,
		ConfirmForce:         interfaces.MigrationRepairConfirmation,
		TimeoutSeconds:       1,
	})
	if err != nil {
		t.Fatalf("RepairDirtyMigration: %v", err)
	}
	if result.ProviderJobID != "job-eventual" {
		t.Fatalf("ProviderJobID = %q, want job-eventual", result.ProviderJobID)
	}
	if got := client.listInvocationRequests[1].DeploymentID; got != "" {
		t.Fatalf("DeploymentID filter = %q, want no deployment binding", got)
	}
}

func TestAppPlatformRepairDirtyMigrationSelectsFastActiveNewDeployment(t *testing.T) {
	prevInterval := migrationRepairPollInterval
	migrationRepairPollInterval = time.Millisecond
	t.Cleanup(func() { migrationRepairPollInterval = prevInterval })
	client := &migrationRepairAppClient{
		app: &godo.App{
			ID:               "app-123",
			Spec:             &godo.AppSpec{Name: "bmw-staging"},
			ActiveDeployment: &godo.Deployment{ID: "active-old", Phase: godo.DeploymentPhase_Active},
		},
		deployment:              &godo.Deployment{ID: "active-new", Phase: godo.DeploymentPhase_Active},
		useActiveDeploymentOnly: true,
		invocations: [][]*godo.JobInvocation{{
			{ID: "job-active", JobName: "$repair", DeploymentID: "active-new", Phase: godo.JOBINVOCATIONPHASE_Succeeded},
		}},
	}
	driver := NewAppPlatformDriverWithClient(client, "nyc3")

	result, err := driver.RepairDirtyMigration(context.Background(), interfaces.MigrationRepairRequest{
		AppResourceName:      "bmw-staging",
		DatabaseResourceName: "bmw-staging-db",
		JobImage:             "registry.digitalocean.com/bmw-registry/workflow-migrate:sha",
		SourceDir:            "/migrations",
		ExpectedDirtyVersion: "20260426000005",
		ForceVersion:         "20260422000001",
		ThenUp:               true,
		ConfirmForce:         interfaces.MigrationRepairConfirmation,
		TimeoutSeconds:       1,
	})
	if err != nil {
		t.Fatalf("RepairDirtyMigration: %v", err)
	}
	if result.ProviderJobID != "job-active" {
		t.Fatalf("ProviderJobID = %q, want job-active", result.ProviderJobID)
	}
	if got := client.listInvocationRequests[1].DeploymentID; got != "" {
		t.Fatalf("DeploymentID filter = %q, want no deployment binding", got)
	}
}

func TestAppPlatformRepairDirtyMigrationUsesGeneratedJobInvocationDeployment(t *testing.T) {
	client := &migrationRepairAppClient{
		app:        &godo.App{ID: "app-123", Spec: &godo.AppSpec{Name: "bmw-staging"}},
		deployment: &godo.Deployment{ID: "dep-123", Phase: godo.DeploymentPhase_Deploying},
		invocations: [][]*godo.JobInvocation{{
			{ID: "job-123", JobName: "$repair", DeploymentID: "dep-generated", Phase: godo.JOBINVOCATIONPHASE_Succeeded},
		}},
	}
	driver := NewAppPlatformDriverWithClient(client, "nyc3")

	result, err := driver.RepairDirtyMigration(context.Background(), interfaces.MigrationRepairRequest{
		AppResourceName:      "bmw-staging",
		DatabaseResourceName: "bmw-staging-db",
		JobImage:             "registry.digitalocean.com/bmw-registry/workflow-migrate:sha",
		SourceDir:            "/migrations",
		ExpectedDirtyVersion: "20260426000005",
		ForceVersion:         "20260422000001",
		ThenUp:               true,
		ConfirmForce:         interfaces.MigrationRepairConfirmation,
	})
	if err != nil {
		t.Fatalf("RepairDirtyMigration: %v", err)
	}
	if result.ProviderJobID != "job-123" {
		t.Fatalf("ProviderJobID = %q, want generated job invocation accepted", result.ProviderJobID)
	}
	if got := result.Diagnostics[len(result.Diagnostics)-1].ID; got != "dep-generated" {
		t.Fatalf("diagnostic deployment ID = %q, want invocation deployment", got)
	}
}

func TestAppPlatformRepairDirtyMigrationRestorePreservesConcurrentSpecChanges(t *testing.T) {
	concurrentSpec := &godo.AppSpec{
		Name: "bmw-staging",
		Services: []*godo.AppServiceSpec{{
			Name: "bmw-staging",
			Image: &godo.ImageSourceSpec{
				RegistryType: godo.ImageSourceSpecRegistryType_DOCR,
				Registry:     "bmw-registry",
				Repository:   "buymywishlist",
				Tag:          "new-live-tag",
			},
		}},
	}
	client := &migrationRepairAppClient{
		app: &godo.App{
			ID: "app-123",
			Spec: &godo.AppSpec{
				Name: "bmw-staging",
				Services: []*godo.AppServiceSpec{{
					Name: "bmw-staging",
					Image: &godo.ImageSourceSpec{
						RegistryType: godo.ImageSourceSpecRegistryType_DOCR,
						Registry:     "bmw-registry",
						Repository:   "buymywishlist",
						Tag:          "old-live-tag",
					},
				}},
			},
		},
		deployment:                 &godo.Deployment{ID: "dep-123", Phase: godo.DeploymentPhase_Deploying},
		invocations:                [][]*godo.JobInvocation{{{ID: "job-123", JobName: "$repair", DeploymentID: "dep-123", Phase: godo.JOBINVOCATIONPHASE_Succeeded}}},
		externalSpecBeforeRestore:  concurrentSpec,
		includeRepairJobInLiveSpec: true,
		expectedRestoredServiceTag: "new-live-tag",
		expectedRestoredRepairJobs: 0,
	}
	driver := NewAppPlatformDriverWithClient(client, "nyc3")

	_, err := driver.RepairDirtyMigration(context.Background(), interfaces.MigrationRepairRequest{
		AppResourceName:      "bmw-staging",
		DatabaseResourceName: "bmw-staging-db",
		JobImage:             "registry.digitalocean.com/bmw-registry/workflow-migrate:sha",
		SourceDir:            "/migrations",
		ExpectedDirtyVersion: "20260426000005",
		ForceVersion:         "20260422000001",
		ThenUp:               true,
		ConfirmForce:         interfaces.MigrationRepairConfirmation,
	})
	if err != nil {
		t.Fatalf("RepairDirtyMigration: %v", err)
	}
	restored := client.updates[len(client.updates)-1].Spec
	if got := restored.Services[0].Image.Tag; got != "new-live-tag" {
		t.Fatalf("restored service image tag = %q, want concurrent value new-live-tag", got)
	}
	if len(restored.Jobs) != 0 {
		t.Fatalf("restored jobs = %d, want repair job removed", len(restored.Jobs))
	}
}

func TestAppPlatformRepairDirtyMigrationFindsInvocationOnLaterPage(t *testing.T) {
	client := &migrationRepairAppClient{
		app:        &godo.App{ID: "app-123", Spec: &godo.AppSpec{Name: "bmw-staging"}},
		deployment: &godo.Deployment{ID: "dep-123", Phase: godo.DeploymentPhase_Deploying},
		invocations: [][]*godo.JobInvocation{
			{{ID: "job-other", JobName: "other-job", DeploymentID: "dep-123", Phase: godo.JOBINVOCATIONPHASE_Succeeded}},
			{{ID: "job-123", JobName: "$repair", DeploymentID: "dep-123", Phase: godo.JOBINVOCATIONPHASE_Succeeded}},
		},
		paginateInvocations: true,
	}
	driver := NewAppPlatformDriverWithClient(client, "nyc3")

	result, err := driver.RepairDirtyMigration(context.Background(), interfaces.MigrationRepairRequest{
		AppResourceName:      "bmw-staging",
		DatabaseResourceName: "bmw-staging-db",
		JobImage:             "registry.digitalocean.com/bmw-registry/workflow-migrate:sha",
		SourceDir:            "/migrations",
		ExpectedDirtyVersion: "20260426000005",
		ForceVersion:         "20260422000001",
		ThenUp:               true,
		ConfirmForce:         interfaces.MigrationRepairConfirmation,
	})
	if err != nil {
		t.Fatalf("RepairDirtyMigration: %v", err)
	}
	if result.ProviderJobID != "job-123" {
		t.Fatalf("ProviderJobID = %q, want later page job", result.ProviderJobID)
	}
	if len(client.listInvocationRequests) != 3 || client.listInvocationRequests[2].Page != 2 {
		t.Fatalf("listInvocationRequests = %+v, want second page queried", client.listInvocationRequests)
	}
}

func TestAppPlatformRepairDirtyMigrationPreservesOtherTemporaryRepairJobs(t *testing.T) {
	client := &migrationRepairAppClient{
		app: &godo.App{
			ID: "app-123",
			Spec: &godo.AppSpec{
				Name: "bmw-staging",
				Jobs: []*godo.AppJobSpec{{
					Name: migrationRepairJobPrefix + "-other",
					Kind: godo.AppJobSpecKind_PreDeploy,
				}},
			},
		},
		deployment: &godo.Deployment{ID: "dep-123", Phase: godo.DeploymentPhase_Deploying},
		invocations: [][]*godo.JobInvocation{{
			{ID: "job-123", JobName: "$repair", DeploymentID: "dep-123", Phase: godo.JOBINVOCATIONPHASE_Succeeded},
		}},
	}
	driver := NewAppPlatformDriverWithClient(client, "nyc3")

	_, err := driver.RepairDirtyMigration(context.Background(), interfaces.MigrationRepairRequest{
		AppResourceName:      "bmw-staging",
		DatabaseResourceName: "bmw-staging-db",
		JobImage:             "registry.digitalocean.com/bmw-registry/workflow-migrate:sha",
		SourceDir:            "/migrations",
		ExpectedDirtyVersion: "20260426000005",
		ForceVersion:         "20260422000001",
		ThenUp:               true,
		ConfirmForce:         interfaces.MigrationRepairConfirmation,
	})
	if err != nil {
		t.Fatalf("RepairDirtyMigration: %v", err)
	}
	tempSpec := client.updates[0].Spec
	if len(tempSpec.Jobs) != 2 {
		t.Fatalf("temporary jobs = %d, want existing repair job plus this repair job", len(tempSpec.Jobs))
	}
	if tempSpec.Jobs[0].Name != migrationRepairJobPrefix+"-other" {
		t.Fatalf("existing repair job was not preserved: %+v", tempSpec.Jobs)
	}
	restoredSpec := client.updates[len(client.updates)-1].Spec
	if len(restoredSpec.Jobs) != 1 || restoredSpec.Jobs[0].Name != migrationRepairJobPrefix+"-other" {
		t.Fatalf("restored jobs = %+v, want only other repair job preserved", restoredSpec.Jobs)
	}
}

func TestWithoutMigrationRepairJobNamePreservesNilJobs(t *testing.T) {
	jobs := []*godo.AppJobSpec{
		nil,
		{Name: migrationRepairJobPrefix + "-current"},
		{Name: migrationRepairJobPrefix + "-other"},
	}
	out := withoutMigrationRepairJobName(jobs, migrationRepairJobPrefix+"-current")
	if len(out) != 2 || out[0] != nil || out[1].Name != migrationRepairJobPrefix+"-other" {
		t.Fatalf("jobs = %+v, want nil and unrelated job preserved", out)
	}
}

func TestAppPlatformRepairDirtyMigrationGeneratesUniqueJobNames(t *testing.T) {
	client := &migrationRepairAppClient{
		app: &godo.App{ID: "app-123", Spec: &godo.AppSpec{Name: "bmw-staging"}},
		deployments: []*godo.Deployment{
			{ID: "dep-1", Phase: godo.DeploymentPhase_Deploying},
			{ID: "dep-2", Phase: godo.DeploymentPhase_Deploying},
		},
		invocations: [][]*godo.JobInvocation{
			{{ID: "job-1", JobName: "$repair", DeploymentID: "dep-1", Phase: godo.JOBINVOCATIONPHASE_Succeeded}},
			{{ID: "job-2", JobName: "$repair", DeploymentID: "dep-2", Phase: godo.JOBINVOCATIONPHASE_Succeeded}},
		},
	}
	driver := NewAppPlatformDriverWithClient(client, "nyc3")
	req := interfaces.MigrationRepairRequest{
		AppResourceName:      "bmw-staging",
		DatabaseResourceName: "bmw-staging-db",
		JobImage:             "registry.digitalocean.com/bmw-registry/workflow-migrate:sha",
		SourceDir:            "/migrations",
		ExpectedDirtyVersion: "20260426000005",
		ForceVersion:         "20260422000001",
		ThenUp:               true,
		ConfirmForce:         interfaces.MigrationRepairConfirmation,
	}

	if _, err := driver.RepairDirtyMigration(context.Background(), req); err != nil {
		t.Fatalf("first RepairDirtyMigration: %v", err)
	}
	if _, err := driver.RepairDirtyMigration(context.Background(), req); err != nil {
		t.Fatalf("second RepairDirtyMigration: %v", err)
	}
	first := client.updates[0].Spec.Jobs[0].Name
	second := client.updates[2].Spec.Jobs[0].Name
	if first == second {
		t.Fatalf("job names collided: %q", first)
	}
	if len(client.listInvocationRequests) != 4 {
		t.Fatalf("listInvocationRequests = %d, want 4", len(client.listInvocationRequests))
	}
	if client.listInvocationRequests[1].JobNames[0] != first || client.listInvocationRequests[3].JobNames[0] != second {
		t.Fatalf("listInvocationRequests = %+v, want generated job names %q and %q", client.listInvocationRequests, first, second)
	}
}

func TestMigrationRepairJobNameFitsDigitalOceanLimit(t *testing.T) {
	for range 100 {
		name := migrationRepairJobName()
		if !strings.HasPrefix(name, migrationRepairJobPrefix+"-") {
			t.Fatalf("job name = %q", name)
		}
		if len(name) > 32 {
			t.Fatalf("job name length = %d for %q, want <= 32", len(name), name)
		}
	}
}

func TestMigrationRepairJobNameFallbackFitsDigitalOceanLimit(t *testing.T) {
	migrationRepairNameHookMu.Lock()
	t.Cleanup(migrationRepairNameHookMu.Unlock)
	originalRead := migrationRepairRandomRead
	migrationRepairRandomRead = func([]byte) (int, error) {
		return 0, errors.New("entropy unavailable")
	}
	t.Cleanup(func() {
		migrationRepairRandomRead = originalRead
	})

	name := migrationRepairJobName()
	if !strings.HasPrefix(name, migrationRepairJobPrefix+"-") {
		t.Fatalf("job name = %q", name)
	}
	if len(name) > 32 {
		t.Fatalf("job name length = %d for %q, want <= 32", len(name), name)
	}
}

func TestMigrationRepairJobNameAbsentFromSpecRetriesComponentCollisions(t *testing.T) {
	tests := map[string]*godo.AppSpec{
		"database":    {Databases: []*godo.AppDatabaseSpec{{Name: "wfctl-mig-repair-collision"}}},
		"function":    {Functions: []*godo.AppFunctionsSpec{{Name: "wfctl-mig-repair-collision"}}},
		"job":         {Jobs: []*godo.AppJobSpec{{Name: "wfctl-mig-repair-collision"}}},
		"service":     {Services: []*godo.AppServiceSpec{{Name: "wfctl-mig-repair-collision"}}},
		"static_site": {StaticSites: []*godo.AppStaticSiteSpec{{Name: "wfctl-mig-repair-collision"}}},
		"worker":      {Workers: []*godo.AppWorkerSpec{{Name: "wfctl-mig-repair-collision"}}},
	}

	for name, spec := range tests {
		t.Run(name, func(t *testing.T) {
			migrationRepairNameHookMu.Lock()
			t.Cleanup(migrationRepairNameHookMu.Unlock)
			originalGenerator := migrationRepairJobNameGenerator
			names := []string{"wfctl-mig-repair-collision", "wfctl-mig-repair-available"}
			migrationRepairJobNameGenerator = func() string {
				name := names[0]
				names = names[1:]
				return name
			}
			t.Cleanup(func() {
				migrationRepairJobNameGenerator = originalGenerator
			})

			got, err := migrationRepairJobNameAbsentFromSpec(spec)
			if err != nil {
				t.Fatalf("migrationRepairJobNameAbsentFromSpec() error = %v", err)
			}
			if got != "wfctl-mig-repair-available" {
				t.Fatalf("migrationRepairJobNameAbsentFromSpec() = %q, want available candidate", got)
			}
		})
	}
}

func TestAppPlatformRepairDirtyMigrationRetriesLiveComponentNameCollision(t *testing.T) {
	migrationRepairNameHookMu.Lock()
	t.Cleanup(migrationRepairNameHookMu.Unlock)
	originalGenerator := migrationRepairJobNameGenerator
	names := []string{"wfctl-mig-repair-collision", "wfctl-mig-repair-available"}
	migrationRepairJobNameGenerator = func() string {
		name := names[0]
		names = names[1:]
		return name
	}
	t.Cleanup(func() {
		migrationRepairJobNameGenerator = originalGenerator
	})

	client := &migrationRepairAppClient{
		app: &godo.App{
			ID:   "app-123",
			Spec: &godo.AppSpec{Name: "bmw-staging"},
		},
		getSpecs: []*godo.AppSpec{{
			Name:     "bmw-staging",
			Services: []*godo.AppServiceSpec{{Name: "wfctl-mig-repair-collision"}},
		}},
		deployment: &godo.Deployment{ID: "dep-123", Phase: godo.DeploymentPhase_Deploying},
		invocations: [][]*godo.JobInvocation{{
			{ID: "job-123", JobName: "$repair", DeploymentID: "dep-123", Phase: godo.JOBINVOCATIONPHASE_Succeeded},
		}},
	}
	driver := NewAppPlatformDriverWithClient(client, "nyc3")
	_, err := driver.RepairDirtyMigration(context.Background(), interfaces.MigrationRepairRequest{
		AppResourceName:      "bmw-staging",
		DatabaseResourceName: "bmw-staging-db",
		JobImage:             "registry.digitalocean.com/bmw-registry/workflow-migrate:sha",
		SourceDir:            "/migrations",
		ExpectedDirtyVersion: "20260426000005",
		ForceVersion:         "20260422000001",
		ThenUp:               true,
		ConfirmForce:         interfaces.MigrationRepairConfirmation,
	})
	if err != nil {
		t.Fatalf("RepairDirtyMigration: %v", err)
	}
	if got := client.updates[0].Spec.Jobs[0].Name; got != "wfctl-mig-repair-available" {
		t.Fatalf("temporary job name = %q, want collision-free generated name", got)
	}
	if client.listInvocationRequests[1].JobNames[0] != "wfctl-mig-repair-available" {
		t.Fatalf("listInvocationRequests = %+v, want collision-free generated name", client.listInvocationRequests)
	}
	restoredSpec := client.updates[len(client.updates)-1].Spec
	if len(restoredSpec.Jobs) != 0 {
		t.Fatalf("restored jobs = %+v, want generated repair job removed", restoredSpec.Jobs)
	}
}

func TestAppPlatformRepairDirtyMigrationRetriesUpdateComponentNameConflict(t *testing.T) {
	migrationRepairNameHookMu.Lock()
	t.Cleanup(migrationRepairNameHookMu.Unlock)
	originalGenerator := migrationRepairJobNameGenerator
	names := []string{"wfctl-mig-repair-race", "wfctl-mig-repair-available"}
	migrationRepairJobNameGenerator = func() string {
		name := names[0]
		names = names[1:]
		return name
	}
	t.Cleanup(func() {
		migrationRepairJobNameGenerator = originalGenerator
	})

	client := &migrationRepairAppClient{
		app:                       &godo.App{ID: "app-123", Spec: &godo.AppSpec{Name: "bmw-staging"}},
		componentNameConflictOnce: true,
		getSpecs: []*godo.AppSpec{
			{Name: "bmw-staging"},
			{Name: "bmw-staging", Services: []*godo.AppServiceSpec{{Name: "wfctl-mig-repair-race"}}},
		},
		deployment: &godo.Deployment{ID: "dep-123", Phase: godo.DeploymentPhase_Deploying},
		invocations: [][]*godo.JobInvocation{{
			{ID: "job-123", JobName: "$repair", DeploymentID: "dep-123", Phase: godo.JOBINVOCATIONPHASE_Succeeded},
		}},
	}
	driver := NewAppPlatformDriverWithClient(client, "nyc3")
	_, err := driver.RepairDirtyMigration(context.Background(), interfaces.MigrationRepairRequest{
		AppResourceName:      "bmw-staging",
		DatabaseResourceName: "bmw-staging-db",
		JobImage:             "registry.digitalocean.com/bmw-registry/workflow-migrate:sha",
		SourceDir:            "/migrations",
		ExpectedDirtyVersion: "20260426000005",
		ForceVersion:         "20260422000001",
		ThenUp:               true,
		ConfirmForce:         interfaces.MigrationRepairConfirmation,
	})
	if err != nil {
		t.Fatalf("RepairDirtyMigration: %v", err)
	}
	if len(client.updates) != 2 {
		t.Fatalf("updates = %d, want successful temporary update + restore after retry", len(client.updates))
	}
	if got := client.updates[0].Spec.Jobs[0].Name; got != "wfctl-mig-repair-available" {
		t.Fatalf("temporary job name = %q, want second generated name after update conflict", got)
	}
}

func TestMigrationRepairComponentNameConflictClassifier(t *testing.T) {
	tests := []string{
		"component name must be unique across all app components",
		"services[0].name must be unique",
		"duplicate name",
	}
	for _, message := range tests {
		err := &godo.ErrorResponse{
			Response: &http.Response{StatusCode: http.StatusUnprocessableEntity},
			Message:  message,
		}
		if !isMigrationRepairComponentNameConflict(err) {
			t.Fatalf("isMigrationRepairComponentNameConflict(%q) = false, want true", message)
		}
	}
}

func TestAppPlatformRepairDirtyMigrationJobInvocationUnsupportedReturnsUnimplemented(t *testing.T) {
	client := &migrationRepairAppClient{
		app: &godo.App{ID: "app-123", Spec: &godo.AppSpec{Name: "bmw-staging"}},
		deployment: &godo.Deployment{
			ID:    "dep-123",
			Phase: godo.DeploymentPhase_Deploying,
		},
		preflightErr: &godo.ErrorResponse{
			Response: &http.Response{StatusCode: http.StatusMethodNotAllowed},
			Message:  "method not allowed",
		},
	}
	driver := NewAppPlatformDriverWithClient(client, "nyc3")

	_, err := driver.RepairDirtyMigration(context.Background(), interfaces.MigrationRepairRequest{
		AppResourceName:      "bmw-staging",
		DatabaseResourceName: "bmw-staging-db",
		JobImage:             "registry.digitalocean.com/bmw-registry/workflow-migrate:sha",
		SourceDir:            "/migrations",
		ExpectedDirtyVersion: "20260426000005",
		ForceVersion:         "20260422000001",
		ThenUp:               true,
		ConfirmForce:         interfaces.MigrationRepairConfirmation,
	})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("code = %s, want %s (err=%v)", status.Code(err), codes.Unimplemented, err)
	}
	if len(client.updates) != 0 {
		t.Fatalf("updates = %d, want unsupported API detected before mutation", len(client.updates))
	}
}

func TestAppPlatformRepairDirtyMigrationUnsupportedClientReturnsUnimplemented(t *testing.T) {
	driver := NewAppPlatformDriverWithClient(&migrationRepairBaseAppClient{
		app: &godo.App{ID: "app-123", Spec: &godo.AppSpec{Name: "bmw-staging"}},
	}, "nyc3")

	_, err := driver.RepairDirtyMigration(context.Background(), interfaces.MigrationRepairRequest{
		AppResourceName:      "bmw-staging",
		DatabaseResourceName: "bmw-staging-db",
		JobImage:             "registry.digitalocean.com/bmw-registry/workflow-migrate:sha",
		SourceDir:            "/migrations",
		ExpectedDirtyVersion: "20260426000005",
		ForceVersion:         "20260422000001",
		ThenUp:               true,
		ConfirmForce:         interfaces.MigrationRepairConfirmation,
	})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("code = %s, want %s (err=%v)", status.Code(err), codes.Unimplemented, err)
	}
}

func TestAppPlatformRepairDirtyMigrationMissingDeploymentRestoresWithDiagnostic(t *testing.T) {
	client := &migrationRepairAppClient{
		app: &godo.App{ID: "app-123", Spec: &godo.AppSpec{Name: "bmw-staging"}},
	}
	driver := NewAppPlatformDriverWithClient(client, "nyc3")

	result, err := driver.RepairDirtyMigration(context.Background(), interfaces.MigrationRepairRequest{
		AppResourceName:      "bmw-staging",
		DatabaseResourceName: "bmw-staging-db",
		JobImage:             "registry.digitalocean.com/bmw-registry/workflow-migrate:sha",
		SourceDir:            "/migrations",
		ExpectedDirtyVersion: "20260426000005",
		ForceVersion:         "20260422000001",
		ThenUp:               true,
		ConfirmForce:         interfaces.MigrationRepairConfirmation,
		TimeoutSeconds:       1,
	})
	if err == nil {
		t.Fatal("expected missing deployment error")
	}
	if result == nil || len(result.Diagnostics) == 0 || !strings.Contains(result.Diagnostics[len(result.Diagnostics)-1].Cause, "timed out waiting for migration repair job") {
		t.Fatalf("result diagnostics = %+v, want job polling timeout diagnostic", result)
	}
	if len(client.updates) != 2 {
		t.Fatalf("updates = %d, want temporary update + restore", len(client.updates))
	}
}

type migrationRepairBaseAppClient struct {
	app *godo.App
}

func (m *migrationRepairBaseAppClient) Create(_ context.Context, _ *godo.AppCreateRequest) (*godo.App, *godo.Response, error) {
	return nil, nil, nil
}

func (m *migrationRepairBaseAppClient) Get(_ context.Context, _ string) (*godo.App, *godo.Response, error) {
	return m.app, nil, nil
}

func (m *migrationRepairBaseAppClient) List(_ context.Context, _ *godo.ListOptions) ([]*godo.App, *godo.Response, error) {
	return []*godo.App{m.app}, &godo.Response{}, nil
}

func (m *migrationRepairBaseAppClient) Update(_ context.Context, _ string, req *godo.AppUpdateRequest) (*godo.App, *godo.Response, error) {
	m.app.Spec = req.Spec
	return m.app, nil, nil
}

func (m *migrationRepairBaseAppClient) CreateDeployment(_ context.Context, _ string, _ ...*godo.DeploymentCreateRequest) (*godo.Deployment, *godo.Response, error) {
	return nil, nil, nil
}

func (m *migrationRepairBaseAppClient) ListDeployments(_ context.Context, _ string, _ *godo.ListOptions) ([]*godo.Deployment, *godo.Response, error) {
	return nil, nil, nil
}

func (m *migrationRepairBaseAppClient) Delete(_ context.Context, _ string) (*godo.Response, error) {
	return nil, nil
}

type migrationRepairAppClient struct {
	app                        *godo.App
	getSpecs                   []*godo.AppSpec
	deployment                 *godo.Deployment
	deployments                []*godo.Deployment
	deploymentOnGet            *godo.Deployment
	invocations                [][]*godo.JobInvocation
	logs                       *godo.AppLogs
	listInvocationErr          error
	preflightErr               error
	requirePreflightDeadline   bool
	paginateInvocations        bool
	updateErrAfterApply        bool
	componentNameConflictOnce  bool
	updates                    []*godo.AppUpdateRequest
	deploymentsCreated         []string
	cancelledJobs              []string
	listInvocationRequests     []*godo.ListJobInvocationsOptions
	useActiveDeploymentOnly    bool
	usePendingDeployment       bool
	suppressUpdateDeployment   bool
	requireLiveUpdate          bool
	externalSpecBeforeRestore  *godo.AppSpec
	includeRepairJobInLiveSpec bool
	expectedRestoredServiceTag string
	expectedRestoredRepairJobs int
}

func (m *migrationRepairAppClient) Create(_ context.Context, _ *godo.AppCreateRequest) (*godo.App, *godo.Response, error) {
	return nil, nil, nil
}

func (m *migrationRepairAppClient) Get(_ context.Context, _ string) (*godo.App, *godo.Response, error) {
	if len(m.getSpecs) > 0 {
		spec, err := cloneAppSpec(m.getSpecs[0])
		if err != nil {
			panic(err)
		}
		m.getSpecs = m.getSpecs[1:]
		appCopy := *m.app
		appCopy.Spec = spec
		if m.deploymentOnGet != nil && len(m.updates) > 0 {
			appCopy.InProgressDeployment = m.deploymentOnGet
		}
		return &appCopy, nil, nil
	}
	if m.deploymentOnGet != nil && len(m.updates) > 0 {
		m.app.InProgressDeployment = m.deploymentOnGet
	}
	return m.app, nil, nil
}

func (m *migrationRepairAppClient) List(_ context.Context, _ *godo.ListOptions) ([]*godo.App, *godo.Response, error) {
	return []*godo.App{m.app}, &godo.Response{}, nil
}

func (m *migrationRepairAppClient) Update(_ context.Context, _ string, req *godo.AppUpdateRequest) (*godo.App, *godo.Response, error) {
	if m.componentNameConflictOnce {
		m.componentNameConflictOnce = false
		return m.app, nil, &godo.ErrorResponse{
			Response: &http.Response{StatusCode: http.StatusUnprocessableEntity},
			Message:  "component name must be unique across all app components",
		}
	}
	m.updates = append(m.updates, req)
	m.app.Spec = req.Spec
	if len(req.Spec.Jobs) > 0 {
		deployment := m.nextDeployment()
		if deployment == nil {
			return m.app, nil, nil
		}
		if m.suppressUpdateDeployment {
			m.bindRepairJobName(req.Spec.Jobs[len(req.Spec.Jobs)-1].Name)
			return m.app, nil, nil
		}
		if m.useActiveDeploymentOnly {
			m.app.ActiveDeployment = deployment
		} else if m.usePendingDeployment {
			m.app.PendingDeployment = deployment
		} else {
			m.app.InProgressDeployment = deployment
		}
		m.bindRepairJobName(req.Spec.Jobs[len(req.Spec.Jobs)-1].Name)
		if m.externalSpecBeforeRestore != nil {
			live, err := cloneAppSpec(m.externalSpecBeforeRestore)
			if err != nil {
				panic(err)
			}
			if m.includeRepairJobInLiveSpec {
				live.Jobs = append(live.Jobs, req.Spec.Jobs[len(req.Spec.Jobs)-1])
			}
			m.app.Spec = live
		}
	}
	if m.updateErrAfterApply && len(m.updates) == 1 {
		return m.app, nil, context.DeadlineExceeded
	}
	return m.app, nil, nil
}

func (m *migrationRepairAppClient) nextDeployment() *godo.Deployment {
	if len(m.deployments) > 0 {
		deployment := m.deployments[0]
		m.deployments = m.deployments[1:]
		return deployment
	}
	return m.deployment
}

func (m *migrationRepairAppClient) bindRepairJobName(jobName string) {
	for _, batch := range m.invocations {
		bound := false
		for _, invocation := range batch {
			if invocation != nil && invocation.JobName == "$repair" {
				invocation.JobName = jobName
				bound = true
			}
		}
		if bound {
			return
		}
	}
}

func (m *migrationRepairAppClient) CreateDeployment(_ context.Context, appID string, _ ...*godo.DeploymentCreateRequest) (*godo.Deployment, *godo.Response, error) {
	m.deploymentsCreated = append(m.deploymentsCreated, appID)
	return m.deployment, nil, nil
}

func (m *migrationRepairAppClient) ListDeployments(_ context.Context, _ string, _ *godo.ListOptions) ([]*godo.Deployment, *godo.Response, error) {
	return []*godo.Deployment{m.deployment}, nil, nil
}

func (m *migrationRepairAppClient) Delete(_ context.Context, _ string) (*godo.Response, error) {
	return nil, nil
}

func (m *migrationRepairAppClient) GetDeployment(_ context.Context, _ string, _ string) (*godo.Deployment, *godo.Response, error) {
	return m.deployment, nil, nil
}

func (m *migrationRepairAppClient) GetLogs(_ context.Context, _ string, _ string, _ string, _ godo.AppLogType, _ bool, _ int) (*godo.AppLogs, *godo.Response, error) {
	return m.logs, nil, nil
}

func (m *migrationRepairAppClient) ListJobInvocations(ctx context.Context, _ string, opts *godo.ListJobInvocationsOptions) ([]*godo.JobInvocation, *godo.Response, error) {
	if opts != nil {
		copied := *opts
		copied.JobNames = append([]string(nil), opts.JobNames...)
		m.listInvocationRequests = append(m.listInvocationRequests, &copied)
	}
	if opts != nil && len(opts.JobNames) == 0 && opts.DeploymentID == "" && opts.PerPage == 1 {
		if m.requirePreflightDeadline {
			if _, ok := ctx.Deadline(); !ok {
				return nil, nil, context.DeadlineExceeded
			}
		}
		if m.preflightErr != nil {
			return nil, nil, m.preflightErr
		}
		return nil, &godo.Response{}, nil
	}
	if len(m.invocations) == 0 {
		if m.listInvocationErr != nil {
			return nil, nil, m.listInvocationErr
		}
		return nil, nil, nil
	}
	if m.paginateInvocations && opts != nil && opts.Page != 1 && opts.Page != 2 {
		return nil, &godo.Response{}, nil
	}
	out := m.invocations[0]
	m.invocations = m.invocations[1:]
	resp := &godo.Response{}
	if m.paginateInvocations && opts != nil && opts.Page == 1 {
		resp.Links = &godo.Links{Pages: &godo.Pages{Next: "https://api.digitalocean.com/v2/apps/app-123/jobs?page=2"}}
	}
	return out, resp, nil
}

func (m *migrationRepairAppClient) GetJobInvocation(_ context.Context, _ string, _ string, _ *godo.GetJobInvocationOptions) (*godo.JobInvocation, *godo.Response, error) {
	return nil, nil, nil
}

func (m *migrationRepairAppClient) GetJobInvocationLogs(_ context.Context, _ string, _ string, _ *godo.GetJobInvocationLogsOptions) (*godo.AppLogs, *godo.Response, error) {
	return m.logs, nil, nil
}

func (m *migrationRepairAppClient) CancelJobInvocation(_ context.Context, _ string, jobInvocationID string, _ *godo.CancelJobInvocationOptions) (*godo.JobInvocation, *godo.Response, error) {
	m.cancelledJobs = append(m.cancelledJobs, jobInvocationID)
	return &godo.JobInvocation{ID: jobInvocationID, Phase: godo.JOBINVOCATIONPHASE_Canceled}, nil, nil
}
