package musicparse

import "testing"

func TestParseFromSources_tagsFirst(t *testing.T) {
	raw := `{"format":{"tags":{"title":"无言的结局","artist":"李茂山","album":"经典老歌","album_artist":"李茂山","date":"1985","genre":"Pop","track":"2"}}}`
	meta := ParseFromSources(`D:\Music\李茂山\经典老歌\01.mp3`, raw, 194, 320000)
	if meta.Title != "无言的结局" {
		t.Fatalf("title=%q", meta.Title)
	}
	if meta.Album != "经典老歌" {
		t.Fatalf("album=%q", meta.Album)
	}
	if meta.Year != 1985 {
		t.Fatalf("year=%d", meta.Year)
	}
	if meta.TrackNumber != 2 {
		t.Fatalf("track=%d", meta.TrackNumber)
	}
}

func TestParseFromSources_filenameFallback(t *testing.T) {
	meta := ParseFromSources(`E:\FLAC\清唯 - 我也不想这样.flac`, "", 26, 0)
	if meta.Title != "我也不想这样" {
		t.Fatalf("title=%q", meta.Title)
	}
	if meta.Artist != "清唯" {
		t.Fatalf("artist=%q", meta.Artist)
	}
	if meta.Album != UnknownAlbum {
		t.Fatalf("album=%q", meta.Album)
	}
	if meta.AlbumArtist != VariousArtists {
		t.Fatalf("album artist=%q", meta.AlbumArtist)
	}
}

func TestParseFromSources_directoryFallback(t *testing.T) {
	meta := ParseFromSources(`D:\Music\张学友\吻别\03 - 吻别.mp3`, "", 300, 0)
	if meta.Album != "吻别" {
		t.Fatalf("album=%q", meta.Album)
	}
	if meta.Artist != "张学友" {
		t.Fatalf("artist=%q", meta.Artist)
	}
}

func TestSplitArtists(t *testing.T) {
	parts := splitArtists("A; B / C")
	if len(parts) != 2 || parts[0] != "A" || parts[1] != "B / C" {
		t.Fatalf("parts=%v", parts)
	}
}
