package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"

	"knox-media/internal/buildinfo"
	"knox-media/internal/coreiface"
	"knox-media/internal/postingest"
	"knox-media/internal/publication"
	"knox-media/internal/store"
)

const adminOverviewTimeout = 8 * time.Second
const listLibrariesTimeout = 8 * time.Second

type AdminOverviewData map[string]any

type OverviewBuilder interface {
	Build(context.Context) (AdminOverviewData, error)
}

type budgetSnapshotter interface {
	Snapshot() postingest.BudgetSnapshot
}

type AdminOverviewBuilder struct {
	DB           *sql.DB
	Dispatcher   budgetSnapshotter
	Metrics      *store.SQLiteMetrics
	Capabilities coreiface.CapabilityRegistry
	SampleSystem func(context.Context, string) (SystemSample, error)
}

type PublicationPolicyDiagnostic struct {
	MediaID            int64    `json:"media_id"`
	RunID              int64    `json:"run_id"`
	Generation         int64    `json:"generation"`
	PolicyVersion      int      `json:"policy_version"`
	Status             string   `json:"status"`
	TerminalReason     string   `json:"terminal_reason"`
	RequiredWaiting    int      `json:"required_waiting"`
	RequiredFailed     int      `json:"required_failed"`
	OptionalWaiting    int      `json:"optional_waiting"`
	OptionalFailed     int      `json:"optional_failed"`
	AdapterUnavailable []string `json:"adapter_unavailable"`
	MetadataErrors     []string `json:"metadata_errors"`
	RecoveryError      string   `json:"recovery_error"`
}

const (
	maxPublicationPolicyRows          = 100
	maxPublicationDiagnosticMessage   = 256
	maxPublicationMetadataErrorCount  = 8
	maxPublicationMetadataErrorsBytes = 2048
)

type SystemSample struct {
	CPUPercent    float64
	MemoryPercent float64
	MemoryTotal   uint64
	DiskPercent   float64
}

type PostIngestQueueOverview struct {
	ByStatus             map[string]int64            `json:"by_status"`
	ByType               map[string]map[string]int64 `json:"by_type"`
	OldestWaitingSeconds int64                       `json:"oldest_waiting_seconds"`
	ExpiredLeaseCount    int64                       `json:"expired_lease_count"`
}

type RunningPostIngestTask struct {
	ID           int64  `json:"id"`
	MediaID      int64  `json:"media_id"`
	TaskType     string `json:"task_type"`
	Type         string `json:"type"`
	ScanTaskID   *int64 `json:"scan_task_id"`
	Attempts     int64  `json:"attempts"`
	Attempt      int64  `json:"attempt"`
	MaxAttempts  int64  `json:"max_attempts"`
	RunSeconds   int64  `json:"run_seconds"`
	StartedAt    string `json:"started_at"`
	LeaseOwner   string `json:"lease_owner"`
	LeaseUntil   string `json:"lease_until"`
	LeaseExpires string `json:"lease_expires"`
}

type ScanLeaseOverview struct {
	LibraryID  int64  `json:"library_id"`
	ScanTaskID int64  `json:"scan_task_id"`
	OwnerID    string `json:"owner_id"`
	LeaseUntil string `json:"lease_until"`
	Expired    bool   `json:"expired"`
}

type ResourceBudgetOverview struct {
	GlobalLimit  int `json:"global_limit"`
	GlobalUsed   int `json:"global_used"`
	PosterLimit  int `json:"poster_limit"`
	PosterUsed   int `json:"poster_used"`
	PreviewLimit int `json:"preview_limit"`
	PreviewUsed  int `json:"preview_used"`
}

type SQLiteMetricsOverview struct {
	Scope            string `json:"scope"`
	Persistent       bool   `json:"persistent"`
	BusyRetries      uint64 `json:"busy_retries"`
	BusyExhausted    uint64 `json:"busy_exhausted"`
	ProgressBatches  uint64 `json:"progress_batches"`
	LogBatches       uint64 `json:"log_batches"`
	LogBatchFailures uint64 `json:"log_failures"`
	DroppedLogs      uint64 `json:"dropped_logs"`
}

func NewAdminOverviewBuilder(db *sql.DB, d budgetSnapshotter, m *store.SQLiteMetrics) *AdminOverviewBuilder {
	return &AdminOverviewBuilder{DB: db, Dispatcher: d, Metrics: m, SampleSystem: sampleSystem}
}

