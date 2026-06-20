package transcode

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"knox-media/internal/config"
	"knox-media/internal/keystore"
	"knox-media/internal/storage"
)

type PackageWorker struct {
	DB           *sql.DB
	Vault        *keystore.Vault
	FFmpegPath   string
	TranscodeDir string
	UploadDir    string
	shakaPath    string
	powerDRM     bool
	widevineDRM  bool
	drmCapsReady bool
	engineOrder  []string
	segmentSec   int
	mu           sync.Mutex
	running      map[int64]context.CancelFunc
}

const (
	encryptionModeStandard = "standard"
	encryptionModePowerDRM = "powerdrm"
	encryptionModeDRM      = "drm"
)

var extXMapURIPattern = regexp.MustCompile(`URI="([^"]+)"`)

func logDRMf(taskID, mediaID int64, format string, args ...any) {
	prefix := fmt.Sprintf("drm task_id=%d media_id=%d ", taskID, mediaID)
	log.Printf(prefix+format, args...)
}

func NewPackageWorker(db *sql.DB, cfg *config.Config, vault *keystore.Vault) *PackageWorker {
	if cfg == nil {
		cfg = &config.Config{}
	}
	ord := config.NormalizeDRMPackagingOrder(cfg.DRMPackaging.EngineOrder)
	seg := cfg.DRMPackaging.SegmentSeconds
	if seg <= 0 {
		seg = 4
	}
	return &PackageWorker{
		DB:           db,
		Vault:        vault,
		FFmpegPath:   cfg.FFmpeg.FFmpegPath,
		TranscodeDir: cfg.Data.Transcode,
		UploadDir:    cfg.Data.Upload,
		shakaPath:    strings.TrimSpace(cfg.DRMPackaging.ShakaPackagerPath),
		powerDRM:     cfg.DRM.PowerDRM.Enabled,
		widevineDRM:  cfg.WidevineEnabled(),
		drmCapsReady: true,
		engineOrder:  ord,
		segmentSec:   seg,
		running:      map[int64]context.CancelFunc{},
	}
}

// effectiveEngineOrder returns normalized shaka|ffmpeg order (defaults when the worker
// is constructed without NewPackageWorker or has an empty list).
func (w *PackageWorker) effectiveEngineOrder() []string {
	if w == nil {
		return config.NormalizeDRMPackagingOrder(nil)
	}
	return config.NormalizeDRMPackagingOrder(w.engineOrder)
}

// HealLegacyInitFiles scans completed DRM package outputs and repairs missing
// fMP4 init segments that may have been emitted into server working directory
// by older ffmpeg invocation behavior.
func (w *PackageWorker) HealLegacyInitFiles() (scanned int, fixed int, err error) {
	if w == nil || w.DB == nil {
		return 0, 0, nil
	}
	rows, err := w.DB.Query(`
		SELECT output_path
		FROM package_task
		WHERE pipeline_type = 'cmaf_drm' AND status = 'done' AND COALESCE(output_path,'') <> ''
	`)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var master string
		if err := rows.Scan(&master); err != nil {
			continue
		}
		master = strings.TrimSpace(master)
		if master == "" {
			continue
		}
		scanned++
		dir := filepath.Dir(master)
		playlists, _ := filepath.Glob(filepath.Join(dir, "*.m3u8"))
		for _, pl := range playlists {
			raw, rerr := os.ReadFile(pl)
			if rerr != nil {
				continue
			}
			for _, line := range strings.Split(string(raw), "\n") {
				if !strings.Contains(line, "#EXT-X-MAP:") {
					continue
				}
				m := extXMapURIPattern.FindStringSubmatch(line)
				if len(m) != 2 {
					continue
				}
				uri := strings.TrimSpace(m[1])
				if uri == "" || strings.Contains(uri, "://") {
					continue
				}
				if idx := strings.Index(uri, "?"); idx >= 0 {
					uri = uri[:idx]
				}
				target := filepath.Join(dir, filepath.Clean(uri))
				if st, serr := os.Stat(target); serr == nil && !st.IsDir() {
					continue
				}
				legacy := filepath.Join(".", filepath.Base(uri))
				if st, serr := os.Stat(legacy); serr != nil || st.IsDir() {
					continue
				}
				if cerr := copyFile(legacy, target); cerr == nil {
					fixed++
				}
			}
		}
	}
	return scanned, fixed, nil
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

