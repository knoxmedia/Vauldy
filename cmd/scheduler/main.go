// cmd/scheduler/main.go
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"go.uber.org/zap"

	"knox-media/internal/jit/profile"
	models "knox-media/internal/model"
)

// jitPausedSessionTTL is Redis TTL for session:* while transcode_paused=1 (pause without segment heartbeats).
const jitPausedSessionTTL = 2 * time.Hour

// sliceQueueKey / sliceLegacyChannel must match cmd/sliceworker.SliceQueueKey / SliceQueueLegacyChannel.
// They are duplicated here to avoid importing the worker package (would create a cycle / pull tools).
const (
	sliceQueueKey      = "slice:jobs:queue"
	sliceLegacyChannel = "slice:jobs"
)

// sliceStaleAfter mirrors sliceworker.SliceStaleAfter. After this long without a heartbeat
// the scheduler treats status=slicing as lost and forces a fresh enqueue.
const sliceStaleAfter = 60 * time.Second

const maxSliceMetaErr = 2000

// maxSliceFailureRetries is how many times we re-queue slicing after status=failed before giving up
// (matches historical behavior: first 3 re-attempts allowed, then circuit open).
const maxSliceFailureRetries = 3

func trimSliceMetaErr(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxSliceMetaErr {
		return s
	}
	return s[:maxSliceMetaErr] + "..."
}

type Scheduler struct {
	redis                *redis.Client
	storage              Storage
	logger               *zap.Logger
	mu                   sync.RWMutex
	sliceLock            map[string]*sync.Mutex
	hlsMultiAudioEnabled bool
	hlsContinuous        bool
}

type Storage interface {
	FileExists(path string) bool
	GetFileInfo(path string) (*models.VideoMetadata, error)
	GetSegmentPath(fileID string, segID int, segmentType string) string
	SaveSegment(fileID string, segID int, segmentType string, data []byte) error
	LoadSegment(fileID string, segID int, segmentType string, bitrate string) ([]byte, error)
}

func NewScheduler(redisClient *redis.Client, storage Storage) *Scheduler {
	return &Scheduler{
		redis:     redisClient,
		storage:   storage,
		logger:    zap.L(),
		sliceLock: make(map[string]*sync.Mutex),
	}
}

// SetHLSMultiAudioEnabled controls whether the master playlist may emit separate audio groups.
func (s *Scheduler) SetHLSMultiAudioEnabled(v bool) {
	if s != nil {
		s.hlsMultiAudioEnabled = v
	}
}

// SetHLSContinuous controls whether continuous HLS mode is active.
func (s *Scheduler) SetHLSContinuous(v bool) {
	if s != nil {
		s.hlsContinuous = v
	}
}

func (s *Scheduler) RegisterRoutes(r gin.IRouter) {
	r.GET("/jit/master/:fileId", s.handleMasterPlaylist)
	r.GET("/jit/playlist/:fileId/video/:bitrate", s.handleVideoPlaylist)
	r.GET("/jit/playlist/:fileId/audio/:lang", s.handleAudioPlaylist)
	r.GET("/jit/segment/:fileId/video/:bitrate/:segId", s.handleVideoSegment)
	r.GET("/jit/segment/:fileId/audio/:lang/:segId", s.handleAudioSegment)
	r.POST("/jit/session/pause", s.handleJITSessionPause)
	r.POST("/jit/session/resume", s.handleJITSessionResume)
	r.POST("/jit/session/end", s.handleJITSessionEnd)
	r.POST("/jit/session/seek", s.handleJITSessionSeek)
}

func (s *Scheduler) PrepareVideoMeta(fileID, filePath, format, videoCodec, audioCodec string) error {
	ctx := context.Background()
	return s.redis.HSet(ctx, "video:meta:"+fileID,
		"file_path", filePath,
		"format", format,
		"codec", videoCodec,
		"audio_codec", audioCodec,
	).Err()
}

// PrepareVideoMetaExt 同步 width/height/duration 等附加字段，便于 master playlist 无需等待切片即可
// 选定单清晰度档位。重复键以最新值覆盖。
func (s *Scheduler) PrepareVideoMetaExt(fileID string, width, height int, duration float64) {
	if s == nil || s.redis == nil || strings.TrimSpace(fileID) == "" {
		return
	}
	ctx := context.Background()
	args := []interface{}{}
	if width > 0 {
		args = append(args, "width", width)
	}
	if height > 0 {
		args = append(args, "height", height)
	}
	if duration > 0 {
		args = append(args, "duration", duration)
	}
	if len(args) == 0 {
		return
	}
	_ = s.redis.HSet(ctx, "video:meta:"+fileID, args...).Err()
}

// SetAudioPlaylists stores pre-extracted HLS audio playlist infos for this file.
// When set, generateMasterPlaylist will emit EXT-X-MEDIA AUDIO groups for each track,
// and the transcodeworker will skip audio encoding (-an).
func (s *Scheduler) SetAudioPlaylists(fileID string, playlists []models.AudioPlaylistInfo) {
	if s == nil || s.redis == nil || strings.TrimSpace(fileID) == "" {
		return
	}
	if len(playlists) == 0 {
		return
	}
	b, err := json.Marshal(playlists)
	if err != nil {
		return
	}
	_ = s.redis.HSet(context.Background(), "video:meta:"+fileID, "audio_playlists", string(b)).Err()
}

// SetAudioPlaylist stores the URL path to a pre-extracted HLS audio playlist for this file.
// Deprecated: use SetAudioPlaylists for multi-track support.
func (s *Scheduler) SetAudioPlaylist(fileID string, audioPlaylistURL string) {
	if s == nil || s.redis == nil || strings.TrimSpace(fileID) == "" {
		return
	}
	// Upgrade single URL to the multi-track format.
	playlists := []models.AudioPlaylistInfo{
		{Index: 0, Language: "und", Codec: "aac", URL: audioPlaylistURL},
	}
	b, _ := json.Marshal(playlists)
	_ = s.redis.HSet(context.Background(), "video:meta:"+fileID, "audio_playlists", string(b)).Err()
	// Keep backward compat key for transcodeworker.
	_ = s.redis.HSet(context.Background(), "video:meta:"+fileID, "audio_playlist", audioPlaylistURL).Err()
}

// SetKeyframeCachePath stores the path to pre-extracted keyframe JSON cache.
func (s *Scheduler) SetKeyframeCachePath(fileID string, kfCachePath string) {
	if s == nil || s.redis == nil || strings.TrimSpace(fileID) == "" {
		return
	}
	_ = s.redis.HSet(context.Background(), "video:meta:"+fileID, "kf_cache_path", kfCachePath).Err()
}

func (s *Scheduler) TriggerSlicing(fileID, sessionID string) error {
	return s.ensureVideoSliced(fileID, sessionID)
}

