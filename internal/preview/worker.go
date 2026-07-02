package preview

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"knox-media/internal/keystore"
	"knox-media/internal/storage"
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

type Worker struct {
	DB         *sql.DB
	Vault      *keystore.Vault
	Derived    *storage.DerivedAssetStore
	FFmpegPath string
	PreviewDir string
	mu         sync.Mutex
	running    map[int64]bool
}

func NewWorker(db *sql.DB, vault *keystore.Vault, derived *storage.DerivedAssetStore, ffmpegPath, previewDir string) *Worker {
	return &Worker{
		DB:         db,
		Vault:      vault,
		Derived:    derived,
		PreviewDir: previewDir,
		FFmpegPath: ffmpegPath,
		running:    map[int64]bool{},
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
	if durationSec <= 0 {
		durationSec = 600
	}
	intervalSec := int(math.Ceil(float64(durationSec) / 100.0))
	if intervalSec < 5 {
		intervalSec = 5
	}
	countNum := int(math.Ceil(float64(durationSec) / float64(intervalSec)))
	if countNum < 1 {
		countNum = 1
	}
	if countNum > 100 {
		countNum = 100
	}
	if err := UpsertWaitingPreviewTask(w.DB, mediaID, intervalSec, countNum); err != nil {
		return info, err
	}
	w.startOnce(ctx, mediaID, inputPath, durationSec, intervalSec, countNum)
	return Info{Status: "waiting", Interval: intervalSec, ThumbCount: countNum, Width: 240, Height: 135}, nil
}

// UpsertWaitingPreviewTask queues preview work for media_id. Existing failed rows are left unchanged.
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

func (w *Worker) run(ctx context.Context, mediaID int64, inputPath string, durationSec int64, intervalSec int, count int) error {
	w.mu.Lock()
	if w.running[mediaID] {
		w.mu.Unlock()
		return fmt.Errorf("already running for media %d", mediaID)
	}
	w.running[mediaID] = true
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.running, mediaID)
		w.mu.Unlock()
	}()

	outDir := filepath.Join(w.PreviewDir, strconv.FormatInt(mediaID, 10))
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	spritePlain := filepath.Join(outDir, "sprite.jpg")
	vttPlain := filepath.Join(outDir, "thumbs.vtt")
	_, _ = w.DB.Exec(`UPDATE preview_task SET status='running', updated_at=CURRENT_TIMESTAMP WHERE media_id = ?`, mediaID)
	filter := fmt.Sprintf("fps=1/%d,scale=240:135,tile=10x10", intervalSec)
	post := []string{"-vf", filter, "-frames:v", "1", "-q:v", "3", spritePlain}
	if out, err := storage.RunFFmpeg(ctx, w.DB, w.Vault, w.FFmpegPath, mediaID, inputPath, 0, float64(durationSec), nil, post, ""); err != nil {
		_, _ = w.DB.Exec(`UPDATE preview_task SET status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP WHERE media_id = ?`, trimErr(string(out), err), mediaID)
		return err
	}
	spritePath := spritePlain
	if w.Derived != nil {
		var err error
		spritePath, err = w.Derived.FinalizePath(ctx, mediaID, "preview_sprite", "sprite.jpg", spritePlain)
		if err != nil {
			_, _ = w.DB.Exec(`UPDATE preview_task SET status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP WHERE media_id = ?`, err.Error(), mediaID)
			return err
		}
	}
	vtt := buildVTT(count, intervalSec, durationSec)
	vttPath := vttPlain
	if w.Derived != nil && storage.NeedsDerivedEncryption(w.DB, mediaID) {
		var err error
		vttPath, err = w.Derived.Write(ctx, mediaID, "preview_vtt", "thumbs.vtt", strings.NewReader(vtt))
		if err != nil {
			_, _ = w.DB.Exec(`UPDATE preview_task SET status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP WHERE media_id = ?`, err.Error(), mediaID)
			return err
		}
	} else if err := os.WriteFile(vttPlain, []byte(vtt), 0o644); err != nil {
		_, _ = w.DB.Exec(`UPDATE preview_task SET status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP WHERE media_id = ?`, err.Error(), mediaID)
		return err
	}
	_, _ = w.DB.Exec(
		`UPDATE preview_task SET status='ready', sprite_path=?, vtt_path=?, thumb_width=240, thumb_height=135, thumb_count=?, interval_sec=?, updated_at=CURRENT_TIMESTAMP, error_message=NULL WHERE media_id = ?`,
		spritePath, vttPath, count, intervalSec, mediaID,
	)
	return nil
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