func sampleSystem(ctx context.Context, path string) (SystemSample, error) {
	type result struct {
		sample SystemSample
	}
	ch := make(chan result, 1)
	go func() {
		var r result
		// Interval 0 is non-blocking (since last sample). A fixed 200ms sleep
		// previously burned overview budget and failed the whole request on USB I/O stalls.
		if vals, err := cpu.Percent(0, false); err == nil && len(vals) > 0 {
			r.sample.CPUPercent = vals[0]
		}
		if vm, err := mem.VirtualMemory(); err == nil {
			r.sample.MemoryPercent, r.sample.MemoryTotal = vm.UsedPercent, vm.Total
		}
		if du, err := disk.Usage(path); err == nil {
			r.sample.DiskPercent = du.UsedPercent
		}
		ch <- r
	}()
	select {
	case <-ctx.Done():
		// Soft-fail: overview still returns queue/DB sections without system gauges.
		return SystemSample{}, nil
	case r := <-ch:
		return r.sample, nil
	}
}

func (b *AdminOverviewBuilder) Build(ctx context.Context) (AdminOverviewData, error) {
	if b == nil || b.DB == nil {
		return nil, errors.New("admin overview database is not configured")
	}
	path := "."
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	sample := SystemSample{}
	if b.SampleSystem != nil {
		var err error
		sample, err = b.SampleSystem(ctx, path)
		if err != nil {
			return nil, err
		}
	}
	queryInt := func(q string, dest *int64) error { return b.DB.QueryRowContext(ctx, q).Scan(dest) }
	var transcodeTasks, mediaTotal int64
	if err := queryInt(`SELECT COUNT(1) FROM transcode_task WHERE status IN ('waiting','running')`, &transcodeTasks); err != nil {
		return nil, err
	}
	if err := queryInt(`SELECT COUNT(1) FROM media`, &mediaTotal); err != nil {
		return nil, err
	}
	var dbVersion sql.NullString
	if err := b.DB.QueryRowContext(ctx, `SELECT sqlite_version()`).Scan(&dbVersion); err != nil {
		return nil, err
	}
	queue, err := b.loadQueue(ctx)
	if err != nil {
		return nil, err
	}
	running, err := b.loadRunning(ctx)
	if err != nil {
		return nil, err
	}
	leases, err := b.loadScanLeases(ctx)
	if err != nil {
		return nil, err
	}
	activities, err := b.loadActivities(ctx)
	if err != nil {
		return nil, err
	}
	publicationPolicy, err := b.loadPublicationPolicy(ctx)
	if err != nil {
		return nil, err
	}
	softwareBuild := buildinfo.Current()
	return AdminOverviewData{
		"monitor":    map[string]any{"cpu_percent": sample.CPUPercent, "memory_percent": sample.MemoryPercent, "disk_percent": sample.DiskPercent, "transcode_task_count": transcodeTasks, "media_total": mediaTotal},
		"system":     map[string]any{"cpu_count": runtime.NumCPU(), "memory_total": sample.MemoryTotal, "os": runtime.GOOS + "/" + runtime.GOARCH, "database": "sqlite " + dbVersion.String, "software_version": softwareBuild.Version, "software_commit": softwareBuild.Commit, "software_build_time": softwareBuild.BuildTime, "software_dirty": softwareBuild.Dirty, "software_dirty_known": softwareBuild.DirtyKnown, "software_vcs_revision": softwareBuild.VCS.Revision, "software_vcs_time": softwareBuild.VCS.Time, "software_vcs_modified": softwareBuild.VCS.Modified, "software_vcs_modified_known": softwareBuild.VCS.ModifiedKnown},
		"activities": activities, "post_ingest_queue": queue, "running_post_ingest_tasks": running, "scan_leases": leases, "resource_budget": b.budget(), "sqlite_metrics": b.sqliteMetrics(),
		"publication_policy": publicationPolicy,
	}, nil
}

