package digitalocean_test

import (
	"testing"

	"github.com/GoCodeAlone/workflow/wftest"
)

// TestIntegration_DODeployAppPlatform exercises a single-step pipeline that
// mocks a DigitalOcean App Platform deployment step.
func TestIntegration_DODeployAppPlatform(t *testing.T) {
	h := wftest.New(t,
		wftest.WithYAML(`
pipelines:
  deploy-app:
    steps:
      - name: deploy
        type: step.do_deploy
        config:
          resource_type: infra.container_service
          region: nyc3
          app_name: my-app
`),
		wftest.MockStep("step.do_deploy", wftest.Returns(map[string]any{
			"app_id":   "abc-123-def",
			"live_url": "https://my-app-xyz.ondigitalocean.app",
			"status":   "ACTIVE",
		})),
	)

	result := h.ExecutePipeline("deploy-app", nil)
	if result.Error != nil {
		t.Fatalf("pipeline failed: %v", result.Error)
	}
	if !result.StepExecuted("deploy") {
		t.Error("deploy step should have executed")
	}
	output := result.StepOutput("deploy")
	if output["status"] != "ACTIVE" {
		t.Errorf("expected status=ACTIVE, got %v", output["status"])
	}
	if output["app_id"] != "abc-123-def" {
		t.Errorf("expected app_id=abc-123-def, got %v", output["app_id"])
	}
	if output["live_url"] != "https://my-app-xyz.ondigitalocean.app" {
		t.Errorf("expected live_url=https://my-app-xyz.ondigitalocean.app, got %v", output["live_url"])
	}
}

// TestIntegration_DOProvisionKubernetes exercises a single-step pipeline that
// mocks a DigitalOcean Kubernetes (DOKS) cluster provisioning step.
func TestIntegration_DOProvisionKubernetes(t *testing.T) {
	rec := wftest.RecordStep("step.do_k8s_provision")
	rec.WithOutput(map[string]any{
		"cluster_id":  "k8s-abc-789",
		"endpoint":    "https://k8s-abc-789.k8s.ondigitalocean.com",
		"node_count":  3,
		"status":      "running",
		"kube_config": "apiVersion: v1\nkind: Config\n...",
	})

	h := wftest.New(t,
		wftest.WithYAML(`
pipelines:
  provision-k8s:
    steps:
      - name: provision
        type: step.do_k8s_provision
        config:
          resource_type: infra.k8s_cluster
          region: nyc3
          cluster_name: prod-cluster
          node_pool:
            size: s-4vcpu-8gb
            count: 3
`),
		rec,
	)

	result := h.ExecutePipeline("provision-k8s", nil)
	if result.Error != nil {
		t.Fatalf("pipeline failed: %v", result.Error)
	}
	if rec.CallCount() != 1 {
		t.Errorf("expected step called once, got %d", rec.CallCount())
	}
	output := result.StepOutput("provision")
	if output["status"] != "running" {
		t.Errorf("expected status=running, got %v", output["status"])
	}
	if output["cluster_id"] != "k8s-abc-789" {
		t.Errorf("expected cluster_id=k8s-abc-789, got %v", output["cluster_id"])
	}
	calls := rec.Calls()
	if len(calls) == 0 {
		t.Fatal("expected at least one recorded call")
	}
	cfg := calls[0].Config
	if cfg["cluster_name"] != "prod-cluster" {
		t.Errorf("expected cluster_name=prod-cluster in step config, got %v", cfg["cluster_name"])
	}
}

// TestIntegration_DOMultiStepPipeline exercises a two-step pipeline that mocks
// a DigitalOcean Spaces bucket creation followed by an App Platform deployment
// that references the bucket endpoint.
func TestIntegration_DOMultiStepPipeline(t *testing.T) {
	spacesRec := wftest.RecordStep("step.do_spaces_create")
	spacesRec.WithOutput(map[string]any{
		"bucket_name":     "my-app-assets",
		"endpoint":        "https://my-app-assets.nyc3.digitaloceanspaces.com",
		"region":          "nyc3",
		"status":          "active",
	})

	deployRec := wftest.RecordStep("step.do_deploy")
	deployRec.WithOutput(map[string]any{
		"app_id":   "xyz-456-ghi",
		"live_url": "https://my-app-xyz2.ondigitalocean.app",
		"status":   "ACTIVE",
	})

	h := wftest.New(t,
		wftest.WithYAML(`
pipelines:
  deploy-with-storage:
    steps:
      - name: create-bucket
        type: step.do_spaces_create
        config:
          resource_type: infra.storage
          region: nyc3
          bucket_name: my-app-assets
      - name: deploy-app
        type: step.do_deploy
        config:
          resource_type: infra.container_service
          region: nyc3
          app_name: my-app
          env_vars:
            ASSETS_ENDPOINT: "{{ .create-bucket.endpoint }}"
`),
		spacesRec,
		deployRec,
	)

	result := h.ExecutePipeline("deploy-with-storage", nil)
	if result.Error != nil {
		t.Fatalf("pipeline failed: %v", result.Error)
	}
	if result.StepCount() != 2 {
		t.Errorf("expected 2 steps executed, got %d", result.StepCount())
	}
	if !result.StepExecuted("create-bucket") {
		t.Error("create-bucket step should have executed")
	}
	if !result.StepExecuted("deploy-app") {
		t.Error("deploy-app step should have executed")
	}
	if spacesRec.CallCount() != 1 {
		t.Errorf("expected create-bucket called once, got %d", spacesRec.CallCount())
	}
	if deployRec.CallCount() != 1 {
		t.Errorf("expected deploy-app called once, got %d", deployRec.CallCount())
	}
	bucketOut := result.StepOutput("create-bucket")
	if bucketOut["status"] != "active" {
		t.Errorf("expected bucket status=active, got %v", bucketOut["status"])
	}
	deployOut := result.StepOutput("deploy-app")
	if deployOut["status"] != "ACTIVE" {
		t.Errorf("expected deploy status=ACTIVE, got %v", deployOut["status"])
	}
}
