package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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

	mu         sync.RWMutex
	policy     Policy
	basePolicy Policy
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

// SetBasePolicy installs the YAML-effective policy layer (compiled defaults
// merged with config.yml) that runtime database overrides are applied on top
// of. Clearing an override falls back to this layer.
func (s *Service) SetBasePolicy(p Policy) {
	s.mu.Lock()
	s.basePolicy = p
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

// Reload re-reads the active policy revision from the database and rebuilds the
// effective policy by layering any persisted runtime overrides over the base
// YAML policy. When no active revision exists the current policy is kept.
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
	var j persistedPolicyJSON
	if err := json.Unmarshal([]byte(rev.PolicyJSON), &j); err != nil {
		return fmt.Errorf("scheduler service: parse policy revision: %w", err)
	}
	s.mu.RLock()
	base := s.basePolicy
	s.mu.RUnlock()
	if len(base.TypeConcurrency) == 0 {
		// No base layer installed: the revision payload is the effective policy.
		s.SetPolicy(policyFromRevisionJSON(j))
		return nil
	}
	s.SetPolicy(rebuildEffective(base, j))
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
	// Overrides is the DB concurrency override layer applied over the base
	// YAML policy. Absent/empty means the base layer is authoritative.
	Overrides map[string]int `json:"overrides,omitempty"`
}

// policyFromRevisionJSON rebuilds a Policy from a persisted revision payload
// using the full effective maps.
func policyFromRevisionJSON(j persistedPolicyJSON) Policy {
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
	p.Overrides = maps.Clone(j.Overrides)
	for typ := range p.Overrides {
		if _, ok := Registry[typ]; ok {
			p.Provenance["concurrency."+typ] = "override"
		}
	}
	return p
}

// parsePolicyJSON rebuilds a Policy from a persisted revision payload.
func parsePolicyJSON(raw string) (Policy, error) {
	var j persistedPolicyJSON
	if err := json.Unmarshal([]byte(raw), &j); err != nil {
		return Policy{}, fmt.Errorf("scheduler service: parse policy revision: %w", err)
	}
	return policyFromRevisionJSON(j), nil
}

// rebuildEffective layers a persisted revision payload over the base YAML
// policy. Concurrency overrides are reapplied over the base values, so a
// cleared override falls back to YAML; resource/provider/priority values are
// applied over the base and labelled override only when they differ from it.
func rebuildEffective(base Policy, j persistedPolicyJSON) Policy {
	p := base.clone()
	p.Overrides = maps.Clone(j.Overrides)
	for typ, val := range j.Overrides {
		if _, ok := Registry[typ]; !ok || val < 0 {
			continue
		}
		p.TypeConcurrency[typ] = val
		p.Provenance["concurrency."+typ] = "override"
	}
	for rk, cap := range j.ResourceCapacity {
		rkind := ResourceKind(rk)
		if _, ok := AllResourceKinds[rkind]; !ok || cap < 0 {
			continue
		}
		p.ResourceCapacity[rkind] = cap
		if baseCap, ok := base.ResourceCapacity[rkind]; !ok || baseCap != cap {
			p.Provenance["resource."+rk] = "override"
		}
	}
	for pk, cap := range j.ProviderCapacity {
		if cap < 0 {
			continue
		}
		p.ProviderCapacity[pk] = cap
		if baseCap, ok := base.ProviderCapacity[pk]; !ok || baseCap != cap {
			p.Provenance["provider."+pk] = "override"
		}
	}
	layerPriority := func(key string, val, baseVal int) {
		if val > 0 && val != baseVal {
			p.Provenance[key] = "override"
		}
	}
	if j.AgingIntervalSec > 0 {
		p.AgingIntervalSec = j.AgingIntervalSec
		layerPriority("priority.aging_interval_sec", j.AgingIntervalSec, base.AgingIntervalSec)
	}
	if j.AgingStep > 0 {
		p.AgingStep = j.AgingStep
		layerPriority("priority.aging_step", j.AgingStep, base.AgingStep)
	}
	if j.RunNowAmount > 0 {
		p.RunNowAmount = j.RunNowAmount
		layerPriority("priority.run_now_amount", j.RunNowAmount, base.RunNowAmount)
	}
	if j.RunNowTTLSec > 0 {
		p.RunNowTTLSec = j.RunNowTTLSec
		layerPriority("priority.run_now_ttl_sec", j.RunNowTTLSec, base.RunNowTTLSec)
	}
	return p
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
		Overrides:        p.Overrides,
	})
	if err != nil {
		return "", fmt.Errorf("scheduler service: encode policy revision: %w", err)
	}
	return string(raw), nil
}

