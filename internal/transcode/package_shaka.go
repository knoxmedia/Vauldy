package transcode

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func resetDRMOutDir(outDir string) error {
	if outDir == "" {
		return fmt.Errorf("empty out dir")
	}
	if err := os.RemoveAll(outDir); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.MkdirAll(outDir, 0o755)
}

// runShakaCMAF re-encodes intermediate cleartext MP4s, then runs Shaka Packager with raw-key CENC.
func (w *PackageWorker) runShakaCMAF(ctx context.Context, taskID, mediaID int64, inputPath, outDir, intBase string, ladder []Rendition, keyHex, kidHex string) (string, error) {
	if w == nil {
		return "", fmt.Errorf("package worker is nil")
	}
	if strings.TrimSpace(inputPath) == "" {
		return "", fmt.Errorf("empty input path")
	}
	shaka := strings.TrimSpace(w.shakaPath)
	if shaka == "" {
		return "", fmt.Errorf("shaka packager path not configured")
	}
	if strings.TrimSpace(w.FFmpegPath) == "" {
		return "", fmt.Errorf("ffmpeg path not configured")
	}
	if strings.TrimSpace(keyHex) == "" || strings.TrimSpace(kidHex) == "" {
		return "", fmt.Errorf("drm key material not configured")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	if err := os.MkdirAll(intBase, 0o755); err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(intBase) }()

	segF := float64(w.segmentSecOrDefault())
	segSec := w.segmentSecOrDefault()
	if segSec <= 0 {
		segSec = 4
	}
	gop := segSec * 30
	if gop <= 0 {
		gop = 120
	}

	for i, r := range ladder {
		vOut := filepath.Join(intBase, r.Name+"_v.mp4")
		vf := fmt.Sprintf("scale=-2:%d", r.Height)
		fargs := []string{
			"-y", "-i", inputPath, "-map", "0:v:0",
			"-vf", vf,
			"-c:v", "libx264", "-preset", "veryfast", "-b:v", r.VideoRate, "-maxrate", r.VideoRate, "-bufsize", "2M",
			// Force keyframes on segment boundaries for better A/V timeline alignment.
			"-g", fmt.Sprintf("%d", gop), "-keyint_min", fmt.Sprintf("%d", gop), "-sc_threshold", "0",
			"-an", vOut,
		}
		logDRMf(taskID, mediaID, "shaka pre-encode start: rung=%s cmd=%s %s", r.Name, w.FFmpegPath, strings.Join(fargs, " "))
		fcmd := exec.CommandContext(ctx, w.FFmpegPath, fargs...)
		var fstderr bytes.Buffer
		fcmd.Stderr = &fstderr
		fcmd.Stdout = &fstderr
		if err := fcmd.Run(); err != nil {
			logDRMf(taskID, mediaID, "shaka pre-encode failed: rung=%s err=%v stderr=%s", r.Name, err, trimErrorMessage(fstderr.String()))
			return "", fmt.Errorf("shaka pre-encode rung %d: %v; stderr: %s", i, err, fstderr.String())
		}
		logDRMf(taskID, mediaID, "shaka pre-encode done: rung=%s", r.Name)
	}

	aPath := filepath.Join(intBase, "audio.m4a")
	var hasAudio bool
	{
		aargs := []string{
			"-y", "-i", inputPath, "-map", "0:a:0", "-vn",
			"-c:a", "aac", "-b:a", "128k", "-ar", "48000", "-ac", "2",
			"-f", "mp4", aPath,
		}
		logDRMf(taskID, mediaID, "shaka audio extract start: cmd=%s %s", w.FFmpegPath, strings.Join(aargs, " "))
		acmd := exec.CommandContext(ctx, w.FFmpegPath, aargs...)
		var st bytes.Buffer
		acmd.Stderr = &st
		acmd.Stdout = &st
		if err := acmd.Run(); err == nil {
			if s, serr := os.Stat(aPath); serr == nil && s.Size() > 10 {
				hasAudio = true
				logDRMf(taskID, mediaID, "shaka audio extract done: has_audio=true size=%d (audio clear mode)", s.Size())
			}
		} else {
			logDRMf(taskID, mediaID, "shaka audio extract skipped: err=%v stderr=%s", err, trimErrorMessage(st.String()))
		}
	}

	var streamArgs []string
	for _, r := range ladder {
		vp := filepath.Join(intBase, r.Name+"_v.mp4")
		in, err := toShakaInPath(vp)
		if err != nil {
			return "", err
		}
		initS := r.Name + "_init.mp4"
		tpl := r.Name + "_$Number$.m4s"
		playlist := r.Name + ".m3u8"
		b := r.Bandwidth
		if b <= 0 {
			b = 1_000_000
		}
		s := fmt.Sprintf("in=%s,stream=video,init_segment=%s,segment_template=%s,drm_label=DEFAULT,playlist_name=%s,bandwidth=%d",
			in, initS, tpl, playlist, b)
		if hasAudio {
			s += ",hls_group_id=aud1"
		}
		streamArgs = append(streamArgs, s)
	}
	if hasAudio {
		in, err := toShakaInPath(aPath)
		if err != nil {
			return "", err
		}
		// Edge + ClearKey debug path: keep audio clear to avoid encrypted AAC renderer failures.
		s := fmt.Sprintf("in=%s,stream=audio,init_segment=audio_init.mp4,segment_template=audio_$Number$.m4s,drm_label=DEFAULT,skip_encryption=1,playlist_name=audio.m3u8,bandwidth=128000,hls_name=default,hls_group_id=aud1", in)
		streamArgs = append(streamArgs, s)
	}

	kid := strings.ToLower(strings.TrimSpace(kidHex))
	k := strings.ToLower(strings.TrimSpace(keyHex))
	// Shaka expects: [flags] <stream_descriptor>... (see packager --help).
	// Do not set lang=und on audio: packager rejects it (INVALID_ARGUMENT).
	pargs := []string{
		"--enable_raw_key_encryption",
		fmt.Sprintf("--keys=label=DEFAULT:key_id=%s:key=%s", kid, k),
		"--protection_systems=Widevine",
		fmt.Sprintf("--segment_duration=%.1f", segF),
		"--mpd_output=manifest.mpd",
		"--hls_master_playlist_output=master.m3u8",
	}
	pargs = append(pargs, streamArgs...)

	logDRMf(taskID, mediaID, "shaka packager start: cmd=%s %s", shaka, strings.Join(pargs, " "))
	pcmd := exec.CommandContext(ctx, shaka, pargs...)
	pcmd.Dir = outDir
	var pstderr bytes.Buffer
	pcmd.Stderr = &pstderr
	pcmd.Stdout = &pstderr
	if err := pcmd.Run(); err != nil {
		logDRMf(taskID, mediaID, "shaka packager failed: err=%v stderr=%s", err, trimErrorMessage(pstderr.String()))
		return "", fmt.Errorf("shaka packager: %v; stderr: %s", err, pstderr.String())
	}
	logDRMf(taskID, mediaID, "shaka packager done: out_dir=%s", outDir)
	master := filepath.Join(outDir, "master.m3u8")
	if st, err := os.Stat(master); err != nil || st.IsDir() {
		return "", fmt.Errorf("shaka: master playlist missing: %s", master)
	}
	if err := injectHLSDRMTags(outDir, kidHex); err != nil {
		logDRMf(taskID, mediaID, "shaka m3u8 drm-tag inject failed: err=%v", err)
		return "", err
	}
	logDRMf(taskID, mediaID, "shaka m3u8 drm-tag inject done")
	return master, nil
}

