package drivers_test

import (
	"context"
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

func TestSpacesDriver_Delete(t *testing.T) {
	mock := &mockSpacesClient{}
	d := drivers.NewSpacesDriverWithClient(mock, "nyc3")

	err := d.Delete(context.Background(), interfaces.ResourceRef{Name: "my-bucket"})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
