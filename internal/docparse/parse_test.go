package docparse

import "testing"

func TestShouldSkipPath(t *testing.T) {
	patterns := []string{"node_modules/**", "*.tmp"}
	if !ShouldSkipPath(".secret.pdf", 2048, patterns) {
		t.Fatal("expected hidden file skip")
	}
	if !ShouldSkipPath("docs/small.txt", 512, patterns) {
		t.Fatal("expected small file skip")
	}
	if !ShouldSkipPath("src/node_modules/pkg/index.js", 4096, patterns) {
		t.Fatal("expected exclude pattern match")
	}
	if ShouldSkipPath("books/sample.pdf", 4096, patterns) {
		t.Fatal("expected normal file to pass")
	}
}

func TestParseExcludePatterns(t *testing.T) {
	got := ParseExcludePatterns("node_modules/**,\n.git/**")
	if len(got) != 2 {
		t.Fatalf("expected 2 patterns, got %d", len(got))
	}
}
