package handler

import (
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"knox-media/api/middleware"
)

type playlistItem struct {
	ID        int64  `json:"id"`
	MediaID   int64  `json:"media_id"`
	SortOrder int    `json:"sort_order"`
	Title     string `json:"title"`
	FileType  string `json:"file_type"`
	Duration  int64  `json:"duration"`
	Width     int64  `json:"width"`
	Height    int64  `json:"height"`
	AddedAt   string `json:"added_at"`
}

type playlistResp struct {
	ID           int64          `json:"id"`
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	PosterURL    string         `json:"poster_url"`
	BackgroundURL string        `json:"background_url"`
	LogoURL       string         `json:"logo_url"`
	SquareArtURL  string         `json:"square_art_url"`
	ItemCount     int            `json:"item_count"`
	FirstMediaID int64          `json:"first_media_id"`
	CreatedAt    string         `json:"created_at"`
	UpdatedAt    string         `json:"updated_at"`
	Items        []playlistItem `json:"items,omitempty"`
}

// ListPlaylists returns all playlists for the current user (summary, no items).
func (h *Handler) ListPlaylists(c *gin.Context) {
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not available for API client credentials"})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	rows, err := h.App.DB.Query(`
		SELECT p.id, p.name, p.description, p.poster_url, p.background_url, p.logo_url, p.square_art_url,
			p.created_at, p.updated_at,
			(SELECT COUNT(*) FROM playlist_item WHERE playlist_id = p.id) AS item_count,
			(SELECT pi.media_id FROM playlist_item pi WHERE pi.playlist_id = p.id
			 ORDER BY pi.sort_order ASC, pi.id ASC LIMIT 1) AS first_media_id
		FROM playlist p
		WHERE p.user_id = ?
		ORDER BY p.updated_at DESC
		LIMIT 100`, uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	var playlists []playlistResp
	for rows.Next() {
		var id int64
		var name, desc, posterURL, bgURL, logoURL, squareArtURL, created, updated sql.NullString
		var itemCount int
		var firstMediaID sql.NullInt64
		if err := rows.Scan(&id, &name, &desc, &posterURL, &bgURL, &logoURL, &squareArtURL,
			&created, &updated, &itemCount, &firstMediaID); err != nil {
			continue
		}
		var fm int64
		if firstMediaID.Valid {
			fm = firstMediaID.Int64
		}
		playlists = append(playlists, playlistResp{
			ID:            id,
			Name:          name.String,
			Description:   desc.String,
			PosterURL:     posterURL.String,
			BackgroundURL: bgURL.String,
			LogoURL:       logoURL.String,
			SquareArtURL:  squareArtURL.String,
			ItemCount:     itemCount,
			FirstMediaID:  fm,
			CreatedAt:    created.String,
			UpdatedAt:    updated.String,
		})
	}
	if playlists == nil {
		playlists = []playlistResp{}
	}
	c.JSON(http.StatusOK, gin.H{"items": playlists})
}

// GetPlaylist returns a single playlist with its items.
func (h *Handler) GetPlaylist(c *gin.Context) {
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not available for API client credentials"})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	pid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || pid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid playlist id"})
		return
	}
	var name, desc, posterURL, bgURL, logoURL, squareArtURL, created, updated sql.NullString
	var ownerID int64
	err = h.App.DB.QueryRow(`
		SELECT user_id, name, description, poster_url, background_url, logo_url, square_art_url,
			created_at, updated_at
		FROM playlist WHERE id = ?`, pid).Scan(&ownerID, &name, &desc,
		&posterURL, &bgURL, &logoURL, &squareArtURL, &created, &updated)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "playlist not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if ownerID != uid {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		return
	}
	rows, err := h.App.DB.Query(`
		SELECT pi.id, pi.media_id, pi.sort_order, pi.added_at,
			m.title, m.file_type, m.duration, m.width, m.height
		FROM playlist_item pi
		JOIN media m ON m.id = pi.media_id
		WHERE pi.playlist_id = ?
		ORDER BY pi.sort_order ASC`, pid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	var items []playlistItem
	for rows.Next() {
		var pi playlistItem
		var title, ftype sql.NullString
		var dur, w, hgt sql.NullInt64
		var added sql.NullString
		if err := rows.Scan(&pi.ID, &pi.MediaID, &pi.SortOrder, &added,
			&title, &ftype, &dur, &w, &hgt); err != nil {
			continue
		}
		pi.Title = title.String
		pi.FileType = ftype.String
		pi.Duration = dur.Int64
		pi.Width = w.Int64
		pi.Height = hgt.Int64
		pi.AddedAt = added.String
		items = append(items, pi)
	}
	if items == nil {
		items = []playlistItem{}
	}
	c.JSON(http.StatusOK, gin.H{
		"id":           pid,
		"name":         name.String,
		"description":  desc.String,
		"poster_url":   posterURL.String,
		"background_url": bgURL.String,
		"logo_url":        logoURL.String,
		"square_art_url": squareArtURL.String,
		"created_at":    created.String,
		"updated_at":   updated.String,
		"items":        items,
	})
}

// CreatePlaylist creates a new playlist.
func (h *Handler) CreatePlaylist(c *gin.Context) {
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not available for API client credentials"})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var body struct {
		Name          string `json:"name" binding:"required"`
		Description   string `json:"description"`
		PosterURL     string `json:"poster_url"`
		BackgroundURL string `json:"background_url"`
		LogoURL       string `json:"logo_url"`
		SquareArtURL  string `json:"square_art_url"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	result, err := h.App.DB.Exec(
		`INSERT INTO playlist (user_id, name, description, poster_url, background_url, logo_url, square_art_url) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		uid, body.Name, body.Description, body.PosterURL, body.BackgroundURL, body.LogoURL, body.SquareArtURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	id, _ := result.LastInsertId()
	c.JSON(http.StatusOK, gin.H{"id": id, "ok": true})
}

// UpdatePlaylist updates a playlist's name/description.
func (h *Handler) UpdatePlaylist(c *gin.Context) {
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not available for API client credentials"})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	pid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || pid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid playlist id"})
		return
	}
	var ownerID int64
	err = h.App.DB.QueryRow(`SELECT user_id FROM playlist WHERE id = ?`, pid).Scan(&ownerID)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "playlist not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if ownerID != uid {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		return
	}
	var body struct {
		Name          string `json:"name" binding:"required"`
		Description   string `json:"description"`
		PosterURL     string `json:"poster_url"`
		BackgroundURL string `json:"background_url"`
		LogoURL       string `json:"logo_url"`
		SquareArtURL  string `json:"square_art_url"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	_, err = h.App.DB.Exec(
		`UPDATE playlist SET name = ?, description = ?, poster_url = ?, background_url = ?, logo_url = ?, square_art_url = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		body.Name, body.Description, body.PosterURL, body.BackgroundURL, body.LogoURL, body.SquareArtURL, pid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// DeletePlaylist deletes a playlist.
func (h *Handler) DeletePlaylist(c *gin.Context) {
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not available for API client credentials"})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	pid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || pid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid playlist id"})
		return
	}
	var ownerID int64
	err = h.App.DB.QueryRow(`SELECT user_id FROM playlist WHERE id = ?`, pid).Scan(&ownerID)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "playlist not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if ownerID != uid {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		return
	}
	_, err = h.App.DB.Exec(`DELETE FROM playlist WHERE id = ?`, pid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// AddPlaylistItem adds a media item to a playlist.
func (h *Handler) AddPlaylistItem(c *gin.Context) {
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not available for API client credentials"})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	pid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || pid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid playlist id"})
		return
	}
	var ownerID int64
	err = h.App.DB.QueryRow(`SELECT user_id FROM playlist WHERE id = ?`, pid).Scan(&ownerID)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "playlist not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if ownerID != uid {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		return
	}
	var body struct {
		MediaID int64 `json:"media_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "media_id is required"})
		return
	}
	if _, ok := h.requireMediaAccess(c, body.MediaID, false); !ok {
		return
	}
	// Get current max sort_order
	var maxOrder sql.NullInt64
	_ = h.App.DB.QueryRow(`SELECT MAX(sort_order) FROM playlist_item WHERE playlist_id = ?`, pid).Scan(&maxOrder)
	nextOrder := 0
	if maxOrder.Valid {
		nextOrder = int(maxOrder.Int64) + 1
	}
	_, err = h.App.DB.Exec(
		`INSERT OR IGNORE INTO playlist_item (playlist_id, media_id, sort_order) VALUES (?, ?, ?)`,
		pid, body.MediaID, nextOrder)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Update playlist updated_at
	_, _ = h.App.DB.Exec(`UPDATE playlist SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, pid)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// RemovePlaylistItem removes a media item from a playlist.
func (h *Handler) RemovePlaylistItem(c *gin.Context) {
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not available for API client credentials"})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	pid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || pid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid playlist id"})
		return
	}
	itemID, err := strconv.ParseInt(c.Param("itemId"), 10, 64)
	if err != nil || itemID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid item id"})
		return
	}
	var ownerID int64
	err = h.App.DB.QueryRow(`SELECT user_id FROM playlist WHERE id = ?`, pid).Scan(&ownerID)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "playlist not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if ownerID != uid {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		return
	}
	_, err = h.App.DB.Exec(`DELETE FROM playlist_item WHERE id = ? AND playlist_id = ?`, itemID, pid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	_, _ = h.App.DB.Exec(`UPDATE playlist SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, pid)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ReorderPlaylistItems updates sort orders for all items in a playlist.
func (h *Handler) ReorderPlaylistItems(c *gin.Context) {
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not available for API client credentials"})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	pid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || pid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid playlist id"})
		return
	}
	var ownerID int64
	err = h.App.DB.QueryRow(`SELECT user_id FROM playlist WHERE id = ?`, pid).Scan(&ownerID)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "playlist not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if ownerID != uid {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		return
	}
	var body struct {
		Items []struct {
			ID        int64 `json:"id"`
			SortOrder int   `json:"sort_order"`
		} `json:"items" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "items required"})
		return
	}
	tx, err := h.App.DB.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	stmt, err := tx.Prepare(`UPDATE playlist_item SET sort_order = ? WHERE id = ? AND playlist_id = ?`)
	if err != nil {
		tx.Rollback()
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for _, item := range body.Items {
		_, _ = stmt.Exec(item.SortOrder, item.ID, pid)
	}
	stmt.Close()
	_ = tx.Commit()
	_, _ = h.App.DB.Exec(`UPDATE playlist SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, pid)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// UploadPlaylistImage handles image uploads for a playlist (poster, background, logo, square_art).
func (h *Handler) UploadPlaylistImage(c *gin.Context) {
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not available for API client credentials"})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	pid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || pid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid playlist id"})
		return
	}
	var ownerID int64
	err = h.App.DB.QueryRow(`SELECT user_id FROM playlist WHERE id = ?`, pid).Scan(&ownerID)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "playlist not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if ownerID != uid {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		return
	}
	field := c.Param("field")
	if field != "poster" && field != "background" && field != "logo" && field != "square_art" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "field must be poster, background, logo, or square_art"})
		return
	}
	fh, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file required"})
		return
	}
	ext := filepath.Ext(filepath.Base(fh.Filename))
	if ext == "" {
		ext = ".jpg"
	}
	filename := "playlist-" + strconv.FormatInt(pid, 10) + "-" + field + "-" + uuid.NewString()[0:8] + ext
	dest := filepath.Join(h.Upload.UploadDir, "playlists", filename)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create directory"})
		return
	}
	if err := c.SaveUploadedFile(fh, dest); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	url := "/uploads/playlists/" + filename
	col := field + "_url"
	_, _ = h.App.DB.Exec(`UPDATE playlist SET ` + col + ` = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, url, pid)
	c.JSON(http.StatusOK, gin.H{"ok": true, "url": url})
}