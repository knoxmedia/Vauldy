package storage

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"knox-media/internal/processmetrics"
	"strings"

	kcrypto "knox-media/internal/crypto"
	"knox-media/internal/keystore"
	"knox-media/pkg/ffprobe"
)

// FFmpegLaunchCount returns the process-wide number of actual ffmpeg launch attempts.
func FFmpegLaunchCount() uint64 { return processmetrics.FFmpegLaunchCount() }

// MediaProbe holds ffprobe results and optional pipe cleanup for encrypted sources.
type MediaProbe struct {
	Summary *ffprobe.Summary
	Cleanup func()
}

// ProbeMediaFile probes media, decrypting Knox .enc via pipe when needed.
func ProbeMediaFile(db *sql.DB, vault *keystore.Vault, ffprobePath string, mediaID int64, path string, beforeInput []string) (*MediaProbe, error) {
	in, err := OpenFFmpegInput(db, vault, mediaID, path, 0)
	if err != nil {
		return nil, err
	}
	input, stdin := inputLabelAndStdin(in)
	sum, err := ffprobe.ProbeOptionsIO(ffprobePath, beforeInput, input, stdin)
	if err != nil {
		if in.Cleanup != nil {
			in.Cleanup()
		}
		return nil, err
	}
	return &MediaProbe{Summary: sum, Cleanup: in.Cleanup}, nil
}

// FFprobeOutput runs ffprobe with caller-built args; the final argument must be the input path or pipe:0.
func FFprobeOutput(db *sql.DB, vault *keystore.Vault, ffprobePath string, mediaID int64, path string, startSec, durationSec float64, argsBeforeInput []string) ([]byte, func(), error) {
	return FFprobeOutputContext(context.Background(), db, vault, ffprobePath, mediaID, path, startSec, durationSec, argsBeforeInput)
}

func FFprobeOutputContext(ctx context.Context, db *sql.DB, vault *keystore.Vault, ffprobePath string, mediaID int64, path string, startSec, durationSec float64, argsBeforeInput []string) ([]byte, func(), error) {
	in, err := OpenFFmpegInput(db, vault, mediaID, path, 0)
	if err != nil {
		return nil, nil, err
	}
	input, stdin := inputLabelAndStdin(in)
	full := append(append([]string{}, argsBeforeInput...), input)
	out, err := ffprobe.OutputContext(ctx, ffprobePath, full, stdin)
	if err != nil {
		if in.Cleanup != nil {
			in.Cleanup()
		}
		return nil, nil, err
	}
	return out, in.Cleanup, nil
}

// ProbePath runs ffprobe on path, using decrypt pipe for Knox .enc when mediaID is known.
func ProbePath(db *sql.DB, vault *keystore.Vault, ffprobePath string, mediaID int64, path string, beforeInput []string) (*ffprobe.Summary, error) {
	if mediaID > 0 && vault != nil && InputNeedsPipe(db, mediaID, path) {
		mp, err := ProbeMediaFile(db, vault, ffprobePath, mediaID, path, beforeInput)
		if err != nil {
			return nil, err
		}
		if mp.Cleanup != nil {
			defer mp.Cleanup()
		}
		return mp.Summary, nil
	}
	return ffprobe.ProbeOptions(ffprobePath, path, beforeInput)
}

// RunFFmpeg runs ffmpeg with decrypted pipe input when the catalog path is .enc.
// preInput is inserted before -i (e.g. -ss for plaintext seek); postInput follows -i.
// workDir sets cmd.Dir when non-empty (e.g. CMAF init segment output).
func RunFFmpeg(ctx context.Context, db *sql.DB, vault *keystore.Vault, ffmpegPath string, mediaID int64, path string, startSec, durationSec float64, preInput, postInput []string, workDir string) ([]byte, error) {
	return runFFmpeg(ctx, db, vault, ffmpegPath, mediaID, path, startSec, durationSec, preInput, postInput, workDir, nil)
}

// RunFFmpegWithLiveness is RunFFmpeg with a liveness callback: every chunk of
// output written by the process triggers report. Long-running executors use it
// to keep a task alive while ffmpeg makes progress. A process that stops
// emitting output for the dispatcher's progress-idle timeout is force-cancelled
// as stalled instead of being killed by a fixed wall-clock deadline. The output
// bytes returned mirror CombinedOutput exactly.
func RunFFmpegWithLiveness(ctx context.Context, db *sql.DB, vault *keystore.Vault, ffmpegPath string, mediaID int64, path string, startSec, durationSec float64, preInput, postInput []string, workDir string, report func()) ([]byte, error) {
	return runFFmpeg(ctx, db, vault, ffmpegPath, mediaID, path, startSec, durationSec, preInput, postInput, workDir, report)
}

func runFFmpeg(ctx context.Context, db *sql.DB, vault *keystore.Vault, ffmpegPath string, mediaID int64, path string, startSec, durationSec float64, preInput, postInput []string, workDir string, report func()) ([]byte, error) {
	in, err := OpenFFmpegInput(db, vault, mediaID, path, 0)
	if err != nil {
		return nil, err
	}
	if in.Cleanup != nil {
		defer in.Cleanup()
	}
	args := []string{"-y"}
	if len(preInput) > 0 {
		args = append(args, preInput...)
	}
	var stdin io.Reader
	args, stdin = ApplyFFmpegInput(args, in)
	if len(postInput) > 0 {
		args = append(args, postInput...)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := processmetrics.NewFFmpegCommandContext(ctx, ffmpegPath, args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	if stdin != nil {
		cmd.Stdin = stdin
	}
	if report == nil {
		return cmd.CombinedOutput()
	}
	var buf bytes.Buffer
	liveness := &livenessWriter{buf: &buf, report: report}
	cmd.Stdout = liveness
	cmd.Stderr = liveness
	if err := cmd.Run(); err != nil {
		return buf.Bytes(), err
	}
	return buf.Bytes(), nil
}

// livenessWriter buffers process output while reporting each write as liveness
// evidence, so the caller can observe that a long-running process is still
// making progress (ffmpeg writes progress lines while it works and goes silent
// only when it is stuck or finished).
type livenessWriter struct {
	buf    *bytes.Buffer
	report func()
}

func (w *livenessWriter) Write(p []byte) (int, error) {
	if w.report != nil && len(p) > 0 {
		w.report()
	}
	return w.buf.Write(p)
}

func inputLabelAndStdin(in *FFmpegInput) (string, io.Reader) {
	if in == nil || in.Path != "" {
		if in == nil {
			return "", nil
		}
		return in.Path, nil
	}
	return "pipe:0", in.Stdin
}

// InputNeedsPipe reports whether media at path requires decrypt pipe for ffmpeg/ffprobe.
func InputNeedsPipe(db *sql.DB, mediaID int64, path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	return kcrypto.IsEncFile(path)
}
