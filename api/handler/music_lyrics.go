package handler

import (
	"database/sql"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/internal/musiclyrics"
	"knox-media/internal/storage"
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
		var vttPath, lrcPath sql.NullString
		_ = h.App.DB.QueryRow(`SELECT vtt_path, lrc_path FROM lyric_task WHERE media_id = ? AND status = 'done'`, id).Scan(&vttPath, &lrcPath)
		if lrc := strings.TrimSpace(lrcPath.String); lrc != "" {
			if b, err := h.readLyricFile(id, lrc); err == nil {
				c.JSON(http.StatusOK, gin.H{"lrc": string(b), "source": "asr"})
				return
			}
		}
		if vtt := strings.TrimSpace(vttPath.String); vtt != "" {
			if b, err := h.readLyricFile(id, vtt); err == nil {
				if lrcText := musiclyrics.VTTToLRC(string(b)); lrcText != "" {
					c.JSON(http.StatusOK, gin.H{"lrc": lrcText, "source": "asr"})
					return
				}
			}
		}
		c.JSON(http.StatusOK, gin.H{"lrc": "", "source": ""})
		return
	}
	c.JSON(http.StatusOK, gin.H{"lrc": content, "source": source})
}

func (h *Handler) readLyricFile(mediaID int64, path string) ([]byte, error) {
	seeker, err := storage.OpenDerivedSeeker(h.App.DB, h.KeyVault, mediaID, path)
	if err != nil {
		return nil, err
	}
	defer seeker.Close()
	return io.ReadAll(seeker)
}

type saveLyricsBody struct {
	Lrc string `json:"lrc"`
}

// SaveMediaLyrics persists edited or imported LRC content for an audio track.
func (h *Handler) SaveMediaLyrics(c *gin.Context) {
	if h.LyricWorker == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "lyric worker disabled"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, false); !ok {
		return
	}
	var body saveLyricsBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.LyricWorker.SaveLyrics(c.Request.Context(), id, body.Lrc); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ImportMediaLyrics uploads a .lrc (or .vtt) file and saves it as the track's lyrics.
func (h *Handler) ImportMediaLyrics(c *gin.Context) {
	if h.LyricWorker == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "lyric worker disabled"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, false); !ok {
		return
	}
	fh, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file required"})
		return
	}
	f, err := fh.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty file"})
		return
	}
	// Accept .vtt too: convert to LRC when it looks like WebVTT.
	name := strings.ToLower(filepath.Base(fh.Filename))
	if strings.HasSuffix(name, ".vtt") || strings.HasPrefix(content, "WEBVTT") {
		if lrc := musiclyrics.VTTToLRC(content); strings.TrimSpace(lrc) != "" {
			content = lrc
		}
	}
	if err := h.LyricWorker.SaveLyrics(c.Request.Context(), id, content); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
