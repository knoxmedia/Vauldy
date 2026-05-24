package handler

import "testing"

func TestShouldPreserveSeriesTitle(t *testing.T) {
	t.Parallel()
	if shouldPreserveSeriesTitle("奇思妙探", 1) {
		t.Fatal("first episode scrape should allow title update")
	}
	if !shouldPreserveSeriesTitle("奇思妙探", 2) {
		t.Fatal("existing series with additional episodes should preserve title")
	}
	if shouldPreserveSeriesTitle("", 3) {
		t.Fatal("empty existing title should allow update")
	}
	if shouldPreserveSeriesTitle("  ", 3) {
		t.Fatal("blank existing title should allow update")
	}
}
