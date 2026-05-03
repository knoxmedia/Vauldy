package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
	"knox-media/internal/scraper"
)

type scrapeTaskCreateBody struct {
	MediaIDs []int64 `json:"media_ids"`
	Source   string  `json:"source"`
}

type scrapeRunBody struct {
	IDs   []int64 `json:"ids"`
	Limit int     `json:"limit"`
}

var episodePattern = regexp.MustCompile(`(?i)\bS\d{1,2}E\d{1,3}\b`)

type scrapeConfigBody struct {
	Enabled      *int              `json:"enabled"`
	Providers    []string          `json:"providers"`
	ImageSources []string          `json:"image_sources"`
	APIKeys      map[string]string `json:"api_keys"`
}

type manualMatchBody struct {
	Query  string `json:"query" binding:"required"`
	Year   int    `json:"year"`
	Source string `json:"source"`
}

type updateMetaBody struct {
	Title    string   `json:"title"`
	Overview string   `json:"overview"`
	Rating   float64  `json:"rating"`
	Genres   []string `json:"genres"`
}

type updateImageBody struct {
	Poster   string `json:"poster"`
	Backdrop string `json:"backdrop"`
	Logo     string `json:"logo"`
}

func (h *Handler) enqueueScrapeTask(mediaID int64, createdBy int64, source string) {
	if mediaID <= 0 {
		return
	}
	if source == "" {
		source = "auto"
	}
	var exists int
	_ = h.App.DB.QueryRow(
		`SELECT COUNT(1) FROM scrape_task WHERE media_id = ? AND status IN ('waiting','running')`,
		mediaID,
	).Scan(&exists)
	if exists > 0 {
		return
	}
	_, _ = h.App.DB.Exec(
		`INSERT INTO scrape_task (media_id, source, status, progress, created_by) VALUES (?, ?, 'waiting', 0, ?)`,
		mediaID, source, createdBy,
	)
}

func (h *Handler) GetScrapeConfig(c *gin.Context) {
	var enabled int
	var providers, keys, imageSources string
	if err := h.App.DB.QueryRow(
		`SELECT enabled, providers, api_keys_json, image_sources FROM scrape_config WHERE id = 1`,
	).Scan(&enabled, &providers, &keys, &imageSources); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var keyMap map[string]string
	_ = json.Unmarshal([]byte(keys), &keyMap)
	c.JSON(http.StatusOK, gin.H{
		"enabled":       enabled,
		"providers":     splitCSV(providers),
		"image_sources": splitCSV(imageSources),
		"api_keys":      keyMap,
	})
}

func (h *Handler) SaveScrapeConfig(c *gin.Context) {
	var body scrapeConfigBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var enabled int
	if body.Enabled != nil {
		enabled = *body.Enabled
	} else {
		enabled = 1
	}
	providers := strings.Join(body.Providers, ",")
	if providers == "" {
		providers = "tmdb,omdb,douban,tvdb,bangumi,fanart,ai"
	}
	imageSources := strings.Join(body.ImageSources, ",")
	if imageSources == "" {
		imageSources = "tmdb,omdb,screen_grabber,embedded"
	}
	b, _ := json.Marshal(body.APIKeys)
	_, err := h.App.DB.Exec(
		`UPDATE scrape_config SET enabled = ?, providers = ?, image_sources = ?, api_keys_json = ?, updated_at = CURRENT_TIMESTAMP WHERE id = 1`,
		enabled, providers, imageSources, string(b),
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) CreateScrapeTasks(c *gin.Context) {
	var body scrapeTaskCreateBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(body.MediaIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "media_ids required"})
		return
	}
	uid := middleware.UserID(c)
	created := 0
	for _, mid := range body.MediaIDs {
		before := created
		h.enqueueScrapeTask(mid, uid, body.Source)
		var n int
		_ = h.App.DB.QueryRow(`SELECT COUNT(1) FROM scrape_task WHERE media_id = ?`, mid).Scan(&n)
		if n > 0 {
			created = before + 1
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "created": created})
}

