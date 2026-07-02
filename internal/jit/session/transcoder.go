package session

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"knox-media/internal/jit/hwenc"
	"knox-media/internal/storage"
)

func seekTimeForTranscode(s *Session, targetSec float64, in *storage.FFmpegInput) float64 {
	if targetSec <= 0.01 {
		return targetSec
	}
	// Decrypt pipe must start at byte 0 so ffmpeg reads the container header (moov).
	// Byte-offset pipe seek skips moov and breaks MP4 demux even with faststart layout.
	if in != nil && (in.FromEnc || in.PlainFallback) {
		return plainFileSeekPlan(s, targetSec)
	}
	return targetSec
}

func plainFileSeekPlan(s *Session, targetSec float64) float64 {
	if targetSec <= 0.01 || s == nil || s.mgr == nil {
		return targetSec
	}
	kf := s.mgr.loadKeyframeMeta(s)
	if kf == nil || len(kf.PTS) == 0 {
		return targetSec
	}
	idx := sort.Search(len(kf.PTS), func(i int) bool { return kf.PTS[i] > targetSec }) - 1
	if idx < 0 {
		idx = 0
	}
	return kf.PTS[idx]
}

// TranscodeConfig holds the parameters for session-scoped ffmpeg.
type TranscodeConfig struct {
	SourcePath       string
	Bitrate          string
	Resolution       string
	AudioCodec       string  // source audio codec (aac/eac3/...)
	AudioPlaylistURL string  // if set, skip audio encoding (-an)
	StartTime        float64 // seek time in seconds (0 = from beginning)
	X264Preset       string  // libx264 -preset; empty = veryfast
	CRF              int     // libx264 -crf; 0 = default 23
	UseHWEncoding    bool    // from system options enable_hardware_encoding
	VideoEncoder     hwenc.ID
}

// StartTranscode launches ffmpeg in a goroutine for the session.
func (s *Session) StartTranscode(cfg TranscodeConfig) {
	go func() {
		defer s.SignalDone()
		if err := s.runTranscode(cfg); err != nil {
			zap.L().Warn("session transcode exited",
				zap.String("session_id", s.ID),
				zap.Error(err),
			)
		}
	}()
}

func (s *Session) runTranscode(cfg TranscodeConfig) error {
	s.runMu.Lock()
	defer s.runMu.Unlock()

	ctx := s.Ctx()

	logger := zap.L().With(
		zap.String("session_id", s.ID),
		zap.String("source", cfg.SourcePath),
		zap.String("bitrate", cfg.Bitrate),
	)

	attempts := transcodeAttempts(cfg)
	var lastErr error
	for i, attempt := range attempts {
		if i > 0 {
			logger.Info("session transcode retry with software encoder",
				zap.String("previous_encoder", string(attempts[i-1].VideoEncoder)),
			)
		}
		ffmpegIn, ffmpegInErr := s.openTranscodeInput(cfg.SourcePath)
		if ffmpegInErr != nil {
			return ffmpegInErr
		}
		err := s.runTranscodeOnce(ctx, attempt, ffmpegIn, logger)
		if ffmpegIn != nil && ffmpegIn.Cleanup != nil {
			ffmpegIn.Cleanup()
		}
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("cancelled: %w", ctx.Err())
			}
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}

func (s *Session) openTranscodeInput(sourcePath string) (*storage.FFmpegInput, error) {
	if s.mgr != nil && s.mgr.DB != nil && s.mgr.Vault != nil {
		in, err := storage.OpenFFmpegInput(s.mgr.DB, s.mgr.Vault, s.MediaID, sourcePath, 0)
		if err != nil {
			return nil, err
		}
		return in, nil
	}
	return &storage.FFmpegInput{Path: sourcePath}, nil
}

func transcodeAttempts(cfg TranscodeConfig) []TranscodeConfig {
	if !cfg.UseHWEncoding || cfg.VideoEncoder == "" || cfg.VideoEncoder == hwenc.Libx264 {
		sw := cfg
		sw.UseHWEncoding = false
		sw.VideoEncoder = hwenc.Libx264
		return []TranscodeConfig{sw}
	}
	sw := cfg
	sw.UseHWEncoding = false
	sw.VideoEncoder = hwenc.Libx264
	return []TranscodeConfig{cfg, sw}
}

