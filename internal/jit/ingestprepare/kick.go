package ingestprepare

import (
	"context"
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"knox-media/internal/mediautil"
	"knox-media/internal/transcode"
)

// Scheduler is the subset of JIT scheduler used at ingest (Redis meta + slice trigger).
type Scheduler interface {
	PrepareVideoMeta(fileID, filePath, format, videoCodec, audioCodec string) error
	TriggerSlicing(fileID, sessionID string) error
}

// Kick runs PrepareVideoMeta + TriggerSlicing when the library has jit_prepare_on_ingest or drm_enabled.
// For DRM libraries it also kicks Worker.EnsureHLS (full ladder) in addition to the existing package pipeline.
// Set KNOX_MEDIA_JIT_INGEST_DISABLE=1 to skip (ops kill switch).
func Kick(db *sql.DB, sched Scheduler, worker *transcode.Worker, mediaID int64) {
	if db == nil || mediaID <= 0 || sched == nil {
		return
	}
	rv := reflect.ValueOf(sched)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface:
		if rv.IsNil() {
			return
		}
	}
	if strings.TrimSpace(os.Getenv("KNOX_MEDIA_JIT_INGEST_DISABLE")) == "1" {
		return
	}
	var fileID, filePath, formatCol, metaJSON string
	var height int
	var drmEnabled, jitPrepare int
	err := db.QueryRow(`
		SELECT COALESCE(m.file_id,''), COALESCE(m.file_path,''), COALESCE(m.format,''), COALESCE(m.meta_json,''), COALESCE(m.height,0),
		       COALESCE(l.drm_enabled,0), COALESCE(l.jit_prepare_on_ingest,0)
		FROM media m
		LEFT JOIN library l ON l.id = m.library_id
		WHERE m.id = ?
	`, mediaID).Scan(&fileID, &filePath, &formatCol, &metaJSON, &height, &drmEnabled, &jitPrepare)
	if err != nil {
		return
	}
	if jitPrepare == 0 && drmEnabled == 0 {
		return
	}
	fileID = strings.TrimSpace(fileID)
	filePath = strings.TrimSpace(filePath)
	if fileID == "" || filePath == "" {
		return
	}
	fp := filepath.Clean(filePath)
	if fp == "." {
		return
	}

	prof := mediautil.CodecsFromMetaJSON(metaJSON)
	formatStr := strings.TrimSpace(formatCol)
	if formatStr == "" {
		formatStr = prof.Container
	}

	if err := sched.PrepareVideoMeta(fileID, fp, formatStr, prof.Video, prof.Audio); err != nil {
		log.Printf("ingest JIT PrepareVideoMeta media=%d: %v", mediaID, err)
		return
	}
	if err := sched.TriggerSlicing(fileID, "ingest:"+strconv.FormatInt(mediaID, 10)); err != nil {
		log.Printf("ingest JIT TriggerSlicing media=%d: %v", mediaID, err)
	}

	if drmEnabled != 0 && worker != nil {
		go func(mid int64, fid, path string, srcH int) {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
			defer cancel()
			h := srcH
			if h <= 0 {
				h = 1080
			}
			if _, _, _, err := worker.EnsureHLS(ctx, fid, path, h, 1080, nil); err != nil {
				log.Printf("ingest DRM EnsureHLS media=%d: %v", mid, err)
			}
		}(mediaID, fileID, fp, height)
	}
}
