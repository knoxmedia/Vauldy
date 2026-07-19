package atomicfile

import (
	"context"
	"os"
	"path/filepath"
)

func WriteFile(ctx context.Context, target string, data []byte, mode os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".tmp-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err = tmp.Chmod(mode); err == nil {
		_, err = tmp.Write(data)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return ReplaceFile(tmpPath, target)
}

type StagedFile struct {
	temp, target string
	published    bool
}

func Stage(ctx context.Context, target string, data []byte, mode os.FileMode) (*StagedFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".tmp-")
	if err != nil {
		return nil, err
	}
	temp := tmp.Name()
	if err = tmp.Chmod(mode); err == nil {
		_, err = tmp.Write(data)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(temp)
		return nil, err
	}
	return &StagedFile{temp: temp, target: target}, nil
}
func (s *StagedFile) Publish(ctx context.Context) error {
	if s == nil {
		return os.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ReplaceFile(s.temp, s.target); err != nil {
		return err
	}
	s.published = true
	return nil
}
func (s *StagedFile) Commit() {
	if s != nil {
		s.published = false
		s.temp = ""
	}
}
func (s *StagedFile) Rollback() {
	if s == nil {
		return
	}
	if s.published {
		_ = os.Remove(s.target)
	}
	if s.temp != "" {
		_ = os.Remove(s.temp)
	}
	s.published = false
	s.temp = ""
}
