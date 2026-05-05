package drivers_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

type mockDropletClient struct {
	droplet *godo.Droplet
	err     error
	// gotReq captures the most recent Create request so tests can assert on
	// the godo struct fields the driver populated from spec.Config.
	gotReq *godo.DropletCreateRequest
}

func (m *mockDropletClient) Create(_ context.Context, req *godo.DropletCreateRequest) (*godo.Droplet, *godo.Response, error) {
	m.gotReq = req
	return m.droplet, nil, m.err
}
func (m *mockDropletClient) Get(_ context.Context, _ int) (*godo.Droplet, *godo.Response, error) {
	return m.droplet, nil, m.err
}
func (m *mockDropletClient) Delete(_ context.Context, _ int) (*godo.Response, error) {
	return nil, m.err
}

// mockStorageClient is a lightweight stand-in for godo.StorageService used by
// volume tests and the droplet volumes-by-name path. CreateVolume returns
// vol; ListVolumes filters byName from the volumes slice.
type mockStorageClient struct {
	vol     *godo.Volume
	volumes []godo.Volume // pool returned by ListVolumes (filtered by name)
	err     error
	// gotCreate captures the most recent CreateVolume request.
	gotCreate *godo.VolumeCreateRequest
	// gotListParams captures the most recent ListVolumes params for assertion.
	gotListParams *godo.ListVolumeParams
}

func (m *mockStorageClient) CreateVolume(_ context.Context, req *godo.VolumeCreateRequest) (*godo.Volume, *godo.Response, error) {
	m.gotCreate = req
	return m.vol, nil, m.err
}
func (m *mockStorageClient) GetVolume(_ context.Context, _ string) (*godo.Volume, *godo.Response, error) {
	return m.vol, nil, m.err
}
func (m *mockStorageClient) DeleteVolume(_ context.Context, _ string) (*godo.Response, error) {
	return nil, m.err
}
func (m *mockStorageClient) ListVolumes(_ context.Context, params *godo.ListVolumeParams) ([]godo.Volume, *godo.Response, error) {
	m.gotListParams = params
	if m.err != nil {
		return nil, nil, m.err
	}
	if params == nil || params.Name == "" {
		return m.volumes, nil, nil
	}
	var out []godo.Volume
	for _, v := range m.volumes {
		if v.Name == params.Name {
			out = append(out, v)
		}
	}
	return out, nil, nil
}

func testDroplet() *godo.Droplet {
	return &godo.Droplet{
		ID:     42,
		Name:   "my-droplet",
		Status: "active",
		Size:   &godo.Size{Slug: "s-1vcpu-2gb"},
		Region: &godo.Region{Slug: "nyc3"},
		Networks: &godo.Networks{
			V4: []godo.NetworkV4{
				{IPAddress: "1.2.3.4", Type: "public"},
				{IPAddress: "10.0.0.4", Type: "private"},
			},
		},
	}
}

func TestDropletDriver_Create(t *testing.T) {
	mock := &mockDropletClient{droplet: testDroplet()}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-droplet",
		Config: map[string]any{
			"size":  "s-1vcpu-2gb",
			"image": "ubuntu-24-04-x64",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID != "42" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "42")
	}
	if out.Status != "active" {
		t.Errorf("Status = %q, want %q", out.Status, "active")
	}
	if got, _ := out.Outputs["private_ip"].(string); got != "10.0.0.4" {
		t.Errorf("private_ip = %q, want %q", got, "10.0.0.4")
	}
}

