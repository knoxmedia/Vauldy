package subtitle

import (
	"fmt"
	"regexp"
	"strings"
)

type Format int

const (
	FormatUnknown Format = iota
	FormatVTT
	FormatSRT
	FormatASS
	FormatLRC
)

type Cue struct {
	Start string
	End   string
	Text  string
}

var (
	vttTimeArrow = regexp.MustCompile(`^\s*(\d[\d:.,]*)\s*-->\s*(\d[\d:.,]*)(?:\s+(.*))?$`)
	srtTSLine    = regexp.MustCompile(`^(\d{2}:\d{2}:\d{2},\d{3})\s*-->\s*(\d{2}:\d{2}:\d{2},\d{3})\s*(.*)$`)
	assDialogue  = regexp.MustCompile(`(?i)^Dialogue:\s*\d+,([^,]*),([^,]*),([^,]*),([^,]*),([^,]*),([^,]*),([^,]*),([^,]*),(.*)$`)
	cueIndexLine = regexp.MustCompile(`^\d+$`)
	// lrcTimestamp matches a single LRC timestamp tag like [00:12.34] or [1:02.345].
	lrcTimestamp = regexp.MustCompile(`^\[\d{1,2}:\d{1,2}\.\d{1,3}\]`)
)

func stripUTF8BOM(s string) string {
	return strings.TrimPrefix(s, "\ufeff")
}

func DetectFormat(content, srcURL string) Format {
	trim := strings.TrimSpace(stripUTF8BOM(content))
	lowerURL := strings.ToLower(srcURL)
	switch {
	case strings.HasPrefix(trim, "WEBVTT"):
		return FormatVTT
	case strings.Contains(lowerURL, ".ass"):
		return FormatASS
	case strings.Contains(lowerURL, ".srt"):
		return FormatSRT
	case strings.Contains(lowerURL, ".lrc"):
		return FormatLRC
	case strings.Contains(strings.ToUpper(trim), "[SCRIPT INFO]") || assDialogue.MatchString(trim):
		return FormatASS
	case isLRCContent(trim):
		return FormatLRC
	case srtTSLine.MatchString(trim) || strings.Contains(trim, "-->"):
		if strings.Contains(trim, ",") && strings.Count(trim, "-->") > 0 {
			return FormatSRT
		}
		return FormatVTT
	default:
		return FormatUnknown
	}
}

// isLRCContent reports whether content looks like LRC lyrics: at least one line
// begins with a [mm:ss.xx] timestamp tag. Metadata-only LRC (e.g. [ti:..]) is
// treated as unknown since there are no lyric cues to proofread.
func isLRCContent(trim string) bool {
	for _, line := range splitLines(trim) {
		if lrcTimestamp.MatchString(strings.TrimSpace(line)) {
			return true
		}
	}
	return false
}

func ParseCues(content string, format Format) ([]Cue, Format, error) {
	if format == FormatUnknown {
		format = DetectFormat(content, "")
	}
	switch format {
	case FormatASS:
		cues, err := parseASSCues(content)
		return cues, FormatASS, err
	case FormatSRT:
		cues, err := parseSRTCues(content)
		return cues, FormatSRT, err
	case FormatLRC:
		cues, err := parseLRCCues(content)
		return cues, FormatLRC, err
	default:
		cues, err := parseVTTCues(content)
		return cues, FormatVTT, err
	}
}

func parseVTTCues(content string) ([]Cue, error) {
	content = stripUTF8BOM(content)
	lines := splitLines(content)
	var cues []Cue
	i := 0
	if len(lines) > 0 {
		first := strings.TrimSpace(lines[0])
		if strings.HasPrefix(first, "WEBVTT") {
			i = 1
			for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
				i++
			}
		}
	}
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "NOTE") || strings.HasPrefix(line, "STYLE") || strings.HasPrefix(line, "REGION") {
			i++
			continue
		}
		if cueIndexLine.MatchString(line) && i+1 < len(lines) {
			i++
			line = strings.TrimSpace(lines[i])
		}
		m := vttTimeArrow.FindStringSubmatch(line)
		if m == nil {
			i++
			continue
		}
		i++
		var textLines []string
		for i < len(lines) && strings.TrimSpace(lines[i]) != "" {
			textLines = append(textLines, lines[i])
			i++
		}
		cues = append(cues, Cue{Start: strings.TrimSpace(m[1]), End: strings.TrimSpace(m[2]), Text: strings.Join(textLines, "\n")})
		for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
			i++
		}
	}
	if len(cues) == 0 {
		return nil, fmt.Errorf("no subtitle cues found")
	}
	return cues, nil
}

