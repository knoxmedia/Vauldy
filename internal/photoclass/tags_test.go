package photoclass

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestNormalizeTagLatin1Mojibake(t *testing.T) {
	// UTF-8 bytes of 暖色系 mis-read as Latin-1 runes (Windows Python stdout issue).
	bytes := []byte{0xE6, 0x9A, 0x96, 0xE8, 0x89, 0xB2, 0xE7, 0xB3, 0xBB}
	var b strings.Builder
	for _, by := range bytes {
		b.WriteRune(rune(by))
	}
	garbled := b.String()
	if got := NormalizeTag(garbled); got != "暖色系" {
		t.Fatalf("got %q want 暖色系", got)
	}
}

func TestNormalizeTagAlias(t *testing.T) {
	if got := NormalizeTag("保存"); got != "下载保存" {
		t.Fatalf("got %q", got)
	}
}

func TestTagIDStable(t *testing.T) {
	if TagID("暖色系") != "warm" {
		t.Fatalf("id=%q", TagID("暖色系"))
	}
	if TagName("warm") != "暖色系" {
		t.Fatalf("name=%q", TagName("warm"))
	}
}

func TestNormalizeTagsDedupe(t *testing.T) {
	in := []string{"暖色系", "暖色系", "保存"}
	out := NormalizeTags(in)
	if len(out) != 2 {
		t.Fatalf("len=%d %v", len(out), out)
	}
}

func garbledFromHex(t *testing.T, h string) string {
	t.Helper()
	b, err := hex.DecodeString(h)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTagIDs(t *testing.T) {
	ids := TagIDs([]string{"暖色系", "相机拍摄", "暖色系"})
	if len(ids) != 2 || ids[0] != "warm" || ids[1] != "camera" {
		t.Fatalf("ids=%v", ids)
	}
}

func TestResolveGarbledDBTags(t *testing.T) {
	cases := []struct {
		hex  string
		id   string
		name string
	}{
		{"efbfbdefbfbdefbfbdefbfbd", "people", "人物"},
		{"efbfbddab0efbfbd", "mono", "黑白"},
		{"efbfbde7beb0", "landscape", "风景"},
		{"efbfbdefbfbdc9abcfb5", "cool", "冷色系"},
		{"efbfbddfb1efbfbdefbfbdcdb6efbfbd", "saturated", "高饱和度"},
		{"efbfbdefbfbdcab3", "food", "美食"},
		{"d2b9efbfbdefbfbd", "night", "夜景"},
		{"c5afc9abcfb5", "warm", "暖色系"},
	}
	for _, tc := range cases {
		raw := garbledFromHex(t, tc.hex)
		id, name := ResolveTag(raw)
		if id != tc.id || name != tc.name {
			t.Fatalf("hex=%s id=%q name=%q want %q %q", tc.hex, id, name, tc.id, tc.name)
		}
	}
}