// EnqueueForMedia creates a batch stream-encryption package task (legacy / repair).
// drm_enabled libraries use play-time encrypted JIT from HLSInfo instead of this path.
func (w *PackageWorker) EnqueueForMedia(mediaID int64) (int64, error) {
	if w == nil || w.DB == nil || mediaID <= 0 {
		return 0, nil
	}

	var drmEnabled sql.NullInt64
	var encryptionMode sql.NullString
	err := w.DB.QueryRow(`
		SELECT COALESCE(l.drm_enabled,0), COALESCE(l.encryption_mode,'drm')
		FROM media m
		LEFT JOIN library l ON l.id = m.library_id
		WHERE m.id = ?
		LIMIT 1
	`, mediaID).Scan(&drmEnabled, &encryptionMode)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if drmEnabled.Int64 != 1 {
		return 0, nil
	}

	pipelineType := w.pipelineTypeFromEncryptionMode(encryptionMode.String)
	var existingID int64
	var existingStatus sql.NullString
	err = w.DB.QueryRow(`
		SELECT id, status
		FROM package_task
		WHERE media_id = ? AND pipeline_type = ?
		ORDER BY id DESC
		LIMIT 1
	`, mediaID, pipelineType).Scan(&existingID, &existingStatus)
	if err == nil {
		switch existingStatus.String {
		case "waiting", "running", "done":
			return existingID, nil
		}
	}
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}

	res, err := w.DB.Exec(`INSERT INTO package_task (media_id, pipeline_type, status, progress) VALUES (?, ?, 'waiting', 0)`, mediaID, pipelineType)
	if err != nil {
		return 0, err
	}
	tid, _ := res.LastInsertId()
	go func(taskID int64) {
		_ = w.RunTask(context.Background(), taskID)
	}(tid)
	return tid, nil
}

