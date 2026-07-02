package handler

import (
	"database/sql"
	"path/filepath"
	"strings"
	"sync"

	"knox-media/cmd/scheduler"
	"knox-media/internal/app"
	"knox-media/internal/atrack"
	"knox-media/internal/config"
	"knox-media/internal/doccover"
	"knox-media/internal/jit/session"
	"knox-media/internal/keyframe"
	"knox-media/internal/keystore"
	"knox-media/internal/lyrictask"
	"knox-media/internal/photoclass"
	"knox-media/internal/photoface"
	"knox-media/internal/photogeocode"
	"knox-media/internal/preview"
	"knox-media/internal/storage"
	"knox-media/internal/subtitle"
	"knox-media/internal/transcode"
	"knox-media/internal/upload"
)

type Handler struct {
	App                 *app.App
	Worker              *transcode.Worker
	PackageWorker       *transcode.PackageWorker
	PreviewWorker       *preview.Worker
	Subtitle            *subtitle.Service
	Upload              *upload.Service
	Instant             *scheduler.Scheduler
	SessionManager      *session.Manager
	AtrackWorker        *atrack.Worker
	KeyframeWorker      *keyframe.Worker
	LyricWorker         *lyrictask.Worker
	PhotoClassifyWorker *photoclass.Worker
	PhotoGeocode        *photogeocode.Service
	PhotoLocationWorker *photogeocode.Worker
	PhotoFaceWorker     *photoface.Worker
	DocCoverWorker      *doccover.Worker
	KeyVault            *keystore.Vault
	AssetEncryptor      *storage.AssetEncryptor
	DerivedStore        *storage.DerivedAssetStore
	scanMu              sync.Mutex
	scrapeRunMu         sync.Mutex
	runningScans        map[int64]scanRuntime
}

func New(a *app.App, w *transcode.Worker, pkgw *transcode.PackageWorker, pw *preview.Worker, sub *subtitle.Service, u *upload.Service, instant *scheduler.Scheduler, sm *session.Manager, atw *atrack.Worker, kfw *keyframe.Worker, lw *lyrictask.Worker, pcw *photoclass.Worker, dcw *doccover.Worker, keyVault *keystore.Vault, assetEnc *storage.AssetEncryptor, derived *storage.DerivedAssetStore) *Handler {
	h := &Handler{App: a, Worker: w, PackageWorker: pkgw, PreviewWorker: pw, Subtitle: sub, Upload: u, Instant: instant, SessionManager: sm, AtrackWorker: atw, KeyframeWorker: kfw, LyricWorker: lw, PhotoClassifyWorker: pcw, DocCoverWorker: dcw, KeyVault: keyVault, AssetEncryptor: assetEnc, DerivedStore: derived, PhotoGeocode: photogeocode.New(a.DB), runningScans: map[int64]scanRuntime{}}
	_ = h.PhotoGeocode.EnsureSchema()
	h.PhotoLocationWorker = photogeocode.NewWorker(a.DB, keyVault, h.PhotoGeocode)
	h.PhotoFaceWorker = photoface.NewWorker(a.DB, keyVault, derived, filepath.Dir(a.ConfigPath), a.Config.FFmpeg.FFmpegPath, a.Config.Data.Preview, func() config.PhotoFaceConfig {
		cfg := a.Config.PhotoFace
		if strings.TrimSpace(cfg.PythonPath) == "" {
			cfg.PythonPath = a.Config.PhotoClassify.PythonPath
		}
		if strings.TrimSpace(cfg.ScriptPath) == "" {
			cfg.ScriptPath = "tools/photo_face/detect.py"
		}
		return cfg
	})
	return h
}

type scanRuntime struct {
	TaskID int64
	Cancel func()
}

func (h *Handler) logActivity(userID int64, username, action string, mediaID *int64, message string) {
	if h == nil || h.App == nil || h.App.DB == nil || action == "" {
		return
	}
	var uid any
	if userID > 0 {
		uid = userID
	}
	var mid any
	if mediaID != nil && *mediaID > 0 {
		mid = *mediaID
	}
	var uname any
	if username != "" {
		uname = username
	}
	var msg any
	if message != "" {
		msg = message
	}
	_, _ = h.App.DB.Exec(
		`INSERT INTO activity_log (user_id, username, action, media_id, message) VALUES (?, ?, ?, ?, ?)`,
		uid, uname, action, mid, msg,
	)
}

func nullInt64(v sql.NullInt64) int64 {
	if v.Valid {
		return v.Int64
	}
	return 0
}
