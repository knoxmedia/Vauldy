// cmd/transcodeworker/main.go
package transcodeworker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	// HLSContinuousEnabled when true, uses a single ffmpeg process with -f segment muxer.
	HLSContinuousEnabled bool
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
	// Layout must match scheduler handleVideoSegment / LoadSegment:
	//   <base>/ts/video/<fileID>/<bitrate>/<seg>.ts
	bitrate := strings.TrimPrefix(segmentType, "ts/video/")
	if bitrate == segmentType || bitrate == "" {
		return fmt.Errorf("SaveSegment: expected segmentType ts/video/<bitrate>, got %q", segmentType)
	}
	outputDir := filepath.Join(s.basePath, "ts", "video", fileID, bitrate)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	outputPath := filepath.Join(outputDir, fmt.Sprintf("%d.ts", segID))
	return os.WriteFile(outputPath, data, 0o644)
}

type TranscodeWorker struct {
	redis         *redis.Client
	storage       Storage
	ffmpeg        string
	logger        *zap.Logger
	workerID      string
	semaphore     chan struct{}
	hwEncoder     hwenc.ID
	hlsContinuous bool
	ffmpegLogMu   sync.Mutex // serializes ffmpeg console lines from concurrent tasks
}

// linePrefixWriter buffers subprocess output and emits whole lines prefixed for readability.
type linePrefixWriter struct {
	mu     *sync.Mutex
	out    io.Writer
	prefix string
	buf    bytes.Buffer
}

func (w *linePrefixWriter) Write(p []byte) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}
	n = len(p)
	w.buf.Write(p)
	for {
		b := w.buf.Bytes()
		idx := bytes.IndexByte(b, '\n')
		if idx < 0 {
			break
		}
		line := append([]byte(nil), b[:idx+1]...)
		w.buf.Next(idx + 1)
		w.mu.Lock()
		_, werr := w.out.Write(append([]byte(w.prefix), line...))
		w.mu.Unlock()
		if werr != nil {
			return n, werr
		}
	}
	return n, nil
}

func (w *linePrefixWriter) flush() error {
	b := w.buf.Bytes()
	if len(b) == 0 {
		return nil
	}
	w.buf.Reset()
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := fmt.Fprintf(w.out, "%s%s", w.prefix, b); err != nil {
		return err
	}
	if !bytes.HasSuffix(b, []byte{'\n'}) {
		_, err := w.out.Write([]byte{'\n'})
		return err
	}
	return nil
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
		redis:         redis.NewClient(&redis.Options{Addr: cfg.RedisAddr}),
		storage:       NewStorage(cfg.StoragePath),
		ffmpeg:        cfg.FFmpegPath,
		logger:        logger,
		workerID:      cfg.WorkerID,
		semaphore:     make(chan struct{}, cfg.MaxConcurrent),
		hwEncoder:     hw,
		hlsContinuous: cfg.HLSContinuousEnabled,
	}
}