// RuntimeOverrideRequest is a revisioned runtime policy update. ExpectedRevision
// must equal the currently active policy revision id (or -1 when no active
// revision may exist). Replace performs a PUT (the Concurrency map becomes the
// entire override layer); otherwise the request behaves as a PATCH that merges
// Concurrency and removes the listed ClearConcurrency overrides.
type RuntimeOverrideRequest struct {
	ExpectedRevision int64
	Replace          bool
	Concurrency      map[string]int
	ClearConcurrency []string
	ResourceCapacity map[string]int
	ProviderCapacity map[string]int
	AgingIntervalSec *int
	AgingStep        *int
	RunNowAmount     *int
	RunNowTTLSec     *int
	Author           string
	Reason           string
}

// RuntimeOverrideResult reports the durable outcome of an applied override.
type RuntimeOverrideResult struct {
	RevisionID int64
	Policy     Policy
}

// applyConcurrencyOverridesLayer replaces the override layer on a policy copy
// and re-applies it over the policy's current concurrency values.
func applyConcurrencyOverridesLayer(p Policy, overrides map[string]int) Policy {
	p.Overrides = maps.Clone(overrides)
	for typ, val := range overrides {
		if _, ok := Registry[typ]; !ok || val < 0 {
			continue
		}
		p.TypeConcurrency[typ] = val
		p.Provenance["concurrency."+typ] = "override"
	}
	return p
}

func normalizeRuntimeOverrideRequest(req RuntimeOverrideRequest) RuntimeOverrideRequest {
	req.Author = strings.TrimSpace(req.Author)
	if req.Author == "" {
		req.Author = "system"
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" {
		req.Reason = "runtime override"
	}
	return req
}

