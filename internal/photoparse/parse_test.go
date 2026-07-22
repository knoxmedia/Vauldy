package photoparse

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

func TestIsPhotoLibraryType(t *testing.T) {
	if !IsPhotoLibraryType("photo") || !IsPhotoLibraryType("Photo") {
		t.Fatal("expected photo")
	}
	if IsPhotoLibraryType("music") {
		t.Fatal("music is not photo")
	}
}

func TestShouldScanFile(t *testing.T) {
	cases := []struct {
		lib, ft string
		want    bool
	}{
		{"photo", "image", true},
		{"photo", "video", false},
		{"music", "audio", true},
		{"music", "image", false},
		{"movie", "video", true},
		{"movie", "image", false},
	}
	for _, c := range cases {
		if got := ShouldScanFile(c.lib, c.ft); got != c.want {
			t.Fatalf("%s/%s got=%v want=%v", c.lib, c.ft, got, c.want)
		}
	}
}

func TestParseFromFilePNG(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.png")
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}
	if err := os.WriteFile(path, png, 0o644); err != nil {
		t.Fatal(err)
	}
	meta := ParseFromFile(path)
	if meta.Width != 1 || meta.Height != 1 {
		t.Fatalf("dimensions=%dx%d", meta.Width, meta.Height)
	}
	if meta.TakenAt == "" {
		t.Fatal("expected fallback taken_at")
	}
}

func TestParseFromFileWithDiagnosticsReturnsPartialMetadataAndErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.jpg")
	if err := os.WriteFile(path, []byte("not-an-image"), 0o644); err != nil {
		t.Fatal(err)
	}
	meta, diagnostics := ParseFromFileWithDiagnostics(path)
	if meta.Title != "broken" || meta.MimeType != "image/jpeg" || meta.TakenAt == "" {
		t.Fatalf("partial meta=%+v", meta)
	}
	if len(diagnostics) == 0 {
		t.Fatal("expected diagnostics")
	}
}

func TestParseFromFileWithDiagnosticsReportsMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.jpg")
	meta, diagnostics := ParseFromFileWithDiagnostics(path)
	if meta.Title != "missing" || len(diagnostics) == 0 {
		t.Fatalf("meta=%+v diagnostics=%v", meta, diagnostics)
	}
}

func TestParseFromFileWithDiagnosticsIgnoresAbsentJPEGExif(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.jpg")
	var encoded bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(1, 1, color.RGBA{R: 255, A: 255})
	if err := jpeg.Encode(&encoded, img, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	meta, diagnostics := ParseFromFileWithDiagnostics(path)
	if meta.Width != 3 || meta.Height != 2 {
		t.Fatalf("dimensions=%dx%d", meta.Width, meta.Height)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics=%v", diagnostics)
	}
}

func TestParseFromFileWithDiagnosticsReportsCorruptJPEGExifPayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt-exif.jpg")
	var encoded bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	if err := jpeg.Encode(&encoded, img, nil); err != nil {
		t.Fatal(err)
	}
	jpegData := encoded.Bytes()
	app1Payload := append([]byte("Exif\x00\x00"), []byte("broken-tiff")...)
	segment := []byte{0xff, 0xe1, byte((len(app1Payload) + 2) >> 8), byte(len(app1Payload) + 2)}
	segment = append(segment, app1Payload...)
	withExif := append([]byte{}, jpegData[:2]...)
	withExif = append(withExif, segment...)
	withExif = append(withExif, jpegData[2:]...)
	if err := os.WriteFile(path, withExif, 0o644); err != nil {
		t.Fatal(err)
	}
	meta, diagnostics := ParseFromFileWithDiagnostics(path)
	if meta.Width != 4 || meta.Height != 3 {
		t.Fatalf("dimensions=%dx%d", meta.Width, meta.Height)
	}
	if len(diagnostics) == 0 {
		t.Fatal("expected corrupt EXIF diagnostic")
	}
}
