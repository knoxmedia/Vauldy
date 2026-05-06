package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
	"knox-media/internal/scraper"
)

type updateMediaAdminBody struct {
	Title         *string `json:"title"`
	OriginalTitle *string `json:"original_title"`
	Status        *string `json:"status"`
	Duration      *int64  `json:"duration"`
	Width         *int64  `json:"width"`
	Height        *int64  `json:"height"`
	Bitrate       *int64  `json:"bitrate"`
	Format        *string `json:"format"`
	MetaJSON      *string `json:"meta_json"`
}

func (h *Handler) ListMedia(c *gin.Context) {
	var profile userPermissionProfile
	if !middleware.IsAPIClient(c) {
		uid := middleware.UserID(c)
		if uid > 0 {
			p, err := h.loadUserPermissionProfile(uid)
			if err == nil {
				profile = p
			}
		}
	}
	lib := strings.TrimSpace(c.Query("library_id"))
	if lib != "" && strings.EqualFold(profile.LibraryScope, "selected") {
		if libID, perr := strconv.ParseInt(lib, 10, 64); perr == nil && libID > 0 {
			if _, ok := profile.AllowedLibraryIDs[libID]; !ok {
				c.JSON(http.StatusForbidden, gin.H{"error": "library access denied"})
				return
			}
		}
	}
	q := `SELECT 
		m.id, m.library_id, m.file_id, m.title, m.original_title, m.file_path, m.file_type, m.duration, m.width, m.height, m.bitrate, m.format, m.status, m.created_at,
		(SELECT MAX(pp.update_at) FROM play_progress pp WHERE pp.file_id = m.file_id) AS last_play_at,
		COALESCE(NULLIF(json_extract(m.meta_json, '$.scrape.release_date'), ''), NULLIF(json_extract(m.meta_json, '$.release_date'), '')) AS release_date,
		COALESCE(
			CAST(NULLIF(json_extract(m.meta_json, '$.scrape.year'), '') AS INTEGER),
			CAST(NULLIF(json_extract(m.meta_json, '$.year'), '') AS INTEGER),
			CAST(substr(COALESCE(NULLIF(json_extract(m.meta_json, '$.scrape.release_date'), ''), NULLIF(json_extract(m.meta_json, '$.release_date'), '')), 1, 4) AS INTEGER),
			0
		) AS release_year,
		COALESCE(
			NULLIF(TRIM(json_extract(m.meta_json, '$.scrape.poster')), ''),
			NULLIF(TRIM(json_extract(m.meta_json, '$.scrape.extra.poster')), '')
		) AS poster_url
	FROM media m WHERE 1=1`
	args := []any{}
	if lib != "" {
		q += ` AND library_id = ?`
		args = append(args, lib)
	}
	limit := 500
	if ls := c.Query("limit"); ls != "" {
		if n, err := strconv.Atoi(ls); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	switch c.DefaultQuery("sort", "id_desc") {
	case "created_desc":
		q += ` ORDER BY datetime(created_at) DESC`
	default:
		q += ` ORDER BY id DESC`
	}
	q += ` LIMIT ` + strconv.Itoa(limit)
	rows, err := h.App.DB.Query(q, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	var items []gin.H
	for rows.Next() {
		var mid int64
		var libID sql.NullInt64
		var fileID, title, orig, path, ftype, format, status, created, lastPlayAt, releaseDate, posterURL sql.NullString
		var dur, w, h, br, releaseYear sql.NullInt64
		if err := rows.Scan(&mid, &libID, &fileID, &title, &orig, &path, &ftype, &dur, &w, &h, &br, &format, &status, &created, &lastPlayAt, &releaseDate, &releaseYear, &posterURL); err != nil {
			continue
		}
		if strings.EqualFold(profile.LibraryScope, "selected") {
			if _, ok := profile.AllowedLibraryIDs[libID.Int64]; !ok {
				continue
			}
			if folders := profile.AllowedLibraryFolders[libID.Int64]; len(folders) > 0 && !pathMatchesAnyFolder(path.String, folders) {
				continue
			}
		}
		items = append(items, gin.H{
			"id": mid, "library_id": libID.Int64, "file_id": fileID.String,
			"title": title.String, "original_title": orig.String, "file_path": path.String,
			"file_type": ftype.String, "duration": dur.Int64, "width": w.Int64, "height": h.Int64,
			"bitrate": br.Int64, "format": format.String, "status": status.String, "created_at": created.String,
			"last_play_at": lastPlayAt.String, "release_date": releaseDate.String, "year": releaseYear.Int64,
			"poster_url": posterURL.String,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) GetMedia(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, false); !ok {
		return
	}
	row := h.App.DB.QueryRow(`
		SELECT id, library_id, file_id, title, original_title, file_path, file_type, duration, width, height, bitrate, md5, format, meta_json, status, created_at
		FROM media WHERE id = ?`, id)
	var libID sql.NullInt64
	var fileID, title, orig, path, ftype, md5, format, meta, status, created sql.NullString
	var dur, w, hei, br sql.NullInt64
	var mid int64
	if err := row.Scan(&mid, &libID, &fileID, &title, &orig, &path, &ftype, &dur, &w, &hei, &br, &md5, &format, &meta, &status, &created); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id": mid, "library_id": libID.Int64, "file_id": fileID.String,
		"title": title.String, "original_title": orig.String, "file_path": path.String,
		"file_type": ftype.String, "duration": dur.Int64, "width": w.Int64, "height": hei.Int64,
		"bitrate": br.Int64, "md5": md5.String, "format": format.String, "meta_json": meta.String,
		"status": status.String, "created_at": created.String,
	})
}

func (h *Handler) GetMediaMeta(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, false); !ok {
		return
	}
	var meta sql.NullString
	if err := h.App.DB.QueryRow(`SELECT meta_json FROM media WHERE id = ?`, id).Scan(&meta); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var raw any
	if meta.Valid && meta.String != "" {
		_ = json.Unmarshal([]byte(meta.String), &raw)
	}
	c.JSON(http.StatusOK, gin.H{"meta": raw})
}

func (h *Handler) GetMediaStats(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if _, ok := h.requireMediaAccess(c, id, false); !ok {
		return
	}

	var fileID sql.NullString
	var duration sql.NullInt64
	if err := h.App.DB.QueryRow(`SELECT file_id, duration FROM media WHERE id = ?`, id).Scan(&fileID, &duration); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var watchUsers int64
	var avgPosition sql.NullFloat64
	var latestAt sql.NullString
	if fileID.Valid && fileID.String != "" {
		_ = h.App.DB.QueryRow(
			`SELECT COUNT(DISTINCT user_id), AVG(position), MAX(update_at) FROM play_progress WHERE file_id = ?`,
			fileID.String,
		).Scan(&watchUsers, &avgPosition, &latestAt)
	}

	progressPercent := 0.0
	if duration.Int64 > 0 && avgPosition.Valid {
		progressPercent = (avgPosition.Float64 / float64(duration.Int64)) * 100
		if progressPercent < 0 {
			progressPercent = 0
		}
		if progressPercent > 100 {
			progressPercent = 100
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"watch_users":            watchUsers,
		"avg_position_seconds":   avgPosition.Float64,
		"avg_progress_percent":   progressPercent,
		"latest_watch_at":        latestAt.String,
		"media_duration_seconds": duration.Int64,
	})
}

func (h *Handler) ScrapeMedia(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var title, scraperName sql.NullString
	if err := h.App.DB.QueryRow(`SELECT m.title, l.scraper FROM media m JOIN library l ON m.library_id = l.id WHERE m.id = ?`, id).Scan(&title, &scraperName); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var existing sql.NullString
	_ = h.App.DB.QueryRow(`SELECT meta_json FROM media WHERE id = ?`, id).Scan(&existing)
	query := scraper.NormalizeTitle(title.String)
	if query == "" {
		query = title.String
	}
	res, err := scraper.ScrapeWithConfig(query, scraperName.String, h.readScrapeConfig())
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	patch := map[string]any{
		"scrape": res,
	}
	newMeta, err := scraper.MergeMetaJSON(existing.String, patch)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if _, err := h.App.DB.Exec(`UPDATE media SET meta_json = ? WHERE id = ?`, newMeta, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"scrape": res})
}