func TestDropletDriver_Create_Error(t *testing.T) {
	mock := &mockDropletClient{err: fmt.Errorf("api failure")}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-droplet",
		Config: map[string]any{"size": "s-1vcpu-2gb", "image": "ubuntu-24-04-x64"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDropletDriver_Read_Success(t *testing.T) {
	mock := &mockDropletClient{droplet: testDroplet()}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	out, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name: "my-droplet", ProviderID: "42",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.ProviderID != "42" {
		t.Errorf("ProviderID = %q, want %q", out.ProviderID, "42")
	}
}

func TestDropletDriver_Delete_Success(t *testing.T) {
	mock := &mockDropletClient{droplet: testDroplet()}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "my-droplet", ProviderID: "42",
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestDropletDriver_Delete_Error(t *testing.T) {
	mock := &mockDropletClient{err: fmt.Errorf("delete failed")}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	err := d.Delete(context.Background(), interfaces.ResourceRef{
		Name: "my-droplet", ProviderID: "42",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestDropletDriver_Diff_HasChanges(t *testing.T) {
	mock := &mockDropletClient{}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{"size": "s-1vcpu-2gb"},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{"size": "s-2vcpu-4gb"},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=true for size change")
	}
	if !result.NeedsReplace {
		t.Errorf("expected NeedsReplace=true for size change (ForceNew)")
	}
}

func TestDropletDriver_Diff_NoChanges(t *testing.T) {
	mock := &mockDropletClient{}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{"size": "s-1vcpu-2gb"},
	}
	result, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{"size": "s-1vcpu-2gb"},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.NeedsUpdate {
		t.Errorf("expected NeedsUpdate=false when size unchanged")
	}
}

func TestDropletDriver_HealthCheck(t *testing.T) {
	mock := &mockDropletClient{droplet: testDroplet()}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name:       "my-droplet",
		ProviderID: "42",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if !result.Healthy {
		t.Errorf("expected healthy droplet")
	}
}

func TestDropletDriver_HealthCheck_Unhealthy(t *testing.T) {
	droplet := &godo.Droplet{
		ID:     42,
		Name:   "my-droplet",
		Status: "off",
		Size:   &godo.Size{Slug: "s-1vcpu-2gb"},
		Region: &godo.Region{Slug: "nyc3"},
	}
	mock := &mockDropletClient{droplet: droplet}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	result, err := d.HealthCheck(context.Background(), interfaces.ResourceRef{
		Name: "my-droplet", ProviderID: "42",
	})
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if result.Healthy {
		t.Errorf("expected unhealthy for droplet with status 'off'")
	}
}

func TestDropletDriver_Create_EmptyIDFromAPI(t *testing.T) {
	// API returns success but droplet has zero ID — guard must reject it.
	mock := &mockDropletClient{droplet: &godo.Droplet{Name: "my-droplet"}}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-droplet",
		Config: map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error for zero ProviderID, got nil")
	}
}

func TestDropletDriver_Create_ProviderIDIsAPIAssigned(t *testing.T) {
	// ProviderID must be the API-assigned numeric ID, not the resource name.
	mock := &mockDropletClient{droplet: testDroplet()}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	out, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name:   "my-droplet",
		Config: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ProviderID == "my-droplet" {
		t.Errorf("ProviderID must not equal spec.Name %q; got %q", "my-droplet", out.ProviderID)
	}
	if out.ProviderID == "" {
		t.Errorf("ProviderID must not be empty")
	}
}

// --- Extended-config tests (user_data / vpc_uuid / tags / bools / ssh_keys / volumes) ---

func TestDropletDriver_Create_PassesUserDataAndVPCAndBools(t *testing.T) {
	mock := &mockDropletClient{droplet: testDroplet()}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-droplet",
		Config: map[string]any{
			"user_data":      "#cloud-config\nruncmd:\n  - apt-get update\n",
			"vpc_uuid":       "00000000-0000-0000-0000-000000000001",
			"tags":           []any{"prod", "pg"},
			"enable_backups": true,
			"monitoring":     true,
			"ipv6":           true,
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := mock.gotReq
	if got == nil {
		t.Fatal("create request not captured")
	}
	if got.UserData == "" || got.UserData[:14] != "#cloud-config\n" {
		t.Errorf("UserData not propagated: %q", got.UserData)
	}
	if got.VPCUUID != "00000000-0000-0000-0000-000000000001" {
		t.Errorf("VPCUUID = %q", got.VPCUUID)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "prod" || got.Tags[1] != "pg" {
		t.Errorf("Tags = %v", got.Tags)
	}
	if !got.Backups || !got.Monitoring || !got.IPv6 {
		t.Errorf("bool flags not propagated: backups=%v monitoring=%v ipv6=%v",
			got.Backups, got.Monitoring, got.IPv6)
	}
}

func TestDropletDriver_Create_SSHKeys_Fingerprints(t *testing.T) {
	mock := &mockDropletClient{droplet: testDroplet()}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-droplet",
		Config: map[string]any{
			"ssh_keys": []any{"aa:bb:cc", "11:22:33"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := mock.gotReq.SSHKeys
	if len(got) != 2 || got[0].Fingerprint != "aa:bb:cc" || got[1].Fingerprint != "11:22:33" {
		t.Errorf("SSHKeys = %+v", got)
	}
}

func TestDropletDriver_Create_SSHKeys_NumericIDs(t *testing.T) {
	mock := &mockDropletClient{droplet: testDroplet()}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	// structpb collapses all numerics to float64; cover that shape.
	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-droplet",
		Config: map[string]any{
			"ssh_keys": []any{float64(101), float64(202)},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := mock.gotReq.SSHKeys
	if len(got) != 2 || got[0].ID != 101 || got[1].ID != 202 {
		t.Errorf("SSHKeys = %+v", got)
	}
	if got[0].Fingerprint != "" || got[1].Fingerprint != "" {
		t.Errorf("numeric ssh_keys must not populate Fingerprint: %+v", got)
	}
}

func TestDropletDriver_Create_SSHKeys_Mixed(t *testing.T) {
	mock := &mockDropletClient{droplet: testDroplet()}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-droplet",
		Config: map[string]any{
			"ssh_keys": []any{"aa:bb:cc", float64(7)},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := mock.gotReq.SSHKeys
	if len(got) != 2 {
		t.Fatalf("len(SSHKeys) = %d, want 2", len(got))
	}
	if got[0].Fingerprint != "aa:bb:cc" {
		t.Errorf("[0] = %+v, want fingerprint", got[0])
	}
	if got[1].ID != 7 {
		t.Errorf("[1] = %+v, want ID=7", got[1])
	}
}

func TestDropletDriver_Create_SSHKeys_TopLevelIntSlice(t *testing.T) {
	// Copilot finding #3: dropletSSHKeysFromConfig accepted []any and
	// []string at the top level but bailed on Go-native []int / []int64
	// even though the design admits int IDs. Cover both shapes.
	mock := &mockDropletClient{droplet: testDroplet()}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-droplet",
		Config: map[string]any{
			"ssh_keys": []int{101, 102},
		},
	})
	if err != nil {
		t.Fatalf("Create with []int ssh_keys: %v", err)
	}
	got := mock.gotReq.SSHKeys
	if len(got) != 2 || got[0].ID != 101 || got[1].ID != 102 {
		t.Errorf("SSHKeys = %+v, want [{ID:101} {ID:102}]", got)
	}
}

func TestDropletDriver_Create_SSHKeys_TopLevelInt64Slice(t *testing.T) {
	mock := &mockDropletClient{droplet: testDroplet()}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-droplet",
		Config: map[string]any{
			"ssh_keys": []int64{555, 666},
		},
	})
	if err != nil {
		t.Fatalf("Create with []int64 ssh_keys: %v", err)
	}
	got := mock.gotReq.SSHKeys
	if len(got) != 2 || got[0].ID != 555 || got[1].ID != 666 {
		t.Errorf("SSHKeys = %+v, want [{ID:555} {ID:666}]", got)
	}
}

func TestDropletDriver_Create_SSHKeys_TopLevelIntSlice_NonPositiveRejected(t *testing.T) {
	mock := &mockDropletClient{droplet: testDroplet()}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-droplet",
		Config: map[string]any{
			"ssh_keys": []int{0, 101},
		},
	})
	if err == nil {
		t.Fatal("expected error for non-positive ID in []int ssh_keys")
	}
}

func TestDropletDriver_Create_SSHKeys_FractionalRejected(t *testing.T) {
	mock := &mockDropletClient{droplet: testDroplet()}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-droplet",
		Config: map[string]any{
			"ssh_keys": []any{1.5},
		},
	})
	if err == nil {
		t.Fatal("expected error for fractional ssh_keys ID, got nil")
	}
}

func TestDropletDriver_Create_Volumes_ResolvedByName(t *testing.T) {
	mock := &mockDropletClient{droplet: testDroplet()}
	storage := &mockStorageClient{
		volumes: []godo.Volume{
			{ID: "vol-uuid-1", Name: "pg-data", Region: &godo.Region{Slug: "nyc3"}},
			{ID: "vol-uuid-2", Name: "pg-wal", Region: &godo.Region{Slug: "nyc3"}},
		},
	}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3", storage)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-droplet",
		Config: map[string]any{
			"volumes": []any{"pg-data", "pg-wal"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	vols := mock.gotReq.Volumes
	if len(vols) != 2 {
		t.Fatalf("len(volumes) = %d, want 2", len(vols))
	}
	if vols[0].ID != "vol-uuid-1" || vols[1].ID != "vol-uuid-2" {
		t.Errorf("volumes IDs = %+v, want [vol-uuid-1 vol-uuid-2]", vols)
	}
	// ListVolumes must filter by region so we don't accidentally attach a
	// volume from a different region (DO Block Storage is region-bound).
	if storage.gotListParams == nil || storage.gotListParams.Region != "nyc3" {
		t.Errorf("ListVolumes region = %v, want nyc3", storage.gotListParams)
	}
}

func TestDropletDriver_Create_Volumes_NotFound(t *testing.T) {
	mock := &mockDropletClient{droplet: testDroplet()}
	storage := &mockStorageClient{volumes: nil}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3", storage)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-droplet",
		Config: map[string]any{
			"volumes": []any{"missing-vol"},
		},
	})
	if err == nil {
		t.Fatal("expected error for unresolved volume name")
	}
	wantSubstr := `volume "missing-vol" not found`
	if !contains(err.Error(), wantSubstr) {
		t.Errorf("error %q missing %q", err.Error(), wantSubstr)
	}
}

func TestDropletDriver_Create_Volumes_NoStorageClient(t *testing.T) {
	// Test-side double-check: if a caller wires Droplet without a storage
	// client and then references volumes, fail loudly rather than silently
	// dropping the attachment.
	mock := &mockDropletClient{droplet: testDroplet()}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3") // no storage

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-droplet",
		Config: map[string]any{
			"volumes": []any{"pg-data"},
		},
	})
	if err == nil {
		t.Fatal("expected error when volumes set but no storage client")
	}
}

