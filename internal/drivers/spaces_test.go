package drivers_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
)

type mockSpacesClient struct {
	bucket *drivers.SpacesBucket
	err    error
}

func (m *mockSpacesClient) CreateBucket(_ context.Context, name, region string) (*drivers.SpacesBucket, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.bucket != nil {
		return m.bucket, nil
	}
	return &drivers.SpacesBucket{Name: name, Region: region, CreatedAt: time.Now()}, nil
}

func (m *mockSpacesClient) GetBucket(_ context.Context, name, region string) (*drivers.SpacesBucket, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.bucket != nil {
		return m.bucket, nil
	}
	return &drivers.SpacesBucket{Name: name, Region: region}, nil
}

func (m *mockSpacesClient) DeleteBucket(_ context.Context, _, _ string) error {
	return m.err
}

func TestSpacesDriver_Create(t *testing.T) {
	mock := &mockSpacesClient{}
	d := drivers.NewSpacesDriverWithClient(mock, "nyc3")

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-bucket",
		Config: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID != "my-bucket" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "my-bucket")
	}
	if ep, _ := out.Outputs["endpoint"].(string); ep == "" {
		t.Error("expected endpoint in outputs")
	}
}

func TestSpacesDriver_Create_Error(t *testing.T) {
	mock := &mockSpacesClient{err: fmt.Errorf("api failure")}
	d := drivers.NewSpacesDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-bucket",
		Config: map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestSpacesDriver_Read_Success(t *testing.T) {
	mock := &mockSpacesClient{}
	d := drivers.NewSpacesDriverWithClient(mock, "nyc3")

	out, err := d.Read(context.Background(), interfaces.ResourceRef{Name: "my-bucket"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.ProviderID != "my-bucket" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "my-bucket")
	}
}

func TestSpacesDriver_Delete(t *testing.T) {
	mock := &mockSpacesClient{}
	d := drivers.NewSpacesDriverWithClient(mock, "nyc3")

	err := d.Delete(context.Background(), interfaces.ResourceRef{Name: "my-bucket"})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestSpacesDriver_Delete_Error(t *testing.T) {
	mock := &mockSpacesClient{err: fmt.Errorf("delete failed")}
	d := drivers.NewSpacesDriverWithClient(mock, "nyc3")

	err := d.Delete(context.Background(), interfaces.ResourceRef{Name: "my-bucket"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestSpacesDriver_Diff_NilCurrent(t *testing.T) {
	mock := &mockSpacesClient{}
	d := drivers.NewSpacesDriverWithClient(mock, "nyc3")

	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{Name: "my-bucket"}, nil)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=true when current is nil")
	}
}

func TestSpacesDriver_Diff_NoChanges(t *testing.T) {
	mock := &mockSpacesClient{}
	d := drivers.NewSpacesDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{ProviderID: "my-bucket"}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{Name: "my-bucket"}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=false when current exists")
	}
}

func TestSpacesDriver_HealthCheck_Healthy(t *testing.T) {
	mock := &mockSpacesClient{}
	d := drivers.NewSpacesDriverWithClient(mock, "nyc3")

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "my-bucket"})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if !result.Healthy {
		t.Errorf("expected healthy bucket")
	}
}

func TestSpacesDriver_HealthCheck_Unhealthy(t *testing.T) {
	mock := &mockSpacesClient{err: fmt.Errorf("bucket not found")}
	d := drivers.NewSpacesDriverWithClient(mock, "nyc3")

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "my-bucket"})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if result.Healthy {
		t.Errorf("expected unhealthy when get fails")
	}
}