// ApplyRuntimeOverride validates and durably activates a revisioned runtime
// override. Validation failure returns a ValidationError and activates nothing;
// a stale ExpectedRevision returns a RevisionConflictError. On success a new
// policy revision is created, activated, and audited in one transaction before
// the in-memory policy is swapped, and the newly active revision id is returned.
func (s *Service) ApplyRuntimeOverride(ctx context.Context, req RuntimeOverrideRequest) (RuntimeOverrideResult, error) {
	req = normalizeRuntimeOverrideRequest(req)
	st := NewStore(s.db)

	active, err := st.GetActivePolicyRevision(ctx)
	currentID := int64(-1)
	if err == nil && active != nil {
		currentID = active.ID
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return RuntimeOverrideResult{}, fmt.Errorf("scheduler service: read active revision: %w", err)
	}
	if req.ExpectedRevision != currentID {
		return RuntimeOverrideResult{}, RevisionConflictError{Expected: req.ExpectedRevision, Current: currentID}
	}

	s.mu.RLock()
	base := s.basePolicy
	current := s.policy
	s.mu.RUnlock()
	if len(base.TypeConcurrency) == 0 {
		base = current
	}

	newOverrides := make(map[string]int, len(current.Overrides)+len(req.Concurrency)+len(req.ClearConcurrency))
	if req.Replace {
		for k, v := range req.Concurrency {
			newOverrides[k] = v
		}
	} else {
		for k, v := range current.Overrides {
			newOverrides[k] = v
		}
		for k, v := range req.Concurrency {
			newOverrides[k] = v
		}
		for _, k := range req.ClearConcurrency {
			delete(newOverrides, k)
		}
	}
	for k := range newOverrides {
		if _, ok := Registry[k]; !ok {
			delete(newOverrides, k)
		}
	}

	// Reject invalid requested values before any activation: unknown types and
	// negative limits are validation errors, not silent no-ops.
	var vErrs []string
	for k, v := range req.Concurrency {
		if _, ok := Registry[k]; !ok {
			vErrs = append(vErrs, fmt.Sprintf("unknown task type %q", k))
		} else if v < 0 {
			vErrs = append(vErrs, fmt.Sprintf("concurrency %q cannot be negative: %d", k, v))
		}
	}
	if req.ResourceCapacity != nil {
		for rk, cap := range req.ResourceCapacity {
			if _, ok := AllResourceKinds[ResourceKind(rk)]; !ok {
				vErrs = append(vErrs, fmt.Sprintf("unknown resource kind %q", rk))
			} else if cap < 0 {
				vErrs = append(vErrs, fmt.Sprintf("resource %q cannot be negative: %d", rk, cap))
			}
		}
	}
	if req.ProviderCapacity != nil {
		for pk, cap := range req.ProviderCapacity {
			if cap < 0 {
				vErrs = append(vErrs, fmt.Sprintf("provider %q cannot be negative: %d", pk, cap))
			}
		}
	}
	if len(vErrs) > 0 {
		return RuntimeOverrideResult{}, ValidationError{Errors: vErrs}
	}

	p := applyConcurrencyOverridesLayer(base.clone(), newOverrides)
	if req.ResourceCapacity != nil {
		rc := make(map[ResourceKind]int, len(req.ResourceCapacity))
		for rk, cap := range req.ResourceCapacity {
			rkind := ResourceKind(rk)
			if _, ok := AllResourceKinds[rkind]; !ok || cap < 0 {
				continue
			}
			rc[rkind] = cap
			p.Provenance["resource."+rk] = "override"
		}
		p.ResourceCapacity = rc
	}
	if req.ProviderCapacity != nil {
		pc := make(map[string]int, len(req.ProviderCapacity))
		for pk, cap := range req.ProviderCapacity {
			if cap < 0 {
				continue
			}
			pc[pk] = cap
			p.Provenance["provider."+pk] = "override"
		}
		p.ProviderCapacity = pc
	}
	if req.AgingIntervalSec != nil {
		p.AgingIntervalSec = *req.AgingIntervalSec
		p.Provenance["priority.aging_interval_sec"] = "override"
	}
	if req.AgingStep != nil {
		p.AgingStep = *req.AgingStep
		p.Provenance["priority.aging_step"] = "override"
	}
	if req.RunNowAmount != nil {
		p.RunNowAmount = *req.RunNowAmount
		p.Provenance["priority.run_now_amount"] = "override"
	}
	if req.RunNowTTLSec != nil {
		p.RunNowTTLSec = *req.RunNowTTLSec
		p.Provenance["priority.run_now_ttl_sec"] = "override"
	}

	if err := p.Validate(); err != nil {
		return RuntimeOverrideResult{}, ValidationError{Errors: []string{err.Error()}}
	}

	raw, err := EncodePolicyJSON(p)
	if err != nil {
		return RuntimeOverrideResult{}, err
	}
	detail, _ := json.Marshal(map[string]any{
		"expected_revision": currentID,
		"author":            req.Author,
		"reason":            req.Reason,
	})
	rev, err := st.ApplyPolicyRevision(ctx, ApplyPolicyRevisionParams{
		SchemaVersion:    1,
		PolicyJSON:       raw,
		Author:           req.Author,
		Reason:           req.Reason,
		ValidationHash:   "runtime",
		ExpectedRevision: currentID,
		AuditEventType:   "scheduler.policy_update",
		AuditDetailJSON:  string(detail),
	})
	if err != nil {
		var rc RevisionConflictError
		if errors.As(err, &rc) {
			return RuntimeOverrideResult{}, rc
		}
		return RuntimeOverrideResult{}, fmt.Errorf("scheduler service: apply revision: %w", err)
	}
	s.SetPolicy(p)
	return RuntimeOverrideResult{RevisionID: rev.ID, Policy: p}, nil
}

// PolicyStatus reports the active policy revision provenance plus the current
// effective policy.
type PolicyStatus struct {
	Revision    int64
	Active      bool
	Actor       string
	Reason      string
	ActivatedAt *time.Time
	Policy      Policy
}

