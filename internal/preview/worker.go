package preview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"knox-media/internal/keystore"
	"knox-media/internal/storage"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
)

type Info struct {
	Status     string
	Interval   int
	ThumbCount int
	Width      int
	Height     int
	SpritePath string
	VTTPath    string
	Error      string
}

type FFmpegRunner func(context.Context, *sql.DB, *keystore.Vault, string, int64, string, float64, float64, []string, []string, string) ([]byte, error)

type Worker struct {
	DB         *sql.DB
	Vault      *keystore.Vault
	Derived    *storage.DerivedAssetStore
	FFmpegPath string
	PreviewDir string
	mu         sync.Mutex
	running    map[int64]bool
	runFFmpeg  FFmpegRunner
}

func NewWorker(db *sql.DB, vault *keystore.Vault, derived *storage.DerivedAssetStore, ffmpegPath, previewDir string) *Worker {
	return &Worker{
		DB:         db,
		Vault:      vault,
		Derived:    derived,
		PreviewDir: previewDir,
		FFmpegPath: ffmpegPath,
		running:    map[int64]bool{},
		runFFmpeg:  storage.RunFFmpeg,
	}
}

func (w *Worker) Ensure(ctx context.Context, mediaID int64, inputPath string, durationSec int64) (Info, error) {
	var info Info
	var interval, count, width, height sql.NullInt64
	var status, sprite, vtt, errMsg sql.NullString
	qerr := w.DB.QueryRow(
		`SELECT status, interval_sec, thumb_count, thumb_width, thumb_height, sprite_path, vtt_path, error_message FROM preview_task WHERE media_id = ? LIMIT 1`,
		mediaID,
	).Scan(&status, &interval, &count, &width, &height, &sprite, &vtt, &errMsg)
	if qerr == nil {
		info = Info{
			Status:     status.String,
			Interval:   int(interval.Int64),
			ThumbCount: int(count.Int64),
			Width:      int(width.Int64),
			Height:     int(height.Int64),
			SpritePath: sprite.String,
			VTTPath:    vtt.String,
			Error:      errMsg.String,
		}
		if info.Status == "ready" {
			if st, err := os.Stat(info.SpritePath); err == nil && !st.IsDir() {
				if st2, err2 := os.Stat(info.VTTPath); err2 == nil && !st2.IsDir() {
					return info, nil
				}
			}
		}
		if info.Status == "failed" {
			return info, nil
		}
	}
	if qerr != nil && qerr != sql.ErrNoRows {
		return info, qerr
	}
	intervalSec, countNum := TaskParameters(durationSec)
	if err := UpsertWaitingPreviewTask(w.DB, mediaID, intervalSec, countNum); err != nil {
		return info, err
	}
	w.startOnce(ctx, mediaID, inputPath, durationSec, intervalSec, countNum)
	return Info{Status: "waiting", Interval: intervalSec, ThumbCount: countNum, Width: 240, Height: 135}, nil
}

// UpsertWaitingPreviewTask queues preview work for media_id. Existing failed rows are left unchanged.
// TaskParameters computes the persisted preview sampling parameters for a duration.
func TaskParameters(durationSec int64) (intervalSec, countNum int) {
	if durationSec <= 0 {
		durationSec = 600
	}
	intervalSec = int(math.Ceil(float64(durationSec) / 100.0))
	if intervalSec < 5 {
		intervalSec = 5
	}
	countNum = int(math.Ceil(float64(durationSec) / float64(intervalSec)))
	if countNum < 1 {
		countNum = 1
	}
	if countNum > 100 {
		countNum = 100
	}
	return intervalSec, countNum
}

// EnsureWaitingTask inserts a fully initialized waiting row without changing an existing task.
func EnsureWaitingTask(ctx context.Context, db *sql.DB, mediaID int64, durationSec int64) error {
	if db == nil {
		return errors.New("preview worker: database is not configured")
	}
	intervalSec, countNum := TaskParameters(durationSec)
	_, err := db.ExecContext(ctx, `INSERT INTO preview_task(media_id,status,interval_sec,thumb_count,thumb_width,thumb_height,updated_at) VALUES(?,'waiting',?,?,240,135,CURRENT_TIMESTAMP) ON CONFLICT(media_id) DO NOTHING`, mediaID, intervalSec, countNum)
	return err
}

