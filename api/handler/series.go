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
	"knox-media/internal/tvparse"
	"knox-media/internal/tvstore"
)

// ListLibrarySeries returns TV series grouped for a library (剧 → 季 → 集 hierarchy entry point).
func (h *Handler) ListLibrarySeries(c *gin.Context) {
	libID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || libID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid library id"})
		return
	}
	if !h.requireLibraryAccess(c, libID) {
		return
	}
	items, err := h.queryLibrarySeriesItems(libID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var unlinked int
	_ = h.App.DB.QueryRow(`
		SELECT COUNT(1) FROM media m
		LEFT JOIN episode_media em ON em.media_id = m.id
		WHERE m.library_id = ? AND m.file_type = 'video' AND m.status = 'active' AND em.id IS NULL
	`, libID).Scan(&unlinked)
	if len(items) == 0 || unlinked > 0 {
		var mediaCount int
		_ = h.App.DB.QueryRow(`
			SELECT COUNT(1) FROM media WHERE library_id = ? AND file_type = 'video' AND status = 'active'
		`, libID).Scan(&mediaCount)
		if mediaCount > 0 {
			_, _ = tvstore.BackfillLibraryTV(h.App.DB, libID)
			items, err = h.queryLibrarySeriesItems(libID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) queryLibrarySeriesItems(libID int64) ([]gin.H, error) {
	rows, err := h.App.DB.Query(`
		SELECT s.id, s.title, s.title_norm, COALESCE(s.year, 0), COALESCE(s.tmdb_id, ''), COALESCE(s.tvdb_id, ''),
			COALESCE(
				NULLIF(TRIM(s.poster), ''),
				NULLIF(TRIM(json_extract(s.meta_json, '$.scrape.poster')), ''),
				(SELECT NULLIF(TRIM(json_extract(m.meta_json, '$.scrape.extra.series_poster')), '')
				 FROM episode_media em
				 JOIN episode ep ON ep.id = em.episode_id
				 JOIN season se2 ON se2.id = ep.season_id
				 JOIN media m ON m.id = em.media_id
				 WHERE se2.tv_id = s.id
				 ORDER BY em.sort_order ASC, m.id ASC LIMIT 1),
				(SELECT NULLIF(TRIM(json_extract(m.meta_json, '$.scrape.poster')), '')
				 FROM episode_media em
				 JOIN episode ep ON ep.id = em.episode_id
				 JOIN season se2 ON se2.id = ep.season_id
				 JOIN media m ON m.id = em.media_id
				 WHERE se2.tv_id = s.id
				 ORDER BY em.sort_order ASC, m.id ASC LIMIT 1)
			) AS poster_url,
			COALESCE(s.folder_paths, '[]'), s.created_at, s.updated_at,
			(SELECT COUNT(DISTINCT se.id) FROM season se WHERE se.tv_id = s.id) AS season_count,
			(SELECT COUNT(DISTINCT em.media_id)
			 FROM season se
			 JOIN episode ep ON ep.season_id = se.id
			 JOIN episode_media em ON em.episode_id = ep.id
			 WHERE se.tv_id = s.id) AS episode_count
		FROM series s
		WHERE s.library_id = ?
		ORDER BY s.title COLLATE NOCASE ASC
	`, libID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []gin.H
	for rows.Next() {
		var id, year, seasonCount, episodeCount int64
		var title, titleNorm, tmdbID, tvdbID, posterURL, foldersRaw, created, updated sql.NullString
		if err := rows.Scan(&id, &title, &titleNorm, &year, &tmdbID, &tvdbID, &posterURL, &foldersRaw, &created, &updated, &seasonCount, &episodeCount); err != nil {
			continue
		}
		var folders []string
		if foldersRaw.Valid {
			_ = json.Unmarshal([]byte(foldersRaw.String), &folders)
		}
		items = append(items, gin.H{
			"id": id, "library_id": libID, "title": title.String, "title_norm": titleNorm.String,
			"year": year, "tmdb_id": tmdbID.String, "tvdb_id": tvdbID.String,
			"poster": posterURL.String, "poster_url": posterURL.String, "folder_paths": folders,
			"season_count": seasonCount, "episode_count": episodeCount,
			"created_at": created.String, "updated_at": updated.String,
		})
	}
	return items, nil
}

// GetSeries returns series detail with season summaries.
func (h *Handler) GetSeries(c *gin.Context) {
	seriesID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || seriesID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid series id"})
		return
	}
	row := h.App.DB.QueryRow(`
		SELECT s.id, s.library_id, s.title, s.title_norm, COALESCE(s.year, 0),
			COALESCE(s.tmdb_id, ''), COALESCE(s.tvdb_id, ''),
			COALESCE(
				NULLIF(TRIM(s.poster), ''),
				NULLIF(TRIM(json_extract(s.meta_json, '$.scrape.poster')), ''),
				(SELECT NULLIF(TRIM(json_extract(m.meta_json, '$.scrape.extra.series_poster')), '')
				 FROM episode_media em
				 JOIN episode ep ON ep.id = em.episode_id
				 JOIN season se2 ON se2.id = ep.season_id
				 JOIN media m ON m.id = em.media_id
				 WHERE se2.tv_id = s.id
				 ORDER BY em.sort_order ASC, m.id ASC LIMIT 1),
				(SELECT NULLIF(TRIM(json_extract(m.meta_json, '$.scrape.poster')), '')
				 FROM episode_media em
				 JOIN episode ep ON ep.id = em.episode_id
				 JOIN season se2 ON se2.id = ep.season_id
				 JOIN media m ON m.id = em.media_id
				 WHERE se2.tv_id = s.id
				 ORDER BY em.sort_order ASC, m.id ASC LIMIT 1)
			) AS poster_url,
			COALESCE(s.folder_paths, '[]'), COALESCE(s.meta_json, ''), s.created_at, s.updated_at
		FROM series s WHERE s.id = ?`, seriesID)
	var id, libID, year int64
	var title, titleNorm, tmdbID, tvdbID, posterURL, foldersRaw, metaJSON, created, updated sql.NullString
	if err := row.Scan(&id, &libID, &title, &titleNorm, &year, &tmdbID, &tvdbID, &posterURL, &foldersRaw, &metaJSON, &created, &updated); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !h.requireLibraryAccess(c, libID) {
		return
	}
	var folders []string
	if foldersRaw.Valid {
		_ = json.Unmarshal([]byte(foldersRaw.String), &folders)
	}
	seasons, _ := h.listSeasonSummaries(seriesID)
	c.JSON(http.StatusOK, gin.H{
		"id": id, "library_id": libID, "title": title.String, "title_norm": titleNorm.String,
		"year": year, "tmdb_id": tmdbID.String, "tvdb_id": tvdbID.String,
		"poster": posterURL.String, "poster_url": posterURL.String,
		"folder_paths": folders, "meta_json": metaJSON.String,
		"seasons": seasons, "created_at": created.String, "updated_at": updated.String,
	})
}

func (h *Handler) listSeasonSummaries(seriesID int64) ([]gin.H, error) {
	rows, err := h.App.DB.Query(`
		SELECT se.id, se.season_num, COALESCE(se.name, ''), COALESCE(se.poster, ''),
			(SELECT COUNT(DISTINCT ep.id) FROM episode ep WHERE ep.season_id = se.id) AS episode_count
		FROM season se
		WHERE se.tv_id = ?
		ORDER BY se.season_num ASC
	`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []gin.H
	for rows.Next() {
		var id, seasonNum, epCount int64
		var name, poster sql.NullString
		if err := rows.Scan(&id, &seasonNum, &name, &poster, &epCount); err != nil {
			continue
		}
		items = append(items, gin.H{
			"id": id, "season_num": seasonNum, "name": name.String,
			"poster": poster.String, "episode_count": epCount,
		})
	}
	return items, nil
}

// ListSeasonEpisodes returns episodes for a season with linked media versions.
func (h *Handler) ListSeasonEpisodes(c *gin.Context) {
	seasonID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || seasonID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid season id"})
		return
	}
	var libID int64
	if err := h.App.DB.QueryRow(`
		SELECT sr.library_id FROM season se JOIN series sr ON sr.id = se.tv_id WHERE se.id = ?
	`, seasonID).Scan(&libID); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !h.requireLibraryAccess(c, libID) {
		return
	}
	uid := middleware.UserID(c)
	rows, err := h.App.DB.Query(`
		SELECT ep.id, ep.episode_num, COALESCE(ep.title, ''), COALESCE(ep.duration, 0)
		FROM episode ep
		WHERE ep.season_id = ?
		ORDER BY ep.episode_num ASC
	`, seasonID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	var items []gin.H
	for rows.Next() {
		var epID, epNum, dur int64
		var epTitle sql.NullString
		if err := rows.Scan(&epID, &epNum, &epTitle, &dur); err != nil {
			continue
		}
		versions, _ := h.listEpisodeMediaVersions(epID, uid)
		items = append(items, gin.H{
			"id": epID, "episode_num": epNum, "title": epTitle.String,
			"duration": dur, "versions": versions,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) listEpisodeMediaVersions(episodeID int64, userID int64) ([]gin.H, error) {
	rows, err := h.App.DB.Query(`
		SELECT m.id, m.file_id, m.title, m.file_path, m.duration, m.width, m.height, m.bitrate, m.format,
			em.sort_order,
			COALESCE(NULLIF(TRIM(json_extract(m.meta_json, '$.scrape.poster')), ''), '') AS poster_url,
			COALESCE((SELECT pp.completed FROM play_progress pp WHERE pp.file_id = m.file_id AND pp.user_id = ?), 0) AS play_completed
		FROM episode_media em
		JOIN media m ON m.id = em.media_id
		WHERE em.episode_id = ?
		ORDER BY em.sort_order ASC, m.id ASC
	`, userID, episodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []gin.H
	for rows.Next() {
		var mid, dur, w, h, br, sortOrder, playCompleted int64
		var fileID, title, path, format, poster sql.NullString
		if err := rows.Scan(&mid, &fileID, &title, &path, &dur, &w, &h, &br, &format, &sortOrder, &poster, &playCompleted); err != nil {
			continue
		}
		items = append(items, gin.H{
			"media_id": mid, "file_id": fileID.String, "title": title.String,
			"file_path": path.String, "duration": dur, "width": w, "height": h,
			"bitrate": br, "format": format.String, "sort_order": sortOrder,
			"poster_url": poster.String, "completed": playCompleted,
		})
	}
	return items, nil
}

type updateSeriesBody struct {
	Title    string `json:"title"`
	Year     *int   `json:"year"`
	Poster   string `json:"poster"`
	Overview string `json:"overview"`
}

// GetSeriesPlayTarget returns the media id and resume position for series playback.
func (h *Handler) GetSeriesPlayTarget(c *gin.Context) {
	seriesID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || seriesID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid series id"})
		return
	}
	var libID int64
	if err := h.App.DB.QueryRow(`SELECT library_id FROM series WHERE id = ?`, seriesID).Scan(&libID); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !h.requireLibraryAccess(c, libID) {
		return
	}
	uid := middleware.UserID(c)
	var mediaID, duration, position, completed int64
	err = h.App.DB.QueryRow(`
		SELECT m.id, COALESCE(m.duration, 0), COALESCE(p.position, 0), COALESCE(p.completed, 0)
		FROM play_progress p
		JOIN media m ON m.file_id = p.file_id
		JOIN episode_media em ON em.media_id = m.id
		JOIN episode ep ON ep.id = em.episode_id
		JOIN season se ON se.id = ep.season_id
		WHERE p.user_id = ? AND se.tv_id = ?
		ORDER BY p.update_at DESC
		LIMIT 1
	`, uid, seriesID).Scan(&mediaID, &duration, &position, &completed)
	if err == nil && mediaID > 0 {
		resumePos := position
		if completed == 1 || duration <= 0 || position <= 0 || position >= duration-8 {
			resumePos = 0
		}
		c.JSON(http.StatusOK, gin.H{"media_id": mediaID, "position": resumePos})
		return
	}
	if err := h.App.DB.QueryRow(`
		SELECT m.id
		FROM episode_media em
		JOIN episode ep ON ep.id = em.episode_id
		JOIN season se ON se.id = ep.season_id
		JOIN media m ON m.id = em.media_id
		WHERE se.tv_id = ?
		ORDER BY se.season_num ASC, ep.episode_num ASC, em.sort_order ASC, m.id ASC
		LIMIT 1
	`, seriesID).Scan(&mediaID); err != nil || mediaID <= 0 {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "no playable episode"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"media_id": mediaID, "position": 0})
}

// UpdateSeries updates editable series metadata (title, year, poster, overview).
func (h *Handler) UpdateSeries(c *gin.Context) {
	seriesID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || seriesID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid series id"})
		return
	}
	var body updateSeriesBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var libID int64
	var existingTitle, existingMeta sql.NullString
	var existingYear sql.NullInt64
	if err := h.App.DB.QueryRow(`
		SELECT library_id, title, COALESCE(year, 0), COALESCE(meta_json, '')
		FROM series WHERE id = ?
	`, seriesID).Scan(&libID, &existingTitle, &existingYear, &existingMeta); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !h.requireLibraryAccess(c, libID) {
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		title = strings.TrimSpace(existingTitle.String)
	}
	titleNorm := tvparse.NormalizeSeriesTitle(title)
	year := existingYear.Int64
	if body.Year != nil {
		year = int64(*body.Year)
	}
	poster := strings.TrimSpace(body.Poster)
	var raw map[string]any
	_ = json.Unmarshal([]byte(existingMeta.String), &raw)
	if raw == nil {
		raw = map[string]any{}
	}
	scrape, _ := raw["scrape"].(map[string]any)
	if scrape == nil {
		scrape = map[string]any{}
	}
	if overview := strings.TrimSpace(body.Overview); overview != "" {
		scrape["overview"] = overview
	}
	if title != "" {
		scrape["title"] = title
	}
	if poster != "" {
		scrape["poster"] = poster
	}
	raw["scrape"] = scrape
	metaJSON, _ := json.Marshal(raw)
	_, err = h.App.DB.Exec(`
		UPDATE series SET
			title = ?,
			title_norm = ?,
			year = ?,
			poster = CASE WHEN ? != '' THEN ? ELSE poster END,
			meta_json = ?,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, title, titleNorm, year, poster, poster, string(metaJSON), seriesID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok": true,
		"id": seriesID,
		"title": title,
		"year": year,
		"poster": poster,
		"overview": strings.TrimSpace(body.Overview),
	})
}

// ListSeriesImageCandidates returns poster/backdrop/logo candidates for a TV series,
// querying ONLY the image sources configured on the series' owning library.
//
// Query: kind=poster|backdrop|logo (default poster)
func (h *Handler) ListSeriesImageCandidates(c *gin.Context) {
	seriesID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || seriesID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid series id"})
		return
	}
	kind := strings.ToLower(strings.TrimSpace(c.DefaultQuery("kind", "poster")))

	var libraryID int64
	var title string
	var year int
	var tmdbID string
	if err := h.App.DB.QueryRow(
		`SELECT library_id, COALESCE(title, ''), COALESCE(year, 0), COALESCE(tmdb_id, '') FROM series WHERE id = ?`,
		seriesID,
	).Scan(&libraryID, &title, &year, &tmdbID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "series not found"})
		return
	}
	if !h.requireLibraryAccess(c, libraryID) {
		return
	}

	keyword := strings.TrimSpace(title)
	if keyword == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "series has no title to search"})
		return
	}

	cfg := h.readLibraryScrapeConfig(libraryID)
	candidates, errs, scraped := scraper.FetchImageCandidates(cfg, keyword, year, kind, strings.TrimSpace(tmdbID))
	if candidates == nil {
		candidates = []scraper.ImageCandidate{}
	}
	resp := gin.H{"candidates": candidates, "scraped": scraped}
	if len(errs) > 0 {
		resp["errors"] = errs
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) requireLibraryAccess(c *gin.Context, libraryID int64) bool {
	if h == nil || h.App == nil || libraryID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid library"})
		return false
	}
	uid := middleware.UserID(c)
	if uid <= 0 {
		return true
	}
	profile, err := h.loadUserPermissionProfile(uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return false
	}
	if strings.EqualFold(profile.LibraryScope, "selected") {
		if _, ok := profile.AllowedLibraryIDs[libraryID]; !ok {
			c.JSON(http.StatusForbidden, gin.H{"error": "library access denied"})
			return false
		}
	}
	return true
}
