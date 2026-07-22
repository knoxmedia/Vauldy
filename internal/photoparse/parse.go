package photoparse

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rwcarlsen/goexif/exif"

	"knox-media/internal/keystore"
	"knox-media/internal/storage"
)

// PhotoMeta holds normalized image metadata extracted during library scan.
type PhotoMeta struct {
	Title            string  `json:"title,omitempty"`
	Width            int     `json:"width,omitempty"`
	Height           int     `json:"height,omitempty"`
	TakenAt          string  `json:"taken_at,omitempty"` // RFC3339 UTC
	CameraMake       string  `json:"camera_make,omitempty"`
	CameraModel      string  `json:"camera_model,omitempty"`
	LensModel        string  `json:"lens_model,omitempty"`
	Orientation      int     `json:"orientation,omitempty"`
	MimeType         string  `json:"mime_type,omitempty"`
	ThumbPath        string  `json:"thumb_path,omitempty"`
	MediumPath       string  `json:"medium_path,omitempty"`
	Latitude         float64 `json:"latitude,omitempty"`
	Longitude        float64 `json:"longitude,omitempty"`
	HasGPS           bool    `json:"has_gps,omitempty"`
	PlaceID          string  `json:"place_id,omitempty"`
	LocationName     string  `json:"location_name,omitempty"`
	LocationCity     string  `json:"location_city,omitempty"`
	LocationProvince string  `json:"location_province,omitempty"`
	LocationCountry  string  `json:"location_country,omitempty"`
}

// IsPhotoLibraryType reports whether the library type should use photo scanning.
func IsPhotoLibraryType(libraryType string) bool {
	return strings.EqualFold(strings.TrimSpace(libraryType), "photo")
}

// ShouldScanFile reports whether a discovered file should be ingested for the library type.
func ShouldScanFile(libraryType, fileType string) bool {
	switch strings.ToLower(strings.TrimSpace(libraryType)) {
	case "photo":
		return fileType == "image"
	case "music":
		return fileType == "audio"
	case "document":
		return fileType == "document"
	default:
		return fileType == "video" || fileType == "audio"
	}
}

// ParseFromFile extracts dimensions and EXIF metadata from a local image file.
// It preserves the historical best-effort API; callers needing extraction
// diagnostics should use ParseFromFileWithDiagnostics.
func ParseFromFile(filePath string) PhotoMeta {
	meta, _ := ParseFromFileWithDiagnostics(filePath)
	return meta
}

// ParseFromFileWithDiagnostics extracts all available photo metadata and
// returns non-fatal extraction errors alongside any partial result.
func ParseFromFileWithDiagnostics(filePath string) (PhotoMeta, []error) {
	meta := PhotoMeta{
		Title:    strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath)),
		MimeType: guessMime(filePath),
	}
	data, readErr := os.ReadFile(filePath)
	if readErr != nil {
		return meta, []error{fmt.Errorf("read file: %w", readErr)}
	}
	var diagnostics []error
	if w, h, err := decodeDimensions(data); err != nil {
		diagnostics = append(diagnostics, fmt.Errorf("decode dimensions: %w", err))
	} else {
		meta.Width, meta.Height = w, h
	}
	if values, err := readEXIF(data); err != nil {
		if hasEXIFPayload(data) {
			diagnostics = append(diagnostics, fmt.Errorf("read EXIF: %w", err))
		}
	} else {
		meta.TakenAt = values.takenAt
		meta.CameraMake = values.cameraMake
		meta.CameraModel = values.cameraModel
		meta.LensModel = values.lensModel
		meta.Orientation = values.orientation
		if _, gpsTagErr := values.exif.Get(exif.GPSInfoIFDPointer); gpsTagErr == nil {
			if lat, lon, gpsErr := values.exif.LatLong(); gpsErr != nil {
				diagnostics = append(diagnostics, fmt.Errorf("read GPS: %w", gpsErr))
			} else {
				meta.Latitude, meta.Longitude, meta.HasGPS = lat, lon, true
			}
		}
	}
	if meta.TakenAt == "" {
		if st, err := os.Stat(filePath); err != nil {
			diagnostics = append(diagnostics, fmt.Errorf("read file time: %w", err))
		} else {
			meta.TakenAt = st.ModTime().UTC().Format(time.RFC3339)
		}
	}
	return meta, diagnostics
}

func hasEXIFPayload(data []byte) bool {
	if len(data) >= 2 && data[0] == 0xff && data[1] == 0xd8 {
		return jpegHasEXIF(data)
	}
	if len(data) >= 8 && (bytes.Equal(data[:4], []byte{'I', 'I', 42, 0}) || bytes.Equal(data[:4], []byte{'M', 'M', 0, 42})) {
		return tiffHasMetadataIFD(data)
	}
	if len(data) >= 12 && bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")) {
		return pngHasEXIF(data)
	}
	if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")) {
		return webpHasEXIF(data)
	}
	return false
}

