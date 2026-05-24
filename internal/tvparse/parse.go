package tvparse

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"knox-media/internal/scraper"
)

// EpisodeInfo holds parsed TV episode metadata from a file path.
type EpisodeInfo struct {
	SeriesTitle     string
	SeriesTitleNorm string
	Year            int
	SeasonNum       int
	EpisodeNum      int
	IsSpecial       bool
	TMDBID          string
	TVDBID          string
	EpisodeTitle    string
	SourceFolder    string
}

var (
	sxxeyyRE = regexp.MustCompile(`(?i)(?:^|[\s._\-(\[])(?:[Ss](?:eason\s*)?(\d{1,2})[Ee](?:pisode\s*)?(\d{1,3})|[Ss](\d{1,2})[Ee](\d{1,3}))(?:[\s._\-)\]]|$)`)
	// Standalone S01E01 without word boundaries in some releases.
	sxxeyyCompactRE = regexp.MustCompile(`(?i)\b[Ss](\d{1,2})[Ee](\d{1,3})\b`)
	seasonFolderRE  = regexp.MustCompile(`(?i)(?:^|[/\\])(?:season|saison|s)\s*(\d{1,2})(?:[/\\]|$)`)
	specialsFolderRE = regexp.MustCompile(`(?i)(?:^|[/\\])specials?(?:[/\\]|$)`)
	tmdbIDRE        = regexp.MustCompile(`(?i)(?:\[|\{|\.|\s)tmdbid\s*[=:]\s*(\d+)`)
	tvdbIDRE        = regexp.MustCompile(`(?i)(?:\[|\{|\.|\s)tvdbid\s*[=:]\s*(\d+)`)
	yearParenRE     = regexp.MustCompile(`\(\s*((?:19|20)\d{2})\s*\)`)
	qualityNoiseRE  = regexp.MustCompile(`(?i)\b(?:2160p|1080p|720p|480p|4k|8k|uhd|hdr|hdr10|dv|dovi|remux|bluray|web-?dl|webrip|hdtv|x264|x265|hevc|h\.?264|h\.?265|aac|dts|atmos|10bit|8bit|dual|audio|multi|complete|extended|unrated|proper|repack)\b`)
	// 去有风的地方第1集 / 去有风的地方 第1集 / 第01集 / 第1话 / 第1期
	cnTitleEpisodeRE = regexp.MustCompile(`^(.+?)\s*第\s*0*(\d{1,4})\s*[集话期]\s*$`)
	cnEpisodeRE      = regexp.MustCompile(`第\s*0*(\d{1,4})\s*[集话期]`)
	cnEpisodeOnlyRE  = regexp.MustCompile(`^\s*第\s*0*(\d{1,4})\s*[集话期]\s*$`)
	cnSeasonSuffixRE = regexp.MustCompile(`^(.+?)第\s*([一二三四五六七八九十两〇零\d]{1,4})\s*季$`)
	// S03E01 without ASCII word boundary (e.g. 奇思妙探S03E01).
	sxxeyyAnywhereRE = regexp.MustCompile(`(?i)[Ss](\d{1,2})[Ee](\d{1,3})`)
	// EP01 / E01 / 01 (仅当位于剧集文件夹内时使用)
	episodeOnlyRE = regexp.MustCompile(`(?i)(?:^|[\s._\-(\[])E(?:P(?:isode)?)?\s*0*(\d{1,4})(?:[\s._\-)\]]|$)`)
	numericOnlyRE = regexp.MustCompile(`^0*(\d{1,4})$`)
)

// IsTVLibraryType reports whether the library type should use TV episode parsing.
func IsTVLibraryType(libraryType string) bool {
	switch strings.ToLower(strings.TrimSpace(libraryType)) {
	case "tv", "anime", "television", "series":
		return true
	default:
		return false
	}
}