func TestDropletDriver_Diff_VPCChangeForcesReplace(t *testing.T) {
	// Copilot finding #6: extended droplet fields were wired into Create
	// but Diff only compared "size", so changes to vpc_uuid / tags /
	// volumes / enable_backups silently produced no plan action. Update
	// is disallowed by godo (Droplet PUT only resizes), so any tracked
	// drift must surface as ForceNew.
	mock := &mockDropletClient{}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"size":     "s-1vcpu-2gb",
			"vpc_uuid": "00000000-0000-0000-0000-000000000001",
		},
	}
	r, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"size":     "s-1vcpu-2gb",
			"vpc_uuid": "00000000-0000-0000-0000-000000000002",
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !r.NeedsReplace {
		t.Errorf("vpc_uuid change must force replace; NeedsReplace=%v", r.NeedsReplace)
	}
	var found bool
	for _, c := range r.Changes {
		if c.Path == "vpc_uuid" && c.ForceNew {
			found = true
		}
	}
	if !found {
		t.Errorf("expected FieldChange{Path:\"vpc_uuid\", ForceNew:true} in %+v", r.Changes)
	}
}

func TestDropletDriver_Diff_VPCAddFromEmptyForcesReplace(t *testing.T) {
	// Copilot round-2 finding #7: pre-release Droplet states won't have
	// vpc_uuid in Outputs (the field was added later). Adding vpc_uuid to
	// an already-managed Droplet planned no action because the curVPC != ""
	// guard skipped the change. Drop that guard so empty current vs
	// non-empty desired triggers ForceNew.
	mock := &mockDropletClient{}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"size": "s-1vcpu-2gb",
			// no vpc_uuid — represents pre-release state
		},
	}
	r, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"size":     "s-1vcpu-2gb",
			"vpc_uuid": "00000000-0000-0000-0000-000000000001",
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !r.NeedsReplace {
		t.Errorf("vpc_uuid add-from-empty must force replace; NeedsReplace=%v changes=%+v",
			r.NeedsReplace, r.Changes)
	}
}

