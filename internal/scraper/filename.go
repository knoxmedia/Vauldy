package scraper

import (
	"regexp"
	"strconv"
	"strings"
)

// ParsedMediaTitle is the result of parsing a media filename or dirty display title.
type ParsedMediaTitle struct {
	Title    string
	TitleAlt string
	Year     int
}

var (
	// [yyh3d.com] pure-domain site tags.
	siteTagRE = regexp.MustCompile(`(?i)\[[a-z0-9][a-z0-9\-]*\.[a-z]{2,}(?:\.[a-z]{2,})?\]`)
	// [电影天堂www.dytt8899.com] and similar CN release-site prefixes.
	siteBracketRE = regexp.MustCompile(`(?i)\[[^\]]*(?:www\.|\.com|\.net|\.cn|\.cc|dytt|ygdy|dy2018|mp4ba|xunlei|btbtd|6vhao|renren|gaoqing|高清下载|电影网|影视网|资源网|天堂|影视)[^\]]*\]`)
	cjkTitleRunRE     = regexp.MustCompile(`[\p{Han}][\p{Han}\d：:·・]*[\p{Han}\d]?`)
	leadingAwardRE    = regexp.MustCompile(`^\s*\d{1,3}\s*[届集期]\s*[\.\-_\s]*`)
	chineseAdRE         = regexp.MustCompile(`[【（\[(][^【】()\[\]（）]*(?:Q裙|Q群|V信|微信|QQ|公众号|十万度|推广)[^【】()\[\]（）]*[】）\])]`)
	chineseBookTitleRE  = regexp.MustCompile(`《([^《》]+)》`)
	chineseYearRangeRE  = regexp.MustCompile(`((?:19|20)\d{2})\s*[\-–—~～]\s*((?:19|20)\d{2})`)
	yearInNameRE        = regexp.MustCompile(`(?:^|[^0-9])((?:19|20)\d{2})(?:[^0-9]|$)`)
	latinTitleRunRE     = regexp.MustCompile(`[A-Za-z][A-Za-z0-9 '&:,\.\-]*[A-Za-z0-9]`)
	episodeMarkerRE     = regexp.MustCompile(`(?i)\bS\d{1,2}E\d{1,3}\b`)
	// 489155.com@ style release-site prefixes before the actual title.
	siteAtPrefixRE = regexp.MustCompile(`(?i)^\s*\d+(?:\.[a-z0-9][-a-z0-9]*)*@`)
	// Leading 【ai增强】 / [tag] release metadata before title.
	leadingReleaseBracketRE = regexp.MustCompile(`^(?:\s*[【\[（(][^【】()\[\]（）]{0,60}[】\]\)）]\s*)+`)
	knownMediaExtRE = regexp.MustCompile(`(?i)\.(mkv|mp4|avi|mov|wmv|flv|webm|m4v|ts|m2ts|mp3|flac|aac|wav|mka|ogg|oga|wma|ape|alac)$`)
	trailingVideoTagRE = regexp.MustCompile(`(?i)[\s._\-]+(?:hd|bd|uhd|br|dvd|webrip|web-?dl|bluray|remux|tc|cam|ts|scr|rip|4k|8k|1080p|2160p|1440p|720p|480p|576p|540p|x264|x265|hevc|h\.?264|h\.?265|aac|dts|ac3|truehd|atmos|10bit|8bit)(?:[\s._\-]*[\p{Han}a-z0-9]+)*$`)
	trailingCNReleaseRE = regexp.MustCompile(`[\s._\-]+(?:国[英粤印韩日]双[语字]|中[英日韩]双[字语]|国[语粤]中字|[简繁]体中字|双语字幕|中文字幕|双字幕|双音轨|内封中字|内嵌中字|外挂字幕|听译字幕)(?:[\s._\-]*(?:国[英粤印韩日]双[语字]|中[英日韩]双[字语]|国[语粤]中字|[简繁]体中字|双语字幕|中文字幕|双字幕|双音轨))*$`)
	trailingCNEditionRE = regexp.MustCompile(`[\s._\-]+(?:高清|超清|蓝光|原盘|未删减|完整|修复|导演剪辑|加长|抢先|枪版|剧场|典藏|纪念|特效|终极|收藏)(?:版)?$`)
)

