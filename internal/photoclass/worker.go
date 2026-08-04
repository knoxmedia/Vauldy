package photoclass

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"knox-media/internal/config"
	"knox-media/internal/keystore"
	"knox-media/internal/imagethumb"
	"knox-media/internal/photoparse"
)

// Worker classifies photo library images asynchronously.
type Worker struct {
	DB         *sql.DB
	Vault      *keystore.Vault
	MediaRoot  string
	FFmpegPath string
	PreviewDir string
	Cfg        func() config.PhotoClassifyConfig
	mu         sync.Mutex
	running    map[int64]bool
}

func NewWorker(db *sql.DB, vault *keystore.Vault, mediaRoot, ffmpegPath, previewDir string, cfgFn func() config.PhotoClassifyConfig) *Worker {
	return &Worker{
		DB: db, Vault: vault, MediaRoot: mediaRoot, FFmpegPath: ffmpegPath, PreviewDir: previewDir,
		Cfg: cfgFn, running: map[int64]bool{},
	}
}

func (w *Worker) onnxConfig() ONNXConfig {
	cfg := config.PhotoClassifyConfig{}
	if w.Cfg != nil {
		cfg = w.Cfg()
	}
	return ONNXConfig{
		Engine:     cfg.Engine,
		PythonPath: cfg.PythonPath,
		ScriptPath: ResolveScriptPath(w.MediaRoot, cfg.ScriptPath),
		ModelPath:  ResolveScriptPath(w.MediaRoot, cfg.ModelPath),
		LabelsPath: ResolveScriptPath(w.MediaRoot, cfg.LabelsPath),
	}
}

// Enqueue upserts a pending photo_classify_task.
func (w *Worker) Enqueue(mediaID int64) error {
	_, err := w.DB.Exec(`
		INSERT INTO photo_classify_task (media_id, status, created_at, updated_at)
		VALUES (?, 'pending', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(media_id) DO UPDATE SET
			status = 'pending',
			message = NULL,
			started_at = NULL,
			finished_at = NULL,
			updated_at = CURRENT_TIMESTAMP
	`, mediaID)
	return err
}

// EnsurePendingIfPhoto enqueues when media is an image in a photo library without tags.
func (w *Worker) EnsurePendingIfPhoto(mediaID int64, fileType string) error {
	if w == nil || w.DB == nil || mediaID <= 0 || fileType != "image" {
		return nil
	}
	var libraryType, metaJSON sql.NullString
	if err := w.DB.QueryRow(`
		SELECT l.type, COALESCE(m.meta_json,'')
		FROM media m JOIN library l ON l.id = m.library_id
		WHERE m.id = ?
	`, mediaID).Scan(&libraryType, &metaJSON); err != nil {
		return err
	}
	if !photoparse.IsPhotoLibraryType(libraryType.String) {
		return nil
	}
	tags, _, _ := ReadPhotoTags(metaJSON.String)
	if len(tags) > 0 {
		return nil
	}
	_, err := w.DB.Exec(`
		INSERT OR IGNORE INTO photo_classify_task (media_id, status, created_at, updated_at)
		VALUES (?, 'pending', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	`, mediaID)
	return err
}

