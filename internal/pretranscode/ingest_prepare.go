package pretranscode

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"knox-media/internal/coreiface"
)

type ingestPreparePlanner struct{}

type ingestPrepareTaskJob struct {
	RenditionID   int64           `json:"rendition_id"`
	RenditionName string          `json:"rendition_name"`
	Config        json.RawMessage `json:"config_snapshot"`
}
type ingestPrepareJobSnapshot struct {
	Preset     Preset    `json:"preset"`
	Rendition  Rendition `json:"rendition"`
	OutputPath string    `json:"output_path"`
	Priority   string    `json:"priority"`
}

func (ingestPreparePlanner) PlanIngestPrepareTx(ctx context.Context, tx *sql.Tx, mediaID, runID, stepID, generation int64) error {
	if tx == nil || mediaID <= 0 || runID <= 0 || stepID <= 0 || generation <= 0 {
		return errors.New("pretranscode ingest prepare: invalid linkage")
	}
	var fileID, sourcePath string
	if err := tx.QueryRowContext(ctx, `SELECT file_id,COALESCE(file_path,'') FROM media WHERE id=?`, mediaID).Scan(&fileID, &sourcePath); err != nil {
		return fmt.Errorf("pretranscode ingest prepare: load media: %w", err)
	}
	var linked int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM media_ingest_step s JOIN media_ingest_run r ON r.id=s.run_id WHERE s.id=? AND s.run_id=? AND s.media_id=? AND s.generation=? AND s.step_type='prepare' AND r.media_id=s.media_id AND r.generation=s.generation`, stepID, runID, mediaID, generation).Scan(&linked); err != nil {
		return fmt.Errorf("pretranscode ingest prepare: validate linkage: %w", err)
	}
	if linked != 1 {
		return errors.New("pretranscode ingest prepare: prepare step linkage mismatch")
	}
	var preset Preset
	var builtin, enabled, hw int
	if err := tx.QueryRowContext(ctx, `SELECT id,name,COALESCE(description,''),output_format,COALESCE(encryption_mode,'none'),video_codec,COALESCE(video_preset,''),COALESCE(video_crf,0),COALESCE(video_maxrate,''),COALESCE(video_bufsize,''),COALESCE(video_profile,''),COALESCE(video_gop,0),COALESCE(video_pix_fmt,''),COALESCE(NULLIF(audio_codec,''),'aac'),audio_bitrate,COALESCE(audio_channels,2),COALESCE(audio_sample_rate,48000),COALESCE(hw_fallback,1),is_builtin,is_enabled,COALESCE(sort_order,0),COALESCE(output_dir_mode,'source'),COALESCE(output_dir_custom,'') FROM transcode_preset WHERE is_enabled=1 AND EXISTS (SELECT 1 FROM preset_rendition r WHERE r.preset_id=transcode_preset.id) ORDER BY is_builtin DESC,sort_order,id LIMIT 1`).Scan(&preset.ID, &preset.Name, &preset.Description, &preset.OutputFormat, &preset.EncryptionMode, &preset.VideoCodec, &preset.VideoPreset, &preset.VideoCRF, &preset.VideoMaxrate, &preset.VideoBufsize, &preset.VideoProfile, &preset.VideoGOP, &preset.VideoPixFmt, &preset.AudioCodec, &preset.AudioBitrate, &preset.AudioChannels, &preset.AudioSampleRate, &hw, &builtin, &enabled, &preset.SortOrder, &preset.OutputDirMode, &preset.OutputDirCustom); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("pretranscode ingest prepare: no enabled preset with renditions")
		}
		return fmt.Errorf("pretranscode ingest prepare: select preset: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,preset_id,name,height,COALESCE(width,0),video_bitrate,COALESCE(audio_bitrate,''),COALESCE(bandwidth,0),COALESCE(sort_order,0) FROM preset_rendition WHERE preset_id=? ORDER BY height,sort_order,id`, preset.ID)
	if err != nil {
		return fmt.Errorf("pretranscode ingest prepare: load renditions: %w", err)
	}
	var renditions []Rendition
	var names []string
	for rows.Next() {
		var r Rendition
		if err = rows.Scan(&r.ID, &r.PresetID, &r.Name, &r.Height, &r.Width, &r.VideoBitrate, &r.AudioBitrate, &r.Bandwidth, &r.SortOrder); err != nil {
			rows.Close()
			return err
		}
		renditions = append(renditions, r)
		names = append(names, r.Name)
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if len(renditions) == 0 {
		return errors.New("pretranscode ingest prepare: selected preset has no renditions")
	}
	preset.HWFallback = hw == 1
	preset.IsBuiltin = builtin == 1
	preset.IsEnabled = enabled == 1
	outputRoot := computeIngestOutputRoot(preset.OutputDirMode, preset.OutputDirCustom, fileID, preset.ID, sourcePath)
	res, err := tx.ExecContext(ctx, `INSERT INTO transcode_task(file_id,quality,status,progress,task_type,preset_id,started_at,completed_at,ingest_run_id,ingest_step_id,generation) VALUES(?,?,'waiting',0,'pretranscode',?,NULL,NULL,?,?,?)`, fileID, strings.Join(names, "+"), preset.ID, runID, stepID, generation)
	if err != nil {
		return fmt.Errorf("pretranscode ingest prepare: insert task: %w", err)
	}
	taskID, err := res.LastInsertId()
	if err != nil {
		return err
	}
	var taskJobs []ingestPrepareTaskJob
	for _, r := range renditions {
		snapshot, marshalErr := json.Marshal(ingestPrepareJobSnapshot{Preset: preset, Rendition: r, OutputPath: outputRoot, Priority: "normal"})
		if marshalErr != nil {
			return fmt.Errorf("pretranscode ingest prepare: marshal task snapshot: %w", marshalErr)
		}
		taskJobs = append(taskJobs, ingestPrepareTaskJob{RenditionID: r.ID, RenditionName: r.Name, Config: snapshot})
	}
	taskSnapshot, err := json.Marshal(taskJobs)
	if err != nil {
		return fmt.Errorf("pretranscode ingest prepare: marshal jobs snapshot: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO pretranscode_task_meta(task_id,preset_id,output_format,encryption_mode,priority,output_path,ingest_jobs_snapshot_json) VALUES(?,?,?,?, 'normal',?,?)`, taskID, preset.ID, preset.OutputFormat, preset.EncryptionMode, outputRoot, string(taskSnapshot)); err != nil {
		return fmt.Errorf("pretranscode ingest prepare: insert meta: %w", err)
	}
	for _, r := range renditions {
		snapshot, marshalErr := json.Marshal(ingestPrepareJobSnapshot{Preset: preset, Rendition: r, OutputPath: outputRoot, Priority: "normal"})
		if marshalErr != nil {
			return fmt.Errorf("pretranscode ingest prepare: marshal snapshot: %w", marshalErr)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO pretranscode_rendition_job(task_id,rendition_id,rendition_name,status,config_snapshot_json) VALUES(?,?,?,'waiting',?)`, taskID, r.ID, r.Name, string(snapshot)); err != nil {
			return fmt.Errorf("pretranscode ingest prepare: insert rendition: %w", err)
		}
	}
	return nil
}

func computeIngestOutputRoot(mode, customDir, fileID string, presetID int64, sourcePath string) string {
	presetPart := fmt.Sprintf("preset%d", presetID)
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "custom":
		if strings.TrimSpace(customDir) != "" {
			return filepath.Join(customDir, fileID, presetPart)
		}
	case "source", "":
		if strings.TrimSpace(sourcePath) != "" {
			stem := strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath))
			return filepath.Join(filepath.Dir(sourcePath), stem+".pretranscode", presetPart)
		}
	}
	return filepath.Join(".", fileID, presetPart)
}

func init() { coreiface.RegisterIngestPreparePlanner(ingestPreparePlanner{}) }

var _ coreiface.IngestPreparePlanner = ingestPreparePlanner{}