func (w *TranscodeWorker) Start() {
	ctx := context.Background()

	w.logger.Info("Transcode worker started",
		zap.String("worker_id", w.workerID),
		zap.Int("max_concurrent", cap(w.semaphore)),
	)

	// Continuous HLS 模式：额外启动连续转码循环（处理 long-running ffmpeg）。
	if w.hlsContinuous {
		go w.startContinuousHLSLoop()
	}

	// 逐段转码循环：在两种模式下都运行。
	// - Continuous HLS 模式：处理 seek 后的即时响应段（高优先级，单段转码）。
	// - 传统模式：处理所有段的转码。
	// 优先级从高到低；只有当某档无任务时才落到下一档。
	queues := []string{
		"transcode:queue:high",
		"transcode:queue:normal",
		"transcode:queue:low",
	}

	for {
		// 关键修复：先占用一个 slot 再去拉任务。这避免「pop 出 low priority 任务 → 进 in-memory FIFO →
		// 把 high priority 用户请求挡在身后」的优先级反转。
		w.semaphore <- struct{}{}

		var task *models.TranscodeTask
		for _, queue := range queues {
			res, err := w.redis.ZPopMin(ctx, queue, 1).Result()
			if err != nil || len(res) == 0 {
				continue
			}
			taskData, ok := res[0].Member.(string)
			if !ok {
				w.logger.Error("Unexpected member type", zap.String("queue", queue))
				continue
			}
			var t models.TranscodeTask
			if err := json.Unmarshal([]byte(taskData), &t); err != nil {
				w.logger.Error("Failed to parse task",
					zap.String("queue", queue), zap.Error(err))
				continue
			}
			task = &t
			break
		}

		if task == nil {
			// 三档全空：释放 slot 并退避一会儿，避免 100% 空转 CPU。
			<-w.semaphore
			time.Sleep(100 * time.Millisecond)
			continue
		}

		// 关键修复：goroutine 内捕获 task 的值副本，否则 loop 后续迭代覆盖 task 指针会让
		// 该 goroutine 执行错误的 fileID / segmentID（典型 Go 闭包陷阱）。
		t := *task
		go func(t models.TranscodeTask) {
			defer func() { <-w.semaphore }()
			defer func() {
				if r := recover(); r != nil {
					w.logger.Error("transcode task panic",
						zap.Any("recover", r),
						zap.String("file_id", t.FileID),
						zap.Int("segment_id", t.SegmentID),
					)
					w.updateSegmentStatus(t.FileID, t.SegmentID, t.Bitrate, "failed")
				}
			}()
			w.processTranscodeTask(&t)
		}(t)
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
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		logger.Error("Failed to create transcode tmp dir", zap.Error(err), zap.String("dir", filepath.Dir(outputPath)))
		w.updateSegmentStatus(task.FileID, task.SegmentID, task.Bitrate, "failed")
		return
	}

	transcodeArgs := w.buildTranscodeArgs(inputPath, outputPath, task, ssSec, durSec, w.hwEncoder)
	logger.Info("ffmpeg transcode args", zap.String("encoder", string(w.hwEncoder)), zap.String("args", strings.Join(transcodeArgs, " ")))

	logPrefix := fmt.Sprintf("[transcode file=%s seg=%d br=%s session=%s] ",
		task.FileID, task.SegmentID, task.Bitrate, task.SessionID)
	outLog := &linePrefixWriter{mu: &w.ffmpegLogMu, out: os.Stdout, prefix: logPrefix}
	errLog := &linePrefixWriter{mu: &w.ffmpegLogMu, out: os.Stderr, prefix: logPrefix}

	runOnce := func(args []string) error {
		cmd := exec.Command(w.ffmpeg, args...)
		cmd.Stdout = outLog
		cmd.Stderr = errLog
		if err := cmd.Start(); err != nil {
			return err
		}
		done := make(chan struct{})
		if task.SessionID != "prefetch" {
			go w.monitorSession(cmd, task.SessionID, done)
		}
		err := cmd.Wait()
		close(done)
		_ = outLog.flush()
		_ = errLog.flush()
		return err
	}

	err = runOnce(transcodeArgs)
	if err != nil && w.hwEncoder != hwenc.Libx264 {
		logger.Warn("Hardware transcode failed, falling back to libx264", zap.Error(err))
		transcodeArgs = w.buildTranscodeArgs(inputPath, outputPath, task, ssSec, durSec, hwenc.Libx264)
		logger.Info("ffmpeg transcode fallback args", zap.String("args", strings.Join(transcodeArgs, " ")))
		err = runOnce(transcodeArgs)
	}
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

	// Continuous HLS 模式下不逐段 prefetch：long-running ffmpeg 自然产出后续段。
	if !w.hlsContinuous && task.Priority == 0 {
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

func (w *TranscodeWorker) buildTranscodeArgs(inputPath, outputPath string, task *models.TranscodeTask, ssSec, durSec float64, enc hwenc.ID) []string {
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

	head := []string{"-hide_banner", "-loglevel", "error"}
	mapArgs := []string{"-map", "0:v:0", "-map", "0:a:0?"}

	pipeline := hwenc.PipelineModeForInput(enc != hwenc.Libx264, true)
	if enc == hwenc.Libx264 {
		pipeline = hwenc.PipelineSoftware
	}
	videoArgs := hwenc.BuildInstantVideoArgs(hwenc.InstantVideoPlan{
		Encoder:    enc,
		Mode:       pipeline,
		Resolution: res,
		Bitrate:    task.Bitrate,
		X264Preset: preset,
		SessionGOP: false,
	})
	args := w.appendVideoInput(head, enc, inputPath, ssSec, durSec)
	args = append(args, mapArgs...)
	args = append(args, videoArgs...)
	args = append(args, audioArgs...)
	return append(args, "-f", "mpegts", "-muxdelay", "0", outputPath)
}

func (w *TranscodeWorker) appendVideoInput(head []string, enc hwenc.ID, inputPath string, ssSec, durSec float64) []string {
	if enc != hwenc.Libx264 {
		head = append(head, hwenc.InputAccelArgs(enc)...)
	}
	if durSec > 0 {
		return append(head, "-ss", formatSeekTS(ssSec), "-i", inputPath, "-t", formatSeekTS(durSec))
	}
	return append(head, "-i", inputPath)
}

// audioOutputArgs 返回 ffmpeg 音频输出参数。源 AAC 直接 stream copy；其他格式重编码为 AAC 128k。
// 当源没有音轨时使用 -an 避免 ffmpeg 报错。
// 如果外部已经分离了音轨（audio_playlist 键已在 Redis 中），跳过音频编码，只输出纯视频 TS。
func (w *TranscodeWorker) audioOutputArgs(task *models.TranscodeTask) []string {
	ctx := context.Background()
	// If audio has been pre-extracted as HLS, skip audio in transcode output.
	// Check both old single-key and new multi-track key.
	if ap, _ := w.redis.HGet(ctx, "video:meta:"+task.FileID, "audio_playlists").Result(); strings.TrimSpace(ap) != "" {
		return []string{"-an"}
	}
	if ap, _ := w.redis.HGet(ctx, "video:meta:"+task.FileID, "audio_playlist").Result(); strings.TrimSpace(ap) != "" {
		return []string{"-an"}
	}
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
	// 仅追 2 段：客户端按顺序拉，到达第 N+2 段时再触发下一次链式追档。
	for i := 1; i <= 2; i++ {
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

// ==================== Continuous HLS Transcode ====================

// ContinuousHLSJob describes a long-running ffmpeg process that outputs HLS segments
// directly via -f segment muxer, for a specific fileID + bitrate.
type ContinuousHLSJob struct {
	FileID    string `json:"file_id"`
	Bitrate   string `json:"bitrate"`
	SessionID string `json:"session_id"`
	// StartSegID is the initial segment number (used for -segment_start_number).
	StartSegID int `json:"start_seg_id"`
}

// continuousJobKey is the Redis key tracking an active continuous HLS job for this file+bitrate.
func continuousJobKey(fileID, bitrate string) string {
	return fmt.Sprintf("continuous:hls:%s:%s", fileID, bitrate)
}

// startContinuousHLSLoop checks for continuous HLS jobs and starts a goroutine per job.
func (w *TranscodeWorker) startContinuousHLSLoop() {
	ctx := context.Background()
	queueKey := "continuous:jobs:queue"

	for {
		res, err := w.redis.BLPop(ctx, 5*time.Second, queueKey).Result()
		if err != nil || len(res) < 2 {
			continue
		}
		var job ContinuousHLSJob
		if err := json.Unmarshal([]byte(res[1]), &job); err != nil {
			continue
		}

		// Acquire slot
		w.semaphore <- struct{}{}

		jk := job // capture
		go func() {
			defer func() { <-w.semaphore }()
			defer func() {
				if r := recover(); r != nil {
					w.logger.Error("continuous HLS panic", zap.Any("recover", r),
						zap.String("file_id", jk.FileID), zap.String("bitrate", jk.Bitrate))
				}
			}()
			w.runContinuousHLS(&jk)
		}()
	}
}

// runContinuousHLS builds ffmpeg args with -f segment muxer, runs a single long-lived
// ffmpeg process that outputs TS segments directly to storage, and monitors the .m3u8
// manifest to update per-segment status in Redis.
func (w *TranscodeWorker) runContinuousHLS(job *ContinuousHLSJob) {
	logger := w.logger.With(zap.String("file_id", job.FileID), zap.String("bitrate", job.Bitrate), zap.String("mode", "continuous-hls"))
	logger.Info("Starting continuous HLS transcode", zap.Int("start_seg", job.StartSegID))

	key := continuousJobKey(job.FileID, job.Bitrate)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Mark job as active, storing the start segment ID so the scheduler can
	// decide whether a seek target is within our current range.
	w.redis.Set(ctx, key, w.workerID, 0)
	w.redis.HSet(ctx, key, "start_seg", job.StartSegID, "latest_seg", job.StartSegID)

	// Clean up on exit.
	defer func() {
		w.redis.Del(ctx, key)
		logger.Info("Continuous HLS transcode finished")
	}()

	// Watch for cancellation signals: Redis key deletion or session expiry.
	go func() {
		tk := time.NewTicker(500 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
			}
			// Cancelled externally (scheduler deleted the key).
			n, err := w.redis.Exists(ctx, key).Result()
			if err != nil || n == 0 {
				logger.Info("Continuous HLS cancelled externally (key deleted)")
				cancel()
				return
			}
			// Session ended: stop if no active sessions reference this file.
			if job.SessionID != "" && job.SessionID != "prefetch" {
				sessExists, _ := w.redis.Exists(ctx, "session:"+job.SessionID).Result()
				if sessExists == 0 {
					logger.Info("Continuous HLS session ended", zap.String("session_id", job.SessionID))
					cancel()
					return
				}
			}
		}
	}()

	// Resolve source and segment index.
	segIndex, segErr := w.getSegmentIndexRedis(job.FileID)
	if segErr != nil {
		logger.Error("Get segment index failed", zap.Error(segErr))
		return
	}

	// Calculate the start time offset from the segment index.
	var ssSec float64
	if job.StartSegID > 0 && job.StartSegID < len(segIndex.VideoSegments) {
		ssSec = segIndex.VideoSegments[job.StartSegID].StartTime
	}

	// Get source file path from Redis.
	srcPath, err := w.redis.HGet(ctx, "video:meta:"+job.FileID, "file_path").Result()
	if err != nil || strings.TrimSpace(srcPath) == "" {
		logger.Error("Source file path not found", zap.Error(err))
		return
	}
	inputPath := strings.TrimSpace(srcPath)

	segDuration := 6.0 // matching sliceworker target

	// Output directly into the segment storage directory.
	outDir := filepath.Join(w.storage.BasePath(), "ts", "video", job.FileID, job.Bitrate)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		logger.Error("Failed to create output dir", zap.Error(err))
		return
	}

	m3u8Path := filepath.Join(outDir, "live.m3u8")
	segPattern := filepath.Join(outDir, "%d.ts")

	args := []string{"-hide_banner", "-loglevel", "error"}
	if ssSec > 0.01 {
		// Apply input seek: -copyts -start_at_zero keeps output timestamps starting from 0 after the seek point.
		args = append(args, "-copyts", "-start_at_zero", "-ss", formatSeconds(ssSec))
	}
	args = append(args, "-i", inputPath)
	args = append(args, "-map", "0:v:0")

	// Audio: skip if external audio available.
	audioArgs := w.audioOutputArgs(&models.TranscodeTask{FileID: job.FileID, Bitrate: job.Bitrate})
	if !strings.EqualFold(audioArgs[0], "-an") {
		args = append(args, "-map", "0:a:0?")
	}
	args = append(args, "-sn")

	// Build video encoder args using existing logic.
	videoArgs := w.buildVideoEncoderArgsCtn(job, segDuration)
	args = append(args, videoArgs...)
	args = append(args, audioArgs...)

	// HLS segment muxer.
	args = append(args,
		"-max_delay", "5000000",
		"-avoid_negative_ts", "disabled",
		"-f", "segment",
		"-segment_format", "mpegts",
		"-segment_list", m3u8Path,
		"-segment_list_type", "m3u8",
		"-segment_time", fmt.Sprintf("%.3f", segDuration),
		"-segment_start_number", strconv.Itoa(job.StartSegID),
		"-individual_header_trailer", "0",
		"-write_header_trailer", "0",
		segPattern,
	)

	logger.Info("Continuous HLS ffmpeg args", zap.Float64("ss_sec", ssSec), zap.Int("start_seg", job.StartSegID), zap.String("args", strings.Join(args, " ")))

	cmd := exec.CommandContext(ctx, w.ffmpeg, args...)
	logPrefix := fmt.Sprintf("[continuous-hls file=%s br=%s sess=%s] ", job.FileID, job.Bitrate, job.SessionID)
	outLog := &linePrefixWriter{mu: &w.ffmpegLogMu, out: os.Stdout, prefix: logPrefix}
	errLog := &linePrefixWriter{mu: &w.ffmpegLogMu, out: os.Stderr, prefix: logPrefix}
	cmd.Stdout = outLog
	cmd.Stderr = errLog

	if err := cmd.Start(); err != nil {
		logger.Error("Continuous HLS start failed", zap.Error(err))
		return
	}
	// Continuous HLS runs until cancelled (seek out of range) or source exhausted.
	// No session-based pause/resume — the worker keeps producing segments regardless
	// of playback pauses, avoiding encoder re-initialization cost.
	w.monitorHLSSegments(ctx, m3u8Path, job.FileID, job.Bitrate)

	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			logger.Info("Continuous HLS ffmpeg cancelled")
		} else {
			logger.Warn("Continuous HLS ffmpeg exited", zap.Error(err))
		}
	}
	_ = outLog.flush()
	_ = errLog.flush()
}

