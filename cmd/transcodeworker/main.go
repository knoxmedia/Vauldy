// cmd/transcodeworker/main.go
package transcodeworker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"
	"go.uber.org/zap"

	"knox-media/internal/jit/hwenc"
	"knox-media/internal/jit/processctl"
	models "knox-media/internal/model"
)

type Storage interface {
	BasePath() string
	FileExists(path string) bool
	SegmentExists(fileID, path string, segID int) bool
	GetSegmentPath(fileID string, segID int, segmentType string) string
	SaveSegment(fileID string, segID int, segmentType string, data []byte) error
}

type LocalStorage struct {
	basePath string
}

type Config struct {
	RedisAddr     string
	StoragePath   string
	FFmpegPath    string
	WorkerID      string
	MaxConcurrent int
	// VideoEncoder forces JIT encoder: libx264, h264_qsv, h264_amf, h264_nvenc, h264_vaapi (empty = detect).
	VideoEncoder string
}

func NewStorage(basePath string) Storage {
	return &LocalStorage{basePath: basePath}
}

func (s *LocalStorage) BasePath() string {
	return s.basePath
}

func (s *LocalStorage) FileExists(path string) bool {
	fullPath := path
	if !filepath.IsAbs(path) {
		fullPath = filepath.Join(s.basePath, path)
	}
	_, err := os.Stat(fullPath)
	return err == nil
}

func (s *LocalStorage) SegmentExists(_ string, path string, _ int) bool {
	return s.FileExists(path)
}

func (s *LocalStorage) GetSegmentPath(fileID string, segID int, segmentType string) string {
	return filepath.Join(s.basePath, segmentType, fileID, fmt.Sprintf("segment_%05d.mkv", segID))
}

func (s *LocalStorage) SaveSegment(fileID string, segID int, segmentType string, data []byte) error {
	outputDir := filepath.Join(s.basePath, segmentType, fileID)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}
	outputPath := filepath.Join(outputDir, fmt.Sprintf("%d.ts", segID))
	return os.WriteFile(outputPath, data, 0644)
}

type TranscodeWorker struct {
	redis     *redis.Client
	storage   Storage
	ffmpeg    string
	logger    *zap.Logger
	workerID  string
	semaphore chan struct{}
	hwEncoder hwenc.ID
}

func NewTranscodeWorker(cfg *Config) *TranscodeWorker {
	hw := hwenc.DetectH264Encoder(cfg.FFmpegPath)
	if v := strings.TrimSpace(cfg.VideoEncoder); v != "" {
		if id, ok := hwenc.ParseEncoder(v); ok {
			hw = id
		}
	} else if v := strings.TrimSpace(os.Getenv("KNOX_MEDIA_JIT_ENCODER")); v != "" {
		if id, ok := hwenc.ParseEncoder(v); ok {
			hw = id
		}
	}
	logger := zap.L()
	logger.Info("Transcode worker JIT encoder", zap.String("encoder", string(hw)))
	return &TranscodeWorker{
		redis:     redis.NewClient(&redis.Options{Addr: cfg.RedisAddr}),
		storage:   NewStorage(cfg.StoragePath),
		ffmpeg:    cfg.FFmpegPath,
		logger:    logger,
		workerID:  cfg.WorkerID,
		semaphore: make(chan struct{}, cfg.MaxConcurrent),
		hwEncoder: hw,
	}
}

func (w *TranscodeWorker) Start() {
	ctx := context.Background()

	w.logger.Info("Transcode worker started", zap.String("worker_id", w.workerID))

	// 轮询多个优先级队列
	queues := []string{
		"transcode:queue:high",
		"transcode:queue:normal",
		"transcode:queue:low",
	}

	for {
		for _, queue := range queues {
			// 从队列中获取任务
			result, err := w.redis.ZPopMin(ctx, queue, 1).Result()
			if err != nil || len(result) == 0 {
				continue
			}

			taskData := []byte(result[0].Member.(string))
			var task models.TranscodeTask
			if err := json.Unmarshal(taskData, &task); err != nil {
				w.logger.Error("Failed to parse task", zap.Error(err))
				continue
			}

			// 异步处理
			go func() {
				w.semaphore <- struct{}{}
				defer func() { <-w.semaphore }()

				w.processTranscodeTask(&task)
			}()
		}

		time.Sleep(100 * time.Millisecond)
	}
}

