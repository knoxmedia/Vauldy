package musicparse

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"knox-media/internal/scraper"
	"knox-media/internal/textencoding"
)

const UnknownAlbum = "[Unknown Album]"
const VariousArtists = "Various Artists"

// TrackMeta holds normalized music metadata for a single audio file.
type TrackMeta struct {
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	AlbumArtist string `json:"album_artist"`
	Album       string `json:"album"`
	TrackNumber int    `json:"track_number"`
	DiscNumber  int    `json:"disc_number"`
	Year        int    `json:"year"`
	Genre       string `json:"genre"`
	SampleRate  int    `json:"sample_rate"`
}

var artistTitleRE = regexp.MustCompile(`^(.+?)\s*[-–—]\s*(.+)$`)
var trackNumRE = regexp.MustCompile(`^(\d{1,3})(?:\s*/\s*\d+)?$`)

// IsMusicLibraryType reports whether the library type should use music parsing.
func IsMusicLibraryType(libraryType string) bool {
	return strings.EqualFold(strings.TrimSpace(libraryType), "music")
}

// ParseFromSources extracts metadata using tag → filename → directory priority.
func ParseFromSources(filePath, ffprobeJSON string, durationSec, bitrate int) TrackMeta {
	meta := parseFFprobeTags(ffprobeJSON)
	base := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))

	if strings.TrimSpace(meta.Title) == "" {
		meta.Title = base
	}
	if strings.TrimSpace(meta.Title) == "" {
		meta.Title = "Untitled"
	}

	if strings.TrimSpace(meta.Artist) == "" && strings.TrimSpace(meta.AlbumArtist) == "" {
		if artist, title, ok := parseFilenamePattern(base); ok {
			if strings.TrimSpace(meta.Title) == base || strings.TrimSpace(meta.Title) == "" {
				meta.Title = title
			}
			meta.Artist = artist
		}
	}

	if strings.TrimSpace(meta.Album) == "" {
		if album, artist := parseDirectoryLayout(filePath); album != "" {
			meta.Album = album
			if strings.TrimSpace(meta.Artist) == "" && artist != "" {
				meta.Artist = artist
			}
		}
	}

	meta.Title = strings.TrimSpace(meta.Title)
	meta.Artist = normalizeArtist(meta.Artist)
	meta.AlbumArtist = normalizeArtist(meta.AlbumArtist)
	meta.Album = strings.TrimSpace(meta.Album)
	meta.Genre = strings.TrimSpace(meta.Genre)

	if meta.Album == "" {
		meta.Album = UnknownAlbum
	}
	if meta.AlbumArtist == "" {
		meta.AlbumArtist = pickAlbumArtist(meta.Artist, meta.Album)
	}
	if meta.Artist == "" {
		meta.Artist = meta.AlbumArtist
	}
	if meta.AlbumArtist == "" {
		meta.AlbumArtist = meta.Artist
	}
	if meta.AlbumArtist == "" {
		meta.AlbumArtist = VariousArtists
	}

	if meta.Title == "" {
		meta.Title = base
	}
	return RepairTrackMeta(meta)
}

