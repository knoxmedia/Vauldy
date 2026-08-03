package scheduler

import (
	"fmt"
	"time"
)

// BlockerCode is a deterministic, stable code for an admission blocker.
type BlockerCode string

const (
	// BlockerTerminalRemoved means the row is marked removed/deleted.
	BlockerTerminalRemoved BlockerCode = "terminal_removed"
	// BlockerGeneration means the row belongs to a superseded generation.
	BlockerGeneration BlockerCode = "generation"
	// BlockerDependencyNotMet means one or more upstream dependencies
	// have not completed yet.
	BlockerDependencyNotMet BlockerCode = "dependency_not_met"
	// BlockerDependencyUnsatisfied means the dependency can never be met
	// (e.g. a required ancestor failed or was permanently skipped).
	BlockerDependencyUnsatisfied BlockerCode = "dependency_permanently_unsatisfied"
	// BlockerBackoff means the row is in a backoff window.
	BlockerBackoff BlockerCode = "backoff"
	// BlockerCapabilitySource means no worker is capable of executing
	// this task type, or the source file is not yet ready.
	BlockerCapabilitySource BlockerCode = "capability_source"
	// BlockerCiphertextBarrier means the media ciphertext key is not yet available.
	BlockerCiphertextBarrier BlockerCode = "ciphertext_barrier"
	// BlockerControl means the task type is paused or draining.
	BlockerControl BlockerCode = "control"
	// BlockerTypeExhausted means the task type is at its concurrency limit.
	BlockerTypeExhausted BlockerCode = "type_exhausted"
	// BlockerResourceExhausted means at least one required resource
	// budget would be exceeded.
	BlockerResourceExhausted BlockerCode = "resource_exhausted"
	// BlockerProviderExhausted means the external provider is at capacity.
	BlockerProviderExhausted BlockerCode = "provider_exhausted"
	// BlockerFairnessOrder means the row is not the current turn in
	// library fairness rotation.
	BlockerFairnessOrder BlockerCode = "fairness_order"
)

// blockerPrecedence defines the canonical evaluation order for blockers
// from most to least severe. The first matching blocker is the primary.
var blockerPrecedence = []BlockerCode{
	BlockerTerminalRemoved,
	BlockerGeneration,
	BlockerDependencyNotMet,
	BlockerDependencyUnsatisfied,
	BlockerBackoff,
	BlockerCapabilitySource,
	BlockerCiphertextBarrier,
	BlockerControl,
	BlockerTypeExhausted,
	BlockerResourceExhausted,
	BlockerProviderExhausted,
	BlockerFairnessOrder,
}

// QueueRow describes a candidate task waiting in the admission queue.
// It carries enough state to evaluate all admission blockers without
// mutating the scheduler.
type QueueRow struct {
	ID          int64      `json:"id"`
	TaskType    string     `json:"task_type"`
	Priority    int64      `json:"priority"`
	BasePriority int64     `json:"base_priority"`
	AvailableAt time.Time  `json:"available_at"`
	CreatedAt   time.Time  `json:"created_at"`
	RunNowExpires *time.Time `json:"run_now_expires,omitempty"`
	LibraryID   *int64     `json:"library_id,omitempty"`

	// Terminal states
	Removed    bool `json:"removed"`

	// Generation
	Superseded bool `json:"superseded"`

	// Dependency
	DependencyMet                  bool `json:"dependency_met"`
	DependencyPermanentlyUnsatisfied bool `json:"dependency_permanently_unsatisfied"`

	// Backoff
	BackoffUntil *time.Time `json:"backoff_until,omitempty"`

	// Worker capability: false means no worker can execute this task type.
	CapableWorker bool `json:"capable_worker"`

	// Source / Ciphertext barriers
	SourceReady     bool `json:"source_ready"`
	CiphertextReady bool `json:"ciphertext_ready"`

	// Runnable signal set by the owner. A row that is not runnable is skipped
	// by admission before any checks are evaluated.
	Runnable bool `json:"runnable"`
}

// BlockerDetail records one admission blocker with a human-readable reason
// and optional structured details.
type BlockerDetail struct {
	Code    BlockerCode     `json:"code"`
	Reason  string          `json:"reason"`
	Details map[string]any `json:"details,omitempty"`
}

