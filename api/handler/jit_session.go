package handler

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/internal/jit/hwenc"
	"knox-media/internal/jit/session"
	"knox-media/internal/playback"
)

func jitSegmentStartTime(seg int) float64 {
	return float64(seg) * session.JITSegmentDurationSeconds
}

// ensureJITTranscodeRunning starts ffmpeg when the session has no running encoder
// (for example after throttle pause cancelled the process).
func (h *Handler) ensureJITTranscodeRunning(s *session.Session) {
	s.Mu.Lock()
	busy := s.Cmd != nil
	s.Mu.Unlock()
	if busy {
		return
	}
	cfg := h.buildTranscodeConfig(s, jitSegmentStartTime(s.NextSegmentToEmit()))
	s.StartTranscode(cfg)
}

// jitServedContentType returns a correct media Content-Type for JIT outputs.
// OS mime tables often map ".ts" to text/plain (TypeScript); HLS segments need video/mp2t.
func jitServedContentType(filePath string) string {
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".ts":
		return "video/mp2t"
	case ".m3u8":
		return "application/vnd.apple.mpegurl"
	case ".m4s":
		return "video/iso.segment"
	case ".mp4":
		return "video/mp4"
	case ".vtt":
		return "text/vtt"
	default:
		return ""
	}
}

func jitAccessToken(c *gin.Context) string {
	t := strings.TrimSpace(c.Query("access_token"))
	if t != "" {
		return t
	}
	auth := c.GetHeader("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return auth[7:]
	}
	return ""
}