func TestDropletDriver_Diff_VPCAbsentSkipped(t *testing.T) {
	// Inverse: when vpc_uuid is absent from desired, current vpc_uuid must
	// NOT be cleared as drift. Backwards-compat for YAML predating the field.
	mock := &mockDropletClient{}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"size":     "s-1vcpu-2gb",
			"vpc_uuid": "00000000-0000-0000-0000-000000000001",
		},
	}
	r, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{"size": "s-1vcpu-2gb"},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if r.NeedsReplace || r.NeedsUpdate {
		t.Errorf("absent vpc_uuid must NOT trigger drift; NeedsReplace=%v changes=%+v",
			r.NeedsReplace, r.Changes)
	}
}

func TestDropletDriver_Diff_TagsChangeForcesReplace(t *testing.T) {
	mock := &mockDropletClient{}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"size": "s-1vcpu-2gb",
			"tags": []any{"prod"},
		},
	}
	r, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"size": "s-1vcpu-2gb",
			"tags": []any{"prod", "audit"},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !r.NeedsReplace {
		t.Errorf("tags change must force replace")
	}
}

func TestDropletDriver_Diff_TagsClearForcesReplace(t *testing.T) {
	// Copilot round-2 finding #3: clearing tags (non-empty current → empty
	// desired) was silently ignored because the Diff path skipped when
	// len(desired.tags)==0. Drop the empty-side guard so a desired:[] with
	// non-empty current surfaces as ForceNew. Operators sometimes need to
	// strip tags to remove a Droplet from a tag-based firewall or backup
	// schedule; "no diff" is dangerously wrong here.
	mock := &mockDropletClient{}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"size": "s-1vcpu-2gb",
			"tags": []any{"prod", "pg"},
		},
	}
	r, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"size": "s-1vcpu-2gb",
			"tags": []any{}, // explicit empty
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !r.NeedsReplace {
		t.Errorf("clearing tags must force replace; NeedsReplace=%v", r.NeedsReplace)
	}
}