// buildVideoEncoderArgsCtn builds video encoder arguments for continuous HLS mode.
// It aligns with the per-segment buildTranscodeArgs but without -ss/-t input trimming,
// since the continuous process handles timing via the segment muxer.
func (w *TranscodeWorker) buildVideoEncoderArgsCtn(job *ContinuousHLSJob, segDuration float64) []string {
	ctx := context.Background()

	// Read codec preference from Redis metadata.
	pickedBitrate := job.Bitrate
	codec := strings.TrimSpace(w.hwEncoderCodec(ctx, job.FileID))
	if codec == "libx264" {
		w.hwEncoder = hwenc.Libx264
	}

	res := resolutionForBitrate(pickedBitrate)
	wPx, hPx := parseResolutionWH(res)

	gops := []string{"-g", fmt.Sprintf("%d", int(segDuration*8)), "-keyint_min", fmt.Sprintf("%d", int(segDuration*8)), "-sc_threshold", "0"}

	switch w.hwEncoder {
	case hwenc.H264QSV:
		return append([]string{
			"-vf", fmt.Sprintf("scale=%s:%s,format=nv12", wPx, hPx),
			"-c:v", "h264_qsv",
			"-preset", "medium",
			"-b:v", pickedBitrate, "-maxrate", pickedBitrate, "-bufsize", "2M",
			"-profile:v", "high",
		}, append(gops)...)
	case hwenc.H264AMF:
		return append([]string{
			"-vf", fmt.Sprintf("scale=%s:%s", wPx, hPx),
			"-c:v", "h264_amf",
			"-quality", "balanced",
			"-b:v", pickedBitrate, "-maxrate", pickedBitrate, "-bufsize", "2M",
			"-profile:v", "high",
		}, append(gops)...)
	case hwenc.H264NVENC:
		return append([]string{
			"-vf", fmt.Sprintf("scale=%s:%s", wPx, hPx),
			"-c:v", "h264_nvenc",
			"-preset", "p4",
			"-b:v", pickedBitrate, "-maxrate", pickedBitrate, "-bufsize", "2M",
			"-profile:v", "high",
		}, append(gops)...)
	case hwenc.H264VAAPI:
		return append([]string{
			"-vf", fmt.Sprintf("scale=%s:%s,format=nv12", wPx, hPx),
			"-c:v", "h264_vaapi",
			"-b:v", pickedBitrate, "-maxrate", pickedBitrate, "-bufsize", "2M",
			"-profile:v", "high",
		}, append(gops)...)
	default:
		return append([]string{
			"-vf", fmt.Sprintf("scale=%s:%s", wPx, hPx),
			"-c:v", "libx264",
			"-preset", "medium",
			"-b:v", pickedBitrate, "-maxrate", pickedBitrate, "-bufsize", "2M",
			"-profile:v", "high",
		}, append(gops)...)
	}
}