// ParseMediaFilename extracts search title/year from release filenames (nowen-video style).
func ParseMediaFilename(filename string) ParsedMediaTitle {
	out := ParsedMediaTitle{}
	if strings.TrimSpace(filename) == "" {
		return out
	}

	name := strings.TrimSpace(filename)
	// Strip release-site prefix before extension detection — dots in 489155.com@ are not file extensions.
	siteStripped := siteAtPrefixRE.ReplaceAllString(name, "")
	hadSiteAtPrefix := siteStripped != name
	name = stripMediaExtension(siteStripped)
	name = episodeMarkerRE.ReplaceAllString(name, " ")

	name = siteBracketRE.ReplaceAllString(name, " ")
	name = siteTagRE.ReplaceAllString(name, " ")
	name = leadingAwardRE.ReplaceAllString(name, " ")
	name = chineseAdRE.ReplaceAllString(name, " ")
	name = strings.ReplaceAll(name, "。", ".")
	name = strings.ReplaceAll(name, "　", " ")
	name = stripGluedReleaseSuffix(name)

	bracketStripped := leadingReleaseBracketRE.ReplaceAllString(name, "")
	hadReleaseBracket := bracketStripped != name
	name = bracketStripped

	if m := chineseYearRangeRE.FindStringSubmatch(name); len(m) >= 2 {
		if y, err := strconv.Atoi(m[1]); err == nil && y >= 1900 && y <= 2099 {
			out.Year = y
		}
		name = chineseYearRangeRE.ReplaceAllString(name, " ")
	}

	if ms := chineseBookTitleRE.FindAllStringSubmatch(name, -1); len(ms) > 0 {
		for _, m := range ms {
			inner := strings.TrimSpace(m[1])
			if inner == "" {
				continue
			}
			if out.Title == "" {
				out.Title = inner
				continue
			}
			if out.TitleAlt == "" && containsLatin(inner) {
				out.TitleAlt = inner
			}
		}
		name = chineseBookTitleRE.ReplaceAllString(name, " ")
	}

	if out.Title == "" && (hadSiteAtPrefix || hadReleaseBracket) {
		if cn := extractPrimaryChineseTitle(name); cn != "" {
			out.Title = cn
		}
	}

	name = bracketCharsRE.ReplaceAllString(name, " ")
	name = insertScriptBoundaries(name)
	name = splitNoise.ReplaceAllString(name, " ")
	name = stripChineseNoise(name)
	name = englishNoiseRE.ReplaceAllString(name, " ")

	if out.Year == 0 {
		if m := yearInNameRE.FindStringSubmatch(name); len(m) >= 2 {
			if y, err := strconv.Atoi(m[1]); err == nil && y >= 1900 && y <= 2099 {
				out.Year = y
			}
		}
	}
	if out.Year > 0 {
		name = removeYearToken(name, out.Year)
	}

	replacer := strings.NewReplacer(".", " ", "_", " ")
	clean := replacer.Replace(name)
	clean = strings.Join(strings.Fields(clean), " ")
	clean = strings.Trim(clean, " -·・")

	if out.Title == "" {
		if cn := pickBestChineseTitleSegment(clean); cn != "" {
			out.Title = cn
			if out.TitleAlt == "" {
				if en := pickLongestLatinSegment(clean); en != "" && en != cn {
					out.TitleAlt = en
				}
			}
		} else {
			out.Title = strings.TrimSpace(clean)
		}
	} else if out.TitleAlt == "" {
		if en := pickLongestLatinSegment(clean); en != "" {
			out.TitleAlt = en
		}
	}

	out.Title = strings.Trim(out.Title, " -·・:")
	out.TitleAlt = strings.Trim(out.TitleAlt, " -·・:")
	if out.Year == 0 {
		out.Year = extractYear(normalizeRawTitle(filename))
	}
	return out
}

func stripGluedReleaseSuffix(s string) string {
	v := strings.TrimSpace(s)
	for i := 0; i < 8; i++ {
		prev := v
		v = insertScriptBoundaries(v)
		v = trailingVideoTagRE.ReplaceAllString(v, "")
		v = trailingCNReleaseRE.ReplaceAllString(v, "")
		v = trailingCNEditionRE.ReplaceAllString(v, "")
		v = strings.TrimSpace(v)
		if v == prev {
			break
		}
	}
	return v
}

func containsLatin(s string) bool {
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			return true
		}
	}
	return false
}

// pickBestChineseTitleSegment chooses the longest CJK run that is not a release-site name.
func pickBestChineseTitleSegment(s string) string {
	runs := cjkTitleRunRE.FindAllString(s, -1)
	best := ""
	for _, run := range runs {
		run = strings.Trim(run, "：: ·・-")
		if len([]rune(run)) < 2 || isSiteNameSegment(run) {
			continue
		}
		if len([]rune(run)) > len([]rune(best)) {
			best = run
		}
	}
	if first := pickFirstChineseSegment(s); first != "" {
		if len([]rune(first)) > len([]rune(best)) {
			best = first
		}
	}
	if best != "" {
		best = strings.ReplaceAll(best, "：", ": ")
		best = strings.Join(strings.Fields(best), " ")
		return strings.Trim(best, " -·・:")
	}
	return pickFirstChineseSegment(s)
}

func extractPrimaryChineseTitle(name string) string {
	probe := bracketCharsRE.ReplaceAllString(name, "")
	probe = strings.ReplaceAll(probe, "@", " ")
	probe = insertScriptBoundaries(probe)
	probe = strings.Join(strings.Fields(probe), " ")
	return pickFirstChineseSegment(probe)
}

