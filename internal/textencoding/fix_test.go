package textencoding

import (
	"strings"
	"testing"
)

func TestFixMetadataStringGBKAsLatin1(t *testing.T) {
	// GBK bytes for 站长素材 interpreted as Latin-1 runes (common in ffmpeg ID3 on Windows).
	gbk := []byte{0xD5, 0xBE, 0xB3, 0xA4, 0xCB, 0xD8, 0xB2, 0xC4}
	var garbled strings.Builder
	for _, b := range gbk {
		garbled.WriteRune(rune(b))
	}
	got := FixMetadataString(garbled.String())
	if got != "站长素材" {
		t.Fatalf("got %q want 站长素材", got)
	}
}

func TestFixMetadataStringUTF8Misread(t *testing.T) {
	bytes := []byte{0xE6, 0x97, 0xA0, 0xE8, 0xA8, 0x80, 0xE7, 0x9A, 0x84, 0xE7, 0xBB, 0x93, 0xE5, 0xB1, 0x80}
	var b strings.Builder
	for _, by := range bytes {
		b.WriteRune(rune(by))
	}
	garbled := b.String()
	got := FixMetadataString(garbled)
	if got != "无言的结局" {
		t.Fatalf("got %q want 无言的结局", got)
	}
}

func TestFixMetadataStringGBKWithASCIISuffix(t *testing.T) {
	gbk := []byte{0xD5, 0xBE, 0xB3, 0xA4, 0xCB, 0xD8, 0xB2, 0xC4}
	var garbled strings.Builder
	for _, b := range gbk {
		garbled.WriteRune(rune(b))
	}
	got := FixMetadataString(garbled.String() + "(sc.chinaz.com)")
	if got != "站长素材(sc.chinaz.com)" {
		t.Fatalf("got %q", got)
	}
}

func TestFixMetadataStringPreservesASCII(t *testing.T) {
	if got := FixMetadataString("Song Title"); got != "Song Title" {
		t.Fatalf("got %q", got)
	}
}
