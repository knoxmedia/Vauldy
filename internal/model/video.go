// internal/models/video.go
package models

import "time"

// 视频元数据
type VideoMetadata struct {
	FileID          string    `json:"file_id"`
	FilePath        string    `json:"file_path"`
	Duration        float64   `json:"duration"`
	Width           int       `json:"width"`
	Height          int       `json:"height"`
	Bitrate         int       `json:"bitrate"`
	Codec           string    `json:"codec"`
	AudioCodec      string    `json:"audio_codec"`
	Format          string    `json:"format"`
	Size            int64     `json:"size"`
	AudioPlaylists []AudioPlaylistInfo `json:"audio_playlists,omitempty"`
	CreatedAt      time.Time           `json:"created_at"`
}

// AudioPlaylistInfo describes one external audio HLS playlist for the master playlist.
type AudioPlaylistInfo struct {
	Index    int    `json:"index"`
	Language string `json:"language"`
	Codec    string `json:"codec"`
	URL      string `json:"url"`
}

// 切片索引（存储在 Redis）
type SegmentIndex struct {
	FileID        string             `json:"file_id"`
	Status        string             `json:"status"` // slicing, ready, failed
	TotalSegments int                `json:"total_segments"`
	Duration      float64            `json:"duration"`
	KeyframePTS   []float64          `json:"keyframe_pts,omitempty"` // 源视频关键帧时间戳（秒）
	VideoSegments []VideoSegmentInfo `json:"video_segments"`
	AudioSegments []AudioSegmentInfo `json:"audio_segments"`
	CreatedAt     time.Time          `json:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
}

type VideoSegmentInfo struct {
	ID        int     `json:"id"`
	StartTime float64 `json:"start_time"`
	EndTime   float64 `json:"end_time"`
	Duration  float64 `json:"duration"`
	Keyframe  bool    `json:"keyframe"`
	SlicePath string  `json:"slice_path,omitempty"` // 非空：旧版物理切片 MKV；空：虚拟切片（源文件 + 时间）
	Status    string  `json:"status"`               // indexed=虚拟已就绪, sliced=物理切片就绪, pending/slicing/failed
	WorkerID  string  `json:"worker_id,omitempty"`
}

type AudioSegmentInfo struct {
	ID        int     `json:"id"`
	StartTime float64 `json:"start_time"`
	EndTime   float64 `json:"end_time"`
	Duration  float64 `json:"duration"`
	Overlap   float64 `json:"overlap"`
	Language  string  `json:"language"`
	SlicePath string  `json:"slice_path,omitempty"` // 入库一次性 ffmpeg segment 输出路径（相对存储根）
	Status    string  `json:"status"`               // pending → sliced；无音轨时不生成音频段
}

// 转码任务
type TranscodeTask struct {
	FileID     string `json:"file_id"`
	SegmentID  int    `json:"segment_id"`
	Bitrate    string `json:"bitrate"`
	Resolution string `json:"resolution"`
	Codec      string `json:"codec"`            // libx264 forces software; empty = worker auto (QSV/AMF/NVENC/VAAPI/libx264)
	Preset     string `json:"preset,omitempty"` // libx264/x264-style preset hint; mapped for HW encoders
	SessionID  string `json:"session_id"`
	Priority   int    `json:"priority"`
	CreatedAt  int64  `json:"created_at"`
}

// 切片任务
type SliceTask struct {
	FileID    string `json:"file_id"`
	SessionID string `json:"session_id"`
	CreatedAt int64  `json:"created_at"`
}
