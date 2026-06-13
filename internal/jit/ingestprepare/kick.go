package ingestprepare

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"knox-media/internal/mediautil"
	models "knox-media/internal/model"
)

// Scheduler is the subset of JIT scheduler used at ingest (Redis meta + slice trigger).
type Scheduler interface {
	PrepareVideoMeta(fileID, filePath, format, videoCodec, audioCodec string) error
	TriggerSlicing(fileID, sessionID string) error
	SetAudioPlaylists(fileID string, playlists []models.AudioPlaylistInfo)
}

// Kick runs PrepareVideoMeta + TriggerSlicing when the library has jit_prepare_on_ingest.
// Transport stream real-time encryption (drm_enabled) skips ingest pre-encoding; packaging runs at play time.
// Set KNOX_MEDIA_JIT_INGEST_DISABLE=1 to skip (ops kill switch).
func Kick(db *sql.DB, sched Scheduler, mediaID int64) {
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
	var jitPrepare int
	var drmEnabled int
	err := db.QueryRow(`
		SELECT COALESCE(m.file_id,''), COALESCE(m.file_path,''), COALESCE(m.format,''), COALESCE(m.meta_json,''), COALESCE(l.jit_prepare_on_ingest,0), COALESCE(l.drm_enabled,0)
		FROM media m
		LEFT JOIN library l ON l.id = m.library_id
		WHERE m.id = ?
	`, mediaID).Scan(&fileID, &filePath, &formatCol, &metaJSON, &jitPrepare, &drmEnabled)
	if err != nil {
		return
	}
	if drmEnabled != 0 {
		return
	}
	if jitPrepare == 0 {
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
	var atrackStatus sql.NullString
	if err := db.QueryRow(`SELECT status FROM atrack_task WHERE media_id = ? AND status = 'done' LIMIT 1`, mediaID).Scan(&atrackStatus); err == nil && atrackStatus.String == "done" {
		audioURL := fmt.Sprintf("/atracks/%d/0/index.m3u8", mediaID)
		sched.SetAudioPlaylists(fileID, []models.AudioPlaylistInfo{
			{Index: 0, Language: "und", URL: audioURL},
		})
	}
	if err := sched.TriggerSlicing(fileID, "ingest:"+strconv.FormatInt(mediaID, 10)); err != nil {
		log.Printf("ingest JIT TriggerSlicing media=%d: %v", mediaID, err)
	}
}
