package transcode

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"knox-media/internal/jit/hwenc"

	_ "modernc.org/sqlite"
)

func newTranscodeTestDB(t *testing.T) *sql.DB {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "transcode-test.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`
CREATE TABLE transcode_task (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id TEXT,
    quality TEXT,
    status TEXT DEFAULT 'waiting',
    progress INTEGER DEFAULT 0,
    error_message TEXT,
    output_path TEXT
);
`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return db
}

func writeMockEncoderLister(t *testing.T, output string) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		path := filepath.Join(dir, "ffmpeg-list.bat")
		content := "@echo off\r\n" +
			"if \"%~1\"==\"-hide_banner\" (\r\n" +
			"  echo " + output + "\r\n" +
			"  exit /b 0\r\n" +
			")\r\n" +
			"exit /b 0\r\n"
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatalf("write lister: %v", err)
		}
		return path
	}

	path := filepath.Join(dir, "ffmpeg-list.sh")
	content := "#!/bin/sh\n" +
		"if [ \"$1\" = \"-hide_banner\" ]; then\n" +
		"  echo \"" + output + "\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"exit 0\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write lister: %v", err)
	}
	return path
}

func writeMockFFmpegRunner(t *testing.T, shouldFail bool) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		path := filepath.Join(dir, "ffmpeg-run.bat")
		content := "@echo off\r\n" +
			"setlocal EnableDelayedExpansion\r\n" +
			"set \"out=\"\r\n" +
			":next\r\n" +
			"if \"%~1\"==\"\" goto done\r\n" +
			"set \"out=%~1\"\r\n" +
			"shift\r\n" +
			"goto next\r\n" +
			":done\r\n"
		if shouldFail {
			content += "echo fail 1>&2\r\nexit /b 1\r\n"
		} else {
			content += "if \"%out%\"==\"\" exit /b 2\r\n" +
				"echo #EXTM3U> \"%out%\"\r\n" +
				"exit /b 0\r\n"
		}
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatalf("write runner: %v", err)
		}
		return path
	}

	path := filepath.Join(dir, "ffmpeg-run.sh")
	content := "#!/bin/sh\n" +
		"out=\"\"\n" +
		"for arg in \"$@\"; do out=\"$arg\"; done\n"
	if shouldFail {
		content += "echo fail 1>&2\nexit 1\n"
	} else {
		content += "if [ -z \"$out\" ]; then exit 2; fi\n" +
			"echo \"#EXTM3U\" > \"$out\"\n" +
			"exit 0\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write runner: %v", err)
	}
	return path
}

func TestDetectEncoderBackend(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		hwAccel string
		wantEnc EncoderBackend
	}{
		{name: "qsv", hwAccel: "qsv", wantEnc: EncoderQSV},
		{name: "vaapi", hwAccel: "vaapi", wantEnc: EncoderVAAPI},
		{name: "nvenc", hwAccel: "nvenc", wantEnc: EncoderNVENC},
		{name: "amf", hwAccel: "amf", wantEnc: EncoderAMF},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id, ok := hwenc.HardwareAccelToEncoder(tc.hwAccel)
			if !ok {
				t.Fatalf("HardwareAccelToEncoder(%q) not ok", tc.hwAccel)
			}
			var got EncoderBackend
			switch id {
			case hwenc.H264QSV:
				got = EncoderQSV
			case hwenc.H264AMF:
				got = EncoderAMF
			case hwenc.H264NVENC:
				got = EncoderNVENC
			case hwenc.H264VAAPI:
				got = EncoderVAAPI
			default:
				got = EncoderX264
			}
			if got != tc.wantEnc {
				t.Fatalf("encoder mapping=%s want %s", got, tc.wantEnc)
			}
		})
	}
}

func TestEncoderArgsHardwareAccelerated(t *testing.T) {
	t.Parallel()

	cases := []struct {
		enc       EncoderBackend
		wantParts []string
	}{
		{enc: EncoderQSV, wantParts: []string{"h264_qsv", "-maxrate"}},
		{enc: EncoderVAAPI, wantParts: []string{"h264_vaapi", "scale_vaapi=w=-2:h=720"}},
		{enc: EncoderNVENC, wantParts: []string{"h264_nvenc", "-preset", "p4"}},
		{enc: EncoderX264, wantParts: []string{"libx264", "veryfast"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(string(tc.enc), func(t *testing.T) {
			args := (&Worker{Encoder: tc.enc}).encoderArgs("scale=-2:720", "2800k")
			all := strings.Join(args, " ")
			for _, want := range tc.wantParts {
				if !strings.Contains(all, want) {
					t.Fatalf("encoder=%s args missing %q: %v", tc.enc, want, args)
				}
			}
		})
	}
}

func TestRunHLSSuccessAndFailure(t *testing.T) {
	t.Parallel()

	t.Run("success updates done", func(t *testing.T) {
		db := newTranscodeTestDB(t)
		res, err := db.Exec(`INSERT INTO transcode_task (file_id, quality, status, progress) VALUES ('f1', 'abr:360p', 'waiting', 0)`)
		if err != nil {
			t.Fatalf("insert task: %v", err)
		}
		taskID, _ := res.LastInsertId()

		w := &Worker{
			DB:           db,
			FFmpegPath:   writeMockFFmpegRunner(t, false),
			TranscodeDir: t.TempDir(),
			Encoder:      EncoderNVENC, // verify hw path still works in run
			running:      map[int64]context.CancelFunc{},
		}
		ladder := []Rendition{{Name: "720p", Height: 720, Width: 1280, VideoRate: "2800k", AudioRate: "128k", Bandwidth: 3300000}}
		outDir := filepath.Join(t.TempDir(), "out")

		if err := w.runHLS(context.Background(), taskID, "input.mp4", outDir, ladder); err != nil {
			t.Fatalf("runHLS error: %v", err)
		}

		var status string
		var progress int
		var outPath sql.NullString
		if err := db.QueryRow(`SELECT status, progress, output_path FROM transcode_task WHERE id = ?`, taskID).Scan(&status, &progress, &outPath); err != nil {
			t.Fatalf("query task: %v", err)
		}
		if status != "done" || progress != 100 {
			t.Fatalf("unexpected task status=%s progress=%d", status, progress)
		}
		if !outPath.Valid || strings.TrimSpace(outPath.String) == "" {
			t.Fatalf("output path missing: %#v", outPath)
		}
		if _, err := os.Stat(filepath.Join(outDir, "master.m3u8")); err != nil {
			t.Fatalf("master playlist missing: %v", err)
		}
	})

	t.Run("failure updates failed", func(t *testing.T) {
		db := newTranscodeTestDB(t)
		res, err := db.Exec(`INSERT INTO transcode_task (file_id, quality, status, progress) VALUES ('f2', 'abr:360p', 'waiting', 0)`)
		if err != nil {
			t.Fatalf("insert task: %v", err)
		}
		taskID, _ := res.LastInsertId()

		w := &Worker{
			DB:           db,
			FFmpegPath:   writeMockFFmpegRunner(t, true),
			TranscodeDir: t.TempDir(),
			Encoder:      EncoderQSV,
			running:      map[int64]context.CancelFunc{},
		}
		ladder := []Rendition{{Name: "360p", Height: 360, Width: 640, VideoRate: "850k", AudioRate: "96k", Bandwidth: 1000000}}
		err = w.runHLS(context.Background(), taskID, "input.mp4", filepath.Join(t.TempDir(), "out2"), ladder)
		if err == nil {
			t.Fatal("expected error, got nil")
		}

		var status string
		var progress int
		var errMsg sql.NullString
		if err := db.QueryRow(`SELECT status, progress, error_message FROM transcode_task WHERE id = ?`, taskID).Scan(&status, &progress, &errMsg); err != nil {
			t.Fatalf("query task: %v", err)
		}
		if status != "failed" || progress != 0 {
			t.Fatalf("unexpected failed task status=%s progress=%d", status, progress)
		}
		if !errMsg.Valid || strings.TrimSpace(errMsg.String) == "" {
			t.Fatalf("error_message should exist: %#v", errMsg)
		}
	})
}