func (s *Session) runTranscodeOnce(ctx context.Context, cfg TranscodeConfig, ffmpegIn *storage.FFmpegInput, logger *zap.Logger) error {
	segDuration := JITSegmentDurationSeconds
	startSeg := s.NextSegmentToEmit()

	args := []string{"-hide_banner", "-loglevel", "error"}
	localFile := strings.TrimSpace(ffmpegIn.Path) != "" && !ffmpegIn.FromEnc
	pipeline := hwenc.PipelineModeForInput(cfg.UseHWEncoding, localFile)
	if pipeline == hwenc.PipelineHWFull {
		args = append(args, hwenc.InputAccelArgs(cfg.VideoEncoder)...)
	}

	seekTime := seekTimeForTranscode(s, cfg.StartTime, ffmpegIn)
	if seekTime > 0.01 {
		args = append(args, "-y", "-copyts", "-start_at_zero")
		args = append(args, "-ss", formatSeconds(seekTime))
	}
	var stdin io.Reader
	args, stdin = storage.ApplyFFmpegInput(args, ffmpegIn)

	// Video stream selection.
	args = append(args, "-map", "0:v:0")

	// Audio: include unless pre-extracted audio is available.
	if strings.TrimSpace(cfg.AudioPlaylistURL) == "" && strings.TrimSpace(cfg.AudioCodec) != "" {
		args = append(args, "-map", "0:a:0?")
	} else {
		args = append(args, "-an")
	}
	args = append(args, "-sn")

	videoPlan := hwenc.InstantVideoPlan{
		Encoder:    cfg.VideoEncoder,
		Mode:       pipeline,
		Resolution: cfg.Resolution,
		Bitrate:    cfg.Bitrate,
		X264Preset: cfg.X264Preset,
		CRF:        cfg.CRF,
		SessionGOP: true,
	}
	if !cfg.UseHWEncoding {
		videoPlan.Encoder = hwenc.Libx264
		videoPlan.Mode = hwenc.PipelineSoftware
	}
	args = append(args, hwenc.BuildInstantVideoArgs(videoPlan)...)

	// Audio encoder args.
	if strings.TrimSpace(cfg.AudioPlaylistURL) == "" && strings.TrimSpace(cfg.AudioCodec) != "" {
		if strings.ToLower(cfg.AudioCodec) == "aac" {
			args = append(args, "-c:a", "copy", "-bsf:a", "aac_adtstoasc")
		} else {
			args = append(args, "-c:a", "aac", "-b:a", "128k", "-ac", "2", "-ar", "48000")
		}
	}

	// Segment muxer output (mpegts + m3u8 list). Preserves stream PTS when combined with
	// -copyts -start_at_zero -ss before -i on mid-stream starts; -reset_timestamps 0 keeps
	// continuity across segment files instead of zeroing each fragment.
	m3u8Path := filepath.Join(s.TempDir, "master.m3u8")
	// FFmpeg on Windows accepts forward slashes; segment_write_temp writes each .ts via a temp name then renames.
	segPattern := filepath.ToSlash(filepath.Join(s.TempDir, "%d.ts"))

	if s.StreamEncryption != nil && strings.TrimSpace(s.StreamEncryption.KeyInfoPath) != "" {
		hlsArgs := []string{
			"-max_delay", "5000000",
			"-avoid_negative_ts", "disabled",
			"-hls_key_info_file", filepath.ToSlash(s.StreamEncryption.KeyInfoPath),
			"-f", "hls",
			"-hls_time", fmt.Sprintf("%.3f", segDuration),
			"-hls_list_size", "0",
			"-hls_playlist_type", "segment", // this is required for the segment muxer to work
			"-hls_flags", "append_list+omit_endlist+temp_file",
		}
		if startSeg > 0 {
			// HLS muxer exposes this as -start_number (not -hls_start_number) on common FFmpeg builds.
			hlsArgs = append(hlsArgs, "-start_number", strconv.Itoa(startSeg))
		}
		hlsArgs = append(hlsArgs,
			"-hls_segment_filename", segPattern,
			"-y",
			filepath.ToSlash(m3u8Path),
		)
		args = append(args, hlsArgs...)
	} else {
		args = append(args,
			"-max_delay", "5000000",
			"-avoid_negative_ts", "disabled",
			"-f", "segment",
			"-segment_format", "mpegts",
			"-segment_list", filepath.ToSlash(m3u8Path),
			"-segment_list_type", "m3u8",
			"-segment_time", fmt.Sprintf("%.3f", segDuration),
			"-segment_start_number", strconv.Itoa(startSeg),
			"-individual_header_trailer", "0",
			"-write_header_trailer", "0",
			"-segment_write_temp", "1", // this is required for the segment muxer to work
			"-y",
			segPattern,
		)
	}

	logger.Info("starting session ffmpeg",
		zap.String("encoder", string(videoPlan.Encoder)),
		zap.String("pipeline", pipelineName(pipeline)),
		zap.String("args", strings.Join(args, " ")),
	)

	ffmpeg := s.ffmpegPath
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	s.SetCmd(cmd)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ffmpeg start: %w", err)
	}

	go s.monitorSegments(ctx, m3u8Path)

	err := cmd.Wait()
	if err != nil {
		return fmt.Errorf("ffmpeg exited: %w", err)
	}
	return nil
}