// RunBatch processes pending tasks.
func (w *Worker) RunBatch(ctx context.Context, limit int) (done, failed int) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := w.DB.Query(`
		SELECT media_id FROM photo_classify_task
		WHERE status IN ('pending', 'failed', 'running')
		ORDER BY updated_at ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return 0, 0
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		select {
		case <-ctx.Done():
			return done, failed
		default:
		}
		if err := w.Process(ctx, id); err != nil {
			failed++
		} else {
			done++
		}
	}
	return done, failed
}

// Process classifies one media item.
func (w *Worker) Process(ctx context.Context, mediaID int64) error {
	if w == nil || w.DB == nil || mediaID <= 0 {
		return fmt.Errorf("worker unavailable")
	}
	w.mu.Lock()
	if w.running[mediaID] {
		w.mu.Unlock()
		return nil
	}
	w.running[mediaID] = true
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.running, mediaID)
		w.mu.Unlock()
	}()

	_, _ = w.DB.Exec(`
		UPDATE photo_classify_task SET status='running', started_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		WHERE media_id = ?
	`, mediaID)

	var filePath, metaJSON, title, format sql.NullString
	var wVal, hVal sql.NullInt64
	err := w.DB.QueryRow(`
		SELECT file_path, COALESCE(meta_json,''), title, format, width, height
		FROM media WHERE id = ? AND file_type = 'image'
	`, mediaID).Scan(&filePath, &metaJSON, &title, &format, &wVal, &hVal)
	if err != nil {
		w.failTask(mediaID, err.Error())
		return err
	}

	photoCache := filepath.Join(w.PreviewDir, "photos")
	thumbPath := imagethumb.ExpectedPaths(photoCache, mediaID).Thumb
	if w.FFmpegPath != "" && strings.TrimSpace(filePath.String) != "" {
		if _, err := imagethumb.Ensure(context.Background(), w.DB, w.Vault, nil, w.FFmpegPath, filePath.String, photoCache, mediaID); err == nil {
			thumbPath = imagethumb.ExpectedPaths(photoCache, mediaID).Thumb
		}
	}

	var photo photoparse.PhotoMeta
	if strings.TrimSpace(metaJSON.String) != "" {
		var root struct {
			Photo photoparse.PhotoMeta `json:"photo"`
		}
		_ = json.Unmarshal([]byte(metaJSON.String), &root)
		photo = root.Photo
	}

	in := Input{
		FilePath:    filePath.String,
		ThumbPath:   thumbPath,
		Title:       title.String,
		Width:       int(wVal.Int64),
		Height:      int(hVal.Int64),
		CameraMake:  photo.CameraMake,
		CameraModel: photo.CameraModel,
		Format:      format.String,
	}

	res, classifyErr := ClassifyWithONNX(ctx, w.onnxConfig(), in)
	if classifyErr != nil {
		res = Classify(in)
	}

	_, manualOverride, manualTags := readManual(metaJSON.String)
	merged := MergeTagsIntoMetaJSON(metaJSON.String, res.Tags, res.Engine, manualOverride, manualTags)
	_, err = w.DB.Exec(`UPDATE media SET meta_json = ? WHERE id = ?`, merged, mediaID)
	if err != nil {
		w.failTask(mediaID, err.Error())
		return err
	}

	msg := fmt.Sprintf("tags=%d engine=%s", len(res.Tags), res.Engine)
	_, _ = w.DB.Exec(`
		UPDATE photo_classify_task SET status='done', message=?, finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		WHERE media_id = ?
	`, msg, mediaID)
	return classifyErr
}

func (w *Worker) failTask(mediaID int64, msg string) {
	_, _ = w.DB.Exec(`
		UPDATE photo_classify_task SET status='failed', message=?, finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		WHERE media_id = ?
	`, msg, mediaID)
}

func readManual(raw string) (aiTags []string, manualOverride bool, manualTags []string) {
	_, aiTags, manualOverride = ReadPhotoTags(raw)
	if manualOverride {
		var root struct {
			Photo struct {
				ManualTags []string `json:"manual_tags"`
			} `json:"photo"`
		}
		_ = json.Unmarshal([]byte(raw), &root)
		manualTags = root.Photo.ManualTags
	}
	return aiTags, manualOverride, manualTags
}

// LibraryProgress returns classification stats for a photo library.
func (w *Worker) LibraryProgress(libraryID int64) (total, classified, pending int64, err error) {
	if w == nil || w.DB == nil {
		return 0, 0, 0, fmt.Errorf("worker unavailable")
	}
	err = w.DB.QueryRow(`
		SELECT COUNT(1) FROM media WHERE library_id = ? AND file_type = 'image' AND status = 'active'
	`, libraryID).Scan(&total)
	if err != nil {
		return
	}
	err = w.DB.QueryRow(`
		SELECT COUNT(1) FROM media
		WHERE library_id = ? AND file_type = 'image' AND status = 'active'
		  AND COALESCE(json_array_length(json_extract(meta_json, '$.photo.tags')), 0) > 0
	`, libraryID).Scan(&classified)
	if err != nil {
		return
	}
	err = w.DB.QueryRow(`
		SELECT COUNT(1) FROM photo_classify_task t
		JOIN media m ON m.id = t.media_id
		WHERE m.library_id = ? AND t.status IN ('pending', 'running', 'failed')
	`, libraryID).Scan(&pending)
	return
}

// EnqueueLibrary queues all untagged images in a library.
func (w *Worker) EnqueueLibrary(libraryID int64) (int64, error) {
	rows, err := w.DB.Query(`
		SELECT id FROM media
		WHERE library_id = ? AND file_type = 'image' AND status = 'active'
		  AND COALESCE(json_array_length(json_extract(meta_json, '$.photo.tags')), 0) = 0
	`, libraryID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	return w.enqueueRows(rows)
}

// EnqueueLibraryAll re-queues every active image in a photo library (force reclassify).
func (w *Worker) EnqueueLibraryAll(libraryID int64) (int64, error) {
	rows, err := w.DB.Query(`
		SELECT id FROM media
		WHERE library_id = ? AND file_type = 'image' AND status = 'active'
	`, libraryID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	return w.enqueueRows(rows)
}

func (w *Worker) enqueueRows(rows *sql.Rows) (int64, error) {
	var n int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) != nil || id <= 0 {
			continue
		}
		if e := w.Enqueue(id); e == nil {
			n++
		}
	}
	return n, nil
}

// ApplyManualTags sets user-corrected tags.
func ApplyManualTags(db *sql.DB, mediaID int64, tags []string) error {
	if db == nil || mediaID <= 0 {
		return fmt.Errorf("invalid args")
	}
	var metaJSON sql.NullString
	if err := db.QueryRow(`SELECT COALESCE(meta_json,'') FROM media WHERE id = ?`, mediaID).Scan(&metaJSON); err != nil {
		return err
	}
	clean := dedupeTags(tags)
	_, aiTags, _ := ReadPhotoTags(metaJSON.String)
	merged := MergeTagsIntoMetaJSON(metaJSON.String, aiTags, "manual", true, clean)
	_, err := db.Exec(`UPDATE media SET meta_json = ? WHERE id = ?`, merged, mediaID)
	return err
}