// PolicyStatus returns the active policy revision metadata and the current
// effective policy.
func (s *Service) PolicyStatus(ctx context.Context) (PolicyStatus, error) {
	st := NewStore(s.db)
	rev, err := st.GetActivePolicyRevision(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PolicyStatus{Policy: s.currentPolicy()}, nil
		}
		return PolicyStatus{}, err
	}
	if rev == nil {
		return PolicyStatus{Policy: s.currentPolicy()}, nil
	}
	return PolicyStatus{
		Revision:    rev.ID,
		Active:      rev.IsActive,
		Actor:       rev.Author,
		Reason:      rev.Reason,
		ActivatedAt: rev.ActivatedAt,
		Policy:      s.currentPolicy(),
	}, nil
}

// ControlResult reports the outcome of a control command.
type ControlResult struct {
	TaskType         string
	State            string
	Revision         int64
	LiveReservations int
	Drained          bool
	Actor            string
	Reason           string
}

// Control transitions the control state for a task type: "pause" to paused,
// "resume" to running, and "drain" to draining (paused immediately when no live
// reservations remain). Repeating the same command is an idempotent no-op that
// keeps the current revision. A non-negative expectedRevision must match the
// current control revision or a ControlConflictError is returned. The state
// transition and audit are persisted before the result is returned.
func (s *Service) Control(ctx context.Context, taskType, command string, expectedRevision int64, actor, reason string) (ControlResult, error) {
	taskType = strings.TrimSpace(taskType)
	command = strings.TrimSpace(command)
	if _, ok := Registry[taskType]; !ok {
		return ControlResult{}, fmt.Errorf("scheduler service: unknown task type %q", taskType)
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		actor = "system"
	}
	reason = strings.TrimSpace(reason)

	st := NewStore(s.db)
	current, err := st.GetControlState(ctx, taskType)
	curState := ""
	curRevision := int64(0)
	if err == nil && current != nil {
		curState = current.State
		curRevision = int64(current.Revision)
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ControlResult{}, fmt.Errorf("scheduler service: read control state: %w", err)
	}

	live, err := ActiveReservationCount(ctx, s.db, taskType, time.Now())
	if err != nil {
		return ControlResult{}, fmt.Errorf("scheduler service: live reservations: %w", err)
	}

	target := ""
	switch command {
	case "pause":
		target = "paused"
	case "resume":
		target = "running"
	case "drain":
		if live == 0 {
			target = "paused"
		} else {
			target = "draining"
		}
	default:
		return ControlResult{}, fmt.Errorf("scheduler service: unknown control command %q", command)
	}

	if curState == target {
		return ControlResult{
			TaskType:         taskType,
			State:            curState,
			Revision:         curRevision,
			LiveReservations: live,
			Drained:          curState != "running" && live == 0,
			Actor:            actor,
			Reason:           reason,
		}, nil
	}
	if expectedRevision >= 0 && expectedRevision != curRevision {
		return ControlResult{}, ControlConflictError{Expected: expectedRevision, Current: curRevision}
	}
	if err := st.SetControlState(ctx, taskType, target); err != nil {
		return ControlResult{}, fmt.Errorf("scheduler service: set control state: %w", err)
	}
	fresh, err := st.GetControlState(ctx, taskType)
	if err != nil {
		return ControlResult{}, fmt.Errorf("scheduler service: read control state: %w", err)
	}
	detail, _ := json.Marshal(map[string]any{
		"task_type": taskType,
		"command":   command,
		"state":     fresh.State,
		"revision":  fresh.Revision,
		"reason":    reason,
	})
	if _, err := st.RecordAudit(ctx, "scheduler.control", actor, string(detail)); err != nil {
		return ControlResult{}, fmt.Errorf("scheduler service: control audit: %w", err)
	}
	return ControlResult{
		TaskType:         taskType,
		State:            fresh.State,
		Revision:         int64(fresh.Revision),
		LiveReservations: live,
		Drained:          fresh.State != "running" && live == 0,
		Actor:            actor,
		Reason:           reason,
	}, nil
}