func pipelineName(mode hwenc.PipelineMode) string {
	switch mode {
	case hwenc.PipelineHWFull:
		return "hw_full"
	case hwenc.PipelineHWEncodeOnly:
		return "hw_encode_only"
	default:
		return "software"
	}
}

// monitorSegments polls the segment m3u8 and updates LatestSeg.
func (s *Session) monitorSegments(ctx context.Context, m3u8Path string) {
	tk := time.NewTicker(200 * time.Millisecond)
	defer tk.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			entries := parseSegmentM3U8(m3u8Path)
			for _, e := range entries {
				s.SetLatestSeg(e.ID)
				break
			}
		}
	}
}

// runScheduler applies background rules 1, 2, 6 independent of HTTP requests.
func (s *Session) runScheduler() {
	latest := s.LatestSegment()
	req := s.LastRequestedSeg()
	idle := s.TimeSinceLastRequest()

	// Rule 6: session timeout — no request ≥ 120s → full cleanup.
	if idle >= 120*time.Second {
		zap.L().Info("jit session timed out, cancelling",
			zap.String("session_id", s.ID),
			zap.Duration("idle", idle),
		)
		if s.mgr != nil {
			s.mgr.CancelSession(s.ID)
		} else {
			s.cancel()
		}
		return
	}

	// Rule 2: too far ahead — ffmpeg leading by ≥20 segments → pause.
	if latest-req >= 20 {
		zap.L().Info("jit session far ahead, pausing",
			zap.String("session_id", s.ID),
			zap.Int("latest", latest),
			zap.Int("requested", req),
		)
		s.pause()
		return
	}

	// Rule 1: idle playback — ≥30s since last request and way ahead → pause.
	if idle >= 30*time.Second && latest > req+2 {
		zap.L().Info("jit session idle, pausing",
			zap.String("session_id", s.ID),
			zap.Duration("idle", idle),
			zap.Int("latest", latest),
			zap.Int("requested", req),
		)
		s.pause()
	}
}

// schedulerLoop runs rules 1, 2, and 6 on a fixed interval for the whole time the session
// is registered with the manager. It survives pause/seek (ctx replacement) and periods
// without a running ffmpeg; monitorSegments only covers one ffmpeg run.
func (s *Session) schedulerLoop() {
	const tick = 5 * time.Second
	staleCtxWaits := 0
	for {
		if s.mgr != nil && s.mgr.Get(s.ID) != s {
			return
		}
		ctx := s.Ctx()
		if ctx.Err() != nil {
			staleCtxWaits++
			if s.mgr == nil && staleCtxWaits > 40 {
				return
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		staleCtxWaits = 0

		tk := time.NewTicker(tick)
		func() {
			defer tk.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tk.C:
					s.runScheduler()
				}
			}
		}()
	}
}

type m3u8Entry struct {
	ID       int
	Duration float64
}

// parseSegmentM3U8 reads ffmpeg -segment_list m3u8 and returns the last completed segment
// (from the end: last segment URI, then the preceding #EXTINF). Growing playlists can be
// large; scanning only the tail avoids parsing the entire file each poll.
func parseSegmentM3U8(path string) []m3u8Entry {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		base := line
		if idx := strings.LastIndexByte(line, '/'); idx >= 0 {
			base = line[idx+1:]
		}
		idStr := strings.TrimSuffix(base, ".ts")
		segID, err := strconv.Atoi(idStr)
		if err != nil {
			return nil
		}
		for j := i - 1; j >= 0; j-- {
			prev := strings.TrimSpace(lines[j])
			if prev == "" {
				continue
			}
			if strings.HasPrefix(prev, "#EXTINF:") {
				s := strings.TrimPrefix(prev, "#EXTINF:")
				s = strings.TrimSuffix(s, ",")
				dur, _ := strconv.ParseFloat(s, 64)
				return []m3u8Entry{{ID: segID, Duration: dur}}
			}
			if !strings.HasPrefix(prev, "#") {
				return nil
			}
		}
		return nil
	}
	return nil
}

// formatSeconds produces an H:M:S.ms string suitable for ffmpeg -ss.
func formatSeconds(sec float64) string {
	h := int(sec) / 3600
	m := (int(sec) % 3600) / 60
	s := sec - float64(h*3600+m*60)
	return fmt.Sprintf("%02d:%02d:%06.3f", h, m, s)
}

// Platform-specific implementations in pause_linux.go / pause_windows.go.
// On unsupported platforms these are no-ops.
