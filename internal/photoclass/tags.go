package photoclass

import (
	"encoding/hex"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// CategoryDef is a stable catalog entry for photo classification UI.
type CategoryDef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"` // scene | color | source
}

// CategoryCatalog is the canonical list (ASCII id + UTF-8 display name).
var CategoryCatalog = []CategoryDef{
	{ID: "people", Name: "人物", Kind: "scene"},
	{ID: "landscape", Name: "风景", Kind: "scene"},
	{ID: "food", Name: "美食", Kind: "scene"},
	{ID: "animal", Name: "动物", Kind: "scene"},
	{ID: "architecture", Name: "建筑", Kind: "scene"},
	{ID: "document", Name: "文档/截图", Kind: "scene"},
	{ID: "night", Name: "夜景", Kind: "scene"},
	{ID: "selfie", Name: "自拍", Kind: "scene"},
	{ID: "warm", Name: "暖色系", Kind: "color"},
	{ID: "cool", Name: "冷色系", Kind: "color"},
	{ID: "mono", Name: "黑白", Kind: "color"},
	{ID: "saturated", Name: "高饱和度", Kind: "color"},
	{ID: "camera", Name: "相机拍摄", Kind: "source"},
	{ID: "screenshot", Name: "手机截图", Kind: "source"},
	{ID: "download", Name: "下载保存", Kind: "source"},
}

var (
	nameToID   map[string]string
	idToName   map[string]string
	tagAliases map[string]string
	// Fingerprints of tags corrupted with U+FFFD (bytes lost; map to catalog id).
	garbledHexToID map[string]string
)

func init() {
	nameToID = make(map[string]string, len(CategoryCatalog))
	idToName = make(map[string]string, len(CategoryCatalog))
	for _, c := range CategoryCatalog {
		nameToID[c.Name] = c.ID
		idToName[c.ID] = c.Name
	}
	tagAliases = map[string]string{
		"保存": "下载保存", // common truncation / mis-parse
	}
	garbledHexToID = map[string]string{
		"efbfbdefbfbdefbfbdefbfbd":             "people",
		"efbfbddab0efbfbd":                     "mono",
		"efbfbde7beb0":                         "landscape",
		"efbfbdefbfbdc9abcfb5":                 "cool",
		"efbfbddfb1efbfbdefbfbdcdb6efbfbd":     "saturated",
		"efbfbdefbfbdcab3":                     "food",
		"d2b9efbfbdefbfbd":                     "night",
	}
}

// TagID returns stable id for a display tag name, or empty if unknown.
func TagID(name string) string {
	id, _ := ResolveTag(name)
	return id
}

// TagName returns display name for a category id.
func TagName(id string) string {
	return idToName[strings.TrimSpace(id)]
}

// IsBuiltinTag reports whether tag normalizes to a catalog name.
func IsBuiltinTag(tag string) bool {
	return TagID(tag) != ""
}

// ResolveTag maps a raw stored tag (possibly mojibake) to catalog id and display name.
func ResolveTag(raw string) (id, name string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if id, ok := nameToID[raw]; ok {
		return id, raw
	}
	if canon, ok := tagAliases[raw]; ok {
		return nameToID[canon], canon
	}
	for _, attempt := range repairAttempts(raw) {
		if id, ok := nameToID[attempt]; ok {
			return id, attempt
		}
		if canon, ok := tagAliases[attempt]; ok {
			return nameToID[canon], canon
		}
	}
	if name := matchCatalogFragment(raw); name != "" {
		return nameToID[name], name
	}
	if id = garbledHexID(raw); id != "" {
		return id, TagName(id)
	}
	return "", ""
}

