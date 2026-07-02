package musiclyrics

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var vttTimeArrow = regexp.MustCompile(`^\s*(\d[\d:.,]*)\s*-->\s*(\d[\d:.,]*)`)

// VTTToLRC converts WebVTT subtitle text to LRC lyrics (one timestamp per cue start).
func VTTToLRC(vtt string) string {
	cues := parseVTTCues(vtt)
	if len(cues) == 0 {
		return ""
	}
	var b strings.Builder
	for _, c := range cues {
		text := strings.TrimSpace(c.text)
		if text == "" {
			continue
		}
		b.WriteString(formatLRCTimestamp(c.startSec))
		b.WriteString(text)
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

type vttCue struct {
	startSec float64
	text     string
}

func parseVTTCues(vtt string) []vttCue {
	vtt = strings.ReplaceAll(vtt, "\r\n", "\n")
	vtt = strings.ReplaceAll(vtt, "\r", "\n")
	lines := strings.Split(vtt, "\n")
	var cues []vttCue
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "WEBVTT") || strings.HasPrefix(line, "NOTE") {
			continue
		}
		if strings.Contains(line, "-->") {
			m := vttTimeArrow.FindStringSubmatch(line)
			if len(m) < 2 {
				continue
			}
			start, err := parseVTTTime(m[1])
			if err != nil {
				continue
			}
			var textLines []string
			for j := i + 1; j < len(lines); j++ {
				next := strings.TrimSpace(lines[j])
				if next == "" {
					i = j
					break
				}
				if strings.Contains(next, "-->") {
					i = j - 1
					break
				}
				textLines = append(textLines, stripVTTTags(next))
				i = j
			}
			text := strings.TrimSpace(strings.Join(textLines, " "))
			if text != "" {
				cues = append(cues, vttCue{startSec: start, text: text})
			}
			continue
		}
	}
	return cues
}

func stripVTTTags(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func parseVTTTime(raw string) (float64, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, ",", "."))
	if raw == "" {
		return 0, fmt.Errorf("empty time")
	}
	parts := strings.Split(raw, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("invalid vtt time: %s", raw)
	}
	var h, m int
	var sec float64
	var err error
	switch len(parts) {
	case 2:
		m, err = strconv.Atoi(parts[0])
		if err != nil {
			return 0, err
		}
		sec, err = strconv.ParseFloat(parts[1], 64)
		if err != nil {
			return 0, err
		}
	case 3:
		h, err = strconv.Atoi(parts[0])
		if err != nil {
			return 0, err
		}
		m, err = strconv.Atoi(parts[1])
		if err != nil {
			return 0, err
		}
		sec, err = strconv.ParseFloat(parts[2], 64)
		if err != nil {
			return 0, err
		}
	}
	return float64(h*3600+m*60) + sec, nil
}

func formatLRCTimestamp(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	totalMs := int(sec*100 + 0.5) // centiseconds
	cs := totalMs % 100
	totalSec := totalMs / 100
	s := totalSec % 60
	m := (totalSec / 60) % 60
	return fmt.Sprintf("[%02d:%02d.%02d]", m, s, cs)
}

// ConvertVTTFile reads a .vtt file and writes LRC content to destPath.
func ConvertVTTFile(vttPath, destPath string) error {
	data, err := os.ReadFile(vttPath)
	if err != nil {
		return err
	}
	lrc := VTTToLRC(string(data))
	if strings.TrimSpace(lrc) == "" {
		return fmt.Errorf("no lyrics cues in vtt")
	}
	return os.WriteFile(destPath, []byte(lrc+"\n"), 0o644)
}