func (w *TranscodeWorker) processTranscodeTask(task *models.TranscodeTask) {
	logger := w.logger.With(
		zap.String("file_id", task.FileID),
		zap.Int("segment_id", task.SegmentID),
		zap.String("bitrate", task.Bitrate),
	)

	startTime := time.Now()
	logger.Info("Processing transcode task")

	// 1. 检查是否已存在
	tsPath := fmt.Sprintf("ts/video/%s/%s/%d.ts", task.FileID, task.Bitrate, task.SegmentID)
	if w.storage.SegmentExists(task.FileID, tsPath, 0) {
		logger.Info("Segment already exists")
		w.updateSegmentStatus(task.FileID, task.SegmentID, task.Bitrate, "ready")
		return
	}

	// 2. 获取分布式锁
	lockKey := fmt.Sprintf("lock:transcode:%s:%d:%s", task.FileID, task.SegmentID, task.Bitrate)
	ctx := context.Background()
	locked, err := w.redis.SetNX(ctx, lockKey, w.workerID, 120*time.Second).Result()
	if err != nil || !locked {
		logger.Info("Failed to acquire lock")
		return
	}
	defer w.redis.Del(ctx, lockKey)

	// 3. 双重检查
	if w.storage.SegmentExists(task.FileID, tsPath, 0) {
		logger.Info("Segment created by another worker")
		w.updateSegmentStatus(task.FileID, task.SegmentID, task.Bitrate, "ready")
		return
	}

	// 4. 解析输入：虚拟切片用源文件 + -ss/-t；旧索引用物理 MKV
	inputPath, ssSec, durSec, err := w.resolveSegmentSource(task)
	if err != nil {
		logger.Error("Resolve segment source failed", zap.Error(err))
		w.updateSegmentStatus(task.FileID, task.SegmentID, task.Bitrate, "failed")
		return
	}

	// 5. 检查会话是否存活（如果不是预取任务）
	if task.SessionID != "prefetch" && !w.isSessionAlive(task.SessionID) {
		logger.Info("Session ended, aborting transcode")
		w.updateSegmentStatus(task.FileID, task.SegmentID, task.Bitrate, "aborted")
		return
	}

	// 5.1 lookahead 节流：当转码进度领先客户端请求过多，则延后该任务，避免无限制超前消耗 CPU/GPU。
	// 行为对齐 Jellyfin/Emby 的 transcoding throttle：用户暂停或长时间停留时停止超前转码。
	if w.shouldDeferLookahead(task) {
		logger.Info("Deferring segment (lookahead throttle)",
			zap.String("session_id", task.SessionID),
			zap.Int("segment_id", task.SegmentID),
		)
		w.updateSegmentStatus(task.FileID, task.SegmentID, task.Bitrate, "deferred")
		w.requeueLowPriority(task, 5*time.Second)
		return
	}

	// 6. 执行转码
	outputPath := filepath.Join(w.storage.BasePath(), "tmp",
		fmt.Sprintf("%s_%d_%s.ts", task.FileID, task.SegmentID, task.Bitrate))

	cmd := exec.Command(w.ffmpeg, w.buildTranscodeArgs(inputPath, outputPath, task, ssSec, durSec)...)

	if err := cmd.Start(); err != nil {
		logger.Error("Transcode start failed", zap.Error(err))
		w.updateSegmentStatus(task.FileID, task.SegmentID, task.Bitrate, "failed")
		return
	}
	done := make(chan struct{})
	if task.SessionID != "prefetch" {
		go w.monitorSession(cmd, task.SessionID, done)
	}
	err = cmd.Wait()
	close(done)
	if err != nil {
		logger.Error("Transcode failed", zap.Error(err))
		w.updateSegmentStatus(task.FileID, task.SegmentID, task.Bitrate, "failed")
		return
	}

	// 7. 保存结果
	data, err := os.ReadFile(outputPath)
	if err != nil {
		logger.Error("Failed to read output", zap.Error(err))
		return
	}

	if err := w.storage.SaveSegment(task.FileID, task.SegmentID, "ts/video/"+task.Bitrate, data); err != nil {
		logger.Error("Failed to save segment", zap.Error(err))
		return
	}

	// 8. 更新状态和统计
	w.updateSegmentStatus(task.FileID, task.SegmentID, task.Bitrate, "ready")
	w.updateSegmentStats(task.FileID, task.Bitrate, task.SegmentID, len(data))
	w.updateTranscodeStats(task.FileID, task.Bitrate, true)

	// 9. 清理临时文件
	os.Remove(outputPath)

	logger.Info("Transcode completed",
		zap.Duration("duration", time.Since(startTime)),
		zap.Int("size", len(data)),
	)

	// 10. 预取后续切片（如果是从高优先级队列处理的）
	if task.Priority == 0 {
		w.prefetchNextSegments(task.FileID, task.SegmentID, task.Bitrate)
	}
}

