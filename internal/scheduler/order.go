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

// saturatingAdd returns a + b, clamped to the signed int64 range.
func saturatingAdd(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	if b < 0 && a < math.MinInt64-b {
		return math.MinInt64
	}
	return a + b
}

// saturatingMul returns a * b, clamped to the signed int64 range.
func saturatingMul(a, b int64) int64 {
	if a == 0 || b == 0 {
		return 0
	}
	if a == -1 && b == math.MinInt64 || b == -1 && a == math.MinInt64 {
		return math.MaxInt64
	}
	if a > 0 {
		if b > 0 && a > math.MaxInt64/b {
			return math.MaxInt64
		}
		if b < 0 && b < math.MinInt64/a {
			return math.MinInt64
		}
	} else {
		if b > 0 && a < math.MinInt64/b {
			return math.MinInt64
		}
		if b < 0 && a < math.MaxInt64/b {
			return math.MaxInt64
		}
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

// LibraryFairnessPick returns the row index for the next distinct eligible
// library bucket. The input must be in canonical row order, so the first row
// seen for a bucket is that bucket's best candidate. The boolean is false when
// no eligible bucket exists.
func LibraryFairnessPick(cursor *LibraryFairnessCursor, window []LibraryCandidate, maxLookahead int) (int, bool) {
	if len(window) == 0 || maxLookahead <= 0 {
		return 0, false
	}
	type libKey struct {
		isNull bool
		id     int64
	}
	keyOf := func(c LibraryCandidate) libKey {
		if c.LibraryID == nil {
			return libKey{isNull: true}
		}
		return libKey{id: *c.LibraryID}
	}
	seen := make(map[libKey]struct{}, len(window))
	eligible := make([]int, 0, len(window))
	for i, c := range window {
		if c.Removed || c.Superseded {
			continue
		}
		key := keyOf(c)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		eligible = append(eligible, i)
	}
	if len(eligible) == 0 {
		return 0, false
	}
	start := 0
	if cursor != nil && cursor.LastServedLibrary != nil {
		last := libKey{id: *cursor.LastServedLibrary}
		if *cursor.LastServedLibrary == NullLibrarySentinel {
			last = libKey{isNull: true}
		}
		for pos, idx := range eligible {
			if keyOf(window[idx]) == last {
				start = (pos + 1) % len(eligible)
				break
			}
		}
	}
	return eligible[start], true
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
	if interval <= 0 {
		return "0"
	}
	steps := fmt.Sprintf("((%s) / %d)", waitAge, interval)
	return saturatingMulSQL(steps, int64(policy.AgingStep))
}

func saturatingAddSQL(a, b string) string {
	return fmt.Sprintf(`CASE WHEN (%[2]s)>0 AND (%[1]s)>9223372036854775807-(%[2]s) THEN 9223372036854775807 WHEN (%[2]s)<0 AND (%[1]s)<-9223372036854775807-1-(%[2]s) THEN -9223372036854775807-1 ELSE CAST((%[1]s)+(%[2]s) AS INTEGER) END`, a, b)
}

func saturatingMulSQL(a string, b int64) string {
	if b == 0 {
		return "0"
	}
	if b > 0 {
		return fmt.Sprintf(`CASE WHEN (%[1]s)>9223372036854775807/%[2]d THEN 9223372036854775807 WHEN (%[1]s)<(-9223372036854775807-1)/%[2]d THEN -9223372036854775807-1 ELSE CAST((%[1]s)*%[2]d AS INTEGER) END`, a, b)
	}
	if b == math.MinInt64 {
		return fmt.Sprintf(`CASE WHEN (%[1]s)>1 THEN -9223372036854775807-1 WHEN (%[1]s)<-1 THEN 9223372036854775807 WHEN (%[1]s)=-1 THEN 9223372036854775807 ELSE CAST((%[1]s)*(-9223372036854775807-1) AS INTEGER) END`, a)
	}
	return fmt.Sprintf(`CASE WHEN (%[1]s)>(-9223372036854775807-1)/%[2]d THEN -9223372036854775807-1 WHEN (%[1]s)<9223372036854775807/%[2]d THEN 9223372036854775807 ELSE CAST((%[1]s)*%[2]d AS INTEGER) END`, a, b)
}

// EffectivePrioritySQL computes the same signed saturating integer expression
// as EffectivePriority without allowing SQLite to promote overflow to REAL.
func EffectivePrioritySQL(alias string, policy *Policy, now time.Time) string {
	base := fmt.Sprintf("COALESCE(%s.base_priority,0)", alias)
	row := fmt.Sprintf("COALESCE(%s.priority,0)", alias)
	result := saturatingAddSQL(base, row)
	result = saturatingAddSQL(result, AgingBoostSQL(alias, policy, now))
	return saturatingAddSQL(result, RunNowBoostSQL(alias, policy, now))
}
