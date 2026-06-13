package photoparse

import (
	"database/sql"
	"encoding/json"
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
func ParseFromFile(filePath string) PhotoMeta {
	meta := PhotoMeta{
		Title: strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath)),
	}
	meta.MimeType = guessMime(filePath)
	if w, h, ok := decodeDimensions(filePath); ok {
		meta.Width = w
		meta.Height = h
	}
	taken, camMake, camModel := readEXIF(filePath)
	if taken != "" {
		meta.TakenAt = taken
	}
	if camMake != "" {
		meta.CameraMake = camMake
	}
	if camModel != "" {
		meta.CameraModel = camModel
	}
	if lat, lon, gpsOK := readGPS(filePath); gpsOK {
		meta.Latitude = lat
		meta.Longitude = lon
		meta.HasGPS = true
	}
	if meta.TakenAt == "" {
		if st, err := os.Stat(filePath); err == nil {
			meta.TakenAt = st.ModTime().UTC().Format(time.RFC3339)
		}
	}
	return meta
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

func decodeDimensions(filePath string) (int, int, bool) {
	f, err := os.Open(filePath)
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0, false
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

func readEXIF(filePath string) (takenAt, makeName, modelName string) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", "", ""
	}
	defer f.Close()
	x, err := exif.Decode(f)
	if err != nil {
		return "", "", ""
	}
	if tm, err := x.DateTime(); err == nil {
		takenAt = tm.UTC().Format(time.RFC3339)
	}
	if tag, err := x.Get(exif.Make); err == nil {
		if v, err := tag.StringVal(); err == nil {
			makeName = strings.TrimSpace(v)
		}
	}
	if tag, err := x.Get(exif.Model); err == nil {
		if v, err := tag.StringVal(); err == nil {
			modelName = strings.TrimSpace(v)
		}
	}
	return takenAt, makeName, modelName
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