// ParseVideoPath extracts TV episode info from an absolute file path.
// Supports SxxEyy, 第N集/话/期, EP01, and numeric-only names inside a show folder.
func ParseVideoPath(filePath string) (info EpisodeInfo, ok bool) {
	filePath = filepath.Clean(strings.TrimSpace(filePath))
	if filePath == "" {
		return info, false
	}
	base := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	fullPathSlash := filepath.ToSlash(filePath)
	showFolder := seriesFolderName(filePath)

	season, episode, seriesFromName, matched := parseSeasonEpisode(base, fullPathSlash, showFolder)
	if !matched {
		return info, false
	}

	info.SeasonNum = season
	info.EpisodeNum = episode
	info.IsSpecial = season == 0
	if !info.IsSpecial && info.SeasonNum <= 0 {
		info.SeasonNum = 1
	}

	info.TMDBID = extractID(fullPathSlash, tmdbIDRE)
	info.TVDBID = extractID(fullPathSlash, tvdbIDRE)

	info.SeriesTitle, info.Year = extractSeriesTitle(filePath, base, showFolder, seriesFromName, season, episode)
	info.SeriesTitleNorm = NormalizeSeriesTitle(info.SeriesTitle)
	if info.SeriesTitleNorm == "" && strings.TrimSpace(info.SeriesTitle) != "" {
		info.SeriesTitleNorm = strings.ToLower(strings.TrimSpace(info.SeriesTitle))
	}
	info.SourceFolder = extractSourceFolder(filePath)
	info.EpisodeTitle = extractEpisodeTitle(base, season, episode)
	return info, info.SeriesTitle != "" && info.EpisodeNum > 0
}

func parseSeasonEpisode(baseName, fullPath, showFolder string) (season, episode int, seriesFromName string, ok bool) {
	if m := sxxeyyRE.FindStringSubmatch(baseName); len(m) >= 3 {
		s, e := pickIntPair(m[1], m[2], m[3], m[4])
		if s >= 0 && e > 0 {
			return defaultSeasonFromFolder(s, showFolder), e, "", true
		}
	}
	if m := sxxeyyCompactRE.FindStringSubmatch(baseName); len(m) >= 3 {
		s, err1 := strconv.Atoi(m[1])
		e, err2 := strconv.Atoi(m[2])
		if err1 == nil && err2 == nil && s >= 0 && e > 0 {
			return defaultSeasonFromFolder(s, showFolder), e, "", true
		}
	}
	if m := sxxeyyAnywhereRE.FindStringSubmatch(baseName); len(m) >= 3 {
		s, err1 := strconv.Atoi(m[1])
		e, err2 := strconv.Atoi(m[2])
		if err1 == nil && err2 == nil && s >= 0 && e > 0 {
			return defaultSeasonFromFolder(s, showFolder), e, "", true
		}
	}
	// 去有风的地方第1集
	if m := cnTitleEpisodeRE.FindStringSubmatch(strings.TrimSpace(baseName)); len(m) >= 3 {
		if ep, err := strconv.Atoi(m[2]); err == nil && ep > 0 {
			seriesFromName = strings.TrimSpace(m[1])
			return seasonFromShowFolder(showFolder), ep, seriesFromName, true
		}
	}
	// 文件名含 第N集 但剧名取自文件夹
	if m := cnEpisodeOnlyRE.FindStringSubmatch(strings.TrimSpace(baseName)); len(m) >= 2 {
		if ep, err := strconv.Atoi(m[1]); err == nil && ep > 0 && isValidShowFolder(showFolder) {
			return seasonFromShowFolder(showFolder), ep, "", true
		}
	}
	if m := cnEpisodeRE.FindStringSubmatch(baseName); len(m) >= 2 {
		if ep, err := strconv.Atoi(m[1]); err == nil && ep > 0 {
			return seasonFromShowFolder(showFolder), ep, "", true
		}
	}
	if m := episodeOnlyRE.FindStringSubmatch(baseName); len(m) >= 2 {
		if ep, err := strconv.Atoi(m[1]); err == nil && ep > 0 {
			return seasonFromShowFolder(showFolder), ep, "", true
		}
	}
	// 同目录下纯数字命名：01 / 001（需有效剧集文件夹）
	if m := numericOnlyRE.FindStringSubmatch(strings.TrimSpace(baseName)); len(m) >= 2 {
		if ep, err := strconv.Atoi(m[1]); err == nil && ep > 0 && isValidShowFolder(showFolder) {
			return seasonFromShowFolder(showFolder), ep, "", true
		}
	}
	// 宿醉1 / 宿醉2 / 奇思妙探03 — 文件名末尾数字且前缀匹配文件夹剧名
	if ep, ok := parseTrailingNumberEpisode(baseName, showFolder); ok {
		return seasonFromShowFolder(showFolder), ep, "", true
	}
	if specialsFolderRE.MatchString(fullPath) {
		if ep := parseEpisodeOnlyLegacy(baseName); ep > 0 {
			return 0, ep, "", true
		}
	}
	if m := seasonFolderRE.FindStringSubmatch(fullPath); len(m) >= 2 {
		if s, err := strconv.Atoi(m[1]); err == nil && s >= 0 {
			if ep := parseEpisodeOnlyLegacy(baseName); ep > 0 {
				return s, ep, "", true
			}
		}
	}
	return 0, 0, "", false
}

