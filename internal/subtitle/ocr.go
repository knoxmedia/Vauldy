package subtitle

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

)

// OCRConfig enables Tesseract-based extraction for bitmap (PGS / VobSub) subtitles.
type OCRConfig struct {
	Enabled        bool
	TesseractPath  string
	TessdataPrefix string
	Languages      string
	PythonPath     string
	ScriptPath     string
	PgsripPath     string
	MkvextractPath string
	MkvmergePath   string
}

func defaultString(s, def string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	return s
}

// RunBitmapSubtitleOCR invokes the Python helper (Tesseract + pgsrip / mkvtoolnix for tracks).
func (s *Service) RunBitmapSubtitleOCR(ctx context.Context, mediaID int64, videoPath string, streamIndex int, outVtt string) error {
	if strings.TrimSpace(s.OCR.ScriptPath) == "" {
		return fmt.Errorf("graphical_ocr.script_path not set")
	}
	py := defaultString(s.OCR.PythonPath, "python3")
	if runtime.GOOS == "windows" {
		py = defaultString(s.OCR.PythonPath, "python")
	}
	input, stdin, cleanup, err := s.openVideoPipeInput(mediaID, videoPath)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}
	args := []string{
		s.OCR.ScriptPath,
		"--input", input,
		"--stream-index", strconv.Itoa(streamIndex),
		"--output-vtt", outVtt,
		"--tesseract", defaultString(s.OCR.TesseractPath, "tesseract"),
		"--ffmpeg", s.FFmpegPath,
		"--ffprobe", s.FFprobePath,
	}
	lang := strings.TrimSpace(s.OCR.Languages)
	if lang != "" {
		args = append(args, "--lang", lang)
	}
	if p := strings.TrimSpace(s.OCR.TessdataPrefix); p != "" {
		args = append(args, "--tessdata-prefix", p)
	}
	if p := strings.TrimSpace(s.OCR.PgsripPath); p != "" {
		args = append(args, "--pgsrip", p)
	}
	if p := strings.TrimSpace(s.OCR.MkvextractPath); p != "" {
		args = append(args, "--mkvextract", p)
	}
	if p := strings.TrimSpace(s.OCR.MkvmergePath); p != "" {
		args = append(args, "--mkvmerge", p)
	}
	cmd := exec.CommandContext(ctx, py, args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	cmd.Env = os.Environ()
	if p := strings.TrimSpace(s.OCR.TessdataPrefix); p != "" {
		cmd.Env = append(cmd.Env, "TESSDATA_PREFIX="+p)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, trimBytes(out))
	}
	return nil
}

// RunVobSubIdxOCR runs OCR on a VobSub pair (.idx + .sub).
func (s *Service) RunVobSubIdxOCR(ctx context.Context, idxPath, outVtt string) error {
	if strings.TrimSpace(s.OCR.ScriptPath) == "" {
		return fmt.Errorf("graphical_ocr.script_path not set")
	}
	py := defaultString(s.OCR.PythonPath, "python3")
	if runtime.GOOS == "windows" {
		py = defaultString(s.OCR.PythonPath, "python")
	}
	args := []string{
		s.OCR.ScriptPath,
		"--mode", "vobsub",
		"--vobsub-idx", idxPath,
		"--output-vtt", outVtt,
		"--tesseract", defaultString(s.OCR.TesseractPath, "tesseract"),
		"--ffmpeg", s.FFmpegPath,
		"--ffprobe", s.FFprobePath,
	}
	if lang := strings.TrimSpace(s.OCR.Languages); lang != "" {
		args = append(args, "--lang", lang)
	}
	if p := strings.TrimSpace(s.OCR.TessdataPrefix); p != "" {
		args = append(args, "--tessdata-prefix", p)
	}
	cmd := exec.CommandContext(ctx, py, args...)
	cmd.Env = os.Environ()
	if p := strings.TrimSpace(s.OCR.TessdataPrefix); p != "" {
		cmd.Env = append(cmd.Env, "TESSDATA_PREFIX="+p)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, trimBytes(out))
	}
	return nil
}
