package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/internal/imagethumb"
	"knox-media/internal/photoparse"
	"knox-media/internal/storage"
)

func (h *Handler) photoCacheDir() string {
	if h == nil || h.App == nil || h.App.Config == nil {
		return ""
	}
	return filepath.Join(h.App.Config.Data.Preview, "photos")
}

func (h *Handler) ServePhotoThumb(c *gin.Context) {
	h.servePhotoVariant(c, "thumb")
}

func (h *Handler) ServePhotoMedium(c *gin.Context) {
	h.servePhotoVariant(c, "medium")
}

func (h *Handler) servePhotoVariant(c *gin.Context, variant string) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, false); !ok {
		return
	}
	var filePath, fileType sql.NullString
	if err := h.App.DB.QueryRow(`SELECT file_path, file_type FROM media WHERE id = ?`, id).Scan(&filePath, &fileType); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if fileType.String != "image" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "not an image"})
		return
	}
	paths := imagethumb.ResolvedPaths(h.App.DB, h.photoCacheDir(), id)
	target := paths.Thumb
	if variant == "medium" {
		target = paths.Medium
	}
	if st, err := os.Stat(target); err != nil || st.IsDir() || st.Size() == 0 {
		if enc, ok := storage.LookupEncPath(h.App.DB, id, map[bool]string{true: "photo_medium", false: "photo_thumb"}[variant == "medium"], map[bool]string{true: "medium.jpg", false: "thumb.jpg"}[variant == "medium"]); ok {
			target = enc
		}
	}
	if st, err := os.Stat(target); err != nil || st.IsDir() || st.Size() == 0 {
		if genErr := h.ensurePhotoVariants(id, filePath.String); genErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": genErr.Error()})
			return
		}
		paths = imagethumb.ResolvedPaths(h.App.DB, h.photoCacheDir(), id)
		target = paths.Thumb
		if variant == "medium" {
			target = paths.Medium
		}
	}
	h.serveDerivedAsset(c, id, target, "image/jpeg")
}

func (h *Handler) PhotoPreviewInfo(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, false); !ok {
		return
	}
	var fileType sql.NullString
	var metaJSON sql.NullString
	var w, hei sql.NullInt64
	if err := h.App.DB.QueryRow(`
		SELECT file_type, COALESCE(meta_json,''), width, height
		FROM media WHERE id = ?`, id).Scan(&fileType, &metaJSON, &w, &hei); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if fileType.String != "image" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "not an image"})
		return
	}
	base := "http://" + c.Request.Host
	if c.Request.TLS != nil {
		base = "https://" + c.Request.Host
	}
	token := strings.TrimSpace(c.Query("access_token"))
	qs := ""
	if token != "" {
		qs = "?access_token=" + token
	}
	paths := imagethumb.ExpectedPaths(h.photoCacheDir(), id)
	thumbReady := fileExists(paths.Thumb)
	mediumReady := fileExists(paths.Medium)
	resp := gin.H{
		"thumb_url":   base + "/api/v1/media/" + c.Param("id") + "/photo/thumb.jpg" + qs,
		"medium_url":  base + "/api/v1/media/" + c.Param("id") + "/photo/medium.jpg" + qs,
		"original_url": base + "/api/v1/media/" + c.Param("id") + "/play" + qs,
		"thumb_ready": thumbReady,
		"medium_ready": mediumReady,
		"width":       w.Int64,
		"height":      hei.Int64,
	}
	if photo := decodePhotoMeta(metaJSON.String); photo != nil {
		if photo.TakenAt != "" {
			resp["taken_at"] = photo.TakenAt
		}
		if photo.CameraMake != "" {
			resp["camera_make"] = photo.CameraMake
		}
		if photo.CameraModel != "" {
			resp["camera_model"] = photo.CameraModel
		}
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) ensurePhotoVariants(mediaID int64, srcPath string) error {
	ffmpegPath := ""
	if h.App != nil && h.App.Config != nil {
		ffmpegPath = strings.TrimSpace(h.App.Config.FFmpeg.FFmpegPath)
	}
	paths, err := imagethumb.Ensure(context.Background(), h.App.DB, h.KeyVault, h.DerivedStore, ffmpegPath, srcPath, h.photoCacheDir(), mediaID)
	if err != nil {
		return err
	}
	var metaJSON sql.NullString
	_ = h.App.DB.QueryRow(`SELECT COALESCE(meta_json,'') FROM media WHERE id = ?`, mediaID).Scan(&metaJSON)
	var root map[string]any
	if strings.TrimSpace(metaJSON.String) != "" {
		_ = json.Unmarshal([]byte(metaJSON.String), &root)
	}
	if root == nil {
		root = map[string]any{}
	}
	photo, _ := root["photo"].(map[string]any)
	if photo == nil {
		photo = map[string]any{}
	}
	photo["thumb_path"] = paths.Thumb
	photo["medium_path"] = paths.Medium
	root["photo"] = photo
	b, _ := json.Marshal(root)
	_, _ = h.App.DB.Exec(`UPDATE media SET meta_json = ? WHERE id = ?`, string(b), mediaID)
	h.scheduleLibraryPreviewRefreshForMedia(mediaID)
	return nil
}

func decodePhotoMeta(raw string) *photoparse.PhotoMeta {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var root struct {
		Photo photoparse.PhotoMeta `json:"photo"`
	}
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return nil
	}
	if root.Photo.TakenAt == "" && root.Photo.Width == 0 {
		return nil
	}
	return &root.Photo
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Size() > 0
}

// resolvePhotoThumbSource returns a readable JPEG path for face crop / detection.
// Encrypted Knox .enc thumbs are materialized to a temp file when needed.
func (h *Handler) resolvePhotoThumbSource(mediaID int64, catalogPath string) (workPath string, cleanup func(), err error) {
	cleanup = func() {}
	if h == nil || mediaID <= 0 {
		return "", cleanup, fmt.Errorf("invalid media id")
	}
	paths := imagethumb.ResolvedPaths(h.App.DB, h.photoCacheDir(), mediaID)
	thumb := strings.TrimSpace(paths.Thumb)
	if thumb == "" || !fileExists(thumb) {
		if enc, ok := storage.LookupEncPath(h.App.DB, mediaID, "photo_thumb", "thumb.jpg"); ok && fileExists(enc) {
			thumb = enc
		}
	}
	if thumb == "" || !fileExists(thumb) {
		if genErr := h.ensurePhotoVariants(mediaID, catalogPath); genErr != nil {
			return "", cleanup, genErr
		}
		paths = imagethumb.ResolvedPaths(h.App.DB, h.photoCacheDir(), mediaID)
		thumb = strings.TrimSpace(paths.Thumb)
	}
	if thumb == "" || !fileExists(thumb) {
		return "", cleanup, fmt.Errorf("photo thumb unavailable")
	}
	return storage.MaterializeCLIFile(h.App.DB, h.KeyVault, mediaID, thumb)
}

// GeneratePhotoVariants creates cached thumb/medium JPEGs for an image media item.
func (h *Handler) GeneratePhotoVariants(mediaID int64) {
	if h == nil || mediaID <= 0 {
		return
	}
	var filePath, fileType sql.NullString
	if err := h.App.DB.QueryRow(`SELECT file_path, file_type FROM media WHERE id = ?`, mediaID).Scan(&filePath, &fileType); err != nil {
		return
	}
	if fileType.String != "image" || !filePath.Valid || strings.TrimSpace(filePath.String) == "" {
		return
	}
	_ = h.ensurePhotoVariants(mediaID, filePath.String)
}
