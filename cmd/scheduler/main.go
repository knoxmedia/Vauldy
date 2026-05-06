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
	models "knox-media/internal/model"
)

// jitPausedSessionTTL is Redis TTL for session:* while transcode_paused=1 (pause without segment heartbeats).
const jitPausedSessionTTL = 2 * time.Hour

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
	redis     *redis.Client
	storage   Storage
	logger    *zap.Logger
	mu        sync.RWMutex
	sliceLock map[string]*sync.Mutex
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

func (s *Scheduler) RegisterRoutes(r gin.IRouter) {
	r.GET("/jit/master/:fileId", s.handleMasterPlaylist)
	r.GET("/jit/playlist/:fileId/video/:bitrate", s.handleVideoPlaylist)
	r.GET("/jit/playlist/:fileId/audio/:lang", s.handleAudioPlaylist)
	r.GET("/jit/segment/:fileId/video/:bitrate/:segId", s.handleVideoSegment)
	r.GET("/jit/segment/:fileId/audio/:lang/:segId", s.handleAudioSegment)
	r.POST("/jit/session/pause", s.handleJITSessionPause)
	r.POST("/jit/session/resume", s.handleJITSessionResume)
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

func (s *Scheduler) TriggerSlicing(fileID, sessionID string) error {
	return s.ensureVideoSliced(fileID, sessionID)
}

// ==================== Master Playlist ====================
func (s *Scheduler) handleMasterPlaylist(c *gin.Context) {
	fileID := c.Param("fileId")
	sessionID := s.getOrCreateSessionID(c)

	// 1. 确保视频已被切片（触发切片任务）
	if err := s.ensureVideoSliced(fileID, sessionID); err != nil {
		ctx := context.Background()
		metaKey := "video:meta:" + fileID
		meta, _ := s.redis.HGetAll(ctx, metaKey).Result()
		sliceErr := strings.TrimSpace(meta["slice_error"])
		filePath := strings.TrimSpace(meta["file_path"])
		metaStatus := strings.TrimSpace(meta["status"])

		retryKey := "retry:slice:" + fileID
		retryN, rerr := s.redis.Get(ctx, retryKey).Int64()
		switch rerr {
		case redis.Nil:
			retryN = 0
		case nil:
			// ok
		default:
			retryN = -1
		}

		logFields := []zap.Field{
			zap.Error(err),
			zap.String("file_id", fileID),
			zap.String("meta_status", metaStatus),
		}
		if retryN >= 0 {
			logFields = append(logFields, zap.Int64("slice_retry_count", retryN))
		}
		if filePath != "" {
			logFields = append(logFields, zap.String("file_path", filePath))
		}
		if sliceErr != "" {
			logFields = append(logFields, zap.String("slice_error", sliceErr))
		}
		s.logger.Error("Failed to ensure video sliced", logFields...)

		body := gin.H{"error": "Video processing failed: " + err.Error()}
		if sliceErr != "" {
			body["slice_error"] = sliceErr
		}
		if filePath != "" {
			body["file_path"] = filePath
		}
		if metaStatus != "" {
			body["meta_status"] = metaStatus
		}
		if retryN >= 0 {
			body["slice_retry_count"] = retryN
		}
		if sliceErr == "" {
			body["hint"] = "slice_error is empty in Redis. Verify file_path exists and ffprobe works; DEL " + retryKey + " to reset the retry counter after fixing the issue."
		}
		c.JSON(500, body)
		return
	}

	// 2. 获取视频元数据
	metadata, err := s.getVideoMetadata(fileID)
	if err != nil {
		c.JSON(404, gin.H{"error": "Video not found"})
		return
	}

	// 3. 生成 Master Playlist（子播放列表/分片 URL 需带 access_token，否则 hls.js 请求会 401）
	content := s.generateMasterPlaylist(c, fileID, metadata)

	c.Header("Content-Type", "application/vnd.apple.mpegurl")
	c.String(200, content)
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
		// 等待切片完成 (longer timeout for large files).
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

	// 更新状态为 slicing（新一次尝试，清掉上次的错误文案）
	_ = s.redis.HDel(ctx, "video:meta:"+fileID, "slice_error").Err()
	s.redis.HSet(ctx, "video:meta:"+fileID, "status", "slicing")

	// 发布切片任务到队列
	task := &models.SliceTask{
		FileID:    fileID,
		SessionID: sessionID,
		CreatedAt: time.Now().Unix(),
	}

	taskData, _ := json.Marshal(task)
	if err := s.redis.Publish(ctx, "slice:jobs", taskData).Err(); err != nil {
		s.redis.HSet(ctx, "video:meta:"+fileID, "status", "failed", "slice_error", trimSliceMetaErr("redis publish slice:jobs: "+err.Error()))
		return err
	}

	s.logger.Info("Slicing task published", zap.String("file_id", fileID))

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
		}
		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("slicing timeout")
}