// Explanation is a point-in-time, read-only snapshot of why a queue row
// cannot be admitted. It includes resource, priority, and fairness context.
type Explanation struct {
	TaskType        string          `json:"task_type"`
	Runnable        bool            `json:"runnable"`
	PrimaryBlocker  BlockerDetail   `json:"primary_blocker"`
	AllBlockers     []BlockerDetail `json:"all_blockers"`
	PolicyRevision  int64           `json:"policy_revision"`
	ControlRevision int64           `json:"control_revision"`
	SnapshotAt      time.Time       `json:"snapshot_at"`

	// Resource context
	RequiredResources map[string]int `json:"required_resources"`
	ResourceUsage     map[string]int `json:"resource_usage"`
	ResourceLimits    map[string]int `json:"resource_limits"`
	TypeUsage         int            `json:"type_usage"`
	TypeLimit         int            `json:"type_limit"`

	// Priority context
	SourcePriority    int64      `json:"source_priority"`
	BasePriority      int64      `json:"base_priority"`
	EffectivePriority int64      `json:"effective_priority"`
	AgeSecs           int64      `json:"age_secs"`
	AgingStep         int        `json:"aging_step"`
	RunNowExpires     *time.Time `json:"run_now_expires,omitempty"`

	// Fairness context
	LibraryTurn   string `json:"library_turn"`
	EstimatedRank int64  `json:"estimated_claim_rank"`
}