func jitPlaylistQuery(c *gin.Context, mediaID int64) string {
	q := url.Values{}
	if mediaID > 0 {
		q.Set("media_id", strconv.FormatInt(mediaID, 10))
	}
	if tok := jitAccessToken(c); tok != "" {
		q.Set("access_token", tok)
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

// lookupMediaIDFromJITSessionLog finds media_id from player access logs that included
// session_id in the message (see playback/progress handlers).
func (h *Handler) lookupMediaIDFromJITSessionLog(sessionID string) (int64, bool) {
	if h.App == nil || h.App.DB == nil || !session.ValidJITSessionID(sessionID) {
		return 0, false
	}
	needle := "session_id=" + sessionID
	pat := "%" + needle + "%"
	var mid sql.NullInt64
	err := h.App.DB.QueryRow(`
		SELECT media_id FROM activity_log
		WHERE COALESCE(media_id, 0) > 0
		  AND action IN ('playback_start', 'playback_end', 'progress')
		  AND message LIKE ?
		  AND datetime(created_at) >= datetime('now', '-90 days')
		ORDER BY id DESC
		LIMIT 1
	`, pat).Scan(&mid)
	if err != nil || !mid.Valid || mid.Int64 <= 0 {
		return 0, false
	}
	return mid.Int64, true
}

// tryReviveJITSession restores a session after idle cleanup using ?media_id= or a prior
// activity_log row (playback_start / playback_end / progress) that recorded session_id.
func (h *Handler) tryReviveJITSession(c *gin.Context, sessionID, asset string) (*session.Session, bool) {
	mediaID := int64(0)
	if v := strings.TrimSpace(c.Query("media_id")); v != "" {
		parsed, perr := strconv.ParseInt(v, 10, 64)
		if perr == nil && parsed > 0 {
			mediaID = parsed
		}
	}
	if mediaID <= 0 {
		recID, ok := h.lookupMediaIDFromJITSessionLog(sessionID)
		if !ok {
			return nil, false
		}
		mediaID = recID
	}
	if _, ok := h.requireMediaAccess(c, mediaID, true); !ok {
		return nil, false
	}
	var fileID, filePath sql.NullString
	var srcHeight, srcWidth, srcDuration sql.NullInt64
	if err := h.App.DB.QueryRow(
		`SELECT file_id, file_path, height, width, duration FROM media WHERE id = ?`,
		mediaID,
	).Scan(&fileID, &filePath, &srcHeight, &srcWidth, &srcDuration); err != nil {
		return nil, false
	}
	if !fileID.Valid || strings.TrimSpace(fileID.String) == "" || !filePath.Valid || strings.TrimSpace(filePath.String) == "" {
		return nil, false
	}
	homeLimit, _ := h.loadHomeStreamPlayback()
	caps := readClientCaps(c)
	bitrate, resolution := playback.PickJITParams(int(srcHeight.Int64), int(srcWidth.Int64), caps.MaxHeight, homeLimit)
	if !h.ensureEncryptedISOPipePlayback(c, mediaID, filePath.String) {
		return nil, false
	}
	s, err := h.SessionManager.RestoreSession(sessionID, mediaID, fileID.String, filePath.String, bitrate, resolution, float64(srcDuration.Int64))
	if err != nil {
		log.Printf("jit revive session %s: %v", sessionID, err)
		return nil, false
	}
	if s.StreamEncryption == nil {
		pol := h.loadStreamPolicy(mediaID)
		if pol.DRMEnabled {
			if streamEnc, encErr := h.ensureStreamJITEncryption(mediaID, pol, s.TempDir); encErr == nil {
				s.StreamEncryption = streamEnc
			}
		}
	}
	isMaster := asset == "/master.m3u8" || strings.TrimPrefix(asset, "/") == "master.m3u8"
	if isMaster {
		return s, true
	}
	segID := parseSegID(asset)
	if segID < 0 {
		return nil, false
	}
	log.Printf("jit session %s: revive at segment %d (session was gone)", sessionID, segID)
	s.PrepareSegmentWindow(segID)
	cfg := h.buildTranscodeConfig(s, jitSegmentStartTime(segID))
	s.StartTranscode(cfg)
	return s, true
}

// serveJITAsset serves files from a JIT session's temp directory.
// Route: GET /jit/session/:sessionID/*asset
func (h *Handler) ServeJITAsset(c *gin.Context) {
	sessionID := c.Param("sessionID")
	asset := c.Param("asset")
	if asset == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing asset path"})
		return
	}

	s := h.SessionManager.Get(sessionID)
	if s == nil {
		var ok bool
		s, ok = h.tryReviveJITSession(c, sessionID, asset)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
			return
		}
	}

	// Master playlist: generate a full VOD-style m3u8 dynamically.
	if asset == "/master.m3u8" || asset == "master.m3u8" {
		s.Mu.Lock()
		cmd := s.Cmd
		s.Mu.Unlock()
		if cmd == nil {
			h.startSessionTranscode(s)
		}
		c.Header("Content-Type", "application/vnd.apple.mpegurl")
		c.String(http.StatusOK, h.generateMasterM3U8(s, c))
		return
	}

	// Segment request: parse seg ID and apply scheduling rules.
	segID := parseSegID(asset)
	if segID < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid segment ID"})
		return
	}

	latest := s.LatestSegment()

	// Record player interest before waiting so seek transcodes keep the session alive.
	s.RecordRequest(segID)

	// Rollback before the current encode window: ffmpeg only emits segment_start_number..N;
	// requesting seg < StartSeg cannot be satisfied without a new transcode from that seg.
	if segID < s.StartSeg {
		log.Printf("jit session %s: rollback seg %d < current start %d, restarting ffmpeg", sessionID, segID, s.StartSeg)
		startTime := jitSegmentStartTime(segID)
		s.ResetForSeek(segID)
		cfg := h.buildTranscodeConfig(s, startTime)
		s.StartTranscode(cfg)
	} else if latest >= 0 && (segID-latest >= 3) {
		// Rule 5: far jump — requested seg jumps ≥ 2 ahead of latest → restart ffmpeg.
		// Both forward (segID > latest+1) and backward (segID < latest-1) jumps.
		log.Printf("jit session %s: far seek %d->%d, restarting ffmpeg", sessionID, latest, segID)
		startTime := jitSegmentStartTime(segID)
		s.ResetForSeek(segID)
		cfg := h.buildTranscodeConfig(s, startTime)
		s.StartTranscode(cfg)
	}

	// Rule 3: caught up — requested seg at or past latest → resume ffmpeg.
	if segID >= latest {
		h.resumeJITSessionWithStallGuard(s, latest, segID)
	}

	// Serve segment file. If not ready yet, wait up to 60s.
	filePath := filepath.Join(s.TempDir, filepath.Clean(asset))
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := os.Stat(filePath); err == nil {
			if ct := jitServedContentType(filePath); ct != "" {
				c.Header("Content-Type", ct)
			}
			http.ServeFile(c.Writer, c.Request, filePath)
			return
		}
		if s.Ctx().Err() != nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "segment not ready"})
}

