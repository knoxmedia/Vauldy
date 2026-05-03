package subtitle

import (
	"path/filepath"
	"regexp"
	"strings"
)

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// DetectLanguageFromFilename infers BCP-47-like short codes (zh, en, ja, ko) from a subtitle file name.
func DetectLanguageFromFilename(name string) (code string, ok bool) {
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)))
	base = nonAlnum.ReplaceAllString(base, " ")
	base = strings.TrimSpace(base)
	if base == "" {
		return "", false
	}
	tokens := strings.Fields(base)
	for _, t := range tokens {
		if c, hit := tokenToLang(t); hit {
			return c, true
		}
	}
	// Whole-string patterns (e.g. "chs", "cht" as standalone segment already handled)
	if c, hit := tokenToLang(base); hit {
		return c, true
	}
	return "", false
}

func tokenToLang(s string) (string, bool) {
	switch s {
	case "zh", "chi", "chs", "cht", "sc", "tc", "mandarin", "cn", "chinese":
		return "zh", true
	case "en", "eng", "english", "英":
		return "en", true
	case "ja", "jp", "jpn", "japanese", "日":
		return "ja", true
	case "ko", "kor", "korean", "韩":
		return "ko", true
	case "fr", "fra", "fre", "french":
		return "fr", true
	case "de", "ger", "deu", "german":
		return "de", true
	case "es", "spa", "spanish":
		return "es", true
	case "ru", "rus", "russian":
		return "ru", true
	case "pt", "por", "portuguese":
		return "pt", true
	case "it", "ita", "italian":
		return "it", true
	}
	return "", false
}

// NormalizeFFprobeLang maps ffprobe three-letter / locale tags to short codes.
func NormalizeFFprobeLang(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return ""
	}
	if idx := strings.IndexByte(s, '-'); idx > 0 {
		s = s[:idx]
	}
	switch s {
	case "chi", "zho", "cmn", "yue", "cht", "chs":
		return "zh"
	case "eng":
		return "en"
	case "jpn":
		return "ja"
	case "kor":
		return "ko"
	case "fre", "fra":
		return "fr"
	case "ger", "deu":
		return "de"
	case "spa":
		return "es"
	case "rus":
		return "ru"
	case "por":
		return "pt"
	case "ita":
		return "it"
	default:
		if len(s) == 2 {
			return s
		}
		return s
	}
}
