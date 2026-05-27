package textencoding

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// FixMetadataString repairs common mojibake in embedded metadata (GBK or UTF-8 read as Latin-1).
func FixMetadataString(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	best := s
	bestScore := scoreMetadata(s)
	for _, attempt := range repairAttempts(s) {
		if sc := scoreMetadata(attempt); sc > bestScore {
			best = attempt
			bestScore = sc
		}
	}
	return best
}

func scoreMetadata(s string) int {
	if s == "" || !utf8.ValidString(s) {
		return -1000
	}
	if strings.ContainsRune(s, '\uFFFD') {
		return -500
	}
	score := len([]rune(s))
	han := 0
	highLatin := 0
	for _, r := range s {
		switch {
		case r >= 0x4e00 && r <= 0x9fff:
			han++
		case r >= 0x80 && r <= 0xff:
			highLatin++
		}
	}
	score += han * 12
	score -= highLatin * 4
	if looksLikeGBKAsLatin1(s) && han == 0 {
		score -= 50
	}
	return score
}

func looksLikeGBKAsLatin1(s string) bool {
	highLatin := 0
	han := 0
	for _, r := range s {
		switch {
		case r >= 0x4e00 && r <= 0x9fff:
			han++
		case r >= 0x80 && r <= 0xff:
			highLatin++
		}
	}
	return highLatin >= 2 && han == 0 && len(s) >= 3
}

func repairAttempts(raw string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 6)
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || s == raw {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	stripped := stripReplacementRunes(raw)
	utf8Misread := looksLikeUTF8AsLatin1(raw) || looksLikeUTF8AsLatin1(stripped)
	if utf8Misread {
		add(repairUTF8FromLatin1(raw))
		add(repairUTF8FromLatin1(stripped))
	}
	if !utf8Misread && (looksLikeGBKAsLatin1(raw) || looksLikeGBKAsLatin1(stripped)) {
		add(decodeGBKBytes(latin1Bytes(raw)))
		add(decodeGBKBytes(latin1Bytes(stripped)))
		add(repairMixedGBKLatin1(raw))
		add(repairMixedGBKLatin1(stripped))
	}
	return out
}

// repairMixedGBKLatin1 decodes GBK mojibake runes and preserves trailing ASCII (e.g. "(sc.chinaz.com)").
func repairMixedGBKLatin1(s string) string {
	var latin []byte
	var out strings.Builder
	flush := func() {
		if len(latin) == 0 {
			return
		}
		if fixed := decodeGBKBytes(latin); fixed != "" {
			out.WriteString(fixed)
		} else {
			for _, b := range latin {
				out.WriteByte(b)
			}
		}
		latin = latin[:0]
	}
	for _, r := range s {
		if r >= 0x80 && r <= 0xff {
			latin = append(latin, byte(r))
			continue
		}
		flush()
		out.WriteRune(r)
	}
	flush()
	return out.String()
}

func looksLikeUTF8AsLatin1(s string) bool {
	b := latin1Bytes(s)
	if len(b) < 3 || !utf8.Valid(b) {
		return false
	}
	han := 0
	for _, r := range string(b) {
		if r >= 0x4e00 && r <= 0x9fff {
			han++
		}
	}
	return han > 0
}

func stripReplacementRunes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\uFFFD' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func latin1Bytes(s string) []byte {
	b := make([]byte, 0, len(s))
	for _, r := range s {
		if r <= 0xff {
			b = append(b, byte(r))
		}
	}
	return b
}

func decodeGBKBytes(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	out, err := simplifiedchinese.GBK.NewDecoder().Bytes(b)
	if err != nil || !utf8.Valid(out) {
		return ""
	}
	return string(out)
}

func repairUTF8FromLatin1(s string) string {
	b := latin1Bytes(s)
	if len(b) == 0 {
		return ""
	}
	if len(b) != len([]rune(s)) {
		b = latin1Bytes(stripReplacementRunes(s))
	}
	if len(b) == 0 || !utf8.Valid(b) {
		return ""
	}
	return string(b)
}
