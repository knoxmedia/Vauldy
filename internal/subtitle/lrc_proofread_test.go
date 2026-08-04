package subtitle

import (
	"strings"
	"testing"
)

func TestDetectFormatLRC(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    Format
	}{
		{"plain lrc", "[00:12.34]Hello world\n[00:15.00]Second line\n", FormatLRC},
		{"lrc with metadata", "[ti:Title]\n[ar:Artist]\n[00:01.00]First lyric\n", FormatLRC},
		{"multi timestamp", "[00:01.00][00:05.00]Repeated lyric\n", FormatLRC},
		{"vtt stays vtt", "WEBVTT\n\n00:00:01.000 --> 00:00:04.000\nHello\n", FormatVTT},
		{"metadata only is unknown", "[ti:Title]\n[ar:Artist]\n", FormatUnknown},
	}
	for _, c := range cases {
		if got := DetectFormat(c.content, ""); got != c.want {
			t.Fatalf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	// URL hint also drives detection.
	if got := DetectFormat("lyric text only", "track.lrc"); got != FormatLRC {
		t.Fatalf("url hint lrc: got %v want FormatLRC", got)
	}
}

func TestParseAndRenderLRC(t *testing.T) {
	raw := "[00:12.34]Hello world\n[00:15.00]Second line\n"
	cues, format, err := ParseCues(raw, FormatLRC)
	if err != nil {
		t.Fatal(err)
	}
	if format != FormatLRC || len(cues) != 2 {
		t.Fatalf("unexpected: format=%v cues=%d", format, len(cues))
	}
	if cues[0].Start != "[00:12.34]" || cues[0].Text != "Hello world" {
		t.Fatalf("first cue: %+v", cues[0])
	}
	out := RenderCues(cues, FormatLRC)
	if !strings.Contains(out, "[00:12.34]Hello world") || !strings.Contains(out, "[00:15.00]Second line") {
		t.Fatalf("render lrc: %q", out)
	}
}

func TestParseLRCCuesSkipsMetadata(t *testing.T) {
	raw := "[ti:Title]\n[ar:Artist]\n[00:01.00]Lyric\n\n[00:03.00]More\n"
	cues, err := parseLRCCues(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 2 {
		t.Fatalf("expected 2 lyric cues, got %d: %+v", len(cues), cues)
	}
	if cues[0].Start != "[00:01.00]" || cues[1].Start != "[00:03.00]" {
		t.Fatalf("metadata leaked into cues: %+v", cues)
	}
}

func TestParseLRCCuesMultiTimestamp(t *testing.T) {
	raw := "[00:01.00][00:05.00]Repeated lyric\n"
	cues, err := parseLRCCues(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 2 {
		t.Fatalf("expected 2 cues from multi-timestamp line, got %d", len(cues))
	}
	if cues[0].Text != "Repeated lyric" || cues[1].Text != "Repeated lyric" {
		t.Fatalf("text mismatch: %+v", cues)
	}
	if cues[0].Start != "[00:01.00]" || cues[1].Start != "[00:05.00]" {
		t.Fatalf("timestamp mismatch: %+v", cues)
	}
}

func TestSplitLRCLyricLine(t *testing.T) {
	cases := []struct {
		line     string
		prefix   string
		text     string
		ok       bool
	}{
		{"[00:12.34]Hello world", "[00:12.34]", "Hello world", true},
		{"  [00:12.34]  spaced  ", "[00:12.34]", "spaced", true},
		{"[00:01.00][00:05.00]Repeated", "[00:01.00][00:05.00]", "Repeated", true},
		{"[ti:Title]", "", "", false},
		{"[ar:Artist]", "", "", false},
		{"plain text no tag", "", "", false},
		{"", "", "", false},
		{"   ", "", "", false},
	}
	for _, c := range cases {
		p, txt, ok := splitLRCLyricLine(c.line)
		if p != c.prefix || txt != c.text || ok != c.ok {
			t.Fatalf("splitLRCLyricLine(%q): prefix=%q text=%q ok=%v; want prefix=%q text=%q ok=%v",
				c.line, p, txt, ok, c.prefix, c.text, c.ok)
		}
	}
}

// TestProofreadLRCContentReassembly verifies timestamp/metadata preservation without
// hitting the network by injecting a fake correction via a minimal provider-free path.
// Since proofreadLRCContent requires providers, this test exercises the splitting +
// reassembly logic indirectly through splitLRCLyricLine, mirroring the production flow.
func TestProofreadLRCReassemblyLogic(t *testing.T) {
	raw := "[ti:Title]\n[ar:Artist]\n[00:01.00]heloo wrold\n[00:03.50]seconnd lne\n"
	lines := splitLines(raw)
	out := make([]string, len(lines))
	type entry struct{ prefix, text string; idx int }
	var lyrics []entry
	for i, l := range lines {
		out[i] = l
		p, txt, ok := splitLRCLyricLine(l)
		if !ok {
			continue
		}
		lyrics = append(lyrics, entry{p, txt, i})
	}
	if len(lyrics) != 2 {
		t.Fatalf("expected 2 lyric lines, got %d", len(lyrics))
	}
	// Simulate LLM correction.
	fixed := []string{"hello world", "second line"}
	for j, e := range lyrics {
		out[e.idx] = e.prefix + fixed[j]
	}
	result := strings.Join(out, "\n")
	if strings.Contains(result, "heloo") || strings.Contains(result, "seconnd") {
		t.Fatalf("lyric text not corrected: %q", result)
	}
	if !strings.Contains(result, "[ti:Title]") || !strings.Contains(result, "[ar:Artist]") {
		t.Fatalf("metadata tags dropped: %q", result)
	}
	if !strings.Contains(result, "[00:01.00]hello world") || !strings.Contains(result, "[00:03.50]second line") {
		t.Fatalf("timestamps not preserved with corrected text: %q", result)
	}
}
