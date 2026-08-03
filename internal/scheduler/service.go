package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"knox-media/internal/store"
)

// ClaimResult is an admitted claim returned by the scheduler service.
type ClaimResult struct {
	ExecutionID string
	TaskType    string
	Owner       string
	QueueID     int64
	MediaID     int64
	LeaseUntil  time.Time
	// Payload is the caller's claim payload (uninterpreted by the scheduler).
	// It lets the caller launch the exact admitted unit without a second query.
	Payload any
}

// Claimer admits one claim from a caller-defined work source. It returns
// (nil, nil) when no candidate is currently admissible.
type Claimer func(ctx context.Context, owner string, taskTypes []string) (*ClaimResult, error)

// BudgetSnapshot reports current scheduler usage and limits in the
// compatibility shape consumed by the admin overview.
type BudgetSnapshot struct {
	GlobalLimit, GlobalUsed               int
	PosterLimit, PosterUsed               int
	PreviewLimit, PreviewUsed             int
	SubtitleLimit, SubtitleUsed           int
	ResourceUsage                         map[ResourceKind]int
	ResourceLimits                        map[ResourceKind]int
}

// FallbackRequest asks the scheduler for a fresh admission decision after an
// initial attempt failed (e.g. GPU initialization). The original reservation
// is fenced and the request is admitted only when the fallback resource
// profile fits within current capacity.
type FallbackRequest struct {
	ExecutionID string
	TaskType    string
	Owner       string
	Resources   ResourceRequest
}

// postIngestTypeNames is the canonical poll order for the post-ingest task
// families. The dispatcher asks the service in this order; admission is the
// scheduler's authority, not a dispatcher-owned priority band.
var postIngestTypeNames = []string{
	"poster", "poster_repair", "thumbnail", "preview", "keyframe",
	"subtitle", "subtitle_recognize", "ai_analysis", "atrack", "encrypt",
}

func postIngestType(t string) bool {
	for _, name := range postIngestTypeNames {
		if name == t {
			return true
		}
	}
	return false
}

// Service is the scheduler authority consulted by executors for admission,
// budget snapshots, and fresh fallback admission.
type Service struct {
	db      *sql.DB
	claimer Claimer

	mu     sync.RWMutex
	policy Policy
}

// NewService creates a scheduler Service backed by db.
func NewService(db *sql.DB) *Service {
	return &Service{db: db}
}

// SetPolicy sets the effective policy used for snapshots and fallback
// admission. The dispatcher installs the same policy the queue claims with.
func (s *Service) SetPolicy(p Policy) {
	s.mu.Lock()
	s.policy = p
	s.mu.Unlock()
}

// SetClaimer installs the admission claimer used by Claim.
func (s *Service) SetClaimer(c Claimer) {
	s.mu.Lock()
	s.claimer = c
	s.mu.Unlock()
}

// currentPolicy returns a copy of the effective policy.
func (s *Service) currentPolicy() Policy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.policy
}

// CurrentPolicy returns a copy of the effective policy.
func (s *Service) CurrentPolicy() Policy {
	return s.currentPolicy()
}