func defaultSeasonFromFolder(season int, showFolder string) int {
	if season == 0 {
		return 0
	}
	if season > 0 {
		return season
	}
	return seasonFromShowFolder(showFolder)
}

func seasonFromShowFolder(showFolder string) int {
	if _, s, ok := parseSeasonInTitle(showFolder); ok && s > 0 {
		return s
	}
	return 1
}

func seriesFolderName(filePath string) string {
	dir := filepath.Dir(filePath)
	name := filepath.Base(dir)
	if isSeasonFolderName(name) || specialsFolderRE.MatchString(filepath.ToSlash(dir)) {
		name = filepath.Base(filepath.Dir(dir))
	}
	return strings.TrimSpace(name)
}

func isValidShowFolder(name string) bool {
	name = cleanSeriesFolderName(name)
	return name != ""
}

// ShowFolderName returns the inferred series folder name for a media file path.
func ShowFolderName(filePath string) string {
	return seriesFolderName(filePath)
}

// IsValidShowFolderName reports whether a directory name represents a TV series folder.
func IsValidShowFolderName(name string) bool {
	return isValidShowFolder(name)
}

// BuildEpisodeInfoFromFolder constructs episode metadata from folder name and episode number.
func BuildEpisodeInfoFromFolder(filePath, showFolder string, episodeNum int) EpisodeInfo {
	title := strings.TrimSpace(showFolder)
	season := seasonFromShowFolder(showFolder)
	if sTitle, s, ok := parseSeasonInTitle(showFolder); ok && strings.TrimSpace(sTitle) != "" {
		title = strings.TrimSpace(sTitle)
		if s > 0 {
			season = s
		}
	}
	title = cleanSeriesFolderName(title)
	if title == "" {
		title = strings.TrimSpace(showFolder)
	}
	year := extractYearFromTitle(title)
	if year > 0 {
		title = stripYearFromTitle(title)
	}
	info := EpisodeInfo{
		SeriesTitle:     strings.TrimSpace(title),
		SeasonNum:       season,
		EpisodeNum:      episodeNum,
		SourceFolder:    extractSourceFolder(filePath),
		EpisodeTitle:    "第" + strconv.Itoa(episodeNum) + "集",
	}
	info.SeriesTitleNorm = NormalizeSeriesTitle(info.SeriesTitle)
	if info.SeriesTitleNorm == "" && info.SeriesTitle != "" {
		info.SeriesTitleNorm = strings.ToLower(info.SeriesTitle)
	}
	return info
}

// ParseLooseEpisodeNumber extracts an episode number from a basename when strict parsing fails.
func ParseLooseEpisodeNumber(baseName, showFolder string) (int, bool) {
	baseName = strings.TrimSpace(baseName)
	if baseName == "" || !isValidShowFolder(showFolder) {
		return 0, false
	}
	if m := cnTitleEpisodeRE.FindStringSubmatch(baseName); len(m) >= 3 {
		if ep, err := strconv.Atoi(m[2]); err == nil && ep > 0 {
			return ep, true
		}
	}
	if m := cnEpisodeOnlyRE.FindStringSubmatch(baseName); len(m) >= 2 {
		if ep, err := strconv.Atoi(m[1]); err == nil && ep > 0 {
			return ep, true
		}
	}
	if m := cnEpisodeRE.FindStringSubmatch(baseName); len(m) >= 2 {
		if ep, err := strconv.Atoi(m[1]); err == nil && ep > 0 {
			return ep, true
		}
	}
	if m := numericOnlyRE.FindStringSubmatch(baseName); len(m) >= 2 {
		if ep, err := strconv.Atoi(m[1]); err == nil && ep > 0 {
			return ep, true
		}
	}
	if m := sxxeyyAnywhereRE.FindStringSubmatch(baseName); len(m) >= 3 {
		if ep, err := strconv.Atoi(m[2]); err == nil && ep > 0 {
			return ep, true
		}
	}
	return parseTrailingNumberEpisode(baseName, showFolder)
}

