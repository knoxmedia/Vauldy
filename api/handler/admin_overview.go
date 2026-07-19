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
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"

	"knox-media/internal/postingest"
	"knox-media/internal/store"
)

const adminOverviewTimeout = 3 * time.Second

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
	SampleSystem func(context.Context, string) (SystemSample, error)
}

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
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		var r result
		if vals, err := cpu.Percent(200*time.Millisecond, false); err == nil && len(vals) > 0 {
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
		return SystemSample{}, ctx.Err()
	case r := <-ch:
		return r.sample, r.err
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
	softwareVersion := "dev"
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		softwareVersion = bi.Main.Version
	}
	return AdminOverviewData{
		"monitor":    map[string]any{"cpu_percent": sample.CPUPercent, "memory_percent": sample.MemoryPercent, "disk_percent": sample.DiskPercent, "transcode_task_count": transcodeTasks, "media_total": mediaTotal},
		"system":     map[string]any{"cpu_count": runtime.NumCPU(), "memory_total": sample.MemoryTotal, "os": runtime.GOOS + "/" + runtime.GOARCH, "database": "sqlite " + dbVersion.String, "software_version": softwareVersion},
		"activities": activities, "post_ingest_queue": queue, "running_post_ingest_tasks": running, "scan_leases": leases, "resource_budget": b.budget(), "sqlite_metrics": b.sqliteMetrics(),
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
