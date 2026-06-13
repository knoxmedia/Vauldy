// Package keyframes extracts and caches video keyframe PTS lists used by the JIT scheduler
// to align HLS segment boundaries with source GOPs (required for `-c:v copy` passthrough and
// frame-accurate seeking).
//
// 性能：原 ffprobe `-show_frames` 路径需要解码每一帧，对 2 小时电影可达数分钟。
// 这里改用 `-show_packets` + `flags` 字段，只 demux 不解码，速度提升 10-50x。
// 结果以 JSON 形式落盘到 cacheDir/<file_id>.json，下次同 mtime+size 直接读取。
package keyframes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"knox-media/internal/keystore"
	"knox-media/internal/storage"
)

// Cache 表示一个本地关键帧列表缓存目录。
type Cache struct {
	Dir         string
	FFprobePath string
}

// Meta 是缓存条目结构。SrcMTime+SrcSize 作为缓存失效依据：源文件被替换 / 切片大小变化时丢弃缓存。
type Meta struct {
	FileID    string    `json:"file_id"`
	FilePath  string    `json:"file_path"`
	SrcMTime  int64     `json:"src_mtime"`
	SrcSize   int64     `json:"src_size"`
	Duration  float64   `json:"duration"`
	PTS       []float64 `json:"pts"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewCache 在 dir 下创建缓存目录（不存在则创建）。
func NewCache(dir string, ffprobePath string) (*Cache, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("keyframes: empty cache dir")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Cache{Dir: dir, FFprobePath: strings.TrimSpace(ffprobePath)}, nil
}

func (c *Cache) path(fileID string) string {
	return filepath.Join(c.Dir, sanitizeFileID(fileID)+".json")
}

func sanitizeFileID(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= '0' && ch <= '9',
			ch >= 'a' && ch <= 'z',
			ch >= 'A' && ch <= 'Z',
			ch == '-' || ch == '_' || ch == '.':
			out = append(out, ch)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

// Load returns cached entry if mtime+size match; nil if absent or stale.
func (c *Cache) Load(fileID, srcPath string) (*Meta, error) {
	if c == nil {
		return nil, nil
	}
	raw, err := os.ReadFile(c.path(fileID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if strings.TrimSpace(srcPath) != "" {
		st, err := os.Stat(srcPath)
		if err != nil {
			return nil, nil
		}
		if st.Size() != m.SrcSize || st.ModTime().Unix() != m.SrcMTime {
			return nil, nil
		}
	}
	return &m, nil
}

// Save persists meta to disk atomically (write tmp + rename).
func (c *Cache) Save(m *Meta) error {
	if c == nil || m == nil {
		return nil
	}
	if strings.TrimSpace(m.FileID) == "" {
		return errors.New("keyframes: empty file id")
	}
	m.UpdatedAt = time.Now()
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	final := c.path(m.FileID)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// ExtractForMedia probes keyframes, using decrypt pipe for Knox .enc when needed.
func (c *Cache) ExtractForMedia(ctx context.Context, db *sql.DB, vault *keystore.Vault, mediaID int64, fileID, srcPath string, duration float64) (*Meta, error) {
	if c == nil || strings.TrimSpace(c.FFprobePath) == "" {
		return nil, errors.New("keyframes: ffprobe path not configured")
	}
	if strings.TrimSpace(srcPath) == "" {
		return nil, errors.New("keyframes: empty source path")
	}
	st, err := os.Stat(srcPath)
	if err != nil {
		return nil, err
	}
	var pts []float64
	if storage.InputNeedsPipe(db, mediaID, srcPath) {
		out, cleanup, perr := storage.FFprobeOutput(db, vault, c.FFprobePath, mediaID, srcPath, 0, duration, []string{
			"-v", "error",
			"-select_streams", "v:0",
			"-show_packets",
			"-show_entries", "packet=pts_time,flags",
			"-of", "csv=print_section=0",
		})
		if cleanup != nil {
			defer cleanup()
		}
		if perr != nil {
			return nil, perr
		}
		pts = parseKeyframePackets(string(out))
	} else {
		pts, err = probeKeyframes(ctx, c.FFprobePath, srcPath)
		if err != nil {
			return nil, err
		}
	}
	return &Meta{
		FileID:   fileID,
		FilePath: srcPath,
		SrcMTime: st.ModTime().Unix(),
		SrcSize:  st.Size(),
		Duration: duration,
		PTS:      pts,
	}, nil
}

// Extract probes the source file and returns the keyframe PTS list. Costly for long videos
// so callers should cache the result via Save().
//
// 实现：使用 `ffprobe -select_streams v:0 -show_packets -show_entries packet=pts_time,flags`
// 解析出 flags 含 'K' 的包的 pts_time。`-show_packets` 不会解码任何帧，性能远好于 `-show_frames`.
func (c *Cache) Extract(ctx context.Context, fileID, srcPath string, duration float64) (*Meta, error) {
	if c == nil || strings.TrimSpace(c.FFprobePath) == "" {
		return nil, errors.New("keyframes: ffprobe path not configured")
	}
	if strings.TrimSpace(srcPath) == "" {
		return nil, errors.New("keyframes: empty source path")
	}
	st, err := os.Stat(srcPath)
	if err != nil {
		return nil, err
	}
	pts, err := probeKeyframes(ctx, c.FFprobePath, srcPath)
	if err != nil {
		return nil, err
	}
	return &Meta{
		FileID:   fileID,
		FilePath: srcPath,
		SrcMTime: st.ModTime().Unix(),
		SrcSize:  st.Size(),
		Duration: duration,
		PTS:      pts,
	}, nil
}

func probeKeyframes(ctx context.Context, ffprobe, srcPath string) ([]float64, error) {
	cmd := exec.CommandContext(ctx, ffprobe,
		"-v", "error",
		"-select_streams", "v:0",
		"-show_packets",
		"-show_entries", "packet=pts_time,flags",
		"-of", "csv=print_section=0",
		srcPath,
	)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		stderr := ""
		if errors.As(err, &ee) {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		if stderr != "" {
			return nil, fmt.Errorf("ffprobe show_packets failed: %v: %s", err, stderr)
		}
		return nil, fmt.Errorf("ffprobe show_packets failed: %w", err)
	}
	return parseKeyframePackets(string(out)), nil
}

// parseKeyframePackets accepts CSV lines `pts_time,flags` (e.g. `1.234000,K_`) and
// returns the pts_time values whose flag string contains 'K' (keyframe).
func parseKeyframePackets(s string) []float64 {
	out := make([]float64, 0, 256)
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// CSV format yields "pts_time,flags". Some packets have empty pts_time.
		comma := strings.IndexByte(line, ',')
		if comma < 0 {
			continue
		}
		ptsField := strings.TrimSpace(line[:comma])
		flags := strings.TrimSpace(line[comma+1:])
		if !strings.ContainsAny(flags, "Kk") {
			continue
		}
		if ptsField == "" || ptsField == "N/A" {
			continue
		}
		v, err := strconv.ParseFloat(ptsField, 64)
		if err != nil {
			continue
		}
		out = append(out, v)
	}
	return out
}

// EnsureCached returns cached keyframes or extracts + saves them now.
func (c *Cache) EnsureCachedForMedia(ctx context.Context, db *sql.DB, vault *keystore.Vault, mediaID int64, fileID, srcPath string, duration float64) (*Meta, error) {
	if c == nil {
		return nil, errors.New("keyframes: nil cache")
	}
	if got, err := c.Load(fileID, srcPath); err == nil && got != nil && len(got.PTS) > 0 {
		return got, nil
	}
	m, err := c.ExtractForMedia(ctx, db, vault, mediaID, fileID, srcPath, duration)
	if err != nil {
		return nil, err
	}
	if err := c.Save(m); err != nil {
		return m, err
	}
	return m, nil
}

// EnsureCached returns cached keyframes or extracts + saves them now.
// Uses srcPath stat to drive cache invalidation. Long videos may take seconds to extract;
// callers should run this in background where possible.
func (c *Cache) EnsureCached(ctx context.Context, fileID, srcPath string, duration float64) (*Meta, error) {
	if c == nil {
		return nil, errors.New("keyframes: nil cache")
	}
	if got, err := c.Load(fileID, srcPath); err == nil && got != nil && len(got.PTS) > 0 {
		return got, nil
	}
	m, err := c.Extract(ctx, fileID, srcPath, duration)
	if err != nil {
		return nil, err
	}
	if err := c.Save(m); err != nil {
		return m, err
	}
	return m, nil
}