func resolutionForBitrate(bitrate string) string {
	m := map[string]string{
		"8000k": "3840x2160",
		"4000k": "1920x1080",
		"2000k": "1280x720",
		"1000k": "854x480",
		"500k":  "640x360",
	}
	if res, ok := m[bitrate]; ok {
		return res
	}
	return "1280x720"
}

func mapX264PresetToQSV(p string) string {
	switch p {
	case "ultrafast", "veryfast":
		return "veryfast"
	case "fast":
		return "faster"
	case "slow":
		return "slow"
	default:
		return "medium"
	}
}

func mapX264PresetToAMF(p string) string {
	switch p {
	case "ultrafast", "veryfast", "fast":
		return "speed"
	case "slow":
		return "quality"
	default:
		return "balanced"
	}
}

func mapX264PresetToNVENC(p string) string {
	switch p {
	case "ultrafast":
		return "p1"
	case "veryfast":
		return "p2"
	case "fast":
		return "p3"
	default:
		return "p4"
	}
}

func parseResolutionWH(res string) (w, h string) {
	res = strings.TrimSpace(strings.ToLower(res))
	idx := strings.IndexByte(res, 'x')
	if idx <= 0 || idx >= len(res)-1 {
		return "1280", "720"
	}
	return strings.TrimSpace(res[:idx]), strings.TrimSpace(res[idx+1:])
}

func formatSeekTS(sec float64) string {
	return strconv.FormatFloat(sec, 'f', 4, 64)
}

func (w *TranscodeWorker) appendStdInput(head []string, inputPath string, ssSec, durSec float64) []string {
	if durSec > 0 {
		return append(head, "-ss", formatSeekTS(ssSec), "-i", inputPath, "-t", formatSeekTS(durSec))
	}
	return append(head, "-i", inputPath)
}

func (w *TranscodeWorker) appendVAAPIInput(head []string, dev, inputPath string, ssSec, durSec float64) []string {
	out := append([]string(nil), head...)
	if durSec > 0 {
		out = append(out, "-ss", formatSeekTS(ssSec))
	}
	out = append(out, "-vaapi_device", dev, "-i", inputPath)
	if durSec > 0 {
		out = append(out, "-t", formatSeekTS(durSec))
	}
	return out
}