// ==================== Video Playlist ====================
func (s *Scheduler) handleVideoPlaylist(c *gin.Context) {
	fileID := c.Param("fileId")
	bitrate := c.Param("bitrate")

	// 获取切片索引
	index, err := s.getSegmentIndex(fileID)
	if err != nil {
		c.JSON(404, gin.H{"error": "Video not ready"})
		return
	}

	// 生成 Playlist
	content := "#EXTM3U\n"
	content += "#EXT-X-VERSION:3\n"
	content += "#EXT-X-TARGETDURATION:10\n"
	content += "#EXT-X-PLAYLIST-TYPE:VOD\n"
	content += "#EXT-X-MEDIA-SEQUENCE:0\n\n"

	for _, seg := range index.VideoSegments {
		content += fmt.Sprintf("#EXTINF:%.3f,\n", seg.Duration)
		segPath := fmt.Sprintf("/api/v1/jit/segment/%s/video/%s/%d", fileID, bitrate, seg.ID)
		content += jitURLWithAccessToken(c, segPath) + "\n"
	}

	content += "#EXT-X-ENDLIST\n"

	// 缓存 Playlist（1小时）
	c.Header("Cache-Control", "public, max-age=3600")
	c.Header("Content-Type", "application/vnd.apple.mpegurl")
	c.String(200, content)
}

// ==================== Audio Playlist ====================
func (s *Scheduler) handleAudioPlaylist(c *gin.Context) {
	fileID := c.Param("fileId")
	lang := c.Param("lang")

	index, err := s.getSegmentIndex(fileID)
	if err != nil {
		c.JSON(404, gin.H{"error": "Video not ready"})
		return
	}

	content := "#EXTM3U\n"
	content += "#EXT-X-VERSION:3\n"
	content += "#EXT-X-TARGETDURATION:10\n"
	content += "#EXT-X-PLAYLIST-TYPE:VOD\n"
	content += "#EXT-X-MEDIA-SEQUENCE:0\n\n"

	segmentCount := 0
	for _, seg := range index.AudioSegments {
		if seg.Language != "" && seg.Language != lang {
			continue
		}
		content += fmt.Sprintf("#EXTINF:%.3f,\n", seg.Duration)
		segPath := fmt.Sprintf("/api/v1/jit/segment/%s/audio/%s/%d", fileID, lang, seg.ID)
		content += jitURLWithAccessToken(c, segPath) + "\n"
		segmentCount++
	}

	if segmentCount == 0 {
		c.JSON(404, gin.H{"error": "Audio track not found"})
		return
	}

	content += "#EXT-X-ENDLIST\n"

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
		if d >= 4 {
			s.markSessionSeekBoost(sessionID)
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

	// 4. 启动转码任务
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

	if err := s.waitForSegment(fileID, segID, bitrate, 15*time.Second); err != nil {
		c.JSON(504, gin.H{"error": "Transcode timeout"})
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

func (s *Scheduler) generateMasterPlaylist(c *gin.Context, fileID string, _ *models.VideoMetadata) string {
	audioURI := jitURLWithAccessToken(c, "/api/v1/jit/playlist/"+fileID+"/audio/eng")

	content := "#EXTM3U\n"
	content += "#EXT-X-VERSION:4\n\n"

	// 音频流（URI 需带 token，否则 hls.js 拉子列表 401）
	content += "#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio\",LANGUAGE=\"eng\",NAME=\"English\",DEFAULT=YES,URI=\"" + audioURI + "\"\n\n"

	// 视频流（多码率）；CODECS 含 AAC 以匹配 AUDIO 组，避免部分 HLS 解析器拒绝 master
	bitrates := []struct {
		name       string
		bandwidth  int
		resolution string
	}{
		{"8000k", 8000000, "3840x2160"},
		{"4000k", 4000000, "1920x1080"},
		{"2000k", 2000000, "1280x720"},
		{"1000k", 1000000, "854x480"},
		{"500k", 500000, "640x360"},
	}

	for _, br := range bitrates {
		content += fmt.Sprintf("#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=%s,CODECS=\"avc1.640028,mp4a.40.2\",AUDIO=\"audio\"\n",
			br.bandwidth, br.resolution)
		videoURI := jitURLWithAccessToken(c, fmt.Sprintf("/api/v1/jit/playlist/%s/video/%s", fileID, br.name))
		content += videoURI + "\n\n"
	}

	return content
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
