package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
	"knox-media/internal/coreiface"
	"knox-media/internal/metadatalib"
	"knox-media/internal/publication"
	"knox-media/internal/scraper"
	"knox-media/internal/store"
	"knox-media/internal/tvparse"
)

type scrapeTaskCreateBody struct {
	MediaIDs []int64 `json:"media_ids"`
	Source   string  `json:"source"`
}

type scrapeRunBody struct {
	IDs   []int64 `json:"ids"`
	Limit int     `json:"limit"`
}

var scrapeBeforeFailUpdate func() error
var withImmediateScrapeTx = store.WithImmediateConnTx

const (
	maxScrapeTaskFailures = 3
	scrapeWorkerInterval  = 20 * time.Second
	scrapeWorkerBatchMin  = 5
	scrapeWorkerBatchMax  = 20
)

// StartScrapeTaskLoop continuously drains waiting scrape tasks (not only via scheduled_task).
func (h *Handler) StartScrapeTaskLoop(ctx context.Context) {
	h.runScrapeWorkerOnce(ctx)
	tk := time.NewTicker(scrapeWorkerInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			h.runScrapeWorkerOnce(ctx)
		}
	}
}

func (h *Handler) runScrapeWorkerOnce(ctx context.Context) {
	if h == nil || h.App == nil || h.App.DB == nil {
		return
	}
	if !h.isScrapeEnabled(ctx) {
		return
	}
	pending := h.countPendingScrapeTasks(ctx)
	if pending == 0 {
		return
	}
	limit := scrapeWorkerBatchLimit(pending)
	done, failed := h.runScrapeTasksWithLimit(ctx, nil, limit)
	if done+failed > 0 {
		remaining := h.countPendingScrapeTasks(ctx)
		log.Printf("scrape worker: processed=%d ok=%d fail=%d remaining=%d", done+failed, done, failed, remaining)
	}
}

func (h *Handler) isScrapeEnabled(ctx context.Context) bool {
	var enabled int
	if err := h.App.DB.QueryRowContext(ctx, `SELECT enabled FROM scrape_config WHERE id = 1`).Scan(&enabled); err != nil {
		return true
	}
	return enabled == 1
}

