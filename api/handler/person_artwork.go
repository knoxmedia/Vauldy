package handler

import (
	"database/sql"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func (h *Handler) personAvatarCacheFile(personID int64) string {
	if h == nil || h.App == nil || h.App.Config == nil || personID <= 0 {
		return ""
	}
	preview := strings.TrimSpace(h.App.Config.Data.Preview)
	if preview == "" {
		return ""
	}
	return filepath.Join(preview, "cast-person", strconv.FormatInt(personID, 10), "avatar.jpg")
}

func (h *Handler) materializePersonAvatar(personID int64, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || personID <= 0 {
		return ""
	}
	if local := h.existingArtworkFile(raw); local != "" {
		if local != raw && artworkFileReady(local) {
			if cached := h.copyToPersonAvatarCache(personID, local); cached != "" {
				return cached
			}
		}
		return local
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		if cached := h.downloadPersonAvatarURL(personID, raw); cached != "" {
			return cached
		}
		return raw
	}
	return raw
}

func (h *Handler) copyToPersonAvatarCache(personID int64, src string) string {
	outFile := h.personAvatarCacheFile(personID)
	if outFile == "" || !artworkFileReady(src) {
		return ""
	}
	if err := os.MkdirAll(filepath.Dir(outFile), 0o755); err != nil {
		return ""
	}
	in, err := os.Open(src)
	if err != nil {
		return ""
	}
	defer in.Close()
	out, err := os.Create(outFile)
	if err != nil {
		return ""
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return ""
	}
	return outFile
}

func (h *Handler) downloadPersonAvatarURL(personID int64, u string) string {
	outFile := h.personAvatarCacheFile(personID)
	if outFile == "" {
		return ""
	}
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp != nil {
			resp.Body.Close()
		}
		return ""
	}
	defer resp.Body.Close()
	if err := os.MkdirAll(filepath.Dir(outFile), 0o755); err != nil {
		return ""
	}
	out, err := os.Create(outFile)
	if err != nil {
		return ""
	}
	defer out.Close()
	if _, err := io.Copy(out, resp.Body); err != nil {
		return ""
	}
	return outFile
}

// ServePersonAvatar serves cast person avatar.
func (h *Handler) ServePersonAvatar(c *gin.Context) {
	personID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || personID <= 0 {
		c.Status(http.StatusBadRequest)
		return
	}
	var avatarPath sql.NullString
	if err := h.App.DB.QueryRow(`SELECT avatar_url FROM cast_person WHERE id = ? AND deleted_at IS NULL`, personID).Scan(&avatarPath); err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	stored := strings.TrimSpace(avatarPath.String)
	if p := h.existingArtworkFile(stored); p != "" {
		c.File(p)
		return
	}
	if cached := h.personAvatarCacheFile(personID); artworkFileReady(cached) {
		c.File(cached)
		return
	}
	c.Status(http.StatusNotFound)
}
