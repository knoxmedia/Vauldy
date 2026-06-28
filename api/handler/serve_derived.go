package handler

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	kcrypto "knox-media/internal/crypto"
	"knox-media/internal/storage"
)

// serveDerivedAsset streams a derived artifact, decrypting Knox .enc when needed.
func (h *Handler) serveDerivedAsset(c *gin.Context, mediaID int64, path, contentType string) {
	h.serveDerivedAssetKind(c, mediaID, path, contentType, "", "")
}

func (h *Handler) serveDerivedAssetKind(c *gin.Context, mediaID int64, path, contentType, kind, logicalName string) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "missing path"})
		return
	}
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		c.JSON(http.StatusNotFound, gin.H{"error": "file missing"})
		return
	}
	c.Header("Content-Type", contentType)
	if storage.NeedsDerivedEncryption(h.App.DB, mediaID) || kcrypto.IsEncFile(path) {
		c.Header("Cache-Control", "private, no-store")
	} else {
		c.Header("Cache-Control", "private, max-age=3600")
	}
	if !kcrypto.IsEncFile(path) {
		http.ServeFile(c.Writer, c.Request, path)
		return
	}
	seeker, err := storage.OpenDerivedArtifactSeeker(h.App.DB, h.KeyVault, mediaID, path, kind, logicalName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file missing"})
		return
	}
	defer seeker.Close()
	modTime := seeker.ModTime()
	if modTime.IsZero() {
		modTime = time.Now()
	}
	http.ServeContent(c.Writer, c.Request, filepath.Base(path), modTime, seeker)
}
