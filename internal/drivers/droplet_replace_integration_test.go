//go:build integration
// +build integration

package drivers_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal/drivers"
	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// TestReplaceTC2ScenarioReplay replays the TC2 orphan-Droplet scenario using
// a sequenced godo mock matching the existing DO plugin test pattern.
//
// Sequence:
//  1. Get(100)                      → {ID:100, VolumeIDs:["vol-a"]}
//  2. DetachByDropletID(vol-a, 100) → action ID 1, status in-progress
//  3. Actions.Get(1)                → "completed"
//  4. Delete(100)                   → success
//  5. Create(req)                   → {ID:200}
//
// Asserts:
// - Exact ordering via shared callLog (cross-fake): the load-bearing 422-fix
//   invariant. A regression that swapped Delete↔Create or skipped detach
//   shows up as a different events slice.
// - New Droplet gets ProviderID "200".
// - DetachByDropletID args are correct (volume + droplet IDs).
func TestReplaceTC2ScenarioReplay(t *testing.T) {
	log := &callLog{}
	droplets := newRecordingDropletsClient().withCallLog(log)
	droplets.getResponse = &godo.Droplet{
		ID:        100,
		Name:      "coredump-staging-pg",
		Status:    "active",
		VolumeIDs: []string{"vol-a"},
		Size:      &godo.Size{Slug: "s-1vcpu-2gb"},
		Region:    &godo.Region{Slug: "nyc1"},
	}
	sa := newRecordingStorageActionsClient().withCallLog(log)
	actions := newRecordingActionsClient("completed").withCallLog(log)

	d := drivers.NewDropletDriverWithClient(droplets, "nyc1",
		drivers.StorageActionsClient(sa),
		drivers.ActionsClient(actions),
	)

	spec := interfaces.ResourceSpec{
		Name: "coredump-staging-pg",
		Config: map[string]any{
			"size":   "s-1vcpu-2gb",
			"region": "nyc1",
		},
	}
	oldRef := interfaces.ResourceRef{Name: "coredump-staging-pg", ProviderID: "100"}

	out, err := d.Replace(context.Background(), oldRef, spec)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if out.ProviderID != "200" {
		t.Errorf("expected new ProviderID=200, got %q", out.ProviderID)
	}

	// Exact-ordering assertion — strongest defense against the 422 regression.
	// If a future change swaps Delete↔Create or skips DetachByDropletID, this
	// slice differs and the test fails. Per-method booleans alone cannot
	// detect Create-before-Delete.
	want := []string{
		"Droplets.Get",
		"StorageActions.DetachByDropletID",
		"Actions.Get",
		"Droplets.Delete",
		"Droplets.Create",
	}
	if !reflect.DeepEqual(log.events, want) {
		t.Errorf("call sequence wrong:\n  got:  %v\n  want: %v", log.events, want)
	}

	// Detach call args (the ordering log doesn't carry per-call args).
	if len(sa.detachCalls) != 1 {
		t.Errorf("DetachByDropletID: got %d calls, want 1", len(sa.detachCalls))
	}
	if sa.detachCalls[0].VolumeID != "vol-a" {
		t.Errorf("DetachByDropletID: VolumeID=%q, want vol-a", sa.detachCalls[0].VolumeID)
	}
	if sa.detachCalls[0].DropletID != 100 {
		t.Errorf("DetachByDropletID: DropletID=%d, want 100", sa.detachCalls[0].DropletID)
	}
}
