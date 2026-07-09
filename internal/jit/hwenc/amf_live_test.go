package hwenc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncoderListedInFFmpeg(t *testing.T) {
	ff := os.Getenv("FFMPEG_PATH")
	if ff == "" {
		ff = "ffmpeg"
	}
	if _, err := exec.LookPath(ff); err != nil {
		t.Skip("ffmpeg not in PATH")
	}
	if !EncoderListedInFFmpeg(ff, "libx264") {
		t.Fatal("expected libx264 in ffmpeg encoders")
	}
}

func TestListAvailableEncodersIncludesAMFOnHost(t *testing.T) {
	ff := os.Getenv("FFMPEG_PATH")
	if ff == "" {
		ff = "ffmpeg"
	}
	if _, err := exec.LookPath(ff); err != nil {
		t.Skip("ffmpeg not in PATH")
	}
	encoders := ListAvailableEncoders(ff)
	has := func(id string) bool {
		for _, e := range encoders {
			if e.ID == id {
				return true
			}
		}
		return false
	}
	if EncoderListedInFFmpeg(ff, "h264_amf") && detectAMDGPU() && !has("h264_amf") {
		t.Fatalf("ffmpeg lists h264_amf and AMD GPU present but encoder missing from ListAvailableEncoders")
	}
	if EncoderListedInFFmpeg(ff, "hevc_amf") && detectAMDGPU() && !has("hevc_amf") {
		t.Fatalf("ffmpeg lists hevc_amf and AMD GPU present but encoder missing from ListAvailableEncoders")
	}
}

func TestAMFEncodeLive(t *testing.T) {
	if os.Getenv("KNOX_MEDIA_LIVE_FFMPEG_TEST") == "" {
		t.Skip("set KNOX_MEDIA_LIVE_FFMPEG_TEST=1 to run live AMF encode probes")
	}
	ff := os.Getenv("FFMPEG_PATH")
	if ff == "" {
		ff = "ffmpeg"
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "amf.ts")
	for _, tc := range []struct {
		name, encoder string
	}{
		{"h264_amf", "h264_amf"},
		{"hevc_amf", "hevc_amf"},
	} {
		if !EncoderListedInFFmpeg(ff, tc.encoder) {
			t.Skipf("%s not in ffmpeg build", tc.encoder)
		}
		t.Run(tc.name, func(t *testing.T) {
			args := []string{
				"-hide_banner", "-loglevel", "error", "-y",
				"-f", "lavfi", "-i", "testsrc=duration=1:size=640x360:rate=30",
				"-vf", "scale=-2:360,format=nv12",
				"-c:v", tc.encoder,
				"-quality", "balanced", "-rc", "vbr_peak",
				"-b:v", "1400k", "-frames:v", "30",
				"-f", "mpegts", out,
			}
			outBytes, err := exec.Command(ff, args...).CombinedOutput()
			if err != nil {
				t.Fatalf("encode failed: %v\n%s", err, string(outBytes))
			}
			if !strings.Contains(string(outBytes), tc.encoder) && err == nil {
				// ffmpeg may not echo encoder name on success; verify output file exists
			}
			st, err := os.Stat(out)
			if err != nil || st.Size() == 0 {
				t.Fatalf("output missing or empty: %v", err)
			}
		})
	}
}
