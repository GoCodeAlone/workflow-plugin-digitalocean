package drivers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/digitalocean/godo"
)

type fakeActionsClient struct {
	statusSequence []string
	defaultStatus  string // returned when callCount >= len(statusSequence); empty → return "ran out" error
	callCount      int
}

func (f *fakeActionsClient) Get(_ context.Context, actionID int) (*godo.Action, *godo.Response, error) {
	if f.callCount >= len(f.statusSequence) {
		if f.defaultStatus != "" {
			f.callCount++
			return &godo.Action{ID: actionID, Status: f.defaultStatus}, nil, nil
		}
		return nil, nil, errors.New("ran out of statuses")
	}
	s := f.statusSequence[f.callCount]
	f.callCount++
	return &godo.Action{ID: actionID, Status: s}, nil, nil
}

func TestWaitForActionComplete_CompletesQuickly(t *testing.T) {
	f := &fakeActionsClient{statusSequence: []string{"in-progress", "completed"}}
	err := waitForActionComplete(context.Background(), f, 12345, 2*time.Second, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	if f.callCount != 2 {
		t.Errorf("want 2 polls, got %d", f.callCount)
	}
}

func TestWaitForActionComplete_ErroredStatusReturnsError(t *testing.T) {
	f := &fakeActionsClient{statusSequence: []string{"errored"}}
	err := waitForActionComplete(context.Background(), f, 12345, 2*time.Second, 100*time.Millisecond)
	if err == nil {
		t.Fatal("want error, got nil")
	}
}

func TestWaitForActionComplete_ContextCancelPropagates(t *testing.T) {
	f := &fakeActionsClient{statusSequence: []string{"in-progress", "in-progress"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitForActionComplete(ctx, f, 12345, 2*time.Second, 100*time.Millisecond)
	if err == nil {
		t.Fatal("want context cancellation error")
	}
}

func TestWaitForActionComplete_TimeoutBoundary(t *testing.T) {
	// defaultStatus="in-progress" so polls beyond statusSequence keep returning
	// "in-progress" instead of erroring with "ran out of statuses". This is
	// what waitForActionComplete needs to actually exercise its timeout path
	// (it polls one more time after the deadline check before exiting).
	f := &fakeActionsClient{
		statusSequence: []string{"in-progress"},
		defaultStatus:  "in-progress",
	}
	err := waitForActionComplete(context.Background(), f, 12345, 200*time.Millisecond, 50*time.Millisecond)
	if err == nil {
		t.Fatal("want timeout error")
	}
	if !strings.Contains(err.Error(), "timeout after") {
		t.Errorf("want timeout error, got: %v", err)
	}
}
