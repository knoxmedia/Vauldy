package pretranscode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildHLSArgs(t *testing.T) {
	p := &Preset{VideoCodec: "libx264", VideoPreset: "veryfast", VideoCRF: 23, AudioCodec: "aac", AudioBitrate: "128k", AudioChannels: 2, AudioSampleRate: 48000, VideoPixFmt: "yuv420p"}
	r := &Rendition{Name: "720p", Height: 720, VideoBitrate: "2800k"}
	dir := t.TempDir()
	got := BuildHLSArgs(dir, p, r, nil, "")
	joined := strings.Join(AttachFFmpegInput(got.Args, "input.mp4"), " ")
	checks := []string{"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-b:v", "2800k", "-f", "hls", "-hls_time", "4", "-hls_playlist_type", "vod"}
	for _, c := range checks {
		if !strings.Contains(joined, c) {
			t.Errorf("HLS args missing %q in: %s", c, joined)
		}
	}
	if !strings.HasSuffix(got.OutFile, "720p.m3u8") {
		t.Errorf("expected 720p.m3u8 output, got %s", got.OutFile)
	}
}

func TestBuildHLSArgsAES128(t *testing.T) {
	p := &Preset{VideoCodec: "libx264", VideoPreset: "veryfast", VideoCRF: 23, AudioCodec: "aac", AudioBitrate: "128k", AudioChannels: 2, VideoPixFmt: "yuv420p", EncryptionMode: "aes128"}
	r := &Rendition{Name: "720p", Height: 720, VideoBitrate: "2800k"}
	dir := t.TempDir()
	ki := &KeyInfo{KeyInfoPath: filepath.Join(dir, "enc.keyinfo"), KeyPath: filepath.Join(dir, "enc.key"), IVHex: "abcdef0123456789abcdef0123456789"}
	got := BuildHLSArgs(dir, p, r, ki, "")
	if !strings.Contains(strings.Join(AttachFFmpegInput(got.Args, "input.mp4"), " "), "-hls_key_info_file") {
		t.Errorf("AES-128 HLS args missing -hls_key_info_file")
	}
}

func TestBuildMP4ArgsFaststart(t *testing.T) {
	p := &Preset{VideoCodec: "libx264", VideoPreset: "veryfast", VideoCRF: 23, AudioCodec: "aac", AudioBitrate: "128k", AudioChannels: 2, VideoPixFmt: "yuv420p"}
	r := &Rendition{Name: "720p", Height: 720, VideoBitrate: "2800k"}
	dir := t.TempDir()
	got := BuildMP4Args(dir, p, r, "")
	joined := strings.Join(AttachFFmpegInput(got.Args, "input.mp4"), " ")
	if !strings.Contains(joined, "-movflags") || !strings.Contains(joined, "+faststart") {
		t.Errorf("MP4 args missing +faststart: %s", joined)
	}
	if !strings.HasSuffix(got.OutFile, "720p.mp4") {
		t.Errorf("expected 720p.mp4 output, got %s", got.OutFile)
	}
}