func parseTrailingNumberEpisode(baseName, showFolder string) (int, bool) {
	baseName = strings.TrimSpace(baseName)
	m := regexp.MustCompile(`^(.+?)[\s._\-]*0*(\d{1,4})$`).FindStringSubmatch(baseName)
	if len(m) < 3 {
		return 0, false
	}
	prefix := strings.TrimSpace(m[1])
	ep, err := strconv.Atoi(m[2])
	if err != nil || ep <= 0 || ep > 9999 {
		return 0, false
	}
	seriesName := showFolder
	if sTitle, _, ok := parseSeasonInTitle(showFolder); ok && strings.TrimSpace(sTitle) != "" {
		seriesName = strings.TrimSpace(sTitle)
	}
	if titlePrefixMatches(prefix, seriesName, showFolder) {
		return ep, true
	}
	return 0, false
}

func titlePrefixMatches(prefix, seriesName, showFolder string) bool {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return false
	}
	for _, ref := range []string{seriesName, showFolder, cleanSeriesFolderName(showFolder)} {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if strings.EqualFold(prefix, ref) || strings.HasPrefix(ref, prefix) || strings.HasPrefix(prefix, ref) {
			return true
		}
	}
	return false
}

func pickIntPair(a1, a2, b1, b2 string) (int, int) {
	if a1 != "" && a2 != "" {
		s, err1 := strconv.Atoi(a1)
		e, err2 := strconv.Atoi(a2)
		if err1 == nil && err2 == nil {
			return s, e
		}
	}
	if b1 != "" && b2 != "" {
		s, err1 := strconv.Atoi(b1)
		e, err2 := strconv.Atoi(b2)
		if err1 == nil && err2 == nil {
			return s, e
		}
	}
	return -1, 0
}

var legacyEpisodeOnlyRE = regexp.MustCompile(`(?i)(?:^|[\s._\-(\[])E(?:pisode\s*)?(\d{1,3})(?:[\s._\-)\]]|$)`)