// ExplainTask evaluates all admission blockers for a single queue row without
// claiming, reserving, mutating fairness, or changing audit state.
//
// Parameters:
//   - row: the queue row to evaluate
//   - policy: the current effective scheduler policy
//   - controlState: the control state for row.TaskType ("running", "paused", "draining")
//   - typeCount: the current count of active reservations for row.TaskType
//   - resourceUsage: current aggregate resource usage by ResourceKind
//   - providerUsage: current aggregate usage by provider key
//   - fairnessPosition: the fairness cursor position for row.TaskType
//     (library id that was last served, or -1 for null library sentinel)
//   - now: the evaluation timestamp
func ExplainTask(
	row QueueRow,
	policy Policy,
	controlState string,
	typeCount int,
	resourceUsage map[ResourceKind]int,
	providerUsage map[string]int,
	fairnessPosition int64,
	now time.Time,
) Explanation {
	exp := Explanation{
		TaskType:           row.TaskType,
		Runnable:           row.Runnable,
		SnapshotAt:         now,
		RequiredResources:  make(map[string]int),
		ResourceUsage:      make(map[string]int),
		ResourceLimits:     make(map[string]int),
		RunNowExpires:      row.RunNowExpires,
	}
	exp.PrimaryBlocker.Details = map[string]any{}

	// Populate required resources from registry.
	if desc, ok := Registry[row.TaskType]; ok {
		for rk, count := range desc.Resources {
			k := string(rk)
			exp.RequiredResources[k] = count
		}
	}

	// Populate resource usage and limits.
	for rk, used := range resourceUsage {
		exp.ResourceUsage[string(rk)] = used
	}
	for rk, cap := range policy.ResourceCapacity {
		if cap > 0 {
			exp.ResourceLimits[string(rk)] = cap
		}
	}

	// Type concurrency context.
	if limit, ok := policy.TypeConcurrency[row.TaskType]; ok {
		exp.TypeLimit = limit
	}
	exp.TypeUsage = typeCount

	// Priority computation.
	exp.SourcePriority = row.BasePriority + row.Priority
	exp.BasePriority = row.BasePriority
	exp.AgeSecs = nonNegativeWaitAge(now, row.AvailableAt, now)
	exp.AgingStep = policy.AgingStep

	effective := EffectivePriority(
		&policy,
		row.BasePriority,
		row.Priority,
		row.AvailableAt,
		row.RunNowExpires,
		now,
	)
	exp.EffectivePriority = effective

	// Library turn.
	exp.LibraryTurn = LibraryKey(row.LibraryID)

	// Build blockers map keyed by code (a set lookup).
	blockerMap := make(map[BlockerCode]BlockerDetail)

	// 1. Terminal / removed.
	if row.Removed {
		blockerMap[BlockerTerminalRemoved] = BlockerDetail{
			Code:   BlockerTerminalRemoved,
			Reason: "task row is removed",
			Details: map[string]any{
				"row_id":  row.ID,
				"removed": true,
			},
		}
	}

	// 2. Superseded generation.
	if row.Superseded {
		blockerMap[BlockerGeneration] = BlockerDetail{
			Code:   BlockerGeneration,
			Reason: "task belongs to a superseded generation",
			Details: map[string]any{
				"row_id":     row.ID,
				"superseded": true,
			},
		}
	}

	// 3. Dependency — check the stronger condition first.
	if row.DependencyPermanentlyUnsatisfied {
		blockerMap[BlockerDependencyUnsatisfied] = BlockerDetail{
			Code:   BlockerDependencyUnsatisfied,
			Reason: "dependency is permanently unsatisfied",
			Details: map[string]any{
				"row_id":             row.ID,
				"permanently_unsatisfied": true,
			},
		}
	} else if !row.DependencyMet {
		blockerMap[BlockerDependencyNotMet] = BlockerDetail{
			Code:   BlockerDependencyNotMet,
			Reason: "upstream dependency has not completed",
			Details: map[string]any{
				"row_id":          row.ID,
				"dependency_met":  false,
			},
		}
	}

	// 4. Backoff.
	if row.BackoffUntil != nil && row.BackoffUntil.After(now) {
		blockerMap[BlockerBackoff] = BlockerDetail{
			Code:   BlockerBackoff,
			Reason: fmt.Sprintf("task in backoff until %s", row.BackoffUntil.Format(time.RFC3339)),
			Details: map[string]any{
				"row_id":        row.ID,
				"backoff_until": row.BackoffUntil.Format(time.RFC3339),
			},
		}
	}

	// 5. Capability / source barrier.
	if !row.CapableWorker {
		blockerMap[BlockerCapabilitySource] = BlockerDetail{
			Code:   BlockerCapabilitySource,
			Reason: "no capable worker available for this task type",
			Details: map[string]any{
				"row_id":          row.ID,
				"task_type":       row.TaskType,
				"capable_worker":  false,
			},
		}
	} else if !row.SourceReady {
		blockerMap[BlockerCapabilitySource] = BlockerDetail{
			Code:   BlockerCapabilitySource,
			Reason: "source file is not ready",
			Details: map[string]any{
				"row_id":       row.ID,
				"source_ready": false,
			},
		}
	}

	// 6. Ciphertext barrier.
	if !row.CiphertextReady {
		blockerMap[BlockerCiphertextBarrier] = BlockerDetail{
			Code:   BlockerCiphertextBarrier,
			Reason: "ciphertext key is not available",
			Details: map[string]any{
				"row_id":          row.ID,
				"ciphertext_ready": false,
			},
		}
	}

	// 7. Control (paused / draining).
	if controlState == "paused" {
		blockerMap[BlockerControl] = BlockerDetail{
			Code:   BlockerControl,
			Reason: fmt.Sprintf("task type %q is paused", row.TaskType),
			Details: map[string]any{
				"task_type":     row.TaskType,
				"control_state": controlState,
			},
		}
	} else if controlState == "draining" {
		blockerMap[BlockerControl] = BlockerDetail{
			Code:   BlockerControl,
			Reason: fmt.Sprintf("task type %q is draining", row.TaskType),
			Details: map[string]any{
				"task_type":     row.TaskType,
				"control_state": controlState,
			},
		}
	}

	// 8. Type concurrency exhausted.
	if limit, ok := policy.TypeConcurrency[row.TaskType]; ok {
		if limit > 0 && typeCount >= limit {
			blockerMap[BlockerTypeExhausted] = BlockerDetail{
				Code:   BlockerTypeExhausted,
				Reason: fmt.Sprintf("type %q at concurrency limit (%d/%d)", row.TaskType, typeCount, limit),
				Details: map[string]any{
					"task_type": row.TaskType,
					"used":      typeCount,
					"limit":     limit,
				},
			}
		}
	} else {
		// Unknown type with no limit is a subtype of type exhausted.
		blockerMap[BlockerTypeExhausted] = BlockerDetail{
			Code:   BlockerTypeExhausted,
			Reason: fmt.Sprintf("type %q has zero or unknown concurrency limit", row.TaskType),
			Details: map[string]any{
				"task_type": row.TaskType,
				"used":      typeCount,
				"limit":     0,
			},
		}
	}

	// 9. Resource budget exhausted.
	if desc, ok := Registry[row.TaskType]; ok {
		for rk, requested := range desc.Resources {
			capacity, hasCap := policy.ResourceCapacity[rk]
			if !hasCap || capacity <= 0 {
				continue
			}
			used := resourceUsage[rk]
			if used+requested > capacity {
				blockerMap[BlockerResourceExhausted] = BlockerDetail{
					Code:   BlockerResourceExhausted,
					Reason: fmt.Sprintf("resource %q budget exceeded: used %d + requested %d > capacity %d", rk, used, requested, capacity),
					Details: map[string]any{
						"task_type":    row.TaskType,
						"resource":     string(rk),
						"used":         used,
						"requested":    requested,
						"capacity":     capacity,
					},
				}
				break // one resource blocker is sufficient
			}
		}
	}

	// 10. Provider budget exhausted.
	if desc, ok := Registry[row.TaskType]; ok && desc.Provider != "" {
		limit, hasLimit := policy.ProviderCapacity[desc.Provider]
		if hasLimit && limit > 0 {
			used := providerUsage[desc.Provider]
			if used+1 > limit {
				blockerMap[BlockerProviderExhausted] = BlockerDetail{
					Code:   BlockerProviderExhausted,
					Reason: fmt.Sprintf("provider %q at capacity: used %d + 1 > limit %d", desc.Provider, used, limit),
					Details: map[string]any{
						"task_type": row.TaskType,
						"provider":  desc.Provider,
						"used":      used,
						"limit":     limit,
					},
				}
			}
		}
	}

	// 11. Fairness / order.
	if row.LibraryID == nil {
		// Null library; only fair if cursor is not already past the null bucket.
		// If cursor is anything other than nil/fresh (represented as 0 default is tricky), or
		// if cursor is at -1 (null library sentinel), null library is NOT the next turn.
		// Actually, a null library row is only the current turn when cursor hasn't advanced
		// past it — which would mean no fairness cursor exists. In a single-row context,
		// we can't determine exact position, so we flag fairness_order when the cursor
		// appears to point elsewhere.
		// When fairnessPosition > 0 (a real library ID was last served), null library is
		// not the current turn.
		if fairnessPosition > 0 {
			blockerMap[BlockerFairnessOrder] = BlockerDetail{
				Code:   BlockerFairnessOrder,
				Reason: "not the current fairness turn (null library bucket)",
				Details: map[string]any{
					"task_type":          row.TaskType,
					"library_id":         nil,
					"last_served_library": fairnessPosition,
				},
			}
		}
		// When fairnessPosition == -1 (NullLibrarySentinel), null library WAS last served,
		// so it's not our turn. Add a blocker.
		if fairnessPosition == NullLibrarySentinel {
			blockerMap[BlockerFairnessOrder] = BlockerDetail{
				Code:   BlockerFairnessOrder,
				Reason: "not the current fairness turn (null library was last served)",
				Details: map[string]any{
					"task_type":           row.TaskType,
					"library_id":          nil,
					"last_served_library": "null",
				},
			}
		}
	} else {
		// Real library ID: check if it's the current turn.
		libID := *row.LibraryID
		if fairnessPosition != libID && fairnessPosition != 0 {
			blockerMap[BlockerFairnessOrder] = BlockerDetail{
				Code:   BlockerFairnessOrder,
				Reason: fmt.Sprintf("not the current fairness turn (library %d)", libID),
				Details: map[string]any{
					"task_type":          row.TaskType,
					"library_id":         libID,
					"last_served_library": fairnessPosition,
				},
			}
		}
	}

	// Build ordered blocker list from the map using canonical precedence.
	var allBlockers []BlockerDetail
	for _, code := range blockerPrecedence {
		if b, ok := blockerMap[code]; ok {
			allBlockers = append(allBlockers, b)
		}
	}

	if len(allBlockers) > 0 {
		exp.PrimaryBlocker = allBlockers[0]
		exp.Runnable = false
	} else {
		// If row is not runnable but we found no blockers, the row simply isn't ready.
		if !row.Runnable {
			exp.Runnable = false
			exp.PrimaryBlocker = BlockerDetail{
				Code:   BlockerDependencyNotMet,
				Reason: "task is not runnable",
				Details: map[string]any{
					"row_id":   row.ID,
					"runnable": false,
				},
			}
			allBlockers = append(allBlockers, exp.PrimaryBlocker)
		} else {
			// Truly runnable — no blockers.
			exp.Runnable = true
			exp.PrimaryBlocker = BlockerDetail{
				Code:    "",
				Reason:  "task is runnable with no blockers",
				Details: map[string]any{},
			}
		}
	}

	exp.AllBlockers = allBlockers

	// Estimated claim rank: a rough indicator based on effective priority.
	// Higher effective priority maps to lower (better) rank. Since we only
	// have a single row, we project a relative estimate inversely proportional
	// to priority. In multi-row contexts callers should compute the exact
	// position from the queue.
	exp.EstimatedRank = maxInt64(0, 100000-exp.EffectivePriority)

	return exp
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
