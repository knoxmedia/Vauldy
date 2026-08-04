// cmd/sliceworker/main.go
package sliceworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"knox-media/internal/jit/preheat"
	models "knox-media/internal/model"

	"github.com/go-redis/redis/v8"
	"go.uber.org/zap"
)

type Storage interface {
	BasePath() string
}

type LocalStorage struct {
	basePath string
}

type Config struct {
	RedisAddr   string
	StoragePath string
	FFmpegPath  string
	FFprobePath string
	WorkerID    string
	// NoPreheat disables per-segment preheat enqueuing (use when continuous HLS handles all segments).
	NoPreheat bool
}

func NewStorage(basePath string) Storage {
	return &LocalStorage{basePath: basePath}
}

func (s *LocalStorage) BasePath() string {
	return s.basePath
}

type SliceWorker struct {
	redis     *redis.Client
	storage   Storage
	ffmpeg    string
	ffprobe   string
	logger    *zap.Logger
	workerID  string
	noPreheat bool
}

type VideoInfo struct {
	Duration   float64
	Size       int64
	Width      int
	Height     int
	VideoCodec string
	AudioCodec string
	Bitrate    int
}

func NewSliceWorker(cfg *Config) *SliceWorker {
	ffprobe := strings.TrimSpace(cfg.FFprobePath)
	if ffprobe == "" {
		ffprobe = "ffprobe"
	}
	w := &SliceWorker{
		redis:     redis.NewClient(&redis.Options{Addr: cfg.RedisAddr}),
		storage:   NewStorage(cfg.StoragePath),
		ffmpeg:    cfg.FFmpegPath,
		ffprobe:   ffprobe,
		logger:    zap.L(),
		workerID:  cfg.WorkerID,
		noPreheat: cfg.NoPreheat,
	}
	w.warnIfProbeLooksLikeFFmpeg()
	return w
}

// toolBinDir returns the directory containing the executable so Windows loads sibling DLLs reliably.
func toolBinDir(toolPath string) string {
	toolPath = strings.TrimSpace(toolPath)
	if toolPath == "" {
		return ""
	}
	p := toolPath
	if !filepath.IsAbs(p) {
		if lp, err := exec.LookPath(filepath.Base(p)); err == nil {
			p = lp
		} else if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
	}
	d := filepath.Dir(p)
	if d == "." || d == "" || strings.EqualFold(d, toolPath) {
		return ""
	}
	return d
}

func (w *SliceWorker) ffprobeCommand(args ...string) *exec.Cmd {
	cmd := exec.Command(w.ffprobe, args...)
	if dir := toolBinDir(w.ffprobe); dir != "" {
		cmd.Dir = dir
	}
	return cmd
}

func (w *SliceWorker) ffmpegCommand(args ...string) *exec.Cmd {
	cmd := exec.Command(w.ffmpeg, args...)
	if dir := toolBinDir(w.ffmpeg); dir != "" {
		cmd.Dir = dir
	}
	return cmd
}

func (w *SliceWorker) warnIfProbeLooksLikeFFmpeg() {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(w.ffprobe)))
	if base == "" {
		return
	}
	if strings.Contains(base, "ffmpeg") && !strings.Contains(base, "ffprobe") {
		w.logger.Warn("FFprobePath appears to be ffmpeg, not ffprobe; use ffprobe_path in config",
			zap.String("path", w.ffprobe))
	}
}

