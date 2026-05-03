package drivers_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// mockStorageActionsClient is a tiny stand-in for godo.StorageActionsService
// that only handles the Resize call the VolumeDriver issues. resizedTo
// captures the size parameter so tests can assert resize was actually called
// with the desired GB.
type mockStorageActionsClient struct {
	err       error
	resizedTo int
	region    string
}

func (m *mockStorageActionsClient) Resize(_ context.Context, _ string, sizeGB int, region string) (*godo.Action, *godo.Response, error) {
	m.resizedTo = sizeGB
	m.region = region
	if m.err != nil {
		return nil, nil, m.err
	}
	return &godo.Action{Status: "completed"}, nil, nil
}

func testVolume() *godo.Volume {
	return &godo.Volume{
		ID:             "vol-aaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Name:           "pg-data",
		Region:         &godo.Region{Slug: "nyc3"},
		SizeGigaBytes:  100,
		FilesystemType: "ext4",
		Description:    "Postgres data volume",
		Tags:           []string{"prod", "pg"},
	}
}

func TestVolumeDriver_Create(t *testing.T) {
	mock := &mockStorageClient{vol: testVolume()}
	d := drivers.NewVolumeDriverWithClient(mock, nil, "nyc3")

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "pg-data",
		Config: map[string]any{
			"size_gb":         100,
			"filesystem_type": "ext4",
			"description":     "Postgres data volume",
			"tags":            []any{"prod", "pg"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID == "" {
		t.Fatal("ProviderID empty")
	}
	if out.Outputs["filesystem_type"] != "ext4" {
		t.Errorf("filesystem_type = %v", out.Outputs["filesystem_type"])
	}
	if out.Outputs["size_gb"].(float64) != 100 {
		t.Errorf("size_gb = %v", out.Outputs["size_gb"])
	}
	got := mock.gotCreate
	if got == nil {
		t.Fatal("create request not captured")
	}
	if got.Name != "pg-data" || got.SizeGigaBytes != 100 || got.FilesystemType != "ext4" || got.Description != "Postgres data volume" {
		t.Errorf("create request = %+v", got)
	}
	if got.Region != "nyc3" {
		t.Errorf("Region = %q, want nyc3", got.Region)
	}
	if len(got.Tags) != 2 {
		t.Errorf("Tags len = %d", len(got.Tags))
	}
}

func TestVolumeDriver_Create_RegionOverride(t *testing.T) {
	mock := &mockStorageClient{vol: testVolume()}
	d := drivers.NewVolumeDriverWithClient(mock, nil, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "pg-data",
		Config: map[string]any{"size_gb": 50, "region": "sfo3"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if mock.gotCreate.Region != "sfo3" {
		t.Errorf("Region = %q, want sfo3", mock.gotCreate.Region)
	}
}

func TestVolumeDriver_Create_MissingSize(t *testing.T) {
	mock := &mockStorageClient{vol: testVolume()}
	d := drivers.NewVolumeDriverWithClient(mock, nil, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "pg-data",
		Config: map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error for missing size_gb")
	}
}

func TestVolumeDriver_Create_APIError(t *testing.T) {
	mock := &mockStorageClient{err: fmt.Errorf("api failure")}
	d := drivers.NewVolumeDriverWithClient(mock, nil, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "pg-data",
		Config: map[string]any{"size_gb": 50},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestVolumeDriver_Create_EmptyIDFromAPI(t *testing.T) {
	mock := &mockStorageClient{vol: &godo.Volume{Name: "pg-data"}}
	d := drivers.NewVolumeDriverWithClient(mock, nil, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "pg-data",
		Config: map[string]any{"size_gb": 50},
	})
	if err == nil {
		t.Fatal("expected error for empty ID from API")
	}
}

func TestVolumeDriver_Create_FractionalSizeRejected(t *testing.T) {
	// Copilot finding #2: intFromConfig truncates float64 — size_gb: 100.9
	// previously created a 100 GB volume silently. The strict path must
	// reject fractional values explicitly so operators can fix the typo.
	mock := &mockStorageClient{vol: testVolume()}
	d := drivers.NewVolumeDriverWithClient(mock, nil, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "pg-data",
		Config: map[string]any{"size_gb": 100.9},
	})
	if err == nil {
		t.Fatal("expected error rejecting fractional size_gb")
	}
	wantSubstr := "fractional value 100.9 rejected"
	if !contains(err.Error(), wantSubstr) {
		t.Errorf("error %q missing %q", err.Error(), wantSubstr)
	}
	if mock.gotCreate != nil {
		t.Errorf("CreateVolume must not be called when size_gb is fractional; got %+v", mock.gotCreate)
	}
}

func TestVolumeDriver_Update_FractionalSizeRejected(t *testing.T) {
	mock := &mockStorageClient{vol: testVolume()}
	actions := &mockStorageActionsClient{}
	d := drivers.NewVolumeDriverWithClient(mock, actions, "nyc3")

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "pg-data", ProviderID: "vol-aaaa",
	}, interfaces.ResourceSpec{
		Name:   "pg-data",
		Config: map[string]any{"size_gb": 200.5},
	})
	if err == nil {
		t.Fatal("expected error rejecting fractional size_gb on update")
	}
	if actions.resizedTo != 0 {
		t.Errorf("Resize must not be called for fractional size; resizedTo = %d", actions.resizedTo)
	}
}

func TestVolumeDriver_Create_WholeFloatAccepted(t *testing.T) {
	// structpb collapses 50 → float64(50) on the wire; whole-valued floats
	// must still pass the strict check.
	mock := &mockStorageClient{vol: testVolume()}
	d := drivers.NewVolumeDriverWithClient(mock, nil, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "pg-data",
		Config: map[string]any{"size_gb": float64(50)},
	})
	if err != nil {
		t.Fatalf("Create with whole-valued float64 size_gb must succeed: %v", err)
	}
	if mock.gotCreate == nil || mock.gotCreate.SizeGigaBytes != 50 {
		t.Errorf("CreateVolume.SizeGigaBytes = %v, want 50", mock.gotCreate)
	}
}

func TestVolumeDriver_Read_Success(t *testing.T) {
	mock := &mockStorageClient{vol: testVolume()}
	d := drivers.NewVolumeDriverWithClient(mock, nil, "nyc3")

	out, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name: "pg-data", ProviderID: "vol-aaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.Name != "pg-data" {
		t.Errorf("Name = %q", out.Name)
	}
}

