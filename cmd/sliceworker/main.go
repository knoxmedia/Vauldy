// cmd/sliceworker/main.go
package sliceworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"go.uber.org/zap"
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

	// 1. 获取视频元数据
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

	// 2. 分析视频（获取关键帧、时长等）
	videoInfo, err := w.analyzeVideo(videoPath)
	if err != nil {
		logger.Error("Failed to analyze video", zap.Error(err))
		w.markSliceFailed(task.FileID, err.Error())
		return
	}

	// 3. 生成虚拟视频分段索引；音频仍一次性物理切片（segment muxer + overlap，避免逐段 -ss/-t 不连续）
	index, err := w.generateSegmentIndex(task.FileID, videoInfo)
	if err != nil {
		logger.Error("Failed to generate segment index", zap.Error(err))
		w.markSliceFailed(task.FileID, err.Error())
		return
	}

	if len(index.AudioSegments) > 0 {
		if err := w.sliceAudio(task.FileID, videoPath, index, videoInfo.AudioCodec); err != nil {
			logger.Error("Failed to slice audio", zap.Error(err))
			w.markSliceFailed(task.FileID, err.Error())
			return
		}
	}

	// 5. 保存索引到 Redis
	if err := w.saveIndex(task.FileID, index); err != nil {
		logger.Error("Failed to save index", zap.Error(err))
		w.markSliceFailed(task.FileID, err.Error())
		return
	}

	if err := preheat.EnqueueInitialSegments(context.Background(), w.redis, task.FileID, len(index.VideoSegments)); err != nil {
		logger.Warn("JIT preheat enqueue failed", zap.Error(err))
	}

	// 6. 更新元数据
	w.updateVideoMetadata(task.FileID, videoInfo)

	logger.Info("Slice task completed",
		zap.Duration("duration", time.Since(startTime)),
		zap.Int("video_segments", len(index.VideoSegments)),
		zap.Int("audio_segments", len(index.AudioSegments)),
	)
}

func (w *SliceWorker) analyzeVideo(videoPath string) (*VideoInfo, error) {
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

	// 获取关键帧位置（必须跳过非关键帧，否则长片会枚举十几万帧，JSON 巨大导致 ffprobe 异常退出）
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

	// 音频索引（仅当有音轨时）；物理文件由 sliceAudio 一次性生成，与 segment muxer 时间轴一致
	if strings.TrimSpace(info.AudioCodec) != "" {
		audioDuration := 6.0
		overlap := 0.1
		audioSegID := 0
		audioTime := 0.0

		for audioTime < info.Duration {
			endTime := audioTime + audioDuration + overlap
			if endTime > info.Duration {
				endTime = info.Duration
			}

			index.AudioSegments = append(index.AudioSegments, models.AudioSegmentInfo{
				ID:        audioSegID,
				StartTime: audioTime,
				EndTime:   endTime,
				Duration:  endTime - audioTime,
				Overlap:   overlap,
				Language:  "eng",
				SlicePath: fmt.Sprintf("raw/audio/%s/segment_%05d.m4a", fileID, audioSegID),
				Status:    "pending",
			})

			audioTime += audioDuration
			audioSegID++
		}
	}

	index.TotalSegments = len(index.VideoSegments)

	return index, nil
}

// sliceAudio 单次 ffmpeg 流水线切分全部音频段。长片整轨重编码极易在 Windows 上失败，故对 AAC 优先 stream copy。
// 不使用 -segment_overlap_duration：部分发行版 ffmpeg 未编译该选项，会报 “Option not found”。
func (w *SliceWorker) sliceAudio(fileID, videoPath string, index *models.SegmentIndex, audioCodec string) error {
	outputDir := filepath.Join(w.storage.BasePath(), "raw", "audio", fileID)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	outPattern := filepath.Join(outputDir, "segment_%05d.m4a")

	codec := strings.ToLower(strings.TrimSpace(audioCodec))
	if codec == "aac" {
		for _, useBSF := range []bool{false, true} {
			if err := w.ffmpegAudioSegmentCopy(videoPath, outPattern, useBSF); err == nil {
				w.logger.Info("Audio segmented via AAC stream copy",
					zap.String("file_id", fileID), zap.Bool("aac_adtstoasc", useBSF))
				for i := range index.AudioSegments {
					index.AudioSegments[i].Status = "sliced"
				}
				return nil
			}
		}
		w.logger.Warn("AAC stream copy failed; falling back to re-encode", zap.String("file_id", fileID))
	}

	if err := w.ffmpegAudioSegmentReencode(videoPath, outPattern); err != nil {
		return err
	}
	for i := range index.AudioSegments {
		index.AudioSegments[i].Status = "sliced"
	}
	return nil
}

func (w *SliceWorker) ffmpegAudioSegmentCopy(videoPath, outPattern string, aacADTSToASC bool) error {
	args := []string{
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-i", videoPath,
		"-vn",
	}
	if aacADTSToASC {
		args = append(args, "-bsf:a", "aac_adtstoasc")
	}
	args = append(args,
		"-c:a", "copy",
		"-f", "segment",
		"-segment_time", "6",
		outPattern,
	)
	cmd := w.ffmpegCommand(args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg slice audio (copy, aac_adtstoasc=%v): %w: %s", aacADTSToASC, err, stderrTail(string(out), 1800))
	}
	return nil
}

func (w *SliceWorker) ffmpegAudioSegmentReencode(videoPath, outPattern string) error {
	cmd := w.ffmpegCommand(
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-i", videoPath,
		"-vn",
		"-c:a", "aac",
		"-b:a", "128k",
		"-threads", "2",
		"-af", "afade=t=in:st=0:d=0.05",
		"-f", "segment",
		"-segment_time", "6",
		outPattern,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg slice audio (re-encode): %w: %s", err, stderrTail(string(out), 1800))
	}
	return nil
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
