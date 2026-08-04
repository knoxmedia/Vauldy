package taskcontrol

// AllowedActions encodes the server-authoritative actions available for
// a task given its current projection state.
type AllowedActions struct {
	Abort  bool `json:"abort"`
	Remove bool `json:"remove"`
	Reset  bool `json:"reset"`
	RunNow bool `json:"run_now"`
	Skip   bool `json:"skip"`
	Reopen bool `json:"reopen"`
}

// actionPolicy computes the allowed actions from a projection row and
// optional context (control state, worker capability, retry policy,
// encrypted-source status, recovery journal, cleanup state).
type actionPolicy struct {
	row         *ProjectionRow
	controlState    string // "running", "paused", "draining"
	capableWorker   bool
	retryPolicyMax  int  // max retry rounds allowed under policy (0 = unlimited)
	encryptedSource bool
	recoveryActive  bool
	cleanupState    string // "", "cleanup_pending", "cleanup_in_progress"
	skippable       bool   // true when dependencies were auto-skipped
	aiTask          bool   // true for ai_analysis tasks
}

// ComputeActions derives allowed actions from projection truth and
// optional server-side context.
func ComputeActions(row *ProjectionRow, opts ...ActionOption) AllowedActions {
	ap := actionPolicy{
		row:           row,
		capableWorker: true,
	}
	for _, o := range opts {
		o(&ap)
	}

	if row.NormalizedStatus.IsRunning() && ap.controlState == "" {
		ap.controlState = "running"
	}

	// Remove is always allowed for non-removed, non-tombstone items.
	removeAllowed := row.RemovedAt == nil && !row.Tombstone

	// Abort: only running and the type is not paused/drained.
	abortAllowed := row.NormalizedStatus == StatusRunning &&
		ap.controlState != "paused" && ap.controlState != "draining"

	// Reset: terminal states, policy-permitted retry rounds.
	resetAllowed := false
	if row.NormalizedStatus.IsTerminal() {
		resetAllowed = ap.retryPolicyMax == 0 || (row.RetryRound < ap.retryPolicyMax)
	}

	// RunNow: waiting tasks with a capable worker.
	runNowAllowed := row.NormalizedStatus == StatusWaiting && ap.capableWorker

	// Skip: only explicitly skippable waiting nodes.
	skipAllowed := row.NormalizedStatus == StatusWaiting && ap.skippable

	// Reopen: only eligible skipped AI analysis tasks.
	reopenAllowed := row.NormalizedStatus == StatusSkipped && ap.aiTask

	return AllowedActions{
		Abort:  abortAllowed,
		Remove: removeAllowed,
		Reset:  resetAllowed,
		RunNow: runNowAllowed,
		Skip:   skipAllowed,
		Reopen: reopenAllowed,
	}
}

// ActionOption configures the action computation context.
type ActionOption func(*actionPolicy)

// WithControlState sets the scheduler control state.
func WithControlState(state string) ActionOption {
	return func(ap *actionPolicy) {
		ap.controlState = state
	}
}

// WithCapableWorker sets whether a capable worker is available.
func WithCapableWorker(available bool) ActionOption {
	return func(ap *actionPolicy) {
		ap.capableWorker = available
	}
}

// WithRetryPolicyMax sets the maximum allowed retry rounds.
func WithRetryPolicyMax(maxRounds int) ActionOption {
	return func(ap *actionPolicy) {
		ap.retryPolicyMax = maxRounds
	}
}

// WithEncryptedSource sets whether the source requires encryption handling.
func WithEncryptedSource(encrypted bool) ActionOption {
	return func(ap *actionPolicy) {
		ap.encryptedSource = encrypted
	}
}

// WithRecoveryActive sets whether recovery journal is active.
func WithRecoveryActive(active bool) ActionOption {
	return func(ap *actionPolicy) {
		ap.recoveryActive = active
	}
}

// WithCleanupState sets the cleanup state.
func WithCleanupState(state string) ActionOption {
	return func(ap *actionPolicy) {
		ap.cleanupState = state
	}
}

// WithSkippable sets whether the node is explicitly skippable.
func WithSkippable(skippable bool) ActionOption {
	return func(ap *actionPolicy) {
		ap.skippable = skippable
	}
}

// WithAITask marks the task as an AI analysis task eligible for reopen.
func WithAITask(ai bool) ActionOption {
	return func(ap *actionPolicy) {
		ap.aiTask = ai
	}
}
