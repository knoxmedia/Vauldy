package publication

import (
	"context"
	"fmt"

	"knox-media/internal/store"
)

// retirementBarrierProbe is installed by tests to observe the Task 12 hook call site.
var retirementBarrierProbe func(runID int64)

// retirementBarrierRecompute is installed by internal/retirement to flip blocked↔ready.
var retirementBarrierRecompute func(ctx context.Context, tx store.SQLExecutor, runID int64) error

// SetRetirementBarrierProbeForTest installs a probe observing RecomputeRetirementBarrierTx.
func SetRetirementBarrierProbeForTest(fn func(runID int64)) { retirementBarrierProbe = fn }

// ClearRetirementBarrierProbeForTest clears the Task 12 barrier probe.
func ClearRetirementBarrierProbeForTest() { retirementBarrierProbe = nil }

// SetRetirementBarrierRecompute registers the authoritative barrier recompute implementation.
// Called from retirement.init (wire.go) to avoid an import cycle.
func SetRetirementBarrierRecompute(fn func(ctx context.Context, tx store.SQLExecutor, runID int64) error) {
	retirementBarrierRecompute = fn
}

// RecomputeRetirementBarrierTx is the plaintext-retirement barrier hook invoked after
// every durable node/queue transition finalization. When retirement is wired it updates
// blocked↔ready; otherwise it remains a successful no-op.
func RecomputeRetirementBarrierTx(ctx context.Context, tx store.SQLExecutor, runID int64) error {
	if tx == nil || runID <= 0 {
		return fmt.Errorf("publication retirement barrier: invalid transaction or run")
	}
	if retirementBarrierProbe != nil {
		retirementBarrierProbe(runID)
	}
	if retirementBarrierRecompute != nil {
		return retirementBarrierRecompute(ctx, tx, runID)
	}
	return nil
}

// projectNodeTransitionTx runs dependency propagation, plan-completion recompute, and
// the retirement-barrier hook. Aggregate is intentionally separate so startup can
// validate snapshot/queue semantics before mutating publication visibility.
func projectNodeTransitionTx(ctx context.Context, tx store.SQLExecutor, runID int64) error {
	if err := PropagateImpossibleDependenciesTx(ctx, tx, runID); err != nil {
		return err
	}
	if err := RecomputePlanCompletionTx(ctx, tx, runID); err != nil {
		return err
	}
	return RecomputeRetirementBarrierTx(ctx, tx, runID)
}

// FinalizeNodeTransitionTx centralizes post-transition plan projection work inside the
// caller-owned transaction: impossible-dependency propagation, plan-completion recompute,
// retirement-barrier recompute (Task 12 hook), and publication aggregate.
func FinalizeNodeTransitionTx(ctx context.Context, tx store.SQLExecutor, runID int64) error {
	if tx == nil || runID <= 0 {
		return fmt.Errorf("publication finalize: invalid transaction or run")
	}
	if err := projectNodeTransitionTx(ctx, tx, runID); err != nil {
		return err
	}
	return AggregateTx(ctx, tx, runID)
}

// FinalizeClaimTransitionTx keeps plan-completion waiting/running counts accurate after a
// durable waiting→running claim. Claim does not change terminals or publication visibility,
// so propagate, retirement barrier, and aggregate are intentionally skipped.
func FinalizeClaimTransitionTx(ctx context.Context, tx store.SQLExecutor, runID int64) error {
	if tx == nil || runID <= 0 {
		return fmt.Errorf("publication claim finalize: invalid transaction or run")
	}
	return RecomputePlanCompletionTx(ctx, tx, runID)
}
