package pretranscode

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"

	"knox-media/internal/coreiface"
)

// PlaybackService implements coreiface.PretranscodeModule.
type PlaybackService struct {
	DB           *sql.DB
	TranscodeDir string
}

// GetPretranscodeStatus aggregates rendition state for a file (SRS 3.3.1).
func (p *PlaybackService) GetPretranscodeStatus(ctx context.Context, fileID string) (*coreiface.PretranscodeStatus, error) {
	if fileID == "" {
		return &coreiface.PretranscodeStatus{Available: false}, nil
	}
	row := p.DB.QueryRow(`SELECT pt.preset_id, COALESCE(p.name,''), pt.output_format, COALESCE(pt.encryption_mode,'none'), COALESCE(pt.output_path,'')
		FROM pretranscode_task_meta pt
		JOIN transcode_task t ON t.id = pt.task_id
		JOIN transcode_preset p ON p.id = pt.preset_id
		WHERE t.file_id = ? AND t.status IN ('waiting','running','done')
		ORDER BY t.id DESC LIMIT 1`, fileID)
	var presetID int64
	var presetName, outFormat, encMode, outputPath string
	if err := row.Scan(&presetID, &presetName, &outFormat, &encMode, &outputPath); err == sql.ErrNoRows {
		return &coreiface.PretranscodeStatus{Available: false}, nil
	} else if err != nil {
		return nil, err
	}
	rows, err := p.DB.Query(`SELECT rendition_name, status, progress FROM pretranscode_rendition_job
		WHERE task_id IN (SELECT id FROM transcode_task WHERE file_id = ?) ORDER BY id`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var renditions []coreiface.RenditionStatus
	anyDone := false
	for rows.Next() {
		var r coreiface.RenditionStatus
		if err := rows.Scan(&r.Name, &r.Status, &r.Progress); err != nil {
			return nil, err
		}
		if r.Status == "done" {
			anyDone = true
		}
		renditions = append(renditions, r)
	}
	return &coreiface.PretranscodeStatus{
		Available:    anyDone,
		PresetName:   presetName,
		Renditions:   renditions,
		Encryption:   encMode,
		OutputFormat: outFormat,
	}, nil
}

// GetMasterPlaylist returns the master.m3u8 path for a pretranscoded HLS
// file. Empty string when no pretranscode output exists.
func (p *PlaybackService) GetMasterPlaylist(ctx context.Context, fileID string) (string, error) {
	var outputPath string
	err := p.DB.QueryRow(`SELECT pt.output_path FROM pretranscode_task_meta pt
		JOIN transcode_task t ON t.id = pt.task_id
		WHERE t.file_id = ? AND t.status = 'done' AND pt.output_format = 'hls'
		ORDER BY t.id DESC LIMIT 1`, fileID).Scan(&outputPath)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	// The output_path is the per-task root; master.m3u8 lives inside.
	master := filepath.Join(outputPath, "master.m3u8")
	if _, err := os.Stat(master); err != nil {
		return "", nil
	}
	return master, nil
}

// HasPretranscodeOutput returns true when any done rendition exists for the
// file (SRS PLAY-01 decision gate).
func (p *PlaybackService) HasPretranscodeOutput(fileID string) bool {
	if fileID == "" {
		return false
	}
	var n int
	_ = p.DB.QueryRow(`SELECT COUNT(1) FROM pretranscode_rendition_job j
		JOIN transcode_task t ON t.id = j.task_id
		WHERE t.file_id = ? AND j.status = 'done' LIMIT 1`, fileID).Scan(&n)
	return n > 0
}

// OnMediaDeleted cascades cleanup of pretranscode artifacts (SRS STOR-04).
func (p *PlaybackService) OnMediaDeleted(ctx context.Context, mediaID int64, fileIDs []string) error {
	for _, fid := range fileIDs {
		// Best-effort filesystem cleanup of output directories.
		rows, err := p.DB.Query(`SELECT DISTINCT pt.output_path FROM pretranscode_task_meta pt
			JOIN transcode_task t ON t.id = pt.task_id WHERE t.file_id = ?`, fid)
		if err != nil {
			continue
		}
		var paths []string
		for rows.Next() {
			var s string
			if rows.Scan(&s) == nil && s != "" {
				paths = append(paths, s)
			}
		}
		rows.Close()
		for _, pp := range paths {
			_ = os.RemoveAll(pp)
		}
		// DB rows cascade via FK on transcode_task deletion. The media_delete
		// handler already deletes transcode_task rows; this is a safety net.
		_, _ = p.DB.Exec(`DELETE FROM pretranscode_rendition_job WHERE task_id IN (SELECT id FROM transcode_task WHERE file_id = ?)`, fid)
		_, _ = p.DB.Exec(`DELETE FROM pretranscode_task_meta WHERE task_id IN (SELECT id FROM transcode_task WHERE file_id = ?)`, fid)
	}
	return nil
}

// PretranscodeStatus is the playback-facing status shape (mirrors
// coreiface.PretranscodeStatus but local to this package for JSON tags).
type PretranscodeStatus = coreiface.PretranscodeStatus

// RenditionStatus is a single rendition's playback status.
type RenditionStatus = coreiface.RenditionStatus
