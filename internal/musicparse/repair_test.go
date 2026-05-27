package musicparse

import (
	"strings"
	"testing"
)

func TestRepairTrackMetaGBKTags(t *testing.T) {
	gbkTitle := []byte{0xD5, 0xBE, 0xB3, 0xA4, 0xCB, 0xD8, 0xB2, 0xC4}
	var title strings.Builder
	for _, b := range gbkTitle {
		title.WriteRune(rune(b))
	}
	garbled := title.String() + "(sc.chinaz.com)"
	raw := `{"format":{"tags":{"title":"` + garbled + `","artist":"` + title.String() + `","album":"` + title.String() + `"}}}`
	meta := ParseFromSources(`F:\audio\test.mp3`, raw, 0, 0)
	want := "站长素材(sc.chinaz.com)"
	if meta.Title != want {
		t.Fatalf("title=%q want %q", meta.Title, want)
	}
	if meta.Album != "站长素材" {
		t.Fatalf("album=%q", meta.Album)
	}
}
