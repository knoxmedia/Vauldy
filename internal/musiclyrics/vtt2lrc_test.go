package musiclyrics

import "testing"

func TestVTTToLRC(t *testing.T) {
	vtt := `WEBVTT

00:00:01.500 --> 00:00:04.000
第一句歌词

00:00:04.000 --> 00:00:07.250
第二句歌词
`
	got := VTTToLRC(vtt)
	want := "[00:01.50]第一句歌词\n[00:04.00]第二句歌词"
	if got != want {
		t.Fatalf("VTTToLRC:\n got %q\n want %q", got, want)
	}
}

func TestParseVTTTime(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"00:01.500", 1.5},
		{"00:01,500", 1.5},
		{"00:00:01.500", 1.5},
	}
	for _, c := range cases {
		got, err := parseVTTTime(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if got < c.want-0.001 || got > c.want+0.001 {
			t.Fatalf("%q: got %v want %v", c.in, got, c.want)
		}
	}
}