// NormalizeTag repairs common mojibake and maps aliases to canonical UTF-8 names.
func NormalizeTag(raw string) string {
	if id, name := ResolveTag(raw); id != "" && name != "" {
		return name
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if utf8.ValidString(raw) {
		return raw
	}
	return raw
}

// TagIDs returns stable catalog ids for normalized tag labels.
func TagIDs(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		id, _ := ResolveTag(t)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// NormalizeTags deduplicates and normalizes a tag list for storage/API output.
func NormalizeTags(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, t := range in {
		n := NormalizeTag(t)
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

func repairAttempts(raw string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 8)
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
	add(repairUTF8FromLatin1(raw))
	add(repairUTF8FromLatin1(stripped))
	add(decodeGBKBytes(latin1Bytes(raw)))
	add(decodeGBKBytes(latin1Bytes(stripped)))
	add(repairGBKAsUTF8(raw))
	add(repairGBKAsUTF8(stripped))
	return out
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

func matchCatalogFragment(raw string) string {
	for _, attempt := range repairAttempts(raw) {
		if name := uniqueCatalogByText(attempt); name != "" {
			return name
		}
	}
	if stripped := stripReplacementRunes(raw); stripped != raw {
		if frag := decodeGBKBytes(latin1Bytes(stripped)); frag != "" {
			if name := uniqueCatalogByText(frag); name != "" {
				return name
			}
			if strings.Contains(frag, "色系") {
				if name := disambiguateColorFamily(raw); name != "" {
					return name
				}
			}
		}
	}
	return ""
}

func uniqueCatalogByText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if id, ok := nameToID[text]; ok && id != "" {
		return text
	}
	var hits []string
	for _, c := range CategoryCatalog {
		if strings.Contains(c.Name, text) || strings.Contains(text, c.Name) {
			hits = append(hits, c.Name)
		}
	}
	if len(hits) == 1 {
		return hits[0]
	}
	for _, ch := range uniqueHanChars(text) {
		hits = hits[:0]
		for _, c := range CategoryCatalog {
			if strings.Contains(c.Name, ch) {
				hits = append(hits, c.Name)
			}
		}
		if len(hits) == 1 {
			return hits[0]
		}
	}
	return ""
}

func uniqueHanChars(s string) []string {
	seen := map[rune]struct{}{}
	out := make([]string, 0, 4)
	for _, r := range s {
		if r < 0x4e00 || r > 0x9fff {
			continue
		}
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, string(r))
	}
	return out
}

func disambiguateColorFamily(raw string) string {
	// Warm tags often survive as pure Latin-1 mojibake (ů = U+016F / bytes C5 AF).
	for _, r := range raw {
		if r == 0x016F || r == 0x00AF || r == 0x00C5 {
			return "暖色系"
		}
	}
	if strings.Contains(strings.ToLower(hex.EncodeToString([]byte(raw))), "c5af") {
		return "暖色系"
	}
	// Remaining 色系 fragments with replacement chars are cold-tone tags from the bad import.
	if strings.Contains(strings.ToLower(hex.EncodeToString([]byte(raw))), "c9ab") {
		return "冷色系"
	}
	return "冷色系"
}

func garbledHexID(raw string) string {
	return garbledHexToID[strings.ToLower(hex.EncodeToString([]byte(raw)))]
}

// repairUTF8FromLatin1 fixes UTF-8 bytes that were interpreted as Latin-1 runes.
func repairUTF8FromLatin1(s string) string {
	b := latin1Bytes(s)
	if len(b) == 0 || len(b) != len([]rune(s)) {
		// High runes present (e.g. U+FFFD) — try bytes after stripping replacements.
		b = latin1Bytes(stripReplacementRunes(s))
	}
	if len(b) == 0 {
		return s
	}
	if !utf8.Valid(b) {
		return s
	}
	return string(b)
}

// repairGBKAsUTF8 fixes UTF-8 mis-decoding of GBK bytes (common on Windows Python stdout).
func repairGBKAsUTF8(s string) string {
	if _, ok := nameToID[s]; ok {
		return s
	}
	dec := simplifiedchinese.GBK.NewDecoder()
	out, err := dec.String(s)
	if err != nil {
		return s
	}
	if !utf8.ValidString(out) {
		return s
	}
	return out
}

func containsHan(s string) bool {
	for _, r := range s {
		if r >= 0x4e00 && r <= 0x9fff {
			return true
		}
	}
	return false
}