func (b *AdminOverviewBuilder) loadQueue(ctx context.Context) (PostIngestQueueOverview, error) {
	out := PostIngestQueueOverview{ByStatus: map[string]int64{}, ByType: map[string]map[string]int64{}}
	rows, err := b.DB.QueryContext(ctx, `SELECT status, COUNT(*) FROM post_ingest_task GROUP BY status ORDER BY status`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var k string
		var n int64
		if err := rows.Scan(&k, &n); err != nil {
			rows.Close()
			return out, err
		}
		out.ByStatus[k] = n
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()
	rows, err = b.DB.QueryContext(ctx, `SELECT task_type, status, COUNT(*) FROM post_ingest_task GROUP BY task_type, status ORDER BY task_type, status`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var taskType, status string
		var n int64
		if err := rows.Scan(&taskType, &status, &n); err != nil {
			rows.Close()
			return out, err
		}
		if out.ByType[taskType] == nil {
			out.ByType[taskType] = map[string]int64{}
		}
		out.ByType[taskType][status] = n
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()
	if err := b.DB.QueryRowContext(ctx, `SELECT CAST(MAX(0, COALESCE((julianday(CURRENT_TIMESTAMP)-julianday(MIN(available_at)))*86400,0)) AS INTEGER) FROM post_ingest_task WHERE status='waiting'`).Scan(&out.OldestWaitingSeconds); err != nil {
		return out, err
	}
	if err := b.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM post_ingest_task WHERE status='running' AND lease_until < CURRENT_TIMESTAMP`).Scan(&out.ExpiredLeaseCount); err != nil {
		return out, err
	}
	return out, nil
}

func (b *AdminOverviewBuilder) loadRunning(ctx context.Context) ([]RunningPostIngestTask, error) {
	rows, err := b.DB.QueryContext(ctx, `SELECT id,media_id,scan_task_id,task_type,attempts,max_attempts,CAST(MAX(0,COALESCE((julianday(CURRENT_TIMESTAMP)-julianday(started_at))*86400,0)) AS INTEGER),COALESCE(started_at,''),COALESCE(lease_owner,''),COALESCE(lease_until,'') FROM post_ingest_task WHERE status='running' ORDER BY started_at ASC,id ASC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RunningPostIngestTask, 0)
	for rows.Next() {
		var v RunningPostIngestTask
		var scan sql.NullInt64
		if err := rows.Scan(&v.ID, &v.MediaID, &scan, &v.TaskType, &v.Attempts, &v.MaxAttempts, &v.RunSeconds, &v.StartedAt, &v.LeaseOwner, &v.LeaseUntil); err != nil {
			return nil, err
		}
		v.Type = v.TaskType
		v.Attempt = v.Attempts
		v.LeaseExpires = v.LeaseUntil
		if scan.Valid {
			x := scan.Int64
			v.ScanTaskID = &x
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (b *AdminOverviewBuilder) loadScanLeases(ctx context.Context) ([]ScanLeaseOverview, error) {
	rows, err := b.DB.QueryContext(ctx, `SELECT library_id,scan_task_id,owner_id,lease_until,lease_until < CURRENT_TIMESTAMP FROM scan_lease ORDER BY lease_until ASC,library_id ASC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ScanLeaseOverview, 0)
	for rows.Next() {
		var v ScanLeaseOverview
		if err := rows.Scan(&v.LibraryID, &v.ScanTaskID, &v.OwnerID, &v.LeaseUntil, &v.Expired); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (b *AdminOverviewBuilder) loadActivities(ctx context.Context) ([]map[string]any, error) {
	rows, err := b.DB.QueryContext(ctx, `SELECT id,COALESCE(username,''),action,COALESCE(media_id,0),COALESCE(message,''),created_at FROM activity_log ORDER BY id DESC LIMIT 30`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]any, 0, 30)
	for rows.Next() {
		var id, mid int64
		var user, action, msg, created string
		if err := rows.Scan(&id, &user, &action, &mid, &msg, &created); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "username": user, "action": action, "media_id": mid, "message": msg, "created_at": created})
	}
	return out, rows.Err()
}
func (b *AdminOverviewBuilder) budget() ResourceBudgetOverview {
	var s postingest.BudgetSnapshot
	if b.Dispatcher != nil {
		s = b.Dispatcher.Snapshot()
	}
	return ResourceBudgetOverview{s.GlobalLimit, s.GlobalUsed, s.PosterLimit, s.PosterUsed, s.PreviewLimit, s.PreviewUsed}
}
func (b *AdminOverviewBuilder) sqliteMetrics() SQLiteMetricsOverview {
	o := SQLiteMetricsOverview{Scope: "process_since_start", Persistent: false}
	if b.Metrics != nil {
		o.BusyRetries = b.Metrics.BusyRetries.Load()
		o.BusyExhausted = b.Metrics.BusyExhausted.Load()
		o.ProgressBatches = b.Metrics.ProgressBatches.Load()
		o.LogBatches = b.Metrics.LogBatches.Load()
		o.LogBatchFailures = b.Metrics.LogBatchFailures.Load()
		o.DroppedLogs = b.Metrics.DroppedLogs.Load()
	}
	return o
}

func (b *AdminOverviewBuilder) loadPublicationPolicy(ctx context.Context) ([]PublicationPolicyDiagnostic, error) {
	out := make([]PublicationPolicyDiagnostic, 0)
	rows, err := b.DB.QueryContext(ctx, `
SELECT r.id, r.media_id, r.generation, COALESCE(r.policy_version,1), r.status, COALESCE(r.terminal_reason,''), COALESCE(r.config_snapshot_json,''), COALESCE(r.updated_at,''),
       COALESCE(SUM(CASE WHEN s.required=1 AND s.status='waiting' THEN 1 ELSE 0 END),0),
       COALESCE(SUM(CASE WHEN s.required=1 AND s.status='failed' THEN 1 ELSE 0 END),0),
       COALESCE(SUM(CASE WHEN s.required=0 AND s.status='waiting' THEN 1 ELSE 0 END),0),
       COALESCE(SUM(CASE WHEN s.required=0 AND s.status='failed' THEN 1 ELSE 0 END),0)
FROM media_ingest_run r
JOIN media m ON m.id=r.media_id AND m.ingest_generation=r.generation
LEFT JOIN media_ingest_step s ON s.run_id=r.id
WHERE r.superseded_at IS NULL AND r.superseded_by_generation IS NULL
GROUP BY r.id
ORDER BY r.id`)
	if err != nil {
		return out, err
	}
	defer rows.Close()

	type candidate struct {
		row       PublicationPolicyDiagnostic
		snapshot  string
		updatedAt string
		severity  int
	}
	candidates := make([]candidate, 0)
	mediaIDs := make([]int64, 0)
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.row.RunID, &c.row.MediaID, &c.row.Generation, &c.row.PolicyVersion, &c.row.Status, &c.row.TerminalReason, &c.snapshot, &c.updatedAt,
			&c.row.RequiredWaiting, &c.row.RequiredFailed, &c.row.OptionalWaiting, &c.row.OptionalFailed); err != nil {
			return out, err
		}
		c.row.MetadataErrors = parseBoundedMetadataErrors(c.snapshot)
		c.row.AdapterUnavailable = b.adapterUnavailableForRun(ctx, c.row.RunID, c.snapshot)
		candidates = append(candidates, c)
		mediaIDs = append(mediaIDs, c.row.MediaID)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	recoveryByMedia := loadLatestRecoveryErrorsByMedia(ctx, b.DB, mediaIDs)
	filtered := make([]candidate, 0, len(candidates))
	for _, c := range candidates {
		c.row.RecoveryError = recoveryByMedia[c.row.MediaID]
		if !publicationPolicyActionable(c.row) {
			continue
		}
		c.severity = publicationPolicySeverity(c.row.Status)
		filtered = append(filtered, c)
	}
	candidates = filtered
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].severity != candidates[j].severity {
			return candidates[i].severity < candidates[j].severity
		}
		if candidates[i].row.RequiredFailed != candidates[j].row.RequiredFailed {
			return candidates[i].row.RequiredFailed > candidates[j].row.RequiredFailed
		}
		if candidates[i].row.OptionalFailed != candidates[j].row.OptionalFailed {
			return candidates[i].row.OptionalFailed > candidates[j].row.OptionalFailed
		}
		if candidates[i].updatedAt != candidates[j].updatedAt {
			return candidates[i].updatedAt > candidates[j].updatedAt
		}
		return candidates[i].row.RunID > candidates[j].row.RunID
	})
	if len(candidates) > maxPublicationPolicyRows {
		candidates = candidates[:maxPublicationPolicyRows]
	}
	for _, c := range candidates {
		row := c.row
		if row.AdapterUnavailable == nil {
			row.AdapterUnavailable = []string{}
		}
		if row.MetadataErrors == nil {
			row.MetadataErrors = []string{}
		}
		out = append(out, row)
	}
	return out, nil
}

func publicationPolicyActionable(row PublicationPolicyDiagnostic) bool {
	switch row.Status {
	case "processing", "degraded", "failed", "cancelled":
		return true
	}
	return row.RequiredWaiting > 0 || row.RequiredFailed > 0 || row.OptionalWaiting > 0 || row.OptionalFailed > 0 ||
		row.RecoveryError != "" || len(row.AdapterUnavailable) > 0 || len(row.MetadataErrors) > 0
}

func publicationPolicySeverity(status string) int {
	switch status {
	case "failed":
		return 0
	case "cancelled":
		return 1
	case "degraded":
		return 2
	case "processing":
		return 3
	default:
		return 4
	}
}

func (b *AdminOverviewBuilder) adapterUnavailableForRun(ctx context.Context, runID int64, snapshot string) []string {
	if b == nil || b.Capabilities == nil {
		return []string{}
	}
	steps := optionalStepTypesFromSnapshot(snapshot)
	if len(steps) == 0 {
		rows, err := b.DB.QueryContext(ctx, `SELECT DISTINCT step_type FROM media_ingest_step WHERE run_id=? AND required=0 ORDER BY step_type`, runID)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var step string
				if err := rows.Scan(&step); err != nil {
					break
				}
				steps = append(steps, step)
			}
		}
	}
	out := make([]string, 0)
	seen := map[string]bool{}
	for _, step := range steps {
		step = strings.TrimSpace(step)
		if step == "" || seen[step] {
			continue
		}
		seen[step] = true
		if !b.Capabilities.Available(step) {
			out = append(out, step)
		}
	}
	sort.Strings(out)
	return out
}

func optionalStepTypesFromSnapshot(raw string) []string {
	var snap publication.ConfigSnapshot
	if err := json.Unmarshal([]byte(raw), &snap); err != nil {
		return nil
	}
	out := make([]string, 0, len(snap.OptionalSteps))
	for _, step := range snap.OptionalSteps {
		out = append(out, string(step))
	}
	return out
}

func parseBoundedMetadataErrors(raw string) []string {
	var snap struct {
		Metadata publication.MetadataAttempt `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(raw), &snap); err != nil {
		return []string{}
	}
	out := make([]string, 0, len(snap.Metadata.Errors))
	total := 0
	for _, diag := range snap.Metadata.Errors {
		msg := strings.TrimSpace(diag.Source)
		if diag.Message != "" {
			if msg != "" {
				msg += ": "
			}
			msg += diag.Message
		}
		msg = truncateUTF8Bound(msg, maxPublicationDiagnosticMessage)
		if msg == "" {
			continue
		}
		if len(out) >= maxPublicationMetadataErrorCount || total+len(msg) > maxPublicationMetadataErrorsBytes {
			break
		}
		out = append(out, msg)
		total += len(msg)
	}
	return out
}

func loadLatestRecoveryError(ctx context.Context, db *sql.DB, mediaID int64) string {
	errs := loadUnresolvedRecoveryErrors(ctx, db, mediaID)
	if len(errs) == 0 {
		return ""
	}
	return errs[0]
}

func loadLatestRecoveryErrorsByMedia(ctx context.Context, db *sql.DB, mediaIDs []int64) map[int64]string {
	out := map[int64]string{}
	if db == nil || len(mediaIDs) == 0 {
		return out
	}
	seen := make(map[int64]struct{}, len(mediaIDs))
	ids := make([]int64, 0, len(mediaIDs))
	for _, id := range mediaIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return out
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)*3)
	for i := range ids {
		placeholders[i] = "?"
	}
	inList := strings.Join(placeholders, ",")
	for range 3 {
		for _, id := range ids {
			args = append(args, id)
		}
	}
	query := `
SELECT media_id, recovery_error, updated_at FROM (
  SELECT media_id, recovery_error, updated_at FROM media_asset_stage_journal WHERE media_id IN (` + inList + `) AND TRIM(recovery_error)<>'' AND state<>'committed'
  UNION ALL
  SELECT media_id, recovery_error, updated_at FROM media_encryption_stage_journal WHERE media_id IN (` + inList + `) AND TRIM(recovery_error)<>'' AND state<>'committed'
  UNION ALL
  SELECT media_id, recovery_error, updated_at FROM poster_repair_stage WHERE media_id IN (` + inList + `) AND TRIM(recovery_error)<>'' AND state<>'committed'
) ORDER BY updated_at DESC`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		for _, id := range ids {
			if msg := loadLatestRecoveryError(ctx, db, id); msg != "" {
				out[id] = msg
			}
		}
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var mediaID int64
		var msg string
		var updatedAt string
		if err := rows.Scan(&mediaID, &msg, &updatedAt); err != nil {
			return out
		}
		if _, exists := out[mediaID]; exists {
			continue
		}
		msg = truncateUTF8Bound(strings.TrimSpace(msg), maxPublicationDiagnosticMessage)
		if msg != "" {
			out[mediaID] = msg
		}
	}
	return out
}

func truncateUTF8Bound(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	s = s[:max]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func (h *Handler) AdminOverview(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), adminOverviewTimeout)
	defer cancel()
	data, err := h.AdminOverviewBuilder.Build(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			c.JSON(http.StatusGatewayTimeout, gin.H{"code": "admin_overview_timeout"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"code": "admin_overview_internal"})
		}
		return
	}
	c.JSON(http.StatusOK, data)
}