func (h *Handler) UpdateMediaAdmin(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var body updateMediaAdminBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	fields := make([]string, 0, 9)
	args := make([]any, 0, 10)
	if body.Title != nil {
		fields = append(fields, "title = ?")
		args = append(args, strings.TrimSpace(*body.Title))
	}
	if body.OriginalTitle != nil {
		fields = append(fields, "original_title = ?")
		args = append(args, strings.TrimSpace(*body.OriginalTitle))
	}
	if body.Status != nil {
		fields = append(fields, "status = ?")
		args = append(args, strings.TrimSpace(*body.Status))
	}
	if body.Duration != nil {
		if *body.Duration < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "duration must be >= 0"})
			return
		}
		fields = append(fields, "duration = ?")
		args = append(args, *body.Duration)
	}
	if body.Width != nil {
		if *body.Width < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "width must be >= 0"})
			return
		}
		fields = append(fields, "width = ?")
		args = append(args, *body.Width)
	}
	if body.Height != nil {
		if *body.Height < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "height must be >= 0"})
			return
		}
		fields = append(fields, "height = ?")
		args = append(args, *body.Height)
	}
	if body.Bitrate != nil {
		if *body.Bitrate < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bitrate must be >= 0"})
			return
		}
		fields = append(fields, "bitrate = ?")
		args = append(args, *body.Bitrate)
	}
	if body.Format != nil {
		fields = append(fields, "format = ?")
		args = append(args, strings.TrimSpace(*body.Format))
	}
	if body.MetaJSON != nil {
		raw := strings.TrimSpace(*body.MetaJSON)
		if raw != "" {
			var probe any
			if err := json.Unmarshal([]byte(raw), &probe); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "meta_json must be valid json"})
				return
			}
		}
		fields = append(fields, "meta_json = ?")
		args = append(args, raw)
	}
	if len(fields) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no updatable fields"})
		return
	}
	args = append(args, id)
	query := "UPDATE media SET " + strings.Join(fields, ", ") + " WHERE id = ?"
	res, err := h.App.DB.Exec(query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "updated": n})
}