func toShakaInPath(p string) (string, error) {
	a, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(a); err != nil {
		return "", err
	}
	return filepath.ToSlash(a), nil
}

func (w *PackageWorker) segmentSecOrDefault() int {
	if w == nil || w.segmentSec <= 0 {
		return 4
	}
	return w.segmentSec
}

func injectHLSDRMTags(outDir string, kidHex string) error {
	kid := strings.ToLower(strings.TrimSpace(kidHex))
	if outDir == "" || kid == "" {
		return nil
	}
	psshB64, err := buildWidevinePSSHBase64(kid)
	if err != nil {
		return err
	}
	wvTag := fmt.Sprintf(`#EXT-X-SESSION-KEY:METHOD=SAMPLE-AES-CTR,URI="data:text/plain;base64,%s",KEYID=0x%s,KEYFORMAT="urn:uuid:edef8ba9-79d6-4ace-a3c8-27dcd51d21ed",KEYFORMATVERSIONS="1"`, psshB64, strings.ToUpper(kid))
	fpTag := fmt.Sprintf(`#EXT-X-SESSION-KEY:METHOD=SAMPLE-AES-CTR,URI="skd://%s",KEYFORMAT="com.apple.streamingkeydelivery",KEYFORMATVERSIONS="1"`, strings.ToUpper(kid))
	keyTag := fmt.Sprintf(`#EXT-X-KEY:METHOD=SAMPLE-AES-CTR,URI="data:text/plain;base64,%s",KEYID=0x%s,KEYFORMAT="urn:uuid:edef8ba9-79d6-4ace-a3c8-27dcd51d21ed",KEYFORMATVERSIONS="1"`, psshB64, strings.ToUpper(kid))

	master := filepath.Join(outDir, "master.m3u8")
	if err := injectSessionKeysIntoMaster(master, wvTag, fpTag); err != nil {
		return err
	}
	pls, err := filepath.Glob(filepath.Join(outDir, "*.m3u8"))
	if err != nil {
		return err
	}
	for _, p := range pls {
		if strings.EqualFold(filepath.Base(p), "master.m3u8") {
			continue
		}
		// Compatibility mode: keep audio playlist clear, do not inject DRM key tag.
		if strings.HasPrefix(strings.ToLower(filepath.Base(p)), "audio") {
			continue
		}
		if err := injectKeyIntoVariant(p, keyTag); err != nil {
			return err
		}
	}
	return nil
}

