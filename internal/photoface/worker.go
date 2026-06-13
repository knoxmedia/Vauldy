package photoface

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"knox-media/internal/config"
	"knox-media/internal/keystore"
	"knox-media/internal/imagethumb"
	"knox-media/internal/photoparse"
)

// Worker detects faces and assigns person clusters asynchronously.
type Worker struct {
	DB         *sql.DB
	Vault      *keystore.Vault
	MediaRoot  string
	PreviewDir string
	FFmpegPath string
	Cfg        func() config.PhotoFaceConfig
	mu         sync.Mutex
	busy       map[int64]bool
}

func NewWorker(db *sql.DB, vault *keystore.Vault, mediaRoot, ffmpegPath, previewDir string, cfgFn func() config.PhotoFaceConfig) *Worker {
	return &Worker{
		DB: db, Vault: vault, MediaRoot: mediaRoot, FFmpegPath: ffmpegPath, PreviewDir: previewDir,
		Cfg: cfgFn, busy: map[int64]bool{},
	}
}

func (w *Worker) detectConfig() DetectConfig {
	cfg := config.PhotoFaceConfig{}
	if w.Cfg != nil {
		cfg = w.Cfg()
	}
	return ConfigFrom(cfg, w.MediaRoot)
}

func (w *Worker) similarityThreshold() float32 {
	cfg := config.PhotoFaceConfig{}
	if w.Cfg != nil {
		cfg = w.Cfg()
	}
	if cfg.SimilarityThreshold > 0 {
		return cfg.SimilarityThreshold
	}
	return 0.45
}

func (w *Worker) Enqueue(mediaID, libraryID int64) error {
	if w == nil || w.DB == nil || mediaID <= 0 || libraryID <= 0 {
		return fmt.Errorf("invalid enqueue args")
	}
	_, err := w.DB.Exec(`
		INSERT INTO photo_face_task (media_id, library_id, status, created_at, updated_at)
		VALUES (?, ?, 'pending', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(media_id) DO UPDATE SET
			library_id = excluded.library_id,
			status = 'pending',
			message = NULL,
			started_at = NULL,
			finished_at = NULL,
			updated_at = CURRENT_TIMESTAMP
	`, mediaID, libraryID)
	return err
}

func (w *Worker) EnqueueLibraryAll(libraryID int64) (int64, error) {
	rows, err := w.DB.Query(`
		SELECT id FROM media
		WHERE library_id = ? AND file_type = 'image' AND status = 'active'
	`, libraryID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var n int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) != nil || id <= 0 {
			continue
		}
		if e := w.Enqueue(id, libraryID); e == nil {
			n++
		}
	}
	return n, nil
}

func (w *Worker) LibraryProgress(libraryID int64) (total, processed, withFaces, pending int64, err error) {
	if w == nil || w.DB == nil {
		return 0, 0, 0, 0, fmt.Errorf("worker unavailable")
	}
	if err = w.DB.QueryRow(`
		SELECT COUNT(1) FROM media WHERE library_id = ? AND file_type = 'image' AND status = 'active'
	`, libraryID).Scan(&total); err != nil {
		return
	}
	if err = w.DB.QueryRow(`
		SELECT COUNT(1) FROM photo_face_task
		WHERE library_id = ? AND status = 'done'
	`, libraryID).Scan(&processed); err != nil {
		return
	}
	if err = w.DB.QueryRow(`
		SELECT COUNT(DISTINCT media_id) FROM photo_face WHERE library_id = ?
	`, libraryID).Scan(&withFaces); err != nil {
		return
	}
	err = w.DB.QueryRow(`
		SELECT COUNT(1) FROM photo_face_task
		WHERE library_id = ? AND status IN ('pending', 'running', 'failed')
	`, libraryID).Scan(&pending)
	return
}

// EnsurePendingIfPhoto enqueues face detection for new photo library images.
func (w *Worker) EnsurePendingIfPhoto(mediaID int64, fileType string) error {
	if w == nil || w.DB == nil || mediaID <= 0 || fileType != "image" {
		return nil
	}
	var libraryID int64
	var libraryType sql.NullString
	if err := w.DB.QueryRow(`
		SELECT m.library_id, l.type FROM media m JOIN library l ON l.id = m.library_id WHERE m.id = ?
	`, mediaID).Scan(&libraryID, &libraryType); err != nil {
		return err
	}
	if libraryID <= 0 || !photoparse.IsPhotoLibraryType(libraryType.String) {
		return nil
	}
	_, err := w.DB.Exec(`
		INSERT OR IGNORE INTO photo_face_task (media_id, library_id, status, created_at, updated_at)
		VALUES (?, ?, 'pending', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	`, mediaID, libraryID)
	return err
}

