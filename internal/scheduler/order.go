package scheduler

import (
	"fmt"
	"math"
	"time"
)

// CandidateOrder holds the fields used for stable tie-breaking when effective
// priorities are equal.
type CandidateOrder struct {
	Priority    int64
	AvailableAt time.Time
	CreatedAt   time.Time
	ID          int64
}

// LibraryCandidate describes one library row in a fairness window.
type LibraryCandidate struct {
	LibraryID   *int64 // nil means null library (synthetic bucket)
	Priority    int64
	AvailableAt time.Time
	CreatedAt   time.Time
	ID          int64
	Removed     bool
	Superseded  bool
}

// NullLibrarySentinel is stored in LastServedLibrary when the null library
// (synthetic bucket) was last served, so that a nil pointer can mean "fresh /
// never served yet."
var NullLibrarySentinel = int64(-1)

// LibraryFairnessCursor tracks which library was last served for a task type.
type LibraryFairnessCursor struct {
	TaskType          string
	LastServedLibrary *int64 // nil means fresh/uninitialized; &NullLibrarySentinel means null library bucket
}

// EffectivePriority computes the claim-order effective priority for a
// candidate row using saturating integer arithmetic and no cap.
//
//	effective = base_priority + row_priority + run_now_boost
//	            + floor(nonnegative_wait_age / aging_interval) * aging_step
//
// Negative and future wait ages clamp to zero.  Integer overflow saturates at
// math.MaxInt64.
func EffectivePriority(policy *Policy, basePriority, rowPriority int64, availableAt time.Time, runNowExpires *time.Time, now time.Time) int64 {
	age := nonNegativeWaitAge(now, availableAt, now)
	aging := saturatingDiv(age, int64(policy.AgingIntervalSec))
	agingBoost := saturatingMul(aging, int64(policy.AgingStep))

	runNowBoost := int64(0)
	if runNowExpires != nil {
		if runNowExpires.After(now) {
			runNowBoost = int64(policy.RunNowAmount)
		}
	}

	result := saturatingAdd(basePriority, rowPriority)
	result = saturatingAdd(result, agingBoost)
	result = saturatingAdd(result, runNowBoost)
	return result
}

// nonNegativeWaitAge returns the number of seconds between availableAt and now.
// Returns 0 when availableAt is zero or in the future.
func nonNegativeWaitAge(now, availableAt, fallback time.Time) int64 {
	if availableAt.IsZero() {
		return 0
	}
	if availableAt.After(now) {
		return 0
	}
	d := now.Sub(availableAt)
	if d < 0 {
		return 0
	}
	secs := int64(d / time.Second)
	if secs < 0 {
		return 0
	}
	return secs
}