type flushErrorWriter interface {
	FlushError() error
}

type unwrapResponseWriter interface {
	Unwrap() http.ResponseWriter
}

func supportsFlush(w http.ResponseWriter) bool {
	for depth := 0; w != nil && depth < 16; depth++ {
		if _, ok := w.(flushErrorWriter); ok {
			return true
		}
		unwrapper, ok := w.(unwrapResponseWriter)
		if !ok {
			_, ok = w.(http.Flusher)
			return ok
		}
		next := unwrapper.Unwrap()
		if next == nil || next == w {
			_, ok = w.(http.Flusher)
			return ok
		}
		w = next
	}
	return false
}

func flushResponse(w http.ResponseWriter) error {
	for depth := 0; w != nil && depth < 16; depth++ {
		if flusher, ok := w.(flushErrorWriter); ok {
			return flusher.FlushError()
		}
		unwrapper, ok := w.(unwrapResponseWriter)
		if !ok {
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
				return nil
			}
			return http.ErrNotSupported
		}
		next := unwrapper.Unwrap()
		if next == nil || next == w {
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
				return nil
			}
			return http.ErrNotSupported
		}
		w = next
	}
	return http.ErrNotSupported
}

func writeResponse(w http.ResponseWriter, payload []byte) error {
	n, err := w.Write(payload)
	if err != nil {
		return err
	}
	if n != len(payload) {
		return io.ErrShortWrite
	}
	return nil
}