func jpegHasEXIF(data []byte) bool {
	for offset := 2; offset+1 < len(data); {
		if data[offset] != 0xff {
			return false
		}
		for offset < len(data) && data[offset] == 0xff {
			offset++
		}
		if offset >= len(data) {
			return false
		}
		marker := data[offset]
		offset++
		if marker == 0xd9 || marker == 0xda {
			return false
		}
		if marker == 0x01 || marker >= 0xd0 && marker <= 0xd7 {
			continue
		}
		if offset+2 > len(data) {
			return false
		}
		length := int(binary.BigEndian.Uint16(data[offset : offset+2]))
		if length < 2 || offset+length > len(data) {
			return false
		}
		payload := data[offset+2 : offset+length]
		if marker == 0xe1 && len(payload) >= 6 && bytes.Equal(payload[:6], []byte("Exif\x00\x00")) {
			return true
		}
		offset += length
	}
	return false
}

func tiffHasMetadataIFD(data []byte) bool {
	var order binary.ByteOrder
	if bytes.Equal(data[:4], []byte{'I', 'I', 42, 0}) {
		order = binary.LittleEndian
	} else {
		order = binary.BigEndian
	}
	offset := uint64(order.Uint32(data[4:8]))
	if offset+2 > uint64(len(data)) {
		return false
	}
	count := uint64(order.Uint16(data[offset : offset+2]))
	if count > (uint64(len(data))-offset-2)/12 {
		return false
	}
	for i := uint64(0); i < count; i++ {
		entry := offset + 2 + i*12
		tag := order.Uint16(data[entry : entry+2])
		switch tag {
		case 0x0112, 0x010e, 0x010f, 0x0110, 0x0132, 0x8769, 0x8825:
			return true
		}
	}
	return false
}

func pngHasEXIF(data []byte) bool {
	for offset := 8; offset+12 <= len(data); {
		length := uint64(binary.BigEndian.Uint32(data[offset : offset+4]))
		end := uint64(offset) + 12 + length
		if end > uint64(len(data)) {
			return false
		}
		if bytes.Equal(data[offset+4:offset+8], []byte("eXIf")) {
			return true
		}
		offset = int(end)
	}
	return false
}

func webpHasEXIF(data []byte) bool {
	for offset := 12; offset+8 <= len(data); {
		length := uint64(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		end := uint64(offset) + 8 + length + length%2
		if end > uint64(len(data)) {
			return false
		}
		if bytes.Equal(data[offset:offset+4], []byte("EXIF")) {
			return true
		}
		offset = int(end)
	}
	return false
}

// ParseForMedia extracts photo metadata, materializing Knox .enc to a temp file when needed.
func ParseForMedia(db *sql.DB, vault *keystore.Vault, mediaID int64, filePath string) PhotoMeta {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return PhotoMeta{}
	}
	work := filePath
	if storage.InputNeedsPipe(db, mediaID, filePath) {
		tmp, cleanup, err := storage.MaterializePlaintextTemp(db, vault, mediaID, filePath)
		if err != nil {
			return PhotoMeta{Title: strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))}
		}
		defer cleanup()
		work = tmp
	}
	return ParseFromFile(work)
}

func decodeDimensions(data []byte) (int, int, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, fmt.Errorf("invalid dimensions %dx%d", cfg.Width, cfg.Height)
	}
	return cfg.Width, cfg.Height, nil
}

type exifValues struct {
	takenAt, cameraMake, cameraModel, lensModel string
	orientation                                 int
	exif                                        *exif.Exif
}

func readEXIF(data []byte) (exifValues, error) {
	x, err := exif.Decode(bytes.NewReader(data))
	if err != nil {
		return exifValues{}, err
	}
	values := exifValues{exif: x}
	if tm, err := x.DateTime(); err == nil {
		values.takenAt = tm.UTC().Format(time.RFC3339)
	}
	values.cameraMake = exifString(x, exif.Make)
	values.cameraModel = exifString(x, exif.Model)
	values.lensModel = exifString(x, exif.LensModel)
	if tag, err := x.Get(exif.Orientation); err == nil {
		if orientation, err := tag.Int(0); err == nil {
			values.orientation = orientation
		}
	}
	return values, nil
}

func exifString(x *exif.Exif, field exif.FieldName) string {
	tag, err := x.Get(field)
	if err != nil {
		return ""
	}
	value, err := tag.StringVal()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func guessMime(filePath string) string {
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".heic", ".heif":
		return "image/heic"
	case ".svg":
		return "image/svg+xml"
	case ".tif", ".tiff":
		return "image/tiff"
	default:
		return "application/octet-stream"
	}
}

// MergePhotoMetaJSON stores photo metadata under meta_json.photo.
func MergePhotoMetaJSON(raw string, meta PhotoMeta) string {
	var root map[string]any
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &root)
	}
	if root == nil {
		root = map[string]any{}
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return raw
	}
	var photo map[string]any
	_ = json.Unmarshal(b, &photo)
	if existing, ok := root["photo"].(map[string]any); ok {
		if tags, ok := existing["tags"]; ok {
			photo["tags"] = tags
		}
		if ai, ok := existing["ai_tags"]; ok {
			photo["ai_tags"] = ai
		}
	}
	root["photo"] = photo
	if strings.TrimSpace(meta.Title) != "" {
		root["title"] = meta.Title
	}
	if strings.TrimSpace(meta.TakenAt) != "" {
		root["release_date"] = meta.TakenAt[:10]
	}
	out, err := json.Marshal(root)
	if err != nil {
		return raw
	}
	return string(out)
}
