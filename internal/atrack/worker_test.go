package atrack

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestAtrackWorker_CancelRestoresWaitingAndCleansOutput(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE atrack_task(media_id INTEGER UNIQUE,status TEXT,output_dir TEXT,error_message TEXT,updated_at TEXT); CREATE TABLE media_encrypted_assets(media_id INTEGER,status TEXT); INSERT INTO atrack_task(media_id,status) VALUES(41,'waiting')`)
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	w := NewWorker(db, nil, nil, "ffmpeg", "ffprobe", out)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = w.Run(ctx, 41, filepath.Join(t.TempDir(), "video.mp4"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want canceled", err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM atrack_task WHERE media_id=41`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "waiting" {
		t.Fatalf("status=%q want waiting", status)
	}
	if _, err := os.Stat(filepath.Join(out, "41")); !os.IsNotExist(err) {
		t.Fatalf("temporary output remains err=%v", err)
	}
}

func TestAtrackWorker_FinalDatabaseErrorIsReturned(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`CREATE TABLE atrack_task(media_id INTEGER UNIQUE,status TEXT,output_dir TEXT,error_message TEXT,updated_at TEXT); CREATE TABLE media_encrypted_assets(media_id INTEGER,status TEXT); INSERT INTO atrack_task(media_id,status) VALUES(41,'waiting')`)
	w := NewWorker(db, nil, nil, "ffmpeg", "ffprobe", t.TempDir())
	_ = db.Close()
	if err := w.Run(context.Background(), 41, "video.mp4"); err == nil {
		t.Fatal("expected database error")
	}
}

func TestAtrackWorker_CommitGuardFailureKeepsExistingOutput(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, _ = db.Exec(`CREATE TABLE atrack_task(media_id INTEGER UNIQUE,status TEXT,output_dir TEXT,error_message TEXT,updated_at TEXT); CREATE TABLE media_encrypted_assets(media_id INTEGER,status TEXT); INSERT INTO atrack_task(media_id,status) VALUES(41,'waiting')`)
	root := t.TempDir()
	final := filepath.Join(root, "41")
	_ = os.MkdirAll(final, 0755)
	old := filepath.Join(final, "old.txt")
	_ = os.WriteFile(old, []byte("old"), 0644)
	ctx := WithCommitGuard(context.Background(), func(context.Context) error { return errors.New("stale generation") })
	w := NewWorker(db, nil, nil, "ffmpeg", "ffprobe", root)
	err = w.Run(ctx, 41, "missing.mp4")
	if err == nil {
		t.Fatal("expected guard/processing error")
	}
	if b, e := os.ReadFile(old); e != nil || string(b) != "old" {
		t.Fatalf("existing output changed: %q %v", b, e)
	}
	matches, _ := filepath.Glob(filepath.Join(root, "41.tmp-*"))
	if len(matches) != 0 {
		t.Fatalf("staging remains: %v", matches)
	}
}

func TestAtrackWorker_CancelWithStaleGuardDoesNotChangeDomainStatus(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, _ = db.Exec(`CREATE TABLE atrack_task(media_id INTEGER UNIQUE,status TEXT,output_dir TEXT,error_message TEXT,updated_at TEXT); CREATE TABLE media_encrypted_assets(media_id INTEGER,status TEXT); INSERT INTO atrack_task(media_id,status) VALUES(41,'running')`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ctx = WithCommitGuard(ctx, func(context.Context) error { return errors.New("stale") })
	w := NewWorker(db, nil, nil, "ffmpeg", "ffprobe", t.TempDir())
	_ = w.Run(ctx, 41, "missing.mp4")
	var status string
	_ = db.QueryRow(`SELECT status FROM atrack_task WHERE media_id=41`).Scan(&status)
	if status != "running" {
		t.Fatalf("status=%q", status)
	}
}

func TestAtrackWorker_JoinsRestoreFailureWithCommitError(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows command fixture")
	}
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE atrack_task(media_id INTEGER UNIQUE,status TEXT,output_dir TEXT,error_message TEXT,updated_at TEXT); CREATE TABLE media_encrypted_assets(media_id INTEGER,status TEXT); INSERT INTO atrack_task(media_id,status) VALUES(41,'waiting'); CREATE TRIGGER reject_atrack_done BEFORE UPDATE ON atrack_task WHEN NEW.status='done' BEGIN SELECT RAISE(FAIL,'reject done'); END`)
	if err != nil {
		t.Fatal(err)
	}
	tools := t.TempDir()
	probe := filepath.Join(tools, "probe.bat")
	ffmpeg := filepath.Join(tools, "ffmpeg.bat")
	if err := os.WriteFile(probe, []byte("@echo off\r\necho {\"streams\":[{\"index\":0,\"codec_name\":\"aac\",\"codec_type\":\"audio\"}]}\r\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ffmpegBody := `@echo off
setlocal EnableDelayedExpansion
set "seg="
set "last="
:loop
if "%~1"=="" goto done
if "!wantseg!"=="1" (set "seg=%~1"&set "wantseg=0") else if "%~1"=="-hls_segment_filename" (set "wantseg=1")
set "last=%~1"
shift
goto loop
:done
set "seg=!seg:%%03d=000!"
for %%D in ("!seg!") do if not exist "%%~dpD" mkdir "%%~dpD"
echo segment> "!seg!"
for %%D in ("!last!") do if not exist "%%~dpD" mkdir "%%~dpD"
(echo #EXTM3U&echo #EXTINF:6,&echo seg_000.ts)> "!last!"
exit /b 0
`
	if err := os.WriteFile(ffmpeg, []byte(strings.ReplaceAll(ffmpegBody, "\n", "\r\n")), 0644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	final := filepath.Join(root, "41")
	_ = os.MkdirAll(final, 0755)
	_ = os.WriteFile(filepath.Join(final, "old.txt"), []byte("old"), 0644)
	restoreErr := errors.New("restore sentinel")
	w := NewWorker(db, nil, nil, ffmpeg, probe, root)
	w.restoreDir = func(string, string) error { return restoreErr }
	input := filepath.Join(root, "input.mp4")
	if writeErr := os.WriteFile(input, []byte("video"), 0644); writeErr != nil {
		t.Fatal(writeErr)
	}
	err = w.Run(context.Background(), 41, input)
	if err == nil || !strings.Contains(err.Error(), "reject done") || !errors.Is(err, restoreErr) {
		t.Fatalf("err=%v", err)
	}
}
