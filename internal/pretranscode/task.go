package pretranscode

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"knox-media/internal/publication"
	"knox-media/internal/storage"
	"log"
	"os"
	"sort"
	"strings"
	"time"
)

var ErrLinkedIngestTaskOperation = errors.New("operation is not allowed for linked ingest prepare task")

// TaskService manages pretranscode task lifecycle. It writes to the shared
// transcode_task table (community, task_type='pretranscode') plus the
// commercial pretranscode_task_meta and pretranscode_rendition_job tables.
type TaskService struct {
	DB           *sql.DB
	TranscodeDir string
}

// UnifiedTask is the joined row for the unified task list (SRS 3.2.1).
type UnifiedTask struct {
	ID             int64  `json:"id"`
	TaskType       string `json:"task_type"`
	FileID         string `json:"file_id"`
	MediaID        int64  `json:"media_id"`
	Title          string `json:"title"`
	Quality        string `json:"quality"`
	Status         string `json:"status"`
	Progress       int    `json:"progress"`
	ErrorMessage   string `json:"error_message"`
	OutputPath     string `json:"output_path"`
	PresetID       int64  `json:"preset_id,omitempty"`
	PresetName     string `json:"preset_name,omitempty"`
	Priority       string `json:"priority,omitempty"`
	EncryptionMode string `json:"encryption_mode,omitempty"`
	OutputFormat   string `json:"output_format,omitempty"`
	CreatedAt      string `json:"created_at"`
	StartedAt      string `json:"started_at,omitempty"`
	CompletedAt    string `json:"completed_at,omitempty"`
}

// CreateTask inserts a pretranscode task for each media id (SRS TASK-01).
// Returns the created task ids.
func (s *TaskService) CreateTask(mediaIDs []int64, presetID int64, priority string) ([]int64, error) {
	preset, err := s.loadPreset(presetID)
	if err != nil {
		return nil, err
	}
	if priority == "" {
		priority = "normal"
	}
	var ids []int64
	for _, mid := range mediaIDs {
		loc, err := s.lookupMediaLocation(mid)
		if err != nil {
			return ids, err
		}
		if !storage.PlaintextSourceAvailable(s.DB, mid, loc.libraryID, loc.filePath) {
			return ids, ErrPlaintextSourceUnavailable
		}
		fileID, title := loc.fileID, loc.title
		absSource := loc.filePath
		if loc.libraryID > 0 {
			if resolved := storage.ResolveMediaAbsolutePath(s.DB, loc.libraryID, loc.filePath); resolved != "" {
				absSource = resolved
			}
		}
		outDir := ComputeTaskOutputRoot(TaskOutputRootInput{
			Mode:         preset.OutputDirMode,
			CustomDir:    preset.OutputDirCustom,
			TranscodeDir: s.TranscodeDir,
			FileID:       fileID,
			PresetID:     preset.ID,
			SourcePath:   absSource,
		})
		res, err := s.DB.Exec(`INSERT INTO transcode_task (file_id, quality, status, progress, task_type, preset_id, started_at, completed_at)
			VALUES (?, ?, 'waiting', 0, 'pretranscode', ?, NULL, NULL)`,
			fileID, ladderKey(preset), presetID)
		if err != nil {
			return ids, fmt.Errorf("insert task: %w", err)
		}
		tid, _ := res.LastInsertId()
		_, err = s.DB.Exec(`INSERT INTO pretranscode_task_meta (task_id, preset_id, output_format, encryption_mode, priority, output_path)
			VALUES (?, ?, ?, ?, ?, ?)`,
			tid, presetID, preset.OutputFormat, preset.EncryptionMode, priority, outDir)
		if err != nil {
			return ids, fmt.Errorf("insert meta: %w", err)
		}
		for _, r := range preset.Renditions {
			if _, err := s.DB.Exec(`INSERT INTO pretranscode_rendition_job (task_id, rendition_id, rendition_name, status)
				VALUES (?, ?, ?, 'waiting')`, tid, r.ID, r.Name); err != nil {
				_, _ = s.DB.Exec(`DELETE FROM pretranscode_task_meta WHERE task_id = ?`, tid)
				_, _ = s.DB.Exec(`DELETE FROM transcode_task WHERE id = ?`, tid)
				return ids, fmt.Errorf("insert rendition_job (rendition=%s): %w", r.Name, err)
			}
		}
		_ = title
		ids = append(ids, tid)
	}
	return ids, nil
}

