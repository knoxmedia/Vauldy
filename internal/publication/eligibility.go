package publication

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"knox-media/internal/coreiface"
	"knox-media/internal/scheduler"
	"knox-media/internal/store"
)

type QueueFamily string

const (
	QueuePostIngest QueueFamily = "post_ingest"
	QueueScrape     QueueFamily = "scrape"
	QueuePrepare    QueueFamily = "prepare"
)

type ClaimPayload struct {
	Family                                QueueFamily
	QueueID, MediaID                      int64
	RunID, StepID, ScanTaskID, Generation sql.NullInt64
	TaskType, Owner                       string
	Attempts, MaxAttempts, RetryRound     int
	LeaseUntil                            time.Time
	SourceClass, BasePriority             int
	LibraryID                             sql.NullInt64
	ResourceProfileVersion                int
	ResourceProfileJSON                   string
}

type ClaimRequest struct {
	Family          QueueFamily
	TaskType, Owner string
	TaskTypes       []string
	QueueID         *int64
	Registry        coreiface.CapabilityRegistry
	Metrics         *store.SQLiteMetrics
	SchedulerPolicy *scheduler.Policy
	afterCommit     func() error
}

type PrepareParentIdentity struct {
	TaskID, RunID, StepID, MediaID, Generation int64
	Owner                                      string
	RetryRound                                 int
}

var ErrClaimCommitUncertain = errors.New("publication claim commit outcome uncertain")

func ClaimEligibleAny(ctx context.Context, db *sql.DB, req ClaimRequest) (*ClaimPayload, error) {
	if req.Family != QueuePostIngest || len(req.TaskTypes) == 0 {
		return nil, errors.New("publication claim any: post-ingest task types are required")
	}
	seen := make(map[string]bool, len(req.TaskTypes))
	filtered := make([]string, 0, len(req.TaskTypes))
	for _, typ := range req.TaskTypes {
		typ = strings.TrimSpace(typ)
		if typ == "" || seen[typ] || req.Registry == nil || !req.Registry.Available(typ) {
			continue
		}
		seen[typ] = true
		filtered = append(filtered, typ)
	}
	if len(filtered) == 0 {
		return nil, nil
	}
	req.TaskTypes = filtered
	if ok, err := postIngestCandidateHint(ctx, db, req); err != nil || !ok {
		return nil, err
	}
	return claimEligible(ctx, db, req, true)
}

func ClaimEligible(ctx context.Context, db *sql.DB, req ClaimRequest) (*ClaimPayload, error) {
	return claimEligible(ctx, db, req, false)
}

func claimEligible(ctx context.Context, db *sql.DB, req ClaimRequest, any bool) (*ClaimPayload, error) {
	if db == nil || strings.TrimSpace(req.Owner) == "" {
		return nil, errors.New("publication claim: invalid database or owner")
	}
	var payload *ClaimPayload
	ownerToken := strings.TrimSpace(req.Owner) + "/" + uuid.NewString()
	policy := store.RetryPolicy{Operation: "publication_claim", MaxElapsed: 2 * time.Second, BaseBackoff: 10 * time.Millisecond, MaxBackoff: 100 * time.Millisecond}
	err := store.WithBusyRetryPolicyContext(ctx, nil, policy, func(attempt context.Context) error {
		payload = nil
		req.Metrics.RecordImmediateTransaction()
		outcome, err := store.WithImmediateConnTx(attempt, db, func(tx store.ImmediateConnTx) error {
			var inner error
			if any {
				payload, inner = claimEligibleAnyTx(attempt, tx, req, ownerToken)
			} else {
				payload, inner = claimEligibleTx(attempt, tx, req, ownerToken)
			}
			return inner
		})
		if err != nil && outcome.CommitAttempted {
			return fmt.Errorf("%w: %v", ErrClaimCommitUncertain, err)
		}
		if err == nil && outcome.CommitConfirmed && payload != nil && req.afterCommit != nil {
			if hookErr := req.afterCommit(); hookErr != nil {
				return fmt.Errorf("%w: %v", ErrClaimCommitUncertain, hookErr)
			}
		}
		return err
	})
	if errors.Is(err, ErrClaimCommitUncertain) {
		reconciled, reconcileErr := claimByOwner(ctx, db, req.Family, ownerToken)
		if reconcileErr == nil && reconciled != nil {
			return reconciled, nil
		}
		return nil, err
	}
	return payload, err
}