func (w *TranscodeWorker) resolveSegmentSource(task *models.TranscodeTask) (inputPath string, ssSec, durSec float64, err error) {
	ctx := context.Background()
	raw, err := w.redis.Get(ctx, "video:index:"+task.FileID).Bytes()
	if err != nil {
		return "", 0, 0, err
	}
	var idx models.SegmentIndex
	if err := json.Unmarshal(raw, &idx); err != nil {
		return "", 0, 0, err
	}
	if task.SegmentID < 0 || task.SegmentID >= len(idx.VideoSegments) {
		return "", 0, 0, fmt.Errorf("segment id out of range")
	}
	seg := idx.VideoSegments[task.SegmentID]
	virtual := strings.TrimSpace(seg.SlicePath) == "" || seg.Status == "indexed"
	if virtual && seg.Duration <= 0 {
		return "", 0, 0, fmt.Errorf("invalid virtual segment duration")
	}
	if !virtual {
		mkv := filepath.Join(w.storage.BasePath(), "raw", "video", task.FileID, fmt.Sprintf("segment_%05d.mkv", task.SegmentID))
		if !w.storage.FileExists(mkv) {
			return "", 0, 0, fmt.Errorf("legacy mkv segment missing")
		}
		return filepath.Clean(mkv), 0, 0, nil
	}
	fp, err := w.redis.HGet(ctx, "video:meta:"+task.FileID, "file_path").Result()
	if err != nil || strings.TrimSpace(fp) == "" {
		return "", 0, 0, fmt.Errorf("source file_path missing")
	}
	inputPath = strings.TrimSpace(fp)
	if !filepath.IsAbs(inputPath) {
		inputPath = filepath.Join(w.storage.BasePath(), filepath.FromSlash(inputPath))
	}
	return filepath.Clean(inputPath), seg.StartTime, seg.Duration, nil
}

func (w *TranscodeWorker) buildTranscodeArgs(inputPath, outputPath string, task *models.TranscodeTask, ssSec, durSec float64) []string {
	res := task.Resolution
	if res == "" {
		res = resolutionForBitrate(task.Bitrate)
	}
	preset := strings.TrimSpace(task.Preset)
	if preset == "" {
		if w.sessionSeekBoost(task.SessionID) {
			preset = "ultrafast"
		} else {
			preset = "medium"
		}
	}

	forceSW := strings.EqualFold(strings.TrimSpace(task.Codec), "libx264")
	enc := w.hwEncoder
	if forceSW {
		enc = hwenc.Libx264
	}

	// 音频处理：源已经是 AAC 时直接 -c:a copy（避免重编码），否则 transcode 为 AAC 128k。
	// 不再单独输出音频段；音视频混流到同一个 .ts 由播放器消费。
	audioArgs := w.audioOutputArgs(task)

	// Codec passthrough：当源已经是 H.264 且没有强制软编时，直接 -c:v copy 可大幅降低 CPU。
	// 仅当请求的目标比特率不显著低于源（缩小档）时启用，否则继续走重编码以适配 ABR。
	if !forceSW && w.canVideoPassthrough(task) {
		head := []string{"-hide_banner", "-loglevel", "error"}
		args := w.appendStdInput(head, inputPath, ssSec, durSec)
		args = append(args,
			"-map", "0:v:0",
			"-map", "0:a:0?",
			"-c:v", "copy",
			"-bsf:v", "h264_mp4toannexb",
		)
		args = append(args, audioArgs...)
		args = append(args, "-f", "mpegts", "-muxdelay", "0", outputPath)
		return args
	}

	wPx, hPx := parseResolutionWH(res)
	gops := []string{"-g", "48", "-keyint_min", "48", "-sc_threshold", "0"}
	head := []string{"-hide_banner", "-loglevel", "error"}
	mapArgs := []string{"-map", "0:v:0", "-map", "0:a:0?"}

	switch enc {
	case hwenc.H264QSV:
		vf := "scale=" + wPx + ":" + hPx + ",format=nv12"
		args := w.appendStdInput(head, inputPath, ssSec, durSec)
		args = append(args, mapArgs...)
		args = append(args, "-vf", vf,
			"-c:v", "h264_qsv",
			"-preset", mapX264PresetToQSV(preset),
			"-b:v", task.Bitrate, "-maxrate", task.Bitrate, "-bufsize", "2M",
			"-profile:v", "high")
		args = append(args, gops...)
		args = append(args, audioArgs...)
		return append(args, "-f", "mpegts", "-muxdelay", "0", outputPath)
	case hwenc.H264AMF:
		vf := "scale=" + wPx + ":" + hPx
		args := w.appendStdInput(head, inputPath, ssSec, durSec)
		args = append(args, mapArgs...)
		args = append(args, "-vf", vf,
			"-c:v", "h264_amf",
			"-quality", mapX264PresetToAMF(preset),
			"-rc", "vbr_peak",
			"-b:v", task.Bitrate)
		args = append(args, gops...)
		args = append(args, audioArgs...)
		return append(args, "-f", "mpegts", "-muxdelay", "0", outputPath)
	case hwenc.H264NVENC:
		vf := "scale=" + wPx + ":" + hPx
		args := w.appendStdInput(head, inputPath, ssSec, durSec)
		args = append(args, mapArgs...)
		args = append(args, "-vf", vf,
			"-c:v", "h264_nvenc",
			"-preset", mapX264PresetToNVENC(preset),
			"-b:v", task.Bitrate, "-maxrate", task.Bitrate, "-bufsize", "2M",
			"-profile:v", "high")
		args = append(args, gops...)
		args = append(args, audioArgs...)
		return append(args, "-f", "mpegts", "-muxdelay", "0", outputPath)
	case hwenc.H264VAAPI:
		dev := hwenc.VAAPIDevice()
		vf := "format=nv12,hwupload,scale_vaapi=w=" + wPx + ":h=" + hPx
		args := w.appendVAAPIInput(head, dev, inputPath, ssSec, durSec)
		args = append(args, mapArgs...)
		args = append(args, "-vf", vf,
			"-c:v", "h264_vaapi", "-b:v", task.Bitrate)
		args = append(args, gops...)
		args = append(args, audioArgs...)
		return append(args, "-f", "mpegts", "-muxdelay", "0", outputPath)
	default:
		codec := strings.TrimSpace(task.Codec)
		if codec == "" {
			codec = "libx264"
		}
		args := w.appendStdInput(head, inputPath, ssSec, durSec)
		args = append(args, mapArgs...)
		args = append(args,
			"-c:v", codec,
			"-b:v", task.Bitrate,
			"-s", res,
			"-preset", preset,
			"-profile:v", "high")
		args = append(args, gops...)
		args = append(args, audioArgs...)
		return append(args, "-f", "mpegts", "-muxdelay", "0", outputPath)
	}
}

