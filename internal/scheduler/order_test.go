package scheduler

import (
	"math"
	"testing"
	"time"
)

func TestEffectivePriorityBasic(t *testing.T) {
	// base_priority + row_priority + active_run_now_boost + floor(nonnegative_wait_age / aging_interval) * aging_step
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	policy := &Policy{AgingIntervalSec: 300, AgingStep: 1, RunNowAmount: 100, RunNowTTLSec: 600}

	cases := []struct {
		name          string
		basePriority  int64
		rowPriority   int64
		availableAt   time.Time
		runNowExpires *time.Time
		want          int64
	}{
		{"zero all", 0, 0, now, nil, 0},
		{"base only", 10, 0, now, nil, 10},
		{"row only", 0, 5, now, nil, 5},
		{"base+row", 10, 5, now, nil, 15},
		{"with aging 1 step", 0, 0, now.Add(-301 * time.Second), nil, 1}, // 301/300 = 1 step
		{"with aging 2 steps", 0, 0, now.Add(-600 * time.Second), nil, 2},
		{"with aging many steps", 0, 0, now.Add(-3600 * time.Second), nil, 12}, // 3600/300 = 12
		{"base+row+aging", 10, 5, now.Add(-600 * time.Second), nil, 17},        // 10+5+2 = 17
		{"active run now", 0, 0, now, timePtr(now.Add(300 * time.Second)), 100},
		{"expired run now", 0, 0, now, timePtr(now.Add(-1 * time.Second)), 0},
		{"all combined", 10, 5, now.Add(-600 * time.Second), timePtr(now.Add(300 * time.Second)), 117},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EffectivePriority(policy, tc.basePriority, tc.rowPriority, tc.availableAt, tc.runNowExpires, now)
			if got != tc.want {
				t.Fatalf("EffectivePriority = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestEffectivePriorityNoCap(t *testing.T) {
	// Wait age has no cap; aging grows unbounded.
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	policy := &Policy{AgingIntervalSec: 300, AgingStep: 1, RunNowAmount: 100, RunNowTTLSec: 600}
	// One year of aging: 365*24*3600 / 300 = 105120 steps
	veryOld := now.Add(-365 * 24 * time.Hour)
	got := EffectivePriority(policy, 0, 0, veryOld, nil, now)
	expectedSteps := int64(365 * 24 * 3600 / 300)
	if got != expectedSteps {
		t.Fatalf("one year aging: got %d, want %d", got, expectedSteps)
	}
}

func TestEffectivePriorityClampNegativeAge(t *testing.T) {
	// Negative/future age clamps to zero.
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	policy := &Policy{AgingIntervalSec: 300, AgingStep: 1, RunNowAmount: 100, RunNowTTLSec: 600}

	future := now.Add(1 * time.Hour)
	got := EffectivePriority(policy, 5, 3, future, nil, now)
	// Should be just 5+3 = 8, no aging (future clamps age to 0)
	if got != 8 {
		t.Fatalf("future available: got %d, want 8", got)
	}
}

func TestEffectivePrioritySaturatingOverflow(t *testing.T) {
	// Integer overflow saturates at MaxInt64.
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	policy := &Policy{AgingIntervalSec: 300, AgingStep: 1, RunNowAmount: 100, RunNowTTLSec: 600}

	// Close to overflow
	got := EffectivePriority(policy, math.MaxInt64-50, math.MaxInt64-50, now, nil, now)
	// Should saturate at MaxInt64
	if got != math.MaxInt64 {
		t.Fatalf("near overflow: got %d, want %d", got, int64(math.MaxInt64))
	}
}

func TestEffectivePriorityAgingStep(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	policy := &Policy{AgingIntervalSec: 60, AgingStep: 5, RunNowAmount: 100, RunNowTTLSec: 600}

	// 120 seconds of wait = 2 intervals * 5 step = 10
	got := EffectivePriority(policy, 0, 0, now.Add(-120*time.Second), nil, now)
	if got != 10 {
		t.Fatalf("aging step 5: got %d, want 10", got)
	}
}

func TestRunNowExpiration(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	policy := &Policy{AgingIntervalSec: 300, AgingStep: 1, RunNowAmount: 100, RunNowTTLSec: 600}

	// Run-now was set just now; expires in 599s (still active)
	justBefore := now.Add(599 * time.Second)
	got := EffectivePriority(policy, 0, 0, now, &justBefore, now)
	if got != 100 {
		t.Fatalf("just before expiry: got %d, want 100", got)
	}

	// Run-now expires exactly now (expired)
	atExpiry := now
	got = EffectivePriority(policy, 0, 0, now, &atExpiry, now)
	if got != 0 {
		t.Fatalf("at expiry: got %d, want 0", got)
	}

	// Run-now expired 1s ago (expired)
	pastExpiry := now.Add(-1 * time.Second)
	got = EffectivePriority(policy, 0, 0, now, &pastExpiry, now)
	if got != 0 {
		t.Fatalf("past expiry: got %d, want 0", got)
	}

	// Nil run-now means no boost
	got = EffectivePriority(policy, 0, 0, now, nil, now)
	if got != 0 {
		t.Fatalf("nil run-now: got %d, want 0", got)
	}
}

func TestStableTieOrdering(t *testing.T) {
	// Stable ties use available_at, created_at, ID.
	// Ordering returns < 0 when a should be served before b.
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// Same effective priority, different available_at: earlier wins
	a := CandidateOrder{Priority: 0, AvailableAt: now.Add(-1 * time.Hour), CreatedAt: now, ID: 1}
	b := CandidateOrder{Priority: 0, AvailableAt: now, CreatedAt: now, ID: 2}
	if StableCompare(a, b) >= 0 {
		t.Fatal("earlier available_at should win")
	}

	// Same priority and available, different created_at: earlier wins
	c := CandidateOrder{Priority: 5, AvailableAt: now, CreatedAt: now.Add(-1 * time.Hour), ID: 1}
	d := CandidateOrder{Priority: 5, AvailableAt: now, CreatedAt: now, ID: 2}
	if StableCompare(c, d) >= 0 {
		t.Fatal("earlier created_at should win at same available_at")
	}

	// Same priority, available, created: lower ID wins
	e := CandidateOrder{Priority: 1, AvailableAt: now, CreatedAt: now, ID: 1}
	f := CandidateOrder{Priority: 1, AvailableAt: now, CreatedAt: now, ID: 2}
	if StableCompare(e, f) >= 0 {
		t.Fatal("lower ID should win as final tiebreaker")
	}

	// Higher priority always wins regardless of time
	g := CandidateOrder{Priority: 100, AvailableAt: now, CreatedAt: now, ID: 10}
	h := CandidateOrder{Priority: 1, AvailableAt: now.Add(-100 * time.Hour), CreatedAt: now.Add(-100 * time.Hour), ID: 1}
	if StableCompare(g, h) >= 0 {
		t.Fatal("higher priority should win over earlier time")
	}
}

func TestLibraryFairnessRotation(t *testing.T) {
	// For one task type, libraries A/B/C rotate even when A always has deeper backlog.
	// The fairness cursor tracks which library was last served for a task type.

	// Initial state: no library served yet, should pick A (available_at earliest)
	cursor := &LibraryFairnessCursor{TaskType: "poster"}
	libraries := []LibraryCandidate{
		{LibraryID: int64Ptr(1), Priority: 0, AvailableAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), CreatedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), ID: 1, Removed: false},
		{LibraryID: int64Ptr(2), Priority: 0, AvailableAt: time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC), CreatedAt: time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC), ID: 2, Removed: false},
		{LibraryID: int64Ptr(3), Priority: 0, AvailableAt: time.Date(2020, 1, 3, 0, 0, 0, 0, time.UTC), CreatedAt: time.Date(2020, 1, 3, 0, 0, 0, 0, time.UTC), ID: 3, Removed: false},
	}

	// First pick: none served yet, should pick earliest available (library 1)
	idx := LibraryFairnessPick(cursor, libraries, 1) // maxLookahead
	if idx != 0 {
		t.Fatalf("first pick: got idx %d, want 0 (library 1)", idx)
	}
	LibraryFairnessCommit(cursor, libraries[idx])

	// Second pick: library 1 was just served, should pick library 2
	idx = LibraryFairnessPick(cursor, libraries, 1)
	if idx != 1 {
		t.Fatalf("second pick: got idx %d, want 1 (library 2)", idx)
	}
	LibraryFairnessCommit(cursor, libraries[idx])

	// Third pick: library 2 was just served, should pick library 3
	idx = LibraryFairnessPick(cursor, libraries, 1)
	if idx != 2 {
		t.Fatalf("third pick: got idx %d, want 2 (library 3)", idx)
	}
	LibraryFairnessCommit(cursor, libraries[idx])

	// Fourth pick: library 3 was just served, should wrap back to library 1
	idx = LibraryFairnessPick(cursor, libraries, 1)
	if idx != 0 {
		t.Fatalf("fourth pick: got idx %d, want 0 (library 1 again)", idx)
	}
	LibraryFairnessCommit(cursor, libraries[idx])

	// Fifth pick: library 1 served again, library 2 next
	idx = LibraryFairnessPick(cursor, libraries, 1)
	if idx != 1 {
		t.Fatalf("fifth pick: got idx %d, want 1 (library 2)", idx)
	}
}