func parseSRTCues(content string) ([]Cue, error) {
	lines := splitLines(content)
	var cues []Cue
	i := 0
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			i++
			continue
		}
		if regexp.MustCompile(`^\d+$`).MatchString(line) {
			i++
			if i >= len(lines) {
				break
			}
			line = strings.TrimSpace(lines[i])
		}
		m := srtTSLine.FindStringSubmatch(line)
		if m == nil {
			i++
			continue
		}
		i++
		var textLines []string
		for i < len(lines) && strings.TrimSpace(lines[i]) != "" {
			textLines = append(textLines, lines[i])
			i++
		}
		cues = append(cues, Cue{Start: m[1], End: m[2], Text: strings.Join(textLines, "\n")})
	}
	if len(cues) == 0 {
		return nil, fmt.Errorf("no subtitle cues found")
	}
	return cues, nil
}

func parseASSCues(content string) ([]Cue, error) {
	lines := splitLines(content)
	var cues []Cue
	for _, line := range lines {
		m := assDialogue.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		cues = append(cues, Cue{
			Start: m[1],
			End:   m[2],
			Text:  m[9],
		})
	}
	if len(cues) == 0 {
		return nil, fmt.Errorf("no ass dialogue lines found")
	}
	return cues, nil
}

// parseLRCCues parses LRC lyrics into cues. Each [mm:ss.xx] prefix becomes a cue
// whose Start holds the verbatim tag and Text holds the lyric text. Lines without
// a leading timestamp (metadata tags like [ti:..], blank lines) are skipped.
// Multi-timestamp lines ([00:01.00][00:05.00]text) expand into one cue per tag.
func parseLRCCues(content string) ([]Cue, error) {
	lines := splitLines(content)
	var cues []Cue
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		rest := line
		var stamps []string
		for strings.HasPrefix(rest, "[") {
			end := strings.Index(rest, "]")
			if end < 0 {
				break
			}
			tag := rest[:end+1]
			if !lrcTimestamp.MatchString(tag) {
				break
			}
			stamps = append(stamps, tag)
			rest = rest[end+1:]
		}
		if len(stamps) == 0 {
			continue
		}
		text := strings.TrimSpace(rest)
		for _, s := range stamps {
			cues = append(cues, Cue{Start: s, Text: text})
		}
	}
	if len(cues) == 0 {
		return nil, fmt.Errorf("no lrc cues found")
	}
	return cues, nil
}

func RenderCues(cues []Cue, format Format) string {
	switch format {
	case FormatASS:
		return renderASS(cues)
	case FormatSRT:
		return renderSRT(cues)
	case FormatLRC:
		return renderLRC(cues)
	default:
		return renderVTT(cues)
	}
}

func renderVTT(cues []Cue) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for i, c := range cues {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n", i+1, c.Start, c.End, c.Text)
	}
	return b.String()
}

func renderSRT(cues []Cue) string {
	var b strings.Builder
	for i, c := range cues {
		if i > 0 {
			b.WriteByte('\n')
		}
		start := strings.ReplaceAll(c.Start, ".", ",")
		end := strings.ReplaceAll(c.End, ".", ",")
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n", i+1, start, end, c.Text)
	}
	return b.String()
}

func renderASS(cues []Cue) string {
	var b strings.Builder
	b.WriteString("[Script Info]\nScriptType: v4.00+\n\n[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\nStyle: Default,Arial,20,&H00FFFFFF,&H000000FF,&H00000000,&H80000000,0,0,0,0,100,100,0,0,1,2,0,2,10,10,10,1\n\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")
	for _, c := range cues {
		fmt.Fprintf(&b, "Dialogue: 0,%s,%s,Default,,0,0,0,,%s\n", c.Start, c.End, c.Text)
	}
	return b.String()
}

func renderLRC(cues []Cue) string {
	var b strings.Builder
	for _, c := range cues {
		b.WriteString(c.Start)
		b.WriteString(c.Text)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}

// NormalizeForPowerPlayer rewrites WebVTT/SRT cues with numeric indices for PowerPlayer 6 parser.
func NormalizeForPowerPlayer(content string) (string, error) {
	content = stripUTF8BOM(content)
	format := DetectFormat(content, "")
	cues, _, err := ParseCues(content, format)
	if err != nil {
		return "", err
	}
	if format == FormatASS {
		return RenderCues(cues, FormatASS), nil
	}
	return renderVTT(cues), nil
}
