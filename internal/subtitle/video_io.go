package subtitle

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"knox-media/internal/storage"
	"knox-media/pkg/ffprobe"
)

const ffmpegPipeInput = "pipe:0"

// subtitleStreams probes embedded subtitle tracks, using decrypt pipe for Knox .enc.
func (s *Service) subtitleStreams(ctx context.Context, mediaID int64, videoPath string) ([]ffprobe.SubtitleStream, error) {
	_ = ctx
	if s == nil {
		return nil, fmt.Errorf("subtitle service nil")
	}
	if storage.InputNeedsPipe(s.DB, mediaID, videoPath) {
		out, cleanup, err := storage.FFprobeOutput(s.DB, s.Vault, s.FFprobePath, mediaID, videoPath, 0, 0, []string{
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
	return ffprobe.SubtitleStreams(s.FFprobePath, videoPath)
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

// extractASRAudio writes 16 kHz mono WAV via ffmpeg pipe for Whisper / shell ASR.
func (s *Service) extractASRAudio(ctx context.Context, mediaID int64, videoPath, wavPath string) error {
	post := []string{"-vn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", wavPath}
	if _, err := storage.RunFFmpeg(ctx, s.DB, s.Vault, s.FFmpegPath, mediaID, videoPath, 0, 0, nil, post, ""); err != nil {
		return err
	}
	if fi, err := os.Stat(wavPath); err != nil || fi.Size() == 0 {
		return fmt.Errorf("asr audio extract produced empty output")
	}
	return nil
}

// asrInputPath returns a filesystem path suitable for Whisper/shell ASR (pipe-derived WAV or plain path).
func (s *Service) asrInputPath(ctx context.Context, mediaID int64, videoPath, outDir string) (path string, cleanup func(), err error) {
	if !storage.InputNeedsPipe(s.DB, mediaID, videoPath) {
		return videoPath, func() {}, nil
	}
	wavPath := filepath.Join(outDir, ".asr-pipe-input.wav")
	if err := s.extractASRAudio(ctx, mediaID, videoPath, wavPath); err != nil {
		return "", nil, err
	}
	return wavPath, func() { _ = os.Remove(wavPath) }, nil
}
