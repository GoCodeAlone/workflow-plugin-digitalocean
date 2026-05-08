package drivers_test

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

func TestDropletDriver_Replace_DetachesVolumesBeforeDelete(t *testing.T) {
	log := &callLog{}
	droplets := newRecordingDropletsClient().withCallLog(log)
	droplets.getResponse = &godo.Droplet{
		ID: 100, VolumeIDs: []string{"vol-a", "vol-b"},
		Size:   &godo.Size{Slug: "s-1vcpu-2gb"},
		Region: &godo.Region{Slug: "nyc1"},
		Status: "active",
	}
	sa := newRecordingStorageActionsClient().withCallLog(log)
	actions := newRecordingActionsClient("completed", "completed").withCallLog(log)

	d := drivers.NewDropletDriverWithClient(droplets, "nyc1",
		drivers.StorageClient(newRecordingStorageClient()),
		drivers.StorageActionsClient(sa),
		drivers.ActionsClient(actions),
	)

	spec := interfaces.ResourceSpec{Name: "pg", Config: map[string]any{"size": "s-1vcpu-2gb"}}
	oldRef := interfaces.ResourceRef{Name: "pg", ProviderID: "100"}

	out, err := d.Replace(context.Background(), oldRef, spec)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if len(sa.detachCalls) != 2 {
		t.Errorf("expected 2 detach calls, got %d", len(sa.detachCalls))
	}
	// Exact-ordering assertion via cross-fake call log. Replaces the prior
	// boolean-based check that couldn't distinguish Create-before-Delete from
	// Delete-before-Create — the load-bearing 422-fix invariant.
	want := []string{
		"Droplets.Get",
		"StorageActions.DetachByDropletID",
		"Actions.Get",
		"StorageActions.DetachByDropletID",
		"Actions.Get",
		"Droplets.Delete",
		"Droplets.Create",
	}
	if !reflect.DeepEqual(log.events, want) {
		t.Errorf("call sequence wrong:\n  got:  %v\n  want: %v", log.events, want)
	}
	if out.ProviderID == "" {
		t.Error("expected new ProviderID")
	}
}

func TestDropletDriver_Replace_OldNotFound_SkipsDetachAndDelete(t *testing.T) {
	droplets := newRecordingDropletsClient()
	// Simulate a 404 from godo via *godo.ErrorResponse so WrapGodoError maps
	// it to interfaces.ErrResourceNotFound and Replace treats it as "orphan state"
	// (old Droplet already gone) — skip detach + delete, proceed to Create.
	droplets.getErr = &godo.ErrorResponse{
		Response: &http.Response{StatusCode: 404},
		Message:  "droplet not found",
	}
	sa := newRecordingStorageActionsClient()
	actions := newRecordingActionsClient()

	d := drivers.NewDropletDriverWithClient(droplets, "nyc1",
		drivers.StorageActionsClient(sa), drivers.ActionsClient(actions),
	)

	spec := interfaces.ResourceSpec{Name: "pg"}
	oldRef := interfaces.ResourceRef{Name: "pg", ProviderID: "100"}

	out, err := d.Replace(context.Background(), oldRef, spec)
	if err != nil {
		t.Fatalf("Replace on 404-old: %v", err)
	}
	if len(sa.detachCalls) != 0 {
		t.Error("detach should NOT fire on 404 old droplet")
	}
	if droplets.deleteCalled {
		t.Error("delete should NOT fire on 404 old droplet")
	}
	if !droplets.createCalled {
		t.Error("create should still fire (recovery path)")
	}
	if out == nil {
		t.Error("expected non-nil output")
	}
}

func TestDropletDriver_Replace_NoVolumes_NoDetachCall(t *testing.T) {
	droplets := newRecordingDropletsClient()
	droplets.getResponse = &godo.Droplet{
		ID: 100, VolumeIDs: []string{},
		Size:   &godo.Size{Slug: "s-1vcpu-2gb"},
		Region: &godo.Region{Slug: "nyc1"},
		Status: "active",
	}
	sa := newRecordingStorageActionsClient()
	actions := newRecordingActionsClient()

	d := drivers.NewDropletDriverWithClient(droplets, "nyc1",
		drivers.StorageActionsClient(sa), drivers.ActionsClient(actions),
	)

	spec := interfaces.ResourceSpec{Name: "pg"}
	oldRef := interfaces.ResourceRef{Name: "pg", ProviderID: "100"}

	_, err := d.Replace(context.Background(), oldRef, spec)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if len(sa.detachCalls) != 0 {
		t.Errorf("expected zero detach calls; got %d", len(sa.detachCalls))
	}
	if !droplets.deleteCalled || !droplets.createCalled {
		t.Error("expected Delete + Create on no-volumes path")
	}
}

