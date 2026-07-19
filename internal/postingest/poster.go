package postingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"

	"knox-media/internal/keystore"
	"knox-media/internal/scraper"
	"knox-media/internal/storage"
	"knox-media/internal/store"
)

const (
	posterKind        = "poster"
	posterLogicalName = "poster.jpg"
)

type posterCommitGuardKey struct{}
type mediaLockEntry struct {
	sem  chan struct{}
	refs int
}

var posterMediaLocks = struct {
	sync.Mutex
	entries map[int64]*mediaLockEntry
}{entries: map[int64]*mediaLockEntry{}}

func lockPosterMedia(ctx context.Context, mediaID int64) (func(), error) {
	posterMediaLocks.Lock()
	e := posterMediaLocks.entries[mediaID]
	if e == nil {
		e = &mediaLockEntry{sem: make(chan struct{}, 1)}
		e.sem <- struct{}{}
		posterMediaLocks.entries[mediaID] = e
	}
	e.refs++
	posterMediaLocks.Unlock()
	select {
	case <-ctx.Done():
		posterMediaLocks.Lock()
		e.refs--
		if e.refs == 0 {
			delete(posterMediaLocks.entries, mediaID)
		}
		posterMediaLocks.Unlock()
		return nil, ctx.Err()
	case <-e.sem:
	}
	return func() {
		e.sem <- struct{}{}
		posterMediaLocks.Lock()
		e.refs--
		if e.refs == 0 {
			delete(posterMediaLocks.entries, mediaID)
		}
		posterMediaLocks.Unlock()
	}, nil
}

type PosterRunner interface {
	Capture(context.Context, int64, int64, scraper.Config) (posterURL, source string, err error)
}

type PosterAdapter struct {
	DB        *sql.DB
	UploadDir string
	Derived   *storage.DerivedAssetStore
	Runner    PosterRunner
}

func NewPosterAdapter(db *sql.DB, upload string, derived *storage.DerivedAssetStore, runner PosterRunner) *PosterAdapter {
	return &PosterAdapter{DB: db, UploadDir: upload, Derived: derived, Runner: runner}
}

func (a *PosterAdapter) Execute(ctx context.Context, task Task) error {
	if a == nil || a.DB == nil {
		return permanentPosterError("database is not configured")
	}
	if task.Type != TaskPoster {
		return permanentPosterError(fmt.Sprintf("unsupported task type %q", task.Type))
	}
	if task.MediaID <= 0 {
		return permanentPosterError("invalid media id")
	}
	if a.Runner == nil {
		return permanentPosterError("runner is not configured")
	}
	unlock, err := lockPosterMedia(ctx, task.MediaID)
	if err != nil {
		return err
	}
	defer unlock()

	var libraryID int64
	var raw, fileType string
	if err := a.DB.QueryRowContext(ctx, `SELECT library_id, COALESCE(meta_json,''), COALESCE(file_type,'') FROM media WHERE id=?`, task.MediaID).Scan(&libraryID, &raw, &fileType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return permanentPosterError("media not found")
		}
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(fileType), "video") {
		return permanentPosterError("poster requires video media")
	}
	meta := decodePosterMeta(raw)
	if posterInMeta(meta) != "" {
		return nil
	}
	plain := filepath.Join(strings.TrimSpace(a.UploadDir), "posters", fmt.Sprintf("%d.jpg", task.MediaID))
	if nonEmptyFile(plain) {
		return a.persistPosterMeta(ctx, task.MediaID, storage.PlainPosterURL(task.MediaID), "")
	}
	derivedExists, err := derivedPosterExists(ctx, a.DB, task.MediaID)
	if err != nil {
		return err
	}
	if derivedExists {
		return a.persistPosterMeta(ctx, task.MediaID, storage.DerivedPosterAPIPath(task.MediaID), "")
	}
	if err := a.validateLease(ctx, task); err != nil {
		return err
	}
	if task.LeaseOwner != "" {
		guard := func(guardCtx context.Context) error { return a.validateLease(guardCtx, task) }
		ctx = context.WithValue(ctx, posterCommitGuardKey{}, guard)
	}
	cfg, err := a.configForLibrary(ctx, libraryID)
	if err != nil {
		return err
	}
	url, source, err := a.Runner.Capture(ctx, task.MediaID, libraryID, cfg)
	if err != nil {
		return err
	}
	if err := a.validateLease(ctx, task); err != nil {
		return err
	}
	if strings.TrimSpace(url) == "" {
		return permanentPosterError("poster runner returned empty URL")
	}
	return a.persistPosterMeta(ctx, task.MediaID, url, source)
}

func (a *PosterAdapter) validateLease(ctx context.Context, task Task) error {
	if strings.TrimSpace(task.LeaseOwner) == "" {
		return nil
	}
	var one int
	err := a.DB.QueryRowContext(ctx, `SELECT 1 FROM post_ingest_task WHERE id=? AND media_id=? AND task_type='poster' AND status='running' AND lease_owner=?`, task.ID, task.MediaID, task.LeaseOwner).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ClassifiedError{Kind: FailureShutdown, Err: fmt.Errorf("poster adapter: stale lease for task %d", task.ID)}
	}
	return err
}