// Reload re-reads the active policy revision from the database. When no
// active revision exists the current policy is kept.
func (s *Service) Reload(ctx context.Context) error {
	st := NewStore(s.db)
	rev, err := st.GetActivePolicyRevision(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if rev == nil {
		return nil
	}
	p, err := parsePolicyJSON(rev.PolicyJSON)
	if err != nil {
		return err
	}
	s.SetPolicy(p)
	return nil
}

// Claim asks the scheduler service for the next admitted post-ingest claim.
// The returned reservation is the only unit the caller may launch.
func (s *Service) Claim(ctx context.Context, owner string, taskTypes []string) (*ClaimResult, error) {
	s.mu.RLock()
	c := s.claimer
	s.mu.RUnlock()
	if c == nil {
		return nil, errors.New("scheduler service: no claimer installed")
	}
	return c(ctx, owner, taskTypes)
}

// Snapshot computes usage from durable reservations and limits from the
// effective policy. Global/poster/preview/subtitle rollups cover the
// post-ingest families for the compatibility overview.
func (s *Service) Snapshot(ctx context.Context) (BudgetSnapshot, error) {
	st := NewStore(s.db)
	active, err := st.ListActiveReservations(ctx)
	if err != nil {
		return BudgetSnapshot{}, err
	}
	p := s.currentPolicy()
	snap := BudgetSnapshot{ResourceUsage: map[ResourceKind]int{}, ResourceLimits: map[ResourceKind]int{}}
	for _, res := range active {
		if !postIngestType(res.TaskType) {
			continue
		}
		snap.GlobalUsed++
		switch res.TaskType {
		case "poster", "poster_repair":
			snap.PosterUsed++
		case "preview":
			snap.PreviewUsed++
		case "subtitle", "subtitle_recognize", "ai_analysis":
			snap.SubtitleUsed++
		}
		desc, ok := Registry[res.TaskType]
		if !ok {
			continue
		}
		for rk, count := range desc.Resources {
			snap.ResourceUsage[rk] += count * res.ReservedUnits
		}
	}
	for _, typ := range postIngestTypeNames {
		snap.GlobalLimit += p.TypeConcurrency[typ]
		switch typ {
		case "poster", "poster_repair":
			snap.PosterLimit += p.TypeConcurrency[typ]
		case "preview":
			snap.PreviewLimit += p.TypeConcurrency[typ]
		case "subtitle", "subtitle_recognize", "ai_analysis":
			snap.SubtitleLimit += p.TypeConcurrency[typ]
		}
	}
	for rk, cap := range p.ResourceCapacity {
		if cap > 0 {
			snap.ResourceLimits[rk] = cap
		}
	}
	return snap, nil
}

const fallbackPollInterval = 50 * time.Millisecond

// AcquireFallback fences the original reservation and waits for a fresh
// admission decision using req.Resources. It returns the newly admitted
// reservation once the fallback profile fits within current capacity, or an
// error when the request is invalid or the context is cancelled.
func (s *Service) AcquireFallback(ctx context.Context, req FallbackRequest) (*Reservation, error) {
	executionID := strings.TrimSpace(req.ExecutionID)
	if executionID == "" {
		return nil, errors.New("scheduler service: fallback execution id is required")
	}
	if strings.TrimSpace(req.TaskType) == "" {
		return nil, errors.New("scheduler service: fallback task type is required")
	}
	if strings.TrimSpace(req.Owner) == "" {
		return nil, errors.New("scheduler service: fallback owner is required")
	}
	if len(req.Resources) == 0 {
		return nil, errors.New("scheduler service: fallback must request resources")
	}
	if err := ValidateResourceRequest(req.Resources); err != nil {
		return nil, fmt.Errorf("scheduler service: fallback resources: %w", err)
	}

	st := NewStore(s.db)
	// Fence the original attempt so its reservation no longer holds capacity.
	if err := st.ReleaseReservation(ctx, executionID, "gpu_fallback_fence", strings.TrimSpace(req.Owner)); err != nil {
		return nil, fmt.Errorf("scheduler service: fence original attempt: %w", err)
	}

	policy := s.currentPolicy()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		res, err := s.tryAdmitFallback(ctx, req, policy)
		if err == nil {
			return res, nil
		}
		if !errors.Is(err, errFallbackPending) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(fallbackPollInterval):
		}
	}
}

var errFallbackPending = errors.New("scheduler service: fallback pending capacity")

