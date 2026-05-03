// cmd/sliceworker/main.go
package sliceworker

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
	return &SliceWorker{
		redis:    redis.NewClient(&redis.Options{Addr: cfg.RedisAddr}),
		storage:  NewStorage(cfg.StoragePath),
		ffmpeg:   cfg.FFmpegPath,
		logger:   zap.L(),
		workerID: cfg.WorkerID,
	}
}

func (w *SliceWorker) Start() {
	ctx := context.Background()

	// 订阅切片任务
	pubsub := w.redis.Subscribe(ctx, "slice:jobs")
	defer pubsub.Close()

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
		w.markSliceFailed(task.FileID)
		return
	}

	// 2. 分析视频（获取关键帧、时长等）
	videoInfo, err := w.analyzeVideo(videoPath)
	if err != nil {
		logger.Error("Failed to analyze video", zap.Error(err))
		w.markSliceFailed(task.FileID)
		return
	}

	// 3. 生成虚拟视频分段索引；音频仍一次性物理切片（segment muxer + overlap，避免逐段 -ss/-t 不连续）
	index, err := w.generateSegmentIndex(task.FileID, videoInfo)
	if err != nil {
		logger.Error("Failed to generate segment index", zap.Error(err))
		w.markSliceFailed(task.FileID)
		return
	}

	if len(index.AudioSegments) > 0 {
		if err := w.sliceAudio(task.FileID, videoPath, index); err != nil {
			logger.Error("Failed to slice audio", zap.Error(err))
			w.markSliceFailed(task.FileID)
			return
		}
	}

	// 5. 保存索引到 Redis
	if err := w.saveIndex(task.FileID, index); err != nil {
		logger.Error("Failed to save index", zap.Error(err))
		w.markSliceFailed(task.FileID)
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
	cmd := exec.Command(w.ffmpeg,
		"-i", videoPath,
		"-show_format",
		"-show_streams",
		"-v", "quiet",
		"-print_format", "json",
	)

	output, err := cmd.Output()
	if err != nil {
		return nil, err
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

	// 获取关键帧位置
	keyframes, err := w.getKeyframes(videoPath)
	if err == nil {
		info.Keyframes = keyframes
	}

	return info, nil
}

func (w *SliceWorker) getKeyframes(videoPath string) ([]float64, error) {
	cmd := exec.Command(w.ffmpeg,
		"-i", videoPath,
		"-select_streams", "v:0",
		"-show_frames",
		"-show_entries", "frame=pkt_pts_time,key_frame",
		"-v", "quiet",
		"-print_format", "json",
	)

	output, err := cmd.Output()
	if err != nil {
		return nil, err
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

// sliceAudio 单次 ffmpeg 流水线切分全部音频段（含 overlap），保证播放连续性。
func (w *SliceWorker) sliceAudio(fileID, videoPath string, index *models.SegmentIndex) error {
	outputDir := filepath.Join(w.storage.BasePath(), "raw", "audio", fileID)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}

	cmd := exec.Command(w.ffmpeg,
		"-i", videoPath,
		"-vn",
		"-c:a", "aac",
		"-b:a", "128k",
		"-af", "afade=t=in:st=0:d=0.05",
		"-f", "segment",
		"-segment_time", "6",
		"-segment_overlap_duration", "0.1",
		filepath.Join(outputDir, "segment_%05d.m4a"),
	)
	if err := cmd.Run(); err != nil {
		return err
	}
	for i := range index.AudioSegments {
		index.AudioSegments[i].Status = "sliced"
	}
	return nil
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

	// 更新状态
	w.redis.HSet(ctx, "video:meta:"+fileID, "status", "ready")

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
}

func (w *SliceWorker) markSliceFailed(fileID string) {
	ctx := context.Background()
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