func TestDropletDriver_Diff_TagsAbsentSkipped(t *testing.T) {
	// Inverse of the clear test: when "tags" key is absent from desired
	// (operator hasn't said anything about tags), Diff must NOT plan a
	// change just because current has tags. That would force-recreate any
	// Droplet whose YAML predates the tags field.
	mock := &mockDropletClient{}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"size": "s-1vcpu-2gb",
			"tags": []any{"prod"},
		},
	}
	r, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"size": "s-1vcpu-2gb",
			// no "tags" key
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if r.NeedsReplace || r.NeedsUpdate {
		t.Errorf("absent tags key must NOT trigger drift; got NeedsReplace=%v changes=%+v",
			r.NeedsReplace, r.Changes)
	}
}

func TestDropletDriver_Diff_TagsReorderNoReplace(t *testing.T) {
	mock := &mockDropletClient{}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"size": "s-1vcpu-2gb",
			"tags": []any{"prod", "pg"},
		},
	}
	r, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"size": "s-1vcpu-2gb",
			"tags": []any{"pg", "prod"},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if r.NeedsReplace {
		t.Errorf("reordered-but-equal tags must NOT force replace")
	}
}

func TestDropletDriver_Diff_BackupsToggleForcesReplace(t *testing.T) {
	mock := &mockDropletClient{}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"size":           "s-1vcpu-2gb",
			"enable_backups": false,
		},
	}
	r, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"size":           "s-1vcpu-2gb",
			"enable_backups": true,
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !r.NeedsReplace {
		t.Errorf("enable_backups toggle must force replace")
	}
}

// mockStorageClientByID returns volumes from a map keyed by ID, so GetVolume
// can resolve UUIDs back to names. Used by Read-side tests for finding #1.
type mockStorageClientByID struct {
	mockStorageClient
	byID map[string]godo.Volume
	// getErrIDs is the set of volume IDs for which GetVolume returns err
	// (simulating an out-of-band volume deletion).
	getErrIDs map[string]bool
	// getCalls records the order of GetVolume invocations so tests can
	// assert caching (no duplicate lookups for the same ID).
	getCalls []string
}

func (m *mockStorageClientByID) GetVolume(_ context.Context, id string) (*godo.Volume, *godo.Response, error) {
	m.getCalls = append(m.getCalls, id)
	if m.getErrIDs[id] {
		return nil, nil, fmt.Errorf("volume %s: not found (404)", id)
	}
	if v, ok := m.byID[id]; ok {
		return &v, nil, nil
	}
	return nil, nil, fmt.Errorf("volume %s: missing from mock", id)
}

