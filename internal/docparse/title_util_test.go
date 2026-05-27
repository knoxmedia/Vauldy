package docparse

import "testing"

func TestTitlesCompatible(t *testing.T) {
	if !titlesCompatible("国家为什么会失败", "国家为什么会失败") {
		t.Fatal("expected exact match")
	}
	if titlesCompatible("国家为什么会失败", "国家为份么会失") {
		t.Fatal("expected mismatch for mis-decoded title")
	}
	if titlesCompatible("国家为什么会失败", "Unknown") {
		t.Fatal("expected mismatch for generic metadata title")
	}
}

func TestSanitizeMetadataText(t *testing.T) {
	if got := SanitizeMetadataText("  正常标题  "); got != "正常标题" {
		t.Fatalf("got %q", got)
	}
	if got := SanitizeMetadataText("\ufffd bad"); got != "" {
		t.Fatalf("expected empty for garbled input, got %q", got)
	}
}