func TestVolumeDriver_Read_EmptyProviderID(t *testing.T) {
	mock := &mockStorageClient{vol: testVolume()}
	d := drivers.NewVolumeDriverWithClient(mock, nil, "nyc3")

	_, err := d.Read(context.Background(), interfaces.ResourceRef{Name: "pg-data"})
	if err == nil {
		t.Fatal("expected error for empty ProviderID")
	}
}

func TestVolumeDriver_Update_Resize(t *testing.T) {
	current := testVolume()
	updated := *current
	updated.SizeGigaBytes = 200
	mock := &mockStorageClient{vol: current}
	actions := &mockStorageActionsClient{}
	d := drivers.NewVolumeDriverWithClient(mock, actions, "nyc3")

	// Prepare: GetVolume first returns the original, then the resized version.
	// Our simple mock returns vol on every call, so swap after Resize().
	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "pg-data", ProviderID: current.ID,
	}, interfaces.ResourceSpec{
		Name:   "pg-data",
		Config: map[string]any{"size_gb": 200},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if actions.resizedTo != 200 {
		t.Errorf("resizedTo = %d, want 200", actions.resizedTo)
	}
	if actions.region != "nyc3" {
		t.Errorf("resize region = %q, want nyc3", actions.region)
	}
}

func TestVolumeDriver_Update_NoChange(t *testing.T) {
	mock := &mockStorageClient{vol: testVolume()}
	actions := &mockStorageActionsClient{}
	d := drivers.NewVolumeDriverWithClient(mock, actions, "nyc3")

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "pg-data", ProviderID: "vol-aaaa",
	}, interfaces.ResourceSpec{
		Name:   "pg-data",
		Config: map[string]any{"size_gb": 100}, // same as current
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if actions.resizedTo != 0 {
		t.Errorf("Resize must not be called when size unchanged; resizedTo = %d", actions.resizedTo)
	}
}

func TestVolumeDriver_Update_ShrinkRejected(t *testing.T) {
	mock := &mockStorageClient{vol: testVolume()} // 100 GB
	actions := &mockStorageActionsClient{}
	d := drivers.NewVolumeDriverWithClient(mock, actions, "nyc3")

	_, err := d.Update(context.Background(), interfaces.ResourceRef{
		Name: "pg-data", ProviderID: "vol-aaaa",
	}, interfaces.ResourceSpec{
		Name:   "pg-data",
		Config: map[string]any{"size_gb": 50},
	})
	if err == nil {
		t.Fatal("expected error rejecting shrink")
	}
	if actions.resizedTo != 0 {
		t.Errorf("shrink must not invoke Resize; resizedTo = %d", actions.resizedTo)
	}
}