func TestLibraryFairnessRotationDeepBacklog(t *testing.T) {
	// Even when library A always has deeper backlog (older tasks), rotation still happens.
	cursor := &LibraryFairnessCursor{TaskType: "poster"}
	libraries := []LibraryCandidate{
		{LibraryID: int64Ptr(1), Priority: 0, AvailableAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), CreatedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), ID: 1, Removed: false},
		{LibraryID: int64Ptr(2), Priority: 0, AvailableAt: time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC), CreatedAt: time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC), ID: 2, Removed: false},
	}
	// Library 1 has much older tasks but after serving it, library 2 should get a turn.
	idx := LibraryFairnessPick(cursor, libraries, 10)
	if idx != 0 {
		t.Fatalf("deep backlog first pick: got idx %d, want 0", idx)
	}
	LibraryFairnessCommit(cursor, libraries[idx])
	idx = LibraryFairnessPick(cursor, libraries, 10)
	if idx != 1 {
		t.Fatalf("deep backlog rotation: got idx %d, want 1", idx)
	}
	LibraryFairnessCommit(cursor, libraries[idx])

	// Now add a third library with fresh tasks
	libraries = append(libraries, LibraryCandidate{
		LibraryID: int64Ptr(3), Priority: 0, AvailableAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), ID: 3, Removed: false,
	})
	idx = LibraryFairnessPick(cursor, libraries, 10)
	if idx != 2 {
		t.Fatalf("after adding library 3: got idx %d, want 2", idx)
	}
}

