package metadatalib

import "testing"

func TestRelShardDir(t *testing.T) {
	got := RelShardDir(12345)
	want := "30/39/12345"
	if got != want {
		t.Fatalf("RelShardDir(12345)=%q want %q", got, want)
	}
}

func TestPublicURLAndParse(t *testing.T) {
	u := PublicURL(99, "poster.jpg")
	if u != "/metadata/library/00/63/99/poster.jpg" {
		t.Fatalf("PublicURL=%q", u)
	}
	id, ok := ParseMediaIDFromPublicURL(u)
	if !ok || id != 99 {
		t.Fatalf("parse=%v id=%d", ok, id)
	}
}

func TestIsLocalMetadataURL(t *testing.T) {
	if !IsLocalMetadataURL("/metadata/library/01/02/3/poster.jpg") {
		t.Fatal("expected local")
	}
	if IsLocalMetadataURL("https://image.tmdb.org/x.jpg") {
		t.Fatal("expected remote")
	}
}