func (h *Handler) ListScrapeTasks(c *gin.Context) {
	limit := 100
	if ls := c.Query("limit"); ls != "" {
		if n, err := strconv.Atoi(ls); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	rows, err := h.App.DB.Query(`
		SELECT t.id, t.media_id, m.title, t.task_type, t.source, t.query, t.year, t.status, t.progress, t.message, t.created_at, t.started_at, t.finished_at
		FROM scrape_task t
		LEFT JOIN media m ON m.id = t.media_id
		ORDER BY t.id DESC LIMIT ?`, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	var items []gin.H
	for rows.Next() {
		var id, mediaID, year, progress sql.NullInt64
		var title, taskType, source, query, status, message, createdAt, startedAt, finishedAt sql.NullString
		if err := rows.Scan(&id, &mediaID, &title, &taskType, &source, &query, &year, &status, &progress, &message, &createdAt, &startedAt, &finishedAt); err != nil {
			continue
		}
		items = append(items, gin.H{
			"id": id.Int64, "media_id": mediaID.Int64, "title": title.String, "task_type": taskType.String, "source": source.String,
			"query": query.String, "year": year.Int64, "status": status.String, "progress": progress.Int64, "message": message.String,
			"created_at": createdAt.String, "started_at": startedAt.String, "finished_at": finishedAt.String,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) RunScrapeTasks(c *gin.Context) {
	var body scrapeRunBody
	_ = c.ShouldBindJSON(&body)
	limit := body.Limit
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	done, failed := h.runScrapeTasksWithLimit(body.IDs, limit)
	c.JSON(http.StatusOK, gin.H{"ok": true, "done": done, "failed": failed})
}

func (h *Handler) runScrapeTasksWithLimit(ids []int64, limit int) (int, int) {
	var taskIDs []int64
	if len(ids) > 0 {
		taskIDs = ids
	} else {
		rows, err := h.App.DB.Query(`SELECT id FROM scrape_task WHERE status IN ('waiting','failed') ORDER BY id LIMIT ?`, limit)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var id int64
				if rows.Scan(&id) == nil {
					taskIDs = append(taskIDs, id)
				}
			}
		}
	}
	done := 0
	failed := 0
	cfg := h.readScrapeConfig()
	for _, taskID := range taskIDs {
		var mediaID int64
		var libraryID int64
		var source, query, title, existingMeta, filePath, fileType string
		var year sql.NullInt64
		err := h.App.DB.QueryRow(`
			SELECT t.media_id, t.source, COALESCE(t.query,''), t.year, COALESCE(m.title,''), COALESCE(m.meta_json,''), m.library_id, COALESCE(m.file_path,''), COALESCE(m.file_type,'')
			FROM scrape_task t JOIN media m ON m.id = t.media_id WHERE t.id = ?`, taskID,
		).Scan(&mediaID, &source, &query, &year, &title, &existingMeta, &libraryID, &filePath, &fileType)
		if err != nil {
			continue
		}
		if query == "" {
			query = title
		}
		if year.Valid && year.Int64 > 0 {
			query = strings.TrimSpace(query + " " + strconv.FormatInt(year.Int64, 10))
		}
		_, _ = h.App.DB.Exec(`UPDATE scrape_task SET status='running', progress=15, started_at=CURRENT_TIMESTAMP, message='scraping...' WHERE id = ?`, taskID)
		res, sErr := scraper.ScrapeWithConfig(query, source, cfg)
		if sErr != nil {
			fmsg := formatTaskErrorMessage(sErr)
			_, _ = h.App.DB.Exec(`UPDATE scrape_task SET status='failed', progress=100, finished_at=CURRENT_TIMESTAMP, message=? WHERE id = ?`, fmsg, taskID)
			_, _ = h.App.DB.Exec(`INSERT INTO scrape_history (task_id, media_id, source, query, status, message) VALUES (?, ?, ?, ?, 'failed', ?)`, taskID, mediaID, source, query, fmsg)
			failed++
			continue
		}
		patch := map[string]any{"scrape": res}
		merged, mErr := scraper.MergeMetaJSON(existingMeta, patch)
		if mErr != nil {
			_, _ = h.App.DB.Exec(`UPDATE scrape_task SET status='failed', progress=100, finished_at=CURRENT_TIMESTAMP, message=? WHERE id = ?`, mErr.Error(), taskID)
			failed++
			continue
		}
		_, _ = h.App.DB.Exec(`UPDATE media SET title = ?, meta_json = ? WHERE id = ?`, res.Title, merged, mediaID)
		if strings.EqualFold(fileType, "video") {
			h.syncSeriesCollectionMeta(libraryID, filePath, res)
		}
		js, _ := json.Marshal(res)
		okMsg := summarizeProviderWarnings(res)
		_, _ = h.App.DB.Exec(`UPDATE scrape_task SET status='done', progress=100, finished_at=CURRENT_TIMESTAMP, message=? WHERE id = ?`, okMsg, taskID)
		_, _ = h.App.DB.Exec(`INSERT INTO scrape_history (task_id, media_id, source, query, status, message, result_json) VALUES (?, ?, ?, ?, 'done', ?, ?)`, taskID, mediaID, source, query, okMsg, string(js))
		done++
	}
	return done, failed
}

func (h *Handler) ListScrapeHistory(c *gin.Context) {
	limit := 100
	if ls := c.Query("limit"); ls != "" {
		if n, err := strconv.Atoi(ls); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	rows, err := h.App.DB.Query(`SELECT id, task_id, media_id, source, query, status, message, created_at FROM scrape_history ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	var items []gin.H
	for rows.Next() {
		var id, taskID, mediaID sql.NullInt64
		var source, query, status, message, createdAt sql.NullString
		if err := rows.Scan(&id, &taskID, &mediaID, &source, &query, &status, &message, &createdAt); err != nil {
			continue
		}
		items = append(items, gin.H{
			"id": id.Int64, "task_id": taskID.Int64, "media_id": mediaID.Int64, "source": source.String,
			"query": query.String, "status": status.String, "message": message.String, "created_at": createdAt.String,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) ManualMatchMedia(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var body manualMatchBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	res, sErr := scraper.ScrapeWithConfig(body.Query, body.Source, h.readScrapeConfig())
	if sErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": sErr.Error()})
		return
	}
	var existing sql.NullString
	_ = h.App.DB.QueryRow(`SELECT meta_json FROM media WHERE id = ?`, id).Scan(&existing)
	merged, _ := scraper.MergeMetaJSON(existing.String, map[string]any{"scrape": res})
	_, _ = h.App.DB.Exec(`UPDATE media SET title = ?, meta_json = ? WHERE id = ?`, res.Title, merged, id)
	c.JSON(http.StatusOK, gin.H{"ok": true, "scrape": res})
}

func (h *Handler) readScrapeConfig() scraper.Config {
	var providers, keysJSON, imageSources string
	if err := h.App.DB.QueryRow(`SELECT providers, api_keys_json, image_sources FROM scrape_config WHERE id = 1`).Scan(&providers, &keysJSON, &imageSources); err != nil {
		return scraper.Config{
			Providers:    []string{"tmdb", "omdb", "bangumi"},
			ImageSources: []string{"tmdb", "omdb", "screen_grabber", "embedded"},
			APIKeys:      map[string]string{},
		}
	}
	keys := map[string]string{}
	_ = json.Unmarshal([]byte(keysJSON), &keys)
	return scraper.Config{
		Providers:    splitCSV(providers),
		ImageSources: splitCSV(imageSources),
		APIKeys:      keys,
	}
}

func seriesCollectionKey(filePath string) string {
	base := strings.TrimSpace(strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath)))
	base = scraper.NormalizeTitle(base)
	base = episodePattern.ReplaceAllString(base, "")
	base = strings.Join(strings.Fields(base), " ")
	return strings.TrimSpace(base)
}

func (h *Handler) syncSeriesCollectionMeta(libraryID int64, filePath string, res *scraper.ScrapeResult) {
	if res == nil {
		return
	}
	key := seriesCollectionKey(filePath)
	if key == "" {
		return
	}
	rows, err := h.App.DB.Query(`SELECT id, COALESCE(meta_json,'') FROM media WHERE library_id = ? AND lower(file_path) LIKE ?`, libraryID, "%"+strings.ToLower(key)+"%")
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var mid int64
		var existing string
		if rows.Scan(&mid, &existing) != nil {
			continue
		}
		merged, mErr := scraper.MergeMetaJSON(existing, map[string]any{"scrape": res, "series_collection": key})
		if mErr != nil {
			continue
		}
		_, _ = h.App.DB.Exec(`UPDATE media SET meta_json = ? WHERE id = ?`, merged, mid)
	}
}

func (h *Handler) UpdateMediaMetadata(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var body updateMetaBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var existing sql.NullString
	_ = h.App.DB.QueryRow(`SELECT meta_json FROM media WHERE id = ?`, id).Scan(&existing)
	var raw map[string]any
	_ = json.Unmarshal([]byte(existing.String), &raw)
	if raw == nil {
		raw = map[string]any{}
	}
	sv, _ := raw["scrape"].(map[string]any)
	if sv == nil {
		sv = map[string]any{}
	}
	if body.Overview != "" {
		sv["overview"] = body.Overview
	}
	if body.Rating > 0 {
		sv["rating"] = body.Rating
	}
	if len(body.Genres) > 0 {
		sv["genres"] = body.Genres
	}
	raw["scrape"] = sv
	js, _ := json.Marshal(raw)
	title := body.Title
	if title == "" {
		_, _ = h.App.DB.Exec(`UPDATE media SET meta_json = ? WHERE id = ?`, string(js), id)
	} else {
		_, _ = h.App.DB.Exec(`UPDATE media SET title = ?, meta_json = ? WHERE id = ?`, title, string(js), id)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) UpdateMediaImages(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var body updateImageBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var existing sql.NullString
	_ = h.App.DB.QueryRow(`SELECT meta_json FROM media WHERE id = ?`, id).Scan(&existing)
	var raw map[string]any
	_ = json.Unmarshal([]byte(existing.String), &raw)
	if raw == nil {
		raw = map[string]any{}
	}
	sv, _ := raw["scrape"].(map[string]any)
	if sv == nil {
		sv = map[string]any{}
	}
	extra, _ := sv["extra"].(map[string]any)
	if extra == nil {
		extra = map[string]any{}
	}
	if body.Poster != "" {
		extra["poster"] = body.Poster
	}
	if body.Backdrop != "" {
		extra["backdrop"] = body.Backdrop
	}
	if body.Logo != "" {
		extra["logo"] = body.Logo
	}
	sv["extra"] = extra
	raw["scrape"] = sv
	js, _ := json.Marshal(raw)
	_, _ = h.App.DB.Exec(`UPDATE media SET meta_json = ? WHERE id = ?`, string(js), id)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) SearchTMDbImages(c *gin.Context) {
	query := strings.TrimSpace(c.Query("query"))
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query required"})
		return
	}
	year := strings.TrimSpace(c.Query("year"))
	cfg := h.readScrapeConfig()
	key := strings.TrimSpace(cfg.APIKeys["tmdb"])
	if key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "TMDb API key not configured"})
		return
	}
	u := "https://api.themoviedb.org/3/search/multi?api_key=" + url.QueryEscape(key) +
		"&query=" + url.QueryEscape(query) + "&language=zh-CN&page=1&include_adult=false"
	if year != "" {
		u += "&year=" + url.QueryEscape(year)
	}
	searchBody, err := simpleGet(u, map[string]string{"Accept": "application/json"})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	var search struct {
		Results []struct {
			ID int64 `json:"id"`
		} `json:"results"`
	}
	if err := json.Unmarshal(searchBody, &search); err != nil || len(search.Results) == 0 {
		c.JSON(http.StatusOK, gin.H{"items": []any{}})
		return
	}
	id := search.Results[0].ID
	imgURL := fmt.Sprintf("https://api.themoviedb.org/3/movie/%d/images?api_key=%s", id, url.QueryEscape(key))
	imgBody, err := simpleGet(imgURL, map[string]string{"Accept": "application/json"})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	var imgs struct {
		Posters []struct {
			FilePath string `json:"file_path"`
		} `json:"posters"`
		Backdrops []struct {
			FilePath string `json:"file_path"`
		} `json:"backdrops"`
		Logos []struct {
			FilePath string `json:"file_path"`
		} `json:"logos"`
	}
	_ = json.Unmarshal(imgBody, &imgs)
	base := "https://image.tmdb.org/t/p/original"
	posters := make([]string, 0, len(imgs.Posters))
	for _, p := range imgs.Posters {
		if p.FilePath != "" {
			posters = append(posters, base+p.FilePath)
		}
	}
	backdrops := make([]string, 0, len(imgs.Backdrops))
	for _, p := range imgs.Backdrops {
		if p.FilePath != "" {
			backdrops = append(backdrops, base+p.FilePath)
		}
	}
	logos := make([]string, 0, len(imgs.Logos))
	for _, p := range imgs.Logos {
		if p.FilePath != "" {
			logos = append(logos, base+p.FilePath)
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"tmdb_id":    id,
		"posters":    posters,
		"backdrops":  backdrops,
		"logos":      logos,
	})
}

func splitCSV(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		n := strings.TrimSpace(p)
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

func simpleGet(u string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func formatTaskErrorMessage(err error) string {
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return "remote_error: unknown"
	}
	return msg
}

func summarizeProviderWarnings(res *scraper.ScrapeResult) string {
	if res == nil || res.Extra == nil {
		return "ok"
	}
	raw := res.Extra["provider_errors"]
	switch typed := raw.(type) {
	case map[string]map[string]string:
		if len(typed) == 0 {
			return "ok"
		}
		parts := make([]string, 0, len(typed))
		for provider, detail := range typed {
			cat := strings.TrimSpace(detail["category"])
			if cat == "" {
				cat = "remote_error"
			}
			parts = append(parts, provider+":"+cat)
		}
		if len(parts) == 0 {
			return "ok"
		}
		return "ok_with_warnings: " + strings.Join(parts, "; ")
	case map[string]any:
		if len(typed) == 0 {
			return "ok"
		}
		parts := make([]string, 0, len(typed))
		for provider, v := range typed {
			detail, ok := v.(map[string]any)
			if !ok {
				continue
			}
			cat, _ := detail["category"].(string)
			if strings.TrimSpace(cat) == "" {
				cat = "remote_error"
			}
			parts = append(parts, provider+":"+cat)
		}
		if len(parts) == 0 {
			return "ok"
		}
		return "ok_with_warnings: " + strings.Join(parts, "; ")
	default:
		return "ok"
	}
}

