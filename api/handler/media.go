package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
	"knox-media/internal/playcompletion"
	"knox-media/internal/scraper"
	"knox-media/internal/textencoding"
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
	h.listMediaObserved(c, nil, "")
}

func (h *Handler) listMediaObserved(c *gin.Context, afterBatch func(mediaListStats), publicationState string) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	var profile userPermissionProfile
	listUID := int64(0)
	if !middleware.IsAPIClient(c) {
		listUID = middleware.UserID(c)
		if listUID > 0 {
			var err error
			profile, err = h.loadUserPermissionProfileContext(ctx, listUID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		}
	}
	spec, err := parseMediaListSpec(c, profile, listUID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if middleware.IsAdmin(c) {
		spec.IncludeUnpublished = true
		spec.PublicationState = publicationState
	}
	if spec.LibraryID != nil && spec.RestrictLibraries {
		if _, ok := profile.AllowedLibraryIDs[*spec.LibraryID]; !ok {
			c.JSON(http.StatusForbidden, gin.H{"error": "library access denied"})
			return
		}
	}
	requestedLimit := spec.Limit
	if spec.IncludeUnpublished {
		spec.Limit = requestedLimit + 1
	}
	rows, _, err := h.listMediaRowsObserved(ctx, spec, afterBatch)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	hasMore := spec.IncludeUnpublished && len(rows) > requestedLimit
	if hasMore {
		rows = rows[:requestedLimit]
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		item := gin.H{
			"id": row.ID, "library_id": row.LibraryID.Int64, "file_id": row.FileID.String,
			"title": row.Title.String, "original_title": row.OriginalTitle.String, "file_path": row.FilePath.String,
			"file_type": row.FileType.String, "duration": row.Duration.Int64, "width": row.Width.Int64, "height": row.Height.Int64,
			"bitrate": row.Bitrate.Int64, "format": row.Format.String, "status": row.Status.String, "created_at": row.CreatedAt.String,
			"last_play_at": row.LastPlayAt.String, "completed": row.PlayCompleted.Int64, "release_date": row.ReleaseDate.String, "year": row.ReleaseYear.Int64,
			"poster_url": row.PosterURL.String, "backdrop_url": row.BackdropURL.String, "scraped": row.Scraped.Int64 == 1,
			"encrypted_asset": row.EncryptedAsset.Int64 == 1,
			"photo_taken_at":  row.PhotoTakenAt.String, "photo_tags": row.PhotoTags, "photo_tag_ids": row.PhotoTagIDs,
			"music_album_id": row.MusicAlbumID.Int64, "music_album_title": textencoding.FixMetadataString(row.MusicAlbumTitle.String),
			"music_artist":      textencoding.FixMetadataString(row.MusicArtist.String),
			"publication_state": row.PublicationState.String,
		}
		if spec.IncludeUnpublished {
			item["published_at"] = row.PublishedAt.String
			item["publication_error"] = row.PublicationError.String
			item["ingest_generation"] = row.IngestGeneration.Int64
		}
		items = append(items, item)
	}
	response := gin.H{"items": items}
	if spec.IncludeUnpublished {
		response["has_more"] = hasMore
		if hasMore && len(rows) > 0 {
			response["next_cursor"] = strconv.FormatInt(rows[len(rows)-1].ID, 10)
		}
	}
	c.JSON(http.StatusOK, response)
}
func photoTagIDMatches(filterID string, tags, tagIDs []string) bool {
	if filterID == "" || filterID == "all" {
		return true
	}
	for _, id := range tagIDs {
		if id == filterID {
			return true
		}
	}
	if strings.HasPrefix(filterID, "custom:") {
		name := strings.TrimPrefix(filterID, "custom:")
		for _, tag := range tags {
			if tag == name {
				return true
			}
		}
	}
	return false
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
	var libraryID int64
	if err := h.App.DB.QueryRow(
		`SELECT m.title, l.scraper, m.library_id FROM media m JOIN library l ON m.library_id = l.id WHERE m.id = ?`,
		id,
	).Scan(&title, &scraperName, &libraryID); err != nil {
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
	cfg := h.readLibraryScrapeConfig(libraryID)
	res, err := scraper.ScrapeWithConfig(query, scraperName.String, cfg)
	if res == nil {
		res = &scraper.ScrapeResult{Title: query, Genres: []string{}, Extra: map[string]any{}}
	}
	var fileType string
	_ = h.App.DB.QueryRow(`SELECT COALESCE(file_type,'') FROM media WHERE id = ?`, id).Scan(&fileType)
	h.applyScrapeLocalImages(id, libraryID, fileType, cfg, res)
	if !scraper.HasMeaningfulScrapeData(res) {
		msg := scraper.NoDataFailureMessage(res)
		if err != nil {
			msg = scraper.FormatScrapeErrorMessage(err)
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}
	scraper.PreserveScrapeImagesFromExisting(res, existing.String)
	if _, pErr := h.persistScrapeArtwork(id, res); pErr != nil {
		log.Printf("scrape media artwork persist id=%d: %v", id, pErr)
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
	h.scheduleLibraryPreviewRefresh(libraryID)
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
	progress := playcompletion.Store{DB: h.App.DB, Now: h.PlayCompletionNow}
	watched := c.Request.Method == http.MethodPut
	if watched {
		err = progress.MarkWatched(c.Request.Context(), userID, id)
	} else {
		err = progress.MarkUnwatched(c.Request.Context(), userID, id)
	}
	if err != nil {
		writePlaybackStoreError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "watched": watched})
}