func TestDropletDriver_Read_VolumesResolvedToNames(t *testing.T) {
	// Copilot round-2 finding #1: previously dropletOutput stored godo's
	// raw VolumeIDs (UUIDs) as Outputs["volumes"], while desired config
	// carries volume *names*. After every successful Create, the next plan
	// would compare ["vol-uuid-1"] against ["pg-data"] and force-replace
	// the Droplet — a deploy-time loss of all PG state.
	//
	// dropletOutput must call Storage.GetVolume to resolve each ID to its
	// name so Diff comparisons line up.
	droplet := testDroplet()
	droplet.VolumeIDs = []string{"vol-uuid-1", "vol-uuid-2"}
	mock := &mockDropletClient{droplet: droplet}
	storage := &mockStorageClientByID{
		byID: map[string]godo.Volume{
			"vol-uuid-1": {ID: "vol-uuid-1", Name: "pg-data"},
			"vol-uuid-2": {ID: "vol-uuid-2", Name: "pg-wal"},
		},
	}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3", storage)

	out, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name: "my-droplet", ProviderID: "42",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	vols, ok := out.Outputs["volumes"].([]any)
	if !ok {
		t.Fatalf("Outputs[volumes] = %T, want []any", out.Outputs["volumes"])
	}
	if len(vols) != 2 {
		t.Fatalf("len(volumes) = %d, want 2", len(vols))
	}
	if vols[0] != "pg-data" || vols[1] != "pg-wal" {
		t.Errorf("volumes = %v, want [pg-data pg-wal] (names, not UUIDs)", vols)
	}
}

func TestDropletDriver_Diff_VolumesNoReplaceWhenNamesMatch(t *testing.T) {
	// End-to-end check: with the current Outputs populated by
	// dropletOutput's name-resolution path AND the desired config carrying
	// the same names, Diff must NOT plan a replace. This is the regression
	// guarding against the "destroy PG Droplet on every deploy" bug.
	mock := &mockDropletClient{}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"size":    "s-1vcpu-2gb",
			"volumes": []any{"pg-data", "pg-wal"},
		},
	}
	r, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"size":    "s-1vcpu-2gb",
			"volumes": []any{"pg-data", "pg-wal"},
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if r.NeedsUpdate || r.NeedsReplace {
		t.Errorf("matching volume names must NOT trigger a diff; got NeedsUpdate=%v NeedsReplace=%v changes=%+v",
			r.NeedsUpdate, r.NeedsReplace, r.Changes)
	}
}

func TestDropletDriver_Diff_VolumesReplaceWhenDesiredMissingName(t *testing.T) {
	// Removing a volume from the desired config (or adding one) is a real
	// drift and must surface as ForceNew.
	mock := &mockDropletClient{}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"size":    "s-1vcpu-2gb",
			"volumes": []any{"pg-data", "pg-wal"},
		},
	}
	r, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"size":    "s-1vcpu-2gb",
			"volumes": []any{"pg-data"}, // pg-wal removed
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !r.NeedsReplace {
		t.Errorf("removing a volume from desired must force replace; got NeedsReplace=%v", r.NeedsReplace)
	}
}

func TestDropletDriver_Read_VolumeResolutionFailureFallsBackToID(t *testing.T) {
	// If GetVolume errors (e.g. the volume was deleted out-of-band) we
	// must surface the raw ID rather than dropping the entry, AND we must
	// record the failure in Outputs["volumes_resolution"] so operators
	// can debug.
	droplet := testDroplet()
	droplet.VolumeIDs = []string{"vol-uuid-deleted"}
	mock := &mockDropletClient{droplet: droplet}
	storage := &mockStorageClientByID{
		byID:      map[string]godo.Volume{},
		getErrIDs: map[string]bool{"vol-uuid-deleted": true},
	}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3", storage)

	out, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name: "my-droplet", ProviderID: "42",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	vols, _ := out.Outputs["volumes"].([]any)
	if len(vols) != 1 || vols[0] != "vol-uuid-deleted" {
		t.Errorf("volumes = %v, want [vol-uuid-deleted] (ID fallback)", vols)
	}
	resolution, ok := out.Outputs["volumes_resolution"].(map[string]any)
	if !ok {
		t.Fatalf("expected volumes_resolution in Outputs, got %T", out.Outputs["volumes_resolution"])
	}
	unresolved, _ := resolution["unresolved_ids"].([]any)
	if len(unresolved) != 1 || unresolved[0] != "vol-uuid-deleted" {
		t.Errorf("unresolved_ids = %v, want [vol-uuid-deleted]", unresolved)
	}
}

