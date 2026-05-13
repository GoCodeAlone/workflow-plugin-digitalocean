package steps_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/digitalocean/godo"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/steps"
)

// fakeScaleClient is a minimal in-memory AppsScaleClient for unit tests.
type fakeScaleClient struct {
	getApp    *godo.App
	getErr    error
	updateApp *godo.App
	updateErr error

	// capture the request so tests can verify the spec sent to Update.
	lastUpdateReq *godo.AppUpdateRequest
}

func (f *fakeScaleClient) Get(_ context.Context, _ string) (*godo.App, *godo.Response, error) {
	return f.getApp, scaleOKResp(), f.getErr
}

func (f *fakeScaleClient) Update(_ context.Context, _ string, req *godo.AppUpdateRequest) (*godo.App, *godo.Response, error) {
	f.lastUpdateReq = req
	return f.updateApp, scaleOKResp(), f.updateErr
}

func scaleOKResp() *godo.Response {
	return &godo.Response{Response: &http.Response{StatusCode: http.StatusOK}}
}

// makeScaleTestApp builds a minimal *godo.App with one service component.
func makeScaleTestApp(appID, appName, componentName string, instanceCount int64) *godo.App {
	return &godo.App{
		ID: appID,
		Spec: &godo.AppSpec{
			Name: appName,
			Services: []*godo.AppServiceSpec{
				{
					Name:          componentName,
					InstanceCount: instanceCount,
				},
			},
		},
		ActiveDeployment: &godo.Deployment{
			ID:    "dep-before",
			Phase: godo.DeploymentPhase_Active,
		},
	}
}

// makeScaleTestAppMultiSvc builds a *godo.App with multiple service components.
func makeScaleTestAppMultiSvc(appID, appName string, svcs []struct{ name string; count int64 }) *godo.App {
	specs := make([]*godo.AppServiceSpec, 0, len(svcs))
	for _, s := range svcs {
		specs = append(specs, &godo.AppServiceSpec{
			Name:          s.name,
			InstanceCount: s.count,
		})
	}
	return &godo.App{
		ID: appID,
		Spec: &godo.AppSpec{
			Name:     appName,
			Services: specs,
		},
		ActiveDeployment: &godo.Deployment{
			ID:    "dep-before",
			Phase: godo.DeploymentPhase_Active,
		},
	}
}