// StartWaiting launches up to limit waiting package tasks in background goroutines.
func (w *PackageWorker) StartWaiting(ctx context.Context, limit int) int {
	if w == nil || w.DB == nil || limit <= 0 {
		return 0
	}
	rows, err := w.DB.Query(`
		SELECT id FROM package_task
		WHERE status = 'waiting'
		ORDER BY id ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return 0
	}
	defer rows.Close()

	started := 0
	for rows.Next() {
		if started >= limit {
			break
		}
		var taskID int64
		if rows.Scan(&taskID) != nil {
			continue
		}
		started++
		id := taskID
		go func() {
			_ = w.RunTask(context.Background(), id)
		}()
	}
	return started
}

func (w *PackageWorker) RunTask(ctx context.Context, taskID int64) error {
	if w == nil || w.DB == nil || taskID <= 0 {
		return nil
	}
	res, err := w.DB.Exec(`
		UPDATE package_task
		SET status='running', progress=5, drm_status='running', updated_at=CURRENT_TIMESTAMP
		WHERE id = ? AND status IN ('waiting','failed')
	`, taskID)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		var current sql.NullString
		if err := w.DB.QueryRow(`SELECT status FROM package_task WHERE id = ? LIMIT 1`, taskID).Scan(&current); err != nil {
			if err == sql.ErrNoRows {
				return nil
			}
			return err
		}
		// running/done/cancelled should not be re-entered by another worker.
		if current.String == "running" || current.String == "done" || current.String == "cancelled" {
			return nil
		}
	}

	ctx2, cancel := context.WithCancel(ctx)
	w.mu.Lock()
	if w.running == nil {
		w.running = map[int64]context.CancelFunc{}
	}
	w.running[taskID] = cancel
	w.mu.Unlock()
	defer func() {
		cancel()
		w.mu.Lock()
		delete(w.running, taskID)
		w.mu.Unlock()
	}()

	var mediaID int64
	var pipelineType string
	if err := w.DB.QueryRow(`SELECT media_id, pipeline_type FROM package_task WHERE id = ? LIMIT 1`, taskID).Scan(&mediaID, &pipelineType); err != nil {
		return err
	}

	var fileID, sourcePath sql.NullString
	var sourceHeight sql.NullInt64
	var cleanupFlag sql.NullInt64
	if err := w.DB.QueryRow(`
		SELECT COALESCE(m.file_id,''), COALESCE(m.file_path,''), COALESCE(m.height,1080), COALESCE(l.cleanup_local_source_after_package,0)
		FROM media m
		LEFT JOIN library l ON l.id = m.library_id
		WHERE m.id = ?
		LIMIT 1
	`, mediaID).Scan(&fileID, &sourcePath, &sourceHeight, &cleanupFlag); err != nil {
		_, _ = w.DB.Exec(`UPDATE package_task SET status='failed', progress=0, drm_status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP WHERE id = ?`, trimErrorMessage(err.Error()), taskID)
		return err
	}

	ladder := chooseLadder(int(sourceHeight.Int64), 1080, nil)
	if len(ladder) == 0 {
		ladder = selectRenditions("720p", 1080)
	}
	profileKey := ladderKey(ladder)
	fileKey := strings.TrimSpace(fileID.String)
	if fileKey == "" {
		fileKey = strconv.FormatInt(mediaID, 10)
	}
	outDir := w.encryptedOutputDir(sourcePath.String, fileKey, profileKey)
	kidHex, keyHex, err := GenerateDRMMaterial()
	if err != nil {
		_, _ = w.DB.Exec(`UPDATE package_task SET status='failed', progress=0, drm_status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP WHERE id = ?`, trimErrorMessage(err.Error()), taskID)
		return err
	}
	mode := encryptionModeFromPipelineType(pipelineType)
	outMaster := ""
	switch mode {
	case encryptionModeStandard:
		outMaster, err = w.runAESHLSPackage(ctx2, taskID, mediaID, sourcePath.String, outDir, ladder, keyHex, kidHex)
	case encryptionModePowerDRM:
		outMaster, err = w.runAESHLSPackage(ctx2, taskID, mediaID, sourcePath.String, outDir, ladder, keyHex, kidHex)
		if err == nil {
			err = rewriteManifestsToPowerDRM(outDir, kidHex)
		}
	default:
		outMaster, err = w.runCMAFDRMPackage(ctx2, taskID, mediaID, sourcePath.String, outDir, ladder, keyHex, kidHex)
	}
	if err != nil {
		_, _ = w.DB.Exec(`UPDATE package_task SET status='failed', progress=0, drm_status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP WHERE id = ?`, trimErrorMessage(err.Error()), taskID)
		return err
	}
	if mode == encryptionModeStandard {
		if err := w.persistAES128KeyMaterial(mediaID, kidHex, keyHex, kidHex); err != nil {
			_, _ = w.DB.Exec(`UPDATE package_task SET status='failed', progress=0, drm_status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP WHERE id = ?`, trimErrorMessage(err.Error()), taskID)
			return err
		}
		_, _ = w.DB.Exec(`DELETE FROM drm_asset WHERE media_id = ?`, mediaID)
	} else {
		_, _ = w.DB.Exec(`DELETE FROM drm_key_material WHERE media_id = ?`, mediaID)
	}
	if mode == encryptionModePowerDRM || mode == encryptionModeDRM {
		keyRefPath, perr := w.persistDRMMaterial(outDir, mediaID, kidHex, keyHex)
		if perr != nil {
			_, _ = w.DB.Exec(`UPDATE package_task SET status='failed', progress=0, drm_status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP WHERE id = ?`, trimErrorMessage(perr.Error()), taskID)
			return perr
		}
		_, _ = w.DB.Exec(`
			INSERT INTO drm_asset (media_id, kid, key_ref, manifest_path, license_policy_json, updated_at)
			VALUES (?, ?, ?, ?, '{}', CURRENT_TIMESTAMP)
			ON CONFLICT(media_id) DO UPDATE SET
			  kid=excluded.kid,
			  key_ref=excluded.key_ref,
			  manifest_path=excluded.manifest_path,
			  updated_at=CURRENT_TIMESTAMP
		`, mediaID, kidHex, keyRefPath, outMaster)
	} else {
		_, _ = w.DB.Exec(`DELETE FROM drm_asset WHERE media_id = ?`, mediaID)
	}

	cleanupStatus := "skipped"
	if cleanupFlag.Int64 == 1 && shouldCleanup(w.UploadDir, sourcePath.String) {
		if err := os.Remove(sourcePath.String); err != nil {
			cleanupStatus = "failed"
		} else {
			cleanupStatus = "success"
		}
	}
	_, _ = w.DB.Exec(`
		UPDATE package_task
		SET status='done', progress=100, drm_status='done', output_path=?, source_cleanup_status=?, error_message=NULL, updated_at=CURRENT_TIMESTAMP
		WHERE id = ?
	`, outMaster, cleanupStatus, taskID)
	return nil
}

func (w *PackageWorker) pipelineTypeFromEncryptionMode(mode string) string {
	switch w.normalizeEncryptionMode(mode) {
	case encryptionModeStandard:
		return "hls_aes_128"
	case encryptionModePowerDRM:
		return "hls_powerdrm"
	default:
		return "cmaf_drm"
	}
}

func encryptionModeFromPipelineType(pipeline string) string {
	switch strings.TrimSpace(pipeline) {
	case "hls_aes_128":
		return encryptionModeStandard
	case "hls_powerdrm":
		return encryptionModePowerDRM
	default:
		return encryptionModeDRM
	}
}

func (w *PackageWorker) normalizeEncryptionMode(mode string) string {
	widevineEnabled := true
	powerdrmEnabled := false
	if w != nil && w.drmCapsReady {
		widevineEnabled = w.widevineDRM
		powerdrmEnabled = w.powerDRM
	}
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "standard", "hls_aes_128", "aes_128":
		return encryptionModeStandard
	case "powerdrm":
		if powerdrmEnabled {
			return encryptionModePowerDRM
		}
		if widevineEnabled {
			return encryptionModeDRM
		}
		return encryptionModeStandard
	default:
		if !widevineEnabled {
			return encryptionModeStandard
		}
		return encryptionModeDRM
	}
}

func (w *PackageWorker) runAESHLSPackage(ctx context.Context, taskID, mediaID int64, inputPath, outDir string, ladder []Rendition, keyHex, kidHex string) (string, error) {
	if strings.TrimSpace(inputPath) == "" {
		return "", fmt.Errorf("empty input path")
	}
	if strings.TrimSpace(w.FFmpegPath) == "" {
		return "", fmt.Errorf("ffmpeg path not configured")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	keyBytes, err := hex.DecodeString(strings.TrimSpace(keyHex))
	if err != nil {
		return "", fmt.Errorf("decode aes key: %w", err)
	}
	keyFile, err := os.CreateTemp("", "knox-aes128-key-*")
	if err != nil {
		return "", err
	}
	keyPath := keyFile.Name()
	_ = keyFile.Close()
	defer func() { _ = os.Remove(keyPath) }()
	if err := os.WriteFile(keyPath, keyBytes, 0o600); err != nil {
		return "", err
	}
	iv := strings.TrimSpace(kidHex)
	if iv == "" {
		iv = strings.Repeat("0", 32)
	}
	keyInfoPath := filepath.Join(outDir, "enc.keyinfo")
	keyURI := fmt.Sprintf("/api/v1/drm/hls/aes128/key?media_id=%d&kid=%s", mediaID, kidHex)
	keyInfo := strings.Join([]string{keyURI, filepath.ToSlash(keyPath), iv}, "\n")
	if err := os.WriteFile(keyInfoPath, []byte(keyInfo), 0o600); err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(keyInfoPath) }()
	for i, r := range ladder {
		vf := fmt.Sprintf("scale=-2:%d", r.Height)
		hlsSec := fmt.Sprintf("%d", w.segmentSecOrDefault())
		post := []string{
			"-map", "0:v:0", "-map", "0:a:0?",
			"-vf", vf,
			"-c:v", "libx264", "-preset", "veryfast", "-b:v", r.VideoRate, "-maxrate", r.VideoRate, "-bufsize", "2M",
			"-c:a", "aac", "-b:a", r.AudioRate,
			"-hls_key_info_file", keyInfoPath,
			"-f", "hls",
			"-hls_time", hlsSec,
			"-hls_playlist_type", "vod",
			"-hls_segment_filename", filepath.Join(outDir, r.Name+"_%03d.ts"),
			filepath.Join(outDir, r.Name+".m3u8"),
		}
		logDRMf(taskID, mediaID, "ffmpeg aes128 rung start: rung=%s", r.Name)
		if out, runErr := storage.RunFFmpeg(ctx, w.DB, w.Vault, w.FFmpegPath, mediaID, inputPath, 0, 0, nil, post, ""); runErr != nil {
			logDRMf(taskID, mediaID, "ffmpeg aes128 rung failed: rung=%s err=%v stderr=%s", r.Name, runErr, trimErrorMessage(string(out)))
			return "", fmt.Errorf("ffmpeg aes128 rung %d failed: %v; stderr: %s", i, runErr, string(out))
		}
		logDRMf(taskID, mediaID, "ffmpeg aes128 rung done: rung=%s", r.Name)
	}
	if err := writeMasterPlaylist(outDir, ladder); err != nil {
		return "", err
	}
	return filepath.Join(outDir, "master.m3u8"), nil
}

func (w *PackageWorker) persistAES128KeyMaterial(mediaID int64, kidHex, keyHex, ivHex string) error {
	if w == nil || w.DB == nil || mediaID <= 0 {
		return nil
	}
	_, err := w.DB.Exec(`
		INSERT INTO drm_key_material (media_id, mode, kid, key_hex, iv_hex, updated_at)
		VALUES (?, 'hls_aes_128', ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(media_id) DO UPDATE SET
		  mode='hls_aes_128',
		  kid=excluded.kid,
		  key_hex=excluded.key_hex,
		  iv_hex=excluded.iv_hex,
		  updated_at=CURRENT_TIMESTAMP
	`, mediaID, strings.TrimSpace(kidHex), strings.TrimSpace(keyHex), strings.TrimSpace(ivHex))
	return err
}

func (w *PackageWorker) Cancel(taskID int64) bool {
	if w == nil || taskID <= 0 {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if c, ok := w.running[taskID]; ok {
		c()
		return true
	}
	return false
}

// RepairBrokenTasks scans historical DRM package tasks and marks outputs with missing
// master/variant/segments as failed. When retry=true, broken tasks are re-queued.
func (w *PackageWorker) RepairBrokenTasks(ctx context.Context, limit int, retry bool) (scanned int, broken int, retried int, err error) {
	if w == nil || w.DB == nil {
		return 0, 0, 0, nil
	}
	if limit <= 0 {
		limit = 200
	}
	rows, err := w.DB.Query(`
		SELECT id, status, COALESCE(output_path,'')
		FROM package_task
		WHERE pipeline_type='cmaf_drm'
		  AND status IN ('done','failed')
		ORDER BY id DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return 0, 0, 0, err
	}
	defer rows.Close()

	type item struct {
		id     int64
		status string
		out    string
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.status, &it.out); err != nil {
			continue
		}
		items = append(items, it)
	}
	for _, it := range items {
		scanned++
		out := strings.TrimSpace(it.out)
		valid := false
		if out != "" {
			if st, e := os.Stat(out); e == nil && !st.IsDir() {
				if verr := validateHLSArtifacts(out); verr == nil {
					valid = true
				}
			}
		}
		if valid {
			continue
		}
		broken++
		msg := "drm output is incomplete or missing (master/variant/segments not found)"
		_, _ = w.DB.Exec(`
			UPDATE package_task
			SET status='failed', progress=0, drm_status='failed', error_message=?, updated_at=CURRENT_TIMESTAMP
			WHERE id = ?
		`, msg, it.id)
		if retry {
			_, _ = w.DB.Exec(`
				UPDATE package_task
				SET status='waiting', progress=0, drm_status='waiting', source_cleanup_status='pending', error_message=NULL, updated_at=CURRENT_TIMESTAMP
				WHERE id = ? AND status='failed'
			`, it.id)
			retried++
			go func(taskID int64) {
				_ = w.RunTask(ctx, taskID)
			}(it.id)
		}
	}
	return scanned, broken, retried, nil
}