// hwEncoderCodec reads the preferred codec for a file from Redis.
func (w *TranscodeWorker) hwEncoderCodec(ctx context.Context, fileID string) string {
	c, _ := w.redis.HGet(ctx, "video:meta:"+fileID, "codec").Result()
	return strings.TrimSpace(c)
}

// monitorHLSSegments polls the segment m3u8 manifest and publishes segment readiness to Redis
// as soon as each TS file appears on disk.
func (w *TranscodeWorker) monitorHLSSegments(ctx context.Context, m3u8Path, fileID, bitrate string) {
	tk := time.NewTicker(500 * time.Millisecond)
	defer tk.Stop()

	lastReportedSeg := -1

	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}

		entries := parseSegmentM3U8(m3u8Path)
		for _, seg := range entries {
			if seg.ID > lastReportedSeg {
				tsPath := fmt.Sprintf("ts/video/%s/%s/%d.ts", fileID, bitrate, seg.ID)
				if w.storage.FileExists(tsPath) {
					w.updateSegmentStatus(fileID, seg.ID, bitrate, "ready")
					lastReportedSeg = seg.ID
					// Update latest_seg in Redis so scheduler knows current range.
					w.redis.HSet(ctx, continuousJobKey(fileID, bitrate), "latest_seg", seg.ID)
				}
			}
		}
	}
}

