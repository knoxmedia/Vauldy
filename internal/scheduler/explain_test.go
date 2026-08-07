package scheduler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Step 1: RED — comprehensive blocker fidelity matrix
// ---------------------------------------------------------------------------

// policyForExplain returns a test policy with known limits.
func policyForExplain() Policy {
	p := PolicyDefaults()
	p.TypeConcurrency["poster"] = 3
	p.ResourceCapacity[CPU] = 10
	p.ResourceCapacity[ExternalProcess] = 3
	p.ProviderCapacity = map[string]int{"provider:test": 2}
	p.AgingIntervalSec = 300
	p.AgingStep = 1
	p.RunNowAmount = 100
	p.RunNowTTLSec = 600
	return p
}

// controlStateDesc returns the control state description for a given state.
func controlStateDesc(state string) string {
	switch state {
	case "paused":
		return `type "poster" is paused`
	case "draining":
		return `type "poster" is draining`
	default:
		return ""
	}
}

func TestExplainTaskRunnable(t *testing.T) {
	row := QueueRow{
		ID:           1,
		TaskType:     "poster",
		Priority:     5,
		BasePriority: 0,
		AvailableAt:  time.Now().Add(-time.Hour),
		CreatedAt:    time.Now().Add(-2 * time.Hour),
		Runnable:     true,
		DependencyMet: true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
	}
	policy := policyForExplain()
	now := time.Now()
	usage := map[ResourceKind]int{CPU: 0}
	pUsage := map[string]int{"provider:test": 0}
	exp := ExplainTask(row, policy, "running", 0, usage, pUsage, 0, now)
	if !exp.Runnable {
		t.Fatalf("expected runnable, got primary=%q", exp.PrimaryBlocker.Code)
	}
	if len(exp.AllBlockers) != 0 {
		t.Fatalf("expected no blockers, got %d: %+v", len(exp.AllBlockers), exp.AllBlockers)
	}
	if exp.EffectivePriority <= 0 {
		t.Fatalf("expected positive effective priority, got %d", exp.EffectivePriority)
	}
}

