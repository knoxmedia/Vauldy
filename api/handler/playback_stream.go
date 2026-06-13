package handler

import (
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"

	kcrypto "knox-media/internal/crypto"
	"knox-media/internal/storage"
)

// serveMediaSource streams the media file to the client with Range support.
// Encrypted .enc assets are decrypted in memory and served via http.ServeContent (no temp files).
func (h *Handler) serveMediaSource(c *gin.Context, mediaID int64, path, downloadName string) {
	path = filepath.Clean(path)
	if fi, err := os.Stat(path); err == nil && !fi.IsDir() && !kcrypto.IsEncFile(path) {
		c.Header("Accept-Ranges", "bytes")
		http.ServeFile(c.Writer, c.Request, path)
		return
	}

	seeker, err := storage.OpenPlaintextSeeker(h.App.DB, h.KeyVault, mediaID, path)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file missing"})
		return
	}
	defer seeker.Close()

	modTime := seeker.ModTime()
	if modTime.IsZero() {
		modTime = time.Now()
	}
	c.Header("Accept-Ranges", "bytes")
	c.Header("Cache-Control", "private, no-store")
	http.ServeContent(c.Writer, c.Request, downloadName, modTime, seeker)
}