func (w *SliceWorker) selfCheckFFprobe() error {
	cmd := w.ffprobeCommand("-hide_banner", "-version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s -version: %w: %s", w.ffprobe, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func formatToolExit(tool string, input string, err error, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	tool = strings.TrimSpace(tool)
	if tool == "" {
		tool = "tool"
	} else {
		tool = filepath.Base(tool)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code := ee.ExitCode()
		u := uint32(code)
		if stderr != "" {
			return fmt.Errorf("%s %q: exit code %d (0x%X): %s", tool, input, code, u, stderr)
		}
		return fmt.Errorf("%s %q: exit code %d (0x%X)", tool, input, code, u)
	}
	if stderr != "" {
		return fmt.Errorf("%s %q: %v: %s", tool, input, err, stderr)
	}
	return fmt.Errorf("%s %q: %w", tool, input, err)
}

// SliceQueueKey is the durable Redis LIST used to queue slice tasks. Keep RPUSH-LPOP semantics
// so messages survive worker restarts (unlike pubsub, which silently drops messages when no
// subscriber is connected at publish time — root cause of "video:meta status stuck on slicing").
const SliceQueueKey = "slice:jobs:queue"

// SliceQueueLegacyChannel is kept for backwards compatibility: external pubsub subscribers can
// still observe job arrivals, but the worker no longer relies on this channel.
const SliceQueueLegacyChannel = "slice:jobs"

func (w *SliceWorker) Start() {
	ctx := context.Background()

	// Pubsub is kept open only as a fallback (older clients may publish on this channel).
	pubsub := w.redis.Subscribe(ctx, SliceQueueLegacyChannel)
	defer pubsub.Close()

	if err := w.selfCheckFFprobe(); err != nil {
		w.logger.Error("ffprobe self-check failed; fix ffmpeg.ffprobe_path or install VC++ runtime for bundled tools",
			zap.Error(err), zap.String("ffprobe", w.ffprobe), zap.String("worker_id", w.workerID))
	} else {
		w.logger.Info("ffprobe self-check ok", zap.String("ffprobe", w.ffprobe), zap.String("worker_id", w.workerID))
	}

	w.logger.Info("Slice worker started",
		zap.String("worker_id", w.workerID),
		zap.String("queue_key", SliceQueueKey),
	)

	pubsubCh := pubsub.Channel()

	go w.recoverStaleSlicing(ctx)

	for {
		// Block on the durable queue first; pubsub is read non-blocking via select with a
		// short timeout so we never miss a queued job because we were waiting on pubsub.
		select {
		case msg := <-pubsubCh:
			if msg == nil {
				continue
			}
			var task models.SliceTask
			if err := json.Unmarshal([]byte(msg.Payload), &task); err != nil {
				w.logger.Error("Failed to parse pubsub task", zap.Error(err))
				continue
			}
			w.processSliceTask(&task)
		default:
		}

		// BLPOP with 5s timeout: balances latency vs cost when the queue is empty.
		res, err := w.redis.BLPop(ctx, 5*time.Second, SliceQueueKey).Result()
		if err == redis.Nil {
			continue
		}
		if err != nil {
			w.logger.Warn("BLPop slice queue failed; retrying after 1s", zap.Error(err))
			time.Sleep(time.Second)
			continue
		}
		if len(res) < 2 {
			continue
		}
		var task models.SliceTask
		if err := json.Unmarshal([]byte(res[1]), &task); err != nil {
			w.logger.Error("Failed to parse queued task", zap.Error(err))
			continue
		}
		w.processSliceTask(&task)
	}
}

// recoverStaleSlicing periodically scans video:meta:* hashes whose status=slicing has gone
// stale (no heartbeat for >SliceStaleAfter). On hit it resets status to "" and re-enqueues
// the task, so a request that arrived during a worker outage will eventually succeed.
//
// 这是修复 "video:meta status 一直是 slicing" 卡死的关键：即便切片任务因为 pubsub 丢失或
// worker 重启而被遗忘，也能在 30s 内自愈。
func (w *SliceWorker) recoverStaleSlicing(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		w.scanAndRecoverOnce(ctx)
	}
}

// SliceStaleAfter is how long we tolerate status=slicing without a heartbeat before
// considering the job lost. Long enough to cover legitimate pauses while ffprobe runs
// (a few seconds even on multi-GB sources), short enough that users see playback start
// within ~1 minute on a worst-case stuck queue.
const SliceStaleAfter = 60 * time.Second

func (w *SliceWorker) scanAndRecoverOnce(ctx context.Context) {
	now := time.Now().Unix()
	iter := w.redis.Scan(ctx, 0, "video:meta:*", 256).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		got, err := w.redis.HMGet(ctx, key, "status", "slicing_started_at", "slicing_heartbeat_at", "file_path").Result()
		if err != nil || len(got) < 4 {
			continue
		}
		status, _ := got[0].(string)
		if strings.TrimSpace(status) != "slicing" {
			continue
		}
		startedAt := parseInt(got[1])
		heartbeat := parseInt(got[2])
		latest := heartbeat
		if startedAt > latest {
			latest = startedAt
		}
		if latest > 0 && now-latest < int64(SliceStaleAfter.Seconds()) {
			continue
		}
		filePath, _ := got[3].(string)
		filePath = strings.TrimSpace(filePath)
		if filePath == "" {
			continue
		}
		fileID := strings.TrimPrefix(key, "video:meta:")
		w.logger.Warn("Recovering stale slicing task; re-enqueueing",
			zap.String("file_id", fileID),
			zap.Int64("seconds_since_heartbeat", now-latest),
		)
		// Wipe heartbeat fields and re-publish to the durable queue.
		_ = w.redis.HDel(ctx, key, "status", "slice_error").Err()
		_ = w.redis.Del(ctx, "lock:slice:"+fileID).Err()
		task := models.SliceTask{
			FileID:    fileID,
			SessionID: "auto-recover",
			CreatedAt: now,
		}
		raw, err := json.Marshal(task)
		if err != nil {
			continue
		}
		_ = w.redis.RPush(ctx, SliceQueueKey, raw).Err()
	}
}

