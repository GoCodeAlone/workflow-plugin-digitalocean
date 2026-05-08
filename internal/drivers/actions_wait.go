package drivers

import (
	"context"
	"fmt"
	"time"
)

// waitForActionComplete polls actions.Get(ctx, actionID) until Status
// transitions to "completed" or "errored", or until timeout expires
// (returning a wrapped error). ctx cancellation propagates immediately.
//
// Bounds: 60s default with 2s polling — typical DO action time is 1-5s
// for volume detach; 60s is the operator-recovery boundary documented
// in the design doc. Both bounds are parameterized for testability.
func waitForActionComplete(ctx context.Context, c ActionsClient, actionID int, timeout, pollInterval time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait action %d: %w", actionID, err)
		}
		action, _, err := c.Get(ctx, actionID)
		if err != nil {
			return fmt.Errorf("wait action %d: poll: %w", actionID, err)
		}
		switch action.Status {
		case "completed":
			return nil
		case "errored":
			return fmt.Errorf("wait action %d: errored", actionID)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("wait action %d: timeout after %s (status=%s)", actionID, timeout, action.Status)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait action %d: %w", actionID, ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

// Production defaults — exported for callers that want the canonical
// bounds without naming magic numbers.
const (
	defaultActionWaitTimeout      = 60 * time.Second
	defaultActionWaitPollInterval = 2 * time.Second
)