func pickAlbumArtist(trackArtist, album string) string {
	if album == UnknownAlbum {
		return VariousArtists
	}
	parts := splitArtists(trackArtist)
	if len(parts) > 1 {
		return VariousArtists
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return VariousArtists
}

func normalizeArtist(raw string) string {
	parts := splitArtists(raw)
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return strings.Join(parts, " / ")
}

func splitArtists(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	raw = strings.ReplaceAll(raw, "；", ";")
	for _, sep := range []string{";", "/"} {
		if strings.Contains(raw, sep) {
			var out []string
			for _, p := range strings.Split(raw, sep) {
				p = strings.TrimSpace(p)
				if p != "" {
					out = append(out, p)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	return []string{raw}
}

func parseFilenamePattern(base string) (artist, title string, ok bool) {
	base = strings.TrimSpace(base)
	m := artistTitleRE.FindStringSubmatch(base)
	if len(m) != 3 {
		return "", "", false
	}
	artist = strings.TrimSpace(m[1])
	title = strings.TrimSpace(m[2])
	if artist == "" || title == "" {
		return "", "", false
	}
	// Skip "03 - Track Title" track-number prefixes.
	if trackNumRE.MatchString(artist) {
		return "", "", false
	}
	return artist, title, true
}

func parseDirectoryLayout(filePath string) (album, artist string) {
	dir := filepath.Dir(filePath)
	albumDir := filepath.Base(dir)
	parentDir := filepath.Base(filepath.Dir(dir))
	rootDir := filepath.Base(filepath.Dir(filepath.Dir(dir)))

	if albumDir == "" || albumDir == "." {
		return "", ""
	}
	// Heuristic: .../Artist/Album/track.ext or .../Album/track.ext
	if parentDir != "" && parentDir != "." && !isGenericFolder(parentDir) {
		if isGenericFolder(rootDir) || rootDir == "." {
			return albumDir, parentDir
		}
	}
	if !isGenericFolder(albumDir) {
		return albumDir, ""
	}
	return "", ""
}

func isGenericFolder(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "music", "audio", "flac", "mp3", "wav", "download", "downloads", "temp", "tmp":
		return true
	default:
		return false
	}
}

func parseFFprobeTags(rawJSON string) TrackMeta {
	var meta TrackMeta
	rawJSON = strings.TrimSpace(rawJSON)
	if rawJSON == "" {
		return meta
	}
	var root struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
		Streams []struct {
			CodecType string            `json:"codec_type"`
			SampleRate string           `json:"sample_rate"`
			Tags       map[string]string `json:"tags"`
		} `json:"streams"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &root); err != nil {
		return meta
	}
	tags := root.Format.Tags
	meta.Title = textencoding.FixMetadataString(tagValue(tags, "title", "TIT2", "TITLE"))
	meta.Artist = textencoding.FixMetadataString(tagValue(tags, "artist", "TPE1", "ARTIST"))
	meta.AlbumArtist = textencoding.FixMetadataString(tagValue(tags, "album_artist", "TPE2", "ALBUM_ARTIST", "album artist"))
	meta.Album = textencoding.FixMetadataString(tagValue(tags, "album", "TALB", "ALBUM"))
	meta.Genre = textencoding.FixMetadataString(tagValue(tags, "genre", "TCON", "GENRE"))
	meta.Year = parseYear(tagValue(tags, "date", "TDRC", "YEAR", "DATE", "TYER"))
	meta.TrackNumber = parseTrackNumber(tagValue(tags, "track", "TRCK", "TRACK"))
	meta.DiscNumber = parseTrackNumber(tagValue(tags, "disc", "TPOS", "DISC"))
	for _, st := range root.Streams {
		if st.CodecType == "audio" && st.SampleRate != "" {
			if sr, err := strconv.Atoi(strings.TrimSpace(st.SampleRate)); err == nil && sr > 0 {
				meta.SampleRate = sr
			}
			break
		}
	}
	return meta
}

func tagValue(tags map[string]string, keys ...string) string {
	if len(tags) == 0 {
		return ""
	}
	lower := make(map[string]string, len(tags))
	for k, v := range tags {
		lower[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	for _, key := range keys {
		if v, ok := lower[strings.ToLower(key)]; ok && v != "" {
			return v
		}
	}
	return ""
}

func parseYear(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if len(raw) >= 4 {
		if y, err := strconv.Atoi(raw[:4]); err == nil && y >= 1900 && y <= 2100 {
			return y
		}
	}
	if y, err := strconv.Atoi(raw); err == nil && y >= 1900 && y <= 2100 {
		return y
	}
	return 0
}

func parseTrackNumber(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if m := trackNumRE.FindStringSubmatch(raw); len(m) == 2 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n
		}
	}
	if n, err := strconv.Atoi(raw); err == nil {
		return n
	}
	return 0
}

// NormKey normalizes a string for deduplication lookups.
func NormKey(s string) string {
	return strings.ToLower(strings.TrimSpace(scraper.NormalizeTitle(s)))
}
