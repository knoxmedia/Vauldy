package doccover

import (
	"context"
	"database/sql"
	"log"
	"os"
	"sync"
	"time"

	"knox-media/internal/config"
	"knox-media/internal/keystore"
	"knox-media/internal/storage"
)

// WorkerConfig supplies runtime paths for cover generation.
type WorkerConfig struct {
	DB         *sql.DB
	Vault      *keystore.Vault
	Derived    *storage.DerivedAssetStore
	MediaRoot  string
	PreviewDir string
	FFmpegPath string
	DocTrans   config.DocTransConfig
	TimeoutSec func() int
	OnCoverReady func(mediaID int64)
}

// Worker serializes document cover jobs so LibreOffice conversions do not stampede or time out while waiting.
type Worker struct {
	cfg WorkerConfig

	mu      sync.Mutex
	pending map[int64]struct{}
	wake    chan struct{}
}

func NewWorker(cfg WorkerConfig) *Worker {
	return &Worker{
		cfg:     cfg,
		pending: map[int64]struct{}{},
		wake:    make(chan struct{}, 1),
	}
}

// SetOnCoverReady registers a callback after a document cover is generated.
func (w *Worker) SetOnCoverReady(fn func(mediaID int64)) {
	if w == nil {
		return
	}
	w.cfg.OnCoverReady = fn
}

// Start runs the cover worker loop until ctx is cancelled.
func (w *Worker) Start(ctx context.Context) {
	if w == nil {
		return
	}
	log.Printf("document cover worker started (preview=%s)", w.cfg.PreviewDir)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		id, ok := w.dequeue(ctx)
		if !ok {
			continue
		}
		w.runOne(id)
	}
}

// Enqueue schedules cover generation for one document media row.
func (w *Worker) Enqueue(mediaID int64) {
	if w == nil || mediaID <= 0 {
		return
	}
	w.mu.Lock()
	if w.pending == nil {
		w.pending = map[int64]struct{}{}
	}
	w.pending[mediaID] = struct{}{}
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// BackfillLibrary enqueues documents in a library that still lack a cached cover.
func (w *Worker) BackfillLibrary(libraryID int64) {
	if w == nil || w.cfg.DB == nil || libraryID <= 0 {
		return
	}
	preview := w.cfg.PreviewDir
	derivedBase := ""
	if w.cfg.Derived != nil {
		derivedBase = w.cfg.Derived.BaseDir
	}
	rows, err := w.cfg.DB.Query(`
		SELECT id FROM media
		WHERE library_id = ? AND file_type = 'document' AND status = 'active'`, libraryID)
	if err != nil {
		return
	}
	defer rows.Close()
	var queued int
	for rows.Next() {
		var id int64
		if rows.Scan(&id) != nil || id <= 0 {
			continue
		}
		if !NeedsCoverWork(w.cfg.DB, preview, derivedBase, id, 0) {
			continue
		}
		w.Enqueue(id)
		queued++
	}
	if queued > 0 {
		log.Printf("document cover backfill library=%d queued=%d", libraryID, queued)
	}
}

// BackfillAllLibraries enqueues missing covers for every document library.
func (w *Worker) BackfillAllLibraries() {
	if w == nil || w.cfg.DB == nil {
		return
	}
	rows, err := w.cfg.DB.Query(`SELECT id FROM library WHERE lower(type) = 'document'`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var libraryID int64
		if rows.Scan(&libraryID) != nil || libraryID <= 0 {
			continue
		}
		w.BackfillLibrary(libraryID)
	}
}

func (w *Worker) dequeue(ctx context.Context) (int64, bool) {
	for {
		w.mu.Lock()
		var pick int64
		for id := range w.pending {
			pick = id
			delete(w.pending, pick)
			break
		}
		w.mu.Unlock()
		if pick > 0 {
			return pick, true
		}
		select {
		case <-ctx.Done():
			return 0, false
		case <-w.wake:
		}
	}
}

// coverJobTimeoutSec returns a timeout long enough for LibreOffice PDF→JPEG export.
// Large PDFs can exceed the default doc_trans timeout (180s).
const minCoverJobTimeoutSec = 600

func coverJobTimeoutSec(timeoutSec func() int) int {
	timeout := minCoverJobTimeoutSec
	if timeoutSec != nil {
		if v := timeoutSec(); v > timeout {
			timeout = v
		}
	}
	return timeout
}

func (w *Worker) runOne(mediaID int64) {
	if w == nil || w.cfg.DB == nil || mediaID <= 0 {
		return
	}
	var filePath, fileType sql.NullString
	var fileMtime sql.NullInt64
	if err := w.cfg.DB.QueryRow(`
		SELECT file_path, file_type, file_mtime FROM media WHERE id = ? AND status = 'active'`, mediaID).
		Scan(&filePath, &fileType, &fileMtime); err != nil {
		return
	}
	if fileType.String != "document" || filePath.String == "" {
		return
	}
	mtime := fileMtime.Int64
	if mtime <= 0 {
		if st, err := os.Stat(filePath.String); err == nil {
			mtime = st.ModTime().UTC().Unix()
		}
	}
	preview := w.cfg.PreviewDir
	derivedBase := ""
	if w.cfg.Derived != nil {
		derivedBase = w.cfg.Derived.BaseDir
	}
	if !NeedsCoverWork(w.cfg.DB, preview, derivedBase, mediaID, mtime) {
		return
	}
	log.Printf("document cover generating media=%d", mediaID)
	timeout := coverJobTimeoutSec(w.cfg.TimeoutSec)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()
	opts := Options{
		DB:         w.cfg.DB,
		Vault:      w.cfg.Vault,
		Derived:    w.cfg.Derived,
		FFmpegPath: w.cfg.FFmpegPath,
		PreviewDir: preview,
		MediaRoot:  w.cfg.MediaRoot,
		DocTrans:   w.cfg.DocTrans,
	}
	if err := Ensure(ctx, opts, mediaID, filePath.String, mtime); err != nil {
		MarkCoverFailed(preview, mediaID, err)
		log.Printf("document cover failed media=%d path=%s: %v", mediaID, filePath.String, err)
		return
	}
	_ = os.Remove(coverSkipPath(preview, mediaID))
	log.Printf("document cover ready media=%d", mediaID)
	if w.cfg.OnCoverReady != nil {
		w.cfg.OnCoverReady(mediaID)
	}
}