func permanentPosterError(message string) error {
	return ClassifiedError{Kind: FailurePermanent, Err: errors.New("poster adapter: " + message)}
}

func (a *PosterAdapter) configForLibrary(ctx context.Context, libraryID int64) (scraper.Config, error) {
	if libraryID <= 0 {
		return scraper.Config{}, permanentPosterError("invalid library id")
	}
	var providers string
	if err := a.DB.QueryRowContext(ctx, `SELECT COALESCE(image_providers,'') FROM library WHERE id=?`, libraryID).Scan(&providers); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return scraper.Config{}, permanentPosterError("library not found")
		}
		return scraper.Config{}, err
	}
	cfg := scraper.Config{APIKeys: map[string]string{}}
	for _, p := range strings.Split(providers, ",") {
		if p = strings.TrimSpace(p); p != "" {
			cfg.ImageSources = append(cfg.ImageSources, p)
		}
	}
	return cfg, nil
}

func decodePosterMeta(raw string) map[string]any {
	root := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &root)
	}
	if root == nil {
		root = map[string]any{}
	}
	return root
}
func mapValue(parent map[string]any, key string) map[string]any {
	child, _ := parent[key].(map[string]any)
	if child == nil {
		child = map[string]any{}
		parent[key] = child
	}
	return child
}
func posterInMeta(root map[string]any) string {
	scrape, _ := root["scrape"].(map[string]any)
	if scrape == nil {
		return ""
	}
	if p := stringValue(scrape["poster"]); p != "" {
		return p
	}
	extra, _ := scrape["extra"].(map[string]any)
	if extra == nil {
		return ""
	}
	return stringValue(extra["poster"])
}
func stringValue(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}
func (a *PosterAdapter) persistPosterMeta(ctx context.Context, mediaID int64, url, source string) error {
	return store.WithBusyRetry(ctx, nil, func() error {
		tx, err := a.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var current string
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(meta_json,'') FROM media WHERE id=?`, mediaID).Scan(&current); err != nil {
			return err
		}
		root := decodePosterMeta(current)
		scrape := mapValue(root, "scrape")
		if stringValue(scrape["poster"]) == "" {
			scrape["poster"] = url
		}
		extra := mapValue(scrape, "extra")
		if stringValue(extra["poster"]) == "" {
			extra["poster"] = url
		}
		if strings.TrimSpace(source) != "" && stringValue(extra["local_poster_source"]) == "" {
			extra["local_poster_source"] = source
		}
		raw, err := json.Marshal(root)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE media SET meta_json=? WHERE id=?`, string(raw), mediaID); err != nil {
			return err
		}
		return tx.Commit()
	})
}
func nonEmptyFile(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Size() > 0
}
func derivedPosterExists(ctx context.Context, db *sql.DB, mediaID int64) (bool, error) {
	var p string
	err := db.QueryRowContext(ctx, `SELECT enc_path FROM media_derived_assets WHERE media_id=? AND artifact_kind=? AND logical_name=?`, mediaID, posterKind, posterLogicalName).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return nonEmptyFile(p), nil
}

type PosterFFmpegFunc func(context.Context, *sql.DB, *keystore.Vault, string, int64, string, float64, float64, []string, []string, string) ([]byte, error)
type PosterProbeFunc func(context.Context, *sql.DB, *keystore.Vault, string, int64, string, []string) ([]byte, func(), error)

type LocalPosterRunner struct {
	DB          *sql.DB
	Vault       *keystore.Vault
	Derived     *storage.DerivedAssetStore
	FFmpegPath  string
	FFprobePath string
	UploadDir   string
	RunFFmpeg   PosterFFmpegFunc
	ProbeOutput PosterProbeFunc
	finalize    func(context.Context, *storage.DerivedAssetStore, *sql.DB, int64, string) (string, error)
}

