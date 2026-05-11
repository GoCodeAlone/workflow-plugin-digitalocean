package drivers_test

import (
	"context"
	"fmt"

	"github.com/digitalocean/godo"
)

// callLog is a shared ordered log of method calls across all recording fakes.
// Tests assert the cross-fake call sequence (Get → Detach → Wait → Delete →
// Create) — the load-bearing invariant for avoiding the 422 failure class.
// Per-fake booleans are kept for backwards-compat with tests that don't need
// ordering.
type callLog struct {
	events []string
}

func (l *callLog) record(s string) {
	if l == nil {
		return
	}
	l.events = append(l.events, s)
}

// recordingDropletsClient records every method call and lets tests
// program per-method responses. Replaces mockDropletClient for tests
// that need ordering / request-inspection guarantees.
type recordingDropletsClient struct {
	// Programmable responses
	getResponse *godo.Droplet
	getErr      error
	createResp  *godo.Droplet
	createErr   error
	deleteErr   error

	// Call recording
	getCalled    bool
	deleteCalled bool
	createCalled bool
	createReq    *godo.DropletCreateRequest // most-recent create request (nil before first call)

	// log: optional shared call log (set via withCallLog) for cross-fake
	// ordering assertions. nil-safe via callLog.record's nil-receiver check.
	log *callLog
}

func (m *recordingDropletsClient) withCallLog(log *callLog) *recordingDropletsClient {
	m.log = log
	return m
}

func (m *recordingDropletsClient) Get(_ context.Context, _ int) (*godo.Droplet, *godo.Response, error) {
	m.getCalled = true
	m.log.record("Droplets.Get")
	return m.getResponse, nil, m.getErr
}
func (m *recordingDropletsClient) Create(_ context.Context, req *godo.DropletCreateRequest) (*godo.Droplet, *godo.Response, error) {
	m.createCalled = true
	m.createReq = req
	m.log.record("Droplets.Create")
	return m.createResp, nil, m.createErr
}
func (m *recordingDropletsClient) Delete(_ context.Context, _ int) (*godo.Response, error) {
	m.deleteCalled = true
	m.log.record("Droplets.Delete")
	return nil, m.deleteErr
}

// newRecordingDropletsClient defaults to: Get returning nil (caller programs
// explicitly); Create returning a nominal new Droplet; Delete success.
func newRecordingDropletsClient() *recordingDropletsClient {
	return &recordingDropletsClient{
		createResp: &godo.Droplet{
			ID:     200,
			Name:   "new-droplet",
			Status: "active",
			Size:   &godo.Size{Slug: "s-1vcpu-2gb"},
			Region: &godo.Region{Slug: "nyc1"},
			// Networks populated so dropletReady() short-circuits the
			// post-Create wait poll. Without this, the wait loop hits
			// recordingDropletsClient.Get (returning the OLD droplet's
			// getResponse, which lacks Networks) and times out after
			// the production 5-minute deadline.
			Networks: &godo.Networks{
				V4: []godo.NetworkV4{
					{IPAddress: "10.0.0.200", Type: "private"},
				},
			},
		},
	}
}

// recordingStorageClient: only ListVolumes and GetVolume are exercised by
// Replace's supporting paths. ListVolumes filtering is not needed by Replace
// (it bypasses name lookup via resolved IDs), but GetVolume is used by
// dropletOutput to resolve VolumeIDs to names.
type recordingStorageClient struct {
	listResponse []godo.Volume
	listErr      error
	listCalls    int
}

func (m *recordingStorageClient) CreateVolume(_ context.Context, _ *godo.VolumeCreateRequest) (*godo.Volume, *godo.Response, error) {
	return nil, nil, fmt.Errorf("not implemented in this fake")
}
func (m *recordingStorageClient) GetVolume(_ context.Context, id string) (*godo.Volume, *godo.Response, error) {
	for _, v := range m.listResponse {
		if v.ID == id {
			return &v, nil, nil
		}
	}
	return nil, nil, fmt.Errorf("volume %q not in fake", id)
}
func (m *recordingStorageClient) DeleteVolume(_ context.Context, _ string) (*godo.Response, error) {
	return nil, nil
}
func (m *recordingStorageClient) ListVolumes(_ context.Context, _ *godo.ListVolumeParams) ([]godo.Volume, *godo.Response, error) {
	m.listCalls++
	return m.listResponse, nil, m.listErr
}

func newRecordingStorageClient() *recordingStorageClient {
	return &recordingStorageClient{}
}

// recordingStorageActionsClient records DetachByDropletID calls
// (volume + droplet IDs in invocation order). Returns programmable
// action ID (incrementing) so subsequent Actions.Get can match.
type recordingStorageActionsClient struct {
	detachCalls  []struct{ VolumeID string; DropletID int }
	detachErr    error
	nextActionID int

	log *callLog
}

func (m *recordingStorageActionsClient) withCallLog(log *callLog) *recordingStorageActionsClient {
	m.log = log
	return m
}

func (m *recordingStorageActionsClient) DetachByDropletID(_ context.Context, vol string, drop int) (*godo.Action, *godo.Response, error) {
	m.detachCalls = append(m.detachCalls, struct{ VolumeID string; DropletID int }{vol, drop})
	m.log.record("StorageActions.DetachByDropletID")
	if m.detachErr != nil {
		return nil, nil, m.detachErr
	}
	m.nextActionID++
	return &godo.Action{ID: m.nextActionID, Status: "in-progress"}, nil, nil
}

// Resize satisfies StorageActionsClient — not used in Replace tests.
func (m *recordingStorageActionsClient) Resize(_ context.Context, _ string, _ int, _ string) (*godo.Action, *godo.Response, error) {
	return nil, nil, fmt.Errorf("not implemented in this fake")
}

func newRecordingStorageActionsClient() *recordingStorageActionsClient {
	return &recordingStorageActionsClient{}
}

// recordingActionsClient: programmable Get-status sequence. callCount
// indexes into statusSequence; out-of-bounds returns defaultStatus
// ("completed" for happy-path tests; override via withDefaultStatus for
// timeout tests that need perpetual "in-progress").
type recordingActionsClient struct {
	statusSequence []string
	defaultStatus  string
	callCount      int

	log *callLog
}

func (c *recordingActionsClient) withCallLog(log *callLog) *recordingActionsClient {
	c.log = log
	return c
}

func (m *recordingActionsClient) Get(_ context.Context, _ int) (*godo.Action, *godo.Response, error) {
	var status string
	if m.callCount < len(m.statusSequence) {
		status = m.statusSequence[m.callCount]
	} else {
		status = m.defaultStatus
	}
	m.callCount++
	m.log.record("Actions.Get")
	return &godo.Action{Status: status}, nil, nil
}

func newRecordingActionsClient(seq ...string) *recordingActionsClient {
	return &recordingActionsClient{statusSequence: seq, defaultStatus: "completed"}
}

// withDefaultStatus returns the receiver after setting the given default
// status for out-of-sequence polls. Use "in-progress" for timeout tests.
func (c *recordingActionsClient) withDefaultStatus(status string) *recordingActionsClient {
	c.defaultStatus = status
	return c
}
