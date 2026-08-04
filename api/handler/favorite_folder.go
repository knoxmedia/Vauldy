package handler

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
)

type favoriteFolderItem struct {
	ID        int64  `json:"id"`
	MediaID   int64  `json:"media_id"`
	SortOrder int    `json:"sort_order"`
	Title     string `json:"title"`
	FileType  string `json:"file_type"`
	Duration  int64  `json:"duration"`
	Width     int64  `json:"width"`
	Height    int64  `json:"height"`
	PosterURL string `json:"poster_url"`
	AddedAt   string `json:"added_at"`
}

type favoriteFolderPreview struct {
	MediaID   int64  `json:"media_id"`
	PosterURL string `json:"poster_url"`
}

type favoriteFolderResp struct {
	ID           int64                   `json:"id"`
	Name         string                  `json:"name"`
	Description  string                  `json:"description"`
	ItemCount    int                     `json:"item_count"`
	FirstMediaID int64                   `json:"first_media_id"`
	CoverURL     string                  `json:"cover_url"`
	CreatedAt    string                  `json:"created_at"`
	UpdatedAt    string                  `json:"updated_at"`
	PreviewItems []favoriteFolderPreview `json:"preview_items"`
	Items        []favoriteFolderItem    `json:"items,omitempty"`
}

func (h *Handler) ListFavoriteFolders(c *gin.Context) {
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
		SELECT f.id, f.name, f.description, f.created_at, f.updated_at,
			(SELECT COUNT(*) FROM favorite_folder_item WHERE folder_id = f.id) AS item_count,
			(SELECT ffi.media_id FROM favorite_folder_item ffi
			 WHERE ffi.folder_id = f.id ORDER BY ffi.sort_order ASC, ffi.id ASC LIMIT 1) AS first_media_id,
			(SELECT COALESCE(
				NULLIF(TRIM(json_extract(m.meta_json, '$.scrape.poster')), ''),
				NULLIF(TRIM(json_extract(m.meta_json, '$.scrape.extra.poster')), '')
			) FROM favorite_folder_item ffi
			 JOIN media m ON m.id = ffi.media_id
			 WHERE ffi.folder_id = f.id ORDER BY ffi.sort_order ASC, ffi.id ASC LIMIT 1) AS cover_url
		FROM favorite_folder f
		WHERE f.user_id = ?
		ORDER BY f.updated_at DESC
		LIMIT 200`, uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	var items []favoriteFolderResp
	for rows.Next() {
		var id int64
		var name, desc, created, updated, coverURL sql.NullString
		var itemCount int
		var firstMediaID sql.NullInt64
		if err := rows.Scan(&id, &name, &desc, &created, &updated, &itemCount, &firstMediaID, &coverURL); err != nil {
			continue
		}
		var fm int64
		if firstMediaID.Valid {
			fm = firstMediaID.Int64
		}
		items = append(items, favoriteFolderResp{
			ID:           id,
			Name:         name.String,
			Description:  desc.String,
			ItemCount:    itemCount,
			FirstMediaID: fm,
			CoverURL:     coverURL.String,
			CreatedAt:    created.String,
			UpdatedAt:    updated.String,
		})
	}
	if items == nil {
		items = []favoriteFolderResp{}
	}
	items = h.attachFavoriteFolderPreviews(items)
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) attachFavoriteFolderPreviews(folders []favoriteFolderResp) []favoriteFolderResp {
	if len(folders) == 0 {
		return folders
	}
	ids := make([]int64, len(folders))
	idxByID := make(map[int64]int, len(folders))
	for i, f := range folders {
		ids[i] = f.ID
		idxByID[f.ID] = i
		folders[i].PreviewItems = []favoriteFolderPreview{}
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	q := fmt.Sprintf(`
		SELECT ffi.folder_id, ffi.media_id,
			COALESCE(
				NULLIF(TRIM(json_extract(m.meta_json, '$.scrape.poster')), ''),
				NULLIF(TRIM(json_extract(m.meta_json, '$.scrape.extra.poster')), '')
			) AS poster_url
		FROM favorite_folder_item ffi
		JOIN media m ON m.id = ffi.media_id
		WHERE ffi.folder_id IN (%s)
		ORDER BY ffi.folder_id, ffi.sort_order ASC, ffi.id ASC`, placeholders)
	rows, err := h.App.DB.Query(q, args...)
	if err != nil {
		return folders
	}
	defer rows.Close()
	counts := make(map[int64]int, len(folders))
	for rows.Next() {
		var folderID, mediaID int64
		var posterURL sql.NullString
		if rows.Scan(&folderID, &mediaID, &posterURL) != nil {
			continue
		}
		if counts[folderID] >= 6 {
			continue
		}
		idx, ok := idxByID[folderID]
		if !ok {
			continue
		}
		folders[idx].PreviewItems = append(folders[idx].PreviewItems, favoriteFolderPreview{
			MediaID:   mediaID,
			PosterURL: posterURL.String,
		})
		counts[folderID]++
	}
	return folders
}

func (h *Handler) GetFavoriteFolder(c *gin.Context) {
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not available for API client credentials"})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	fid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || fid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid folder id"})
		return
	}
	var name, desc, created, updated sql.NullString
	var ownerID int64
	err = h.App.DB.QueryRow(`
		SELECT user_id, name, description, created_at, updated_at
		FROM favorite_folder WHERE id = ?`, fid).Scan(&ownerID, &name, &desc, &created, &updated)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "folder not found"})
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
	profile, _ := h.loadUserPermissionProfile(uid)
	rows, err := h.App.DB.Query(`
		SELECT fi.id, fi.media_id, fi.sort_order, fi.added_at,
			m.title, m.file_type, m.duration, m.width, m.height, m.file_path, m.library_id,
			COALESCE(
				NULLIF(TRIM(json_extract(m.meta_json, '$.scrape.poster')), ''),
				NULLIF(TRIM(json_extract(m.meta_json, '$.scrape.extra.poster')), '')
			) AS poster_url
		FROM favorite_folder_item fi
		JOIN media m ON m.id = fi.media_id
		WHERE fi.folder_id = ?
		ORDER BY fi.sort_order ASC, fi.id ASC`, fid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	var items []favoriteFolderItem
	for rows.Next() {
		var fi favoriteFolderItem
		var title, ftype, added, posterURL, fpath sql.NullString
		var libID sql.NullInt64
		var dur, w, hgt sql.NullInt64
		if err := rows.Scan(&fi.ID, &fi.MediaID, &fi.SortOrder, &added,
			&title, &ftype, &dur, &w, &hgt, &fpath, &libID, &posterURL); err != nil {
			continue
		}
		if strings.EqualFold(profile.LibraryScope, "selected") {
			if _, ok := profile.AllowedLibraryIDs[libID.Int64]; !ok {
				continue
			}
			if folders := profile.AllowedLibraryFolders[libID.Int64]; len(folders) > 0 && !pathMatchesAnyFolder(fpath.String, folders) {
				continue
			}
		}
		fi.Title = title.String
		fi.FileType = ftype.String
		fi.Duration = dur.Int64
		fi.Width = w.Int64
		fi.Height = hgt.Int64
		fi.PosterURL = posterURL.String
		fi.AddedAt = added.String
		items = append(items, fi)
	}
	if items == nil {
		items = []favoriteFolderItem{}
	}
	c.JSON(http.StatusOK, gin.H{
		"id":          fid,
		"name":        name.String,
		"description": desc.String,
		"item_count":  len(items),
		"created_at":  created.String,
		"updated_at":  updated.String,
		"items":       items,
	})
}

func (h *Handler) CreateFavoriteFolder(c *gin.Context) {
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
		Name        string `json:"name" binding:"required"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	result, err := h.App.DB.Exec(
		`INSERT INTO favorite_folder (user_id, name, description) VALUES (?, ?, ?)`,
		uid, name, strings.TrimSpace(body.Description))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	id, _ := result.LastInsertId()
	c.JSON(http.StatusOK, gin.H{"id": id, "ok": true})
}