// resumeJITSessionWithStallGuard clears throttle-pause state, starts ffmpeg if it was stopped,
// and if no new segment appears within 3s (playlist latest index unchanged), restarts transcode from restartFromSeg.
func (h *Handler) resumeJITSessionWithStallGuard(s *session.Session, latestBefore int, restartFromSeg int) {
	h.SessionManager.ResumeSession(s.ID)
	h.ensureJITTranscodeRunning(s)
	h.scheduleResumeStallGuard(s, latestBefore, restartFromSeg)
}

func (h *Handler) scheduleResumeStallGuard(s *session.Session, latestBefore int, restartFromSeg int) {
	ev := s.BumpResumeWatchEpoch()
	go func() {
		time.Sleep(3 * time.Second)
		if s.Ctx().Err() != nil {
			return
		}
		if s.ResumeWatchStale(ev) {
			return
		}
		if s.LatestSegment() > latestBefore {
			return
		}
		log.Printf("jit session %s: stalled after resume (latest %d, baseline %d); restarting from seg %d",
			s.ID, s.LatestSegment(), latestBefore, restartFromSeg)
		startTime := jitSegmentStartTime(restartFromSeg)
		s.ResetForSeek(restartFromSeg)
		cfg := h.buildTranscodeConfig(s, startTime)
		s.StartTranscode(cfg)
	}()
}

// parseSegID extracts the segment number from a path like "/0.ts" or "0.ts".
func parseSegID(path string) int {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	numStr := strings.TrimSuffix(base, ext)
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return -1
	}
	return n
}