// saturatingAdd returns a + b or math.MaxInt64 on overflow.
func saturatingAdd(a, b int64) int64 {
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

// saturatingMul returns a * b or math.MaxInt64 on overflow.
func saturatingMul(a, b int64) int64 {
	if a == 0 || b == 0 {
		return 0
	}
	if a > math.MaxInt64/b {
		return math.MaxInt64
	}
	return a * b
}

// saturatingDiv returns a / b, clamping division by zero to 0.
func saturatingDiv(a, b int64) int64 {
	if b <= 0 {
		return 0
	}
	return a / b
}

// StableCompare returns < 0 if a should be served before b in claim selection
// given equal effective priorities.
func StableCompare(a, b CandidateOrder) int {
	if a.Priority != b.Priority {
		if a.Priority > b.Priority {
			return -1
		}
		return 1
	}
	if !a.AvailableAt.Equal(b.AvailableAt) {
		if a.AvailableAt.Before(b.AvailableAt) {
			return -1
		}
		return 1
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		if a.CreatedAt.Before(b.CreatedAt) {
			return -1
		}
		return 1
	}
	if a.ID < b.ID {
		return -1
	}
	if a.ID > b.ID {
		return 1
	}
	return 0
}

// LibraryFairnessPick returns the index of the next library candidate to serve
// from the window. It skips removed and superseded libraries, wraps around
// after the last-served library, and limits the search to maxLookahead to
// avoid unbounded scans. If maxLookahead is <= 0, it defaults to 1.
func LibraryFairnessPick(cursor *LibraryFairnessCursor, window []LibraryCandidate, maxLookahead int) int {
	if len(window) == 0 || maxLookahead <= 0 {
		return 0
	}

	eligible := make([]int, 0, len(window))
	for i, c := range window {
		if !c.Removed && !c.Superseded {
			eligible = append(eligible, i)
		}
	}
	if len(eligible) == 0 {
		return 0
	}

	// Build a map from library key to eligible index for fast lookup.
	type libKey struct {
		isNull bool
		id     int64
	}
	keyOf := func(i int) libKey {
		if window[i].LibraryID == nil {
			return libKey{isNull: true}
		}
		return libKey{isNull: false, id: *window[i].LibraryID}
	}

	// Find the position after the last-served library.
	lastKey := libKey{}
	if cursor.LastServedLibrary != nil {
		if *cursor.LastServedLibrary == NullLibrarySentinel {
			lastKey = libKey{isNull: true}
		} else {
			lastKey = libKey{isNull: false, id: *cursor.LastServedLibrary}
		}
	}
	// When LastServedLibrary is nil, the cursor is fresh and we start at
	// position 0 (lastKey remains zero-value, which won't match any real key).

	startPos := 0
	for i, idx := range eligible {
		if keyOf(idx) == lastKey {
			startPos = i + 1
			break
		}
	}

	// Search forward from startPos, bounded by maxLookahead.
	for offset := 0; offset < maxLookahead && offset < len(eligible); offset++ {
		pos := (startPos + offset) % len(eligible)
		return eligible[pos]
	}

	return eligible[0]
}

// LibraryFairnessCommit records that the given candidate was served,
// updating the cursor's last-served library.
func LibraryFairnessCommit(cursor *LibraryFairnessCursor, candidate LibraryCandidate) {
	if candidate.LibraryID == nil {
		cursor.LastServedLibrary = &NullLibrarySentinel
	} else {
		v := *candidate.LibraryID
		cursor.LastServedLibrary = &v
	}
}

// LibraryKey returns a deterministic string key for SQL parameterization and
// Go-side cursor tracking. Nil library maps to the empty string (synthetic
// null bucket).
func LibraryKey(id *int64) string {
	if id == nil {
		return ""
	}
	return fmt.Sprintf("%d", *id)
}

// RunNowBoostSQL returns a SQL expression that evaluates to the active
// run-now boost for alias q at the given now.  Returns 0 when the boost has
// expired or when run_now_expires IS NULL.
//
// The expression uses the Policy fields RunNowAmount and RunNowTTLSec to
// compute whether the boost is still active.
func RunNowBoostSQL(alias string, policy *Policy, now time.Time) string {
	nowStr := now.UTC().Format("2006-01-02 15:04:05")
	amount := int64(policy.RunNowAmount)
	return fmt.Sprintf(
		`CASE WHEN %s.run_now_expires IS NOT NULL AND %s.run_now_expires > '%s' THEN %d ELSE 0 END`,
		alias, alias, nowStr, amount,
	)
}

// WaitAgeSQL returns a SQL expression that computes the non-negative wait age
// in seconds for alias q at the given now.  Uses available_at first, then
// falls back to created_at.
func WaitAgeSQL(alias string, now time.Time) string {
	nowStr := now.UTC().Format("2006-01-02 15:04:05")
	return fmt.Sprintf(
		`MAX(0, CAST((julianday('%s') - julianday(COALESCE(%s.available_at, %s.created_at))) * 86400 AS INTEGER))`,
		nowStr, alias, alias,
	)
}

// AgingBoostSQL returns a SQL expression that computes the aging portion of
// effective priority: floor(wait_age / interval) * step, with saturating
// arithmetic and no cap.
func AgingBoostSQL(alias string, policy *Policy, now time.Time) string {
	waitAge := WaitAgeSQL(alias, now)
	interval := int64(policy.AgingIntervalSec)
	step := int64(policy.AgingStep)
	return fmt.Sprintf(
		`MIN(9223372036854775807, (%s / %d) * %d)`,
		waitAge, interval, step,
	)
}

// EffectivePrioritySQL returns a single SQL expression that computes the
// effective ordering priority for alias q.  The expression is parameterized
// with the Policy fields and the given now timestamp.
//
//	effective = base_priority + row_priority + run_now_boost + aging_boost
func EffectivePrioritySQL(alias string, policy *Policy, now time.Time) string {
	aging := AgingBoostSQL(alias, policy, now)
	runNow := RunNowBoostSQL(alias, policy, now)
	return fmt.Sprintf(
		`MIN(9223372036854775807, CAST(COALESCE(%s.base_priority, 0) + COALESCE(%s.priority, 0) + %s + %s AS INTEGER))`,
		alias, alias, runNow, aging,
	)
}