func TestDropletDriver_Read_VolumeResolutionCachesPerCall(t *testing.T) {
	// If a Droplet somehow lists the same VolumeID twice, dropletOutput
	// must only call GetVolume once for that ID.
	droplet := testDroplet()
	droplet.VolumeIDs = []string{"vol-uuid-1", "vol-uuid-1"}
	mock := &mockDropletClient{droplet: droplet}
	storage := &mockStorageClientByID{
		byID: map[string]godo.Volume{
			"vol-uuid-1": {ID: "vol-uuid-1", Name: "pg-data"},
		},
	}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3", storage)

	_, err := d.Read(context.Background(), interfaces.ResourceRef{
		Name: "my-droplet", ProviderID: "42",
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(storage.getCalls) != 1 {
		t.Errorf("GetVolume called %d times for duplicate ID; want 1 (cached): %v",
			len(storage.getCalls), storage.getCalls)
	}
}

func TestDropletDriver_Create_Volumes_NonStringEntryRejected(t *testing.T) {
	// Copilot finding #1: strSliceFromConfig silently drops non-string
	// entries. For volume attachments that risks leaving the Droplet
	// running without an expected disk. dropletVolumesFromConfig must
	// reject non-string entries with an explicit error.
	mock := &mockDropletClient{droplet: testDroplet()}
	storage := &mockStorageClient{
		volumes: []godo.Volume{{ID: "vol-uuid-2", Name: "data", Region: &godo.Region{Slug: "nyc3"}}},
	}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3", storage)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-droplet",
		Config: map[string]any{
			"volumes": []any{123, "data"},
		},
	})
	if err == nil {
		t.Fatal("expected error rejecting non-string volumes entry")
	}
	wantSubstr := `droplet volumes: invalid entry at index 0: expected non-empty string, got int`
	if !contains(err.Error(), wantSubstr) {
		t.Errorf("error %q missing %q", err.Error(), wantSubstr)
	}
}

func TestDropletDriver_Create_Volumes_EmptyStringRejected(t *testing.T) {
	mock := &mockDropletClient{droplet: testDroplet()}
	storage := &mockStorageClient{volumes: nil}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3", storage)

	_, err := d.Create(context.Background(), interfaces.ResourceSpec{
		Name: "my-droplet",
		Config: map[string]any{
			"volumes": []any{""},
		},
	})
	if err == nil {
		t.Fatal("expected error rejecting empty volumes entry")
	}
	if !contains(err.Error(), "expected non-empty string, got empty string") {
		t.Errorf("error %q does not flag empty string", err.Error())
	}
}

// TestDropletDriver_Diff_RegionChangeForcesReplace covers issue #70:
// Droplets are regional and DO does not support region change in Update
// (godo Droplet PUT only resizes). Region drift must surface as ForceNew
// so cascade dependents (Volume mount, Firewall tags) get correctly
// re-coordinated against the replaced Droplet.
func TestDropletDriver_Diff_RegionChangeForcesReplace(t *testing.T) {
	mock := &mockDropletClient{}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"size":   "s-1vcpu-2gb",
			"region": "nyc3",
		},
	}
	r, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"size":   "s-1vcpu-2gb",
			"region": "nyc1",
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !r.NeedsReplace {
		t.Fatal("region change must force replace; NeedsReplace=false")
	}
	var found bool
	for _, c := range r.Changes {
		if c.Path == "region" && c.Old == "nyc3" && c.New == "nyc1" && c.ForceNew {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected FieldChange{Path:region, Old:nyc3, New:nyc1, ForceNew:true}; got %+v", r.Changes)
	}
}

// TestDropletDriver_Diff_RegionEmptyCurrentSkipped covers the upgrade-safe
// guard: Droplets in state from earlier plugin versions (when dropletOutput
// didn't include region) must not false-positive on the first plan after
// upgrade — they'll Read on next apply to populate the field.
func TestDropletDriver_Diff_RegionEmptyCurrentSkipped(t *testing.T) {
	mock := &mockDropletClient{}
	d := drivers.NewDropletDriverWithClient(mock, "nyc3")

	current := &interfaces.ResourceOutput{
		Outputs: map[string]any{
			"size": "s-1vcpu-2gb",
			// no region — represents pre-region-output state
		},
	}
	r, err := d.Diff(context.Background(), interfaces.ResourceSpec{
		Config: map[string]any{
			"size":   "s-1vcpu-2gb",
			"region": "nyc1",
		},
	}, current)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if r.NeedsReplace {
		t.Error("empty curRegion should not force replace; got NeedsReplace=true")
	}
	for _, c := range r.Changes {
		if c.Path == "region" {
			t.Errorf("empty curRegion should not emit a region change; got %+v", c)
		}
	}
}

func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