func TestGenerateAES128KeyInfo(t *testing.T) {
	dir := t.TempDir()
	ki, err := GenerateAES128KeyInfo(dir, 42, "file-id-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ki.KeyHex) != 32 {
		t.Errorf("key hex must be 32 chars, got %d", len(ki.KeyHex))
	}
	if len(ki.IVHex) != 32 {
		t.Errorf("iv hex must be 32 chars, got %d", len(ki.IVHex))
	}
	if !strings.Contains(ki.KeyURI, "/api/v1/drm/hls-key/42") {
		t.Errorf("key URI unexpected: %s", ki.KeyURI)
	}
	if _, err := os.Stat(ki.KeyPath); err != nil {
		t.Errorf("key file not written: %v", err)
	}
	// keyinfo file format: URI, key path, IV — three lines.
	content, err := os.ReadFile(ki.KeyInfoPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	if len(lines) != 3 || lines[0] != ki.KeyURI || lines[1] != ki.KeyPath || lines[2] != ki.IVHex {
		t.Errorf("keyinfo file format wrong: %q", string(content))
	}
}

func TestGenerateAES128KeyInfoUnique(t *testing.T) {
	dir := t.TempDir()
	k1, _ := GenerateAES128KeyInfo(dir, 1, "")
	k2, _ := GenerateAES128KeyInfo(dir, 2, "")
	if k1.KeyHex == k2.KeyHex || k1.IVHex == k2.IVHex {
		t.Errorf("key material must be unique across calls")
	}
}

func TestBuildHLSArgsDefaultsEmptyAudioCodec(t *testing.T) {
	p := &Preset{VideoCodec: "libx264", VideoPreset: "veryfast", VideoCRF: 23, AudioBitrate: "128k"}
	r := &Rendition{Name: "480p", Height: 480, VideoBitrate: "1400k"}
	got := BuildHLSArgs(t.TempDir(), p, r, nil, "")
	joined := strings.Join(AttachFFmpegInput(got.Args, "input.mp4"), " ")
	if !strings.Contains(joined, "-c:a aac") {
		t.Fatalf("expected default audio codec aac, got: %s", joined)
	}
}

func TestBuildHLSArgsHEVCNVENC(t *testing.T) {
	p := &Preset{VideoCodec: "hevc_nvenc", VideoPreset: "veryfast", AudioCodec: "aac", AudioBitrate: "128k", AudioChannels: 2, VideoPixFmt: "yuv420p"}
	r := &Rendition{Name: "720p", Height: 720, VideoBitrate: "2800k"}
	got := BuildHLSArgs(t.TempDir(), p, r, nil, "")
	if got.EncoderUsed != "hevc_nvenc" {
		t.Fatalf("expected hevc_nvenc, got %s", got.EncoderUsed)
	}
	joined := strings.Join(AttachFFmpegInput(got.Args, "input.mp4"), " ")
	if !strings.Contains(joined, "-c:v hevc_nvenc") {
		t.Fatalf("missing hevc_nvenc encoder: %s", joined)
	}
	if !strings.Contains(joined, "-preset p4") {
		t.Fatalf("expected NVENC preset p4, got: %s", joined)
	}
	if strings.Contains(joined, "-crf") {
		t.Fatalf("hevc_nvenc should not use -crf: %s", joined)
	}
}

func TestBuildHLSArgsAMF(t *testing.T) {
	p := &Preset{VideoCodec: "h264_amf", VideoPreset: "veryfast", AudioCodec: "aac", AudioBitrate: "128k", AudioChannels: 2}
	r := &Rendition{Name: "720p", Height: 720, VideoBitrate: "2800k"}
	got := BuildHLSArgs(t.TempDir(), p, r, nil, "h264_amf")
	joined := strings.Join(AttachFFmpegInput(got.Args, "input.mp4"), " ")
	for _, want := range []string{"-c:v", "h264_amf", "format=nv12", "-quality", "speed", "-rc", "vbr_peak"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in: %s", want, joined)
		}
	}
}

func TestBuildHLSArgsHEVCAMF(t *testing.T) {
	p := &Preset{VideoCodec: "hevc_amf", VideoPreset: "medium", AudioCodec: "aac", AudioBitrate: "128k"}
	r := &Rendition{Name: "1080p", Height: 1080, VideoBitrate: "5000k"}
	got := BuildHLSArgs(t.TempDir(), p, r, nil, "hevc_amf")
	joined := strings.Join(AttachFFmpegInput(got.Args, "input.mp4"), " ")
	for _, want := range []string{"-c:v", "hevc_amf", "format=nv12", "-quality", "balanced", "-rc", "vbr_peak"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in: %s", want, joined)
		}
	}
}

func TestBuildHLSArgsHardwareEncoder(t *testing.T) {
	p := &Preset{VideoCodec: "h264_nvenc", VideoPreset: "", AudioCodec: "aac", AudioBitrate: "128k", AudioChannels: 2, VideoPixFmt: "yuv420p"}
	r := &Rendition{Name: "720p", Height: 720, VideoBitrate: "2800k"}
	dir := t.TempDir()
	got := BuildHLSArgs(dir, p, r, nil, "")
	if got.EncoderUsed != "h264_nvenc" {
		t.Errorf("expected h264_nvenc, got %s", got.EncoderUsed)
	}
	if !got.UsesHardware {
		t.Errorf("expected hardware flag true")
	}
	joined := strings.Join(AttachFFmpegInput(got.Args, "input.mp4"), " ")
	if strings.Contains(joined, "-crf") {
		t.Errorf("hardware encoder should not have -crf")
	}
	if !strings.Contains(joined, "-preset p4") {
		t.Errorf("expected NVENC preset p4, got: %s", joined)
	}
}
