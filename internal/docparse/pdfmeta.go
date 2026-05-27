package docparse

import (
	"encoding/binary"
	"encoding/hex"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

type pdfMeta struct {
	Title     string
	Author    string
	Subject   string
	PageCount int
}

var (
	rePDFPages = regexp.MustCompile(`(?i)/Count\s+(\d+)`)
)

func parsePDFMeta(path string) *pdfMeta {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, 256*1024)
	n, _ := f.Read(buf)
	if n == 0 {
		return nil
	}
	data := string(buf[:n])
	pm := &pdfMeta{}
	if raw, ok := findPDFStringField(data, "Title"); ok {
		pm.Title = SanitizeMetadataText(raw)
	}
	if raw, ok := findPDFStringField(data, "Author"); ok {
		pm.Author = SanitizeMetadataText(raw)
	}
	if raw, ok := findPDFStringField(data, "Subject"); ok {
		pm.Subject = SanitizeMetadataText(raw)
	}
	if m := rePDFPages.FindStringSubmatch(data); len(m) == 2 {
		if v, err := strconv.Atoi(m[1]); err == nil {
			pm.PageCount = v
		}
	}
	return pm
}

func findPDFStringField(data, field string) (string, bool) {
	re := regexp.MustCompile(`(?i)/` + regexp.QuoteMeta(field) + `\s*`)
	loc := re.FindStringIndex(data)
	if loc == nil {
		return "", false
	}
	rest := strings.TrimLeft(data[loc[1]:], " \t\r\n")
	switch {
	case strings.HasPrefix(rest, "<"):
		end := strings.IndexByte(rest, '>')
		if end < 0 {
			return "", false
		}
		raw, err := parsePDFHexString(rest[1:end])
		if err != nil || len(raw) == 0 {
			return "", false
		}
		return pdfBytesToText(raw), true
	case strings.HasPrefix(rest, "("):
		inner, _, ok := scanPDFLiteralString(rest)
		if !ok {
			return "", false
		}
		raw := decodePDFStringLiteral(inner)
		if len(raw) == 0 {
			return "", false
		}
		return pdfBytesToText(raw), true
	default:
		return "", false
	}
}

func scanPDFLiteralString(s string) (inner string, consumed int, ok bool) {
	if len(s) == 0 || s[0] != '(' {
		return "", 0, false
	}
	depth := 0
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			b.WriteByte(c)
			b.WriteByte(s[i+1])
			i++
			continue
		}
		if c == '(' {
			if depth > 0 {
				b.WriteByte(c)
			}
			depth++
			continue
		}
		if c == ')' {
			depth--
			if depth == 0 {
				return b.String(), i + 1, true
			}
			b.WriteByte(c)
			continue
		}
		b.WriteByte(c)
	}
	return "", 0, false
}

func decodePDFStringLiteral(s string) []byte {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			out = append(out, s[i])
			continue
		}
		if i+1 >= len(s) {
			break
		}
		n := s[i+1]
		switch n {
		case 'n':
			out = append(out, '\n')
			i++
		case 'r':
			out = append(out, '\r')
			i++
		case 't':
			out = append(out, '\t')
			i++
		case 'b':
			out = append(out, '\b')
			i++
		case 'f':
			out = append(out, '\f')
			i++
		case '(', ')', '\\':
			out = append(out, n)
			i++
		case '\r':
			if i+2 < len(s) && s[i+2] == '\n' {
				i += 2
			} else {
				i++
			}
		case '\n':
			// line continuation
		default:
			if n >= '0' && n <= '7' {
				octLen := 1
				for j := 2; j < 4 && i+j < len(s); j++ {
					if s[i+j] >= '0' && s[i+j] <= '7' {
						octLen++
					} else {
						break
					}
				}
				if v, err := strconv.ParseInt(s[i+1:i+1+octLen], 8, 32); err == nil {
					out = append(out, byte(v))
				}
				i += octLen
				continue
			}
			out = append(out, n)
			i++
		}
	}
	return out
}

func parsePDFHexString(raw string) ([]byte, error) {
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r == ' ', r == '\t', r == '\r', r == '\n':
			continue
		case (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F'):
			b.WriteRune(r)
		default:
			return nil, hex.InvalidByteError(r)
		}
	}
	hexStr := b.String()
	if len(hexStr)%2 == 1 {
		hexStr += "0"
	}
	return hex.DecodeString(hexStr)
}

func pdfBytesToText(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if s, ok := decodeUTF16WithBOM(b); ok {
		return s
	}
	if utf8.Valid(b) {
		return string(b)
	}
	if s, ok := tryUTF16BE(b); ok {
		return s
	}
	if s, ok := tryUTF16LE(b); ok {
		return s
	}
	return string(b)
}

func decodeUTF16WithBOM(b []byte) (string, bool) {
	if len(b) < 2 {
		return "", false
	}
	switch {
	case b[0] == 0xFE && b[1] == 0xFF:
		return decodeUTF16(b[2:], binary.BigEndian)
	case b[0] == 0xFF && b[1] == 0xFE:
		return decodeUTF16(b[2:], binary.LittleEndian)
	default:
		return "", false
	}
}

func tryUTF16BE(b []byte) (string, bool) {
	if len(b) < 2 || len(b)%2 != 0 {
		return "", false
	}
	return decodeUTF16(b, binary.BigEndian)
}

func tryUTF16LE(b []byte) (string, bool) {
	if len(b) < 2 || len(b)%2 != 0 {
		return "", false
	}
	return decodeUTF16(b, binary.LittleEndian)
}

func decodeUTF16(b []byte, order binary.ByteOrder) (string, bool) {
	if len(b) == 0 || len(b)%2 != 0 {
		return "", false
	}
	u16 := make([]uint16, len(b)/2)
	for i := range u16 {
		u16[i] = order.Uint16(b[i*2:])
	}
	runes := utf16.Decode(u16)
	s := string(runes)
	if !utf8.ValidString(s) || IsGarbledText(s) {
		return "", false
	}
	return s, true
}