func tableAvailable(ctx context.Context, tx store.SQLExecutor, table string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n)
	return n == 1, err
}

func sqlPlaceholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func postIngestCandidateHint(ctx context.Context, db *sql.DB, req ClaimRequest) (bool, error) {
	args := make([]any, len(req.TaskTypes))
	for i, typ := range req.TaskTypes {
		args[i] = typ
	}
	query := fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM post_ingest_task q LEFT JOIN media_ingest_step st ON st.id=q.ingest_step_id WHERE %s AND q.task_type IN (%s) AND (%s OR (%s) OR (q.task_type='poster_repair' AND q.ingest_run_id IS NOT NULL AND q.ingest_step_id IS NULL AND q.generation>0 AND EXISTS(SELECT 1 FROM media m JOIN media_ingest_run r ON r.id=q.ingest_run_id WHERE m.id=q.media_id AND m.ingest_generation=q.generation AND m.publication_state IN ('published','degraded') AND m.published_at IS NOT NULL AND r.media_id=q.media_id AND r.generation=q.generation AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL))))`, familyDueSQL(QueuePostIngest, "q"), sqlPlaceholders(len(args)), familyLegacySQL(QueuePostIngest, "q"), linkedEligibilitySQL("q"))
	var available bool
	err := db.QueryRowContext(ctx, query, args...).Scan(&available)
	return available, err
}

func claimEligibleAnyTx(ctx context.Context, tx store.ImmediateConnTx, req ClaimRequest, owner string) (*ClaimPayload, error) {
	present, err := tableAvailable(ctx, tx, "post_ingest_task")
	if err != nil || !present {
		return nil, err
	}
	// Restrict required HOL to types the caller can claim now (e.g. exclude poster
	// when poster slots are full) so other required work can use remaining global slots.
	oldest, err := oldestEligibleRequiredFor(ctx, tx, req.Registry, req.TaskTypes, req.SchedulerPolicy)
	if err != nil {
		return nil, err
	}
	if oldest != nil {
		if oldest.family != QueuePostIngest || !containsString(req.TaskTypes, oldest.taskType) {
			return nil, nil
		}
		req.TaskType = oldest.taskType
		return updateFamilyClaim(ctx, tx, req, oldest.id, owner)
	}
	args := make([]any, 0, len(req.TaskTypes)*2)
	for _, typ := range req.TaskTypes {
		args = append(args, typ)
	}
	caseOrder := "CASE q.task_type "
	for index, typ := range req.TaskTypes {
		caseOrder += fmt.Sprintf("WHEN ? THEN %d ", index)
		args = append(args, typ)
	}
	caseOrder += fmt.Sprintf("ELSE %d END", len(req.TaskTypes))
	query := fmt.Sprintf(`SELECT q.id,q.task_type FROM post_ingest_task q LEFT JOIN media_ingest_step st ON st.id=q.ingest_step_id WHERE %s AND q.task_type IN (%s) AND COALESCE(st.required,0)=0 AND (%s OR (%s) OR (q.task_type='poster_repair' AND q.ingest_run_id IS NOT NULL AND q.ingest_step_id IS NULL AND q.generation>0 AND EXISTS(SELECT 1 FROM media m JOIN media_ingest_run r ON r.id=q.ingest_run_id WHERE m.id=q.media_id AND m.ingest_generation=q.generation AND m.publication_state IN ('published','degraded') AND m.published_at IS NOT NULL AND r.media_id=q.media_id AND r.generation=q.generation AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL))) ORDER BY %s,%s LIMIT 1`, familyDueSQL(QueuePostIngest, "q"), sqlPlaceholders(len(req.TaskTypes)), familyLegacySQL(QueuePostIngest, "q"), linkedEligibilitySQL("q"), caseOrder, familyOrderSQL(QueuePostIngest, "q", req.SchedulerPolicy, time.Now()))
	var id int64
	var typ string
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&id, &typ); errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	req.TaskType = typ
	return updateFamilyClaim(ctx, tx, req, id, owner)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func claimEligibleTx(ctx context.Context, tx store.ImmediateConnTx, req ClaimRequest, owner string) (*ClaimPayload, error) {
	if req.Family != QueuePostIngest && req.Family != QueueScrape && req.Family != QueuePrepare {
		return nil, errors.New("publication claim: invalid family")
	}
	table, _ := familySource(req.Family)
	present, err := tableAvailable(ctx, tx, table)
	if err != nil {
		return nil, err
	}
	if !present {
		if req.Registry != nil && req.Registry.Available(req.TaskType) {
			return nil, fmt.Errorf("publication claim: advertised capability %s missing table %s", req.TaskType, table)
		}
		return nil, nil
	}
	if strings.TrimSpace(req.TaskType) == "" || req.Registry == nil || !req.Registry.Available(req.TaskType) {
		// Legacy rows are handled below without registry; linked work fails closed.
	}
	candidate, required, linked, err := selectFamilyCandidate(ctx, tx, req)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if linked && (req.Registry == nil || !req.Registry.Available(req.TaskType)) {
		return nil, nil
	}
	if linked {
		oldest, err := oldestEligibleRequired(ctx, tx, req.Registry, req.SchedulerPolicy)
		if err != nil {
			return nil, err
		}
		if required {
			if oldest == nil || oldest.family != req.Family || oldest.id != candidate {
				return nil, nil
			}
		} else if oldest != nil {
			return nil, nil
		}
	}
	return updateFamilyClaim(ctx, tx, req, candidate, owner)
}

func familySource(f QueueFamily) (table, alias string) {
	switch f {
	case QueuePostIngest:
		return "post_ingest_task", "q"
	case QueueScrape:
		return "scrape_task", "q"
	case QueuePrepare:
		return "transcode_task", "q"
	}
	return "", ""
}

func linkedEligibilitySQL(alias string) string {
	return fmt.Sprintf(`%[1]s.ingest_run_id IS NOT NULL AND %[1]s.ingest_step_id IS NOT NULL AND %[1]s.generation IS NOT NULL AND EXISTS(
SELECT 1 FROM media_ingest_step st JOIN media_ingest_run r ON r.id=st.run_id JOIN media m ON m.id=st.media_id
WHERE st.id=%[1]s.ingest_step_id AND st.run_id=%[1]s.ingest_run_id AND st.media_id=%[1]s.media_id AND st.generation=%[1]s.generation
AND r.media_id=st.media_id AND r.generation=st.generation AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL AND m.ingest_generation=st.generation
AND st.status='waiting' AND ((st.required=1 AND r.status='processing') OR (st.required=0 AND m.published_at IS NOT NULL AND ((r.status='processing' AND m.publication_state IN ('published','degraded')) OR (r.status='published' AND NOT EXISTS(SELECT 1 FROM media_ingest_step rr WHERE rr.run_id=r.id AND rr.required=1 AND rr.status NOT IN ('done','skipped'))) OR (r.status='degraded' AND NOT EXISTS(SELECT 1 FROM media_ingest_step rr WHERE rr.run_id=r.id AND rr.required=1 AND rr.status IN ('waiting','running'))))))
AND NOT EXISTS(SELECT 1 FROM media_ingest_step_dependency d LEFT JOIN media_ingest_step dep ON dep.id=d.depends_on_step_id WHERE d.step_id=st.id AND NOT ((d.dependency_kind='success' AND dep.id IS NOT NULL AND dep.run_id=st.run_id AND dep.media_id=st.media_id AND dep.generation=st.generation AND dep.status='done') OR (d.dependency_kind='terminal' AND dep.id IS NOT NULL AND dep.run_id=st.run_id AND dep.media_id=st.media_id AND dep.generation=st.generation AND dep.status IN ('done','skipped','failed','cancelled')))))`, alias)
}

func familyDueSQL(f QueueFamily, alias string) string {
	switch f {
	case QueuePostIngest:
		return fmt.Sprintf(`%s.status='waiting' AND %s.removed_at IS NULL AND %s.available_at<=CURRENT_TIMESTAMP AND %s.attempts<%s.max_attempts AND (%s.scan_task_id IS NULL OR EXISTS(SELECT 1 FROM scan_task sc WHERE sc.id=%s.scan_task_id AND sc.cancelled=0 AND sc.status<>'cancelled'))`, alias, alias, alias, alias, alias, alias, alias)
	case QueueScrape:
		return fmt.Sprintf(`%s.status IN ('waiting','failed') AND COALESCE(%s.fail_count,0)<%d AND COALESCE(%s.available_at,CURRENT_TIMESTAMP)<=CURRENT_TIMESTAMP AND (%s.lease_until IS NULL OR %s.lease_until<CURRENT_TIMESTAMP)`, alias, alias, DefaultNetworkMaxAttempts, alias, alias, alias)
	case QueuePrepare:
		return fmt.Sprintf(`%s.status='waiting' AND COALESCE(%s.task_type,'batch')='pretranscode' AND (%s.lease_until IS NULL OR %s.lease_until<CURRENT_TIMESTAMP)`, alias, alias, alias, alias)
	}
	return "0"
}

func familyAvailableSQL(f QueueFamily, alias string) string {
	if f == QueuePrepare {
		return alias + ".created_at"
	}
	return "COALESCE(" + alias + ".available_at," + alias + ".created_at)"
}
func familyOrderSQL(f QueueFamily, alias string, policy *scheduler.Policy, now time.Time) string {
	if f == QueuePostIngest {
		if policy != nil {
			return scheduler.EffectivePrioritySQL(alias, policy, now) + " DESC," + familyAvailableSQL(f, alias) + "," + alias + ".created_at," + alias + ".id"
		}
		return alias + ".source_class DESC," + alias + ".base_priority DESC," + alias + ".priority DESC," + familyAvailableSQL(f, alias) + "," + alias + ".created_at," + alias + ".id"
	}
	return alias + ".priority DESC," + familyAvailableSQL(f, alias) + "," + alias + ".created_at," + alias + ".id"
}

func familyLegacySQL(f QueueFamily, alias string) string {
	if f == QueuePostIngest {
		return fmt.Sprintf("(%s.ingest_run_id IS NULL AND %s.ingest_step_id IS NULL AND %s.generation=0)", alias, alias, alias)
	}
	return fmt.Sprintf("(%s.ingest_run_id IS NULL AND %s.ingest_step_id IS NULL AND %s.generation IS NULL)", alias, alias, alias)
}

func selectFamilyCandidate(ctx context.Context, tx store.SQLExecutor, req ClaimRequest) (id int64, required, linked bool, err error) {
	table, alias := familySource(req.Family)
	due := familyDueSQL(req.Family, alias)
	link := linkedEligibilitySQL(alias)
	typeFilter := ""
	args := []any{}
	if req.Family == QueuePostIngest {
		typeFilter = " AND q.task_type=?"
		args = append(args, req.TaskType)
	}
	if req.QueueID != nil {
		typeFilter += " AND q.id=?"
		args = append(args, *req.QueueID)
	}
	repair := "0"
	if req.Family == QueuePostIngest && req.TaskType == "poster_repair" {
		repair = `q.task_type='poster_repair' AND q.ingest_run_id IS NOT NULL AND q.ingest_step_id IS NULL AND q.generation>0 AND EXISTS(SELECT 1 FROM media m JOIN media_ingest_run r ON r.id=q.ingest_run_id WHERE m.id=q.media_id AND m.ingest_generation=q.generation AND m.publication_state IN ('published','degraded') AND m.published_at IS NOT NULL AND r.media_id=q.media_id AND r.generation=q.generation AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL)`
	}
	query := fmt.Sprintf(`SELECT q.id,COALESCE(st.required,0),q.ingest_run_id IS NOT NULL FROM %s q LEFT JOIN media_ingest_step st ON st.id=q.ingest_step_id WHERE %s%s AND (%s OR (%s) OR (%s)) ORDER BY %s LIMIT 1`, table, due, typeFilter, familyLegacySQL(req.Family, alias), link, repair, familyOrderSQL(req.Family, alias, req.SchedulerPolicy, time.Now()))
	err = tx.QueryRowContext(ctx, query, args...).Scan(&id, &required, &linked)
	return
}

type requiredCandidate struct {
	family             QueueFamily
	id                 int64
	taskType           string
	priority           int64
	sourceClass        int64
	basePriority       int64
	available, created string
}

func familyCapabilities(f QueueFamily) []string {
	switch f {
	case QueuePostIngest:
		return []string{"poster", "poster_repair", "thumbnail", "encrypt", "preview", "keyframe", "subtitle", "atrack"}
	case QueueScrape:
		return []string{"scrape"}
	case QueuePrepare:
		return []string{"prepare"}
	}
	return nil
}
func familyAdvertised(registry coreiface.CapabilityRegistry, f QueueFamily) bool {
	if registry == nil {
		return false
	}
	for _, typ := range familyCapabilities(f) {
		if registry.Available(typ) {
			return true
		}
	}
	return false
}
func oldestEligibleRequired(ctx context.Context, tx store.SQLExecutor, registry coreiface.CapabilityRegistry, policy *scheduler.Policy) (*requiredCandidate, error) {
	return oldestEligibleRequiredFor(ctx, tx, registry, nil, policy)
}

func oldestEligibleRequiredFor(ctx context.Context, tx store.SQLExecutor, registry coreiface.CapabilityRegistry, postTypes []string, policy *scheduler.Policy) (*requiredCandidate, error) {
	var best *requiredCandidate
	for _, f := range []QueueFamily{QueuePostIngest, QueueScrape, QueuePrepare} {
		table, a := familySource(f)
		present, err := tableAvailable(ctx, tx, table)
		if err != nil {
			return nil, err
		}
		if !present {
			if familyAdvertised(registry, f) {
				return nil, fmt.Errorf("publication claim: advertised capability %s missing table %s", familyCapabilities(f)[0], table)
			}
			continue
		}
		typeFilter := ""
		var queryArgs []any
		if f == QueuePostIngest {
			typeFilter = " AND st.step_type=q.task_type"
			if len(postTypes) > 0 {
				typeFilter += " AND q.task_type IN (" + sqlPlaceholders(len(postTypes)) + ")"
				queryArgs = make([]any, len(postTypes))
				for i, postType := range postTypes {
					queryArgs[i] = postType
				}
			}
		}
		availableExpr := familyAvailableSQL(f, a)
		query := fmt.Sprintf(`SELECT q.id,st.step_type,COALESCE(q.priority,0),CAST(0 AS INTEGER),CAST(0 AS INTEGER),CAST(%s AS TEXT),CAST(q.created_at AS TEXT) FROM %s q JOIN media_ingest_step st ON st.id=q.ingest_step_id WHERE st.required=1 AND %s AND %s%s ORDER BY %s LIMIT 1`, availableExpr, table, familyDueSQL(f, a), linkedEligibilitySQL(a), typeFilter, familyOrderSQL(f, a, policy, time.Now()))
		var c requiredCandidate
		c.family = f
		err = tx.QueryRowContext(ctx, query, queryArgs...).Scan(&c.id, &c.taskType, &c.priority, &c.sourceClass, &c.basePriority, &c.available, &c.created)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if registry == nil || !registry.Available(c.taskType) {
			continue
		}
		if best == nil || requiredLess(c, *best) {
			copy := c
			best = &copy
		}
	}
	return best, nil
}
func requiredLess(a, b requiredCandidate) bool {
	if a.family == QueuePostIngest && b.family == QueuePostIngest {
		if a.sourceClass != b.sourceClass {
			return a.sourceClass > b.sourceClass
		}
		if a.basePriority != b.basePriority {
			return a.basePriority > b.basePriority
		}
	}
	if a.priority != b.priority {
		return a.priority > b.priority
	}
	if a.available != b.available {
		return a.available < b.available
	}
	if a.created != b.created {
		return a.created < b.created
	}
	if a.id != b.id {
		return a.id < b.id
	}
	return a.family < b.family
}

func eligibleRequiredExists(ctx context.Context, tx store.SQLExecutor, registry coreiface.CapabilityRegistry) (bool, error) {
	c, err := oldestEligibleRequired(ctx, tx, registry, nil)
	return c != nil, err
}

func updateFamilyClaim(ctx context.Context, tx store.SQLExecutor, req ClaimRequest, id int64, owner string) (*ClaimPayload, error) {
	table, a := familySource(req.Family)
	due := familyDueSQL(req.Family, a)
	link := linkedEligibilitySQL(a)
	set := ""
	switch req.Family {
	case QueuePostIngest:
		set = `status='running',attempts=attempts+1,lease_owner=?,lease_until=datetime(CURRENT_TIMESTAMP,'+90 seconds'),started_at=COALESCE(started_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP`
	case QueueScrape:
		set = `status='running',fail_count=COALESCE(fail_count,0)+1,progress=15,message='scraping...',lease_owner=?,lease_until=datetime(CURRENT_TIMESTAMP,'+90 seconds'),started_at=COALESCE(started_at,CURRENT_TIMESTAMP)`
	case QueuePrepare:
		set = `status='running',lease_owner=?,lease_until=datetime(CURRENT_TIMESTAMP,'+90 seconds'),started_at=COALESCE(started_at,CURRENT_TIMESTAMP)`
	}
	repair := "0"
	if req.Family == QueuePostIngest && req.TaskType == "poster_repair" {
		repair = `q.task_type='poster_repair' AND q.ingest_run_id IS NOT NULL AND q.ingest_step_id IS NULL AND q.generation>0 AND EXISTS(SELECT 1 FROM media m JOIN media_ingest_run r ON r.id=q.ingest_run_id WHERE m.id=q.media_id AND m.ingest_generation=q.generation AND m.publication_state IN ('published','degraded') AND m.published_at IS NOT NULL AND r.media_id=q.media_id AND r.generation=q.generation AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL)`
	}
	q := fmt.Sprintf(`UPDATE %s AS q SET %s WHERE q.id=? AND %s AND (%s OR (%s) OR (%s))`, table, set, due, familyLegacySQL(req.Family, a), link, repair)
	res, err := tx.ExecContext(ctx, q, owner, id)
	if err != nil {
		return nil, err
	}

	n, _ := res.RowsAffected()
	if n != 1 {
		return nil, nil
	}
	p := &ClaimPayload{Family: req.Family, QueueID: id, Owner: owner, TaskType: req.TaskType}
	var lease sql.NullTime
	switch req.Family {
	case QueuePostIngest:
		err = tx.QueryRowContext(ctx, `SELECT media_id,ingest_run_id,ingest_step_id,scan_task_id,generation,task_type,attempts,max_attempts,retry_round,lease_until,source_class,base_priority,library_id,resource_profile_version,resource_profile_json FROM post_ingest_task WHERE id=?`, id).Scan(&p.MediaID, &p.RunID, &p.StepID, &p.ScanTaskID, &p.Generation, &p.TaskType, &p.Attempts, &p.MaxAttempts, &p.RetryRound, &lease, &p.SourceClass, &p.BasePriority, &p.LibraryID, &p.ResourceProfileVersion, &p.ResourceProfileJSON)
	case QueueScrape:
		err = tx.QueryRowContext(ctx, `SELECT media_id,ingest_run_id,ingest_step_id,generation,COALESCE(fail_count,0),retry_round,lease_until FROM scrape_task WHERE id=?`, id).Scan(&p.MediaID, &p.RunID, &p.StepID, &p.Generation, &p.Attempts, &p.RetryRound, &lease)
		p.TaskType = "scrape"
		p.MaxAttempts = DefaultNetworkMaxAttempts
	case QueuePrepare:
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(media_id,(SELECT m.id FROM media m WHERE m.file_id=transcode_task.file_id),0),ingest_run_id,ingest_step_id,generation,COALESCE(retry_round,0),lease_until FROM transcode_task WHERE id=?`, id).Scan(&p.MediaID, &p.RunID, &p.StepID, &p.Generation, &p.RetryRound, &lease)
		p.TaskType = "prepare"
		p.Attempts = 1
		p.MaxAttempts = DefaultLocalMaxAttempts
	}
	if err != nil {
		return nil, err
	}
	if lease.Valid {
		p.LeaseUntil = lease.Time
	}
	if p.StepID.Valid {
		stepResult, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE media_ingest_step SET status='running',attempts=?,lease_owner=?,lease_until=(SELECT lease_until FROM %s WHERE id=?),started_at=COALESCE(started_at,CURRENT_TIMESTAMP),updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='waiting'`, table), p.Attempts, p.Owner, id, p.StepID.Int64)
		if err != nil {
			return nil, err
		}
		stepRows, err := stepResult.RowsAffected()
		if err != nil {
			return nil, err
		}
		if stepRows != 1 {
			return nil, fmt.Errorf("publication claim: linked step transition affected %d rows", stepRows)
		}
		if p.RunID.Valid {
			if err := FinalizeClaimTransitionTx(ctx, tx, p.RunID.Int64); err != nil {
				return nil, err
			}
		}
	}
	return p, nil
}

func claimByOwner(ctx context.Context, db *sql.DB, f QueueFamily, owner string) (*ClaimPayload, error) {
	p := &ClaimPayload{Family: f, Owner: owner}
	var lease sql.NullTime
	var err error
	switch f {
	case QueuePostIngest:
		err = db.QueryRowContext(ctx, `SELECT q.id,q.media_id,q.ingest_run_id,q.ingest_step_id,q.scan_task_id,q.generation,q.task_type,q.attempts,q.max_attempts,q.retry_round,q.lease_until,q.source_class,q.base_priority,q.library_id,q.resource_profile_version,q.resource_profile_json FROM post_ingest_task q LEFT JOIN media_ingest_step st ON st.id=q.ingest_step_id LEFT JOIN media_ingest_run r ON r.id=q.ingest_run_id LEFT JOIN media m ON m.id=q.media_id WHERE q.lease_owner=? AND q.status='running' AND ((q.ingest_run_id IS NULL AND q.ingest_step_id IS NULL AND q.generation=0) OR (st.status='running' AND st.lease_owner=q.lease_owner AND st.run_id=q.ingest_run_id AND st.media_id=q.media_id AND st.generation=q.generation AND r.id=st.run_id AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL AND m.ingest_generation=q.generation) OR (q.task_type='poster_repair' AND q.ingest_run_id IS NOT NULL AND q.ingest_step_id IS NULL AND q.generation>0 AND EXISTS(SELECT 1 FROM media m JOIN media_ingest_run r ON r.id=q.ingest_run_id WHERE m.id=q.media_id AND m.ingest_generation=q.generation AND m.publication_state IN ('published','degraded') AND m.published_at IS NOT NULL AND r.media_id=q.media_id AND r.generation=q.generation AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL)))`, owner).Scan(&p.QueueID, &p.MediaID, &p.RunID, &p.StepID, &p.ScanTaskID, &p.Generation, &p.TaskType, &p.Attempts, &p.MaxAttempts, &p.RetryRound, &lease, &p.SourceClass, &p.BasePriority, &p.LibraryID, &p.ResourceProfileVersion, &p.ResourceProfileJSON)
	case QueueScrape:
		err = db.QueryRowContext(ctx, `SELECT q.id,q.media_id,q.ingest_run_id,q.ingest_step_id,q.generation,COALESCE(q.fail_count,0),q.retry_round,q.lease_until FROM scrape_task q LEFT JOIN media_ingest_step st ON st.id=q.ingest_step_id LEFT JOIN media_ingest_run r ON r.id=q.ingest_run_id LEFT JOIN media m ON m.id=q.media_id WHERE q.lease_owner=? AND q.status='running' AND ((q.ingest_run_id IS NULL AND q.ingest_step_id IS NULL AND q.generation IS NULL) OR (st.status='running' AND st.lease_owner=q.lease_owner AND st.run_id=q.ingest_run_id AND st.media_id=q.media_id AND st.generation=q.generation AND r.id=st.run_id AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL AND m.ingest_generation=q.generation))`, owner).Scan(&p.QueueID, &p.MediaID, &p.RunID, &p.StepID, &p.Generation, &p.Attempts, &p.RetryRound, &lease)
		p.TaskType = "scrape"
		p.MaxAttempts = DefaultNetworkMaxAttempts
	case QueuePrepare:
		err = db.QueryRowContext(ctx, `SELECT q.id,COALESCE(q.media_id,(SELECT m2.id FROM media m2 WHERE m2.file_id=q.file_id),0),q.ingest_run_id,q.ingest_step_id,q.generation,COALESCE(q.retry_round,0),q.lease_until FROM transcode_task q LEFT JOIN media_ingest_step st ON st.id=q.ingest_step_id LEFT JOIN media_ingest_run r ON r.id=q.ingest_run_id LEFT JOIN media m ON m.id=q.media_id WHERE q.lease_owner=? AND q.status='running' AND ((q.ingest_run_id IS NULL AND q.ingest_step_id IS NULL AND q.generation IS NULL) OR (st.status='running' AND st.lease_owner=q.lease_owner AND st.run_id=q.ingest_run_id AND st.media_id=q.media_id AND st.generation=q.generation AND r.id=st.run_id AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL AND m.ingest_generation=q.generation))`, owner).Scan(&p.QueueID, &p.MediaID, &p.RunID, &p.StepID, &p.Generation, &p.RetryRound, &lease)
		p.TaskType = "prepare"
		p.Attempts = 1
		p.MaxAttempts = DefaultLocalMaxAttempts
	default:
		return nil, errors.New("publication claim reconciliation: invalid family")
	}
	if err != nil {
		return nil, err
	}
	if lease.Valid {
		p.LeaseUntil = lease.Time
	}
	return p, nil
}

// LinkedClaimEligibilitySQL is the canonical linked-or-legacy dependency predicate.
func LinkedClaimEligibilitySQL(alias string) string {
	return fmt.Sprintf(`((%[1]s.ingest_run_id IS NULL AND %[1]s.ingest_step_id IS NULL AND %[1]s.generation IS NULL) OR (%s))`, alias, linkedEligibilitySQL(alias))
}