// getSegmentIndexRedis reads the video segment index from Redis.
func (w *TranscodeWorker) getSegmentIndexRedis(fileID string) (*models.SegmentIndex, error) {
	ctx := context.Background()
	raw, err := w.redis.Get(ctx, "video:index:"+fileID).Result()
	if err != nil {
		return nil, err
	}
	var idx models.SegmentIndex
	if err := json.Unmarshal([]byte(raw), &idx); err != nil {
		return nil, err
	}
	return &idx, nil
}

// formatSeconds produces an H:M:S.ms string suitable for ffmpeg -ss.
func formatSeconds(sec float64) string {
	h := int(sec) / 3600
	m := (int(sec) % 3600) / 60
	s := sec - float64(h*3600+m*60)
	return fmt.Sprintf("%02d:%02d:%06.3f", h, m, s)
}

// segmentM3U8Entry represents one EXTINF entry in a segment m3u8 playlist.
type segmentM3U8Entry struct {
	ID       int
	Duration float64
	File     string
}

// parseSegmentM3U8 parses a simple ffmpeg segment_list m3u8 file.
func parseSegmentM3U8(path string) []segmentM3U8Entry {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var entries []segmentM3U8Entry
	lines := strings.Split(string(data), "\n")
	var dur float64
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#EXTINF:") {
			s := strings.TrimPrefix(line, "#EXTINF:")
			s = strings.TrimSuffix(s, ",")
			dur, _ = strconv.ParseFloat(s, 64)
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Segment filename: typically "segID.ts"
		base := filepath.Base(line)
		idStr := strings.TrimSuffix(base, ".ts")
		if id, err := strconv.Atoi(idStr); err == nil {
			entries = append(entries, segmentM3U8Entry{ID: id, Duration: dur, File: line})
		}
		dur = 0
	}
	return entries
}