func (w *Worker) RunBatch(ctx context.Context, limit int) (done, failed int) {
	if w == nil || w.DB == nil || limit <= 0 {
		return 0, 0
	}
	rows, err := w.DB.Query(`
		SELECT media_id FROM photo_face_task
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

func (w *Worker) Process(ctx context.Context, mediaID int64) error {
	if w == nil || w.DB == nil || mediaID <= 0 {
		return fmt.Errorf("invalid media id")
	}
	w.mu.Lock()
	if w.busy[mediaID] {
		w.mu.Unlock()
		return nil
	}
	w.busy[mediaID] = true
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.busy, mediaID)
		w.mu.Unlock()
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	_, _ = w.DB.Exec(`
		UPDATE photo_face_task SET status='running', started_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		WHERE media_id = ?`, mediaID)

	var libraryID int64
	var path string
	if err := w.DB.QueryRow(`
		SELECT library_id, file_path FROM media WHERE id = ?
	`, mediaID).Scan(&libraryID, &path); err != nil {
		w.fail(mediaID, err.Error())
		return err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		w.fail(mediaID, "empty file path")
		return fmt.Errorf("empty file path")
	}

	detectPath, err := w.ensureDetectImage(mediaID, path)
	if err != nil {
		w.fail(mediaID, err.Error())
		return err
	}

	res, err := RunDetect(ctx, w.detectConfig(), detectPath)
	if err != nil {
		w.fail(mediaID, err.Error())
		return err
	}

	if err := w.replaceFaces(ctx, libraryID, mediaID, res); err != nil {
		w.fail(mediaID, err.Error())
		return err
	}
	msg := ""
	if len(res.Faces) == 0 {
		msg = "no faces"
	}
	w.complete(mediaID, msg)
	return nil
}

func (w *Worker) replaceFaces(ctx context.Context, libraryID, mediaID int64, res *DetectResult) error {
	_ = ctx
	tx, err := w.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var oldPersons []int64
	rows, err := tx.Query(`SELECT DISTINCT person_id FROM photo_face WHERE media_id = ? AND person_id IS NOT NULL`, mediaID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var pid sql.NullInt64
		if rows.Scan(&pid) == nil && pid.Valid {
			oldPersons = append(oldPersons, pid.Int64)
		}
	}
	rows.Close()

	if _, err := tx.Exec(`DELETE FROM photo_face WHERE media_id = ?`, mediaID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, pid := range oldPersons {
		w.refreshPersonStats(pid)
	}

	threshold := w.similarityThreshold()
	for _, face := range res.Faces {
		if len(face.Embedding) == 0 {
			continue
		}
		emb := floats64To32(face.Embedding)
		bbox := face.BBox
		quality := face.Score
		resInsert, err := w.DB.Exec(`
			INSERT INTO photo_face (media_id, library_id, bbox_x, bbox_y, bbox_w, bbox_h, embedding, quality, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
			mediaID, libraryID,
			bbox[0], bbox[1], bbox[2]-bbox[0], bbox[3]-bbox[1],
			packEmbedding(normalizeEmbedding(emb)), quality)
		if err != nil {
			return err
		}
		faceID, err := resInsert.LastInsertId()
		if err != nil {
			return err
		}
		if err := AssignPerson(w.DB, libraryID, faceID, emb, threshold); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) refreshPersonStats(personID int64) {
	var cnt int
	var mediaCnt int
	_ = w.DB.QueryRow(`SELECT COUNT(1), COUNT(DISTINCT media_id) FROM photo_face WHERE person_id = ?`, personID).Scan(&cnt, &mediaCnt)
	if cnt == 0 {
		_, _ = w.DB.Exec(`DELETE FROM photo_person WHERE id = ?`, personID)
		return
	}
	var cover sql.NullInt64
	_ = w.DB.QueryRow(`
		SELECT id FROM photo_face WHERE person_id = ?
		ORDER BY quality DESC, id ASC LIMIT 1`, personID).Scan(&cover)
	rows, err := w.DB.Query(`SELECT embedding FROM photo_face WHERE person_id = ? AND embedding IS NOT NULL`, personID)
	if err != nil {
		return
	}
	defer rows.Close()
	var centroid []float32
	n := 0
	for rows.Next() {
		var b []byte
		if rows.Scan(&b) != nil {
			continue
		}
		centroid = mergeCentroid(centroid, n, unpackEmbedding(b))
		n++
	}
	var coverVal any
	if cover.Valid {
		coverVal = cover.Int64
	}
	_, _ = w.DB.Exec(`
		UPDATE photo_person SET face_count = ?, media_count = ?, cover_face_id = ?, embedding = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`, cnt, mediaCnt, coverVal, packEmbedding(centroid), personID)
}

func (w *Worker) photoCacheDir() string {
	return filepath.Join(w.PreviewDir, "photos")
}

func (w *Worker) ensureDetectImage(mediaID int64, srcPath string) (string, error) {
	cacheDir := w.photoCacheDir()
	paths := imagethumb.ExpectedPaths(cacheDir, mediaID)
	if st, err := os.Stat(paths.Thumb); err == nil && !st.IsDir() && st.Size() > 0 {
		return filepath.Abs(paths.Thumb)
	}
	srcPath = strings.TrimSpace(srcPath)
	if srcPath == "" {
		return "", fmt.Errorf("empty file path")
	}
	if _, err := os.Stat(srcPath); err != nil {
		return "", fmt.Errorf("source image missing: %w", err)
	}
	if strings.TrimSpace(w.FFmpegPath) == "" {
		return "", fmt.Errorf("ffmpeg path empty")
	}
	out, err := imagethumb.Ensure(context.Background(), w.DB, w.Vault, w.FFmpegPath, srcPath, cacheDir, mediaID)
	if err != nil {
		return "", err
	}
	return filepath.Abs(out.Thumb)
}

func (w *Worker) complete(mediaID int64, msg string) {
	var m any
	if msg != "" {
		m = msg
	}
	_, _ = w.DB.Exec(`
		UPDATE photo_face_task SET status='done', message=?, finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		WHERE media_id = ?`, m, mediaID)
}

func (w *Worker) fail(mediaID int64, msg string) {
	_, _ = w.DB.Exec(`
		UPDATE photo_face_task SET status='failed', message=?, finished_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		WHERE media_id = ?`, msg, mediaID)
}

func nullInt64(v sql.NullInt64) any {
	if v.Valid {
		return v.Int64
	}
	return nil
}