func TestDropletDriver_Replace_DetachWaitTimeout_PreservesRecoverable(t *testing.T) {
	droplets := newRecordingDropletsClient()
	droplets.getResponse = &godo.Droplet{
		ID: 100, VolumeIDs: []string{"vol-a"},
		Size:   &godo.Size{Slug: "s-1vcpu-2gb"},
		Region: &godo.Region{Slug: "nyc1"},
		Status: "active",
	}
	sa := newRecordingStorageActionsClient()
	// Use withDefaultStatus("in-progress") so the client keeps returning
	// "in-progress" after the sequence runs out, allowing the timeout to fire.
	actions := newRecordingActionsClient().withDefaultStatus("in-progress")

	d := drivers.NewDropletDriverWithClient(droplets, "nyc1",
		drivers.StorageActionsClient(sa), drivers.ActionsClient(actions),
	)
	// Override to short bounds so the timeout fires quickly.
	d.SetReplaceTimeoutsForTest(100*time.Millisecond, 30*time.Millisecond)

	spec := interfaces.ResourceSpec{Name: "pg"}
	oldRef := interfaces.ResourceRef{Name: "pg", ProviderID: "100"}

	_, err := d.Replace(context.Background(), oldRef, spec)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "wait detach action") {
		t.Errorf("error not wrapped with 'wait detach action': %v", err)
	}
	if droplets.deleteCalled || droplets.createCalled {
		t.Error("delete/create must NOT fire on detach timeout (state preservation)")
	}
}

func TestDropletDriver_Replace_NoStorageActions_Errors(t *testing.T) {
	droplets := newRecordingDropletsClient()
	droplets.getResponse = &godo.Droplet{
		ID: 100, VolumeIDs: []string{"vol-a"},
		Size:   &godo.Size{Slug: "s-1vcpu-2gb"},
		Region: &godo.Region{Slug: "nyc1"},
		Status: "active",
	}

	d := drivers.NewDropletDriverWithClient(droplets, "nyc1") // no StorageActions

	spec := interfaces.ResourceSpec{Name: "pg"}
	oldRef := interfaces.ResourceRef{Name: "pg", ProviderID: "100"}

	_, err := d.Replace(context.Background(), oldRef, spec)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "storage_actions client not configured") {
		t.Errorf("error not wrapped with 'storage_actions': %v", err)
	}
}

func TestDropletDriver_Replace_BypassesNameResolution(t *testing.T) {
	// Set up two volumes with the same name in the region; default
	// Create's resolveDropletVolumes would pick one. Replace MUST
	// pick the volume previously attached to the old droplet.
	droplets := newRecordingDropletsClient()
	droplets.getResponse = &godo.Droplet{
		ID: 100, VolumeIDs: []string{"vol-PREVIOUSLY-ATTACHED-id"},
		Size:   &godo.Size{Slug: "s-1vcpu-2gb"},
		Region: &godo.Region{Slug: "nyc1"},
		Status: "active",
	}
	sa := newRecordingStorageActionsClient()
	actions := newRecordingActionsClient("completed")
	storage := newRecordingStorageClient()
	// Same-name pollution: ListVolumes by name returns BOTH volumes.
	storage.listResponse = []godo.Volume{
		{ID: "vol-OTHER-id", Name: "pg-data"},           // alphabetically first
		{ID: "vol-PREVIOUSLY-ATTACHED-id", Name: "pg-data"},
	}

	d := drivers.NewDropletDriverWithClient(droplets, "nyc1",
		drivers.StorageClient(storage),
		drivers.StorageActionsClient(sa),
		drivers.ActionsClient(actions),
	)

	spec := interfaces.ResourceSpec{
		Name:   "pg",
		Config: map[string]any{"volumes": []any{"pg-data"}},
	}
	oldRef := interfaces.ResourceRef{Name: "pg", ProviderID: "100"}

	_, err := d.Replace(context.Background(), oldRef, spec)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	// Replace bypasses name resolution → ListVolumes must NOT be called.
	if storage.listCalls != 0 {
		t.Errorf("storage.ListVolumes called %d times; want 0 (Replace must bypass name lookup)", storage.listCalls)
	}
	// Inspect droplets.createReq to assert the create request used
	// vol-PREVIOUSLY-ATTACHED-id, NOT the alphabetically-first vol-OTHER-id.
	if droplets.createReq == nil {
		t.Fatal("createReq not recorded")
	}
	if len(droplets.createReq.Volumes) != 1 {
		t.Fatalf("Volumes len = %d, want 1", len(droplets.createReq.Volumes))
	}
	if got := droplets.createReq.Volumes[0].ID; got != "vol-PREVIOUSLY-ATTACHED-id" {
		t.Errorf("Replace.Create used wrong volume ID: got %q, want vol-PREVIOUSLY-ATTACHED-id", got)
	}
}
