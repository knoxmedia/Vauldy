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

	"github.com/go-redis/redis/v8"
	"go.uber.org/zap"
	"knox-media/internal/jit/keyframes"
	"knox-media/internal/jit/preheat"
	models "knox-media/internal/model"
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
	// FFprobePath used for analyzeVideo / keyframes; must be ffprobe, not ffmpeg.
	FFprobePath string
	WorkerID    string
	// KeyframesCacheDir 持久化关键帧 PTS 列表，避免每次播放重复扫描。
	// 留空则缓存禁用（测试场景）。
	KeyframesCacheDir string
}

func NewStorage(basePath string) Storage {
	return &LocalStorage{basePath: basePath}
}

func (s *LocalStorage) BasePath() string {
	return s.basePath
}

type SliceWorker struct {
	redis    *redis.Client
	storage  Storage
	ffmpeg   string
	ffprobe  string
	logger   *zap.Logger
	workerID string
	kfCache  *keyframes.Cache
}

type VideoInfo struct {
	Duration   float64
	Size       int64
	Width      int
	Height     int
	VideoCodec string
	AudioCodec string
	Bitrate    int
	Keyframes  []float64
}

func NewSliceWorker(cfg *Config) *SliceWorker {
	ffprobe := strings.TrimSpace(cfg.FFprobePath)
	if ffprobe == "" {
		ffprobe = "ffprobe"
	}
	w := &SliceWorker{
		redis:    redis.NewClient(&redis.Options{Addr: cfg.RedisAddr}),
		storage:  NewStorage(cfg.StoragePath),
		ffmpeg:   cfg.FFmpegPath,
		ffprobe:  ffprobe,
		logger:   zap.L(),
		workerID: cfg.WorkerID,
	}
	if dir := strings.TrimSpace(cfg.KeyframesCacheDir); dir != "" {
		if c, err := keyframes.NewCache(dir, ffprobe); err == nil {
			w.kfCache = c
		} else {
			zap.L().Warn("keyframes cache disabled", zap.Error(err))
		}
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

func (w *SliceWorker) Start() {
	ctx := context.Background()

	// 订阅切片任务
	pubsub := w.redis.Subscribe(ctx, "slice:jobs")
	defer pubsub.Close()

	if err := w.selfCheckFFprobe(); err != nil {
		w.logger.Error("ffprobe self-check failed; fix ffmpeg.ffprobe_path or install VC++ runtime for bundled tools",
			zap.Error(err), zap.String("ffprobe", w.ffprobe), zap.String("worker_id", w.workerID))
	} else {
		w.logger.Info("ffprobe self-check ok", zap.String("ffprobe", w.ffprobe), zap.String("worker_id", w.workerID))
	}

	w.logger.Info("Slice worker started", zap.String("worker_id", w.workerID))

	for {
		select {
		case msg := <-pubsub.Channel():
			var task models.SliceTask
			if err := json.Unmarshal([]byte(msg.Payload), &task); err != nil {
				w.logger.Error("Failed to parse task", zap.Error(err))
				continue
			}

			w.processSliceTask(&task)
		}
	}
}

func (w *SliceWorker) processSliceTask(task *models.SliceTask) {
	logger := w.logger.With(zap.String("file_id", task.FileID))
	logger.Info("Processing slice task")

	startTime := time.Now()

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

	// 1. 元数据（流信息、时长）—— ffprobe -show_streams/-show_format，秒级返回。
	videoInfo, err := w.analyzeVideoFast(videoPath)
	if err != nil {
		logger.Error("Failed to analyze video", zap.Error(err))
		w.markSliceFailed(task.FileID, err.Error())
		return
	}

	// 2. 关键帧：优先读盘缓存（命中即 µs 级）；缓存未命中则使用 6s 等距网格，先把 master.m3u8 顶起来，
	//    后台异步精化关键帧索引（show_packets，仅 demux）。这是首播延迟从“分钟”降到“秒”的关键。
	if w.kfCache != nil {
		if got, err := w.kfCache.Load(task.FileID, videoPath); err == nil && got != nil && len(got.PTS) > 0 {
			videoInfo.Keyframes = got.PTS
			logger.Info("Keyframes loaded from cache", zap.Int("count", len(got.PTS)))
		}
	}

	// 3. 生成视频分段索引（音频不再单独物理切片，将由 transcodeworker 与视频一起混流到 TS）
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
		zap.Bool("keyframes_cached", len(videoInfo.Keyframes) > 0),
	)

	// 5. 当只触发了一个分片时入预热队列；prefetch 仍按 segment id 顺序。
	if err := preheat.EnqueueInitialSegments(context.Background(), w.redis, task.FileID, len(index.VideoSegments)); err != nil {
		logger.Warn("JIT preheat enqueue failed", zap.Error(err))
	}

	// 6. 后台精化：缓存未命中的话，跑 show_packets 把真实关键帧 PTS 写盘并刷新 Redis 索引。
	if w.kfCache != nil && len(videoInfo.Keyframes) == 0 {
		go w.refineKeyframesAsync(task.FileID, videoPath, videoInfo)
	}
}

// refineKeyframesAsync 后台扫描关键帧 PTS，写盘缓存并把视频分段重新对齐到关键帧。
// 不阻塞首播：即使长片需要数秒到 1-2 分钟，也只是后续切片更精准，不影响 m3u8 已经服务的初段。
func (w *SliceWorker) refineKeyframesAsync(fileID, videoPath string, info *VideoInfo) {
	logger := w.logger.With(zap.String("file_id", fileID), zap.String("phase", "kf-refine"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	meta, err := w.kfCache.Extract(ctx, fileID, videoPath, info.Duration)
	if err != nil {
		logger.Warn("keyframe extract failed; keeping fixed grid", zap.Error(err))
		return
	}
	if err := w.kfCache.Save(meta); err != nil {
		logger.Warn("keyframe save failed", zap.Error(err))
	}
	if len(meta.PTS) == 0 {
		return
	}
	info.Keyframes = meta.PTS
	refined, err := w.generateSegmentIndex(fileID, info)
	if err != nil {
		logger.Warn("regenerate segment index failed", zap.Error(err))
		return
	}
	if err := w.saveIndex(fileID, refined); err != nil {
		logger.Warn("save refined index failed", zap.Error(err))
		return
	}
	logger.Info("Keyframe index refined", zap.Int("count", len(meta.PTS)),
		zap.Int("video_segments", len(refined.VideoSegments)))
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

// analyzeVideo 兼容旧路径：返回带关键帧的完整视频信息。新代码请用 analyzeVideoFast + keyframes 缓存。
func (w *SliceWorker) analyzeVideo(videoPath string) (*VideoInfo, error) {
	info, err := w.analyzeVideoFast(videoPath)
	if err != nil {
		return nil, err
	}
	keyframes, err := w.getKeyframes(videoPath)
	if err == nil {
		info.Keyframes = keyframes
	} else {
		w.logger.Warn("Keyframe list unavailable; using fixed segment grid",
			zap.Error(err), zap.Float64("duration_sec", info.Duration), zap.String("basename", filepath.Base(videoPath)))
	}
	return info, nil
}

func (w *SliceWorker) getKeyframes(videoPath string) ([]float64, error) {
	cmd := w.ffprobeCommand(
		"-v", "error",
		"-skip_frame", "nokey",
		"-print_format", "json",
		"-select_streams", "v:0",
		"-show_frames",
		"-show_entries", "frame=pkt_pts_time,key_frame",
		"-i", videoPath,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, formatToolExit(w.ffprobe, videoPath+" (keyframes)", err, string(output))
	}

	var frames struct {
		Frames []struct {
			KeyFrame int     `json:"key_frame"`
			PtsTime  float64 `json:"pkt_pts_time"`
		} `json:"frames"`
	}

	if err := json.Unmarshal(output, &frames); err != nil {
		return nil, err
	}

	var keyframes []float64
	for _, frame := range frames.Frames {
		if frame.KeyFrame == 1 {
			keyframes = append(keyframes, frame.PtsTime)
		}
	}

	return keyframes, nil
}

func (w *SliceWorker) generateSegmentIndex(fileID string, info *VideoInfo) (*models.SegmentIndex, error) {
	index := &models.SegmentIndex{
		FileID:      fileID,
		Status:      "slicing",
		Duration:    info.Duration,
		KeyframePTS: append([]float64(nil), info.Keyframes...),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	// 根据关键帧生成视频切片
	segmentDuration := 6.0
	currentTime := 0.0
	segID := 0

	for currentTime < info.Duration {
		// 找到下一个关键帧位置
		endTime := currentTime + segmentDuration
		nextKeyframe := endTime

		for _, kf := range info.Keyframes {
			if kf > currentTime && kf <= endTime+0.5 {
				nextKeyframe = kf
				break
			}
		}

		if nextKeyframe > info.Duration {
			nextKeyframe = info.Duration
		}

		duration := nextKeyframe - currentTime

		index.VideoSegments = append(index.VideoSegments, models.VideoSegmentInfo{
			ID:        segID,
			StartTime: currentTime,
			EndTime:   nextKeyframe,
			Duration:  duration,
			Keyframe:  true,
			SlicePath: "",
			Status:    "indexed",
		})

		currentTime = nextKeyframe
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
