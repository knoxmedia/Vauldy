package storage

import (
	"context"
	"os"
	"testing"
)

func TestIsISOBaseMedia(t *testing.T) {
	cases := map[string]bool{
		"clip.mp4": true,
		"clip.MOV": true,
		"clip.m4v": true,
		"clip.mkv": false,
		"clip.ts":  false,
		"":         false,
	}
	for name, want := range cases {
		if got := isISOBaseMedia(name); got != want {
			t.Fatalf("%q: got %v want %v", name, got, want)
		}
	}
}

func TestResolveEncryptSourceSkipsInvalidMP4WhenPlainKept(t *testing.T) {
	dir := t.TempDir()
	plain := dir + "/bad.mp4"
	if err := os.WriteFile(plain, []byte("not-mp4"), 0o644); err != nil {
		t.Fatal(err)
	}
	enc := &AssetEncryptor{DataDir: dir}
	src, cleanup, remuxed, err := enc.resolveEncryptSource(context.Background(), 1, plain, false)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	defer cleanup()
	if remuxed {
		t.Fatal("expected no remux for invalid mp4 when plain kept")
	}
	if src != plain {
		t.Fatalf("src=%q want plain", src)
	}
}