func (h *Handler) countPendingScrapeTasks(ctx context.Context) int {
	var n int
	_ = h.App.DB.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM scrape_task
		WHERE status = 'waiting'
		   OR (status = 'failed' AND COALESCE(fail_count, 0) < ?)`, maxScrapeTaskFailures,
	).Scan(&n)
	return n
}

func scrapeWorkerBatchLimit(pending int) int {
	if pending <= 0 {
		return scrapeWorkerBatchMin
	}
	limit := pending
	if limit < scrapeWorkerBatchMin {
		limit = scrapeWorkerBatchMin
	}
	if limit > scrapeWorkerBatchMax {
		limit = scrapeWorkerBatchMax
	}
	return limit
}

type scrapeConfigBody struct {
	Enabled      *int              `json:"enabled"`
	Providers    []string          `json:"providers"`
	ImageSources []string          `json:"image_sources"`
	APIKeys      map[string]string `json:"api_keys"`
}

type manualMatchBody struct {
	Query      string `json:"query"`
	Year       int    `json:"year"`
	Source     string `json:"source"`
	ExternalID string `json:"external_id"`
	MediaType  string `json:"media_type"`
	Language   string `json:"language"`
	Poster     string `json:"poster"`
	Overview   string `json:"overview"`
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
	// Manual scrape: re-queue an abandoned task instead of creating a duplicate row.
	if source != "auto" && source != "auto-scan" {
		res, _ := h.App.DB.Exec(
			`UPDATE scrape_task SET status='waiting', fail_count=0, progress=0, message='', finished_at=NULL, started_at=NULL, source=?, created_by=?
			 WHERE media_id = ? AND status = 'abandoned'`,
			source, createdBy, mediaID,
		)
		if n, _ := res.RowsAffected(); n > 0 {
			return
		}
	}
	var exists int
	_ = h.App.DB.QueryRow(
		`SELECT COUNT(1) FROM scrape_task WHERE media_id = ? AND status IN ('waiting','running','failed','abandoned')`,
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

type scrapeProviderTestBody struct {
	Provider string `json:"provider" binding:"required"`
}

func (h *Handler) TestScrapeProvider(c *gin.Context) {
	var body scrapeProviderTestBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	provider := strings.ToLower(strings.TrimSpace(body.Provider))
	if provider == "ai" {
		c.JSON(http.StatusOK, h.testAIProviderConnectivity())
		return
	}
	cfg := h.readScrapeConfig()
	c.JSON(http.StatusOK, scraper.CheckProviderConnectivity(provider, cfg.APIKeys))
}

func (h *Handler) testAIProviderConnectivity() scraper.ProviderTestResult {
	rows, err := h.App.DB.Query(`SELECT id FROM ai_provider_config WHERE enabled = 1 ORDER BY id`)
	if err != nil {
		return scraper.ProviderTestResult{OK: false, Message: "无法读取 AI 配置"}
	}
	defer rows.Close()

	var lastMsg string
	var found bool
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		found = true
		result := h.testSingleAIProvider(id)
		if result.OK {
			var name string
			_ = h.App.DB.QueryRow(`SELECT name FROM ai_provider_config WHERE id = ?`, id).Scan(&name)
			if strings.TrimSpace(name) != "" {
				return scraper.ProviderTestResult{OK: true, Message: fmt.Sprintf("连接成功（%s）", name)}
			}
			return result
		}
		lastMsg = result.Message
	}
	if !found {
		return scraper.ProviderTestResult{OK: false, Message: "未启用任何 AI 提供商，请在「AI 提供商」页面配置"}
	}
	if lastMsg != "" {
		return scraper.ProviderTestResult{OK: false, Message: lastMsg}
	}
	return scraper.ProviderTestResult{OK: false, Message: "AI 提供商配置不完整"}
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
		SELECT t.id, t.media_id, m.title, t.task_type, t.source, t.query, t.year, t.status, t.progress, COALESCE(t.fail_count,0), t.message, t.created_at, t.started_at, t.finished_at
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
		var id, mediaID, year, progress, failCount sql.NullInt64
		var title, taskType, source, query, status, message, createdAt, startedAt, finishedAt sql.NullString
		if err := rows.Scan(&id, &mediaID, &title, &taskType, &source, &query, &year, &status, &progress, &failCount, &message, &createdAt, &startedAt, &finishedAt); err != nil {
			continue
		}
		items = append(items, gin.H{
			"id": id.Int64, "media_id": mediaID.Int64, "title": title.String, "task_type": taskType.String, "source": source.String,
			"query": query.String, "year": year.Int64, "status": status.String, "progress": progress.Int64,
			"fail_count": failCount.Int64, "message": message.String,
			"created_at": createdAt.String, "started_at": startedAt.String, "finished_at": finishedAt.String,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

var ErrScrapeClaimLost = errors.New("scrape claim ownership lost")

type scrapeClaim struct {
	ID, MediaID               int64
	RunID, StepID, Generation sql.NullInt64
	Owner                     string
	Attempts, MaxAttempts     int
	LeaseUntil                time.Time
}

func claimScrapeTaskWithOwnerRegistry(ctx context.Context, db *sql.DB, taskID int64, registry coreiface.CapabilityRegistry) (*scrapeClaim, error) {
	payload, err := publication.ClaimEligible(ctx, db, publication.ClaimRequest{Family: publication.QueueScrape, TaskType: "scrape", Owner: "scrape", QueueID: &taskID, Registry: registry})
	if err != nil || payload == nil {
		return nil, err
	}
	return &scrapeClaim{ID: payload.QueueID, MediaID: payload.MediaID, RunID: payload.RunID, StepID: payload.StepID, Generation: payload.Generation, Owner: payload.Owner, Attempts: payload.Attempts, MaxAttempts: payload.MaxAttempts, LeaseUntil: payload.LeaseUntil}, nil
}

func claimScrapeTaskWithOwner(ctx context.Context, db *sql.DB, taskID int64) (*scrapeClaim, error) {
	return claimScrapeTaskWithOwnerRegistry(ctx, db, taskID, publication.NewCapabilityMatrix([]string{"scrape"}))
}
func claimScrapeTask(ctx context.Context, db *sql.DB, taskID int64) (bool, error) {
	c, e := claimScrapeTaskWithOwnerRegistry(ctx, db, taskID, publication.NewCapabilityMatrix([]string{"scrape"}))
	return c != nil, e
}

func (h *Handler) RunScrapeTasks(c *gin.Context) {
	var body scrapeRunBody
	_ = c.ShouldBindJSON(&body)
	limit := body.Limit
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	done, failed := h.runScrapeTasksWithLimit(c.Request.Context(), body.IDs, limit)
	c.JSON(http.StatusOK, gin.H{"ok": true, "done": done, "failed": failed})
}

func (h *Handler) runScrapeTasksWithLimit(ctx context.Context, ids []int64, limit int) (int, int) {
	if h == nil || ctx.Err() != nil {
		return 0, 0
	}
	h.scrapeRunMu.Lock()
	defer h.scrapeRunMu.Unlock()

	var taskIDs []int64
	if len(ids) > 0 {
		taskIDs = ids
	} else {
		rows, err := h.App.DB.QueryContext(ctx, fmt.Sprintf(`
			SELECT q.id FROM scrape_task q
			WHERE (q.available_at IS NULL OR q.available_at<=CURRENT_TIMESTAMP)
			  AND (q.status = 'waiting'
			   OR (q.status = 'failed' AND COALESCE(q.fail_count, 0) < ?))
			  AND %s
			ORDER BY q.id LIMIT ?`, publication.LinkedClaimEligibilitySQL("q")), maxScrapeTaskFailures, limit)
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
	for _, taskID := range taskIDs {
		if ctx.Err() != nil {
			break
		}
		var mediaID int64
		var libraryID int64
		var failCount int
		var taskStatus string
		var source, query, title, existingMeta, filePath, fileType, libraryType string
		var year sql.NullInt64
		claim, claimErr := claimScrapeTaskWithOwnerRegistry(ctx, h.App.DB, taskID, h.PublicationCapabilities)
		claimed := claim != nil
		if claimErr != nil || !claimed {
			continue
		}
		err := h.App.DB.QueryRowContext(ctx, `
			SELECT t.media_id, t.source, COALESCE(t.query,''), t.year, COALESCE(m.title,''), COALESCE(m.meta_json,''),
			       m.library_id, COALESCE(m.file_path,''), COALESCE(m.file_type,''), COALESCE(t.fail_count,0), COALESCE(t.status,''),
			       COALESCE(l.type,'')
			FROM scrape_task t
			JOIN media m ON m.id = t.media_id
			LEFT JOIN library l ON l.id = m.library_id
			WHERE t.id = ?`, taskID,
		).Scan(&mediaID, &source, &query, &year, &title, &existingMeta, &libraryID, &filePath, &fileType, &failCount, &taskStatus, &libraryType)
		if err != nil {
			continue
		}
		if taskStatus != "running" || failCount >= maxScrapeTaskFailures {
			continue
		}
		workCtx, stopWork := context.WithCancel(ctx)
		leaseLost := make(chan struct{}, 1)
		heartbeatDone := make(chan struct{})
		go func(c scrapeClaim) {
			ticker := time.NewTicker(25 * time.Second)
			defer ticker.Stop()
			defer close(heartbeatDone)
			for {
				select {
				case <-workCtx.Done():
					return
				case <-ticker.C:
					if err := renewScrapeClaim(workCtx, h.App.DB, c); err != nil {
						select {
						case leaseLost <- struct{}{}:
						default:
						}
						stopWork()
						return
					}
				}
			}
		}(*claim)
		finishWork := func() bool {
			stopWork()
			<-heartbeatDone
			select {
			case <-leaseLost:
				return false
			default:
				return true
			}
		}
		if query == "" {
			query = title
		}
		if year.Valid && year.Int64 > 0 {
			query = strings.TrimSpace(query + " " + strconv.FormatInt(year.Int64, 10))
		}
		cfg := h.readLibraryScrapeConfig(libraryID)
		var res *scraper.ScrapeResult
		var sErr error
		tvCtx := scraper.ParseTVScrapeContext(existingMeta)
		if tvparse.IsTVLibraryType(libraryType) && tvCtx.ValidEpisode() {
			res, sErr = scraper.ScrapeTVEpisode(cfg, tvCtx, libraryType)
		} else {
			if h.scrapeWithConfig != nil {
				res, sErr = h.scrapeWithConfig(query, source, cfg)
			} else {
				res, sErr = scraper.ScrapeWithConfig(query, source, cfg)
			}
		}
		if res == nil {
			res = &scraper.ScrapeResult{Title: query, Genres: []string{}, Extra: map[string]any{}}
		}
		if !scraper.HasMeaningfulScrapeData(res) {
			fmsg := scraper.NoDataFailureMessage(res)
			if sErr != nil {
				fmsg = scraper.FormatScrapeErrorMessage(sErr)
			}
			if err := failScrapeClaim(ctx, h.App.DB, *claim, source, query, fmsg); err != nil {
				log.Printf("fail scrape task %d: %v", taskID, err)
			}
			failed++
			finishWork()
			continue
		}

		effects := scrapeCompletionEffects{LibraryID: libraryID}
		if claim.Generation.Valid && h.App.Config != nil {
			stageID := fmt.Sprintf("scrape:%d:%d:%d", claim.ID, claim.Attempts, claim.Generation.Int64)
			if staged, e := metadatalib.StageScrapeImagesDurable(workCtx, h.App.DB, h.App.Config.Data.MetadataLibrary, h.App.Config.Data.Upload, claim.RunID.Int64, claim.StepID.Int64, mediaID, claim.Generation.Int64, claim.Owner, stageID, res); e == nil {
				effects.Artwork = staged
			}
		}
		if strings.EqualFold(fileType, "video") && !scraper.HasScrapePoster(res) && (imageSourceEnabled(cfg, "embedded") || imageSourceEnabled(cfg, "screen_grabber")) {
			effects.PosterFallback = true
		}
		if tmdbID, mediaType := extractTMDBIDFromMeta(mustScrapeJSON(res), libraryType); tmdbID != "" {
			if key := cfg.APIKeys["tmdb"]; key != "" {
				if credits, e := scraper.FetchTMDBCredits(tmdbID, mediaType, "zh-CN", key); e == nil {
					effects.Credits = credits
					effects.AvatarBaseURL = scraper.GetTMDBImageBase() + "/t/p/w185"
				}
			}
		}
		okMsg := summarizeProviderWarnings(res)
		if sErr != nil {
			if okMsg == "ok" {
				okMsg = "ok_with_warnings: metadata_partial"
			} else {
				okMsg += "; metadata_partial"
			}
		}
		if !finishWork() {
			failed++
			continue
		}
		if err := completeScrapeClaimWithEffects(ctx, h.App.DB, *claim, source, query, okMsg, res, effects); err != nil {
			if failErr := failScrapeClaim(ctx, h.App.DB, *claim, source, query, "complete scrape task: "+err.Error()); failErr != nil {
				log.Printf("complete/fail scrape task %d: %v", taskID, errors.Join(err, failErr))
			}
			failed++
			continue
		}
		h.scheduleLibraryPreviewRefresh(libraryID)
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
	cfg := h.readScrapeConfig()
	if q := strings.TrimSpace(body.Query); q != "" {
		if normalized, parsedYear := scraper.NormalizeSearchInput(q); normalized != "" {
			body.Query = normalized
			if body.Year <= 0 && parsedYear > 0 {
				body.Year = parsedYear
			}
		}
	}
	var res *scraper.ScrapeResult
	var sErr error
	if strings.TrimSpace(body.ExternalID) != "" && strings.TrimSpace(body.Source) != "" {
		res, sErr = scraper.FetchMatchByExternalID(body.Source, body.ExternalID, body.MediaType, body.Language, cfg)
	} else if strings.TrimSpace(body.Query) != "" {
		res, sErr = scraper.ScrapeWithConfig(body.Query, body.Source, cfg)
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query or external_id required"})
		return
	}
	if res == nil {
		res = &scraper.ScrapeResult{Title: body.Query, Genres: []string{}, Extra: map[string]any{}}
	}
	scraper.ApplyMatchCandidateFields(res, body.Poster, body.Overview)
	var libraryID int64
	var fileType string
	_ = h.App.DB.QueryRow(`SELECT library_id, COALESCE(file_type,'') FROM media WHERE id = ?`, id).Scan(&libraryID, &fileType)
	if err := h.applyManualMatchLocalImages(c.Request.Context(), id, libraryID, fileType, cfg, res); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !scraper.HasMeaningfulScrapeData(res) {
		msg := scraper.NoDataFailureMessage(res)
		if sErr != nil {
			msg = scraper.FormatScrapeErrorMessage(sErr)
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}
	var existing sql.NullString
	_ = h.App.DB.QueryRow(`SELECT meta_json FROM media WHERE id = ?`, id).Scan(&existing)
	scraper.PreserveScrapeImagesFromExisting(res, existing.String)
	if _, pErr := h.persistScrapeArtwork(id, res); pErr != nil {
		log.Printf("manual match artwork persist media=%d: %v", id, pErr)
	}
	merged, mErr := scraper.MergeMetaJSON(existing.String, map[string]any{"scrape": res})
	if mErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存元数据失败: " + mErr.Error()})
		return
	}
	if err := updateMediaTitleAndMetaTx(c.Request.Context(), h.App.DB, id, res.Title, merged); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	scraper.SanitizeScrapeResult(res)
	h.syncSeriesCollectionMeta(libraryID, id, res)
	h.scheduleLibraryPreviewRefresh(libraryID)
	c.JSON(http.StatusOK, gin.H{"ok": true, "scrape": res})
}

func (h *Handler) SearchScrapeMatches(c *gin.Context) {
	query := strings.TrimSpace(c.Query("query"))
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query required"})
		return
	}
	year := 0
	if ys := strings.TrimSpace(c.Query("year")); ys != "" {
		if n, err := strconv.Atoi(ys); err == nil && n > 0 {
			year = n
		}
	}
	if normalized, parsedYear := scraper.NormalizeSearchInput(query); normalized != "" {
		query = normalized
		if year == 0 && parsedYear > 0 {
			year = parsedYear
		}
	}
	source := strings.TrimSpace(c.Query("source"))
	language := strings.TrimSpace(c.Query("language"))
	limit := 20
	if ls := strings.TrimSpace(c.Query("limit")); ls != "" {
		if n, err := strconv.Atoi(ls); err == nil && n > 0 {
			limit = n
		}
	}
	cfg := h.readScrapeConfig()
	items, err := scraper.SearchMatchCandidates(query, year, source, language, cfg, limit)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"items": []any{}, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) ParseScrapeTitle(c *gin.Context) {
	raw := strings.TrimSpace(c.Query("raw"))
	if raw == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "raw required"})
		return
	}
	title, titleAlt, year := scraper.ExtractSearchTerms(raw)
	if title == "" {
		title = strings.TrimSpace(raw)
	}
	c.JSON(http.StatusOK, gin.H{
		"title":     title,
		"title_alt": titleAlt,
		"year":      year,
	})
}

func (h *Handler) UnmatchMedia(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var title, orig, meta sql.NullString
	if err := h.App.DB.QueryRow(`SELECT title, original_title, meta_json FROM media WHERE id = ?`, id).Scan(&title, &orig, &meta); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var raw map[string]any
	_ = json.Unmarshal([]byte(meta.String), &raw)
	if raw == nil {
		raw = map[string]any{}
	}
	delete(raw, "scrape")
	newTitle := title.String
	if strings.TrimSpace(orig.String) != "" {
		newTitle = orig.String
	}
	js, _ := json.Marshal(raw)
	if err := updateMediaTitleAndMetaTx(c.Request.Context(), h.App.DB, id, newTitle, string(js)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
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
		AIProviders:  h.loadEnabledAIProviders(),
	}
}

func (h *Handler) loadEnabledAIProviders() []scraper.AIProviderConfig {
	if h == nil || h.App == nil || h.App.DB == nil {
		return nil
	}
	rows, err := h.App.DB.Query(
		`SELECT id, name, api_url, api_key, model FROM ai_provider_config WHERE enabled = 1 ORDER BY id`,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]scraper.AIProviderConfig, 0)
	for rows.Next() {
		var p scraper.AIProviderConfig
		if rows.Scan(&p.ID, &p.Name, &p.APIURL, &p.APIKey, &p.Model) == nil {
			out = append(out, p)
		}
	}
	return out
}

// readLibraryScrapeConfig merges global API keys with per-library provider priority lists.
func (h *Handler) readLibraryScrapeConfig(libraryID int64) scraper.Config {
	cfg := h.readScrapeConfig()
	if libraryID <= 0 {
		return cfg
	}
	var metadataProviders, imageProviders string
	err := h.App.DB.QueryRow(
		`SELECT COALESCE(metadata_providers, ''), COALESCE(image_providers, '') FROM library WHERE id = ?`,
		libraryID,
	).Scan(&metadataProviders, &imageProviders)
	if err != nil {
		return cfg
	}
	if p := splitCSV(metadataProviders); len(p) > 0 {
		cfg.Providers = p
	}
	if p := splitCSV(imageProviders); len(p) > 0 {
		cfg.ImageSources = p
	}
	return cfg
}

// shouldPreserveSeriesTitle keeps an established series title when linking/scraping additional episodes.
func shouldPreserveSeriesTitle(existingTitle string, linkedEpisodeCount int64) bool {
	return strings.TrimSpace(existingTitle) != "" && linkedEpisodeCount > 1
}

// syncSeriesCollectionMeta updates the series record and shares show-level scrape fields across episodes.
func (h *Handler) syncSeriesCollectionMeta(libraryID int64, mediaID int64, res *scraper.ScrapeResult) {
	if h == nil || h.App == nil || h.App.DB == nil {
		return
	}
	_ = syncSeriesCollectionMetaExecutor(context.Background(), h.App.DB, libraryID, mediaID, res)
}

func stringScrapeField(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", x))
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
	if err := updateMediaTitleAndMetaTx(c.Request.Context(), h.App.DB, id, title, string(js)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
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
	if err := store.UpdateMediaMetaAndPhotoTime(c.Request.Context(), h.App.DB, id, string(js)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
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
		"tmdb_id":   id,
		"posters":   posters,
		"backdrops": backdrops,
		"logos":     logos,
	})
}

// ListMediaImageCandidates returns poster/backdrop/logo candidates for a media item,
// querying ONLY the image sources configured on the media's owning library. Sources
// not selected for the library are never contacted, so unreachable providers can be
// omitted from the library config to avoid long connection delays.
//
// Query: kind=poster|backdrop|logo (default poster)
func (h *Handler) ListMediaImageCandidates(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	kind := strings.ToLower(strings.TrimSpace(c.DefaultQuery("kind", "poster")))

	var libraryID int64
	var title string
	var year int
	var metaJSON string
	if err := h.App.DB.QueryRow(
		`SELECT library_id, COALESCE(title, ''), COALESCE(year, 0), COALESCE(meta_json, '') FROM media WHERE id = ?`,
		id,
	).Scan(&libraryID, &title, &year, &metaJSON); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "media not found"})
		return
	}

	cfg := h.readLibraryScrapeConfig(libraryID)
	tmdbID := extractTmdbID(metaJSON)
	if tmdbID == "" {
		// TV: the tmdb_id lives on the series row rather than the episode media meta.
		_ = h.App.DB.QueryRow(`
			SELECT COALESCE(sr.tmdb_id, '')
			FROM episode_media em
			JOIN episode ep ON ep.id = em.episode_id
			JOIN season se ON se.id = ep.season_id
			JOIN series sr ON sr.id = se.tv_id
			WHERE em.media_id = ? AND sr.library_id = ?
			LIMIT 1`, id, libraryID).Scan(&tmdbID)
	}

	keyword := strings.TrimSpace(title)
	if keyword == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "media has no title to search"})
		return
	}

	candidates, errs, scraped := scraper.FetchImageCandidates(cfg, keyword, year, kind, tmdbID)
	if candidates == nil {
		candidates = []scraper.ImageCandidate{}
	}
	resp := gin.H{"candidates": candidates, "scraped": scraped}
	if len(errs) > 0 {
		resp["errors"] = errs
	}
	c.JSON(http.StatusOK, resp)
}

// extractTmdbID pulls scrape.extra.tmdb_id out of a media meta_json blob.
func extractTmdbID(metaJSON string) string {
	metaJSON = strings.TrimSpace(metaJSON)
	if metaJSON == "" {
		return ""
	}
	var doc struct {
		Scrape struct {
			Extra map[string]any `json:"extra"`
		} `json:"scrape"`
	}
	if json.Unmarshal([]byte(metaJSON), &doc) != nil {
		return ""
	}
	return stringScrapeField(doc.Scrape.Extra["tmdb_id"])
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

func (h *Handler) persistScrapeArtwork(mediaID int64, res *scraper.ScrapeResult) (int, error) {
	if h == nil || h.App == nil || h.App.Config == nil || res == nil || mediaID <= 0 {
		return 0, nil
	}
	return metadatalib.PersistScrapeImages(
		h.App.Config.Data.MetadataLibrary,
		h.App.Config.Data.Upload,
		mediaID,
		res,
	)
}

func (h *Handler) backfillScrapeArtworkFromMeta(mediaID int64, metaJSON string) (merged string, saved int, err error) {
	if !metadatalib.MetaHasRemoteScrapeImages(metaJSON) {
		return metaJSON, 0, nil
	}
	res, ok := metadatalib.ScrapeResultFromMetaJSON(metaJSON)
	if !ok || res == nil {
		return metaJSON, 0, nil
	}
	saved, err = h.persistScrapeArtwork(mediaID, res)
	if saved == 0 {
		return metaJSON, 0, err
	}
	merged, mErr := scraper.MergeMetaJSON(metaJSON, map[string]any{"scrape": res})
	if mErr != nil {
		return metaJSON, saved, mErr
	}
	if updateErr := store.UpdateMediaMetaAndPhotoTime(context.Background(), h.App.DB, mediaID, merged); updateErr != nil {
		return metaJSON, saved, updateErr
	}
	return merged, saved, err
}

func (h *Handler) BackfillScrapeArtwork(c *gin.Context) {
	var body struct {
		MediaIDs []int64 `json:"media_ids"`
		Limit    int     `json:"limit"`
	}
	_ = c.ShouldBindJSON(&body)
	limit := body.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	var rows *sql.Rows
	var err error
	if len(body.MediaIDs) > 0 {
		placeholders := make([]string, len(body.MediaIDs))
		args := make([]any, len(body.MediaIDs))
		for i, id := range body.MediaIDs {
			placeholders[i] = "?"
			args[i] = id
		}
		q := `SELECT id, COALESCE(meta_json,'') FROM media WHERE id IN (` + strings.Join(placeholders, ",") + `)`
		rows, err = h.App.DB.Query(q, args...)
	} else {
		rows, err = h.App.DB.Query(`SELECT id, COALESCE(meta_json,'') FROM media ORDER BY id DESC LIMIT ?`, limit*4)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	processed, filesSaved, failed := 0, 0, 0
	for rows.Next() && processed < limit {
		var mediaID int64
		var meta string
		if rows.Scan(&mediaID, &meta) != nil {
			continue
		}
		if !metadatalib.MetaHasRemoteScrapeImages(meta) {
			continue
		}
		processed++
		_, n, bErr := h.backfillScrapeArtworkFromMeta(mediaID, meta)
		filesSaved += n
		if bErr != nil {
			failed++
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":          true,
		"processed":   processed,
		"files_saved": filesSaved,
		"failed":      failed,
	})
}

func completeScrapeTaskTx(ctx context.Context, db *sql.DB, taskID, mediaID int64, source, query, message, resultJSON, owner string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var run, step sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT ingest_run_id,ingest_step_id FROM scrape_task WHERE id=? AND media_id=? AND status='running' AND ((lease_owner IS NULL AND ?='') OR lease_owner=?)`, taskID, mediaID, owner, owner).Scan(&run, &step); err != nil {
		return err
	}
	r, err := tx.ExecContext(ctx, `UPDATE scrape_task SET status='done',progress=100,finished_at=CURRENT_TIMESTAMP,message=?,lease_owner=NULL,lease_until=NULL WHERE id=? AND media_id=? AND status='running' AND ((lease_owner IS NULL AND ?='') OR lease_owner=?)`, message, taskID, mediaID, owner, owner)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	if step.Valid {
		if _, err = tx.ExecContext(ctx, `UPDATE media_ingest_step SET status='done',finished_at=CURRENT_TIMESTAMP,lease_owner=NULL,lease_until=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=? AND ((lease_owner IS NULL AND ?='') OR lease_owner=?)`, step.Int64, owner, owner); err != nil {
			return err
		}
		if run.Valid {
			if err = publication.AggregateTx(ctx, tx, run.Int64); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO scrape_history(task_id,media_id,source,query,status,message,result_json) VALUES(?,?,?,?, 'done',?,?)`, taskID, mediaID, source, query, message, resultJSON); err != nil {
		return err
	}
	return tx.Commit()
}
func failScrapeTaskDB(ctx context.Context, db *sql.DB, taskID, mediaID int64, source, query, message, owner string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var fails int
	var run, step sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(fail_count,0),ingest_run_id,ingest_step_id FROM scrape_task WHERE id=? AND media_id=? AND status='running' AND ((lease_owner IS NULL AND ?='') OR lease_owner=?)`, taskID, mediaID, owner, owner).Scan(&fails, &run, &step); err != nil {
		return err
	}
	if scrapeBeforeFailUpdate != nil {
		if err := scrapeBeforeFailUpdate(); err != nil {
			return err
		}
	}
	final := fails >= maxScrapeTaskFailures
	status := "failed"
	if !final {
		status = "waiting"
	}
	stepStatus := status
	if final {
		stepStatus = "failed"
	}
	r, err := tx.ExecContext(ctx, `UPDATE scrape_task SET status=?,fail_count=?,progress=CASE WHEN ? THEN 100 ELSE progress END,finished_at=CASE WHEN ? THEN CURRENT_TIMESTAMP ELSE NULL END,message=?,lease_owner=NULL,lease_until=NULL,available_at=CASE WHEN ? THEN available_at ELSE datetime(CURRENT_TIMESTAMP, CASE ? WHEN 1 THEN '+5 seconds' ELSE '+30 seconds' END) END WHERE id=? AND status='running' AND ((lease_owner IS NULL AND ?='') OR lease_owner=?)`, status, fails, final, final, message, final, fails, taskID, owner, owner)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return fmt.Errorf("scrape task ownership lost")
	}
	if step.Valid {
		if _, err = tx.ExecContext(ctx, `UPDATE media_ingest_step SET status=?,attempts=?,last_error=?,finished_at=CASE WHEN ? THEN CURRENT_TIMESTAMP ELSE NULL END,lease_owner=NULL,lease_until=NULL,available_at=(SELECT available_at FROM scrape_task WHERE id=?),updated_at=CURRENT_TIMESTAMP WHERE id=?`, stepStatus, fails, message, final, taskID, step.Int64); err != nil {
			return err
		}
		if run.Valid {
			if err = publication.AggregateTx(ctx, tx, run.Int64); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO scrape_history(task_id,media_id,source,query,status,message) VALUES(?,?,?,?, 'failed',?)`, taskID, mediaID, source, query, message); err != nil {
		return err
	}
	return tx.Commit()
}

func scrapeClaimPredicate() string {
	return `q.id=? AND q.media_id=? AND q.status='running' AND q.lease_owner=? AND ((q.ingest_run_id IS NULL AND q.ingest_step_id IS NULL AND q.generation IS NULL AND ?=0 AND ?=0 AND ?=0) OR (q.ingest_run_id=? AND q.ingest_step_id=? AND q.generation=? AND EXISTS(SELECT 1 FROM media_ingest_step st JOIN media_ingest_run r ON r.id=st.run_id JOIN media m ON m.id=st.media_id WHERE st.id=q.ingest_step_id AND st.run_id=q.ingest_run_id AND st.media_id=q.media_id AND st.generation=q.generation AND st.status='running' AND st.lease_owner=q.lease_owner AND st.attempts=? AND r.media_id=q.media_id AND r.generation=q.generation AND r.status IN ('processing','published','degraded') AND r.superseded_at IS NULL AND r.superseded_by_generation IS NULL AND m.ingest_generation=q.generation)))`
}
func scrapeClaimArgs(c scrapeClaim) []any {
	return []any{c.ID, c.MediaID, c.Owner, c.RunID.Int64, c.StepID.Int64, c.Generation.Int64, c.RunID.Int64, c.StepID.Int64, c.Generation.Int64, c.Attempts}
}
func scrapeClaimImmediate(ctx context.Context, db *sql.DB, fn func(store.ImmediateConnTx) error) error {
	policy := store.RetryPolicy{Operation: "scrape_claim_lifecycle", MaxElapsed: 2 * time.Second, BaseBackoff: 10 * time.Millisecond, MaxBackoff: 100 * time.Millisecond}
	return store.WithBusyRetryPolicyContext(ctx, nil, policy, func(attempt context.Context) error { _, err := withImmediateScrapeTx(attempt, db, fn); return err })
}
func renewScrapeClaim(ctx context.Context, db *sql.DB, c scrapeClaim) error {
	return scrapeClaimImmediate(ctx, db, func(tx store.ImmediateConnTx) error {
		res, err := tx.ExecContext(ctx, `UPDATE scrape_task AS q SET lease_until=datetime(CURRENT_TIMESTAMP,'+90 seconds') WHERE `+scrapeClaimPredicate(), scrapeClaimArgs(c)...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrScrapeClaimLost
		}
		if c.StepID.Valid {
			res, err = tx.ExecContext(ctx, `UPDATE media_ingest_step SET lease_until=(SELECT lease_until FROM scrape_task WHERE id=?) WHERE id=? AND run_id=? AND media_id=? AND generation=? AND status='running' AND lease_owner=? AND attempts=?`, c.ID, c.StepID.Int64, c.RunID.Int64, c.MediaID, c.Generation.Int64, c.Owner, c.Attempts)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n != 1 {
				return ErrScrapeClaimLost
			}
		}
		return nil
	})
}
func completeScrapeClaim(ctx context.Context, db *sql.DB, c scrapeClaim, source, query, message string, result *scraper.ScrapeResult) error {
	return completeScrapeClaimWithEffects(ctx, db, c, source, query, message, result, scrapeCompletionEffects{})
}
func completeScrapeClaimWithEffects(ctx context.Context, db *sql.DB, c scrapeClaim, source, query, message string, result *scraper.ScrapeResult, effects scrapeCompletionEffects) error {
	var expectedManifest []byte
	var expectedDigest string
	err := scrapeClaimImmediate(ctx, db, func(tx store.ImmediateConnTx) error {
		args := append([]any{message}, scrapeClaimArgs(c)...)
		res, err := tx.ExecContext(ctx, `UPDATE scrape_task AS q SET status='done',progress=100,finished_at=CURRENT_TIMESTAMP,message=?,lease_owner=NULL,lease_until=NULL WHERE `+scrapeClaimPredicate(), args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrScrapeClaimLost
		}
		_, committed, err := commitScrapeMediaTx(ctx, tx, c.MediaID, result, result.Title)
		if err != nil {
			return err
		}
		result = committed
		manifestDigest, effectErr := applyScrapeCompletionEffectsTx(ctx, tx, c, result, effects)
		expectedManifest, expectedDigest, _ = canonicalScrapeEffectManifest(c, result, effects)
		if effectErr != nil {
			err = effectErr
			return err
		}
		resultJSON, _ := json.Marshal(map[string]any{"result": result, "effect_manifest_digest": manifestDigest})
		if c.StepID.Valid {
			res, err = tx.ExecContext(ctx, `UPDATE media_ingest_step SET status='done',finished_at=CURRENT_TIMESTAMP,lease_owner=NULL,lease_until=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=? AND run_id=? AND media_id=? AND generation=? AND status='running' AND lease_owner=? AND attempts=?`, c.StepID.Int64, c.RunID.Int64, c.MediaID, c.Generation.Int64, c.Owner, c.Attempts)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n != 1 {
				return ErrScrapeClaimLost
			}
			if err = publication.AggregateTx(ctx, tx, c.RunID.Int64); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO scrape_history(task_id,media_id,source,query,status,message,result_json) VALUES(?,?,?,?, 'done',?,?)`, c.ID, c.MediaID, source, query, message, resultJSON)
		return err
	})
	if err == nil {
		return nil
	}
	var uncertain *store.ImmediateCommitError
	if !errors.As(err, &uncertain) {
		return err
	}
	var n int
	q := `SELECT COUNT(*) FROM scrape_task q JOIN scrape_history h ON h.task_id=q.id JOIN scrape_effect_commit ec ON ec.task_id=q.id AND ec.attempt=? AND ec.generation=? WHERE q.id=? AND q.media_id=? AND q.status='done' AND h.status='done' AND json_extract(h.result_json,'$.effect_manifest_digest')=ec.manifest_digest AND ec.manifest_digest=? AND ec.manifest_json=?`
	args := []any{c.Attempts, c.Generation.Int64, c.ID, c.MediaID, expectedDigest, string(expectedManifest)}
	if effects.Artwork.StageID != "" {
		q += ` AND EXISTS(SELECT 1 FROM media_ingest_evidence e JOIN media_asset_stage_journal j ON j.stage_id=e.stage_id WHERE e.stage_id=? AND e.run_id=? AND e.step_id=? AND e.media_id=? AND e.generation=? AND e.kind='scrape_artwork' AND j.state='committed')`
		args = append(args, effects.Artwork.StageID, c.RunID.Int64, c.StepID.Int64, c.MediaID, c.Generation.Int64)
	}
	if e := db.QueryRowContext(context.Background(), q, args...).Scan(&n); e == nil && n == 1 {
		return nil
	}
	return uncertain
}