func TestVolumeDriver_Update_EmptyProviderID(t *testing.T) {
	mock := &mockStorageClient{vol: testVolume()}
	d := drivers.NewVolumeDriverWithClient(mock, nil, "nyc3")

	_, err := d.Update(context.Background(), interfaces.ResourceRef{Name: "pg-data"}, interfaces.ResourceSpec{
		Name:   "pg-data",
		Config: map[string]any{"size_gb": 200},
	})
	if err == nil {
		t.Fatal("expected error for empty ProviderID")
	}
}

func TestVolumeDriver_Delete_Success(t *testing.T) {
	mock := &mockStorageClient{vol: testVolume()}
	d := drivers.NewVolumeDriverWithClient(mock, nil, "nyc3")

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "pg-data", ProviderID: "vol-aaaa",
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestVolumeDriver_Delete_Error(t *testing.T) {
	mock := &mockStorageClient{err: fmt.Errorf("delete failed")}
	d := drivers.NewVolumeDriverWithClient(mock, nil, "nyc3")

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "pg-data", ProviderID: "vol-aaaa",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestVolumeDriver_Delete_EmptyProviderID(t *testing.T) {
	mock := &mockStorageClient{vol: testVolume()}
	d := drivers.NewVolumeDriverWithClient(mock, nil, "nyc3")

	err := d.Delete(context.Background(), interfaces.ResourceRef{Name: "pg-data"})
	if err == nil {
		t.Fatal("expected error for empty ProviderID")
	}
}

func TestVolumeDriver_Diff_NilCurrent(t *testing.T) {
	d := drivers.NewVolumeDriverWithClient(&mockStorageClient{}, nil, "nyc3")
	r, err := d.Diff(context.Background(), interfaces.ResourceSpec{}, nil)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !r.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=true for nil current")
	}
}

func TestVolumeDriver_Diff_GrowIsUpdateNotReplace(t *testing.T) {
	d := drivers.NewVolumeDriverWithClient(&mockStorageClient{}, nil, "nyc3")
	cur := &interfaces.ResourceOutput{
		Outputs: map[string]any{"size_gb": float64(100)},
	}
	r, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{"size_gb": 200},
	}, cur)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !r.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=true for size growth")
	}
	if r.NeedsReplace {
		t.Errorf("size growth must NOT force replace; got NeedsReplace=true")
	}
}

func TestVolumeDriver_Diff_ShrinkForcesReplace(t *testing.T) {
	d := drivers.NewVolumeDriverWithClient(&mockStorageClient{}, nil, "nyc3")
	cur := &interfaces.ResourceOutput{
		Outputs: map[string]any{"size_gb": float64(100)},
	}
	r, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{"size_gb": 50},
	}, cur)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !r.NeedsReplace {
		t.Errorf("shrink must force replace")
	}
}

func TestVolumeDriver_Diff_RegionForcesReplace(t *testing.T) {
	d := drivers.NewVolumeDriverWithClient(&mockStorageClient{}, nil, "nyc3")
	cur := &interfaces.ResourceOutput{
		Outputs: map[string]any{"size_gb": float64(100), "region": "nyc3"},
	}
	r, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{"size_gb": 100, "region": "sfo3"},
	}, cur)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !r.NeedsReplace {
		t.Errorf("region change must force replace")
	}
}

func TestVolumeDriver_Diff_FilesystemTypeForcesReplace(t *testing.T) {
	d := drivers.NewVolumeDriverWithClient(&mockStorageClient{}, nil, "nyc3")
	cur := &interfaces.ResourceOutput{
		Outputs: map[string]any{"size_gb": float64(100), "filesystem_type": "ext4"},
	}
	r, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{"size_gb": 100, "filesystem_type": "xfs"},
	}, cur)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !r.NeedsReplace {
		t.Errorf("filesystem_type change must force replace")
	}
}