func TestExplainTaskTerminalRemoved(t *testing.T) {
	row := QueueRow{
		ID:        2,
		TaskType:  "poster",
		Priority:  5,
		Removed:   true,
		Runnable:  true,
		AvailableAt: time.Now().Add(-time.Hour),
		CreatedAt:   time.Now().Add(-2 * time.Hour),
	}
	policy := policyForExplain()
	now := time.Now()
	exp := ExplainTask(row, policy, "running", 0, nil, nil, 0, now)
	if exp.Runnable {
		t.Fatal("expected not runnable for removed row")
	}
	if exp.PrimaryBlocker.Code != BlockerTerminalRemoved {
		t.Fatalf("expected primary blocker terminal_removed, got %q", exp.PrimaryBlocker.Code)
	}
	found := false
	for _, b := range exp.AllBlockers {
		if b.Code == BlockerTerminalRemoved {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("terminal_removed not found in all blockers")
	}
}

func TestExplainTaskSupersededGeneration(t *testing.T) {
	row := QueueRow{
		ID:           3,
		TaskType:     "poster",
		Priority:     5,
		Superseded:   true,
		Runnable:     true,
		DependencyMet: true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  time.Now().Add(-time.Hour),
		CreatedAt:    time.Now().Add(-2 * time.Hour),
	}
	policy := policyForExplain()
	now := time.Now()
	exp := ExplainTask(row, policy, "running", 0, nil, nil, 0, now)
	if exp.Runnable {
		t.Fatal("expected not runnable for superseded row")
	}
	if exp.PrimaryBlocker.Code != BlockerGeneration {
		t.Fatalf("expected primary blocker generation, got %q", exp.PrimaryBlocker.Code)
	}
}

func TestExplainTaskDependencyNotMet(t *testing.T) {
	row := QueueRow{
		ID:           4,
		TaskType:     "poster",
		Priority:     5,
		DependencyMet: false,
		DependencyPermanentlyUnsatisfied: false,
		Runnable:     true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  time.Now().Add(-time.Hour),
		CreatedAt:    time.Now().Add(-2 * time.Hour),
	}
	policy := policyForExplain()
	now := time.Now()
	exp := ExplainTask(row, policy, "running", 0, nil, nil, 0, now)
	if exp.Runnable {
		t.Fatal("expected not runnable when dependency not met")
	}
	if exp.PrimaryBlocker.Code != BlockerDependencyNotMet {
		t.Fatalf("expected dependency_not_met, got %q", exp.PrimaryBlocker.Code)
	}
}

func TestExplainTaskDependencyPermanentlyUnsatisfied(t *testing.T) {
	row := QueueRow{
		ID:           5,
		TaskType:     "poster",
		Priority:     5,
		DependencyMet: false,
		DependencyPermanentlyUnsatisfied: true,
		Runnable:     true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  time.Now().Add(-time.Hour),
		CreatedAt:    time.Now().Add(-2 * time.Hour),
	}
	policy := policyForExplain()
	now := time.Now()
	exp := ExplainTask(row, policy, "running", 0, nil, nil, 0, now)
	if exp.Runnable {
		t.Fatal("expected not runnable when dependency permanently unsatisfied")
	}
	if exp.PrimaryBlocker.Code != BlockerDependencyUnsatisfied {
		t.Fatalf("expected dependency_permanently_unsatisfied, got %q", exp.PrimaryBlocker.Code)
	}
}

func TestExplainTaskBackoff(t *testing.T) {
	future := time.Now().Add(time.Hour)
	row := QueueRow{
		ID:           6,
		TaskType:     "poster",
		Priority:     5,
		BackoffUntil: &future,
		Runnable:     true,
		DependencyMet: true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  time.Now().Add(-time.Hour),
		CreatedAt:    time.Now().Add(-2 * time.Hour),
	}
	policy := policyForExplain()
	now := time.Now()
	exp := ExplainTask(row, policy, "running", 0, nil, nil, 0, now)
	if exp.Runnable {
		t.Fatal("expected not runnable during backoff")
	}
	if exp.PrimaryBlocker.Code != BlockerBackoff {
		t.Fatalf("expected backoff blocker, got %q", exp.PrimaryBlocker.Code)
	}
}

func TestExplainTaskBackoffExpired(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	row := QueueRow{
		ID:           7,
		TaskType:     "poster",
		Priority:     5,
		BackoffUntil: &past,
		Runnable:     true,
		DependencyMet: true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  time.Now().Add(-time.Hour),
		CreatedAt:    time.Now().Add(-2 * time.Hour),
	}
	policy := policyForExplain()
	now := time.Now()
	usage := map[ResourceKind]int{CPU: 0}
	pUsage := map[string]int{"provider:test": 0}
	exp := ExplainTask(row, policy, "running", 0, usage, pUsage, 0, now)
	if !exp.Runnable {
		t.Fatalf("expected runnable after backoff expired, got primary=%q", exp.PrimaryBlocker.Code)
	}
}

func TestExplainTaskNoCapableWorker(t *testing.T) {
	row := QueueRow{
		ID:           8,
		TaskType:     "poster",
		Priority:     5,
		CapableWorker: false,
		Runnable:     true,
		DependencyMet: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  time.Now().Add(-time.Hour),
		CreatedAt:    time.Now().Add(-2 * time.Hour),
	}
	policy := policyForExplain()
	now := time.Now()
	exp := ExplainTask(row, policy, "running", 0, nil, nil, 0, now)
	if exp.Runnable {
		t.Fatal("expected not runnable when no capable worker")
	}
	if exp.PrimaryBlocker.Code != BlockerCapabilitySource {
		t.Fatalf("expected capability_source blocker, got %q", exp.PrimaryBlocker.Code)
	}
}

func TestExplainTaskCiphertextBarrier(t *testing.T) {
	row := QueueRow{
		ID:           9,
		TaskType:     "poster",
		Priority:     5,
		CiphertextReady: false,
		Runnable:     true,
		DependencyMet: true,
		CapableWorker: true,
		SourceReady:   true,
		AvailableAt:  time.Now().Add(-time.Hour),
		CreatedAt:    time.Now().Add(-2 * time.Hour),
	}
	policy := policyForExplain()
	now := time.Now()
	exp := ExplainTask(row, policy, "running", 0, nil, nil, 0, now)
	if exp.Runnable {
		t.Fatal("expected not runnable with ciphertext barrier")
	}
	if exp.PrimaryBlocker.Code != BlockerCiphertextBarrier {
		t.Fatalf("expected ciphertext_barrier blocker, got %q", exp.PrimaryBlocker.Code)
	}
}

func TestExplainTaskSourceBarrier(t *testing.T) {
	row := QueueRow{
		ID:           10,
		TaskType:     "poster",
		Priority:     5,
		SourceReady:  false,
		Runnable:     true,
		DependencyMet: true,
		CapableWorker: true,
		CiphertextReady: true,
		AvailableAt:  time.Now().Add(-time.Hour),
		CreatedAt:    time.Now().Add(-2 * time.Hour),
	}
	policy := policyForExplain()
	now := time.Now()
	exp := ExplainTask(row, policy, "running", 0, nil, nil, 0, now)
	if exp.Runnable {
		t.Fatal("expected not runnable with source barrier")
	}
	if exp.PrimaryBlocker.Code != BlockerCapabilitySource {
		t.Fatalf("expected capability_source blocker for source barrier, got %q", exp.PrimaryBlocker.Code)
	}
}

func TestExplainTaskControlPaused(t *testing.T) {
	row := QueueRow{
		ID:           11,
		TaskType:     "poster",
		Priority:     5,
		Runnable:     true,
		DependencyMet: true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  time.Now().Add(-time.Hour),
		CreatedAt:    time.Now().Add(-2 * time.Hour),
	}
	policy := policyForExplain()
	now := time.Now()
	usage := map[ResourceKind]int{CPU: 0}
	pUsage := map[string]int{"provider:test": 0}
	exp := ExplainTask(row, policy, "paused", 0, usage, pUsage, 0, now)
	if exp.Runnable {
		t.Fatal("expected not runnable when paused")
	}
	if exp.PrimaryBlocker.Code != BlockerControl {
		t.Fatalf("expected control blocker for paused, got %q", exp.PrimaryBlocker.Code)
	}
}

func TestExplainTaskControlDraining(t *testing.T) {
	row := QueueRow{
		ID:           12,
		TaskType:     "poster",
		Priority:     5,
		Runnable:     true,
		DependencyMet: true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  time.Now().Add(-time.Hour),
		CreatedAt:    time.Now().Add(-2 * time.Hour),
	}
	policy := policyForExplain()
	now := time.Now()
	usage := map[ResourceKind]int{CPU: 0}
	pUsage := map[string]int{"provider:test": 0}
	exp := ExplainTask(row, policy, "draining", 0, usage, pUsage, 0, now)
	if exp.Runnable {
		t.Fatal("expected not runnable when draining")
	}
	if exp.PrimaryBlocker.Code != BlockerControl {
		t.Fatalf("expected control blocker for draining, got %q", exp.PrimaryBlocker.Code)
	}
}

func TestExplainTaskTypeExhausted(t *testing.T) {
	row := QueueRow{
		ID:           13,
		TaskType:     "poster",
		Priority:     5,
		Runnable:     true,
		DependencyMet: true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  time.Now().Add(-time.Hour),
		CreatedAt:    time.Now().Add(-2 * time.Hour),
	}
	policy := policyForExplain()
	policy.TypeConcurrency["poster"] = 3
	now := time.Now()
	usage := map[ResourceKind]int{CPU: 0}
	pUsage := map[string]int{"provider:test": 0}
	// 3 active of 3 limit => exhausted.
	exp := ExplainTask(row, policy, "running", 3, usage, pUsage, 0, now)
	if exp.Runnable {
		t.Fatal("expected not runnable when type exhausted")
	}
	if exp.PrimaryBlocker.Code != BlockerTypeExhausted {
		t.Fatalf("expected type_exhausted blocker, got %q", exp.PrimaryBlocker.Code)
	}
	if exp.TypeUsage != 3 || exp.TypeLimit != 3 {
		t.Fatalf("expected type_usage=3 type_limit=3 got usage=%d limit=%d", exp.TypeUsage, exp.TypeLimit)
	}
}

func TestExplainTaskResourceExhausted(t *testing.T) {
	row := QueueRow{
		ID:           14,
		TaskType:     "poster",
		Priority:     5,
		Runnable:     true,
		DependencyMet: true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  time.Now().Add(-time.Hour),
		CreatedAt:    time.Now().Add(-2 * time.Hour),
	}
	policy := policyForExplain()
	policy.ResourceCapacity[ExternalProcess] = 2
	now := time.Now()
	// poster needs 1 external_process; 2 used + 1 would be 3 > 2 capacity
	usage := map[ResourceKind]int{ExternalProcess: 2}
	pUsage := map[string]int{"provider:test": 0}
	exp := ExplainTask(row, policy, "running", 0, usage, pUsage, 0, now)
	if exp.Runnable {
		t.Fatal("expected not runnable when resource exhausted")
	}
	if exp.PrimaryBlocker.Code != BlockerResourceExhausted {
		t.Fatalf("expected resource_exhausted blocker, got %q", exp.PrimaryBlocker.Code)
	}
}

func TestExplainTaskProviderExhausted(t *testing.T) {
	// Register a temporary descriptor with a provider so the provider budget
	// check applies. Restore the original after the test.
	orig := Registry["poster"]
	Registry["poster"] = Descriptor{
		TaskType:       "poster",
		Family:         FamilyPostIngest,
		ProfileVersion: 1,
		Resources:      orig.Resources,
		Provider:       "provider:test",
	}
	defer func() { Registry["poster"] = orig }()

	row := QueueRow{
		ID:           15,
		TaskType:     "poster",
		Priority:     5,
		Runnable:     true,
		DependencyMet: true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  time.Now().Add(-time.Hour),
		CreatedAt:    time.Now().Add(-2 * time.Hour),
	}
	policy := policyForExplain()
	policy.ProviderCapacity["provider:test"] = 1
	now := time.Now()
	usage := map[ResourceKind]int{CPU: 0, ExternalProcess: 0}
	pUsage := map[string]int{"provider:test": 1}
	exp := ExplainTask(row, policy, "running", 0, usage, pUsage, 0, now)
	if exp.Runnable {
		t.Fatal("expected not runnable when provider exhausted")
	}
	if exp.PrimaryBlocker.Code != BlockerProviderExhausted {
		t.Fatalf("expected provider_exhausted blocker, got %q", exp.PrimaryBlocker.Code)
	}
}

func TestExplainTaskFairnessOrder(t *testing.T) {
	row := QueueRow{
		ID:           16,
		TaskType:     "poster",
		Priority:     5,
		Runnable:     true,
		DependencyMet: true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  time.Now().Add(-time.Hour),
		CreatedAt:    time.Now().Add(-2 * time.Hour),
		LibraryID:   int64Ptr(42),
	}
	policy := policyForExplain()
	now := time.Now()
	usage := map[ResourceKind]int{CPU: 0, ExternalProcess: 0}
	pUsage := map[string]int{"provider:test": 0}
	// This library is not the current turn (cursor is at 99).
	exp := ExplainTask(row, policy, "running", 0, usage, pUsage, 99, now)
	if exp.Runnable {
		t.Fatal("expected not runnable when not library turn")
	}
	if exp.PrimaryBlocker.Code != BlockerFairnessOrder {
		t.Fatalf("expected fairness_order blocker, got %q", exp.PrimaryBlocker.Code)
	}
}

func TestExplainTaskFairnessNullLibraryTurn(t *testing.T) {
	// Null library should match when cursor is at null library sentinel (-1).
	row := QueueRow{
		ID:           17,
		TaskType:     "poster",
		Priority:     5,
		Runnable:     true,
		DependencyMet: true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  time.Now().Add(-time.Hour),
		CreatedAt:    time.Now().Add(-2 * time.Hour),
		LibraryID:   nil,
	}
	policy := policyForExplain()
	now := time.Now()
	usage := map[ResourceKind]int{CPU: 0, ExternalProcess: 0}
	pUsage := map[string]int{"provider:test": 0}
	// Cursor at -1 means null library was last served; nil library is not the current turn.
	exp := ExplainTask(row, policy, "running", 0, usage, pUsage, -1, now)
	if exp.Runnable {
		t.Fatal("expected not runnable when not null library turn")
	}
	if exp.PrimaryBlocker.Code != BlockerFairnessOrder {
		t.Fatalf("expected fairness_order blocker for null library, got %q", exp.PrimaryBlocker.Code)
	}
}

// ---------------------------------------------------------------------------
// Blocker fidelity: ordered codes, human-safe details
// ---------------------------------------------------------------------------

func TestBlockerFidelityOrderedCodes(t *testing.T) {
	// Register a temporary descriptor with a provider so the provider budget
	// check applies. Restore the original after the test.
	orig := Registry["poster"]
	Registry["poster"] = Descriptor{
		TaskType:       "poster",
		Family:         FamilyPostIngest,
		ProfileVersion: 1,
		Resources:      orig.Resources,
		Provider:       "provider:test",
	}
	defer func() { Registry["poster"] = orig }()

	row := QueueRow{
		ID:           18,
		TaskType:     "poster",
		Priority:     5,
		Removed:      true,
		Superseded:   true,
		DependencyMet: false,
		BackoffUntil: timePtr(time.Now().Add(time.Hour)),
		CapableWorker: false,
		CiphertextReady: false,
		Runnable:     true,
		AvailableAt:  time.Now().Add(-time.Hour),
		CreatedAt:    time.Now().Add(-2 * time.Hour),
	}
	policy := policyForExplain()
	policy.TypeConcurrency["poster"] = 1
	policy.ResourceCapacity[ExternalProcess] = 1
	policy.ProviderCapacity["provider:test"] = 1
	now := time.Now()
	usage := map[ResourceKind]int{ExternalProcess: 2}
	pUsage := map[string]int{"provider:test": 2}
	exp := ExplainTask(row, policy, "paused", 1, usage, pUsage, 99, now)
	if exp.Runnable {
		t.Fatal("expected not runnable")
	}

	expectedOrder := []BlockerCode{
		BlockerTerminalRemoved,
		BlockerGeneration,
		BlockerDependencyNotMet,
		BlockerBackoff,
		BlockerCapabilitySource,
		BlockerCiphertextBarrier,
		BlockerControl,
		BlockerTypeExhausted,
		BlockerResourceExhausted,
		BlockerProviderExhausted,
		BlockerFairnessOrder,
	}
	if len(exp.AllBlockers) != len(expectedOrder) {
		t.Fatalf("expected %d blockers, got %d: %+v", len(expectedOrder), len(exp.AllBlockers), exp.AllBlockers)
	}
	for i, want := range expectedOrder {
		if exp.AllBlockers[i].Code != want {
			t.Fatalf("blocker[%d]: expected %q, got %q", i, want, exp.AllBlockers[i].Code)
		}
	}
}

func TestBlockerFidelityPrimaryIsFirst(t *testing.T) {
	row := QueueRow{
		ID:           19,
		TaskType:     "poster",
		Priority:     5,
		Removed:      true,
		Superseded:   true,
		Runnable:     true,
		DependencyMet: true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  time.Now().Add(-time.Hour),
		CreatedAt:    time.Now().Add(-2 * time.Hour),
	}
	policy := policyForExplain()
	now := time.Now()
	exp := ExplainTask(row, policy, "running", 0, nil, nil, 0, now)
	if exp.PrimaryBlocker.Code != exp.AllBlockers[0].Code {
		t.Fatalf("primary=%q != first all-blocker=%q", exp.PrimaryBlocker.Code, exp.AllBlockers[0].Code)
	}
}

func TestBlockerFidelityHumanSafeDetails(t *testing.T) {
	row := QueueRow{
		ID:           20,
		TaskType:     "poster",
		Priority:     5,
		DependencyMet: false,
		Runnable:     true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  time.Now().Add(-time.Hour),
		CreatedAt:    time.Now().Add(-2 * time.Hour),
	}
	policy := policyForExplain()
	now := time.Now()
	exp := ExplainTask(row, policy, "running", 0, nil, nil, 0, now)

	// Primary blocker must have a human-readable reason string.
	if strings.TrimSpace(exp.PrimaryBlocker.Reason) == "" {
		t.Fatal("primary blocker has empty reason")
	}
	// Details must be present as a JSON-safe map.
	if exp.PrimaryBlocker.Details == nil {
		t.Fatal("primary blocker has nil details map")
	}
	// Must serialize to valid JSON.
	b, err := json.Marshal(exp)
	if err != nil {
		t.Fatalf("explanation json marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("explanation json unmarshal: %v", err)
	}
	if _, ok := back["primary_blocker"]; !ok {
		t.Fatal("primary_blocker missing from json")
	}
	if _, ok := back["all_blockers"]; !ok {
		t.Fatal("all_blockers missing from json")
	}
	if _, ok := back["runnable"]; !ok {
		t.Fatal("runnable missing from json")
	}
}

// ---------------------------------------------------------------------------
// Claim rank estimation
// ---------------------------------------------------------------------------

func TestClaimRankHigherPriorityWins(t *testing.T) {
	policy := policyForExplain()
	now := time.Now()

	rowA := QueueRow{
		ID:           21,
		TaskType:     "poster",
		Priority:     100,
		BasePriority: 0,
		Runnable:     true,
		DependencyMet: true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  now.Add(-time.Hour),
		CreatedAt:    now.Add(-2 * time.Hour),
	}
	rowB := QueueRow{
		ID:           22,
		TaskType:     "poster",
		Priority:     10,
		BasePriority: 0,
		Runnable:     true,
		DependencyMet: true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  now.Add(-time.Hour),
		CreatedAt:    now.Add(-2 * time.Hour),
	}
	usage := map[ResourceKind]int{CPU: 0, ExternalProcess: 0}
	pUsage := map[string]int{"provider:test": 0}

	expA := ExplainTask(rowA, policy, "running", 0, usage, pUsage, 0, now)
	expB := ExplainTask(rowB, policy, "running", 0, usage, pUsage, 0, now)
	if expA.EffectivePriority <= expB.EffectivePriority {
		t.Fatalf("rowA priority %d should be > rowB %d", expA.EffectivePriority, expB.EffectivePriority)
	}
	// Higher priority means better (lower) estimated rank.
	if expA.EstimatedRank > expB.EstimatedRank {
		t.Fatalf("rowA estimated rank %d should be <= rowB %d", expA.EstimatedRank, expB.EstimatedRank)
	}
}

func TestClaimRankRunNowBoost(t *testing.T) {
	policy := policyForExplain()
	now := time.Now()
	future := now.Add(5 * time.Minute)

	boosted := QueueRow{
		ID:           23,
		TaskType:     "poster",
		Priority:     10,
		BasePriority: 0,
		RunNowExpires: &future,
		Runnable:     true,
		DependencyMet: true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  now.Add(-time.Hour),
		CreatedAt:    now.Add(-2 * time.Hour),
	}
	normal := QueueRow{
		ID:           24,
		TaskType:     "poster",
		Priority:     10,
		BasePriority: 0,
		Runnable:     true,
		DependencyMet: true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  now.Add(-time.Hour),
		CreatedAt:    now.Add(-2 * time.Hour),
	}
	usage := map[ResourceKind]int{CPU: 0, ExternalProcess: 0}
	pUsage := map[string]int{"provider:test": 0}

	expBoost := ExplainTask(boosted, policy, "running", 0, usage, pUsage, 0, now)
	expNorm := ExplainTask(normal, policy, "running", 0, usage, pUsage, 0, now)
	if expBoost.EffectivePriority <= expNorm.EffectivePriority {
		t.Fatalf("boosted priority %d should be > normal %d (run-now)", expBoost.EffectivePriority, expNorm.EffectivePriority)
	}
}

func TestClaimRankRunNowExpired(t *testing.T) {
	policy := policyForExplain()
	now := time.Now()
	past := now.Add(-5 * time.Minute)

	expired := QueueRow{
		ID:           25,
		TaskType:     "poster",
		Priority:     10,
		BasePriority: 0,
		RunNowExpires: &past,
		Runnable:     true,
		DependencyMet: true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  now.Add(-time.Hour),
		CreatedAt:    now.Add(-2 * time.Hour),
	}
	noBoost := QueueRow{
		ID:           26,
		TaskType:     "poster",
		Priority:     10,
		BasePriority: 0,
		Runnable:     true,
		DependencyMet: true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  now.Add(-time.Hour),
		CreatedAt:    now.Add(-2 * time.Hour),
	}
	usage := map[ResourceKind]int{CPU: 0, ExternalProcess: 0}
	pUsage := map[string]int{"provider:test": 0}

	expExp := ExplainTask(expired, policy, "running", 0, usage, pUsage, 0, now)
	expNo := ExplainTask(noBoost, policy, "running", 0, usage, pUsage, 0, now)
	if expExp.EffectivePriority != expNo.EffectivePriority {
		t.Fatalf("expired run-now priority %d should equal no-boost %d", expExp.EffectivePriority, expNo.EffectivePriority)
	}
}

// ---------------------------------------------------------------------------
// Policy/control revision, age, library turn, snapshot point-in-time
// ---------------------------------------------------------------------------

func TestAdmissionExplanationSnapshotHasRevision(t *testing.T) {
	row := QueueRow{
		ID:           27,
		TaskType:     "poster",
		Priority:     5,
		Runnable:     true,
		DependencyMet: true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  time.Now().Add(-time.Hour),
		CreatedAt:    time.Now().Add(-2 * time.Hour),
	}
	policy := policyForExplain()
	now := time.Now()
	usage := map[ResourceKind]int{CPU: 0}
	pUsage := map[string]int{}
	exp := ExplainTask(row, policy, "running", 0, usage, pUsage, 0, now)
	if exp.PolicyRevision != 0 {
		t.Fatalf("policy revision should be 0 (not from DB), got %d", exp.PolicyRevision)
	}
	if exp.ControlRevision != 0 {
		t.Fatalf("control revision should be 0 (not from DB), got %d", exp.ControlRevision)
	}
	if exp.SnapshotAt.IsZero() {
		t.Fatal("snapshot_at must not be zero")
	}
}

func TestAdmissionExplanationAgeSteps(t *testing.T) {
	available := time.Now().Add(-3600 * time.Second) // 1 hour ago
	row := QueueRow{
		ID:           28,
		TaskType:     "poster",
		Priority:     5,
		BasePriority: 0,
		Runnable:     true,
		DependencyMet: true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  available,
		CreatedAt:    available.Add(-time.Hour),
	}
	policy := policyForExplain()
	policy.AgingIntervalSec = 300
	policy.AgingStep = 1
	now := time.Now()
	usage := map[ResourceKind]int{CPU: 0}
	pUsage := map[string]int{}
	exp := ExplainTask(row, policy, "running", 0, usage, pUsage, 0, now)
	if exp.AgeSecs <= 0 {
		t.Fatalf("age should be > 0, got %d", exp.AgeSecs)
	}
	// Aging step should match policy.
	if exp.AgingStep != policy.AgingStep {
		t.Fatalf("aging_step=%d want %d", exp.AgingStep, policy.AgingStep)
	}
}

func TestAdmissionExplanationLibraryTurn(t *testing.T) {
	libID := int64(7)
	row := QueueRow{
		ID:           29,
		TaskType:     "poster",
		Priority:     5,
		LibraryID:   &libID,
		Runnable:     true,
		DependencyMet: true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  time.Now().Add(-time.Hour),
		CreatedAt:    time.Now().Add(-2 * time.Hour),
	}
	policy := policyForExplain()
	now := time.Now()
	usage := map[ResourceKind]int{CPU: 0}
	pUsage := map[string]int{}
	exp := ExplainTask(row, policy, "running", 0, usage, pUsage, 0, now)
	if exp.LibraryTurn != "7" {
		t.Fatalf("library_turn=%q want %q", exp.LibraryTurn, "7")
	}
}

func TestAdmissionExplanationNullLibraryTurn(t *testing.T) {
	row := QueueRow{
		ID:           30,
		TaskType:     "poster",
		Priority:     5,
		LibraryID:   nil,
		Runnable:     true,
		DependencyMet: true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  time.Now().Add(-time.Hour),
		CreatedAt:    time.Now().Add(-2 * time.Hour),
	}
	policy := policyForExplain()
	now := time.Now()
	usage := map[ResourceKind]int{CPU: 0}
	pUsage := map[string]int{}
	exp := ExplainTask(row, policy, "running", 0, usage, pUsage, 0, now)
	if exp.LibraryTurn != "" {
		t.Fatalf("null library_turn=%q want empty string", exp.LibraryTurn)
	}
}

// ---------------------------------------------------------------------------
// Side-effect verification: explain must not claim, reserve, mutate fairness
// ---------------------------------------------------------------------------

func TestAdmissionExplanationNoSideEffect(t *testing.T) {
	// Explain is a pure function: calling it with the same inputs multiple times
	// must produce byte-identical JSON output.
	row := QueueRow{
		ID:           31,
		TaskType:     "poster",
		Priority:     5,
		DependencyMet: false,
		BackoffUntil: timePtr(time.Now().Add(time.Hour)),
		Runnable:     true,
		CapableWorker: true,
		SourceReady:   true,
		CiphertextReady: true,
		AvailableAt:  time.Now().Add(-time.Hour),
		CreatedAt:    time.Now().Add(-2 * time.Hour),
	}
	policy := policyForExplain()
	now := time.Now()
	usage := map[ResourceKind]int{CPU: 0}
	pUsage := map[string]int{}

	exp1 := ExplainTask(row, policy, "running", 0, usage, pUsage, 0, now)
	exp2 := ExplainTask(row, policy, "running", 0, usage, pUsage, 0, now)
	b1, _ := json.Marshal(exp1)
	b2, _ := json.Marshal(exp2)
	if string(b1) != string(b2) {
		t.Fatalf("non-deterministic explanation:\n  call1=%s\n  call2=%s", b1, b2)
	}
}

