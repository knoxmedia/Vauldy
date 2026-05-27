package docparse

import (
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// PickDocumentTitle prefers embedded metadata when it looks valid; otherwise uses the file name.
func PickDocumentTitle(filePath, metadataTitle string) string {
	base := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	baseClean := SanitizeMetadataText(base)
	meta := SanitizeMetadataText(metadataTitle)
	if meta == "" {
		if baseClean != "" {
			return baseClean
		}
		return base
	}
	if baseClean == "" {
		return meta
	}
	sim := titleSimilarity(normalizeTitleCompare(baseClean), normalizeTitleCompare(meta))
	if sim >= 0.82 {
		return meta
	}
	// Partial overlap usually means PDF UTF-16 was mis-decoded into near-matching garbage.
	if sim >= 0.55 {
		return baseClean
	}
	if isGenericDocumentTitle(meta) && hasCJK(baseClean) {
		return baseClean
	}
	return meta
}

// SanitizeMetadataText returns empty when text is missing or looks corrupted.
func SanitizeMetadataText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || !utf8.ValidString(s) || IsGarbledText(s) {
		return ""
	}
	return s
}

// IsGarbledText reports obviously broken metadata (replacement chars, NULs, controls, UTF-16 misreads).
func IsGarbledText(s string) bool {
	if s == "" || !utf8.ValidString(s) {
		return true
	}
	if strings.ContainsRune(s, '\uFFFD') {
		return true
	}
	nulls := 0
	controls := 0
	printable := 0
	for _, r := range s {
		switch {
		case r == 0:
			nulls++
		case r < 0x20 && r != '\t' && r != '\n' && r != '\r':
			controls++
		case r == 0x7f:
			controls++
		default:
			printable++
		}
	}
	if nulls > 0 {
		return true
	}
	if controls > 0 {
		return true
	}
	if printable == 0 {
		return true
	}
	// UTF-16 read as Latin-1 often yields many chars in 0x80-0xFF with no CJK.
	if looksLikeUTF16Misread(s) {
		return true
	}
	return false
}

func titlesCompatible(a, b string) bool {
	na := normalizeTitleCompare(a)
	nb := normalizeTitleCompare(b)
	if na == "" || nb == "" {
		return na == nb
	}
	if na == nb {
		return true
	}
	if strings.Contains(na, nb) || strings.Contains(nb, na) {
		return true
	}
	return titleSimilarity(na, nb) >= 0.82
}

func normalizeTitleCompare(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func titleSimilarity(a, b string) float64 {
	ra := []rune(a)
	rb := []rune(b)
	if len(ra) == 0 || len(rb) == 0 {
		return 0
	}
	used := make([]bool, len(rb))
	matches := 0
	for _, ca := range ra {
		for j, cb := range rb {
			if !used[j] && ca == cb {
				matches++
				used[j] = true
				break
			}
		}
	}
	maxLen := len(ra)
	if len(rb) > maxLen {
		maxLen = len(rb)
	}
	return float64(matches) / float64(maxLen)
}

func isGenericDocumentTitle(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "unknown", "untitled", "document", "title", "no title", "microsoft word document":
		return true
	default:
		return false
	}
}

func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func looksLikeUTF16Misread(s string) bool {
	highLatin := 0
	cjk := 0
	for _, r := range s {
		switch {
		case r >= 0x4E00 && r <= 0x9FFF:
			cjk++
		case r >= 0x80 && r <= 0xFF:
			highLatin++
		}
	}
	return highLatin >= 2 && cjk == 0 && len(s) >= 4
}