func (h *Handler) UpdateFavoriteFolder(c *gin.Context) {
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not available for API client credentials"})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	fid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || fid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid folder id"})
		return
	}
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	res, err := h.App.DB.Exec(`
		UPDATE favorite_folder SET name = ?, description = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND user_id = ?`, name, strings.TrimSpace(body.Description), fid, uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "folder not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) DeleteFavoriteFolder(c *gin.Context) {
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not available for API client credentials"})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	fid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || fid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid folder id"})
		return
	}
	res, err := h.App.DB.Exec(`DELETE FROM favorite_folder WHERE id = ? AND user_id = ?`, fid, uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "folder not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) AddFavoriteFolderItem(c *gin.Context) {
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not available for API client credentials"})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	fid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || fid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid folder id"})
		return
	}
	var ownerID int64
	if err := h.App.DB.QueryRow(`SELECT user_id FROM favorite_folder WHERE id = ?`, fid).Scan(&ownerID); err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "folder not found"})
		return
	} else if err != nil {
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
	if err := c.ShouldBindJSON(&body); err != nil || body.MediaID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "media_id is required"})
		return
	}
	if _, ok := h.requireMediaAccess(c, body.MediaID, false); !ok {
		return
	}
	var maxOrder sql.NullInt64
	_ = h.App.DB.QueryRow(`SELECT MAX(sort_order) FROM favorite_folder_item WHERE folder_id = ?`, fid).Scan(&maxOrder)
	nextOrder := int64(0)
	if maxOrder.Valid {
		nextOrder = maxOrder.Int64 + 1
	}
	_, err = h.App.DB.Exec(`
		INSERT INTO favorite_folder_item (folder_id, media_id, sort_order)
		VALUES (?, ?, ?)
		ON CONFLICT(folder_id, media_id) DO NOTHING`,
		fid, body.MediaID, nextOrder)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	_, _ = h.App.DB.Exec(`UPDATE favorite_folder SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, fid)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) RemoveFavoriteFolderItem(c *gin.Context) {
	if middleware.IsAPIClient(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not available for API client credentials"})
		return
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	fid, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || fid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid folder id"})
		return
	}
	itemID, err := strconv.ParseInt(c.Param("itemId"), 10, 64)
	if err != nil || itemID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid item id"})
		return
	}
	var ownerID int64
	if err := h.App.DB.QueryRow(`SELECT user_id FROM favorite_folder WHERE id = ?`, fid).Scan(&ownerID); err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "folder not found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if ownerID != uid {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		return
	}
	res, err := h.App.DB.Exec(`DELETE FROM favorite_folder_item WHERE id = ? AND folder_id = ?`, itemID, fid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "item not found"})
		return
	}
	_, _ = h.App.DB.Exec(`UPDATE favorite_folder SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, fid)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
