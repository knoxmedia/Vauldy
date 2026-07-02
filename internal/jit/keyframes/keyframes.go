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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
// Pos 与 PTS 等长时为明文流字节偏移（ffprobe packet.pos），用于加密源 JIT seek 时 pipe 起点优化。
type Meta struct {
	FileID    string    `json:"file_id"`
	FilePath  string    `json:"file_path"`
	SrcMTime  int64     `json:"src_mtime"`
	SrcSize   int64     `json:"src_size"`
	Duration  float64   `json:"duration"`
	PTS       []float64 `json:"pts"`
	Pos       []int64   `json:"pos,omitempty"`
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

// FilePath returns the on-disk JSON cache path for fileID.
func (c *Cache) FilePath(fileID string) string {
	return c.path(fileID)
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
	return c.load(fileID, srcPath, 0, nil, nil)
}

// LoadForMedia loads cache including Knox .enc derived keyframe metadata.
func (c *Cache) LoadForMedia(db *sql.DB, vault *keystore.Vault, mediaID int64, fileID, srcPath string) (*Meta, error) {
	return c.load(fileID, srcPath, mediaID, db, vault)
}

func (c *Cache) load(fileID, srcPath string, mediaID int64, db *sql.DB, vault *keystore.Vault) (*Meta, error) {
	if c == nil {
		return nil, nil
	}
	raw, err := c.readRaw(fileID, mediaID, db, vault)
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

func (c *Cache) readRaw(fileID string, mediaID int64, db *sql.DB, vault *keystore.Vault) ([]byte, error) {
	if raw, err := os.ReadFile(c.path(fileID)); err == nil {
		return raw, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if db != nil && mediaID > 0 {
		logical := sanitizeFileID(fileID) + ".json"
		if enc, ok := storage.LookupEncPath(db, mediaID, "keyframe_meta", logical); ok {
			seeker, err := storage.OpenDerivedSeeker(db, vault, mediaID, enc)
			if err != nil {
				return nil, err
			}
			defer seeker.Close()
			return io.ReadAll(seeker)
		}
	}
	return nil, os.ErrNotExist
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
	probePath := storage.ResolveKeyframeProbePath(db, mediaID, srcPath)
	var pts []float64
	var pos []int64
	if storage.InputNeedsPipe(db, mediaID, probePath) {
		out, cleanup, perr := storage.FFprobeOutput(db, vault, c.FFprobePath, mediaID, probePath, 0, duration, []string{
			"-v", "error",
			"-select_streams", "v:0",
			"-show_packets",
			"-show_entries", "packet=pts_time,pos,flags",
			"-of", "csv=print_section=0",
		})
		if cleanup != nil {
			defer cleanup()
		}
		if perr != nil {
			return nil, perr
		}
		pts, pos = entriesToPTSPos(parseKeyframePacketEntries(string(out)))
	} else {
		var perr error
		pts, pos, perr = probeKeyframeEntries(ctx, c.FFprobePath, probePath)
		if perr != nil {
			return nil, perr
		}
	}
	return &Meta{
		FileID:   fileID,
		FilePath: srcPath,
		SrcMTime: st.ModTime().Unix(),
		SrcSize:  st.Size(),
		Duration: duration,
		PTS:      pts,
		Pos:      pos,
	}, nil
}

// Extract probes the source file and returns the keyframe PTS list. Costly for long videos
// so callers should cache the result via Save().
//
// 实现：使用 `ffprobe -select_streams v:0 -show_packets -show_entries packet=pts_time,pos,flags`
// 解析出 flags 含 'K' 的包的 pts_time 与 pos（明文字节偏移）。
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
	pts, pos, err := probeKeyframeEntries(ctx, c.FFprobePath, srcPath)
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
		Pos:      pos,
	}, nil
}

func probeKeyframeEntries(ctx context.Context, ffprobe, srcPath string) (pts []float64, pos []int64, err error) {
	cmd := exec.CommandContext(ctx, ffprobe,
		"-v", "error",
		"-select_streams", "v:0",
		"-show_packets",
		"-show_entries", "packet=pts_time,pos,flags",
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
			return nil, nil, fmt.Errorf("ffprobe show_packets failed: %v: %s", err, stderr)
		}
		return nil, nil, fmt.Errorf("ffprobe show_packets failed: %w", err)
	}
	pts, pos = entriesToPTSPos(parseKeyframePacketEntries(string(out)))
	return pts, pos, nil
}