func shouldCleanup(uploadDir, sourcePath string) bool {
	up := strings.TrimSpace(uploadDir)
	src := strings.TrimSpace(sourcePath)
	if up == "" || src == "" {
		return false
	}
	upAbs, err := filepath.Abs(up)
	if err != nil {
		return false
	}
	srcAbs, err := filepath.Abs(src)
	if err != nil {
		return false
	}
	upAbs = filepath.Clean(upAbs)
	srcAbs = filepath.Clean(srcAbs)
	rel, err := filepath.Rel(upAbs, srcAbs)
	if err != nil || rel == "" {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func (w *PackageWorker) encryptedOutputDir(sourcePath, fileKey, profileKey string) string {
	src := strings.TrimSpace(sourcePath)
	if src != "" {
		parent := filepath.Dir(src)
		if strings.TrimSpace(parent) != "" && parent != "." {
			return filepath.Join(parent, ".knox-encrypted", fileKey, profileKey)
		}
	}
	// Fallback for unexpected empty/invalid source path.
	return filepath.Join(w.TranscodeDir, "drm", fileKey, profileKey)
}

func GenerateDRMMaterial() (kidHex string, keyHex string, err error) {
	kid := make([]byte, 16)
	key := make([]byte, 16)
	if _, err := rand.Read(kid); err != nil {
		return "", "", fmt.Errorf("generate kid: %w", err)
	}
	if _, err := rand.Read(key); err != nil {
		return "", "", fmt.Errorf("generate key: %w", err)
	}
	return hex.EncodeToString(kid), hex.EncodeToString(key), nil
}

func (w *PackageWorker) persistDRMMaterial(outDir string, mediaID int64, kidHex, keyHex string) (string, error) {
	refPath := filepath.Join(outDir, "drm_key_ref.json")
	payload := map[string]any{
		"media_id": mediaID,
		"kid":      kidHex,
		"key":      keyHex,
		"alg":      "cenc-aes-ctr",
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(refPath, raw, 0o600); err != nil {
		return "", err
	}
	return refPath, nil
}

func (w *PackageWorker) runCMAFDRMPackage(ctx context.Context, taskID, mediaID int64, inputPath, outDir string, ladder []Rendition, keyHex, kidHex string) (string, error) {
	intBase := filepath.Join(outDir, ".shaka_in")
	var lastErr error
	wrote := false
	for _, eng := range w.effectiveEngineOrder() {
		logDRMf(taskID, mediaID, "packaging engine try: engine=%s out_dir=%s", eng, outDir)
		if wrote {
			if err := resetDRMOutDir(outDir); err != nil {
				return "", err
			}
			wrote = false
		}
		switch eng {
		case "shaka":
			if strings.TrimSpace(w.shakaPath) == "" {
				logDRMf(taskID, mediaID, "packaging engine skip: engine=shaka reason=path_not_configured")
				continue
			}
			p, e := w.runShakaCMAF(ctx, taskID, mediaID, inputPath, outDir, intBase, ladder, keyHex, kidHex)
			if e == nil {
				if verr := validateHLSArtifacts(p); verr != nil {
					logDRMf(taskID, mediaID, "packaging engine invalid-output: engine=shaka err=%v", verr)
					lastErr, wrote = fmt.Errorf("shaka output validation failed: %w", verr), true
					continue
				}
				logDRMf(taskID, mediaID, "packaging engine success: engine=shaka master=%s", p)
				return p, nil
			}
			logDRMf(taskID, mediaID, "packaging engine failed: engine=shaka err=%v", e)
			lastErr, wrote = e, true
		case "ffmpeg":
			p, e := w.runCMAFHLS(ctx, taskID, mediaID, inputPath, outDir, ladder, keyHex, kidHex)
			if e == nil {
				if verr := validateHLSArtifacts(p); verr != nil {
					logDRMf(taskID, mediaID, "packaging engine invalid-output: engine=ffmpeg err=%v", verr)
					lastErr, wrote = fmt.Errorf("ffmpeg output validation failed: %w", verr), true
					continue
				}
				logDRMf(taskID, mediaID, "packaging engine success: engine=ffmpeg master=%s", p)
				return p, nil
			}
			logDRMf(taskID, mediaID, "packaging engine failed: engine=ffmpeg err=%v", e)
			lastErr, wrote = e, true
		}
	}
	if lastErr != nil {
		return "", fmt.Errorf("all drm packagers failed (last: %w)", lastErr)
	}
	return "", fmt.Errorf("drm packaging: no engine is usable; set drm_packaging (engine_order, shaka_packager_path) and ffmpeg_path")
}

func (w *PackageWorker) runCMAFHLS(ctx context.Context, taskID, mediaID int64, inputPath, outDir string, ladder []Rendition, keyHex, kidHex string) (string, error) {
	if strings.TrimSpace(inputPath) == "" {
		return "", fmt.Errorf("empty input path")
	}
	if strings.TrimSpace(w.FFmpegPath) == "" {
		return "", fmt.Errorf("ffmpeg path not configured")
	}
	if strings.TrimSpace(keyHex) == "" || strings.TrimSpace(kidHex) == "" {
		return "", fmt.Errorf("drm key material not configured")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	for i, r := range ladder {
		vf := fmt.Sprintf("scale=-2:%d", r.Height)
		hlsSec := fmt.Sprintf("%d", w.segmentSecOrDefault())
		post := []string{
			"-map", "0:v:0", "-map", "0:a:0?",
			"-vf", vf,
			"-c:v", "libx264", "-preset", "veryfast", "-b:v", r.VideoRate, "-maxrate", r.VideoRate, "-bufsize", "2M",
			"-c:a", "aac", "-b:a", r.AudioRate,
			"-encryption_scheme", "cenc-aes-ctr",
			"-encryption_key", keyHex,
			"-encryption_kid", kidHex,
			"-f", "hls",
			"-hls_time", hlsSec,
			"-hls_playlist_type", "vod",
			"-hls_segment_type", "fmp4",
			"-hls_fmp4_init_filename", r.Name + "_init.mp4",
			"-hls_segment_filename", filepath.Join(outDir, r.Name+"_%03d.m4s"),
			filepath.Join(outDir, r.Name+".m3u8"),
		}
		logDRMf(taskID, mediaID, "ffmpeg rung start: rung=%s", r.Name)
		if out, err := storage.RunFFmpeg(ctx, w.DB, w.Vault, w.FFmpegPath, mediaID, inputPath, 0, 0, nil, post, outDir); err != nil {
			logDRMf(taskID, mediaID, "ffmpeg rung failed: rung=%s err=%v stderr=%s", r.Name, err, trimErrorMessage(string(out)))
			return "", fmt.Errorf("ffmpeg rung %d failed: %v; stderr: %s", i, err, string(out))
		}
		logDRMf(taskID, mediaID, "ffmpeg rung done: rung=%s", r.Name)
	}
	if err := writeMasterPlaylist(outDir, ladder); err != nil {
		return "", err
	}
	return filepath.Join(outDir, "master.m3u8"), nil
}

func validateHLSArtifacts(masterPath string) error {
	if strings.TrimSpace(masterPath) == "" {
		return fmt.Errorf("empty master path")
	}
	masterDir := filepath.Dir(masterPath)
	raw, err := os.ReadFile(masterPath)
	if err != nil {
		return fmt.Errorf("read master: %w", err)
	}
	lines := strings.Split(string(raw), "\n")
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		if strings.Contains(ln, "://") {
			continue
		}
		vp := filepath.Join(masterDir, filepath.Clean(ln))
		if st, e := os.Stat(vp); e != nil || st.IsDir() {
			return fmt.Errorf("missing variant playlist: %s", vp)
		}
		if err := validateVariantPlaylist(vp); err != nil {
			return err
		}
	}
	return nil
}

func validateVariantPlaylist(playlistPath string) error {
	base := filepath.Dir(playlistPath)
	raw, err := os.ReadFile(playlistPath)
	if err != nil {
		return fmt.Errorf("read variant playlist %s: %w", playlistPath, err)
	}
	lines := strings.Split(string(raw), "\n")
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if strings.HasPrefix(ln, "#EXT-X-MAP:") {
			m := extXMapURIPattern.FindStringSubmatch(ln)
			if len(m) != 2 {
				continue
			}
			u := strings.TrimSpace(m[1])
			if idx := strings.Index(u, "?"); idx >= 0 {
				u = u[:idx]
			}
			if u == "" || strings.Contains(u, "://") {
				continue
			}
			p := filepath.Join(base, filepath.Clean(u))
			if st, e := os.Stat(p); e != nil || st.IsDir() {
				return fmt.Errorf("missing init segment: %s", p)
			}
			continue
		}
		if strings.HasPrefix(ln, "#") || strings.Contains(ln, "://") {
			continue
		}
		u := ln
		if idx := strings.Index(u, "?"); idx >= 0 {
			u = u[:idx]
		}
		p := filepath.Join(base, filepath.Clean(u))
		if st, e := os.Stat(p); e != nil || st.IsDir() {
			return fmt.Errorf("missing media segment: %s", p)
		}
	}
	return nil
}