func parseInt(v interface{}) int64 {
	s, _ := v.(string)
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func (w *SliceWorker) processSliceTask(task *models.SliceTask) {
	logger := w.logger.With(zap.String("file_id", task.FileID))

	// 防重复：如果已经完成切片，直接跳过。
	ctx := context.Background()
	status, _ := w.redis.HGet(ctx, "video:meta:"+task.FileID, "status").Result()
	if status == "ready" {
		logger.Debug("Slice already ready, skipping")
		return
	}

	logger.Info("Processing slice task")

	startTime := time.Now()
	w.beatSlicing(task.FileID, startTime.Unix(), true)

	videoPath, err := w.getVideoPath(task.FileID)
	if err != nil {
		logger.Error("Failed to get video path", zap.Error(err))
		w.markSliceFailed(task.FileID, err.Error())
		return
	}
	videoPath = strings.TrimSpace(videoPath)
	if videoPath != "" {
		videoPath = filepath.Clean(videoPath)
	}

	w.beatSlicing(task.FileID, time.Now().Unix(), false)

	// 1. 元数据（流信息、时长）—— ffprobe -show_streams/-show_format，秒级返回。
	videoInfo, err := w.analyzeVideoFast(videoPath)
	if err != nil {
		logger.Error("Failed to analyze video", zap.Error(err))
		w.markSliceFailed(task.FileID, err.Error())
		return
	}
	w.beatSlicing(task.FileID, time.Now().Unix(), false)

	// 2. 生成视频分段索引（固定 6s 网格，不再对齐源片关键帧）
	index, err := w.generateSegmentIndex(task.FileID, videoInfo)
	if err != nil {
		logger.Error("Failed to generate segment index", zap.Error(err))
		w.markSliceFailed(task.FileID, err.Error())
		return
	}

	// 4. 立即落库 + 标记 ready，让 master.m3u8 第一时间返回
	if err := w.saveIndex(task.FileID, index); err != nil {
		logger.Error("Failed to save index", zap.Error(err))
		w.markSliceFailed(task.FileID, err.Error())
		return
	}
	w.updateVideoMetadata(task.FileID, videoInfo)

	logger.Info("Slice index ready",
		zap.Duration("duration", time.Since(startTime)),
		zap.Int("video_segments", len(index.VideoSegments)),
	)

	// 3. 预取后续切片（continuous HLS 模式下跳过，由 long-running ffmpeg 处理）。
	if !w.noPreheat {
		if err := preheat.EnqueueInitialSegments(context.Background(), w.redis, task.FileID, len(index.VideoSegments), "2000k"); err != nil {
			logger.Warn("JIT preheat enqueue failed", zap.Error(err))
		}
	}
}

// analyzeVideoFast 只执行 -show_format/-show_streams，不解码任何视频帧；秒级返回。
// 关键帧 PTS 由调用方决定是否后台异步提取并缓存。
func (w *SliceWorker) analyzeVideoFast(videoPath string) (*VideoInfo, error) {
	cmd := w.ffprobeCommand(
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		"-i", videoPath,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, formatToolExit(w.ffprobe, videoPath, err, string(output))
	}

	var probe struct {
		Format struct {
			Duration string `json:"duration"`
			Size     string `json:"size"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			CodecName string `json:"codec_name"`
			Bitrate   string `json:"bitrate"`
		} `json:"streams"`
	}

	if err := json.Unmarshal(output, &probe); err != nil {
		return nil, err
	}

	duration, _ := strconv.ParseFloat(probe.Format.Duration, 64)
	size, _ := strconv.ParseInt(probe.Format.Size, 10, 64)

	info := &VideoInfo{
		Duration: duration,
		Size:     size,
	}

	for _, stream := range probe.Streams {
		if stream.CodecType == "video" {
			info.Width = stream.Width
			info.Height = stream.Height
			info.VideoCodec = stream.CodecName
			bitrate, _ := strconv.Atoi(stream.Bitrate)
			info.Bitrate = bitrate
		} else if stream.CodecType == "audio" {
			info.AudioCodec = stream.CodecName
		}
	}

	return info, nil
}

func (w *SliceWorker) generateSegmentIndex(fileID string, info *VideoInfo) (*models.SegmentIndex, error) {
	index := &models.SegmentIndex{
		FileID:    fileID,
		Status:    "slicing",
		Duration:  info.Duration,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	// 根据固定时长生成视频切片（不再对齐源片关键帧）。
	// 原因：转码（re-encode）时编码器使用自己的 GOP 节奏（-g 48 = ~2s @24fps），
	// 源片的关键帧位置对转码输出的 TS 分片无意义。对齐源关键帧反而导致：
	//   - 密集 GOP 源片 → 大量 1s 短段 → SourceBuffer 溢出
	//   - 段边界与编码器输出 GOP 不一致 → 播放器首帧解码失败 / glitch
	// 固定 6s 网格保证每段内至少 3 个 GOP（2s × 3 = 6s），播放器可平滑解码。
	// Passthrough（-c:v copy）模式不受影响：sliceworker 不知道最终是 copy 还是 re-encode，
	// 但 transcodeworker passthrough 时会用 -ss/-t 精确截取，关键帧对齐由 ffmpeg 保证。
	segmentDuration := 3.0
	currentTime := 0.0
	segID := 0

	for currentTime < info.Duration {
		endTime := currentTime + segmentDuration
		if endTime > info.Duration {
			endTime = info.Duration
		}
		duration := endTime - currentTime

		// Merge tiny trailing segment into the previous one.
		if duration < 2.0 && endTime >= info.Duration-2.0 {
			n := len(index.VideoSegments)
			if n > 0 {
				index.VideoSegments[n-1].EndTime = endTime
				index.VideoSegments[n-1].Duration = endTime - index.VideoSegments[n-1].StartTime
			}
			break
		}

		index.VideoSegments = append(index.VideoSegments, models.VideoSegmentInfo{
			ID:        segID,
			StartTime: currentTime,
			EndTime:   endTime,
			Duration:  duration,
			Keyframe:  true,
			SlicePath: "",
			Status:    "indexed",
		})

		currentTime = endTime
		segID++
	}

	// 音频不再单独物理切片：transcodeworker 直接把音频与视频混流到同一段 .ts 中（与 Jellyfin/Emby 一致），
	// 避免长片 sliceAudio 阶段长时间占用 ffmpeg 拖慢首播。索引仍记录虚拟音频段，便于后续 audio-only 输出。
	if strings.TrimSpace(info.AudioCodec) != "" {
		// 当视频段已对齐关键帧时复用其时间轴，确保音视频在同一时刻分段。
		for _, vs := range index.VideoSegments {
			index.AudioSegments = append(index.AudioSegments, models.AudioSegmentInfo{
				ID:        vs.ID,
				StartTime: vs.StartTime,
				EndTime:   vs.EndTime,
				Duration:  vs.Duration,
				Overlap:   0,
				Language:  "und",
				SlicePath: "",
				Status:    "indexed",
			})
		}
	}

	index.TotalSegments = len(index.VideoSegments)

	return index, nil
}

func stderrTail(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	if max <= 0 || len(s) <= max {
		return s
	}
	const pref = "…(stderr tail)\n"
	if max <= len(pref) {
		return s[len(s)-max:]
	}
	return pref + s[len(s)-(max-len(pref)):]
}

func (w *SliceWorker) saveIndex(fileID string, index *models.SegmentIndex) error {
	ctx := context.Background()

	index.Status = "ready"
	index.UpdatedAt = time.Now()

	data, err := json.Marshal(index)
	if err != nil {
		return err
	}

	key := "video:index:" + fileID
	if err := w.redis.Set(ctx, key, data, 0).Err(); err != nil {
		return err
	}

	// 更新状态，清除重试计数器与上次失败原因
	w.redis.HSet(ctx, "video:meta:"+fileID, "status", "ready")
	w.redis.HDel(ctx, "video:meta:"+fileID, "slice_error")
	w.redis.Del(ctx, "retry:slice:"+fileID)

	return nil
}

func (w *SliceWorker) updateVideoMetadata(fileID string, info *VideoInfo) {
	ctx := context.Background()
	key := "video:meta:" + fileID

	w.redis.HSet(ctx, key,
		"duration", info.Duration,
		"width", info.Width,
		"height", info.Height,
		"size", info.Size,
		"codec", info.VideoCodec,
		"audio_codec", info.AudioCodec,
		"bitrate", info.Bitrate,
		"status", "ready",
	)
	w.redis.HDel(ctx, key, "slice_error")
	w.redis.Del(ctx, "retry:slice:"+fileID)
}

const maxSliceErrStored = 2000

func trimSliceErrDetail(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	if len(s) <= maxSliceErrStored {
		return s
	}
	const pref = "…"
	return pref + s[len(s)-(maxSliceErrStored-len(pref)):]
}

func (w *SliceWorker) markSliceFailed(fileID string, reason string) {
	ctx := context.Background()
	reason = trimSliceErrDetail(reason)
	if reason != "" {
		w.redis.HSet(ctx, "video:meta:"+fileID, "status", "failed", "slice_error", reason)
		return
	}
	w.redis.HSet(ctx, "video:meta:"+fileID, "status", "failed")
}

func (w *SliceWorker) getVideoPath(fileID string) (string, error) {
	ctx := context.Background()
	path, err := w.redis.HGet(ctx, "video:meta:"+fileID, "file_path").Result()
	if err != nil {
		return "", err
	}
	return path, nil
}

// beatSlicing 写入 slicing_started_at + slicing_heartbeat_at，让 recoverStaleSlicing
// 与 scheduler.waitForSlicingComplete 知道任务还活着。withStartedAt=true 时同时刷新起点，
// 用于任务首次接管。
func (w *SliceWorker) beatSlicing(fileID string, ts int64, withStartedAt bool) {
	if w == nil || w.redis == nil || strings.TrimSpace(fileID) == "" {
		return
	}
	ctx := context.Background()
	args := []interface{}{
		"slicing_heartbeat_at", ts,
		"slicing_worker_id", w.workerID,
	}
	if withStartedAt {
		args = append(args, "status", "slicing", "slicing_started_at", ts)
	}
	_ = w.redis.HSet(ctx, "video:meta:"+fileID, args...).Err()
}