func buildWidevinePSSHBase64(kidHex string) (string, error) {
	kid, err := hex.DecodeString(strings.TrimSpace(kidHex))
	if err != nil {
		return "", fmt.Errorf("decode kid hex: %w", err)
	}
	if len(kid) != 16 {
		return "", fmt.Errorf("invalid kid bytes length: %d", len(kid))
	}
	systemID := []byte{0xed, 0xef, 0x8b, 0xa9, 0x79, 0xd6, 0x4a, 0xce, 0xa3, 0xc8, 0x27, 0xdc, 0xd5, 0x1d, 0x21, 0xed}
	// version 1 pssh with one key id and empty data.
	size := uint32(4 + 4 + 4 + 16 + 4 + 16 + 4)
	buf := make([]byte, 0, size)
	u32 := make([]byte, 4)
	binary.BigEndian.PutUint32(u32, size)
	buf = append(buf, u32...)
	buf = append(buf, []byte{'p', 's', 's', 'h'}...)
	buf = append(buf, 0x01, 0x00, 0x00, 0x00) // version=1 flags=0
	buf = append(buf, systemID...)
	binary.BigEndian.PutUint32(u32, 1) // kid_count
	buf = append(buf, u32...)
	buf = append(buf, kid...)
	binary.BigEndian.PutUint32(u32, 0) // data_size
	buf = append(buf, u32...)
	return base64.StdEncoding.EncodeToString(buf), nil
}

func injectSessionKeysIntoMaster(masterPath string, wvTag string, fpTag string) error {
	raw, err := os.ReadFile(masterPath)
	if err != nil {
		return err
	}
	txt := string(raw)
	if strings.Contains(txt, "#EXT-X-SESSION-KEY:") {
		return nil
	}
	lines := strings.Split(txt, "\n")
	var out []string
	inserted := false
	for _, ln := range lines {
		out = append(out, ln)
		if !inserted && strings.TrimSpace(ln) == "#EXTM3U" {
			out = append(out, wvTag, fpTag)
			inserted = true
		}
	}
	return os.WriteFile(masterPath, []byte(strings.Join(out, "\n")), 0o644)
}

func injectKeyIntoVariant(playlistPath string, keyTag string) error {
	raw, err := os.ReadFile(playlistPath)
	if err != nil {
		return err
	}
	txt := string(raw)
	if strings.Contains(txt, "#EXT-X-KEY:") {
		return nil
	}
	lines := strings.Split(txt, "\n")
	var out []string
	inserted := false
	for _, ln := range lines {
		out = append(out, ln)
		t := strings.TrimSpace(ln)
		if !inserted && (strings.HasPrefix(t, "#EXT-X-MAP:") || strings.HasPrefix(t, "#EXT-X-TARGETDURATION:")) {
			out = append(out, keyTag)
			inserted = true
		}
	}
	if !inserted {
		out = append(out, keyTag)
	}
	return os.WriteFile(playlistPath, []byte(strings.Join(out, "\n")), 0o644)
}