// EndSession 删除 session:* Redis 键并清理相关辅助键。transcodeworker 在下一次心跳时检测到
// session 不再存活后会向其 ffmpeg 子进程发 SIGKILL，从而立即释放转码资源。
// 用户主动退出播放或客户端关闭页面时调用本方法。
//
// 当该会话退出后没有其他同 fileID 的活跃会话时，同步清理切片产物（ts/raw/audio/raw/video/audio）
// 与 Redis 索引/状态键，避免长视频残留几 GB 的临时切片。
func (s *Scheduler) EndSession(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || s == nil || s.redis == nil {
		return
	}
	ctx := context.Background()

	fileID, _ := s.redis.HGet(ctx, "session:"+sessionID, "file_id").Result()
	fileID = strings.TrimSpace(fileID)

	s.redis.Del(ctx, "session:"+sessionID)
	s.redis.Del(ctx, "jit:session_seek_boost:"+sessionID)

	if fileID == "" {
		return
	}
	// Always stop continuous HLS for this file (worker goroutine will also self-detect session gone).
	if !s.fileHasOtherActiveSessions(fileID, sessionID) {
		s.stopAllContinuousHLSForFile(fileID)
	}
	if s.fileHasOtherActiveSessions(fileID, sessionID) {
		return
	}
	s.cleanupFileArtifacts(fileID)
}

// fileHasOtherActiveSessions 扫描所有 session:* 键，看是否还有别的会话观看同一 fileID。
// 用 SCAN MATCH 防止 KEYS 阻塞 Redis；结果数通常是 1-3。
func (s *Scheduler) fileHasOtherActiveSessions(fileID, excludeSessionID string) bool {
	ctx := context.Background()
	iter := s.redis.Scan(ctx, 0, "session:*", 64).Iterator()
	excludeKey := "session:" + excludeSessionID
	for iter.Next(ctx) {
		key := iter.Val()
		if key == excludeKey {
			continue
		}
		fid, err := s.redis.HGet(ctx, key, "file_id").Result()
		if err != nil {
			continue
		}
		if strings.TrimSpace(fid) == fileID {
			return true
		}
	}
	return false
}

// cleanupFileArtifacts 删除该 fileID 在切片存储里的所有产物 (ts/raw/audio/raw/video) 以及
// Redis 中的 video:index/video:meta/lock 等键。无活跃会话时调用。
// 关键帧缓存 (cacheDir/<fileID>.json) 不删除——下次再播放复用。
func (s *Scheduler) cleanupFileArtifacts(fileID string) {
	if s == nil || s.storage == nil {
		return
	}
	if c, ok := s.storage.(interface{ CleanupFile(string) error }); ok {
		if err := c.CleanupFile(fileID); err != nil {
			s.logger.Warn("cleanup file artifacts failed",
				zap.String("file_id", fileID), zap.Error(err))
		} else {
			s.logger.Info("cleaned up file artifacts", zap.String("file_id", fileID))
		}
	}
	ctx := context.Background()
	s.redis.Del(ctx,
		"video:index:"+fileID,
		"video:meta:"+fileID,
		"retry:slice:"+fileID,
	)
	// 状态键按 segment_id+bitrate 分散，使用 SCAN 清理
	for _, prefix := range []string{
		"segment:status:" + fileID + ":",
		"segment:access:" + fileID + ":",
		"transcode:stats:" + fileID + ":",
	} {
		iter := s.redis.Scan(ctx, 0, prefix+"*", 256).Iterator()
		batch := make([]string, 0, 32)
		flush := func() {
			if len(batch) > 0 {
				s.redis.Del(ctx, batch...)
				batch = batch[:0]
			}
		}
		for iter.Next(ctx) {
			batch = append(batch, iter.Val())
			if len(batch) >= 256 {
				flush()
			}
		}
		flush()
	}
	// 删除该 fileID 的待办 transcode 任务
	s.dropAllTranscodeTasksForFile(fileID)
	// 停止所有 continuous HLS worker
	s.stopAllContinuousHLSForFile(fileID)
}

// stopAllContinuousHLSForFile deletes continuous HLS keys for all bitrates of a file.
func (s *Scheduler) stopAllContinuousHLSForFile(fileID string) {
	if s == nil || s.redis == nil {
		return
	}
	ctx := context.Background()
	prefix := fmt.Sprintf("continuous:hls:%s:", fileID)
	iter := s.redis.Scan(ctx, 0, prefix+"*", 64).Iterator()
	batch := make([]string, 0, 16)
	flush := func() {
		if len(batch) > 0 {
			s.redis.Del(ctx, batch...)
			batch = batch[:0]
		}
	}
	for iter.Next(ctx) {
		batch = append(batch, iter.Val())
		if len(batch) >= 64 {
			flush()
		}
	}
	flush()
	// Also clear continuous lock keys.
	lockPrefix := fmt.Sprintf("lock:continuous:hls:%s:", fileID)
	iter2 := s.redis.Scan(ctx, 0, lockPrefix+"*", 64).Iterator()
	for iter2.Next(ctx) {
		s.redis.Del(ctx, iter2.Val())
	}
}

// dropAllTranscodeTasksForFile 同时清空 high/low 队列中属于 fileID 的待办任务。
func (s *Scheduler) dropAllTranscodeTasksForFile(fileID string) {
	if s == nil || s.redis == nil {
		return
	}
	ctx := context.Background()
	needle := `"file_id":"` + fileID + `"`
	for _, q := range []string{"transcode:queue:high", "transcode:queue:low"} {
		members, err := s.redis.ZRange(ctx, q, 0, -1).Result()
		if err != nil {
			continue
		}
		stale := make([]interface{}, 0, len(members))
		for _, m := range members {
			if strings.Contains(m, needle) {
				stale = append(stale, m)
			}
		}
		if len(stale) > 0 {
			s.redis.ZRem(ctx, q, stale...)
		}
	}
}

// MarkSessionSeek 提升会话的 seek 状态，由播放器在拖动进度条后调用。
func (s *Scheduler) MarkSessionSeek(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || s == nil || s.redis == nil {
		return
	}
	s.markSessionSeekBoost(sessionID)
}

// ==================== Master Playlist ====================
// handleMasterPlaylist 不再阻塞等待切片完成。仅需 video:meta（由 PrepareVideoMeta + PrepareVideoMetaExt 写入）
// 即可决定 single-quality 档位并立刻返回。切片在后台并行进行；当客户端拉取 variant playlist
// (handleVideoPlaylist) 时会等待索引 ready（最多 30s）再返回 #EXTINF 列表。
func (s *Scheduler) handleMasterPlaylist(c *gin.Context) {
	fileID := c.Param("fileId")
	sessionID := s.getOrCreateSessionID(c)

	ctx := context.Background()
	meta, err := s.redis.HGetAll(ctx, "video:meta:"+fileID).Result()
	if err != nil || len(meta) == 0 || strings.TrimSpace(meta["file_path"]) == "" {
		c.JSON(404, gin.H{"error": "Video not prepared; call /hls?... first"})
		return
	}

	// 后台触发切片（幂等：sliceworker 通过 video:meta status 字段去重）
	go func(fid, sid string) {
		_ = s.ensureVideoSliced(fid, sid)
	}(fileID, sessionID)

	metadata := videoMetadataFromHash(fileID, meta)

	content := s.generateMasterPlaylist(c, fileID, metadata)
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Type", "application/vnd.apple.mpegurl")
	c.String(200, content)
}

