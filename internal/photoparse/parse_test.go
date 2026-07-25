package photoparse

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
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

func TestParseFromFileWithDiagnosticsReportsMalformedEXIFFieldsAndKeepsPartialValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "malformed-fields.jpg")
	data := jpegWithEXIF(t, malformedFieldTIFF())
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	meta, diagnostics := ParseFromFileWithDiagnostics(path)
	if meta.Width != 5 || meta.Height != 4 {
		t.Fatalf("dimensions=%dx%d", meta.Width, meta.Height)
	}
	if meta.CameraModel != "GoodModel" {
		t.Fatalf("model=%q", meta.CameraModel)
	}
	for _, detail := range []string{"date_time", "camera_make", "orientation", "gps"} {
		if !diagnosticsContain(diagnostics, detail) {
			t.Fatalf("diagnostics=%v missing %q", diagnostics, detail)
		}
	}
}

func TestReadEXIFMissingTagsHaveNoDiagnostics(t *testing.T) {
	values, diagnostics, err := readEXIF(minimalTIFF())
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics=%v", diagnostics)
	}
	if values.takenAt != "" || values.cameraMake != "" || values.orientation != 0 {
		t.Fatalf("values=%+v", values)
	}
}

func diagnosticsContain(diagnostics []error, detail string) bool {
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Error(), detail) {
			return true
		}
	}
	return false
}

func minimalTIFF() []byte {
	return []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 0, 0, 0, 0, 0, 0}
}

func malformedFieldTIFF() []byte {
	// Little-endian TIFF with malformed DateTime/Make/Orientation fields, a valid
	// Model field, and a GPS IFD whose latitude value has the wrong type.
	const entries = 5
	ifdSize := 2 + entries*12 + 4
	modelOffset := 8 + ifdSize
	gpsOffset := modelOffset + len("GoodModel\x00")
	data := make([]byte, gpsOffset+2+4*12+4)
	copy(data, []byte{'I', 'I', 42, 0, 8, 0, 0, 0})
	binary.LittleEndian.PutUint16(data[8:10], entries)
	entry := func(i int, tag, typ uint16, count, value uint32) {
		off := 10 + i*12
		binary.LittleEndian.PutUint16(data[off:off+2], tag)
		binary.LittleEndian.PutUint16(data[off+2:off+4], typ)
		binary.LittleEndian.PutUint32(data[off+4:off+8], count)
		binary.LittleEndian.PutUint32(data[off+8:off+12], value)
	}
	entry(0, 0x0132, 2, 4, uint32('b')|uint32('a')<<8|uint32('d')<<16)
	entry(1, 0x010f, 3, 1, 7)
	entry(2, 0x0110, 2, uint32(len("GoodModel\x00")), uint32(modelOffset))
	entry(3, 0x0112, 2, 2, uint32('x'))
	entry(4, 0x8825, 4, 1, uint32(gpsOffset))
	copy(data[modelOffset:], "GoodModel\x00")
	binary.LittleEndian.PutUint16(data[gpsOffset:gpsOffset+2], 4)
	gpsEntry := func(i int, tag, typ uint16, count, value uint32) {
		off := gpsOffset + 2 + i*12
		binary.LittleEndian.PutUint16(data[off:off+2], tag)
		binary.LittleEndian.PutUint16(data[off+2:off+4], typ)
		binary.LittleEndian.PutUint32(data[off+4:off+8], count)
		binary.LittleEndian.PutUint32(data[off+8:off+12], value)
	}
	gpsEntry(0, 1, 2, 2, uint32('N'))
	gpsEntry(1, 2, 2, 2, uint32('x')) // latitude must be rational
	gpsEntry(2, 3, 2, 2, uint32('E'))
	gpsEntry(3, 4, 2, 2, uint32('x'))
	return data
}

func jpegWithEXIF(t *testing.T, tiff []byte) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 5, 4)), nil); err != nil {
		t.Fatal(err)
	}
	jpegData := encoded.Bytes()
	payload := append([]byte("Exif\x00\x00"), tiff...)
	segment := []byte{0xff, 0xe1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}
	segment = append(segment, payload...)
	result := append([]byte{}, jpegData[:2]...)
	result = append(result, segment...)
	return append(result, jpegData[2:]...)
}

func TestReadEXIFDatePriorityAndFallback(t *testing.T) {
	tests := []struct {
		name           string
		tags           []tiffTestTag
		want           string
		wantDiagnostic string
	}{
		{name: "original wins", tags: []tiffTestTag{{0x9003, "2024:01:02 03:04:05"}, {0x0132, "2023:02:03 04:05:06"}, {0x9004, "2022:03:04 05:06:07"}}, want: "2024-01-02T03:04:05Z"},
		{name: "date time fallback", tags: []tiffTestTag{{0x0132, "2023:02:03 04:05:06"}, {0x9004, "2022:03:04 05:06:07"}}, want: "2023-02-03T04:05:06Z"},
		{name: "digitized fallback", tags: []tiffTestTag{{0x9004, "2022:03:04 05:06:07"}}, want: "2022-03-04T05:06:07Z"},
		{name: "malformed original falls back", tags: []tiffTestTag{{0x9003, "bad"}, {0x0132, "2023:02:03 04:05:06"}}, want: "2023-02-03T04:05:06Z", wantDiagnostic: "date_time_original"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values, diagnostics, err := readEXIF(tiffWithStringTags(tt.tags))
			if err != nil {
				t.Fatal(err)
			}
			if values.takenAt != tt.want {
				t.Fatalf("takenAt=%q want %q diagnostics=%v", values.takenAt, tt.want, diagnostics)
			}
			if tt.wantDiagnostic == "" && len(diagnostics) != 0 {
				t.Fatalf("diagnostics=%v", diagnostics)
			}
			if tt.wantDiagnostic != "" && !diagnosticsContain(diagnostics, tt.wantDiagnostic) {
				t.Fatalf("diagnostics=%v", diagnostics)
			}
		})
	}
}

type tiffTestTag struct {
	id    uint16
	value string
}

func tiffWithStringTags(tags []tiffTestTag) []byte {
	ifdSize := 2 + len(tags)*12 + 4
	offset := 8 + ifdSize
	total := offset
	for _, tag := range tags {
		total += len(tag.value) + 1
	}
	data := make([]byte, total)
	copy(data, []byte{'I', 'I', 42, 0, 8, 0, 0, 0})
	binary.LittleEndian.PutUint16(data[8:10], uint16(len(tags)))
	for i, tag := range tags {
		entry := 10 + i*12
		value := tag.value + "\x00"
		binary.LittleEndian.PutUint16(data[entry:entry+2], tag.id)
		binary.LittleEndian.PutUint16(data[entry+2:entry+4], 2)
		binary.LittleEndian.PutUint32(data[entry+4:entry+8], uint32(len(value)))
		binary.LittleEndian.PutUint32(data[entry+8:entry+12], uint32(offset))
		copy(data[offset:], value)
		offset += len(value)
	}
	return data
}
