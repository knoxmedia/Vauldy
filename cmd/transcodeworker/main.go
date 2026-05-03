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

	wPx, hPx := parseResolutionWH(res)
	gops := []string{"-g", "48", "-keyint_min", "48", "-sc_threshold", "0"}
	head := []string{"-hide_banner", "-loglevel", "error"}

	switch enc {
	case hwenc.H264QSV:
		vf := "scale=" + wPx + ":" + hPx + ",format=nv12"
		args := w.appendStdInput(head, inputPath, ssSec, durSec)
		args = append(args, "-vf", vf,
			"-c:v", "h264_qsv",
			"-preset", mapX264PresetToQSV(preset),
			"-b:v", task.Bitrate, "-maxrate", task.Bitrate, "-bufsize", "2M",
			"-profile:v", "high")
		args = append(args, gops...)
		return append(args, "-an", "-f", "mpegts", "-muxdelay", "0", outputPath)
	case hwenc.H264AMF:
		vf := "scale=" + wPx + ":" + hPx
		args := w.appendStdInput(head, inputPath, ssSec, durSec)
		args = append(args, "-vf", vf,
			"-c:v", "h264_amf",
			"-quality", mapX264PresetToAMF(preset),
			"-rc", "vbr_peak",
			"-b:v", task.Bitrate)
		args = append(args, gops...)
		return append(args, "-an", "-f", "mpegts", "-muxdelay", "0", outputPath)
	case hwenc.H264NVENC:
		vf := "scale=" + wPx + ":" + hPx
		args := w.appendStdInput(head, inputPath, ssSec, durSec)
		args = append(args, "-vf", vf,
			"-c:v", "h264_nvenc",
			"-preset", mapX264PresetToNVENC(preset),
			"-b:v", task.Bitrate, "-maxrate", task.Bitrate, "-bufsize", "2M",
			"-profile:v", "high")
		args = append(args, gops...)
		return append(args, "-an", "-f", "mpegts", "-muxdelay", "0", outputPath)
	case hwenc.H264VAAPI:
		dev := hwenc.VAAPIDevice()
		vf := "format=nv12,hwupload,scale_vaapi=w=" + wPx + ":h=" + hPx
		args := w.appendVAAPIInput(head, dev, inputPath, ssSec, durSec)
		args = append(args, "-vf", vf,
			"-c:v", "h264_vaapi", "-b:v", task.Bitrate)
		args = append(args, gops...)
		return append(args, "-an", "-f", "mpegts", "-muxdelay", "0", outputPath)
	default:
		codec := strings.TrimSpace(task.Codec)
		if codec == "" {
			codec = "libx264"
		}
		args := w.appendStdInput(head, inputPath, ssSec, durSec)
		args = append(args,
			"-c:v", codec,
			"-b:v", task.Bitrate,
			"-s", res,
			"-preset", preset,
			"-profile:v", "high")
		args = append(args, gops...)
		return append(args, "-an", "-f", "mpegts", "-muxdelay", "0", outputPath)
	}
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
	// 预取后续5个切片
	for i := 1; i <= 5; i++ {
		segID := currentSegID + i

		// 检查是否已存在
		tsPath := fmt.Sprintf("ts/video/%s/%s/%d.ts", fileID, bitrate, segID)
		if w.storage.SegmentExists(fileID, tsPath, 0) {
			continue
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
