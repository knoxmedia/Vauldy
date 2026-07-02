package subtitle

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func newSubtitleTestDB(t *testing.T) *sql.DB {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "subtitle-test.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`
CREATE TABLE media_subtitle (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER NOT NULL,
    dedupe_key TEXT NOT NULL,
    source_kind TEXT NOT NULL,
    stream_index INTEGER,
    codec_name TEXT,
    lang TEXT,
    lang_source TEXT,
    label TEXT,
    source_path TEXT,
    vtt_path TEXT NOT NULL,
    status TEXT DEFAULT 'ready',
    error_message TEXT,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(media_id, dedupe_key)
);
`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return db
}

func writeMockTool(t *testing.T, dir, base string) string {
	t.Helper()

	if runtime.GOOS == "windows" {
		path := filepath.Join(dir, base+".bat")
		content := `@echo off
setlocal EnableDelayedExpansion
set "out="
set "next_is_out=0"
set "locked=0"
:next
if "%~1"=="" goto done
if "!next_is_out!"=="1" (
  set "out=%~1"
  set "next_is_out=0"
  set "locked=1"
  shift
  goto next
)
if /I "%~1"=="--output-vtt" (
  set "next_is_out=1"
  shift
  goto next
)
if "!locked!"=="0" set "out=%~1"
shift
goto next
:done
if "%out%"=="" exit /b 2
echo WEBVTT> "%out%"
echo.>> "%out%"
echo 00:00:00.000 --^> 00:00:01.000>> "%out%"
echo mock subtitle>> "%out%"
exit /b 0
`
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatalf("write mock tool: %v", err)
		}
		return path
	}

	path := filepath.Join(dir, base+".sh")
	content := `#!/bin/sh
out=""
next_is_out=0
locked=0
for arg in "$@"; do
  if [ "$next_is_out" = "1" ]; then
    out="$arg"
    next_is_out=0
    locked=1
    continue
  fi
  if [ "$arg" = "--output-vtt" ]; then
    next_is_out=1
    continue
  fi
  if [ "$locked" = "0" ]; then
    out="$arg"
  fi
done
if [ -z "$out" ]; then
  exit 2
fi
cat > "$out" <<'EOF'
WEBVTT

00:00:00.000 --> 00:00:01.000
mock subtitle
EOF
exit 0
`
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write mock tool: %v", err)
	}
	return path
}

func writeASRScriptAndCommand(t *testing.T, dir string) string {
	t.Helper()

	if runtime.GOOS == "windows" {
		script := filepath.Join(dir, "asr-writer.bat")
		content := `@echo off
set "out=%~1"
if "%out%"=="" exit /b 2
echo WEBVTT> "%out%"
echo.>> "%out%"
echo 00:00:00.000 --^> 00:00:01.000>> "%out%"
echo asr line>> "%out%"
exit /b 0
`
		if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
			t.Fatalf("write asr script: %v", err)
		}
		return `"` + script + `" "{output_vtt}"`
	}

	script := filepath.Join(dir, "asr-writer.sh")
	content := `#!/bin/sh
out="$1"
if [ -z "$out" ]; then
  exit 2
fi
cat > "$out" <<'EOF'
WEBVTT

00:00:00.000 --> 00:00:01.000
asr line
EOF
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write asr script: %v", err)
	}
	return script + ` "{output_vtt}"`
}