// PauseJITSession stops the JIT ffmpeg pipeline (throttle); encoding restarts on the next resume or segment pull.
// Route: POST /jit/session/:sessionID/pause
func (h *Handler) PauseJITSession(c *gin.Context) {
	sessionID := c.Param("sessionID")
	h.SessionManager.PauseSession(sessionID)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ResumeJITSession clears pause state and ensures ffmpeg is running if it was stopped.
// Route: POST /jit/session/:sessionID/resume
func (h *Handler) ResumeJITSession(c *gin.Context) {
	sessionID := c.Param("sessionID")
	s := h.SessionManager.Get(sessionID)
	if s == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	latest0 := s.LatestSegment()
	h.SessionManager.ResumeSession(sessionID)
	h.ensureJITTranscodeRunning(s)
	restartSeg := s.LastRequestedSeg()
	if restartSeg < s.StartSeg {
		restartSeg = s.StartSeg
	}
	h.scheduleResumeStallGuard(s, latest0, restartSeg)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// EndJITSession cancels the transcode and cleans up.
// Route: POST /jit/session/:sessionID/end
func (h *Handler) EndJITSession(c *gin.Context) {
	sessionID := c.Param("sessionID")
	h.SessionManager.CancelSession(sessionID)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// SeekJITSession re-targets the transcode to a new segment.
// Route: POST /jit/session/:sessionID/seek?seg=N
func (h *Handler) SeekJITSession(c *gin.Context) {
	sessionID := c.Param("sessionID")
	segStr := strings.TrimSpace(c.Query("seg"))
	seg, err := strconv.Atoi(segStr)
	if err != nil || seg < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid seg"})
		return
	}

	s := h.SessionManager.Get(sessionID)
	if s == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}

	// If target is within current encoding range, no restart needed.
	if s.SegInRange(seg) {
		s.RecordRequest(seg)
		c.JSON(http.StatusOK, gin.H{"ok": true, "restarted": false})
		return
	}

	// Far seek: restart ffmpeg in the same session.
	log.Printf("jit session %s: seek %d, restarting ffmpeg", sessionID, seg)
	startTime := jitSegmentStartTime(seg)
	s.ResetForSeek(seg)
	s.RecordRequest(seg)
	cfg := h.buildTranscodeConfig(s, startTime)
	s.StartTranscode(cfg)

	c.JSON(http.StatusOK, gin.H{
		"ok":         true,
		"restarted":  true,
		"session_id": s.ID,
		"hls_master": fmt.Sprintf("/api/v1/jit/session/%s/master.m3u8?media_id=%d", s.ID, s.MediaID),
	})
}

// startSessionTranscode triggers ffmpeg for a new session.
func (h *Handler) startSessionTranscode(s *session.Session) {
	cfg := h.buildTranscodeConfig(s, jitSegmentStartTime(s.NextSegmentToEmit()))
	s.StartTranscode(cfg)
}

// buildTranscodeConfig reads media metadata from DB and builds the transcode config.
func (h *Handler) buildTranscodeConfig(s *session.Session, startTime float64) session.TranscodeConfig {
	var audioCodec sql.NullString
	var metaJSON sql.NullString

	_ = h.App.DB.QueryRow(
		`SELECT COALESCE(m.meta_json,'') FROM media m WHERE m.id = ? LIMIT 1`,
		s.MediaID,
	).Scan(&metaJSON)

	// Extract audio codec from ffprobe meta_json or use empty (will be probed).
	_ = metaJSON // placeholder

	_ = h.App.DB.QueryRow(
		`SELECT COALESCE((SELECT codec_name FROM json_each(m.meta_json, '$.streams') WHERE json_extract(value, '$.codec_type') = 'audio' LIMIT 1), '') FROM media m WHERE m.id = ? LIMIT 1`,
		s.MediaID,
	).Scan(&audioCodec)

	// Also try direct meta_json extraction.
	if audioCodec.String == "" || !audioCodec.Valid {
		// Fallback: probe from file.
		_ = h.App.DB.QueryRow(
			`SELECT COALESCE(meta_json, '') FROM media WHERE id = ? LIMIT 1`,
			s.MediaID,
		).Scan(&metaJSON)
		if metaJSON.Valid && metaJSON.String != "" {
			// Simple extraction: look for "codec_name":"aac" or similar in the JSON.
			idx := strings.Index(metaJSON.String, `"codec_type":"audio"`)
			if idx >= 0 {
				codecIdx := strings.LastIndex(metaJSON.String[:idx], `"codec_name":"`)
				if codecIdx >= 0 {
					end := strings.Index(metaJSON.String[codecIdx+14:], `"`)
					if end > 0 {
						audioCodec.String = metaJSON.String[codecIdx+14 : codecIdx+14+end]
						audioCodec.Valid = true
					}
				}
			}
		}
	}

	txSettings := h.loadTranscoderSettings()
	encoder := txSettings.EffectiveHWEncoderID()
	useHW := txSettings.EnableHardwareEncoding && encoder != hwenc.Libx264
	return session.TranscodeConfig{
		SourcePath: s.SourcePath,
		Bitrate:    s.Bitrate,
		Resolution: s.Resolution,
		AudioCodec: strings.ToLower(strings.TrimSpace(audioCodec.String)),
		StartTime:  startTime,
		X264Preset: txSettings.InstantX264Preset(),
		CRF:        txSettings.InstantCRF(),
		UseHWEncoding: useHW,
		VideoEncoder:  encoder,
	}
}

// generateMasterM3U8 produces a complete VOD-style media playlist.
// It lists ALL segments (0..totalSegments-1) with absolute URLs plus access_token.
func (h *Handler) generateMasterM3U8(s *session.Session, c *gin.Context) string {
	base := fmt.Sprintf("http://%s", c.Request.Host)
	sessionURL := fmt.Sprintf("%s/api/v1/jit/session/%s", base, s.ID)

	qs := jitPlaylistQuery(c, s.MediaID)

	segDuration := session.JITSegmentDurationSeconds
	totalSegs := int(s.Duration / segDuration)
	// Handle fractional last segment.
	if s.Duration-float64(totalSegs)*segDuration > 0.1 {
		totalSegs++
	}
	lastDur := s.Duration - float64(totalSegs-1)*segDuration
	if lastDur < 0.1 {
		lastDur = segDuration
	}

	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")
	sb.WriteString("#EXT-X-VERSION:3\n")
	if keyLine := h.streamDRMPlaylistKeyLine(base, s, c); keyLine != "" {
		sb.WriteString(keyLine + "\n")
	}
	sb.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", int(segDuration)+1))
	sb.WriteString(fmt.Sprintf("#EXT-X-MEDIA-SEQUENCE:%d\n", s.StartSeg))
	// For seek: the playlist is not truly VOD since we exclude earlier segments.
	if s.StartSeg == 0 {
		sb.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	}

	for i := s.StartSeg; i < totalSegs; i++ {
		dur := segDuration
		if i == totalSegs-1 {
			dur = lastDur
		}
		sb.WriteString(fmt.Sprintf("#EXTINF:%.3f,\n", dur))
		sb.WriteString(fmt.Sprintf("%s/%d.ts%s\n", sessionURL, i, qs))
	}
	sb.WriteString("#EXT-X-ENDLIST\n")
	return sb.String()
}