// ToggleWatched marks a media item as watched or unwatched for the current user.
// PUT marks watched, DELETE marks unwatched.
func (h *Handler) ToggleWatched(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	userID := middleware.UserID(c)
	if userID <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	// Get the file_id for this media item.
	var fileID string
	if err := h.App.DB.QueryRow(`SELECT file_id FROM media WHERE id = ?`, id).Scan(&fileID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "media not found"})
		return
	}

	isPut := c.Request.Method == http.MethodPut

	if isPut {
		// Mark as watched: upsert play_progress with completed=1.
		_, err = h.App.DB.Exec(`
			INSERT INTO play_progress (user_id, file_id, play_end_at, completed)
			VALUES (?, ?, CURRENT_TIMESTAMP, 1)
			ON CONFLICT DO NOTHING`,
			userID, fileID,
		)
		if err == nil {
			// If no conflict, new row created. If conflict, try update.
		}
		_, _ = h.App.DB.Exec(`
			UPDATE play_progress SET completed = 1, play_end_at = CURRENT_TIMESTAMP, update_at = CURRENT_TIMESTAMP
			WHERE user_id = ? AND file_id = ?`,
			userID, fileID,
		)
		c.JSON(http.StatusOK, gin.H{"ok": true, "watched": true})
	} else {
		// Mark as unwatched.
		_, err = h.App.DB.Exec(`
			UPDATE play_progress SET completed = 0, play_end_at = NULL, update_at = CURRENT_TIMESTAMP
			WHERE user_id = ? AND file_id = ?`,
			userID, fileID,
		)
		c.JSON(http.StatusOK, gin.H{"ok": true, "watched": false})
	}
}
