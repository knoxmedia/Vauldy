package subtitle

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"knox-media/internal/storage"
	"knox-media/pkg/ffprobe"
)

const ffmpegPipeInput = "pipe:0"

var asrAudioSuffixes = map[string]bool{
	".wav": true, ".mp3": true, ".flac": true, ".m4a": true,
	".aac": true, ".ogg": true, ".opus": true, ".wma": true,
}

// subtitleStreams probes embedded subtitle tracks, using decrypt pipe for Knox .enc.
func (s *Service) subtitleStreams(ctx context.Context, mediaID int64, videoPath string) ([]ffprobe.SubtitleStream, error) {
	if s == nil {
		return nil, fmt.Errorf("subtitle service nil")
	}
	if storage.InputNeedsPipe(s.DB, mediaID, videoPath) {
		out, cleanup, err := storage.FFprobeOutputContext(ctx, s.DB, s.Vault, s.FFprobePath, mediaID, videoPath, 0, 0, []string{
			"-v", "quiet",
			"-print_format", "json",
			"-show_streams",
		})
		if cleanup != nil {
			defer cleanup()
		}
		if err != nil {
			return nil, err
		}
		return ffprobe.ParseSubtitleStreamsJSON(out)
	}
	out, err := ffprobe.OutputContext(ctx, s.FFprobePath, []string{"-v", "quiet", "-print_format", "json", "-show_streams", videoPath}, nil)
	if err != nil {
		return nil, err
	}
	return ffprobe.ParseSubtitleStreamsJSON(out)
}

// openVideoPipeInput returns pipe:0 + stdin reader for encrypted video, or the plain path.
func (s *Service) openVideoPipeInput(mediaID int64, videoPath string) (input string, stdin io.Reader, cleanup func(), err error) {
	if !storage.InputNeedsPipe(s.DB, mediaID, videoPath) {
		return videoPath, nil, func() {}, nil
	}
	in, err := storage.OpenFFmpegInput(s.DB, s.Vault, mediaID, videoPath, 0)
	if err != nil {
		return "", nil, nil, err
	}
	return ffmpegPipeInput, in.Stdin, in.Cleanup, nil
}

func isASRAudioFile(path string) bool {
	return asrAudioSuffixes[strings.ToLower(filepath.Ext(path))]
}

// extractASRAudio writes 16 kHz mono audio for Whisper (prefer MP3, then FLAC, then WAV).
// Maps only the first audio stream and disables video/subtitle demux.
// Returns the path that was actually written (extension depends on codec availability).
func (s *Service) extractASRAudio(ctx context.Context, mediaID int64, videoPath, outDir string) (string, error) {
	ffmpeg := strings.TrimSpace(s.FFmpegPath)
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	base := filepath.Join(outDir, ".asr-input")
	attempts := []struct {
		path string
		post []string
	}{
		{
			path: base + ".mp3",
			post: []string{"-map", "0:a:0", "-vn", "-sn", "-ac", "1", "-ar", "16000", "-c:a", "libmp3lame", "-q:a", "4", base + ".mp3"},
		},
		{
			path: base + ".flac",
			post: []string{"-map", "0:a:0", "-vn", "-sn", "-ac", "1", "-ar", "16000", "-c:a", "flac", base + ".flac"},
		},
		{
			path: base + ".wav",
			post: []string{"-map", "0:a:0", "-vn", "-sn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", base + ".wav"},
		},
	}
	var lastErr error
	for _, a := range attempts {
		_ = os.Remove(a.path)
		if _, err := storage.RunFFmpeg(ctx, s.DB, s.Vault, ffmpeg, mediaID, videoPath, 0, 0, nil, a.post, ""); err != nil {
			lastErr = err
			_ = os.Remove(a.path)
			continue
		}
		fi, err := os.Stat(a.path)
		if err != nil || fi.Size() == 0 {
			lastErr = fmt.Errorf("asr audio extract produced empty output")
			_ = os.Remove(a.path)
			continue
		}
		return a.path, nil
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("asr audio extract failed")
}

// asrInputPath returns a compact audio file for ASR.
// Video (plain or encrypted) is always extracted once here so shell/Python must not re-demux the movie.
// Existing audio inputs (e.g. lyric tracks) are returned as-is.
func (s *Service) asrInputPath(ctx context.Context, mediaID int64, mediaPath, outDir string) (path string, cleanup func(), err error) {
	noop := func() {}
	mediaPath = strings.TrimSpace(mediaPath)
	if mediaPath == "" {
		return "", nil, fmt.Errorf("empty media path")
	}
	needsPipe := storage.InputNeedsPipe(s.DB, mediaID, mediaPath)
	if !needsPipe && isASRAudioFile(mediaPath) {
		return mediaPath, noop, nil
	}
	outPath, err := s.extractASRAudio(ctx, mediaID, mediaPath, outDir)
	if err != nil {
		return "", nil, fmt.Errorf("extract asr audio: %w", err)
	}
	return outPath, func() { _ = os.Remove(outPath) }, nil
}
