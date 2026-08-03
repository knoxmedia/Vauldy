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
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"

	"knox-media/internal/buildinfo"
	"knox-media/internal/publication"
	"knox-media/internal/store"
)

const adminOverviewTimeout = 8 * time.Second
const listLibrariesTimeout = 8 * time.Second

type AdminOverviewData map[string]any

type OverviewBuilder interface {
	Build(context.Context) (AdminOverviewData, error)
}

type AdminOverviewBuilder struct {
	DB           *sql.DB
	Metrics      *store.SQLiteMetrics
	SampleSystem func(context.Context, string) (SystemSample, error)
}

type SystemSample struct {
	CPUPercent    float64
	MemoryPercent float64
	MemoryTotal   uint64
	DiskPercent   float64
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

func NewAdminOverviewBuilder(db *sql.DB, m *store.SQLiteMetrics) *AdminOverviewBuilder {
	return &AdminOverviewBuilder{DB: db, Metrics: m, SampleSystem: sampleSystem}
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
	var dbVersion sql.NullString
	if err := b.DB.QueryRowContext(ctx, `SELECT sqlite_version()`).Scan(&dbVersion); err != nil {
		return nil, err
	}
	activities, err := b.loadActivities(ctx)
	if err != nil {
		return nil, err
	}
	softwareBuild := buildinfo.Current()
	return AdminOverviewData{
		"monitor": map[string]any{"cpu_percent": sample.CPUPercent, "memory_percent": sample.MemoryPercent, "disk_percent": sample.DiskPercent},
		"system":  map[string]any{"cpu_count": runtime.NumCPU(), "memory_total": sample.MemoryTotal, "os": runtime.GOOS + "/" + runtime.GOARCH, "database": "sqlite " + dbVersion.String, "software_version": softwareBuild.Version, "software_commit": softwareBuild.Commit, "software_build_time": softwareBuild.BuildTime, "software_dirty": softwareBuild.Dirty, "software_dirty_known": softwareBuild.DirtyKnown, "software_vcs_revision": softwareBuild.VCS.Revision, "software_vcs_time": softwareBuild.VCS.Time, "software_vcs_modified": softwareBuild.VCS.Modified, "software_vcs_modified_known": softwareBuild.VCS.ModifiedKnown},
		"activities":     activities,
		"sqlite_metrics": b.sqliteMetrics(),
	}, nil
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

// Shared utilities used by publication diagnostic code (media_ingest.go).

const (
	maxPublicationDiagnosticMessage   = 256
	maxPublicationMetadataErrorCount  = 8
	maxPublicationMetadataErrorsBytes = 2048
)

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
