package handler

import (
	"context"
	"database/sql"
	"log"
	"time"

	"knox-media/internal/transcode"
)

const (
	keyframeWorkerInterval = 15 * time.Second
	keyframeWorkerBatchMax = 5

	atrackWorkerInterval = 15 * time.Second
	atrackWorkerBatchMax = 3

	previewWorkerInterval = 15 * time.Second
	previewWorkerBatchMax = 2

	transcodeWorkerInterval = 20 * time.Second
)

// StartKeyframeTaskLoop drains waiting keyframe tasks in the background.
func (h *Handler) StartKeyframeTaskLoop(ctx context.Context) {
	go h.runKeyframeWorkerOnce()
	tk := time.NewTicker(keyframeWorkerInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			h.runKeyframeWorkerOnce()
		}
	}
}

func (h *Handler) runKeyframeWorkerOnce() {
	if h == nil || h.KeyframeWorker == nil || h.App == nil || h.App.DB == nil {
		return
	}
	_, _ = h.App.DB.Exec(`
		UPDATE keyframe_task SET status = 'waiting', updated_at = CURRENT_TIMESTAMP
		WHERE status = 'running'
		  AND updated_at < datetime('now', '-20 minutes')
	`)
	var n int
	_ = h.App.DB.QueryRow(`SELECT COUNT(1) FROM keyframe_task WHERE status = 'waiting'`).Scan(&n)
	if n == 0 {
		return
	}
	limit := n
	if limit > keyframeWorkerBatchMax {
		limit = keyframeWorkerBatchMax
	}
	done, failed := h.KeyframeWorker.RunBatch(limit)
	if done+failed > 0 {
		log.Printf("keyframe worker: processed=%d ok=%d fail=%d waiting=%d", done+failed, done, failed, n-done-failed)
	}
}

// StartAtrackTaskLoop drains waiting audio-track extraction tasks in the background.
func (h *Handler) StartAtrackTaskLoop(ctx context.Context) {
	go h.runAtrackWorkerOnce()
	tk := time.NewTicker(atrackWorkerInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			h.runAtrackWorkerOnce()
		}
	}
}

func (h *Handler) runAtrackWorkerOnce() {
	if h == nil || h.AtrackWorker == nil || h.App == nil || h.App.DB == nil {
		return
	}
	_, _ = h.App.DB.Exec(`
		UPDATE atrack_task SET status = 'waiting', updated_at = CURRENT_TIMESTAMP
		WHERE status = 'running'
		  AND updated_at < datetime('now', '-20 minutes')
	`)
	var n int
	_ = h.App.DB.QueryRow(`SELECT COUNT(1) FROM atrack_task WHERE status = 'waiting'`).Scan(&n)
	if n == 0 {
		return
	}
	limit := n
	if limit > atrackWorkerBatchMax {
		limit = atrackWorkerBatchMax
	}
	done, failed := h.AtrackWorker.RunBatch(limit)
	if done+failed > 0 {
		log.Printf("atrack worker: processed=%d ok=%d fail=%d waiting=%d", done+failed, done, failed, n-done-failed)
	}
}

// StartPreviewTaskLoop drains waiting progress-bar preview tasks in the background.
func (h *Handler) StartPreviewTaskLoop(ctx context.Context) {
	go h.runPreviewWorkerOnce()
	tk := time.NewTicker(previewWorkerInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			h.runPreviewWorkerOnce()
		}
	}
}

func (h *Handler) runPreviewWorkerOnce() {
	if h == nil || h.PreviewWorker == nil || h.App == nil || h.App.DB == nil {
		return
	}
	_, _ = h.App.DB.Exec(`
		UPDATE preview_task SET status = 'waiting', updated_at = CURRENT_TIMESTAMP
		WHERE status = 'running'
		  AND updated_at < datetime('now', '-20 minutes')
	`)
	var n int
	_ = h.App.DB.QueryRow(`SELECT COUNT(1) FROM preview_task WHERE status = 'waiting'`).Scan(&n)
	if n == 0 {
		return
	}
	limit := n
	if limit > previewWorkerBatchMax {
		limit = previewWorkerBatchMax
	}
	done, failed := h.PreviewWorker.RunBatch(limit)
	if done+failed > 0 {
		log.Printf("preview worker: processed=%d ok=%d fail=%d waiting=%d", done+failed, done, failed, n-done-failed)
	}
}

// StartTranscodeTaskLoop drains waiting HLS transcode and package tasks in the background.
func (h *Handler) StartTranscodeTaskLoop(ctx context.Context) {
	go h.runTranscodeWorkerOnce()
	tk := time.NewTicker(transcodeWorkerInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			h.runTranscodeWorkerOnce()
		}
	}
}

func (h *Handler) runTranscodeWorkerOnce() {
	if h == nil || h.App == nil || h.App.DB == nil {
		return
	}
	if h.Worker == nil && h.PackageWorker == nil {
		return
	}
	_, _ = h.App.DB.Exec(`
		UPDATE package_task SET status = 'waiting', progress = 0, updated_at = CURRENT_TIMESTAMP
		WHERE status = 'running'
		  AND updated_at < datetime('now', '-20 minutes')
	`)

	var waiting int
	_ = h.App.DB.QueryRow(`
		SELECT (
			(SELECT COUNT(1) FROM transcode_task WHERE status = 'waiting') +
			(SELECT COUNT(1) FROM package_task WHERE status = 'waiting')
		)
	`).Scan(&waiting)
	if waiting == 0 {
		return
	}

	settings := h.loadTranscoderSettings()
	running := h.countRunningBackgroundTranscodeJobs()
	slots := transcode.BackgroundSlots(settings, running, waiting)
	if slots <= 0 {
		return
	}

	started := 0
	if h.Worker != nil && slots > 0 {
		n := h.Worker.StartWaiting(context.Background(), slots)
		started += n
		slots -= n
	}
	if h.PackageWorker != nil && slots > 0 {
		n := h.PackageWorker.StartWaiting(context.Background(), slots)
		started += n
	}
	if started > 0 {
		log.Printf("transcode worker: started=%d running_bg=%d max_bg=%d waiting=%d",
			started, running+started, settings.MaxBackgroundConcurrent, waiting)
	}
}

func (h *Handler) loadTranscoderSettings() transcode.Settings {
	if h == nil || h.App == nil || h.App.DB == nil {
		return transcode.DefaultSettings()
	}
	var raw sql.NullString
	if err := h.App.DB.QueryRow(`SELECT options_json FROM system_options WHERE id = 1`).Scan(&raw); err != nil {
		return transcode.DefaultSettings()
	}
	return transcode.SettingsFromOptionsJSON(raw.String)
}

func (h *Handler) kickTranscodeWorker() {
	h.runTranscodeWorkerOnce()
}

func (h *Handler) countRunningBackgroundTranscodeJobs() int {
	var n int
	_ = h.App.DB.QueryRow(`
		SELECT (
			(SELECT COUNT(1) FROM transcode_task WHERE status = 'running') +
			(SELECT COUNT(1) FROM package_task WHERE status = 'running')
		)
	`).Scan(&n)
	return n
}

func (h *Handler) instantTranscodeSlotsAvailable() bool {
	if h == nil || h.SessionManager == nil {
		return true
	}
	settings := h.loadTranscoderSettings()
	return transcode.InstantSlots(settings, h.SessionManager.ActiveSessionCount())
}