func (h *Handler) AdminOverviewStream(c *gin.Context) {
	w := c.Writer
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if !supportsFlush(w) {
		c.Status(http.StatusInternalServerError)
		return
	}
	c.Status(http.StatusOK)

	interval := h.overviewStreamInterval
	if interval <= 0 {
		interval = 3 * time.Second
	}
	buildTimeout := h.overviewBuildTimeout
	if buildTimeout <= 0 {
		buildTimeout = 2 * time.Second
	}
	requestCtx := c.Request.Context()
	failures := 0

	sendSnapshot := func() bool {
		buildCtx, cancel := context.WithTimeout(requestCtx, buildTimeout)
		data, err := h.AdminOverviewBuilder.Build(buildCtx)
		buildErr := buildCtx.Err()
		cancel()

		event := "overview"
		payloadValue := any(data)
		if err != nil {
			if requestCtx.Err() != nil {
				return false
			}
			failures++
			code := "admin_overview_internal"
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(buildErr, context.DeadlineExceeded) {
				code = "admin_overview_timeout"
			}
			event = "error"
			payloadValue = gin.H{"code": code}
		} else {
			failures = 0
		}

		payload, err := json.Marshal(payloadValue)
		if err != nil {
			return false
		}
		frame := []byte("event: " + event + "\ndata: " + string(payload) + "\n\n")
		if err := writeResponse(w, frame); err != nil {
			return false
		}
		if err := flushResponse(w); err != nil {
			return false
		}
		if requestCtx.Err() != nil {
			return false
		}
		return failures < 3
	}

	if !sendSnapshot() {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-requestCtx.Done():
			return
		case <-ticker.C:
			if !sendSnapshot() {
				return
			}
		}
	}
}