// videoMetadataFromHash 把 redis HGetAll 的扁平结果转成 *VideoMetadata；缺失字段保持零值。
func videoMetadataFromHash(fileID string, h map[string]string) *models.VideoMetadata {
	m := &models.VideoMetadata{
		FileID:     fileID,
		FilePath:   strings.TrimSpace(h["file_path"]),
		Codec:      strings.TrimSpace(h["codec"]),
		AudioCodec: strings.TrimSpace(h["audio_codec"]),
		Format:     strings.TrimSpace(h["format"]),
	}
	if raw := strings.TrimSpace(h["audio_playlists"]); raw != "" {
		_ = json.Unmarshal([]byte(raw), &m.AudioPlaylists)
	}
	m.Duration, _ = strconv.ParseFloat(strings.TrimSpace(h["duration"]), 64)
	m.Width, _ = strconv.Atoi(strings.TrimSpace(h["width"]))
	m.Height, _ = strconv.Atoi(strings.TrimSpace(h["height"]))
	if v, err := strconv.ParseInt(strings.TrimSpace(h["size"]), 10, 64); err == nil {
		m.Size = v
	}
	return m
}

// jitAccessTokenForPlaylists reads the same JWT the client used for the master request (query or Bearer).
func jitAccessTokenForPlaylists(c *gin.Context) string {
	if q := strings.TrimSpace(c.Query("access_token")); q != "" {
		return q
	}
	h := c.GetHeader("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// jitURLWithAccessToken appends access_token so nested HLS requests from the browser stay authenticated.
func jitURLWithAccessToken(c *gin.Context, pathOrURL string) string {
	pathOrURL = strings.TrimSpace(pathOrURL)
	if pathOrURL == "" {
		return ""
	}
	tok := jitAccessTokenForPlaylists(c)
	if tok == "" {
		return pathOrURL
	}
	if strings.Contains(pathOrURL, "access_token=") {
		return pathOrURL
	}
	sep := "?"
	if strings.Contains(pathOrURL, "?") {
		sep = "&"
	}
	return pathOrURL + sep + "access_token=" + url.QueryEscape(tok)
}

// 确保视频已被切片（核心逻辑）
func (s *Scheduler) ensureVideoSliced(fileID, sessionID string) error {
	ctx := context.Background()

	// 检查切片状态
	status, err := s.redis.HGet(ctx, "video:meta:"+fileID, "status").Result()
	if err == redis.Nil {
		// 未开始切片，启动切片任务
		return s.startSlicingTask(fileID, sessionID)
	}

	switch status {
	case "ready":
		return nil
	case "slicing":
		// 心跳过期则视为僵死任务，重置后重新入队；否则照常等待。
		if s.sliceLooksStale(fileID) {
			s.logger.Warn("ensureVideoSliced found stale slicing on entry; recovering",
				zap.String("file_id", fileID))
			s.forceReenqueueSlicing(fileID, sessionID)
		}
		return s.waitForSlicingComplete(fileID, 180*time.Second)
	case "failed":
		// 限制重试次数；熔断后不得再 INCR，否则每请求计数上涨、错误文案漂移（after 7/8/9…）
		retryKey := "retry:slice:" + fileID
		n, err := s.redis.Get(ctx, retryKey).Int64()
		if err == redis.Nil {
			n = 0
		} else if err != nil {
			return err
		}
		if n >= maxSliceFailureRetries {
			detail, _ := s.redis.HGet(ctx, "video:meta:"+fileID, "slice_error").Result()
			detail = strings.TrimSpace(detail)
			if detail != "" {
				return fmt.Errorf("slicing failed after %d retries: %s", maxSliceFailureRetries, detail)
			}
			return fmt.Errorf("slicing failed after %d retries", maxSliceFailureRetries)
		}
		if _, err := s.redis.Incr(ctx, retryKey).Result(); err != nil {
			return err
		}
		s.redis.Expire(ctx, retryKey, 10*time.Minute)
		return s.startSlicingTask(fileID, sessionID)
	default:
		return fmt.Errorf("unknown status: %s", status)
	}
}

// 启动切片任务（分布式）
func (s *Scheduler) startSlicingTask(fileID, sessionID string) error {
	ctx := context.Background()

	// 获取分布式锁（防止多个节点同时切片）
	lockKey := "lock:slice:" + fileID
	locked, err := s.redis.SetNX(ctx, lockKey, sessionID, 300*time.Second).Result()
	if err != nil {
		return err
	}

	if !locked {
		// 其他节点已在切片，等待完成
		return s.waitForSlicingComplete(fileID, 180*time.Second)
	}
	defer s.redis.Del(ctx, lockKey)

	// 双重检查
	status, _ := s.redis.HGet(ctx, "video:meta:"+fileID, "status").Result()
	if status == "ready" {
		return nil
	}

	// 检查源文件是否可访问
	filePath, _ := s.redis.HGet(ctx, "video:meta:"+fileID, "file_path").Result()
	if _, statErr := os.Stat(filePath); statErr != nil {
		detail := trimSliceMetaErr(fmt.Sprintf("source not accessible: %s: %v", filePath, statErr))
		s.redis.HSet(ctx, "video:meta:"+fileID, "status", "failed", "slice_error", detail)
		return fmt.Errorf("source file not accessible: %s: %w", filePath, statErr)
	}

	// 更新状态为 slicing（新一次尝试，清掉上次的错误文案）；同时写入起点用于陈旧检测。
	now := time.Now().Unix()
	_ = s.redis.HDel(ctx, "video:meta:"+fileID, "slice_error").Err()
	s.redis.HSet(ctx, "video:meta:"+fileID,
		"status", "slicing",
		"slicing_started_at", now,
		"slicing_heartbeat_at", now,
	)

	task := &models.SliceTask{
		FileID:    fileID,
		SessionID: sessionID,
		CreatedAt: now,
	}

	taskData, _ := json.Marshal(task)
	// 1) 推到持久队列（worker 用 BLPop 消费，不会丢失）。
	if err := s.redis.RPush(ctx, sliceQueueKey, taskData).Err(); err != nil {
		s.redis.HSet(ctx, "video:meta:"+fileID, "status", "failed", "slice_error", trimSliceMetaErr("redis rpush slice queue: "+err.Error()))
		return err
	}
	// 2) 同时 publish 老 channel，向后兼容旧 subscriber（失败不致命）。
	if err := s.redis.Publish(ctx, sliceLegacyChannel, taskData).Err(); err != nil {
		s.logger.Warn("publish legacy slice:jobs failed (non-fatal)",
			zap.String("file_id", fileID), zap.Error(err))
	}

	s.logger.Info("Slicing task enqueued", zap.String("file_id", fileID))

	// 等待切片完成；超时时标记失败以便下次请求可重试
	if err := s.waitForSlicingComplete(fileID, 120*time.Second); err != nil {
		if err.Error() == "slicing timeout" {
			s.redis.HSet(ctx, "video:meta:"+fileID, "status", "failed",
				"slice_error", trimSliceMetaErr("slicing timeout after 120s (worker did not set status=ready; check sliceworker / ffmpeg)"))
		} else {
			// worker 或更早步骤已写入 slice_error 时只保证 status=failed
			s.redis.HSet(ctx, "video:meta:"+fileID, "status", "failed")
		}
		return err
	}
	return nil
}

// waitForIndexReady 等待 video:meta status 进入 ready/未触发；处于 slicing 时阻塞到 timeout。
// 与 waitForSlicingComplete 区别：本函数不需要预存 status（首次调用时会触发切片）。
// 当 status=slicing 但心跳过期，主动 re-enqueue 防止永久卡死。
func (s *Scheduler) waitForIndexReady(fileID string, timeout time.Duration) error {
	ctx := context.Background()
	deadline := time.Now().Add(timeout)
	triggered := false
	staleRetried := false
	for time.Now().Before(deadline) {
		status, err := s.redis.HGet(ctx, "video:meta:"+fileID, "status").Result()
		if err == redis.Nil || strings.TrimSpace(status) == "" {
			if !triggered {
				_ = s.ensureVideoSliced(fileID, "playlist")
				triggered = true
			}
		} else if status == "ready" {
			return nil
		} else if status == "failed" {
			detail, _ := s.redis.HGet(ctx, "video:meta:"+fileID, "slice_error").Result()
			detail = strings.TrimSpace(detail)
			if detail != "" {
				return fmt.Errorf("slicing failed: %s", detail)
			}
			return fmt.Errorf("slicing failed")
		} else if status == "slicing" && !staleRetried && s.sliceLooksStale(fileID) {
			s.logger.Warn("playlist wait found stale slicing; forcing re-enqueue",
				zap.String("file_id", fileID))
			s.forceReenqueueSlicing(fileID, "playlist-recover")
			staleRetried = true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timeout")
}

func (s *Scheduler) waitForSlicingComplete(fileID string, timeout time.Duration) error {
	ctx := context.Background()
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		status, err := s.redis.HGet(ctx, "video:meta:"+fileID, "status").Result()
		if err == nil {
			if status == "ready" {
				return nil
			}
			if status == "failed" {
				detail, _ := s.redis.HGet(ctx, "video:meta:"+fileID, "slice_error").Result()
				detail = strings.TrimSpace(detail)
				if detail != "" {
					return fmt.Errorf("slicing failed: %s", detail)
				}
				return fmt.Errorf("slicing failed")
			}
			if status == "slicing" && s.sliceLooksStale(fileID) {
				// 卡死自愈：worker 没在心跳，直接重置状态并重入队列。
				s.logger.Warn("slicing heartbeat stale; forcing re-enqueue",
					zap.String("file_id", fileID))
				s.forceReenqueueSlicing(fileID, "wait-recover")
			}
		}
		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("slicing timeout")
}

// sliceLooksStale 返回 true 当 video:meta 上的心跳/起点都晚于 sliceStaleAfter 之前。
// 字段缺失（worker 旧版本未写）也视为 stale，避免老 hash 永远卡 slicing。
func (s *Scheduler) sliceLooksStale(fileID string) bool {
	ctx := context.Background()
	got, err := s.redis.HMGet(ctx, "video:meta:"+fileID,
		"slicing_started_at", "slicing_heartbeat_at").Result()
	if err != nil || len(got) < 2 {
		return false
	}
	now := time.Now().Unix()
	hb := redisFieldInt64(got[1])
	st := redisFieldInt64(got[0])
	latest := hb
	if st > latest {
		latest = st
	}
	if latest == 0 {
		// 字段缺失：给 90s 宽限期再判 stale，防止刚启动的 worker 被误判
		return false
	}
	return now-latest >= int64(sliceStaleAfter.Seconds())
}

// forceReenqueueSlicing 清掉 slicing 状态与锁，重新把 slice 任务推到持久队列，但不阻塞等待。
// 调用方自行决定是否轮询 status；这样从 wait* 循环里调用本函数不会引起递归阻塞。
func (s *Scheduler) forceReenqueueSlicing(fileID, reason string) {
	if s == nil || s.redis == nil || strings.TrimSpace(fileID) == "" {
		return
	}
	ctx := context.Background()
	_ = s.redis.HDel(ctx, "video:meta:"+fileID,
		"status", "slice_error", "slicing_started_at", "slicing_heartbeat_at").Err()
	_ = s.redis.Del(ctx, "lock:slice:"+fileID).Err()
	_ = s.redis.Del(ctx, "retry:slice:"+fileID).Err()

	now := time.Now().Unix()
	s.redis.HSet(ctx, "video:meta:"+fileID,
		"status", "slicing",
		"slicing_started_at", now,
		"slicing_heartbeat_at", now,
	)
	task := &models.SliceTask{
		FileID:    fileID,
		SessionID: reason,
		CreatedAt: now,
	}
	taskData, _ := json.Marshal(task)
	if err := s.redis.RPush(ctx, sliceQueueKey, taskData).Err(); err != nil {
		s.logger.Warn("force re-enqueue rpush failed",
			zap.String("file_id", fileID), zap.Error(err))
	}
	_ = s.redis.Publish(ctx, sliceLegacyChannel, taskData).Err()
}

func redisFieldInt64(v interface{}) int64 {
	s, _ := v.(string)
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// ==================== Video Playlist ====================
func (s *Scheduler) handleVideoPlaylist(c *gin.Context) {
	fileID := c.Param("fileId")
	bitrate := c.Param("bitrate")

	// 客户端常常在 master 之后立刻拉 variant；如果切片正在进行，等待最多 30s（首播 fast path 通常 < 1s）。
	if err := s.waitForIndexReady(fileID, 30*time.Second); err != nil {
		c.JSON(404, gin.H{"error": "Video not ready: " + err.Error()})
		return
	}

	index, err := s.getSegmentIndex(fileID)
	if err != nil {
		c.JSON(404, gin.H{"error": "Video not ready"})
		return
	}

	target := targetDuration(index.VideoSegments)
	entries := make([]segmentEntry, 0, len(index.VideoSegments))
	for _, seg := range index.VideoSegments {
		segPath := fmt.Sprintf("/api/v1/jit/segment/%s/video/%s/%d", fileID, bitrate, seg.ID)
		entries = append(entries, segmentEntry{
			Duration: seg.Duration,
			URL:      jitURLWithAccessToken(c, segPath),
		})
	}
	content := renderMediaPlaylist(target, entries)

	// 缓存 Playlist（1小时；分片数量与时长在切片完成后保持稳定）
	c.Header("Cache-Control", "public, max-age=3600")
	c.Header("Content-Type", "application/vnd.apple.mpegurl")
	c.String(200, content)
}

// ==================== Audio Playlist ====================
func (s *Scheduler) handleAudioPlaylist(c *gin.Context) {
	fileID := c.Param("fileId")
	lang := c.Param("lang")

	if err := s.waitForIndexReady(fileID, 30*time.Second); err != nil {
		c.JSON(404, gin.H{"error": "Video not ready: " + err.Error()})
		return
	}

	index, err := s.getSegmentIndex(fileID)
	if err != nil {
		c.JSON(404, gin.H{"error": "Video not ready"})
		return
	}

	entries := make([]segmentEntry, 0, len(index.AudioSegments))
	for _, seg := range index.AudioSegments {
		// "und" 视为通配，匹配所有未标注语言的分片
		segLang := strings.TrimSpace(seg.Language)
		if segLang == "" {
			segLang = "und"
		}
		if !strings.EqualFold(segLang, lang) && lang != "und" {
			continue
		}
		segPath := fmt.Sprintf("/api/v1/jit/segment/%s/audio/%s/%d", fileID, lang, seg.ID)
		entries = append(entries, segmentEntry{
			Duration: seg.Duration,
			URL:      jitURLWithAccessToken(c, segPath),
		})
	}

	if len(entries) == 0 {
		c.JSON(404, gin.H{"error": "Audio track not found"})
		return
	}

	content := renderMediaPlaylist(targetDurationAudio(index.AudioSegments), entries)

	c.Header("Cache-Control", "public, max-age=3600")
	c.Header("Content-Type", "application/vnd.apple.mpegurl")
	c.String(200, content)
}

// ==================== Video Segment（即时转码核心）====================
func (s *Scheduler) handleVideoSegment(c *gin.Context) {
	fileID := c.Param("fileId")
	bitrate := c.Param("bitrate")
	segIDStr := c.Param("segId")
	segID, _ := strconv.Atoi(segIDStr)

	sessionID := s.getOrCreateSessionID(c)

	if prev, ok := s.previousSessionSegment(sessionID); ok {
		d := segID - prev
		if d < 0 {
			d = -d
		}
		// >= 2 段跳转即认为是 seek（>=4 时之前判定过宽，对快进 / 快退响应不及时）。
		if d >= 2 {
			s.markSessionSeekBoost(sessionID)
			// 清理之前位置堆积的 prefetch 任务，避免转码 worker 浪费 CPU 处理
			// 不可能再被请求到的段。会话内仍以本次 segID 为新的播放头。
			s.dropStaleTranscodeTasksForFile(fileID)
			// 重启连续 HLS 转码进程，以当前段号为起始。
			s.restartContinuousHLS(fileID, bitrate, sessionID, segID)
		}
	}

	// 更新会话心跳
	s.updateSession(sessionID, fileID, bitrate, segID)

	// 1. 检查 TS 切片是否已存在
	tsPath := fmt.Sprintf("ts/video/%s/%s/%d.ts", fileID, bitrate, segID)
	if s.storage.FileExists(tsPath) {
		s.updateAccessStats(fileID, bitrate, segID)
		s.serveSegment(c, tsPath)
		return
	}

	// 2. 检查是否正在转码
	status := s.getSegmentStatus(fileID, segID, bitrate)
	if status == "transcoding" {
		// 等待转码完成
		if err := s.waitForSegment(fileID, segID, bitrate, 10*time.Second); err == nil {
			s.serveSegment(c, tsPath)
			return
		}
	}

	// 3. 确保视频切片已存在（MKV）
	if err := s.ensureSegmentSliced(fileID, segID, sessionID); err != nil {
		c.JSON(500, gin.H{"error": "Segment not available"})
		return
	}

	// 4. 启动转码任务（二选一模式）。
	if s.isContinuousHLSMode() {
		// Continuous HLS 模式：确保 worker 在运行（覆盖当前 segment），
		// 同时入队一个快速逐段转码用于即时响应（seek 或首段）。
		if !s.isContinuousHLSRunning(fileID, bitrate) {
			s.ensureContinuousHLSStartedWithSeg(fileID, bitrate, sessionID, segID)
		}
		// 快速逐段转码：立即产出当前 segment（高优先级）。
		_ = s.startTranscodeTask(fileID, segID, bitrate, sessionID)
		// 等待转码完成。
		if err := s.waitForSegment(fileID, segID, bitrate, 45*time.Second); err != nil {
			c.JSON(504, gin.H{"error": "segment not ready", "segment_id": segID})
			return
		}
		s.serveSegment(c, tsPath)
		return
	}
	// 传统逐段转码模式。
	if err := s.startTranscodeTask(fileID, segID, bitrate, sessionID); err != nil {
		c.JSON(500, gin.H{"error": "Failed to start transcode"})
		return
	}

	const previewBitrate = "500k"

	// 5. 等待目标码率；可选先返回最低预览档（显式开启或 Seek 大跳转已标记 boost）
	if (s.allowJITSubstitute(c) || s.sessionSeekBoostActive(sessionID)) && bitrate != previewBitrate {
		if err := s.waitForSegment(fileID, segID, bitrate, 850*time.Millisecond); err == nil {
			if s.storage.FileExists(tsPath) {
				s.updateAccessStats(fileID, bitrate, segID)
				s.serveSegment(c, tsPath)
				return
			}
		}
		fbPath := fmt.Sprintf("ts/video/%s/%s/%d.ts", fileID, previewBitrate, segID)
		if s.storage.FileExists(fbPath) {
			c.Header("X-JIT-Substituted-Bitrate", previewBitrate)
			c.Header("X-JIT-Requested-Bitrate", bitrate)
			c.Header("Cache-Control", "private, no-store")
			s.updateAccessStats(fileID, previewBitrate, segID)
			s.serveSegment(c, fbPath)
			return
		}
		if !s.segmentExists(fileID, segID, previewBitrate) && s.getSegmentStatus(fileID, segID, previewBitrate) != "transcoding" {
			_ = s.startTranscodeTask(fileID, segID, previewBitrate, sessionID)
		}
		if err := s.waitForSegment(fileID, segID, previewBitrate, 2200*time.Millisecond); err == nil && s.storage.FileExists(fbPath) {
			c.Header("X-JIT-Substituted-Bitrate", previewBitrate)
			c.Header("X-JIT-Requested-Bitrate", bitrate)
			c.Header("Cache-Control", "private, no-store")
			s.updateAccessStats(fileID, previewBitrate, segID)
			s.serveSegment(c, fbPath)
			return
		}
	}

	// 首段（segID 较小或 seek 后的第一段）放宽到 60s：cold disk + ffprobe MOOV box +
	// 软编都可能让首帧延迟到几十秒。后续段已通过 prefetch 预热，等待时间通常 <5s。
	waitTimeout := 30 * time.Second
	if segID == 0 || s.sessionSeekBoostActive(sessionID) {
		waitTimeout = 60 * time.Second
	}
	if err := s.waitForSegment(fileID, segID, bitrate, waitTimeout); err != nil {
		// 把当前 segment 状态回到诊断里，方便定位（transcoding=worker 没出片；
		// failed=worker 早已报错；空=任务还没入队/被吞掉）。
		st := s.getSegmentStatus(fileID, segID, bitrate)
		s.logger.Warn("segment wait timeout",
			zap.String("file_id", fileID),
			zap.Int("segment_id", segID),
			zap.String("bitrate", bitrate),
			zap.String("status", st),
			zap.Duration("waited", waitTimeout),
		)
		c.JSON(504, gin.H{
			"error":          "Transcode timeout",
			"segment_status": st,
			"segment_id":     segID,
			"waited_seconds": int(waitTimeout.Seconds()),
		})
		return
	}

	s.serveSegment(c, tsPath)
}

func (s *Scheduler) allowJITSubstitute(c *gin.Context) bool {
	q := strings.TrimSpace(c.Query("jit_substitute"))
	if q == "1" || strings.EqualFold(q, "true") || strings.EqualFold(q, "yes") {
		return true
	}
	return strings.TrimSpace(c.GetHeader("X-JIT-Allow-Substitute")) == "1"
}

func (s *Scheduler) sessionSeekBoostActive(sessionID string) bool {
	ctx := context.Background()
	n, err := s.redis.Exists(ctx, "jit:session_seek_boost:"+sessionID).Result()
	return err == nil && n > 0
}

func (s *Scheduler) handleAudioSegment(c *gin.Context) {
	fileID := c.Param("fileId")
	lang := c.Param("lang")
	segIDStr := c.Param("segId")
	segID, err := strconv.Atoi(segIDStr)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid segment id"})
		return
	}

	data, err := s.storage.LoadSegment(fileID, segID, "audio", lang)
	if err != nil {
		c.JSON(404, gin.H{"error": "Audio segment not found"})
		return
	}

	s.updateAccessStats(fileID, "audio:"+lang, segID)
	c.Header("Cache-Control", "public, max-age=3600")
	c.Data(200, "audio/aac", data)
}

// 确保特定切片已被切分（MKV）
func (s *Scheduler) ensureSegmentSliced(fileID string, segID int, sessionID string) error {
	index, err := s.getSegmentIndex(fileID)
	if err != nil {
		return err
	}

	if segID >= len(index.VideoSegments) {
		return fmt.Errorf("segment ID out of range")
	}

	seg := index.VideoSegments[segID]
	if seg.Status == "sliced" || seg.Status == "indexed" {
		return nil
	}

	if seg.Status == "slicing" {
		return s.waitForSegmentSlice(fileID, segID, 30*time.Second)
	}

	// 启动单个切片任务
	return s.startSingleSliceTask(fileID, segID, sessionID)
}

// 启动单个切片任务（按需切片）
func (s *Scheduler) startSingleSliceTask(fileID string, segID int, sessionID string) error {
	ctx := context.Background()

	// 更新状态
	s.updateSegmentStatus(fileID, segID, "slice", "slicing")

	// 发布切片任务
	task := &models.SliceTask{
		FileID:    fileID,
		SessionID: sessionID,
		CreatedAt: time.Now().Unix(),
	}

	// 可以指定只切特定 segment
	taskData, _ := json.Marshal(task)
	return s.redis.Publish(ctx, "slice:single:"+fileID, taskData).Err()
}

// 启动转码任务
// isContinuousHLSMode reports whether continuous HLS is enabled (global config).
func (s *Scheduler) isContinuousHLSMode() bool {
	return s.hlsContinuous
}

// isContinuousHLSRunning checks if a continuous HLS transcode is active for this file+bitrate.
func (s *Scheduler) isContinuousHLSRunning(fileID, bitrate string) bool {
	key := fmt.Sprintf("continuous:hls:%s:%s", fileID, bitrate)
	n, err := s.redis.Exists(context.Background(), key).Result()
	return err == nil && n > 0
}

// restartContinuousHLS checks whether a running continuous HLS worker covers the target segment.
// If the target is within the worker's current output range, do nothing (it will produce the segment soon).
// If outside range, kill the worker and start a new one from the target segment.
func (s *Scheduler) restartContinuousHLS(fileID, bitrate, sessionID string, targetSegID int) {
	ctx := context.Background()
	key := fmt.Sprintf("continuous:hls:%s:%s", fileID, bitrate)

	// Check if a worker is already covering this range.
	startStr, _ := s.redis.HGet(ctx, key, "start_seg").Result()
	latestStr, _ := s.redis.HGet(ctx, key, "latest_seg").Result()
	startSeg, _ := strconv.Atoi(startStr)
	latestSeg, _ := strconv.Atoi(latestStr)

	// If worker exists and target is within [start, latest+lookahead], no restart needed.
	lookahead := 30 // segments ahead of latest that we consider "in range"
	if latestStr != "" && targetSegID >= startSeg && targetSegID <= latestSeg+lookahead {
		return
	}

	// Target is outside range: kill existing worker and start new one.
	s.redis.Del(ctx, key)
	lockKey := fmt.Sprintf("lock:continuous:hls:%s:%s", fileID, bitrate)
	s.redis.Del(ctx, lockKey)
	time.Sleep(200 * time.Millisecond)
	s.ensureContinuousHLSStartedWithSeg(fileID, bitrate, sessionID, targetSegID)
}

// ensureContinuousHLSStarted enqueues a continuous HLS job if none is running for this file+bitrate.
func (s *Scheduler) ensureContinuousHLSStarted(fileID, bitrate, sessionID string) {
	s.ensureContinuousHLSStartedWithSeg(fileID, bitrate, sessionID, 0)
}

// ensureContinuousHLSStartedWithSeg enqueues a continuous HLS job with a specific start segment.
func (s *Scheduler) ensureContinuousHLSStartedWithSeg(fileID, bitrate, sessionID string, startSegID int) {
	if s.isContinuousHLSRunning(fileID, bitrate) {
		return
	}
	lockKey := fmt.Sprintf("lock:continuous:hls:%s:%s", fileID, bitrate)
	locked, _ := s.redis.SetNX(context.Background(), lockKey, sessionID, 30*time.Second).Result()
	if !locked {
		return
	}
	job := map[string]interface{}{
		"file_id":      fileID,
		"bitrate":      bitrate,
		"session_id":   sessionID,
		"start_seg_id": startSegID,
	}
	data, _ := json.Marshal(job)
	s.redis.LPush(context.Background(), "continuous:jobs:queue", data)
}

func (s *Scheduler) startTranscodeTask(fileID string, segID int, bitrate, sessionID string) error {
	ctx := context.Background()

	// 获取分布式锁
	lockKey := fmt.Sprintf("lock:transcode:%s:%d:%s", fileID, segID, bitrate)
	locked, err := s.redis.SetNX(ctx, lockKey, sessionID, 120*time.Second).Result()
	if err != nil {
		return err
	}

	if !locked {
		// 其他节点正在转码
		return nil
	}
	defer s.redis.Del(ctx, lockKey)

	// 双重检查
	if s.segmentExists(fileID, segID, bitrate) {
		return nil
	}

	// 更新状态
	s.updateSegmentStatus(fileID, segID, bitrate, "transcoding")

	// 获取分辨率
	resolution := s.getResolutionForBitrate(bitrate)

	// 创建转码任务
	task := &models.TranscodeTask{
		FileID:     fileID,
		SegmentID:  segID,
		Bitrate:    bitrate,
		Resolution: resolution,
		Codec:      "",
		SessionID:  sessionID,
		Priority:   0,
		CreatedAt:  time.Now().Unix(),
	}

	taskData, _ := json.Marshal(task)

	// 根据优先级发布到不同队列
	queue := "transcode:queue:high"
	if sessionID == "prefetch" {
		queue = "transcode:queue:low"
	}

	// 使用 Sorted Set 实现优先级队列
	score := float64(time.Now().Unix())
	return s.redis.ZAdd(ctx, queue, &redis.Z{
		Score:  score,
		Member: taskData,
	}).Err()
}

// 等待切片完成（通过 video:index 中的 segment 状态判断）
func (s *Scheduler) waitForSegmentSlice(fileID string, segID int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		index, err := s.getSegmentIndex(fileID)
		if err == nil && segID >= 0 && segID < len(index.VideoSegments) {
			status := index.VideoSegments[segID].Status
			if status == "sliced" || status == "indexed" {
				return nil
			}
			if status == "failed" {
				return fmt.Errorf("slice failed")
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("slice timeout")
}

// 等待切片转码完成
func (s *Scheduler) waitForSegment(fileID string, segID int, bitrate string, timeout time.Duration) error {
	ctx := context.Background()
	deadline := time.Now().Add(timeout)

	key := fmt.Sprintf("segment:status:%s:%d:%s", fileID, segID, bitrate)

	for time.Now().Before(deadline) {
		status, err := s.redis.Get(ctx, key).Result()
		if err == nil {
			if status == "ready" {
				return nil
			}
			if status == "failed" {
				return fmt.Errorf("transcode failed")
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("timeout")
}

// ==================== 辅助方法 ====================
func (s *Scheduler) getSegmentIndex(fileID string) (*models.SegmentIndex, error) {
	ctx := context.Background()

	key := "video:index:" + fileID
	data, err := s.redis.Get(ctx, key).Result()
	if err != nil {
		return nil, err
	}

	var index models.SegmentIndex
	if err := json.Unmarshal([]byte(data), &index); err != nil {
		return nil, err
	}

	return &index, nil
}

func (s *Scheduler) getVideoMetadata(fileID string) (*models.VideoMetadata, error) {
	ctx := context.Background()

	key := "video:meta:" + fileID
	data, err := s.redis.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, err
	}

	if len(data) == 0 {
		return nil, fmt.Errorf("metadata not found")
	}

	duration, _ := strconv.ParseFloat(data["duration"], 64)
	width, _ := strconv.Atoi(data["width"])
	height, _ := strconv.Atoi(data["height"])
	size, _ := strconv.ParseInt(data["size"], 10, 64)

	return &models.VideoMetadata{
		FileID:     fileID,
		FilePath:   data["file_path"],
		Duration:   duration,
		Width:      width,
		Height:     height,
		Size:       size,
		Codec:      data["codec"],
		AudioCodec: data["audio_codec"],
		Format:     data["format"],
	}, nil
}

func (s *Scheduler) updateSegmentStatus(fileID string, segID int, bitrate string, status string) {
	ctx := context.Background()
	key := fmt.Sprintf("segment:status:%s:%d:%s", fileID, segID, bitrate)
	s.redis.Set(ctx, key, status, 5*time.Minute)
}

func (s *Scheduler) getSegmentStatus(fileID string, segID int, bitrate string) string {
	ctx := context.Background()
	key := fmt.Sprintf("segment:status:%s:%d:%s", fileID, segID, bitrate)

	status, err := s.redis.Get(ctx, key).Result()
	if err == redis.Nil || err != nil {
		return ""
	}
	return status
}

func (s *Scheduler) segmentExists(fileID string, segID int, bitrate string) bool {
	tsPath := fmt.Sprintf("ts/video/%s/%s/%d.ts", fileID, bitrate, segID)
	return s.storage.FileExists(tsPath)
}

func (s *Scheduler) updateAccessStats(fileID, bitrate string, segID int) {
	ctx := context.Background()
	key := fmt.Sprintf("segment:access:%s:%s:%d", fileID, bitrate, segID)

	s.redis.HIncrBy(ctx, key, "access_count", 1)
	s.redis.HSet(ctx, key, "last_access", time.Now().Unix())
	s.redis.Expire(ctx, key, 7*24*time.Hour)
}

func (s *Scheduler) previousSessionSegment(sessionID string) (int, bool) {
	ctx := context.Background()
	v, err := s.redis.HGet(ctx, "session:"+sessionID, "current_segment").Result()
	if err != nil || strings.TrimSpace(v) == "" {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, false
	}
	return n, true
}

func (s *Scheduler) markSessionSeekBoost(sessionID string) {
	ctx := context.Background()
	key := "jit:session_seek_boost:" + sessionID
	s.redis.Set(ctx, key, "1", 20*time.Second)
}

// dropStaleTranscodeTasksForFile 清理 transcode:queue:low 中属于该 fileID 的低优先级（prefetch）任务。
// 用户 seek 后旧位置附近的 prefetch 不再有意义，避免转码 worker 浪费在 CPU 上。
// 高优先级队列保留：用户的明确请求即使被 seek 替代也快速失败/超时即可。
func (s *Scheduler) dropStaleTranscodeTasksForFile(fileID string) {
	if s == nil || s.redis == nil || strings.TrimSpace(fileID) == "" {
		return
	}
	ctx := context.Background()
	// ZSet 成员是 task JSON；按 fileID 子串过滤后批量 ZRem。fileID 是 UUID/file id，碰撞概率极低。
	members, err := s.redis.ZRange(ctx, "transcode:queue:low", 0, -1).Result()
	if err != nil {
		return
	}
	needle := `"file_id":"` + fileID + `"`
	stale := make([]interface{}, 0, len(members))
	for _, m := range members {
		if strings.Contains(m, needle) {
			stale = append(stale, m)
		}
	}
	if len(stale) > 0 {
		s.redis.ZRem(ctx, "transcode:queue:low", stale...)
	}
}

func (s *Scheduler) updateSession(sessionID, fileID, bitrate string, segID int) {
	ctx := context.Background()
	key := "session:" + sessionID

	s.redis.HSet(ctx, key,
		"file_id", fileID,
		"bitrate", bitrate,
		"current_segment", segID,
		"last_active", time.Now().Unix(),
	)
	s.redis.Expire(ctx, key, 35*time.Second)
}

// handleJITSessionPause marks the playback session so embedded transcodeworker suspends ffmpeg (SIGSTOP on Linux, NtSuspendProcess on Windows).
func (s *Scheduler) handleJITSessionPause(c *gin.Context) {
	sessionID := strings.TrimSpace(c.GetHeader("X-Session-ID"))
	if sessionID == "" {
		c.JSON(400, gin.H{"error": "X-Session-ID required"})
		return
	}
	ctx := context.Background()
	key := "session:" + sessionID
	if n, _ := s.redis.Exists(ctx, key).Result(); n == 0 {
		c.JSON(404, gin.H{"error": "session not found"})
		return
	}
	s.redis.HSet(ctx, key, "transcode_paused", "1")
	// Long TTL while paused so segment heartbeats are not required during user pause.
	s.redis.Expire(ctx, key, jitPausedSessionTTL)
	c.JSON(200, gin.H{"ok": true})
}

func (s *Scheduler) handleJITSessionResume(c *gin.Context) {
	sessionID := strings.TrimSpace(c.GetHeader("X-Session-ID"))
	if sessionID == "" {
		c.JSON(400, gin.H{"error": "X-Session-ID required"})
		return
	}
	ctx := context.Background()
	key := "session:" + sessionID
	if n, _ := s.redis.Exists(ctx, key).Result(); n == 0 {
		c.JSON(404, gin.H{"error": "session not found"})
		return
	}
	s.redis.HSet(ctx, key, "transcode_paused", "0")
	s.redis.Expire(ctx, key, 35*time.Second)
	c.JSON(200, gin.H{"ok": true})
}

// handleJITSessionEnd 立即结束会话：删除 session:* 键，由 transcodeworker.monitorSession 检测到 isSessionAlive=false 后 SIGKILL 子进程。
// 任何排队中的 prefetch 任务也由 transcodeworker 处理时返回 aborted。
func (s *Scheduler) handleJITSessionEnd(c *gin.Context) {
	sessionID := strings.TrimSpace(c.GetHeader("X-Session-ID"))
	if sessionID == "" {
		c.JSON(400, gin.H{"error": "X-Session-ID required"})
		return
	}
	ctx := context.Background()
	key := "session:" + sessionID
	// 即使 session 不在也返回 ok（幂等），但仍清理 boost 等附属键。
	s.redis.Del(ctx, key)
	s.redis.Del(ctx, "jit:session_seek_boost:"+sessionID)
	c.JSON(200, gin.H{"ok": true})
}

// handleJITSessionSeek 显式标记一次跳转，提示 transcodeworker 切换到 ultrafast 预设并优先服务跳转目标。
// 客户端在用户拖动进度条后请求新分片前调用本接口可避免 N 段“缓冲洞”。
func (s *Scheduler) handleJITSessionSeek(c *gin.Context) {
	sessionID := strings.TrimSpace(c.GetHeader("X-Session-ID"))
	if sessionID == "" {
		c.JSON(400, gin.H{"error": "X-Session-ID required"})
		return
	}
	ctx := context.Background()
	key := "session:" + sessionID
	if n, _ := s.redis.Exists(ctx, key).Result(); n == 0 {
		c.JSON(404, gin.H{"error": "session not found"})
		return
	}
	s.markSessionSeekBoost(sessionID)
	// resume 暂停状态以便用户拖动后继续转码
	s.redis.HSet(ctx, key, "transcode_paused", "0")
	s.redis.Expire(ctx, key, 35*time.Second)
	c.JSON(200, gin.H{"ok": true})
}

func (s *Scheduler) getOrCreateSessionID(c *gin.Context) string {
	sessionID := c.GetHeader("X-Session-ID")
	if sessionID == "" {
		sessionID = c.ClientIP() + "-" + c.Request.UserAgent() + "-" +
			time.Now().Format("20060102150405")
	}
	return sessionID
}

func (s *Scheduler) serveSegment(c *gin.Context, path string) {
	if !filepath.IsAbs(path) {
		if withBase, ok := s.storage.(interface{ BasePath() string }); ok {
			path = filepath.Join(withBase.BasePath(), path)
		}
	}
	c.File(path)
}

func (s *Scheduler) generateMasterPlaylist(c *gin.Context, fileID string, meta *models.VideoMetadata) string {
	// 客户端期望最大高度，参数与 /api/v1/media/{id}/hls 共享 max_height
	maxClientHeight := 0
	if v := strings.TrimSpace(c.Query("max_height")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			maxClientHeight = n
		}
	}

	srcHeight := 0
	if meta != nil {
		srcHeight = meta.Height
	}
	// 单清晰度策略：根据源高度、客户端能力、机器实时 CPU/GPU 负载选定唯一档位。
	picked := profile.Pick(c.Request.Context(), s.redis, srcHeight, maxClientHeight)

	// Check if pre-extracted audio HLS is available (separated audio tracks).
	// Only emit separate audio groups when config allows it.
	audioTracks := meta.AudioPlaylists
	hasAudio := len(audioTracks) > 0 && s.hlsMultiAudioEnabled

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:6\n")
	b.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n\n")

	if hasAudio {
		// Emit one EXT-X-MEDIA per audio track. First track is DEFAULT=YES.
		for i, at := range audioTracks {
			if strings.TrimSpace(at.URL) == "" {
				continue
			}
			name := at.Language
			if name == "" || name == "und" {
				name = "Audio"
			}
			if len(audioTracks) > 1 {
				name = fmt.Sprintf("%s (%s)", name, at.Language)
			}
			defaultFlag := ""
			if i == 0 {
				defaultFlag = ",DEFAULT=YES"
			}
			b.WriteString(fmt.Sprintf(
				"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio\",NAME=\"%s\",LANGUAGE=\"%s\"%s,AUTOSELECT=YES,URI=\"%s\"\n",
				name, at.Language, defaultFlag,
				jitURLWithAccessToken(c, at.URL),
			))
		}
		b.WriteString("\n")
	}

	bw := picked.Bandwidth
	if bw <= 0 {
		bw = formatBitrate(picked.Bitrate)
	}
	codecs := "avc1.640028,mp4a.40.2"
	if hasAudio {
		// When external audio is used, the video variant contains no audio stream.
		codecs = "avc1.640028"
	}
	streamInf := fmt.Sprintf(
		"#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=%dx%d,CODECS=\"%s\"",
		bw, picked.Width, picked.Height, codecs,
	)
	if hasAudio {
		streamInf += ",AUDIO=\"audio\""
	}
	b.WriteString(streamInf + "\n")
	videoURI := jitURLWithAccessToken(c, fmt.Sprintf("/api/v1/jit/playlist/%s/video/%s", fileID, picked.Bitrate))
	b.WriteString(videoURI + "\n")

	return b.String()
}

func (s *Scheduler) getResolutionForBitrate(bitrate string) string {
	resolutions := map[string]string{
		"8000k": "3840x2160",
		"4000k": "1920x1080",
		"2000k": "1280x720",
		"1000k": "854x480",
		"500k":  "640x360",
	}
	if res, ok := resolutions[bitrate]; ok {
		return res
	}
	return "1280x720"
}