// audioOutputArgs 返回 ffmpeg 音频输出参数。源 AAC 直接 stream copy；其他格式重编码为 AAC 128k。
// 当源没有音轨时使用 -an 避免 ffmpeg 报错。
func (w *TranscodeWorker) audioOutputArgs(task *models.TranscodeTask) []string {
	ctx := context.Background()
	codec, _ := w.redis.HGet(ctx, "video:meta:"+task.FileID, "audio_codec").Result()
	codec = strings.ToLower(strings.TrimSpace(codec))
	if codec == "" {
		return []string{"-an"}
	}
	if codec == "aac" {
		return []string{"-c:a", "copy", "-bsf:a", "aac_adtstoasc"}
	}
	return []string{"-c:a", "aac", "-b:a", "128k", "-ac", "2", "-ar", "48000"}
}

func (w *TranscodeWorker) monitorSession(cmd *exec.Cmd, sessionID string, finished <-chan struct{}) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var suspended bool

	for {
		select {
		case <-finished:
			if suspended && cmd.Process != nil && cmd.Process.Pid > 0 {
				_ = processctl.Resume(cmd.Process.Pid)
			}
			return
		case <-ticker.C:
		}

		if cmd.Process == nil {
			return
		}
		pid := cmd.Process.Pid
		if pid <= 0 {
			continue
		}

		if !w.isSessionAlive(sessionID) {
			_ = cmd.Process.Kill()
			return
		}

		paused := w.isSessionTranscodePaused(sessionID)
		if paused && !suspended {
			if err := processctl.Suspend(pid); err != nil {
				w.logger.Debug("transcode suspend skipped",
					zap.String("session_id", sessionID),
					zap.Int("pid", pid),
					zap.Error(err))
				continue
			}
			suspended = true
		}

		for suspended {
			select {
			case <-finished:
				_ = processctl.Resume(pid)
				return
			case <-time.After(200 * time.Millisecond):
			}
			if !w.isSessionAlive(sessionID) {
				_ = cmd.Process.Kill()
				return
			}
			if !w.isSessionTranscodePaused(sessionID) {
				if err := processctl.Resume(pid); err != nil {
					w.logger.Warn("transcode resume failed",
						zap.String("session_id", sessionID),
						zap.Int("pid", pid),
						zap.Error(err))
				}
				suspended = false
				break
			}
		}
	}
}

