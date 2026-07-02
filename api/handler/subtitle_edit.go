package handler

import (
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/internal/subtitle"
)

type subtitleCueDTO struct {
	Start string `json:"start"`
	End   string `json:"end"`
	Text  string `json:"text"`
}

// GetSubtitleCues returns the parsed cue list of a ready subtitle for inline editing.
func (h *Handler) GetSubtitleCues(c *gin.Context) {
	if h.Subtitle == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "subtitle service disabled"})
		return
	}
	mid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || mid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	sid, err := strconv.ParseInt(c.Param("sid"), 10, 64)
	if err != nil || sid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid subtitle id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, mid, false); !ok {
		return
	}
	format, cues, err := h.Subtitle.SubtitleCues(mid, sid, h.KeyVault)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	out := make([]subtitleCueDTO, len(cues))
	for i, cu := range cues {
		out[i] = subtitleCueDTO{Start: cu.Start, End: cu.End, Text: cu.Text}
	}
	c.JSON(http.StatusOK, gin.H{"format": format, "cues": out})
}

type saveSubtitleCuesBody struct {
	Cues []subtitleCueDTO `json:"cues"`
}

// SaveSubtitleCues re-renders edited cues and writes them back to the subtitle file.
func (h *Handler) SaveSubtitleCues(c *gin.Context) {
	if h.Subtitle == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "subtitle service disabled"})
		return
	}
	mid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || mid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	sid, err := strconv.ParseInt(c.Param("sid"), 10, 64)
	if err != nil || sid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid subtitle id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, mid, false); !ok {
		return
	}
	var body saveSubtitleCuesBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(body.Cues) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no cues provided"})
		return
	}
	cues := make([]subtitle.Cue, len(body.Cues))
	for i, cu := range body.Cues {
		cues[i] = subtitle.Cue{Start: cu.Start, End: cu.End, Text: cu.Text}
	}
	if err := h.Subtitle.SaveSubtitleCues(c.Request.Context(), mid, sid, cues); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ImportSubtitle uploads a .vtt/.srt/.ass file and adds it as a new subtitle track.
func (h *Handler) ImportSubtitle(c *gin.Context) {
	if h.Subtitle == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "subtitle service disabled"})
		return
	}
	mid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || mid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, mid, false); !ok {
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
	name := strings.TrimSpace(filepath.Base(fh.Filename))
	row, err := h.Subtitle.ImportSubtitleFile(c.Request.Context(), mid, name, data)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "subtitle": row})
}
