package config

import (
	"runtime"
	"strings"
)

// localizeDefaultConfig adjusts embedded tool paths for the host OS before first write.
func localizeDefaultConfig(raw []byte) []byte {
	s := string(raw)
	if runtime.GOOS == "windows" {
		repl := []struct{ old, new string }{
			{`tools/ffmpeg/bin/ffprobe`, `tools/ffmpeg/bin/ffprobe.exe`},
			{`tools/ffmpeg/bin/ffmpeg`, `tools/ffmpeg/bin/ffmpeg.exe`},
			{`tools/shaka-packager/packager`, `tools/shaka-packager/packager.exe`},
			{`tools/recognition/.venv/bin/python`, `tools/recognition/.venv/Scripts/python.exe`},
			{`tools/recognition/.venv/bin/whisper`, `tools/recognition/.venv/Scripts/whisper.exe`},
			{`tools/recognition/.venv/bin/pgsrip`, `tools/recognition/.venv/Scripts/pgsrip.exe`},
			{`tools/tesseract/tesseract`, `tools/tesseract/tesseract.exe`},
			{`tools/doctran/LibreOfficePortable/App/libreoffice/program/soffice`, `tools/doctran/LibreOfficePortable/App/libreoffice/program/soffice.exe`},
		}
		for _, r := range repl {
			s = strings.ReplaceAll(s, r.old, r.new)
		}
		s = strings.Replace(
			s,
			`shell: '"tools/recognition/.venv/bin/python" "tools/asr/asr_to_vtt.py"`,
			`shell: cd /d "{output_dir}" && "tools/recognition/.venv/Scripts/python.exe" "tools/asr/asr_to_vtt.py"`,
			1,
		)
	}
	return []byte(s)
}