// CreateBatchTask enqueues pretranscode for an entire library (SRS TASK-02).
// filter "untranscoded" limits to media with no existing done rendition.
func (s *TaskService) CreateBatchTask(libraryID, presetID int64, filter, priority string) (int, error) {
	query := `SELECT m.id, m.file_id FROM media m WHERE m.library_id = ? AND m.file_type = 'video'`
	args := []any{libraryID}
	if filter == "untranscoded" {
		query += ` AND NOT EXISTS (
			SELECT 1 FROM pretranscode_rendition_job j
			JOIN transcode_task t ON t.id = j.task_id
			WHERE t.file_id = m.file_id AND j.status = 'done')`
	}
	rows, err := s.DB.Query(query, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var mediaIDs []int64
	for rows.Next() {
		var id int64
		var fid string
		if err := rows.Scan(&id, &fid); err != nil {
			return 0, err
		}
		mediaIDs = append(mediaIDs, id)
	}
	if len(mediaIDs) == 0 {
		return 0, nil
	}
	ids, err := s.CreateTask(mediaIDs, presetID, priority)
	return len(ids), err
}

// ListTasks returns the unified task list (SRS UTSK-01..04). taskType filter
// "all"/"batch"/"pretranscode" controls which rows are returned.
func (s *TaskService) ListTasks(taskType string, limit int) ([]UnifiedTask, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT t.id, COALESCE(t.task_type,'batch'), t.file_id, COALESCE(m.id,0), COALESCE(m.title,''),
			COALESCE(t.quality,''), t.status, t.progress, COALESCE(t.error_message,''), COALESCE(t.output_path,''),
			COALESCE(pt.preset_id,0), COALESCE(p.name,''), COALESCE(pt.priority,''), COALESCE(pt.encryption_mode,''),
			COALESCE(pt.output_format,''), COALESCE(t.created_at,''),
			COALESCE(t.started_at,''), COALESCE(t.completed_at,'')
		FROM transcode_task t
		LEFT JOIN media m ON m.file_id = t.file_id
		LEFT JOIN pretranscode_task_meta pt ON pt.task_id = t.id
		LEFT JOIN transcode_preset p ON p.id = pt.preset_id`
	args := []any{}
	switch taskType {
	case "batch":
		q += ` WHERE COALESCE(t.task_type,'batch') = 'batch'`
	case "pretranscode":
		q += ` WHERE t.task_type = 'pretranscode'`
	}
	q += ` ORDER BY t.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UnifiedTask
	for rows.Next() {
		var t UnifiedTask
		if err := rows.Scan(&t.ID, &t.TaskType, &t.FileID, &t.MediaID, &t.Title, &t.Quality, &t.Status, &t.Progress,
			&t.ErrorMessage, &t.OutputPath, &t.PresetID, &t.PresetName, &t.Priority, &t.EncryptionMode, &t.OutputFormat,
			&t.CreatedAt, &t.StartedAt, &t.CompletedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// GetTask returns a single task with its rendition jobs.
func (s *TaskService) GetTask(id int64) (*UnifiedTask, []RenditionJob, error) {
	tasks, err := s.ListTasks("", 0)
	if err != nil {
		return nil, nil, err
	}
	for _, t := range tasks {
		if t.ID == id {
			jobs, err := s.ListRenditionJobs(id)
			return &t, jobs, err
		}
	}
	return nil, nil, ErrTaskNotFound
}

// CancelTask atomically cancels a linked prepare task and its publication step.
func (s *TaskService) CancelTask(id int64) error {
	tx, err := s.DB.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	linked, runID, stepID, generation, err := taskIngestLinkTx(tx, id)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE pretranscode_rendition_job SET status='cancelled',completed_at=CURRENT_TIMESTAMP,lease_owner=NULL,lease_until=NULL WHERE task_id=? AND status IN ('waiting','running')`, id); err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE transcode_task SET status='cancelled',completed_at=COALESCE(completed_at,CURRENT_TIMESTAMP) WHERE id=? AND status IN ('waiting','running','paused')`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("task %d is not cancellable", id)
	}
	if linked {
		res, err = tx.Exec(`UPDATE media_ingest_step SET status='cancelled',last_error='cancelled',finished_at=CURRENT_TIMESTAMP,lease_owner=NULL,lease_until=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=? AND run_id=? AND generation=? AND step_type='prepare' AND status IN ('waiting','running')`, stepID, runID, generation)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("task %d linked prepare step is not cancellable", id)
		}
		if err = publication.AggregateTx(context.Background(), tx, runID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *TaskService) PauseTask(id int64) error {
	linked, err := s.isLinkedIngestTask(id)
	if err != nil {
		return err
	}
	if linked {
		return ErrLinkedIngestTaskOperation
	}
	_, err = s.DB.Exec(`UPDATE transcode_task SET status='paused' WHERE id=? AND status IN ('waiting','running')`, id)
	return err
}
func (s *TaskService) ResumeTask(id int64) error {
	linked, err := s.isLinkedIngestTask(id)
	if err != nil {
		return err
	}
	if linked {
		return ErrLinkedIngestTaskOperation
	}
	_, err = s.DB.Exec(`UPDATE transcode_task SET status='waiting' WHERE id=? AND status='paused'`, id)
	return err
}

// RetryTask opens a fresh attempt round atomically for linked prepare work.
func (s *TaskService) RetryTask(id int64) error {
	tx, err := s.DB.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	linked, runID, stepID, generation, err := taskIngestLinkTx(tx, id)
	if err != nil {
		return err
	}
	jobRes, err := tx.Exec(`UPDATE pretranscode_rendition_job SET status='waiting',progress=0,error_message=NULL,output_path=NULL,encoder_used=NULL,started_at=NULL,completed_at=NULL,available_at=CURRENT_TIMESTAMP,lease_owner=NULL,lease_until=NULL WHERE task_id=? AND status IN ('failed','cancelled')`, id)
	if err != nil {
		return err
	}
	jobsReset, _ := jobRes.RowsAffected()
	taskRes, err := tx.Exec(`UPDATE transcode_task SET status='waiting',progress=0,error_message=NULL,started_at=NULL,completed_at=NULL WHERE id=? AND status IN ('failed','paused','cancelled','waiting')`, id)
	if err != nil {
		return err
	}
	taskReset, _ := taskRes.RowsAffected()
	if jobsReset == 0 && taskReset == 0 {
		return fmt.Errorf("task %d is not retryable", id)
	}
	if linked {
		res, err := tx.Exec(`UPDATE media_ingest_step SET status='waiting',attempts=0,last_error='',available_at=CURRENT_TIMESTAMP,lease_owner=NULL,lease_until=NULL,started_at=NULL,finished_at=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=? AND run_id=? AND generation=? AND step_type='prepare' AND status IN ('failed','cancelled')`, stepID, runID, generation)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("task %d linked prepare step is not retryable", id)
		}
		if _, err = tx.Exec(`UPDATE media_ingest_run SET status='processing',preserve_visibility=1,error_message='',finished_at=NULL WHERE id=?`, runID); err != nil {
			return err
		}
		if err = publication.AggregateTx(context.Background(), tx, runID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func taskIngestLinkTx(tx *sql.Tx, id int64) (bool, int64, int64, int64, error) {
	var runID, stepID, generation sql.NullInt64
	if err := tx.QueryRow(`SELECT ingest_run_id,ingest_step_id,generation FROM transcode_task WHERE id=?`, id).Scan(&runID, &stepID, &generation); err != nil {
		return false, 0, 0, 0, err
	}
	if !runID.Valid && !stepID.Valid && !generation.Valid {
		return false, 0, 0, 0, nil
	}
	if !runID.Valid || !stepID.Valid || !generation.Valid {
		return false, 0, 0, 0, fmt.Errorf("task %d has partial ingest linkage", id)
	}
	return true, runID.Int64, stepID.Int64, generation.Int64, nil
}
func (s *TaskService) isLinkedIngestTask(id int64) (bool, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	linked, _, _, _, err := taskIngestLinkTx(tx, id)
	return linked, err
}

// RetryLatestFailedTaskForMedia re-queues the most recent failed pretranscode task for a media row.
func (s *TaskService) RetryLatestFailedTaskForMedia(mediaID int64) (int64, error) {
	var fileID string
	if err := s.DB.QueryRow(`SELECT file_id FROM media WHERE id = ?`, mediaID).Scan(&fileID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrMediaNotFound
		}
		return 0, err
	}
	var taskID int64
	err := s.DB.QueryRow(`
		SELECT t.id FROM transcode_task t
		JOIN pretranscode_task_meta p ON p.task_id = t.id
		WHERE t.file_id = ? AND t.task_type = 'pretranscode' AND t.status = 'failed'
		ORDER BY t.id DESC LIMIT 1`, fileID).Scan(&taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if err := s.RetryTask(taskID); err != nil {
		return 0, err
	}
	return taskID, nil
}

// DeleteTask removes the task and its outputs (SRS ACT-03). The CASCADE
// foreign keys on pretranscode_task_meta / pretranscode_rendition_job clean
// up the metadata rows.
func (s *TaskService) DeleteTask(id int64) error {
	var outputPath string
	_ = s.DB.QueryRow(`SELECT COALESCE(pt.output_path,'') FROM pretranscode_task_meta pt WHERE pt.task_id = ?`, id).Scan(&outputPath)
	if outputPath != "" {
		_ = osRemoveAll(outputPath)
	}
	_, err := s.DB.Exec(`DELETE FROM transcode_task WHERE id = ?`, id)
	return err
}

// CleanupFailedTasks removes failed tasks older than `days` (SRS ACT-05).
func (s *TaskService) CleanupFailedTasks(days int) (int, error) {
	if days <= 0 {
		days = 7
	}
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	rows, err := s.DB.Query(`SELECT id FROM transcode_task WHERE status = 'failed' AND created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var n int
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return n, err
		}
		if err := s.DeleteTask(id); err == nil {
			n++
		}
	}
	return n, nil
}

// RenditionJob mirrors pretranscode_rendition_job.
type RenditionJob struct {
	ID            int64  `json:"id"`
	TaskID        int64  `json:"task_id"`
	RenditionID   int64  `json:"rendition_id"`
	RenditionName string `json:"rendition_name"`
	Status        string `json:"status"`
	Progress      int    `json:"progress"`
	OutputPath    string `json:"output_path"`
	ErrorMessage  string `json:"error_message"`
	EncoderUsed   string `json:"encoder_used"`
	StartedAt     string `json:"started_at"`
	CompletedAt   string `json:"completed_at"`
}

// ListRenditionJobs returns the rendition jobs for a task.
func (s *TaskService) ListRenditionJobs(taskID int64) ([]RenditionJob, error) {
	rows, err := s.DB.Query(`SELECT id, task_id, rendition_id, rendition_name, status, progress,
		COALESCE(output_path,''), COALESCE(error_message,''), COALESCE(encoder_used,''),
		COALESCE(started_at,''), COALESCE(completed_at,'')
		FROM pretranscode_rendition_job WHERE task_id = ? ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RenditionJob
	for rows.Next() {
		var j RenditionJob
		if err := rows.Scan(&j.ID, &j.TaskID, &j.RenditionID, &j.RenditionName, &j.Status, &j.Progress,
			&j.OutputPath, &j.ErrorMessage, &j.EncoderUsed, &j.StartedAt, &j.CompletedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, nil
}

// CancelRenditionJob cancels a single rendition job.
func (s *TaskService) CancelRenditionJob(id int64) error {
	linked, err := s.renditionBelongsToLinkedTask(id)
	if err != nil {
		return err
	}
	if linked {
		return ErrLinkedIngestTaskOperation
	}
	_, err = s.DB.Exec(`UPDATE pretranscode_rendition_job SET status='cancelled',completed_at=CURRENT_TIMESTAMP WHERE id=? AND status IN ('waiting','running')`, id)
	return err
}

// RetryRenditionJob re-queues a single failed/cancelled rendition job.
func (s *TaskService) RetryRenditionJob(id int64) error {
	linked, err := s.renditionBelongsToLinkedTask(id)
	if err != nil {
		return err
	}
	if linked {
		return ErrLinkedIngestTaskOperation
	}
	_, err = s.DB.Exec(`UPDATE pretranscode_rendition_job SET status='waiting',progress=0,error_message=NULL,started_at=NULL,completed_at=NULL,available_at=CURRENT_TIMESTAMP WHERE id=? AND status IN ('failed','cancelled')`, id)
	return err
}
func (s *TaskService) renditionBelongsToLinkedTask(id int64) (bool, error) {
	var linked int
	err := s.DB.QueryRow(`SELECT COUNT(1) FROM pretranscode_rendition_job j JOIN transcode_task t ON t.id=j.task_id WHERE j.id=? AND t.ingest_step_id IS NOT NULL`, id).Scan(&linked)
	return linked > 0, err
}

// AggregateProgress computes the weighted average of rendition progress
// (SRS PROG-02). Returns 100 when all renditions are done.
func AggregateProgress(jobs []RenditionJob) int {
	if len(jobs) == 0 {
		return 0
	}
	total := 0
	for _, j := range jobs {
		if j.Status == "done" {
			total += 100
		} else {
			total += j.Progress
		}
	}
	return total / len(jobs)
}

// StorageStats summarizes pretranscode disk usage (SRS STOR-06).
type StorageStats struct {
	TaskCount   int64   `json:"task_count"`
	OutputBytes int64   `json:"output_bytes"`
	OutputMB    float64 `json:"output_mb"`
}

// GetStorageStats walks the pretranscode output directories.
func (s *TaskService) GetStorageStats() (*StorageStats, error) {
	var count int64
	_ = s.DB.QueryRow(`SELECT COUNT(1) FROM transcode_task WHERE task_type = 'pretranscode'`).Scan(&count)
	bytes, _ := dirSize(s.TranscodeDir)
	mb := float64(bytes) / 1024.0 / 1024.0
	return &StorageStats{TaskCount: count, OutputBytes: bytes, OutputMB: mb}, nil
}

// CleanupOutputs removes all pretranscode output for a media file (SRS STOR-05).
func (s *TaskService) CleanupOutputs(fileID string) error {
	rows, err := s.DB.Query(`SELECT DISTINCT pt.output_path FROM pretranscode_task_meta pt
		JOIN transcode_task t ON t.id = pt.task_id WHERE t.file_id = ?`, fileID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err == nil && p != "" {
			_ = osRemoveAll(p)
		}
	}
	return nil
}

func (s *TaskService) markTask(id int64, status string) (bool, error) {
	res, err := s.DB.Exec(`UPDATE transcode_task SET status = ? WHERE id = ?`, status, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *TaskService) loadPreset(presetID int64) (*Preset, error) {
	ps := &PresetService{DB: s.DB}
	p, err := ps.GetPreset(presetID)
	if err != nil {
		return nil, err
	}
	if !p.IsEnabled {
		return nil, ErrPresetDisabled
	}
	return p, nil
}

func (s *TaskService) lookupMedia(mediaID int64) (string, string, error) {
	loc, err := s.lookupMediaLocation(mediaID)
	if err != nil {
		return "", "", err
	}
	return loc.fileID, loc.title, nil
}

type mediaLocation struct {
	fileID    string
	title     string
	filePath  string
	libraryID int64
}

func (s *TaskService) lookupMediaLocation(mediaID int64) (mediaLocation, error) {
	var loc mediaLocation
	err := s.DB.QueryRow(`SELECT file_id, COALESCE(title,''), COALESCE(file_path,''), COALESCE(library_id,0) FROM media WHERE id = ?`, mediaID).
		Scan(&loc.fileID, &loc.title, &loc.filePath, &loc.libraryID)
	if err == sql.ErrNoRows {
		return loc, ErrMediaNotFound
	}
	return loc, err
}

// ladderKey builds the quality column value (rendition names joined by '+').
func ladderKey(p *Preset) string {
	names := make([]string, 0, len(p.Renditions))
	sorted := append([]Rendition(nil), p.Renditions...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Height < sorted[j].Height })
	for _, r := range sorted {
		names = append(names, r.Name)
	}
	return strings.Join(names, "+")
}

// renditionOutputDir is kept for tests referencing legacy layout.
func renditionOutputDir(transcodeDir, fileID string, presetID int64) string {
	return ComputeTaskOutputRoot(TaskOutputRootInput{
		Mode:         OutputDirModeData,
		TranscodeDir: transcodeDir,
		FileID:       fileID,
		PresetID:     presetID,
	})
}

// --- Media Optimization API ---

// OptimizedRendition represents a completed rendition job for the optimization modal.
type OptimizedRendition struct {
	RenditionJobID int64  `json:"rendition_job_id"`
	RenditionName  string `json:"rendition_name"`
	Resolution     string `json:"resolution"`
	Bitrate        string `json:"bitrate"`
	OutputFormat   string `json:"output_format"`
	PresetName     string `json:"preset_name"`
	FileSize       int64  `json:"file_size"`
	CompletedAt    string `json:"completed_at"`
}

// RunningTask represents an in-progress pretranscode task.
type RunningTask struct {
	TaskID       int64  `json:"task_id"`
	PresetName   string `json:"preset_name"`
	Status       string `json:"status"`
	Progress     int    `json:"progress"`
	ErrorMessage string `json:"error_message,omitempty"`
}

// MediaOptimizationStatus is the response for GET /api/v1/media/:id/optimization.
type MediaOptimizationStatus struct {
	MediaID               int64                `json:"media_id"`
	Filename              string               `json:"filename"`
	Duration              int                  `json:"duration"`
	Resolution            string               `json:"resolution"`
	FileSize              int64                `json:"file_size"`
	OptimizationAvailable bool                 `json:"optimization_available"`
	OptimizedRenditions   []OptimizedRendition `json:"optimized_renditions"`
	RunningTasks          []RunningTask        `json:"running_tasks"`
}

// GetMediaOptimizationStatus returns the optimization status for a media item.
func (s *TaskService) GetMediaOptimizationStatus(mediaID int64) (*MediaOptimizationStatus, error) {
	// Get media info
	var fileID, filename, filePath string
	var libraryID int64
	var duration, width, height int
	var fileSize int64
	err := s.DB.QueryRow(`SELECT file_id, COALESCE(title,''), COALESCE(file_path,''), COALESCE(library_id,0), COALESCE(duration,0), COALESCE(width,0), COALESCE(height,0) FROM media WHERE id = ?`, mediaID).Scan(&fileID, &filename, &filePath, &libraryID, &duration, &width, &height)
	if err == sql.ErrNoRows {
		return nil, ErrMediaNotFound
	}
	if err != nil {
		return nil, err
	}

	resolution := ""
	if width > 0 && height > 0 {
		resolution = fmt.Sprintf("%dx%d", width, height)
	}

	// Get file size from source path (prefer plaintext when encrypted).
	absPath := storage.ResolveMediaAbsolutePath(s.DB, libraryID, filePath)
	sizePath := storage.ResolveKeyframeProbePath(s.DB, mediaID, absPath)
	if sizePath != "" {
		if info, err := os.Stat(sizePath); err == nil {
			fileSize = info.Size()
		}
	}

	status := &MediaOptimizationStatus{
		MediaID:               mediaID,
		Filename:              filename,
		Duration:              duration,
		Resolution:            resolution,
		FileSize:              fileSize,
		OptimizationAvailable: storage.PlaintextSourceAvailable(s.DB, mediaID, libraryID, filePath),
		OptimizedRenditions:   []OptimizedRendition{},
		RunningTasks:          []RunningTask{},
	}

	// Get completed rendition jobs
	rows, err := s.DB.Query(`
		SELECT j.id, j.rendition_name, COALESCE(p.output_format,'hls'), COALESCE(pr.name,''),
			COALESCE(j.completed_at,''), COALESCE(j.output_path,'')
		FROM pretranscode_rendition_job j
		JOIN transcode_task t ON t.id = j.task_id
		JOIN pretranscode_task_meta p ON p.task_id = t.id
		LEFT JOIN transcode_preset pr ON pr.id = p.preset_id
		WHERE t.file_id = ? AND j.status = 'done'
		ORDER BY j.id`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var r OptimizedRendition
		var outputPath string
		if err := rows.Scan(&r.RenditionJobID, &r.RenditionName, &r.OutputFormat, &r.PresetName, &r.CompletedAt, &outputPath); err != nil {
			return nil, err
		}
		if outputPath != "" {
			r.FileSize, _ = dirSize(RenditionSizePath(outputPath, r.OutputFormat))
		}
		// Parse resolution from rendition name (e.g., "720p" -> "1280x720", portrait -> "720x1280")
		r.Resolution = resolutionFromRendition(r.RenditionName, width, height)
		r.Bitrate = bitrateFromName(r.RenditionName)
		status.OptimizedRenditions = append(status.OptimizedRenditions, r)
	}

	// Get running tasks (including failed ones so user sees error details)
	taskRows, err := s.DB.Query(`
		SELECT t.id, COALESCE(pr.name,''), t.status, t.progress, COALESCE(t.error_message,'')
		FROM transcode_task t
		JOIN pretranscode_task_meta p ON p.task_id = t.id
		LEFT JOIN transcode_preset pr ON pr.id = p.preset_id
		WHERE t.file_id = ? AND t.status IN ('waiting','running','paused','failed')
		ORDER BY t.id`, fileID)
	if err != nil {
		return nil, err
	}
	defer taskRows.Close()

	for taskRows.Next() {
		var rt RunningTask
		if err := taskRows.Scan(&rt.TaskID, &rt.PresetName, &rt.Status, &rt.Progress, &rt.ErrorMessage); err != nil {
			return nil, err
		}
		status.RunningTasks = append(status.RunningTasks, rt)
	}

	return status, nil
}

// RemoveRenditionJob deletes a rendition job and its output file.
func (s *TaskService) RemoveRenditionJob(jobID int64) error {
	var outputPath, outputFormat string
	err := s.DB.QueryRow(`SELECT COALESCE(j.output_path,''), COALESCE(pt.output_format,'hls')
		FROM pretranscode_rendition_job j
		JOIN pretranscode_task_meta pt ON pt.task_id = j.task_id
		WHERE j.id = ?`, jobID).Scan(&outputPath, &outputFormat)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if deletePath := RenditionDeletePath(outputPath, outputFormat); deletePath != "" {
		_ = osRemoveAll(deletePath)
	}
	_, err = s.DB.Exec(`DELETE FROM pretranscode_rendition_job WHERE id = ?`, jobID)
	return err
}

// BatchRemoveRenditionJobs deletes multiple rendition jobs and their output files.
func (s *TaskService) BatchRemoveRenditionJobs(jobIDs []int64) error {
	for _, id := range jobIDs {
		if err := s.RemoveRenditionJob(id); err != nil {
			return err
		}
	}
	return nil
}

// RecoverOrphanedTasks recreates missing pretranscode_rendition_job rows for
// waiting pretranscode tasks that have no rendition jobs. This fixes tasks
// created by a buggy build where the INSERT errors were silently swallowed.
func (s *TaskService) RecoverOrphanedTasks() int {
	rows, err := s.DB.Query(`SELECT t.id,COALESCE(t.preset_id,0),COALESCE(t.ingest_run_id,0),COALESCE(t.ingest_step_id,0),COALESCE(t.generation,0),COALESCE(m.ingest_jobs_snapshot_json,'') FROM transcode_task t LEFT JOIN pretranscode_task_meta m ON m.task_id=t.id WHERE t.task_type='pretranscode' AND t.status='waiting' AND NOT EXISTS(SELECT 1 FROM pretranscode_rendition_job j WHERE j.task_id=t.id)`)
	if err != nil {
		log.Printf("pretranscode recovery query failed: %v", err)
		return 0
	}
	defer rows.Close()
	type orphan struct {
		taskID, presetID, runID, stepID, generation int64
		snapshot                                    string
	}
	var orphans []orphan
	for rows.Next() {
		var o orphan
		if err := rows.Scan(&o.taskID, &o.presetID, &o.runID, &o.stepID, &o.generation, &o.snapshot); err == nil {
			orphans = append(orphans, o)
		}
	}
	fixed := 0
	for _, o := range orphans {
		if o.runID > 0 || o.stepID > 0 || o.generation > 0 {
			var jobs []ingestPrepareTaskJob
			if json.Unmarshal([]byte(o.snapshot), &jobs) != nil || len(jobs) == 0 {
				if err := s.failLinkedOrphan(o.taskID, o.runID, o.stepID, "invalid immutable prepare jobs snapshot"); err != nil {
					log.Printf("pretranscode recovery fail linked task %d: %v", o.taskID, err)
				}
				continue
			}
			tx, err := s.DB.Begin()
			if err != nil {
				continue
			}
			ok := true
			for _, j := range jobs {
				if len(j.Config) == 0 || !json.Valid(j.Config) {
					ok = false
					break
				}
				if _, err = tx.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_id,rendition_name,status,config_snapshot_json) VALUES(?,?,?,'waiting',?)`, o.taskID, nullRenditionID(j.RenditionID), j.RenditionName, string(j.Config)); err != nil {
					ok = false
					break
				}
			}
			if ok && tx.Commit() == nil {
				fixed++
			} else {
				_ = tx.Rollback()
				_ = s.failLinkedOrphan(o.taskID, o.runID, o.stepID, "invalid immutable prepare job snapshot")
			}
			continue
		}
		preset, err := s.loadPreset(o.presetID)
		if err != nil {
			continue
		}
		inserted := true
		for _, r := range preset.Renditions {
			if _, err := s.DB.Exec(`INSERT INTO pretranscode_rendition_job(task_id,rendition_id,rendition_name,status) VALUES(?,?,?,'waiting')`, o.taskID, r.ID, r.Name); err != nil {
				inserted = false
				break
			}
		}
		if inserted {
			fixed++
		}
	}
	return fixed
}
func nullRenditionID(id int64) any {
	if id > 0 {
		return id
	}
	return nil
}
func (s *TaskService) failLinkedOrphan(taskID, runID, stepID int64, message string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE transcode_task SET status='failed',error_message=? WHERE id=? AND status='waiting'`, message, taskID); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE media_ingest_step SET status='failed',last_error=?,finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=? AND run_id=? AND status='waiting'`, message, stepID, runID); err != nil {
		return err
	}
	if err = publication.AggregateTx(context.Background(), tx, runID); err != nil {
		return err
	}
	return tx.Commit()
}

// RepairStuckWaitingTasks resets failed rendition jobs for pretranscode tasks
// already marked waiting (e.g. after a generic task-list retry).
func (s *TaskService) RepairStuckWaitingTasks() int {
	res, err := s.DB.Exec(`UPDATE pretranscode_rendition_job
		SET status='waiting', progress=0, error_message=NULL, started_at=NULL, completed_at=NULL
		WHERE status IN ('failed','cancelled')
		  AND task_id IN (
		    SELECT id FROM transcode_task
		    WHERE status='waiting' AND COALESCE(task_type,'')='pretranscode'
		  )`)
	if err != nil {
		log.Printf("pretranscode repair stuck waiting tasks failed: %v", err)
		return 0
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		log.Printf("pretranscode repair: reset %d stuck rendition job(s) to waiting", n)
	}
	return int(n)
}

// resolutionFromRendition returns the effective output resolution for a rendition,
// adapting landscape preset sizes for portrait source videos.
func resolutionFromRendition(name string, sourceWidth, sourceHeight int) string {
	r := Rendition{Name: name, Height: heightFromRenditionName(name)}
	adapted := AdaptRenditionForSource(r, sourceWidth, sourceHeight)
	w, h := renditionTargetSize(adapted)
	if w > 0 && h > 0 {
		return fmt.Sprintf("%dx%d", w, h)
	}
	return resolutionFromName(name)
}

func heightFromRenditionName(name string) int {
	switch name {
	case "360p":
		return 360
	case "480p":
		return 480
	case "540p":
		return 540
	case "720p":
		return 720
	case "1080p":
		return 1080
	case "1440p":
		return 1440
	case "2160p", "4K":
		return 2160
	default:
		return 0
	}
}

// resolutionFromName returns a resolution string from a rendition name.
func resolutionFromName(name string) string {
	switch name {
	case "360p":
		return "640x360"
	case "480p":
		return "854x480"
	case "720p":
		return "1280x720"
	case "1080p":
		return "1920x1080"
	case "1440p":
		return "2560x1440"
	case "2160p":
		return "3840x2160"
	default:
		return ""
	}
}

// bitrateFromName returns a typical bitrate string from a rendition name.
func bitrateFromName(name string) string {
	switch name {
	case "360p":
		return "850k"
	case "480p":
		return "1400k"
	case "720p":
		return "2800k"
	case "1080p":
		return "5000k"
	case "1440p":
		return "8000k"
	case "2160p":
		return "15000k"
	default:
		return ""
	}
}

// Standard errors.
var (
	ErrTaskNotFound               = errors.New("task not found")
	ErrMediaNotFound              = errors.New("media not found")
	ErrPresetDisabled             = errors.New("preset is disabled")
	ErrPlaintextSourceUnavailable = errors.New("plaintext source unavailable")
)
