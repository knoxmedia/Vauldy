package taskcontrol

import (
	"testing"
)

// --- Allowed Actions by State ---

func TestAllowedActionsWaiting(t *testing.T) {
	row := &ProjectionRow{NormalizedStatus: StatusWaiting}
	actions := ComputeActions(row)
	if !actions.Remove {
		t.Error("remove should be allowed for waiting")
	}
	if !actions.RunNow {
		t.Error("run_now should be allowed for waiting with capable worker")
	}
	if actions.Abort {
		t.Error("abort should NOT be allowed for waiting")
	}
	if actions.Reset {
		t.Error("reset should NOT be allowed for waiting")
	}
	if actions.Skip {
		t.Error("skip should NOT be allowed for non-skippable waiting")
	}
	if actions.Reopen {
		t.Error("reopen should NOT be allowed for waiting")
	}
}

func TestAllowedActionsRunning(t *testing.T) {
	row := &ProjectionRow{NormalizedStatus: StatusRunning}
	actions := ComputeActions(row)
	if !actions.Abort {
		t.Error("abort should be allowed for running")
	}
	if !actions.Remove {
		t.Error("remove should be allowed for running")
	}
	if actions.RunNow {
		t.Error("run_now should NOT be allowed for running")
	}
	if actions.Reset {
		t.Error("reset should NOT be allowed for running")
	}
	if actions.Skip {
		t.Error("skip should NOT be allowed for running")
	}
}

func TestAllowedActionsDone(t *testing.T) {
	row := &ProjectionRow{NormalizedStatus: StatusDone}
	actions := ComputeActions(row)
	if !actions.Remove {
		t.Error("remove should be allowed for done")
	}
	if !actions.Reset {
		t.Error("reset should be allowed for done (terminal)")
	}
	if actions.Abort {
		t.Error("abort should NOT be allowed for done")
	}
	if actions.RunNow {
		t.Error("run_now should NOT be allowed for done")
	}
}

func TestAllowedActionsFailed(t *testing.T) {
	row := &ProjectionRow{NormalizedStatus: StatusFailed}
	actions := ComputeActions(row)
	if !actions.Remove {
		t.Error("remove should be allowed for failed")
	}
	if !actions.Reset {
		t.Error("reset should be allowed for failed (terminal)")
	}
	if actions.Abort {
		t.Error("abort should NOT be allowed for failed")
	}
}

func TestAllowedActionsCancelled(t *testing.T) {
	row := &ProjectionRow{NormalizedStatus: StatusCancelled}
	actions := ComputeActions(row)
	if !actions.Remove {
		t.Error("remove should be allowed for cancelled")
	}
	if !actions.Reset {
		t.Error("reset should be allowed for cancelled (terminal)")
	}
	if actions.Abort {
		t.Error("abort should NOT be allowed for cancelled")
	}
}

func TestAllowedActionsSkipped(t *testing.T) {
	row := &ProjectionRow{NormalizedStatus: StatusSkipped}
	actions := ComputeActions(row)
	if !actions.Remove {
		t.Error("remove should be allowed for skipped")
	}
	if !actions.Reset {
		t.Error("reset should be allowed for skipped (terminal)")
	}
	if actions.Abort {
		t.Error("abort should NOT be allowed for skipped")
	}
	if actions.Reopen {
		t.Error("reopen should NOT be allowed for non-AI skipped")
	}
}

// --- All Six Normalized States ---

func TestAllowedActionsAllSixStates(t *testing.T) {
	states := []NormalizedStatus{
		StatusWaiting, StatusRunning, StatusDone,
		StatusFailed, StatusCancelled, StatusSkipped,
	}
	for _, s := range states {
		row := &ProjectionRow{NormalizedStatus: s}
		actions := ComputeActions(row)
		// Remove is always allowed for non-removed rows
		if !actions.Remove {
			t.Errorf("remove should be allowed for %s", s)
		}
	}
}

// --- Owner/Fence Tests ---

func TestAllowedActionsAbortOnlyRunningBlocksWhenPaused(t *testing.T) {
	row := &ProjectionRow{NormalizedStatus: StatusRunning}
	actions := ComputeActions(row, WithControlState("paused"))
	if actions.Abort {
		t.Error("abort should NOT be allowed when paused")
	}
}

func TestAllowedActionsAbortOnlyRunningBlocksWhenDraining(t *testing.T) {
	row := &ProjectionRow{NormalizedStatus: StatusRunning}
	actions := ComputeActions(row, WithControlState("draining"))
	if actions.Abort {
		t.Error("abort should NOT be allowed when draining")
	}
}

// --- Retry Policy Tests ---

func TestAllowedActionsResetWhenRetryPolicyExhausted(t *testing.T) {
	row := &ProjectionRow{
		NormalizedStatus: StatusFailed,
		RetryRound:       4,
	}
	actions := ComputeActions(row, WithRetryPolicyMax(3))
	if actions.Reset {
		t.Error("reset should NOT be allowed when retry round exceeds policy max")
	}
}

func TestAllowedActionsResetWhenRetryPolicyUnlimited(t *testing.T) {
	row := &ProjectionRow{
		NormalizedStatus: StatusFailed,
		RetryRound:       99,
	}
	actions := ComputeActions(row, WithRetryPolicyMax(0))
	if !actions.Reset {
		t.Error("reset should be allowed when retry policy is unlimited (max=0)")
	}
}