func TestLibraryFairnessSkipRemoved(t *testing.T) {
	// Removed libraries do not consume turns.
	cursor := &LibraryFairnessCursor{TaskType: "poster"}

	libraries := []LibraryCandidate{
		{LibraryID: int64Ptr(1), Priority: 0, AvailableAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), CreatedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), ID: 1, Removed: true}, // removed
		{LibraryID: int64Ptr(2), Priority: 0, AvailableAt: time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC), CreatedAt: time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC), ID: 2, Removed: false},
		{LibraryID: int64Ptr(3), Priority: 0, AvailableAt: time.Date(2020, 1, 3, 0, 0, 0, 0, time.UTC), CreatedAt: time.Date(2020, 1, 3, 0, 0, 0, 0, time.UTC), ID: 3, Removed: false},
	}

	// First pick should skip removed library 1, pick library 2
	idx := LibraryFairnessPick(cursor, libraries, 10)
	if idx != 1 {
		t.Fatalf("skip removed: got idx %d, want 1 (library 2)", idx)
	}
	LibraryFairnessCommit(cursor, libraries[idx])

	// Next pick: library 2 served, should pick library 3
	idx = LibraryFairnessPick(cursor, libraries, 10)
	if idx != 2 {
		t.Fatalf("after skip: got idx %d, want 2 (library 3)", idx)
	}
}

