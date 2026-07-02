package storage

import (
	"context"
	"database/sql"
	"io"
	"os/exec"
	"strings"

	kcrypto "knox-media/internal/crypto"
	"knox-media/internal/keystore"
	"knox-media/pkg/ffprobe"
)

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
	in, err := OpenFFmpegInput(db, vault, mediaID, path, 0)
	if err != nil {
		return nil, nil, err
	}
	input, stdin := inputLabelAndStdin(in)
	full := append(append([]string{}, argsBeforeInput...), input)
	out, err := ffprobe.Output(ffprobePath, full, stdin)
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
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	if stdin != nil {
		cmd.Stdin = stdin
	}
	return cmd.CombinedOutput()
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