func TestAllowedActionsResetWithinRetryPolicy(t *testing.T) {
	row := &ProjectionRow{
		NormalizedStatus: StatusFailed,
		RetryRound:       2,
	}
	actions := ComputeActions(row, WithRetryPolicyMax(5))
	if !actions.Reset {
		t.Error("reset should be allowed when retry round < policy max")
	}
}

// --- Worker Capability Tests ---

func TestAllowedActionsRunNowWhenNoWorker(t *testing.T) {
	row := &ProjectionRow{NormalizedStatus: StatusWaiting}
	actions := ComputeActions(row, WithCapableWorker(false))
	if actions.RunNow {
		t.Error("run_now should NOT be allowed without capable worker")
	}
}

func TestAllowedActionsRunNowWhenWorkerAvailable(t *testing.T) {
	row := &ProjectionRow{NormalizedStatus: StatusWaiting}
	actions := ComputeActions(row, WithCapableWorker(true))
	if !actions.RunNow {
		t.Error("run_now should be allowed with capable worker")
	}
}

// --- Skip Tests ---

func TestAllowedActionsSkipSkippableWaiting(t *testing.T) {
	row := &ProjectionRow{NormalizedStatus: StatusWaiting}
	actions := ComputeActions(row, WithSkippable(true))
	if !actions.Skip {
		t.Error("skip should be allowed for skippable waiting")
	}
}

func TestAllowedActionsSkipNonSkippableWaiting(t *testing.T) {
	row := &ProjectionRow{NormalizedStatus: StatusWaiting}
	actions := ComputeActions(row, WithSkippable(false))
	if actions.Skip {
		t.Error("skip should NOT be allowed for non-skippable waiting")
	}
}

func TestAllowedActionsSkipRunning(t *testing.T) {
	row := &ProjectionRow{NormalizedStatus: StatusRunning}
	actions := ComputeActions(row, WithSkippable(true))
	if actions.Skip {
		t.Error("skip should NOT be allowed for running")
	}
}

// --- Reopen Tests ---

func TestAllowedActionsReopenSkippedAI(t *testing.T) {
	row := &ProjectionRow{NormalizedStatus: StatusSkipped}
	actions := ComputeActions(row, WithAITask(true))
	if !actions.Reopen {
		t.Error("reopen should be allowed for skipped AI task")
	}
}

func TestAllowedActionsReopenSkippedNonAI(t *testing.T) {
	row := &ProjectionRow{NormalizedStatus: StatusSkipped}
	actions := ComputeActions(row, WithAITask(false))
	if actions.Reopen {
		t.Error("reopen should NOT be allowed for skipped non-AI task")
	}
}

func TestAllowedActionsReopenDone(t *testing.T) {
	row := &ProjectionRow{NormalizedStatus: StatusDone}
	actions := ComputeActions(row, WithAITask(true))
	if actions.Reopen {
		t.Error("reopen should NOT be allowed for done (even AI)")
	}
}

// --- Removed / Tombstone Block Remove ---

func TestAllowedActionsRemoveBlockedByRemoved(t *testing.T) {
	row := &ProjectionRow{NormalizedStatus: StatusDone}
	row.RemovedAt = &row.CreatedAt // non-nil
	actions := ComputeActions(row)
	if actions.Remove {
		t.Error("remove should NOT be allowed when already removed")
	}
}

func TestAllowedActionsRemoveBlockedByTombstone(t *testing.T) {
	row := &ProjectionRow{
		NormalizedStatus: StatusDone,
		Tombstone:        true,
	}
	actions := ComputeActions(row)
	if actions.Remove {
		t.Error("remove should NOT be allowed for tombstone")
	}
}

// --- Combined Options ---

func TestAllowedActionsCombinedOptions(t *testing.T) {
	row := &ProjectionRow{
		NormalizedStatus: StatusWaiting,
	}
	actions := ComputeActions(row,
		WithSkippable(true),
		WithCapableWorker(true),
		WithAITask(false),
	)
	if !actions.RunNow {
		t.Error("run_now with worker")
	}
	if !actions.Skip {
		t.Error("skip with skippable")
	}
	if !actions.Remove {
		t.Error("remove default")
	}
	if actions.Reopen {
		t.Error("reopen not for waiting")
	}
	if actions.Abort {
		t.Error("abort not for waiting")
	}
}

// --- Aggregation / Blocker surface tests ---

func TestAllowedActionsBlockerNoWorker(t *testing.T) {
	row := &ProjectionRow{NormalizedStatus: StatusWaiting}
	actions := ComputeActions(row, WithCapableWorker(false))
	if actions.RunNow {
		t.Fatal("run_now must be blocked when no worker is capable")
	}
	// Verify remove is still available even when blocked
	if !actions.Remove {
		t.Error("remove should still be allowed even when no worker")
	}
}

func TestAllowedActionsBlockerPausedDrain(t *testing.T) {
	for _, state := range []string{"paused", "draining"} {
		row := &ProjectionRow{NormalizedStatus: StatusRunning}
		actions := ComputeActions(row, WithControlState(state))
		if actions.Abort {
			t.Errorf("abort must be blocked when state=%s", state)
		}
		// Remove should still be allowed
		if !actions.Remove {
			t.Errorf("remove should still be allowed when state=%s", state)
		}
	}
}

func TestAllowedActionsBlockerRetryExhausted(t *testing.T) {
	row := &ProjectionRow{
		NormalizedStatus: StatusFailed,
		RetryRound:       10,
	}
	actions := ComputeActions(row, WithRetryPolicyMax(5))
	if actions.Reset {
		t.Error("reset blocked when retry exhausted")
	}
	if !actions.Remove {
		t.Error("remove still allowed even when retry exhausted")
	}
}