func UpsertWaitingPreviewTask(db *sql.DB, mediaID int64, intervalSec, countNum int) error {
	if db == nil || mediaID <= 0 {
		return nil
	}
	_, err := db.Exec(`
		INSERT INTO preview_task (media_id, status, interval_sec, thumb_count, thumb_width, thumb_height, updated_at)
		VALUES (?, 'waiting', ?, ?, 240, 135, CURRENT_TIMESTAMP)
		ON CONFLICT(media_id) DO UPDATE SET
		  status = CASE WHEN preview_task.status = 'failed' THEN preview_task.status ELSE 'waiting' END,
		  interval_sec = excluded.interval_sec,
		  thumb_count = excluded.thumb_count,
		  updated_at = CURRENT_TIMESTAMP,
		  error_message = CASE WHEN preview_task.status = 'failed' THEN preview_task.error_message ELSE NULL END`,
		mediaID, intervalSec, countNum,
	)
	return err
}

// RunOne synchronously processes the existing preview task for mediaID.
func (w *Worker) RunOne(ctx context.Context, mediaID int64) error {
	if w == nil || w.DB == nil {
		return errors.New("preview worker: database is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var libraryID int64
	var filePath, status string
	var durationSec int64
	var intervalSec, count int
	var spritePath, vttPath, errorMessage sql.NullString
	err := w.DB.QueryRowContext(ctx, `
		SELECT m.library_id, m.file_path, COALESCE(m.duration,0),
		       COALESCE(NULLIF(t.interval_sec,0),5), COALESCE(NULLIF(t.thumb_count,0),1),
		       t.status, t.sprite_path, t.vtt_path, t.error_message
		FROM media m
		JOIN preview_task t ON t.media_id=m.id
		WHERE m.id=? AND m.file_type='video' AND m.file_path IS NOT NULL AND trim(m.file_path) != ''
		LIMIT 1`, mediaID).Scan(&libraryID, &filePath, &durationSec, &intervalSec, &count, &status, &spritePath, &vttPath, &errorMessage)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("preview task for media %d not found", mediaID)
	}
	if err != nil {
		return err
	}
	if status == "ready" && regularFile(spritePath.String) && regularFile(vttPath.String) {
		return nil
	}
	if status == "failed" {
		if errorMessage.String != "" {
			return errors.New(errorMessage.String)
		}
		return fmt.Errorf("preview task for media %d failed", mediaID)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	inputPath := storage.PreferredFFmpegPath(w.DB, mediaID, libraryID, filePath)
	if inputPath == "" {
		return fmt.Errorf("preview task for media %d: source file is unavailable", mediaID)
	}
	return w.run(ctx, mediaID, inputPath, durationSec, intervalSec, count)
}

func regularFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// RunBatch processes up to limit waiting preview tasks synchronously.
func (w *Worker) RunBatch(limit int) (done, failed int) {
	if w == nil || w.DB == nil || limit <= 0 {
		return 0, 0
	}
	rows, err := w.DB.Query(`
		SELECT t.media_id, m.file_path, COALESCE(m.duration,0),
		       COALESCE(NULLIF(t.interval_sec,0),5), COALESCE(NULLIF(t.thumb_count,0),1)
		FROM preview_task t
		JOIN media m ON m.id = t.media_id
		WHERE t.status = 'waiting'
		  AND m.file_type = 'video'
		  AND m.file_path IS NOT NULL AND trim(m.file_path) != ''
		ORDER BY t.updated_at ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return 0, 0
	}
	defer rows.Close()

	type job struct {
		mediaID     int64
		inputPath   string
		durationSec int64
		intervalSec int
		count       int
	}
	var jobs []job
	for rows.Next() {
		var j job
		if rows.Scan(&j.mediaID, &j.inputPath, &j.durationSec, &j.intervalSec, &j.count) == nil {
			jobs = append(jobs, j)
		}
	}
	for _, j := range jobs {
		if err := w.run(context.Background(), j.mediaID, j.inputPath, j.durationSec, j.intervalSec, j.count); err != nil {
			failed++
		} else {
			done++
		}
	}
	return done, failed
}

func (w *Worker) startOnce(ctx context.Context, mediaID int64, inputPath string, durationSec int64, intervalSec int, count int) {
	w.mu.Lock()
	if w.running[mediaID] {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()
	go func() {
		_ = w.run(ctx, mediaID, inputPath, durationSec, intervalSec, count)
	}()
}

func (w *Worker) run(ctx context.Context, mediaID int64, inputPath string, durationSec int64, intervalSec int, count int) (runErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	if w.running[mediaID] {
		w.mu.Unlock()
		return fmt.Errorf("already running for media %d", mediaID)
	}
	w.running[mediaID] = true
	w.mu.Unlock()
	defer func() { w.mu.Lock(); delete(w.running, mediaID); w.mu.Unlock() }()
	if err := validateCommitGuard(ctx); err != nil {
		return err
	}
	if _, err := w.DB.ExecContext(ctx, `UPDATE preview_task SET status='running',updated_at=CURRENT_TIMESTAMP WHERE media_id=?`, mediaID); err != nil {
		return err
	}

	outDir := filepath.Join(w.PreviewDir, strconv.FormatInt(mediaID, 10))
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return w.failGuarded(ctx, mediaID, err)
	}
	token := uuid.NewString()
	spriteStage := filepath.Join(outDir, "sprite."+token+".tmp.jpg")
	vttStage := filepath.Join(outDir, "thumbs."+token+".tmp.vtt")
	defer os.Remove(spriteStage)
	defer os.Remove(vttStage)
	filter := fmt.Sprintf("fps=1/%d,scale=240:135,tile=10x10", intervalSec)
	runner := w.runFFmpeg
	if runner == nil {
		runner = storage.RunFFmpeg
	}
	out, err := runner(ctx, w.DB, w.Vault, w.FFmpegPath, mediaID, inputPath, 0, float64(durationSec), nil, []string{"-vf", filter, "-frames:v", "1", "-q:v", "3", spriteStage}, "")
	if err != nil {
		return w.handleRunError(ctx, mediaID, errors.Join(err, errorFromOutput(out)))
	}
	if err = os.WriteFile(vttStage, []byte(buildVTT(count, intervalSec, durationSec)), 0644); err != nil {
		return w.failGuarded(ctx, mediaID, err)
	}
	if err = validateCommitGuard(ctx); err != nil {
		return err
	}

	encrypted := w.Derived != nil && storage.NeedsDerivedEncryption(w.DB, mediaID)
	var spriteStored, vttStored string
	var staged []*storage.StagedDerivedAsset
	if encrypted {
		a, e := w.Derived.StagePath(ctx, mediaID, "preview_sprite", "sprite.jpg", spriteStage)
		if e != nil {
			return w.failGuarded(ctx, mediaID, e)
		}
		staged = append(staged, a)
		b, e := w.Derived.StagePath(ctx, mediaID, "preview_vtt", "thumbs.vtt", vttStage)
		if e != nil {
			w.Derived.AbortStaged(staged...)
			return w.failGuarded(ctx, mediaID, e)
		}
		staged = append(staged, b)
		defer func() {
			if staged != nil {
				w.Derived.AbortStaged(staged...)
			}
		}()
		spriteStored = a.EncPath()
		vttStored = b.EncPath()
	} else {
		spriteStored = filepath.Join(outDir, "sprite.jpg")
		vttStored = filepath.Join(outDir, "thumbs.vtt")
	}

	var backups []fileBackup
	if !encrypted {
		backups, err = replacePreviewPair(spriteStage, spriteStored, vttStage, vttStored)
		if err != nil {
			return w.failGuarded(ctx, mediaID, err)
		}
		defer func() {
			if backups != nil {
				runErr = errors.Join(runErr, restorePreviewPair(backups))
			}
		}()
	}
	tx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = validateCommitGuardTx(ctx, tx); err != nil {
		return err
	}
	var old []string
	if encrypted {
		old, err = w.Derived.CommitStagedTx(ctx, tx, staged...)
		if err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE preview_task SET status='ready',sprite_path=?,vtt_path=?,thumb_width=240,thumb_height=135,thumb_count=?,interval_sec=?,updated_at=CURRENT_TIMESTAMP,error_message=NULL WHERE media_id=?`, spriteStored, vttStored, count, intervalSec, mediaID)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		return fmt.Errorf("preview task for media %d was not persisted as ready", mediaID)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committedBackups := backups
	backups = nil
	staged = nil
	cleanupPreviewBackups(committedBackups)
	if w.Derived != nil {
		w.Derived.CleanupReplaced(old)
	}
	return nil
}

type fileBackup struct {
	final, backup string
	hadOld        bool
}

func replacePreviewPair(stageA, finalA, stageB, finalB string) ([]fileBackup, error) {
	pairs := [][2]string{{stageA, finalA}, {stageB, finalB}}
	done := make([]fileBackup, 0, 2)
	for _, p := range pairs {
		b := fileBackup{final: p[1], backup: p[1] + ".bak-" + uuid.NewString()}
		if _, err := os.Stat(p[1]); err == nil {
			b.hadOld = true
			if err = os.Rename(p[1], b.backup); err != nil {
				_ = restorePreviewPair(done)
				return nil, err
			}
		} else if !os.IsNotExist(err) {
			_ = restorePreviewPair(done)
			return nil, err
		}
		if err := os.Rename(p[0], p[1]); err != nil {
			if b.hadOld {
				_ = os.Rename(b.backup, b.final)
			}
			_ = restorePreviewPair(done)
			return nil, err
		}
		done = append(done, b)
	}
	return done, nil
}
func restorePreviewPair(bs []fileBackup) error {
	var out error
	for i := len(bs) - 1; i >= 0; i-- {
		b := bs[i]
		if err := os.Remove(b.final); err != nil && !os.IsNotExist(err) {
			out = errors.Join(out, err)
		}
		if b.hadOld {
			out = errors.Join(out, os.Rename(b.backup, b.final))
		}
	}
	return out
}
func cleanupPreviewBackups(bs []fileBackup) {
	for _, b := range bs {
		if b.hadOld {
			_ = os.Remove(b.backup)
		}
	}
}
func errorFromOutput(out []byte) error {
	if strings.TrimSpace(string(out)) == "" {
		return nil
	}
	return errors.New(trimErr(string(out), nil))
}
func (w *Worker) handleRunError(ctx context.Context, mediaID int64, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		cleanup := context.WithoutCancel(ctx)
		if guardErr := validateCommitGuard(cleanup); guardErr != nil {
			return errors.Join(err, guardErr)
		}
		_, stateErr := w.DB.ExecContext(cleanup, `UPDATE preview_task SET status='waiting',error_message=?,updated_at=CURRENT_TIMESTAMP WHERE media_id=?`, trimErr("", err), mediaID)
		return errors.Join(err, stateErr)
	}
	return w.failGuarded(ctx, mediaID, err)
}
func (w *Worker) failGuarded(ctx context.Context, mediaID int64, runErr error) error {
	if err := validateCommitGuard(ctx); err != nil {
		return errors.Join(runErr, err)
	}
	_, err := w.DB.ExecContext(ctx, `UPDATE preview_task SET status='failed',error_message=?,updated_at=CURRENT_TIMESTAMP WHERE media_id=?`, trimErr("", runErr), mediaID)
	return errors.Join(runErr, err)
}

func buildVTT(count int, intervalSec int, durationSec int64) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	cols := 10
	w := 240
	h := 135
	total := int(durationSec)
	if total <= 0 {
		total = count * intervalSec
	}
	for i := 0; i < count; i++ {
		start := i * intervalSec
		end := (i + 1) * intervalSec
		if end > total {
			end = total
		}
		x := (i % cols) * w
		y := (i / cols) * h
		b.WriteString(formatTS(start))
		b.WriteString(" --> ")
		b.WriteString(formatTS(end))
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("sprite.jpg#xywh=%d,%d,%d,%d\n\n", x, y, w, h))
	}
	return b.String()
}

func formatTS(sec int) string {
	if sec < 0 {
		sec = 0
	}
	h := sec / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	return fmt.Sprintf("%02d:%02d:%02d.000", h, m, s)
}

func trimErr(out string, err error) string {
	msg := strings.TrimSpace(out)
	if msg == "" && err != nil {
		msg = err.Error()
	}
	if len(msg) > 1500 {
		return msg[:1500]
	}
	return msg
}
