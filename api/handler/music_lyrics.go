package handler

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/internal/musiclyrics"
)

// GetMediaLyrics returns LRC lyrics for an audio track (sidecar file or embedded tags).
func (h *Handler) GetMediaLyrics(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	libID, ok := h.requireMediaAccess(c, id, false)
	if !ok {
		return
	}
	var filePath, metaJSON sql.NullString
	if err := h.App.DB.QueryRow(`SELECT file_path, COALESCE(meta_json,'') FROM media WHERE id = ?`, id).Scan(&filePath, &metaJSON); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	absPath := h.resolveMediaAbsolutePath(libID, filePath.String)
	ffprobePath := strings.TrimSpace(h.App.Config.FFmpeg.FFprobePath)
	content, source, found := musiclyrics.Load(absPath, metaJSON.String, ffprobePath)
	if !found {
		c.JSON(http.StatusOK, gin.H{"lrc": "", "source": ""})
		return
	}
	c.JSON(http.StatusOK, gin.H{"lrc": content, "source": source})
}