func entriesToPTSPos(entries []packetEntry) (pts []float64, pos []int64) {
	if len(entries) == 0 {
		return nil, nil
	}
	pts = make([]float64, len(entries))
	pos = make([]int64, len(entries))
	for i, e := range entries {
		pts[i] = e.PTS
		pos[i] = e.Pos
	}
	return pts, pos
}

// PlanEncryptedSeek picks the nearest keyframe at or before targetSec. Byte offsets are indexed
// for metadata only; MP4 decrypt pipes must start at 0 and seek by time (see session/transcoder).
func (m *Meta) PlanEncryptedSeek(targetSec float64) (pipeOffset int64, ffmpegSS float64, ok bool) {
	if m == nil || len(m.PTS) == 0 || targetSec <= 0.01 || !m.hasPosIndex() {
		return 0, targetSec, false
	}
	idx := sort.Search(len(m.PTS), func(i int) bool { return m.PTS[i] > targetSec }) - 1
	if idx < 0 {
		idx = 0
	}
	if m.Pos[idx] < 0 {
		return 0, targetSec, false
	}
	ffmpegSS = targetSec - m.PTS[idx]
	if ffmpegSS < 0 {
		ffmpegSS = 0
	}
	return m.Pos[idx], ffmpegSS, true
}

func (m *Meta) hasPosIndex() bool {
	if len(m.Pos) != len(m.PTS) || len(m.PTS) == 0 {
		return false
	}
	for _, p := range m.Pos {
		if p < 0 {
			return false
		}
	}
	return true
}

type packetEntry struct {
	PTS float64
	Pos int64
}

func probeKeyframes(ctx context.Context, ffprobe, srcPath string) ([]float64, error) {
	pts, _, err := probeKeyframeEntries(ctx, ffprobe, srcPath)
	return pts, err
}

// parseKeyframePacketEntries accepts CSV `pts_time,pos,flags` and returns keyframe rows.
func parseKeyframePacketEntries(s string) []packetEntry {
	out := make([]packetEntry, 0, 256)
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) < 2 {
			continue
		}
		ptsField := strings.TrimSpace(fields[0])
		posField := ""
		flags := ""
		if len(fields) == 2 {
			flags = strings.TrimSpace(fields[1])
		} else {
			posField = strings.TrimSpace(fields[1])
			flags = strings.TrimSpace(fields[2])
		}
		if !strings.ContainsAny(flags, "Kk") {
			continue
		}
		if ptsField == "" || ptsField == "N/A" {
			continue
		}
		pts, err := strconv.ParseFloat(ptsField, 64)
		if err != nil {
			continue
		}
		pos := int64(-1)
		if posField != "" && posField != "N/A" {
			if p, perr := strconv.ParseInt(posField, 10, 64); perr == nil {
				pos = p
			}
		}
		out = append(out, packetEntry{PTS: pts, Pos: pos})
	}
	return out
}

// parseKeyframePackets accepts legacy CSV `pts_time,flags` or `pts_time,pos,flags`.
func parseKeyframePackets(s string) []float64 {
	entries := parseKeyframePacketEntries(s)
	if len(entries) == 0 {
		// Legacy two-field lines without pos column.
		out := make([]float64, 0, 256)
		for _, line := range strings.Split(s, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
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
	pts := make([]float64, len(entries))
	for i, e := range entries {
		pts[i] = e.PTS
	}
	return pts
}

// EnsureCached returns cached keyframes or extracts + saves them now.
func (c *Cache) EnsureCachedForMedia(ctx context.Context, db *sql.DB, vault *keystore.Vault, mediaID int64, fileID, srcPath string, duration float64) (*Meta, error) {
	if c == nil {
		return nil, errors.New("keyframes: nil cache")
	}
	if got, err := c.LoadForMedia(db, vault, mediaID, fileID, srcPath); err == nil && got != nil && len(got.PTS) > 0 {
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