func TestSyncSidecarsMatchAndGenerate(t *testing.T) {
	t.Parallel()

	db := newSubtitleTestDB(t)
	workDir := t.TempDir()
	videoPath := filepath.Join(workDir, "movie.mp4")
	if err := os.WriteFile(videoPath, []byte("video"), 0o644); err != nil {
		t.Fatalf("write video: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "movie.en.srt"), []byte("1\n00:00:00,000 --> 00:00:01,000\nhello\n"), 0o644); err != nil {
		t.Fatalf("write srt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "movie.zh.vtt"), []byte("WEBVTT\n\n00:00:00.000 --> 00:00:01.000\n你好\n"), 0o644); err != nil {
		t.Fatalf("write vtt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "other.en.srt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write unrelated: %v", err)
	}

	outDir := filepath.Join(workDir, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}

	ffmpegPath := writeMockTool(t, t.TempDir(), "ffmpeg-mock")
	s := &Service{DB: db, FFmpegPath: ffmpegPath}
	if err := s.syncSidecars(context.Background(), 301, videoPath, outDir); err != nil {
		t.Fatalf("syncSidecars error: %v", err)
	}

	rows, err := db.Query(`SELECT lang, source_kind, status, vtt_path FROM media_subtitle WHERE media_id = ? ORDER BY id`, 301)
	if err != nil {
		t.Fatalf("query rows: %v", err)
	}
	defer rows.Close()

	type row struct {
		lang, kind, status, vtt string
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.lang, &r.kind, &r.status, &r.vtt); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("subtitle rows=%d want 2", len(got))
	}
	for _, r := range got {
		if r.kind != "external" || r.status != "ready" {
			t.Fatalf("unexpected row: %+v", r)
		}
		if _, err := os.Stat(r.vtt); err != nil {
			t.Fatalf("vtt file missing: %v", err)
		}
	}
}

func TestRunASRShellGeneratesVTT(t *testing.T) {
	t.Parallel()

	db := newSubtitleTestDB(t)
	outDir := t.TempDir()
	videoPath := filepath.Join(outDir, "movie.mp4")
	if err := os.WriteFile(videoPath, []byte("video"), 0o644); err != nil {
		t.Fatalf("write video: %v", err)
	}

	shellCmd := writeASRScriptAndCommand(t, t.TempDir())
	if runtime.GOOS == "windows" {
		shellCmd = `echo WEBVTT> {output_vtt}`
	}
	s := &Service{
		DB:          db,
		SubtitleDir: outDir,
		ASR: ASRConfig{
			Provider: "shell",
			Shell:    shellCmd,
		},
	}

	if err := s.runASR(context.Background(), 302, videoPath, outDir); err != nil {
		t.Fatalf("runASR error: %v", err)
	}

	var status, vttPath string
	if err := db.QueryRow(`SELECT status, vtt_path FROM media_subtitle WHERE media_id = ? AND dedupe_key = 'asr:auto'`, 302).Scan(&status, &vttPath); err != nil {
		t.Fatalf("query asr row: %v", err)
	}
	if status != "ready" {
		t.Fatalf("asr status=%s want ready", status)
	}
	b, err := os.ReadFile(vttPath)
	if err != nil {
		t.Fatalf("read asr vtt: %v", err)
	}
	if !strings.Contains(string(b), "WEBVTT") {
		t.Fatalf("unexpected asr content: %s", string(b))
	}
}

func TestRunVobSubIdxOCRCreatesVTT(t *testing.T) {
	t.Parallel()

	db := newSubtitleTestDB(t)
	mockPy := writeMockTool(t, t.TempDir(), "python-mock")
	service := &Service{
		DB:         db,
		FFmpegPath: "ffmpeg",
		FFprobePath:"ffprobe",
		OCR: OCRConfig{
			Enabled:    true,
			PythonPath: mockPy,
			ScriptPath: "ocr_script.py",
		},
	}
	idx := filepath.Join(t.TempDir(), "movie.idx")
	if err := os.WriteFile(idx, []byte("idx"), 0o644); err != nil {
		t.Fatalf("write idx: %v", err)
	}
	outVTT := filepath.Join(t.TempDir(), "ocr.vtt")

	if err := service.RunVobSubIdxOCR(context.Background(), idx, outVTT); err != nil {
		t.Fatalf("RunVobSubIdxOCR error: %v", err)
	}
	if _, err := os.Stat(outVTT); err != nil {
		t.Fatalf("ocr vtt missing: %v", err)
	}
}

func TestSyncSidecarsVobSubOCR(t *testing.T) {
	t.Parallel()

	db := newSubtitleTestDB(t)
	workDir := t.TempDir()
	videoPath := filepath.Join(workDir, "movie.mkv")
	if err := os.WriteFile(videoPath, []byte("video"), 0o644); err != nil {
		t.Fatalf("write video: %v", err)
	}
	idxPath := filepath.Join(workDir, "movie.zh.idx")
	subPath := filepath.Join(workDir, "movie.zh.sub")
	if err := os.WriteFile(idxPath, []byte("idx"), 0o644); err != nil {
		t.Fatalf("write idx: %v", err)
	}
	if err := os.WriteFile(subPath, []byte("sub"), 0o644); err != nil {
		t.Fatalf("write sub: %v", err)
	}

	outDir := filepath.Join(workDir, "subs")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}
	mockPy := writeMockTool(t, t.TempDir(), "python-mock")
	s := &Service{
		DB:          db,
		SubtitleDir: outDir,
		FFmpegPath:  "ffmpeg",
		FFprobePath: "ffprobe",
		OCR: OCRConfig{
			Enabled:    true,
			PythonPath: mockPy,
			ScriptPath: "ocr_script.py",
		},
	}

	if err := s.syncSidecars(context.Background(), 303, videoPath, outDir); err != nil {
		t.Fatalf("syncSidecars error: %v", err)
	}

	var kind, status, lang, vtt string
	if err := db.QueryRow(`SELECT source_kind, status, lang, vtt_path FROM media_subtitle WHERE media_id = ? LIMIT 1`, 303).Scan(&kind, &status, &lang, &vtt); err != nil {
		t.Fatalf("query ocr row: %v", err)
	}
	if kind != "external_ocr" || status != "ready" {
		t.Fatalf("unexpected row kind=%s status=%s", kind, status)
	}
	if lang != "zh" {
		t.Fatalf("lang=%s want zh", lang)
	}
	if _, err := os.Stat(vtt); err != nil {
		t.Fatalf("vtt missing: %v", err)
	}
}

func TestCopyOrWriteVTT(t *testing.T) {
	t.Parallel()

	src := filepath.Join(t.TempDir(), "src.vtt")
	dst := filepath.Join(t.TempDir(), "dst.vtt")
	content := "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nline\n"
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := copyOrWriteVTT(context.Background(), "ffmpeg", src, dst); err != nil {
		t.Fatalf("copyOrWriteVTT error: %v", err)
	}
	b, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(b) != content {
		t.Fatalf("content mismatch:\nwant=%q\ngot=%q", content, string(b))
	}
}

func TestSafeFileToken(t *testing.T) {
	t.Parallel()

	in := "movie.zh-Hans (1080p)!"
	got := safeFileToken(in)
	if got == "" || strings.ContainsAny(got, " !()") {
		t.Fatalf("unsafe token: %q", got)
	}
	if got != "movie_zh-Hans__1080p__" {
		t.Fatalf("token=%q", got)
	}
}

func TestNullStr(t *testing.T) {
	t.Parallel()
	if nullStr("   ") != nil {
		t.Fatal("blank should map to nil")
	}
	if v := nullStr("en"); fmt.Sprint(v) != "en" {
		t.Fatalf("value mismatch: %#v", v)
	}
}
