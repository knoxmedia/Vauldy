//go:build windows

package discovery

import "testing"

func TestWindowsPathKeyNormalizesCaseSeparatorsAndDrive(t *testing.T) {
	got, err := PathKey(`c:/MEDIA/Shows/../Film.mkv`)
	if err != nil {
		t.Fatal(err)
	}
	if got != `c:\media\film.mkv` {
		t.Fatalf("key = %q", got)
	}
}
func TestWindowsPathKeyPreservesUNCVolume(t *testing.T) {
	got, err := PathKey(`//SERVER/Share/Media/../Film.mkv`)
	if err != nil {
		t.Fatal(err)
	}
	if got != `\\server\share\film.mkv` {
		t.Fatalf("UNC key = %q", got)
	}
}
func TestWindowsPathContainmentRejectsSiblingPrefix(t *testing.T) {
	if !PathWithinRoot(`C:\Media\Movies\film.mkv`, `c:/media`) {
		t.Fatal("expected contained path")
	}
	if PathWithinRoot(`C:\Media2\film.mkv`, `C:\Media`) {
		t.Fatal("sibling prefix accepted")
	}
}
func TestWindowsLongestRoot(t *testing.T) {
	root, ok, err := LongestRoot(`C:\MEDIA\Movies\film.mkv`, []string{`C:\Media`, `c:/media/movies`})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || root != `c:\media\movies` {
		t.Fatalf("root = %q, ok = %v", root, ok)
	}
}
