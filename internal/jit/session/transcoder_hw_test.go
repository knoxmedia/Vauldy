package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"knox-media/internal/jit/hwenc"
	"knox-media/internal/storage"
)

func TestEncryptedPipeUsesHWEncodeOnlyWithoutInputAccel(t *testing.T) {
	in := &storage.FFmpegInput{FromEnc: true}
	localFile := in.Path != "" && !in.FromEnc
	pipeline := hwenc.PipelineModeForInput(true, localFile)
	if pipeline != hwenc.PipelineHWEncodeOnly {
		t.Fatalf("pipeline=%v want HWEncodeOnly", pipeline)
	}
	if pipeline == hwenc.PipelineHWFull {
		t.Fatal("encrypted pipe must not use full HW decode pipeline")
	}
}

func findMediaTools(t *testing.T) (ffmpeg, ffprobe string) {
	t.Helper()
	candidates := []string{"ffmpeg", "ffmpeg.exe"}
	if runtime.GOOS == "windows" {
		candidates = []string{"tools/ffmpeg/bin/ffmpeg.exe", "ffmpeg.exe", "ffmpeg"}
	}
	for _, p := range candidates {
		if full, err := filepath.Abs(p); err == nil {
			if _, serr := os.Stat(full); serr == nil {
				ffmpeg = full
				break
			}
		}
	}
	if ffmpeg == "" {
		if p, err := exec.LookPath("ffmpeg"); err == nil {
			ffmpeg = p
		}
	}
	if ffmpeg == "" {
		t.Skip("ffmpeg not available")
	}
	ffprobe = strings.TrimSuffix(ffmpeg, "ffmpeg") + "ffprobe"
	if ffprobe == ffmpeg || (func() bool { _, err := os.Stat(ffprobe); return err != nil })() {
		ffprobe = ""
	}
	if ffprobe == "" {
		if p, err := exec.LookPath("ffprobe"); err == nil {
			ffprobe = p
		} else {
			ffprobe = strings.TrimSuffix(ffmpeg, "ffmpeg.exe") + "ffprobe.exe"
			if runtime.GOOS != "windows" {
				ffprobe = strings.TrimSuffix(ffmpeg, "ffmpeg") + "ffprobe"
			}
		}
	}
	return ffmpeg, ffprobe
}

func makeH264TestVideo(t *testing.T, ffmpeg, dir string) string {
	t.Helper()
	out := filepath.Join(dir, "src.mp4")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=10", "-t", "1",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", out)
	if err := cmd.Run(); err != nil {
		t.Skipf("cannot generate test video: %v", err)
	}
	return out
}

func TestSourceSupportsHardwareDecodeH264(t *testing.T) {
	ffmpeg, ffprobe := findMediaTools(t)
	dir := t.TempDir()
	src := makeH264TestVideo(t, ffmpeg, dir)

	s := &Session{ffmpegPath: ffmpeg, mgr: &Manager{ffprobePath: ffprobe}}
	if !s.sourceSupportsHardwareDecode(TranscodeConfig{VideoEncoder: hwenc.H264NVENC}, src) {
		t.Fatal("expected H.264 source to report hardware decode support")
	}
}

func TestProbeSourceVideoCodecEmptyOnMissingProbe(t *testing.T) {
	ffmpeg, _ := findMediaTools(t)
	// 缺少 ffprobe 时探测返回空字符串。
	s := &Session{ffmpegPath: ffmpeg, mgr: &Manager{ffprobePath: ""}}
	if codec := s.probeSourceVideoCodec("/nonexistent/file.mkv"); codec != "" {
		t.Fatalf("expected empty codec for missing probe, got %q", codec)
	}
}