func (r *LocalPosterRunner) Capture(ctx context.Context, mediaID, libraryID int64, cfg scraper.Config) (posterURL, source string, err error) {
	if r == nil || r.DB == nil || mediaID <= 0 || libraryID <= 0 {
		return "", "", permanentPosterError("local runner is not configured")
	}
	if strings.TrimSpace(r.FFmpegPath) == "" || strings.TrimSpace(r.UploadDir) == "" {
		return "", "", permanentPosterError("ffmpeg or upload directory is not configured")
	}
	var catalog string
	var duration int64
	if err = r.DB.QueryRowContext(ctx, `SELECT COALESCE(file_path,''), COALESCE(duration,0) FROM media WHERE id=? AND library_id=?`, mediaID, libraryID).Scan(&catalog, &duration); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", permanentPosterError("media not found")
		}
		return "", "", err
	}
	input := storage.PreferredFFmpegPath(r.DB, mediaID, libraryID, catalog)
	if input == "" {
		return "", "", permanentPosterError("media file is unavailable")
	}
	dir := filepath.Join(strings.TrimSpace(r.UploadDir), "posters")
	if err = os.MkdirAll(dir, 0755); err != nil {
		return "", "", err
	}
	tmp := filepath.Join(dir, fmt.Sprintf("%d.%s.tmp.jpg", mediaID, uuid.NewString()))
	final := filepath.Join(dir, fmt.Sprintf("%d.jpg", mediaID))
	defer func() { _ = os.Remove(tmp) }()
	enabled := func(name string) bool {
		for _, v := range cfg.ImageSources {
			if strings.EqualFold(strings.TrimSpace(v), name) {
				return true
			}
		}
		return false
	}
	if enabled("embedded") && strings.TrimSpace(r.FFprobePath) != "" {
		if index, ok, e := r.attachedPicture(ctx, mediaID, input); e != nil {
			if errors.Is(e, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				return "", "", context.Canceled
			}
		} else if ok {
			_, e = r.ffmpeg(ctx, mediaID, input, nil, []string{"-map", fmt.Sprintf("0:%d", index), "-frames:v", "1", tmp})
			if e == nil && nonEmptyFile(tmp) {
				source = "embedded"
			} else if errors.Is(e, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				return "", "", context.Canceled
			}
		}
	}
	if source == "" && enabled("screen_grabber") {
		snap := posterSnapSecond(duration)
		_, e := r.ffmpeg(ctx, mediaID, input, storage.PosterSeekPreInput(snap, input), []string{"-frames:v", "1", "-q:v", "3", tmp})
		if e != nil {
			if errors.Is(e, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				return "", "", context.Canceled
			}
			return "", "", e
		}
		if nonEmptyFile(tmp) {
			source = "screen_grabber"
		}
	}
	if source == "" {
		return "", "", fmt.Errorf("local poster capture produced no file")
	}
	guard, _ := ctx.Value(posterCommitGuardKey{}).(func(context.Context) error)
	if guard != nil {
		if err := guard(ctx); err != nil {
			return "", "", err
		}
	}
	backup, err := replaceFilePreservingOld(tmp, final)
	if err != nil {
		return "", "", err
	}
	if guard != nil {
		if err := guard(ctx); err != nil {
			_ = restoreReplacedFile(final, backup)
			return "", "", err
		}
	}
	finalize := r.finalize
	if finalize == nil {
		finalize = storage.FinalizeLocalPoster
	}
	posterURL, err = finalize(ctx, r.Derived, r.DB, mediaID, final)
	if err != nil {
		if restoreErr := restoreReplacedFile(final, backup); restoreErr != nil {
			return "", "", fmt.Errorf("%w; restore poster: %v", err, restoreErr)
		}
		return "", "", err
	}
	if backup != "" {
		_ = os.Remove(backup)
	}
	return posterURL, source, nil
}
func replaceFilePreservingOld(tmp, final string) (string, error) {
	backup := ""
	if _, err := os.Stat(final); err == nil {
		backup = final + ".backup-" + uuid.NewString()
		if err = os.Rename(final, backup); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Rename(tmp, final); err != nil {
		if backup != "" {
			_ = os.Rename(backup, final)
		}
		return "", err
	}
	return backup, nil
}
func restoreReplacedFile(final, backup string) error {
	if err := os.Remove(final); err != nil && !os.IsNotExist(err) {
		return err
	}
	if backup != "" {
		return os.Rename(backup, final)
	}
	return nil
}
func (r *LocalPosterRunner) ffmpeg(ctx context.Context, mediaID int64, input string, pre, post []string) ([]byte, error) {
	fn := r.RunFFmpeg
	if fn == nil {
		fn = storage.RunFFmpeg
	}
	return fn(ctx, r.DB, r.Vault, r.FFmpegPath, mediaID, input, 0, 0, pre, post, "")
}
func (r *LocalPosterRunner) attachedPicture(ctx context.Context, mediaID int64, input string) (int, bool, error) {
	fn := r.ProbeOutput
	if fn == nil {
		fn = defaultPosterProbe
	}
	raw, cleanup, err := fn(ctx, r.DB, r.Vault, r.FFprobePath, mediaID, input, []string{"-v", "error", "-select_streams", "v", "-show_entries", "stream=index,codec_type:stream_disposition=attached_pic", "-of", "json"})
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return 0, false, err
	}
	var out struct {
		Streams []struct {
			CodecType   string `json:"codec_type"`
			Index       int    `json:"index"`
			Disposition *struct {
				AttachedPic int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return 0, false, err
	}
	for _, s := range out.Streams {
		if s.CodecType == "video" && s.Disposition != nil && s.Disposition.AttachedPic == 1 {
			return s.Index, true, nil
		}
	}
	return 0, false, nil
}
func defaultPosterProbe(ctx context.Context, db *sql.DB, vault *keystore.Vault, path string, mediaID int64, input string, args []string) ([]byte, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return storage.FFprobeOutputContext(ctx, db, vault, path, mediaID, input, 0, 0, args)
}
func posterSnapSecond(duration int64) int {
	sec := 10
	if duration > 0 {
		sec = int(duration / 5)
		if sec < 10 {
			sec = 10
		}
		if sec > 180 {
			sec = 180
		}
	}
	return sec
}