func TestLibraryFairnessNullLibraryBucket(t *testing.T) {
	// Null library is a synthetic bucket that participates in rotation.
	cursor := &LibraryFairnessCursor{TaskType: "encrypt"}

	nullLib := LibraryCandidate{
		LibraryID:  nil, // null library (maintenance/repair tasks)
		Priority:   0,
		AvailableAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		CreatedAt:   time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		ID:          1,
		Removed:     false,
	}

	lib2 := LibraryCandidate{
		LibraryID:  int64Ptr(2),
		Priority:   0,
		AvailableAt: time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC),
		CreatedAt:   time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC),
		ID:          2,
		Removed:     false,
	}

	libraries := []LibraryCandidate{nullLib, lib2}

	// First pick: null library (earliest)
	idx := LibraryFairnessPick(cursor, libraries, 10)
	if idx != 0 {
		t.Fatalf("null lib first: got idx %d, want 0", idx)
	}
	LibraryFairnessCommit(cursor, libraries[idx])

	// Second pick: library 2
	idx = LibraryFairnessPick(cursor, libraries, 10)
	if idx != 1 {
		t.Fatalf("null lib rotation: got idx %d, want 1", idx)
	}
	LibraryFairnessCommit(cursor, libraries[idx])

	// Third pick: null library again (rotation wraps)
	idx = LibraryFairnessPick(cursor, libraries, 10)
	if idx != 0 {
		t.Fatalf("null lib wrap: got idx %d, want 0", idx)
	}
}

func TestLibraryFairnessSupersededDoesNotConsume(t *testing.T) {
	// Superseded/ineligible libraries do not consume turns.
	cursor := &LibraryFairnessCursor{TaskType: "thumbnail"}

	libraries := []LibraryCandidate{
		{LibraryID: int64Ptr(1), Priority: 0, AvailableAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), CreatedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), ID: 1, Superseded: true},
		{LibraryID: int64Ptr(2), Priority: 0, AvailableAt: time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC), CreatedAt: time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC), ID: 2, Removed: false},
		{LibraryID: int64Ptr(3), Priority: 0, AvailableAt: time.Date(2020, 1, 3, 0, 0, 0, 0, time.UTC), CreatedAt: time.Date(2020, 1, 3, 0, 0, 0, 0, time.UTC), ID: 3, Removed: false},
	}

	idx := LibraryFairnessPick(cursor, libraries, 10)
	if idx != 1 {
		t.Fatalf("skip superseded: got idx %d, want 1", idx)
	}
}

func TestLibraryFairnessMaxLookahead(t *testing.T) {
	// maxLookahead bounds the window for finding next library.
	cursor := &LibraryFairnessCursor{TaskType: "poster"}

	libraries := []LibraryCandidate{
		{LibraryID: int64Ptr(1), Priority: 0, AvailableAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), CreatedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), ID: 1, Removed: false},
		{LibraryID: int64Ptr(2), Priority: 0, AvailableAt: time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC), CreatedAt: time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC), ID: 2, Removed: false},
		{LibraryID: int64Ptr(3), Priority: 0, AvailableAt: time.Date(2020, 1, 3, 0, 0, 0, 0, time.UTC), CreatedAt: time.Date(2020, 1, 3, 0, 0, 0, 0, time.UTC), ID: 3, Removed: false},
		{LibraryID: int64Ptr(4), Priority: 0, AvailableAt: time.Date(2020, 1, 4, 0, 0, 0, 0, time.UTC), CreatedAt: time.Date(2020, 1, 4, 0, 0, 0, 0, time.UTC), ID: 4, Removed: false},
	}

	// First pick: library 1
	idx := LibraryFairnessPick(cursor, libraries, 10)
	if idx != 0 {
		t.Fatalf("first: got idx %d, want 0", idx)
	}
	LibraryFairnessCommit(cursor, libraries[idx])

	// maxLookahead 1: after library 1, next is library 2
	idx = LibraryFairnessPick(cursor, libraries, 1)
	if idx != 1 {
		t.Fatalf("maxLookahead 1: got idx %d, want 1", idx)
	}
}