func TestVolumeDriver_Diff_TagsChangeForcesReplace(t *testing.T) {
	// Copilot finding #5: changes to description / tags were silently
	// ignored because Diff did not compare them. DO Block Storage has no
	// update endpoints for these fields (godo.StorageActions only exposes
	// Resize), so a change MUST surface as ForceNew rather than being
	// dropped.
	d := drivers.NewVolumeDriverWithClient(&mockStorageClient{}, nil, "nyc3")
	cur := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"size_gb": float64(100),
			"tags":    []any{"prod", "pg"},
		},
	}
	r, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"size_gb": 100,
			"tags":    []any{"prod", "pg", "audit"},
		},
	}, cur)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !r.NeedsReplace {
		t.Errorf("tags change must force replace; NeedsReplace=%v", r.NeedsReplace)
	}
	if !r.NeedsUpdate {
		t.Errorf("tags change must signal NeedsUpdate=true")
	}
	// Confirm the FieldChange for tags is present and ForceNew.
	var found bool
	for _, c := range r.Changes {
		if c.Path == "tags" && c.ForceNew {
			found = true
		}
	}
	if !found {
		t.Errorf("expected FieldChange{Path:\"tags\", ForceNew:true} in %+v", r.Changes)
	}
}

func TestVolumeDriver_Diff_TagsUnorderedNoChange(t *testing.T) {
	// DO doesn't preserve tag order across reads. Equal-set must NOT
	// trigger a replace.
	d := drivers.NewVolumeDriverWithClient(&mockStorageClient{}, nil, "nyc3")
	cur := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"size_gb": float64(100),
			"tags":    []any{"prod", "pg"},
		},
	}
	r, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"size_gb": 100,
			"tags":    []any{"pg", "prod"}, // reordered
		},
	}, cur)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if r.NeedsReplace {
		t.Errorf("reordered-but-equal tags must NOT force replace")
	}
}

func TestVolumeDriver_Diff_DescriptionChangeForcesReplace(t *testing.T) {
	d := drivers.NewVolumeDriverWithClient(&mockStorageClient{}, nil, "nyc3")
	cur := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"size_gb":     float64(100),
			"description": "old description",
		},
	}
	r, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"size_gb":     100,
			"description": "new description",
		},
	}, cur)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !r.NeedsReplace {
		t.Errorf("description change must force replace")
	}
}

func TestVolumeDriver_Diff_NoChanges(t *testing.T) {
	d := drivers.NewVolumeDriverWithClient(&mockStorageClient{}, nil, "nyc3")
	cur := &interfaces.ResourceOutput{
		Outputs: map[string]any{"size_gb": float64(100), "region": "nyc3", "filesystem_type": "ext4"},
	}
	r, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{"size_gb": 100, "region": "nyc3", "filesystem_type": "ext4"},
	}, cur)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if r.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=false when nothing changed")
	}
}

func TestVolumeDriver_HealthCheck_Success(t *testing.T) {
	mock := &mockStorageClient{vol: testVolume()}
	d := drivers.NewVolumeDriverWithClient(mock, nil, "nyc3")

	r, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name: "pg-data", ProviderID: "vol-aaaa",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if !r.Healthy {
		t.Errorf("expected healthy")
	}
}

func TestVolumeDriver_HealthCheck_ReadError(t *testing.T) {
	mock := &mockStorageClient{err: fmt.Errorf("api down")}
	d := drivers.NewVolumeDriverWithClient(mock, nil, "nyc3")

	r, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name: "pg-data", ProviderID: "vol-aaaa",
	})
	if err != nil {
		t.Fatalf("HealthCheck must not return error; got %v", err)
	}
	if r.Healthy {
		t.Errorf("expected unhealthy when read fails")
	}
}

func TestVolumeDriver_HealthCheck_EmptyProviderID(t *testing.T) {
	mock := &mockStorageClient{vol: testVolume()}
	d := drivers.NewVolumeDriverWithClient(mock, nil, "nyc3")

	r, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{Name: "pg-data"})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if r.Healthy {
		t.Errorf("expected unhealthy for empty ProviderID")
	}
}

func TestVolumeDriver_ProviderIDFormat_IsUUID(t *testing.T) {
	d := drivers.NewVolumeDriverWithClient(&mockStorageClient{}, nil, "nyc3")
	if got := d.ProviderIDFormat(); got != interfaces.IDFormatUUID {
		t.Errorf("ProviderIDFormat = %v, want IDFormatUUID", got)
	}
}

func TestVolumeDriver_Scale_NotSupported(t *testing.T) {
	d := drivers.NewVolumeDriverWithClient(&mockStorageClient{}, nil, "nyc3")
	_, err := d.Scale(context.Background(), interfaces.ResourceRef{}, 2)
	if err == nil {
		t.Fatal("expected scale not supported error")
	}
}