// tryAdmitFallback attempts one atomic fallback admission. It returns
// errFallbackPending when capacity is unavailable and the caller should retry.
func (s *Service) tryAdmitFallback(ctx context.Context, req FallbackRequest, policy Policy) (*Reservation, error) {
	st := NewStore(s.db)
	var reserved *Reservation
	err := store.WithBusyRetry(ctx, nil, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()

		now := time.Now()
		if blocker, err := CheckTypeConcurrency(ctx, tx, req.TaskType, policy, now); err != nil {
			return err
		} else if blocker != nil {
			return errFallbackPending
		}
		if blocker, err := checkResourceRequestBudget(ctx, tx, req.TaskType, req.Resources, policy, now); err != nil {
			return err
		} else if blocker != nil {
			return errFallbackPending
		}

		var revID sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM scheduler_policy_revision WHERE is_active=1`).Scan(&revID); err != nil {
			return fmt.Errorf("scheduler service: no active policy revision: %w", err)
		}
		executionID := GenerateExecutionID(strings.TrimSpace(req.Owner))
		if _, err := InsertAdmissionReservation(ctx, tx, executionID, req.TaskType, 1, revID.Int64, now.Add(90*time.Second)); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		reserved, err = st.GetReservation(ctx, executionID)
		return err
	})
	if err != nil {
		return nil, err
	}
	if reserved == nil {
		return nil, fmt.Errorf("scheduler service: fallback reservation not found after insert")
	}
	return reserved, nil
}

// checkResourceRequestBudget verifies that the given resource request fits
// within policy capacity given currently active reservations.
func checkResourceRequestBudget(ctx context.Context, tx store.SQLExecutor, taskType string, requested ResourceRequest, policy Policy, now time.Time) (*AdmissionBlocker, error) {
	usage, err := ActiveResourceUsage(ctx, tx, now)
	if err != nil {
		return nil, err
	}
	for rk, request := range requested {
		capacity, hasCap := policy.ResourceCapacity[rk]
		if !hasCap || capacity <= 0 {
			continue
		}
		used := usage[rk]
		if used+request > capacity {
			return &AdmissionBlocker{
				Reason:   fmt.Sprintf("resource %q budget exceeded: used %d + requested %d > capacity %d", rk, used, request, capacity),
				TaskType: taskType,
			}, nil
		}
	}
	return nil, nil
}

type persistedPolicyJSON struct {
	TypeConcurrency  map[string]int `json:"type_concurrency"`
	ResourceCapacity map[string]int `json:"resource_capacity"`
	ProviderCapacity map[string]int `json:"provider_capacity"`
	AgingIntervalSec int            `json:"aging_interval_sec"`
	AgingStep        int            `json:"aging_step"`
	RunNowAmount     int            `json:"run_now_amount"`
	RunNowTTLSec     int            `json:"run_now_ttl_sec"`
}

// parsePolicyJSON rebuilds a Policy from a persisted revision payload.
func parsePolicyJSON(raw string) (Policy, error) {
	var j persistedPolicyJSON
	if err := json.Unmarshal([]byte(raw), &j); err != nil {
		return Policy{}, fmt.Errorf("scheduler service: parse policy revision: %w", err)
	}
	p := PolicyDefaults()
	for name, limit := range j.TypeConcurrency {
		if _, ok := Registry[name]; ok && limit >= 0 {
			p.TypeConcurrency[name] = limit
		}
	}
	p.ResourceCapacity = make(map[ResourceKind]int)
	for rk, cap := range j.ResourceCapacity {
		if _, ok := AllResourceKinds[ResourceKind(rk)]; ok && cap >= 0 {
			p.ResourceCapacity[ResourceKind(rk)] = cap
		}
	}
	p.ProviderCapacity = j.ProviderCapacity
	if j.AgingIntervalSec > 0 {
		p.AgingIntervalSec = j.AgingIntervalSec
	}
	if j.AgingStep > 0 {
		p.AgingStep = j.AgingStep
	}
	if j.RunNowAmount > 0 {
		p.RunNowAmount = j.RunNowAmount
	}
	if j.RunNowTTLSec > 0 {
		p.RunNowTTLSec = j.RunNowTTLSec
	}
	return p, nil
}

// EncodePolicyJSON serializes a policy into the persisted revision payload
// shape consumed by parsePolicyJSON and the scheduler store.
func EncodePolicyJSON(p Policy) (string, error) {
	rc := make(map[string]int, len(p.ResourceCapacity))
	for rk, cap := range p.ResourceCapacity {
		rc[string(rk)] = cap
	}
	raw, err := json.Marshal(persistedPolicyJSON{
		TypeConcurrency:  p.TypeConcurrency,
		ResourceCapacity: rc,
		ProviderCapacity: p.ProviderCapacity,
		AgingIntervalSec: p.AgingIntervalSec,
		AgingStep:        p.AgingStep,
		RunNowAmount:     p.RunNowAmount,
		RunNowTTLSec:     p.RunNowTTLSec,
	})
	if err != nil {
		return "", fmt.Errorf("scheduler service: encode policy revision: %w", err)
	}
	return string(raw), nil
}