func parseEpisodeOnlyLegacy(baseName string) int {
	if m := legacyEpisodeOnlyRE.FindStringSubmatch(baseName); len(m) >= 2 {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

func extractID(path string, re *regexp.Regexp) string {
	if m := re.FindStringSubmatch(path); len(m) >= 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func extractSeriesTitle(filePath, baseName, showFolder, seriesFromName string, season, episode int) (title string, year int) {
	if strings.TrimSpace(seriesFromName) != "" {
		title = strings.TrimSpace(seriesFromName)
		if sTitle, _, ok := parseSeasonInTitle(title); ok && strings.TrimSpace(sTitle) != "" {
			title = strings.TrimSpace(sTitle)
		}
	} else {
		title = showFolder
		if sTitle, _, ok := parseSeasonInTitle(title); ok && strings.TrimSpace(sTitle) != "" {
			title = strings.TrimSpace(sTitle)
		}
	}

	title = cleanSeriesFolderName(title)
	if title == "" {
		title = titleBeforeEpisodeMarker(baseName, season, episode)
		if m := cnTitleEpisodeRE.FindStringSubmatch(strings.TrimSpace(baseName)); len(m) >= 2 {
			title = strings.TrimSpace(m[1])
		}
		title = scraper.NormalizeTitle(title)
	}
	if title == "" {
		title = strings.TrimSpace(showFolder)
		if sTitle, _, ok := parseSeasonInTitle(title); ok && strings.TrimSpace(sTitle) != "" {
			title = strings.TrimSpace(sTitle)
		}
	}

	year = extractYearFromTitle(title)
	if year > 0 {
		title = stripYearFromTitle(title)
	}
	title = strings.TrimSpace(title)
	return title, year
}

// parseSeasonInTitle splits "奇思妙探第三季" → ("奇思妙探", 3, true).
func parseSeasonInTitle(name string) (series string, season int, ok bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", 0, false
	}
	if m := cnSeasonSuffixRE.FindStringSubmatch(name); len(m) >= 3 {
		series = strings.TrimSpace(m[1])
		season = parseChineseOrArabicNumber(strings.TrimSpace(m[2]))
		if series != "" && season > 0 {
			return series, season, true
		}
	}
	if m := regexp.MustCompile(`(?i)^(.+?)\s*[Ss](?:eason\s*)?(\d{1,2})$`).FindStringSubmatch(name); len(m) >= 3 {
		series = strings.TrimSpace(m[1])
		if s, err := strconv.Atoi(m[2]); err == nil && series != "" && s > 0 {
			return series, s, true
		}
	}
	return "", 0, false
}

func parseChineseOrArabicNumber(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if n, err := strconv.Atoi(raw); err == nil {
		return n
	}
	cnDigits := map[rune]int{
		'零': 0, '〇': 0, '一': 1, '二': 2, '两': 2, '三': 3, '四': 4,
		'五': 5, '六': 6, '七': 7, '八': 8, '九': 9, '十': 10,
	}
	if len([]rune(raw)) == 1 {
		if n, ok := cnDigits[[]rune(raw)[0]]; ok {
			return n
		}
	}
	// 十一、十二、二十、二十三
	runes := []rune(raw)
	total := 0
	cur := 0
	for _, r := range runes {
		switch r {
		case '十':
			if cur == 0 {
				cur = 1
			}
			total += cur * 10
			cur = 0
		default:
			if n, ok := cnDigits[r]; ok {
				cur = n
			}
		}
	}
	total += cur
	if total > 0 {
		return total
	}
	return 0
}

func isSeasonFolderName(name string) bool {
	name = strings.TrimSpace(name)
	if specialsFolderRE.MatchString("/" + name + "/") {
		return true
	}
	return seasonFolderRE.MatchString("/" + name + "/") || regexp.MustCompile(`(?i)^[Ss]\d{1,2}$`).MatchString(name)
}

func cleanSeriesFolderName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == "." {
		return ""
	}
	// Skip generic container folders.
	lower := strings.ToLower(name)
	switch lower {
	case "tv", "television", "series", "anime", "shows", "video", "videos", "4k", "1080p", "2160p",
		"movies", "movie", "film", "films":
		return ""
	}
	switch name {
	case "电视剧", "剧集", "国产剧", "日韩剧", "美剧", "港剧", "台剧", "英剧", "泰剧", "韩剧", "日剧":
		return ""
	}
	name = qualityNoiseRE.ReplaceAllString(name, " ")
	name = strings.Join(strings.Fields(name), " ")
	return strings.Trim(name, " ._-")
}

func extractYearFromTitle(title string) int {
	if m := yearParenRE.FindStringSubmatch(title); len(m) >= 2 {
		if y, err := strconv.Atoi(m[1]); err == nil && y >= 1900 && y <= 2099 {
			return y
		}
	}
	parsed := scraper.ParseMediaFilename(title)
	return parsed.Year
}

func stripYearFromTitle(title string) string {
	title = yearParenRE.ReplaceAllString(title, " ")
	return strings.TrimSpace(strings.Join(strings.Fields(title), " "))
}

func titleBeforeEpisodeMarker(baseName string, season, episode int) string {
	markers := []string{
		regexp.QuoteMeta(formatSxxEyy(season, episode)),
	}
	lower := strings.ToLower(baseName)
	for _, m := range []string{"s" + pad2(season) + "e" + padEp(episode), "s" + strconv.Itoa(season) + "e" + strconv.Itoa(episode)} {
		if idx := strings.Index(lower, strings.ToLower(m)); idx > 0 {
			return strings.TrimSpace(baseName[:idx])
		}
	}
	_ = markers
	if m := sxxeyyCompactRE.FindStringIndex(baseName); m != nil && m[0] > 0 {
		return strings.TrimSpace(baseName[:m[0]])
	}
	return baseName
}

func formatSxxEyy(season, episode int) string {
	return "S" + pad2(season) + "E" + padEp(episode)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

func padEp(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

func extractEpisodeTitle(baseName string, season, episode int) string {
	if m := cnTitleEpisodeRE.FindStringSubmatch(strings.TrimSpace(baseName)); len(m) >= 3 {
		return "第" + m[2] + "集"
	}
	marker := sxxeyyCompactRE.FindString(baseName)
	if marker == "" {
		return ""
	}
	idx := strings.Index(strings.ToLower(baseName), strings.ToLower(marker))
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(baseName[idx+len(marker):])
	rest = strings.Trim(rest, " ._-[]()")
	rest = qualityNoiseRE.ReplaceAllString(rest, " ")
	return strings.TrimSpace(strings.Join(strings.Fields(rest), " "))
}

func extractSourceFolder(filePath string) string {
	dir := filepath.Clean(filepath.Dir(filePath))
	if isSeasonFolderName(filepath.Base(dir)) || specialsFolderRE.MatchString(filepath.ToSlash(dir)) {
		dir = filepath.Dir(dir)
	}
	return dir
}

// FormatEpisodeLabel returns a human-readable episode label for media titles.
func FormatEpisodeLabel(info EpisodeInfo) string {
	if info.EpisodeNum <= 0 {
		return info.SeriesTitle
	}
	if containsHan(info.SeriesTitle) || cnEpisodeRE.MatchString(info.EpisodeTitle) {
		label := info.SeriesTitle + " - 第" + strconv.Itoa(info.EpisodeNum) + "集"
		if info.SeasonNum > 1 {
			label = info.SeriesTitle + " - 第" + strconv.Itoa(info.SeasonNum) + "季第" + strconv.Itoa(info.EpisodeNum) + "集"
		}
		return label
	}
	return info.SeriesTitle + " - S" + pad2(info.SeasonNum) + "E" + padEp(info.EpisodeNum)
}

func containsHan(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

// ParseStoredTVMeta reads episode info previously saved in media meta_json.tv during scan.
func ParseStoredTVMeta(metaJSON string) (EpisodeInfo, bool) {
	metaJSON = strings.TrimSpace(metaJSON)
	if metaJSON == "" {
		return EpisodeInfo{}, false
	}
	var root map[string]any
	if json.Unmarshal([]byte(metaJSON), &root) != nil || root == nil {
		return EpisodeInfo{}, false
	}
	tv, _ := root["tv"].(map[string]any)
	if tv == nil {
		return EpisodeInfo{}, false
	}
	info := EpisodeInfo{
		SeriesTitle: stringField(tv["series_title"]),
		TMDBID:      stringField(tv["tmdb_id"]),
		TVDBID:      stringField(tv["tvdb_id"]),
		EpisodeTitle: stringField(tv["episode_title"]),
		SourceFolder: stringField(tv["source_folder"]),
		Year:        intField(tv["year"]),
		SeasonNum:   intField(tv["season"]),
		EpisodeNum:  intField(tv["episode"]),
		IsSpecial:   boolField(tv["is_special"]),
	}
	if info.SeriesTitle == "" || info.EpisodeNum <= 0 || info.SeasonNum < 0 {
		return EpisodeInfo{}, false
	}
	info.SeriesTitleNorm = NormalizeSeriesTitle(info.SeriesTitle)
	if info.SeriesTitleNorm == "" {
		info.SeriesTitleNorm = strings.ToLower(strings.TrimSpace(info.SeriesTitle))
	}
	return info, info.SeriesTitleNorm != ""
}

// ParseEpisodeFromMedia tries filename/path parsing first, then stored meta_json.tv.
func ParseEpisodeFromMedia(filePath, metaJSON string) (EpisodeInfo, bool) {
	if info, ok := ParseVideoPath(filePath); ok {
		return info, true
	}
	return ParseStoredTVMeta(metaJSON)
}

func stringField(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatInt(int64(x), 10)
	default:
		if v == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

func intField(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		return n
	default:
		return 0
	}
}

func boolField(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(strings.TrimSpace(x), "true") || x == "1"
	case float64:
		return x != 0
	default:
		return false
	}
}

// NormalizeSeriesTitle produces a merge key from a series display title.
func NormalizeSeriesTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	original := title
	title = qualityNoiseRE.ReplaceAllString(title, " ")
	title = stripYearFromTitle(title)
	// 中文剧名保留原样，避免 NormalizeTitle 过度清洗
	if !containsHan(title) {
		title = scraper.NormalizeTitle(title)
	} else {
		title = strings.TrimSpace(title)
	}
	if strings.TrimSpace(title) == "" {
		title = stripYearFromTitle(original)
	}
	title = strings.ToLower(title)
	replacer := strings.NewReplacer("'", "", "’", "", ":", " ", ".", " ", "_", " ", "-", " ")
	title = replacer.Replace(title)
	norm := strings.Join(strings.Fields(title), " ")
	if norm == "" {
		raw := qualityNoiseRE.ReplaceAllString(original, " ")
		raw = stripYearFromTitle(raw)
		return strings.ToLower(strings.Join(strings.Fields(raw), " "))
	}
	return norm
}