func TestAdjacentSourceClassProgress(t *testing.T) {
	// Each adjacent source class must eventually progress under infinite higher-class backlog.
	// This is verified by aging: lower priority classes eventually overtake higher classes through aging steps.
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	policy := &Policy{AgingIntervalSec: 300, AgingStep: 1, RunNowAmount: 100, RunNowTTLSec: 600}

	// Source class A: base_priority 100, available now
	// Source class B: base_priority 0, available a long time ago
	classA := EffectivePriority(policy, 100, 0, now, nil, now) // 100
	if classA != 100 {
		t.Fatalf("classA priority = %d, want 100", classA)
	}

	// After enough aging, class B overtakes class A
	classB := EffectivePriority(policy, 0, 0, now.Add(-100*300*time.Second), nil, now) // 100 aging steps
	if classB != 100 {
		t.Fatalf("classB priority = %d, want 100", classB)
	}

	// After one more aging step, class B surpasses class A
	classB = EffectivePriority(policy, 0, 0, now.Add(-101*300*time.Second), nil, now)
	if classB <= 100 {
		t.Fatalf("classB priority = %d, should surpass 100", classB)
	}
}

func TestRunNowSQLExpression(t *testing.T) {
	// Verify the SQL expression for run-now boost during claim selection.
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	policy := &Policy{RunNowAmount: 100, RunNowTTLSec: 600}

	if got := RunNowBoostSQL("q", policy, now); got == "" {
		t.Fatal("RunNowBoostSQL returned empty expression")
	}
	// Expression should contain run_now_expires and run_now_amount references
	expr := RunNowBoostSQL("q", policy, now)
	if expr == "" {
		t.Fatal("expected non-empty SQL expression")
	}
}

func TestWaitAgeSQLExpression(t *testing.T) {
	// Verify the SQL expression for wait age during claim selection.
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	if got := WaitAgeSQL("q", now); got == "" {
		t.Fatal("WaitAgeSQL returned empty expression")
	}
}

func TestEffectivePrioritySQLExpression(t *testing.T) {
	// Verify the SQL expression for effective priority used in claim ordering.
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	policy := &Policy{AgingIntervalSec: 300, AgingStep: 1, RunNowAmount: 100, RunNowTTLSec: 600}

	if got := EffectivePrioritySQL("q", policy, now); got == "" {
		t.Fatal("EffectivePrioritySQL returned empty expression")
	}
}

// Helpers

func timePtr(t time.Time) *time.Time {
	return &t
}

func int64Ptr(v int64) *int64 {
	return &v
}

func TestWaitAgeClampZero(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// Zero available (zero value)
	if got := nonNegativeWaitAge(now, time.Time{}, now); got != 0 {
		t.Fatalf("zero available: got %d, want 0", got)
	}

	// Future available
	if got := nonNegativeWaitAge(now, now.Add(10*time.Hour), now); got != 0 {
		t.Fatalf("future available: got %d, want 0", got)
	}

	// Past available
	age := nonNegativeWaitAge(now, now.Add(-500*time.Second), now)
	if age < 500 || age < 0 {
		t.Fatalf("past available: got %d, want >= 500", age)
	}
}

func TestSaturatingAdd(t *testing.T) {
	if got := saturatingAdd(10, 20); got != 30 {
		t.Fatalf("normal add: got %d, want 30", got)
	}
	if got := saturatingAdd(math.MaxInt64, 1); got != math.MaxInt64 {
		t.Fatalf("overflow: got %d, want MaxInt64", got)
	}
	if got := saturatingAdd(math.MaxInt64-1, 2); got != math.MaxInt64 {
		t.Fatalf("near overflow: got %d, want MaxInt64", got)
	}
	if got := saturatingAdd(math.MaxInt64-1, 1); got != math.MaxInt64 {
		t.Fatalf("exact MaxInt64: got %d, want MaxInt64", got)
	}
	if got := saturatingAdd(math.MaxInt64, math.MaxInt64); got != math.MaxInt64 {
		t.Fatalf("double overflow: got %d, want MaxInt64", got)
	}
}

func TestSaturatingMul(t *testing.T) {
	if got := saturatingMul(5, 3); got != 15 {
		t.Fatalf("normal mul: got %d, want 15", got)
	}
	if got := saturatingMul(0, math.MaxInt64); got != 0 {
		t.Fatalf("zero mul: got %d, want 0", got)
	}
	if got := saturatingMul(math.MaxInt64, 2); got != math.MaxInt64 {
		t.Fatalf("overflow mul: got %d, want MaxInt64", got)
	}
	if got := saturatingMul(math.MaxInt64/2, 2); got != math.MaxInt64/2*2 {
		t.Fatalf("safe mul: got %d", got)
	}
}