func (w *TranscodeWorker) isSessionAlive(sessionID string) bool {
	ctx := context.Background()
	exists, _ := w.redis.Exists(ctx, "session:"+sessionID).Result()
	return exists > 0
}

func (w *TranscodeWorker) sessionSeekBoost(sessionID string) bool {
	if sessionID == "" || sessionID == "prefetch" {
		return false
	}
	ctx := context.Background()
	n, err := w.redis.Exists(ctx, "jit:session_seek_boost:"+sessionID).Result()
	return err == nil && n > 0
}

func (w *TranscodeWorker) isSessionTranscodePaused(sessionID string) bool {
	ctx := context.Background()
	v, err := w.redis.HGet(ctx, "session:"+sessionID, "transcode_paused").Result()
	if err != nil {
		return false
	}
	return v == "1" || v == "true" || v == "yes"
}

func (w *TranscodeWorker) updateSegmentStatus(fileID string, segID int, bitrate, status string) {
	ctx := context.Background()
	key := fmt.Sprintf("segment:status:%s:%d:%s", fileID, segID, bitrate)
	w.redis.Set(ctx, key, status, 5*time.Minute)
}

func (w *TranscodeWorker) updateSegmentStats(fileID, bitrate string, segID, size int) {
	ctx := context.Background()
	key := fmt.Sprintf("segment:access:%s:%s:%d", fileID, bitrate, segID)

	w.redis.HSet(ctx, key,
		"size", size,
		"last_access", time.Now().Unix(),
		"create_time", time.Now().Unix(),
	)
}

func (w *TranscodeWorker) updateTranscodeStats(fileID, bitrate string, success bool) {
	ctx := context.Background()
	key := "transcode:stats:" + fileID + ":" + bitrate

	if success {
		w.redis.HIncrBy(ctx, key, "completed", 1)
	} else {
		w.redis.HIncrBy(ctx, key, "failed", 1)
	}
}

func (w *TranscodeWorker) prefetchNextSegments(fileID string, currentSegID int, bitrate string) {
	// Jellyfin/Emby 风格：仅当客户端最近请求段在 currentSegID 附近时才继续预取，
	// 否则停止预取避免远超前于播放头浪费 CPU/GPU。
	maxAhead := lookaheadLimit()
	for i := 1; i <= 5; i++ {
		segID := currentSegID + i

		tsPath := fmt.Sprintf("ts/video/%s/%s/%d.ts", fileID, bitrate, segID)
		if w.storage.SegmentExists(fileID, tsPath, 0) {
			continue
		}

		// 若已超过任意活跃会话的 lookahead 上限，不再继续 prefetch
		if w.fileLookaheadFull(fileID, segID, maxAhead) {
			break
		}

		res := resolutionForBitrate(bitrate)
		task := &models.TranscodeTask{
			FileID:     fileID,
			SegmentID:  segID,
			Bitrate:    bitrate,
			Resolution: res,
			Codec:      "",
			SessionID:  "prefetch",
			Priority:   2,
			CreatedAt:  time.Now().Unix(),
		}

		taskData, _ := json.Marshal(task)
		w.redis.ZAdd(context.Background(), "transcode:queue:low", &redis.Z{
			Score:  float64(time.Now().Unix()),
			Member: taskData,
		})
	}
}

