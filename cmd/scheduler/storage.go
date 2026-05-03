package scheduler

import (
	"fmt"
	"os"
	"path/filepath"

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
