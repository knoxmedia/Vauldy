package subtitle

import "testing"

func TestIsASRAudioFile(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"a.mp3": true, "b.WAV": true, "c.flac": true, "d.opus": true,
		"e.mp4": false, "f.mkv": false, "g": false,
	}
	for path, want := range cases {
		if got := isASRAudioFile(path); got != want {
			t.Fatalf("%s: got %v want %v", path, got, want)
		}
	}
}
