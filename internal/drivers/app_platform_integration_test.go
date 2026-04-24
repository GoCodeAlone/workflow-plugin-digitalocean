package drivers

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"

	"github.com/GoCodeAlone/workflow/interfaces"
	"github.com/digitalocean/godo"
)

// bmwSpec is a minimal valid ResourceSpec that mirrors BMW staging config.
func bmwSpec(name string) interfaces.ResourceSpec {
	return interfaces.ResourceSpec{
		Name: name,
		Type: "infra.app_platform",
		Config: map[string]any{
			"region": "nyc3",
			"image":  "docker.io/myorg/bmw:latest",
		},
	}
}

// TestAppPlatform_Create_PersistsUUIDInState asserts that Create returns a
// ResourceOutput with the UUID from the API response (never the name), and
// that the resulting ResourceState stores the UUID — not the spec name.
func TestAppPlatform_Create_PersistsUUIDInState(t *testing.T) {
	driver, client := newTestDriver(t)
	const wantUUID = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	client.createResp = &godo.App{
		ID:   wantUUID,
		Spec: &godo.AppSpec{Name: "bmw-staging"},
	}
	state := newInMemoryState()

	rs := applySim(t, context.Background(), driver, state, bmwSpec("bmw-staging"))

	if rs.ProviderID != wantUUID {
		t.Errorf("state.ProviderID = %q, want UUID %q", rs.ProviderID, wantUUID)
	}
	if rs.ProviderID == "bmw-staging" {
		t.Errorf("state.ProviderID is the spec NAME — regression of the v0.7.8 bug class")
	}
}

// TestAppPlatform_Update_UsesExistingUUID asserts that when state already
// has a valid UUID, Update uses it directly without invoking findAppByName.
func TestAppPlatform_Update_UsesExistingUUID(t *testing.T) {
	driver, client := newTestDriver(t)
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	existing := &godo.App{ID: uuid, Spec: &godo.AppSpec{Name: "bmw-staging"}}
	client.getByID[uuid] = existing
	client.updateResp[uuid] = existing

	state := newInMemoryState()
	state.put(interfaces.ResourceState{
		Name: "bmw-staging", Type: "infra.app_platform", ProviderID: uuid,
	})

	rs := applySim(t, context.Background(), driver, state, bmwSpec("bmw-staging"))

	// Update called with UUID exactly once.
	if len(client.updateCalls) != 1 || client.updateCalls[0] != uuid {
		t.Errorf("updateCalls = %v, want exactly [%q]", client.updateCalls, uuid)
	}
	// List (used by findAppByName) must never be called.
	if client.listCalls != 0 {
		t.Errorf("listCalls = %d, want 0 (heal must not fire for valid UUID)", client.listCalls)
	}
	if rs.ProviderID != uuid {
		t.Errorf("state.ProviderID = %q, want %q", rs.ProviderID, uuid)
	}
}

// TestAppPlatform_Update_HealsStaleName is the core regression test.
// It seeds state with ProviderID="bmw-staging" (the stale shape that caused
// BMW deploy 24901939350 to fail), calls applySim, and asserts:
//   - findAppByName was invoked (listCalls ≥ 1)
//   - Update API call used the UUID, not the name
//   - Returned ResourceState has the healed UUID
//   - WARN log was emitted containing "state-heal"
func TestAppPlatform_Update_HealsStaleName(t *testing.T) {
	driver, client := newTestDriver(t)
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	const name = "bmw-staging"

	client.getByID[uuid] = &godo.App{ID: uuid, Spec: &godo.AppSpec{Name: name}}
	client.updateResp[uuid] = &godo.App{ID: uuid, Spec: &godo.AppSpec{Name: name}}

	state := newInMemoryState()
	state.put(interfaces.ResourceState{
		Name:       name,
		Type:       "infra.app_platform",
		ProviderID: name, // ← STALE: name substituted for UUID
	})

	// Capture WARN log.
	var logBuf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(oldOut)

	rs := applySim(t, context.Background(), driver, state, bmwSpec(name))

	// findAppByName must have been called.
	if client.listCalls < 1 {
		t.Errorf("listCalls = %d, want ≥ 1 (findAppByName must fire during heal)", client.listCalls)
	}
	// Update must use the real UUID, not the stale name.
	if len(client.updateCalls) != 1 || client.updateCalls[0] != uuid {
		t.Errorf("updateCalls = %v, want exactly [%q]", client.updateCalls, uuid)
	}
	// State is rewritten with the healed UUID.
	if rs.ProviderID != uuid {
		t.Errorf("state.ProviderID = %q after heal, want %q", rs.ProviderID, uuid)
	}
	// WARN log captured.
	if !strings.Contains(logBuf.String(), "state-heal") {
		t.Errorf("expected WARN log containing 'state-heal', got: %q", logBuf.String())
	}
}

// TestAppPlatform_Delete_HealsStaleName asserts the same heal on the Delete path.
func TestAppPlatform_Delete_HealsStaleName(t *testing.T) {
	driver, client := newTestDriver(t)
	const uuid = "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5"
	const name = "bmw-staging"

	client.getByID[uuid] = &godo.App{ID: uuid, Spec: &godo.AppSpec{Name: name}}

	err := driver.Delete(context.Background(), interfaces.ResourceRef{
		Name: name, Type: "infra.app_platform", ProviderID: name,
	})
	if err != nil {
		t.Fatalf("Delete with stale name: %v", err)
	}
	if len(client.deleteCalls) != 1 || client.deleteCalls[0] != uuid {
		t.Errorf("deleteCalls = %v, want exactly [%q]", client.deleteCalls, uuid)
	}
	if client.listCalls < 1 {
		t.Errorf("listCalls = %d, want ≥ 1 (findAppByName must fire during heal)", client.listCalls)
	}
}

// TestAppPlatform_Update_HealFails_WhenAppNotFound asserts that a clear error
// is returned when the stale name can't be resolved — never sending the name
// as a path parameter to the DO API.
func TestAppPlatform_Update_HealFails_WhenAppNotFound(t *testing.T) {
	driver, _ := newTestDriver(t)

	_, err := driver.Update(context.Background(),
		interfaces.ResourceRef{Name: "ghost-app", Type: "infra.app_platform", ProviderID: "ghost-app"},
		bmwSpec("ghost-app"),
	)
	if err == nil {
		t.Fatal("expected error when heal can't find app by name, got nil")
	}
	if !strings.Contains(err.Error(), "state-heal") {
		t.Errorf("expected error mentioning 'state-heal', got: %v", err)
	}
}
