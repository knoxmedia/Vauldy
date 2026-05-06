package scheduler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	models "knox-media/internal/model"
)

type LocalStorage struct {
	basePath string
}

func NewLocalStorage(basePath string) *LocalStorage {
	return &LocalStorage{basePath: basePath}
}

func (s *LocalStorage) BasePath() string {
	return s.basePath
}

func (s *LocalStorage) resolve(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(s.basePath, path)
}

func (s *LocalStorage) FileExists(path string) bool {
	_, err := os.Stat(s.resolve(path))
	return err == nil
}

func (s *LocalStorage) GetFileInfo(path string) (*models.VideoMetadata, error) {
	info, err := os.Stat(s.resolve(path))
	if err != nil {
		return nil, err
	}
	return &models.VideoMetadata{
		FilePath: path,
		Size:     info.Size(),
	}, nil
}

func (s *LocalStorage) GetSegmentPath(fileID string, segID int, segmentType string) string {
	switch segmentType {
	case "video":
		return filepath.Join(s.basePath, "raw", "video", fileID, fmt.Sprintf("segment_%05d.mkv", segID))
	case "audio":
		return filepath.Join(s.basePath, "raw", "audio", fileID, fmt.Sprintf("segment_%05d.m4a", segID))
	default:
		return filepath.Join(s.basePath, segmentType, fileID, fmt.Sprintf("segment_%05d", segID))
	}
}

func (s *LocalStorage) SaveSegment(fileID string, segID int, segmentType string, data []byte) error {
	// JIT video: <base>/ts/video/<fileID>/<bitrate>/<seg>.ts (same as handleVideoSegment, LoadSegment)
	if strings.HasPrefix(segmentType, "ts/video/") {
		br := strings.TrimPrefix(segmentType, "ts/video/")
		targetDir := filepath.Join(s.basePath, "ts", "video", fileID, br)
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			return err
		}
		targetPath := filepath.Join(targetDir, fmt.Sprintf("%d.ts", segID))
		return os.WriteFile(targetPath, data, 0o644)
	}
	targetDir := filepath.Join(s.basePath, segmentType, fileID)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	targetPath := filepath.Join(targetDir, fmt.Sprintf("%d.ts", segID))
	return os.WriteFile(targetPath, data, 0o644)
}

func (s *LocalStorage) LoadSegment(fileID string, segID int, segmentType string, variant string) ([]byte, error) {
	var p string
	if segmentType == "audio" {
		p = filepath.Join(s.basePath, "raw", "audio", fileID, fmt.Sprintf("segment_%05d.m4a", segID))
	} else {
		p = filepath.Join(s.basePath, "ts", "video", fileID, variant, fmt.Sprintf("%d.ts", segID))
	}
	return os.ReadFile(p)
}

// CleanupFile 删除该 fileID 下的全部即时转码产物：
//   - <base>/ts/video/<fileID>/...     转码后的 .ts 切片
//   - <base>/raw/audio/<fileID>/...    旧路径预切音频（兼容）
//   - <base>/raw/video/<fileID>/...    旧路径 MKV 切片（兼容）
//
// 不影响别的 fileID，也不影响关键帧缓存。在 EndSession 中无活跃会话时调用。
func (s *LocalStorage) CleanupFile(fileID string) error {
	if s == nil || strings.TrimSpace(fileID) == "" {
		return nil
	}
	dirs := []string{
		filepath.Join(s.basePath, "ts", "video", fileID),
		filepath.Join(s.basePath, "raw", "audio", fileID),
		filepath.Join(s.basePath, "raw", "video", fileID),
	}
	var firstErr error
	for _, d := range dirs {
		if err := os.RemoveAll(d); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