// canVideoPassthrough returns true if the task target (bitrate ladder) matches the source resolution
// and source codec is H.264, so we can stream-copy the video track instead of re-encoding.
func (w *TranscodeWorker) canVideoPassthrough(task *models.TranscodeTask) bool {
	if task == nil || task.FileID == "" {
		return false
	}
	if strings.TrimSpace(os.Getenv("KNOX_MEDIA_JIT_DISABLE_PASSTHROUGH")) == "1" {
		return false
	}
	ctx := context.Background()
	meta, err := w.redis.HGetAll(ctx, "video:meta:"+task.FileID).Result()
	if err != nil || len(meta) == 0 {
		return false
	}
	srcCodec := strings.ToLower(strings.TrimSpace(meta["codec"]))
	if !(srcCodec == "h264" || srcCodec == "avc1") {
		return false
	}
	srcH, _ := strconv.Atoi(strings.TrimSpace(meta["height"]))
	if srcH <= 0 {
		return false
	}
	_, hStr := parseResolutionWH(task.Resolution)
	tgtH, _ := strconv.Atoi(hStr)
	if tgtH <= 0 {
		return false
	}
	// 目标高度 ≥ 源高度的 90%：直接复制；否则需要降采样，仍走转码
	if tgtH < srcH-int(float64(srcH)*0.1) {
		return false
	}
	return true
}

// lookaheadLimit 是 ffmpeg 输出领先客户端请求的最大段数；超过则任务延后或不入队。
// 默认 8 段；可由 KNOX_MEDIA_JIT_LOOKAHEAD 覆盖。
func lookaheadLimit() int {
	v := strings.TrimSpace(os.Getenv("KNOX_MEDIA_JIT_LOOKAHEAD"))
	if v == "" {
		return 8
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 8
	}
	return n
}

// shouldDeferLookahead 当 task.SegmentID 比客户端最近请求的段超前过多时返回 true。
// prefetch / 不存在 session 时也按所有相关活跃 session 的最大 current_segment 判断。
func (w *TranscodeWorker) shouldDeferLookahead(task *models.TranscodeTask) bool {
	if task == nil || task.SegmentID < 0 {
		return false
	}
	max := lookaheadLimit()
	ctx := context.Background()
	if task.SessionID != "" && task.SessionID != "prefetch" {
		// 单会话：以该会话的 current_segment 为准
		v, err := w.redis.HGet(ctx, "session:"+task.SessionID, "current_segment").Result()
		if err != nil {
			// 会话还没产生段请求，允许首次预热（不阻塞）
			return false
		}
		curr, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return false
		}
		// seek 提速时段在请求左右大幅波动，放宽限制
		if w.sessionSeekBoost(task.SessionID) {
			return false
		}
		if task.SegmentID-curr > max {
			return true
		}
		return false
	}
	// prefetch：比对该 fileID 的所有活跃会话
	return w.fileLookaheadFull(task.FileID, task.SegmentID, max)
}

// fileLookaheadFull 返回 true 当请求段相对所有活跃会话 current_segment 全都超前 > max。
// 没有任何会话时（典型为 ingest 预热）允许 prefetch 推进。
func (w *TranscodeWorker) fileLookaheadFull(fileID string, segID int, max int) bool {
	ctx := context.Background()
	// 限定遍历会话数量以避免大量按键扫描；单服上一般同时只少数会话观看同一文件
	iter := w.redis.Scan(ctx, 0, "session:*", 64).Iterator()
	any := false
	for iter.Next(ctx) {
		key := iter.Val()
		got, err := w.redis.HGetAll(ctx, key).Result()
		if err != nil {
			continue
		}
		if got["file_id"] != fileID {
			continue
		}
		any = true
		curr, err := strconv.Atoi(strings.TrimSpace(got["current_segment"]))
		if err != nil {
			continue
		}
		if segID-curr <= max {
			return false
		}
	}
	if !any {
		return false
	}
	return true
}

// requeueLowPriority 延迟 backoff 后把任务重新入低优先级队列。
func (w *TranscodeWorker) requeueLowPriority(task *models.TranscodeTask, backoff time.Duration) {
	if task == nil {
		return
	}
	data, err := json.Marshal(task)
	if err != nil {
		return
	}
	w.redis.ZAdd(context.Background(), "transcode:queue:low", &redis.Z{
		Score:  float64(time.Now().Add(backoff).Unix()),
		Member: data,
	})
}