// TestIaCScale_ScaleUp verifies that scaling from 1→3 returns correct outputs.
func TestIaCScale_ScaleUp(t *testing.T) {
	beforeApp := makeScaleTestApp("app-123", "my-app", "web", 1)
	afterApp := &godo.App{
		ID:   "app-123",
		Spec: beforeApp.Spec,
		ActiveDeployment: &godo.Deployment{
			ID:    "dep-after",
			Phase: godo.DeploymentPhase_Active,
		},
		InProgressDeployment: &godo.Deployment{
			ID:    "dep-new",
			Phase: godo.DeploymentPhase_Building,
		},
	}
	fake := &fakeScaleClient{
		getApp:    beforeApp,
		updateApp: afterApp,
	}

	step := steps.NewIaCScaleStep("scale_up", fake)
	result, err := step.Execute(context.Background(), nil, nil,
		map[string]any{
			"app_id": "app-123",
		},
		nil,
		map[string]any{
			"component_name": "web",
			"instance_count": float64(3),
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output["app_id"] != "app-123" {
		t.Errorf("app_id = %v, want app-123", result.Output["app_id"])
	}
	if result.Output["previous_count"] != int64(1) {
		t.Errorf("previous_count = %v, want 1", result.Output["previous_count"])
	}
	if result.Output["new_count"] != int64(3) {
		t.Errorf("new_count = %v, want 3", result.Output["new_count"])
	}
	if result.Output["deployment_id"] != "dep-new" {
		t.Errorf("deployment_id = %v, want dep-new", result.Output["deployment_id"])
	}
	// Verify only the target component was modified.
	if fake.lastUpdateReq == nil {
		t.Fatal("Update was not called")
	}
	for _, svc := range fake.lastUpdateReq.Spec.Services {
		if svc.Name == "web" && svc.InstanceCount != 3 {
			t.Errorf("service web InstanceCount = %d, want 3", svc.InstanceCount)
		}
	}
}

// TestIaCScale_ScaleDown verifies scaling from 5→2.
func TestIaCScale_ScaleDown(t *testing.T) {
	beforeApp := makeScaleTestApp("app-456", "my-app", "api", 5)
	afterApp := &godo.App{
		ID:   "app-456",
		Spec: beforeApp.Spec,
		InProgressDeployment: &godo.Deployment{
			ID:    "dep-scale-down",
			Phase: godo.DeploymentPhase_Deploying,
		},
	}
	fake := &fakeScaleClient{
		getApp:    beforeApp,
		updateApp: afterApp,
	}

	step := steps.NewIaCScaleStep("scale_down", fake)
	result, err := step.Execute(context.Background(), nil, nil, nil, nil,
		map[string]any{
			"app_id":         "app-456",
			"component_name": "api",
			"instance_count": float64(2),
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output["previous_count"] != int64(5) {
		t.Errorf("previous_count = %v, want 5", result.Output["previous_count"])
	}
	if result.Output["new_count"] != int64(2) {
		t.Errorf("new_count = %v, want 2", result.Output["new_count"])
	}
}

// TestIaCScale_Idempotent verifies that scaling to the current count is a no-op
// (Update is not called, previous_count == new_count).
func TestIaCScale_Idempotent(t *testing.T) {
	beforeApp := makeScaleTestApp("app-789", "my-app", "worker", 2)
	fake := &fakeScaleClient{
		getApp: beforeApp,
		// updateApp intentionally nil — test verifies Update is not called.
	}

	step := steps.NewIaCScaleStep("scale_noop", fake)
	result, err := step.Execute(context.Background(), nil, nil, nil, nil,
		map[string]any{
			"app_id":         "app-789",
			"component_name": "worker",
			"instance_count": float64(2),
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.lastUpdateReq != nil {
		t.Error("Update should not be called when count is already at target")
	}
	if result.Output["previous_count"] != int64(2) {
		t.Errorf("previous_count = %v, want 2", result.Output["previous_count"])
	}
	if result.Output["new_count"] != int64(2) {
		t.Errorf("new_count = %v, want 2", result.Output["new_count"])
	}
	if result.Output["deployment_id"] != "" {
		t.Errorf("deployment_id should be empty for no-op, got %v", result.Output["deployment_id"])
	}
}

// TestIaCScale_OnlyTargetComponentModified verifies that other services
// in a multi-service app are NOT modified when scaling a specific component.
func TestIaCScale_OnlyTargetComponentModified(t *testing.T) {
	// Use a slice instead of map to ensure deterministic ordering.
	beforeApp := makeScaleTestAppMultiSvc("app-multi", "multi-svc-app", []struct{ name string; count int64 }{
		{"web", 1},
		{"worker", 3},
		{"api", 2},
	})
	afterApp := &godo.App{
		ID:   "app-multi",
		Spec: beforeApp.Spec,
		InProgressDeployment: &godo.Deployment{
			ID:    "dep-multi",
			Phase: godo.DeploymentPhase_Building,
		},
	}
	fake := &fakeScaleClient{
		getApp:    beforeApp,
		updateApp: afterApp,
	}

	step := steps.NewIaCScaleStep("scale_web_only", fake)
	_, err := step.Execute(context.Background(), nil, nil, nil, nil,
		map[string]any{
			"app_id":         "app-multi",
			"component_name": "web",
			"instance_count": float64(5),
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.lastUpdateReq == nil {
		t.Fatal("Update was not called")
	}
	for _, svc := range fake.lastUpdateReq.Spec.Services {
		switch svc.Name {
		case "web":
			if svc.InstanceCount != 5 {
				t.Errorf("web InstanceCount = %d, want 5", svc.InstanceCount)
			}
		case "worker":
			if svc.InstanceCount != 3 {
				t.Errorf("worker InstanceCount = %d, want 3 (must not change)", svc.InstanceCount)
			}
		case "api":
			if svc.InstanceCount != 2 {
				t.Errorf("api InstanceCount = %d, want 2 (must not change)", svc.InstanceCount)
			}
		}
	}
}

// TestIaCScale_SpecCopyDoesNotMutateOriginal verifies that setServiceCount copies
// the AppServiceSpec struct so the original AppServiceSpec pointed to by the Get
// response is not mutated (only the slice element is replaced with a new pointer).
func TestIaCScale_SpecCopyDoesNotMutateOriginal(t *testing.T) {
	beforeApp := makeScaleTestApp("app-copy", "my-app", "web", 1)
	// Capture the original AppServiceSpec pointer before Execute — not the slice element.
	// The slice element (Services[0]) will be updated to point to a new copy;
	// the original struct pointed to by originalSvc must remain unchanged.
	originalSvc := beforeApp.Spec.Services[0]
	originalCount := originalSvc.InstanceCount

	// afterApp uses a distinct spec (not beforeApp.Spec) so Update and Get
	// don't share the same *AppSpec pointer.
	afterApp := &godo.App{
		ID: "app-copy",
		Spec: &godo.AppSpec{
			Name: "my-app",
			Services: []*godo.AppServiceSpec{
				{Name: "web", InstanceCount: 4},
			},
		},
		InProgressDeployment: &godo.Deployment{ID: "dep-copy"},
	}
	fake := &fakeScaleClient{getApp: beforeApp, updateApp: afterApp}

	step := steps.NewIaCScaleStep("scale_copy", fake)
	_, err := step.Execute(context.Background(), nil, nil, nil, nil,
		map[string]any{
			"app_id":         "app-copy",
			"component_name": "web",
			"instance_count": float64(4),
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The original AppServiceSpec struct (not the slice element) must not have
	// been mutated. setServiceCount copies the struct and replaces the slice
	// element pointer; the original struct pointed to by originalSvc is untouched.
	if originalSvc.InstanceCount != originalCount {
		t.Errorf("original AppServiceSpec struct was mutated: got %d, want %d",
			originalSvc.InstanceCount, originalCount)
	}
}

// TestIaCScale_InvalidComponentName verifies that a component_name not found
// in the app's services returns a clear error.
func TestIaCScale_InvalidComponentName(t *testing.T) {
	beforeApp := makeScaleTestApp("app-notfound", "my-app", "web", 1)
	fake := &fakeScaleClient{getApp: beforeApp}

	step := steps.NewIaCScaleStep("scale_bad_comp", fake)
	_, err := step.Execute(context.Background(), nil, nil, nil, nil,
		map[string]any{
			"app_id":         "app-notfound",
			"component_name": "nonexistent-service",
			"instance_count": float64(3),
		},
	)
	if err == nil {
		t.Fatal("expected error for unknown component_name")
	}
}

// TestIaCScale_NegativeInstanceCount verifies that instance_count < 0 is rejected.
func TestIaCScale_NegativeInstanceCount(t *testing.T) {
	fake := &fakeScaleClient{}

	step := steps.NewIaCScaleStep("scale_negative", fake)
	_, err := step.Execute(context.Background(), nil, nil, nil, nil,
		map[string]any{
			"app_id":         "app-any",
			"component_name": "web",
			"instance_count": float64(-1),
		},
	)
	if err == nil {
		t.Fatal("expected error for negative instance_count")
	}
}

// TestIaCScale_FractionalInstanceCount verifies that a fractional float
// instance_count (e.g. 2.9) is rejected rather than silently truncated.
func TestIaCScale_FractionalInstanceCount(t *testing.T) {
	fake := &fakeScaleClient{}

	step := steps.NewIaCScaleStep("scale_fractional", fake)
	_, err := step.Execute(context.Background(), nil, nil, nil, nil,
		map[string]any{
			"app_id":         "app-any",
			"component_name": "web",
			"instance_count": float64(2.9),
		},
	)
	if err == nil {
		t.Fatal("expected error for fractional instance_count")
	}
}

// TestIaCScale_MissingAppID verifies that missing app_id is rejected.
func TestIaCScale_MissingAppID(t *testing.T) {
	fake := &fakeScaleClient{}

	step := steps.NewIaCScaleStep("scale_no_appid", fake)
	_, err := step.Execute(context.Background(), nil, nil, nil, nil,
		map[string]any{
			"component_name": "web",
			"instance_count": float64(2),
		},
	)
	if err == nil {
		t.Fatal("expected error for missing app_id")
	}
}

// TestIaCScale_MissingComponentName verifies that missing component_name is rejected.
func TestIaCScale_MissingComponentName(t *testing.T) {
	fake := &fakeScaleClient{}

	step := steps.NewIaCScaleStep("scale_no_comp", fake)
	_, err := step.Execute(context.Background(), nil, nil, nil, nil,
		map[string]any{
			"app_id":         "app-any",
			"instance_count": float64(2),
		},
	)
	if err == nil {
		t.Fatal("expected error for missing component_name")
	}
}

// TestIaCScale_NilClient verifies that Execute with a nil client returns a
// clear initialization error instead of panicking.
func TestIaCScale_NilClient(t *testing.T) {
	step := steps.NewIaCScaleStep("scale_nil", nil)
	_, err := step.Execute(context.Background(), nil, nil, nil, nil,
		map[string]any{
			"app_id":         "app-any",
			"component_name": "web",
			"instance_count": float64(2),
		},
	)
	if err == nil {
		t.Fatal("expected error for nil client")
	}
}

// TestIaCScale_GodoConflict verifies that a 409 Conflict from godo (concurrent update)
// is propagated with a clear error message.
func TestIaCScale_GodoConflict(t *testing.T) {
	beforeApp := makeScaleTestApp("app-409", "my-app", "web", 1)
	fake := &fakeScaleClient{
		getApp: beforeApp,
		updateErr: &godo.ErrorResponse{
			Response: &http.Response{StatusCode: http.StatusConflict},
			Message:  "another deployment is already in progress",
		},
	}

	step := steps.NewIaCScaleStep("scale_409", fake)
	_, err := step.Execute(context.Background(), nil, nil, nil, nil,
		map[string]any{
			"app_id":         "app-409",
			"component_name": "web",
			"instance_count": float64(2),
		},
	)
	if err == nil {
		t.Fatal("expected error for 409 conflict")
	}
	if msg := err.Error(); len(msg) == 0 {
		t.Fatal("error message should not be empty")
	}
}

// Ensure fakeScaleClient satisfies the AppsScaleClient interface.
var _ steps.AppsScaleClient = (*fakeScaleClient)(nil)