var knownSiteNameMarkers = []string{
	"电影天堂", "阳光电影", "人人影视", "飘花电影", "迅雷电影", "电影港",
	"6v电影", "66影视", "酷客影视", "片库网", "电影网", "影视网",
}

func isSiteNameSegment(s string) bool {
	s = strings.TrimSpace(s)
	for _, marker := range knownSiteNameMarkers {
		if strings.Contains(s, marker) {
			return true
		}
	}
	lower := strings.ToLower(s)
	return strings.Contains(lower, "www") || strings.Contains(lower, "dytt") ||
		strings.Contains(lower, ".com") || strings.Contains(lower, "ygdy")
}

// pickFirstChineseSegment keeps sub-titles like 蜡笔小新：灼热的春日部舞者 intact.
func pickFirstChineseSegment(s string) string {
	runes := []rune(s)
	start := -1
	for i, r := range runes {
		if isHanRune(r) {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	// If the first Han rune is a Chinese date/time marker (年/月/日/时/分/秒) preceded by
	// digits, include those digits so date-style titles like "6月27日" are not truncated
	// to "月27日". Whitespace/dots between the digits and the marker are skipped.
	if isCJKDateMarker(runes[start]) {
		j := start - 1
		for j >= 0 && (runes[j] == ' ' || runes[j] == '.') {
			j--
		}
		if j >= 0 && runes[j] >= '0' && runes[j] <= '9' {
			for j >= 0 && runes[j] >= '0' && runes[j] <= '9' {
				j--
			}
			start = j + 1
		}
	}
	end := start
	for i := len(runes) - 1; i >= start; i-- {
		if isHanRune(runes[i]) {
			end = i
			break
		}
	}
	var buf strings.Builder
	for i := start; i <= end; i++ {
		r := runes[i]
		if r == ' ' && i+1 <= end && runes[i+1] == ' ' {
			buf.WriteRune(' ')
			for i+1 <= end && runes[i+1] == ' ' {
				i++
			}
			continue
		}
		if r == '.' || r == '\t' {
			buf.WriteRune(' ')
			continue
		}
		buf.WriteRune(r)
	}
	if suffix := trailingSequelDigits(runes, end); suffix != "" {
		buf.WriteString(suffix)
	}
	result := strings.TrimSpace(buf.String())
	result = strings.ReplaceAll(result, "：", ": ")
	result = strings.Join(strings.Fields(result), " ")
	return strings.Trim(result, " -·・:")
}

func isHanRune(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF)
}

func trailingSequelDigits(runes []rune, end int) string {
	var digits string
	for j := end + 1; j < len(runes); j++ {
		r := runes[j]
		if r >= '0' && r <= '9' {
			digits += string(r)
			if len(digits) > 2 {
				return ""
			}
			continue
		}
		if digits != "" {
			return digits
		}
		if r != ' ' && r != '.' && r != '-' {
			return ""
		}
	}
	return digits
}

func pickLongestLatinSegment(s string) string {
	best := ""
	for _, m := range latinTitleRunRE.FindAllString(s, -1) {
		words := strings.Fields(strings.TrimSpace(m))
		kept := make([]string, 0, len(words))
		for _, w := range words {
			w = strings.Trim(w, ".")
			if len(w) < 2 || isNoiseToken(w) || isTechNoiseWord(w) {
				continue
			}
			kept = append(kept, w)
		}
		if len(kept) == 0 {
			continue
		}
		t := strings.Join(kept, " ")
		if len(t) > len(best) {
			best = t
		}
	}
	return best
}

func stripMediaExtension(name string) string {
	return knownMediaExtRE.ReplaceAllString(strings.TrimSpace(name), "")
}

func ExtractSearchTerms(raw string) (keyword, alt string, year int) {
	probe := strings.TrimSpace(raw)
	if probe == "" {
		return "", "", 0
	}
	parsed := ParseMediaFilename(probe)
	if parsed.Title != "" {
		return parsed.Title, parsed.TitleAlt, parsed.Year
	}
	keyword, year = extractSearchLegacy(probe)
	return keyword, "", year
}

// NormalizeSearchInput parses a release filename or dirty title into a scrape query and year.
func NormalizeSearchInput(raw string) (query string, year int) {
	query, _, year = ExtractSearchTerms(raw)
	if query != "" {
		return query, year
	}
	return strings.TrimSpace(raw), extractYear(normalizeRawTitle(raw))
}

func extractSearchLegacy(raw string) (keyword string, year int) {
	clean := normalizeRawTitle(raw)
	year = extractYear(clean)
	if year > 0 {
		clean = removeYearToken(clean, year)
	}
	keyword = pickSearchKeyword(clean)
	if keyword == "" {
		keyword = strings.TrimSpace(clean)
	}
	return keyword, year
}