func failScrapeClaim(ctx context.Context, db *sql.DB, c scrapeClaim, source, query, message string) error {
	return scrapeClaimImmediate(ctx, db, func(tx store.ImmediateConnTx) error {
		final := c.Attempts >= c.MaxAttempts
		status := "waiting"
		if final {
			status = "failed"
		}
		args := append([]any{status, c.Attempts, final, final, message, final, c.Attempts}, scrapeClaimArgs(c)...)
		res, err := tx.ExecContext(ctx, `UPDATE scrape_task AS q SET status=?,fail_count=?,progress=CASE WHEN ? THEN 100 ELSE progress END,finished_at=CASE WHEN ? THEN CURRENT_TIMESTAMP ELSE NULL END,message=?,lease_owner=NULL,lease_until=NULL,available_at=CASE WHEN ? THEN available_at ELSE datetime(CURRENT_TIMESTAMP, CASE ? WHEN 1 THEN '+5 seconds' ELSE '+30 seconds' END) END WHERE `+scrapeClaimPredicate(), args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrScrapeClaimLost
		}
		if c.StepID.Valid {
			stepStatus := "waiting"
			if final {
				stepStatus = "failed"
			}
			res, err = tx.ExecContext(ctx, `UPDATE media_ingest_step SET status=?,attempts=?,last_error=?,finished_at=CASE WHEN ? THEN CURRENT_TIMESTAMP ELSE NULL END,lease_owner=NULL,lease_until=NULL,available_at=(SELECT available_at FROM scrape_task WHERE id=?),updated_at=CURRENT_TIMESTAMP WHERE id=? AND run_id=? AND media_id=? AND generation=? AND status='running' AND lease_owner=? AND attempts=?`, stepStatus, c.Attempts, message, final, c.ID, c.StepID.Int64, c.RunID.Int64, c.MediaID, c.Generation.Int64, c.Owner, c.Attempts)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n != 1 {
				return ErrScrapeClaimLost
			}
			if err = publication.AggregateTx(ctx, tx, c.RunID.Int64); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO scrape_history(task_id,media_id,source,query,status,message) VALUES(?,?,?,?, 'failed',?)`, c.ID, c.MediaID, source, query, message)
		return err
	})
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
