package scraper

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var (
	splitNoise       = regexp.MustCompile(`[._\-+]+`)
	bracketCharsRE = regexp.MustCompile(`[\[【(（\]】)）]`)
	yearPattern      = regexp.MustCompile(`(?:^|[\s._-])((?:19|20)\d{2})(?:$|[\s._-])`)
	englishNoiseRE   = regexp.MustCompile(`(?i)\b(?:bluray|bdrip|brrip|webrip|web-?dl|hdrip|dvdrip|hdtv|uhd|hd|4k|8k|3d|x264|x265|h\.?264|h\.?265|hevc|avc|aac|ac3|dts|truehd|atmos|hdr10|hdr10\+|dv|dovi|remux|repack|proper|extended|unrated|director'?s?\.?cut|imax|10bit|8bit|1080p|2160p|1440p|720p|480p|576p|540p|cd\d|disc\d|disk\d|vol\d|chs|cht|gb|cn|eng|english|chinese|mandarin|cantonese|dual|audio|subs?|subbed|dubbed|complete|limited|special|edition|collectors?)\b`)
)

// Chinese release / source tags often glued to titles without separators.
var chineseNoiseSubstrings = []string{
	"中英双字", "国英双语", "国粤双语", "粤英双语", "中日双语", "中韩双语",
	"简体中字", "繁体中字", "中英字幕", "中日字幕", "双语字幕",
	"国英双音", "国粤双音", "双音轨", "双字幕",
	"中英", "中字", "双语", "双字", "字幕",
	"国英", "国粤", "粤英", "国语", "粤语", "台配", "港版", "韩版", "美版", "日版",
	"内封", "内嵌", "外挂", "封套", "礼盒",
	"抢先版", "枪版", "高清", "超清", "蓝光", "原盘", "正版",
	"完整版", "未删减", "删减", "导演剪辑", "加长版", "修复版", "重制版",
	"收藏版", "终极版", "典藏版", "纪念版", "特效版", "剧场版",
}

var techNoiseWordRE = regexp.MustCompile(`(?i)^(ld|xt|yyh3d|d\d+)$`)

var noiseTokenSet = func() map[string]struct{} {
	tokens := []string{
		"hd", "uhd", "4k", "8k", "3d", "hdr", "dv", "aac", "ac3", "dts",
		"chs", "cht", "gb", "cn", "eng", "sub", "subs", "audio", "dual",
		"repack", "proper", "remux", "webdl", "webrip", "bdrip", "dvdrip",
		"bluray", "hdtv", "x264", "x265", "hevc", "avc", "imax", "ld", "d9",
	}
	m := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		m[t] = struct{}{}
	}
	return m
}()

func NormalizeTitle(raw string) string {
	keyword, _ := ExtractSearch(raw)
	if keyword != "" {
		return keyword
	}
	return normalizeRawTitle(raw)
}

func ExtractSearch(raw string) (keyword string, year int) {
	keyword, _, year = ExtractSearchTerms(raw)
	return keyword, year
}

func normalizeRawTitle(raw string) string {
	v := strings.TrimSpace(raw)
	v = bracketCharsRE.ReplaceAllString(v, " ")
	v = insertScriptBoundaries(v)
	v = splitNoise.ReplaceAllString(v, " ")
	v = stripChineseNoise(v)
	v = englishNoiseRE.ReplaceAllString(v, " ")
	v = strings.Join(strings.Fields(v), " ")
	return strings.TrimSpace(v)
}

func insertScriptBoundaries(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i, r := range runes {
		if i > 0 {
			prev := runes[i-1]
			if isLatinOrDigit(prev) && isCJK(r) {
				b.WriteRune(' ')
			} else if isCJK(prev) && isLatinOrDigit(r) && !isNumericSequelSuffix(runes, i) {
				b.WriteRune(' ')
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isNumericSequelSuffix matches 1–2 trailing digits after CJK (e.g. 流浪地球2, 速度与激情10).
func isNumericSequelSuffix(runes []rune, start int) bool {
	n := 0
	for j := start; j < len(runes); j++ {
		r := runes[j]
		if r < '0' || r > '9' {
			return n > 0 && n <= 2
		}
		n++
		if n > 2 {
			return false
		}
	}
	return n > 0 && n <= 2
}

func stripChineseNoise(s string) string {
	v := s
	for _, p := range chineseNoiseSubstrings {
		v = strings.ReplaceAll(v, p, " ")
	}
	return v
}

func extractYear(s string) int {
	m := yearPattern.FindStringSubmatch(" " + strings.ReplaceAll(s, ".", " ") + " ")
	if len(m) < 2 {
		return 0
	}
	y, err := strconv.Atoi(strings.TrimSpace(m[1]))
	if err != nil || y < 1900 || y > 2099 {
		return 0
	}
	return y
}

func removeYearToken(s string, year int) string {
	ys := strconv.Itoa(year)
	v := yearPattern.ReplaceAllString(" "+s+" ", " ")
	v = strings.ReplaceAll(v, ys, " ")
	return strings.Join(strings.Fields(v), " ")
}

func pickSearchKeyword(clean string) string {
	tokens := strings.Fields(clean)
	if len(tokens) == 0 {
		return ""
	}
	kept := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if isNoiseToken(t) {
			continue
		}
		kept = append(kept, t)
	}
	if len(kept) == 0 {
		return ""
	}

	var han []string
	var latin []string
	for _, t := range kept {
		if containsHan(t) {
			if !isSiteNameSegment(t) {
				han = append(han, t)
			}
			continue
		}
		if len(t) >= 2 {
			latin = append(latin, t)
		}
	}
	if len(han) > 0 {
		best := ""
		for _, seg := range han {
			if len([]rune(seg)) > len([]rune(best)) {
				best = seg
			}
		}
		return best
	}
	return strings.Join(latin, " ")
}

func isNoiseToken(t string) bool {
	_, ok := noiseTokenSet[strings.ToLower(strings.Trim(t, "."))]
	return ok
}

func isTechNoiseWord(w string) bool {
	return techNoiseWordRE.MatchString(strings.ToLower(strings.TrimSpace(w)))
}

func containsHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}

func isLatinOrDigit(r rune) bool {
	return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}

// orderProvidersForKeyword puts Chinese-friendly sources first when the title contains Han characters.
func orderProvidersForKeyword(providers []string, keyword string) []string {
	if !containsHan(keyword) {
		return providers
	}
	// Align with nowen-video Provider Chain priority for CJK titles: 豆瓣 → Bangumi → TMDb.
	boost := []string{"douban", "bangumi", "tmdb", "tvdb"}
	seen := make(map[string]struct{}, len(providers)+len(boost))
	out := make([]string, 0, len(providers)+len(boost))
	for _, name := range boost {
		for _, p := range providers {
			n := strings.ToLower(strings.TrimSpace(p))
			if n != name {
				continue
			}
			if _, ok := seen[n]; ok {
				continue
			}
			seen[n] = struct{}{}
			out = append(out, p)
			break
		}
	}
	for _, p := range providers {
		n := strings.ToLower(strings.TrimSpace(p))
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, p)
	}
	return out
}
