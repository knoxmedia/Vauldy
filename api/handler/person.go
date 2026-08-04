package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/internal/caststore"
	"knox-media/internal/scraper"
	"knox-media/internal/textencoding"
)

func personToJSON(p *caststore.Person) gin.H {
	if p == nil {
		return gin.H{}
	}
	return gin.H{
		"id":           p.ID,
		"name":         textencoding.FixMetadataString(p.Name),
		"name_norm":    p.NameNorm,
		"english_name": textencoding.FixMetadataString(p.EnglishName),
		"gender":       p.Gender,
		"birth_date":   p.BirthDate,
		"birth_place":  textencoding.FixMetadataString(p.BirthPlace),
		"nationality":  textencoding.FixMetadataString(p.Nationality),
		"occupations":  p.Occupations,
		"biography":    textencoding.FixMetadataString(p.Biography),
		"avatar_url":   p.AvatarURL,
		"aliases":      p.Aliases,
		"scraped":      p.Scraped,
		"scraped_at":   p.ScrapedAt,
		"tmdb_id":      p.TMDBID,
		"imdb_id":      p.IMDBID,
		"douban_id":    p.DoubanID,
		"work_count":   p.WorkCount,
		"created_at":   p.CreatedAt,
		"updated_at":   p.UpdatedAt,
	}
}

func linkToJSON(l caststore.MediaPersonLink) gin.H {
	return gin.H{
		"id":             l.ID,
		"media_id":       l.MediaID,
		"person_id":      l.PersonID,
		"person_name":    textencoding.FixMetadataString(l.PersonName),
		"avatar_url":     l.AvatarURL,
		"occupation":     l.Occupation,
		"character_name": textencoding.FixMetadataString(l.CharacterName),
		"role_type":      l.RoleType,
		"sort_order":     l.SortOrder,
		"media_title":    textencoding.FixMetadataString(l.MediaTitle),
		"media_year":     l.MediaYear,
		"poster_url":     l.PosterURL,
	}
}

// ListPersons returns paginated cast/crew persons.
func (h *Handler) ListPersons(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "48"))
	opt := caststore.ListPersonsOptions{
		Search:     c.Query("q"),
		Occupation: c.Query("occupation"),
		Scraped:    c.Query("scraped"),
		Sort:       c.Query("sort"),
		Page:       page,
		PageSize:   pageSize,
	}
	items, total, err := caststore.ListPersons(h.App.DB, opt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]gin.H, 0, len(items))
	for i := range items {
		out = append(out, personToJSON(&items[i]))
	}
	c.JSON(http.StatusOK, gin.H{"items": out, "total": total, "page": opt.Page, "page_size": opt.PageSize})
}

// GetPerson returns person detail.
func (h *Handler) GetPerson(c *gin.Context) {
	personID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || personID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid person id"})
		return
	}
	p, err := caststore.GetPerson(h.App.DB, personID)
	if err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	occCounts, _ := caststore.OccupationCounts(h.App.DB, personID)
	resp := personToJSON(p)
	resp["occupation_counts"] = occCounts
	c.JSON(http.StatusOK, resp)
}

type createPersonBody struct {
	Name        string   `json:"name"`
	EnglishName string   `json:"english_name"`
	Gender      *int     `json:"gender"`
	BirthDate   string   `json:"birth_date"`
	BirthPlace  string   `json:"birth_place"`
	Nationality string   `json:"nationality"`
	Occupations []string `json:"occupations"`
	Biography   string   `json:"biography"`
	AvatarURL   string   `json:"avatar_url"`
	Aliases     string   `json:"aliases"`
	TMDBID      string   `json:"tmdb_id"`
	IMDBID      string   `json:"imdb_id"`
	DoubanID    string   `json:"douban_id"`
}

// CreatePerson adds a new cast/crew person (admin).
func (h *Handler) CreatePerson(c *gin.Context) {
	var body createPersonBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name required"})
		return
	}
	avatar := strings.TrimSpace(body.AvatarURL)
	id, err := caststore.CreatePerson(h.App.DB, caststore.PersonPatch{
		Name:        name,
		EnglishName: body.EnglishName,
		Gender:      body.Gender,
		BirthDate:   body.BirthDate,
		BirthPlace:  body.BirthPlace,
		Nationality: body.Nationality,
		Occupations: body.Occupations,
		Biography:   body.Biography,
		AvatarURL:   avatar,
		Aliases:     body.Aliases,
		TMDBID:      body.TMDBID,
		IMDBID:      body.IMDBID,
		DoubanID:    body.DoubanID,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if avatar != "" {
		cached := h.materializePersonAvatar(id, avatar)
		if cached != "" {
			_, _ = h.App.DB.Exec(`UPDATE cast_person SET avatar_url = ? WHERE id = ?`, cached, id)
		}
	}
	p, _ := caststore.GetPerson(h.App.DB, id)
	c.JSON(http.StatusOK, personToJSON(p))
}

// UpdatePerson updates person metadata (admin).
func (h *Handler) UpdatePerson(c *gin.Context) {
	personID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || personID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid person id"})
		return
	}
	var body createPersonBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	avatar := strings.TrimSpace(body.AvatarURL)
	if avatar != "" {
		avatar = h.materializePersonAvatar(personID, avatar)
	}
	if err := caststore.UpdatePerson(h.App.DB, personID, caststore.PersonPatch{
		Name:        body.Name,
		EnglishName: body.EnglishName,
		Gender:      body.Gender,
		BirthDate:   body.BirthDate,
		BirthPlace:  body.BirthPlace,
		Nationality: body.Nationality,
		Occupations: body.Occupations,
		Biography:   body.Biography,
		AvatarURL:   avatar,
		Aliases:     body.Aliases,
		TMDBID:      body.TMDBID,
		IMDBID:      body.IMDBID,
		DoubanID:    body.DoubanID,
	}); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	p, _ := caststore.GetPerson(h.App.DB, personID)
	c.JSON(http.StatusOK, personToJSON(p))
}

type deletePersonBody struct {
	RemoveLinks bool `json:"remove_links"`
}

// DeletePerson soft-deletes a person (admin).
func (h *Handler) DeletePerson(c *gin.Context) {
	personID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || personID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid person id"})
		return
	}
	var body deletePersonBody
	_ = c.ShouldBindJSON(&body)
	linkCount, _ := caststore.CountMediaLinks(h.App.DB, personID)
	if linkCount > 0 && !body.RemoveLinks {
		c.JSON(http.StatusConflict, gin.H{
			"error":      "person has media links",
			"link_count": linkCount,
			"hint":       "set remove_links=true to delete and unlink",
		})
		return
	}
	if err := caststore.SoftDeletePerson(h.App.DB, personID, body.RemoveLinks); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ListPersonWorks returns filmography.
func (h *Handler) ListPersonWorks(c *gin.Context) {
	personID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || personID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid person id"})
		return
	}
	occupation := c.Query("occupation")
	works, err := caststore.ListPersonWorks(h.App.DB, personID, occupation)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	items := make([]gin.H, 0, len(works))
	for _, w := range works {
		items = append(items, linkToJSON(w))
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// ListPersonCollaborators returns collaboration network.
func (h *Handler) ListPersonCollaborators(c *gin.Context) {
	personID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || personID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid person id"})
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	collabs, err := caststore.ListCollaborators(h.App.DB, personID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	items := make([]gin.H, 0, len(collabs))
	for _, col := range collabs {
		items = append(items, gin.H{
			"person_id":           col.PersonID,
			"name":                textencoding.FixMetadataString(col.Name),
			"avatar_url":          col.AvatarURL,
			"collaboration_count": col.CollaborationCount,
			"recent_movie_titles": col.RecentMovieTitles,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// ListMediaPersons returns cast/crew for a media item.
func (h *Handler) ListMediaPersons(c *gin.Context) {
	mediaID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || mediaID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	var libID int64
	if err := h.App.DB.QueryRow(`SELECT library_id FROM media WHERE id = ?`, mediaID).Scan(&libID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if !h.requireLibraryAccess(c, libID) {
		return
	}
	var meta sql.NullString
	_ = h.App.DB.QueryRow(`SELECT meta_json FROM media WHERE id = ?`, mediaID).Scan(&meta)
	_, _ = caststore.BackfillFromMetaJSON(h.App.DB, mediaID, meta.String)
	links, err := caststore.ListMediaPersons(h.App.DB, mediaID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	resolved := caststore.LookupPersonsByNames(h.App.DB, caststore.CollectMetaPersonNames(meta.String))
	items := make([]gin.H, 0, len(links))
	for _, l := range links {
		items = append(items, linkToJSON(l))
	}
	resolvedOut := make([]gin.H, 0, len(resolved))
	for _, ref := range resolved {
		resolvedOut = append(resolvedOut, gin.H{
			"person_id":   ref.ID,
			"person_name": textencoding.FixMetadataString(ref.Name),
			"avatar_url":  ref.AvatarURL,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "resolved": resolvedOut})
}

type mediaPersonBody struct {
	PersonID      int64  `json:"person_id"`
	Name          string `json:"name"`
	Occupation    string `json:"occupation"`
	CharacterName string `json:"character_name"`
	RoleType      string `json:"role_type"`
	SortOrder     int    `json:"sort_order"`
}

// AddMediaPerson links a person to media (admin).
func (h *Handler) AddMediaPerson(c *gin.Context) {
	mediaID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || mediaID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	var body mediaPersonBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	personID := body.PersonID
	if personID <= 0 {
		name := strings.TrimSpace(body.Name)
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "person_id or name required"})
			return
		}
		personID, err = caststore.FindOrCreateByName(h.App.DB, name)
		if err != nil || personID <= 0 {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "create person failed"})
			return
		}
	}
	occ := strings.TrimSpace(body.Occupation)
	if occ == "" {
		occ = caststore.OccActor
	}
	if err := caststore.LinkMediaPerson(h.App.DB, mediaID, personID, occ, body.CharacterName, body.RoleType, body.SortOrder); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	caststore.MergePersonOccupations(h.App.DB, personID, occ)
	c.JSON(http.StatusOK, gin.H{"ok": true, "person_id": personID})
}

// UpdateMediaPersonLink updates an association (admin).
func (h *Handler) UpdateMediaPersonLink(c *gin.Context) {
	linkID, err := strconv.ParseInt(c.Param("linkId"), 10, 64)
	if err != nil || linkID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid link id"})
		return
	}
	var body mediaPersonBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	occ := strings.TrimSpace(body.Occupation)
	if occ == "" {
		occ = caststore.OccActor
	}
	_, err = h.App.DB.Exec(`
		UPDATE media_person SET
			occupation = ?,
			character_name = ?,
			role_type = ?,
			sort_order = ?,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, occ, strings.TrimSpace(body.CharacterName), strings.TrimSpace(body.RoleType), body.SortOrder, linkID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// DeleteMediaPersonLink removes an association (admin).
func (h *Handler) DeleteMediaPersonLink(c *gin.Context) {
	linkID, err := strconv.ParseInt(c.Param("linkId"), 10, 64)
	if err != nil || linkID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid link id"})
		return
	}
	_, err = h.App.DB.Exec(`DELETE FROM media_person WHERE id = ?`, linkID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// SearchPersonCandidates searches external sources for person scrape matches.
func (h *Handler) SearchPersonCandidates(c *gin.Context) {
	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "q required"})
		return
	}
	source := strings.ToLower(strings.TrimSpace(c.DefaultQuery("source", "tmdb")))
	language := c.DefaultQuery("language", "zh-CN")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	cfg := h.readLibraryScrapeConfig(0)
	switch source {
	case "tmdb":
		items, err := scraper.SearchPersonCandidates(query, language, cfg.APIKeys["tmdb"], limit)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": items})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported source"})
	}
}

type applyPersonScrapeBody struct {
	Source     string `json:"source"`
	ExternalID string `json:"external_id"`
	Language   string `json:"language"`
}

// ApplyPersonScrape fetches and applies external person profile (admin).
func (h *Handler) ApplyPersonScrape(c *gin.Context) {
	personID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || personID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid person id"})
		return
	}
	var body applyPersonScrapeBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	source := strings.ToLower(strings.TrimSpace(body.Source))
	if source == "" {
		source = "tmdb"
	}
	cfg := h.readLibraryScrapeConfig(0)
	var profile *scraper.PersonProfile
	switch source {
	case "tmdb":
		profile, err = scraper.FetchPersonByTMDBID(body.ExternalID, body.Language, cfg.APIKeys["tmdb"])
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported source"})
		return
	}
	if err != nil || profile == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if profile.AvatarURL != "" {
		profile.AvatarURL = h.materializePersonAvatar(personID, profile.AvatarURL)
	}
	if err := caststore.ApplyProfileFromScraper(h.App.DB, personID, profile, true); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	caststore.MarkScraped(h.App.DB, personID)
	p, _ := caststore.GetPerson(h.App.DB, personID)
	c.JSON(http.StatusOK, personToJSON(p))
}

// SearchCastPersons searches local person registry.
func (h *Handler) SearchCastPersons(c *gin.Context) {
	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		c.JSON(http.StatusOK, gin.H{"items": []any{}})
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	items, err := caststore.SearchPersons(h.App.DB, query, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]gin.H, 0, len(items))
	for i := range items {
		out = append(out, personToJSON(&items[i]))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// GetCastStats returns aggregate statistics (admin).
func (h *Handler) GetCastStats(c *gin.Context) {
	stats, err := caststore.StatsSummary(h.App.DB)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, stats)
}

// ListPersonScrapeTasks returns person scrape task queue.
func (h *Handler) ListPersonScrapeTasks(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := h.App.DB.Query(`
		SELECT id, COALESCE(person_id,0), COALESCE(source,''), COALESCE(status,''),
			COALESCE(query,''), COALESCE(external_id,''), COALESCE(error_message,''),
			started_at, completed_at, created_at
		FROM person_scrape_task
		ORDER BY id DESC LIMIT ?
	`, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0)
	for rows.Next() {
		var id, personID sql.NullInt64
		var source, status, query, extID, errMsg, started, completed, created sql.NullString
		if rows.Scan(&id, &personID, &source, &status, &query, &extID, &errMsg, &started, &completed, &created) != nil {
			continue
		}
		items = append(items, gin.H{
			"id": id.Int64, "person_id": personID.Int64, "source": source.String,
			"status": status.String, "query": query.String, "external_id": extID.String,
			"error_message": errMsg.String, "started_at": started.String,
			"completed_at": completed.String, "created_at": created.String,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

type enqueuePersonScrapeBody struct {
	PersonID   int64  `json:"person_id"`
	Query      string `json:"query"`
	Source     string `json:"source"`
	ExternalID string `json:"external_id"`
}

// EnqueuePersonScrapeTask adds a person scrape task (admin).
func (h *Handler) EnqueuePersonScrapeTask(c *gin.Context) {
	var body enqueuePersonScrapeBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	source := strings.TrimSpace(body.Source)
	if source == "" {
		source = "tmdb"
	}
	res, err := h.App.DB.Exec(`
		INSERT INTO person_scrape_task (person_id, source, status, query, external_id)
		VALUES (?, ?, 'pending', ?, ?)
	`, nullIfZero64(body.PersonID), source, strings.TrimSpace(body.Query), strings.TrimSpace(body.ExternalID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	id, _ := res.LastInsertId()
	c.JSON(http.StatusOK, gin.H{"ok": true, "task_id": id})
}

// ImportMediaCreditsFromTMDB triggers TMDB credits import for a media item (admin).
func (h *Handler) ImportMediaCreditsFromTMDB(c *gin.Context) {
	mediaID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || mediaID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	var metaJSON, libraryType sql.NullString
	var libID int64
	if err := h.App.DB.QueryRow(`
		SELECT m.meta_json, m.library_id, COALESCE(l.type,'')
		FROM media m LEFT JOIN library l ON l.id = m.library_id
		WHERE m.id = ?
	`, mediaID).Scan(&metaJSON, &libID, &libraryType); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	tmdbID, mediaType := extractTMDBIDFromMeta(metaJSON.String, libraryType.String)
	if tmdbID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no tmdb_id in media metadata"})
		return
	}
	cfg := h.readLibraryScrapeConfig(libID)
	credits, err := scraper.FetchTMDBCredits(tmdbID, mediaType, "zh-CN", cfg.APIKeys["tmdb"])
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	avatarBase := scraper.GetTMDBImageBase() + "/t/p/w185"
	n, err := caststore.ImportCredits(h.App.DB, mediaID, credits, avatarBase)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "imported": n})
}

// RescrapeAllMediaCredits batch-imports TMDB credits for all media in a library
// that have a tmdb_id in meta_json but no cast/crew links yet (admin).
func (h *Handler) RescrapeAllMediaCredits(c *gin.Context) {
	libraryID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || libraryID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid library id"})
		return
	}
	cfg := h.readLibraryScrapeConfig(libraryID)
	apiKey := cfg.APIKeys["tmdb"]
	if strings.TrimSpace(apiKey) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tmdb api key not configured"})
		return
	}

	var libraryType string
	_ = h.App.DB.QueryRow(`SELECT COALESCE(type,'') FROM library WHERE id = ?`, libraryID).Scan(&libraryType)

	rows, err := h.App.DB.Query(`
		SELECT m.id, m.meta_json
		FROM media m
		WHERE m.library_id = ? AND m.status = 'active'
	`, libraryID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	type mediaMeta struct {
		ID       int64
		MetaJSON string
	}
	var items []mediaMeta
	for rows.Next() {
		var m mediaMeta
		if rows.Scan(&m.ID, &m.MetaJSON) == nil {
			items = append(items, m)
		}
	}

	avatarBase := scraper.GetTMDBImageBase() + "/t/p/w185"
	imported, skipped, failed := 0, 0, 0
	for _, m := range items {
		// Skip media that already have cast links
		if caststore.HasMediaPersonLinks(h.App.DB, m.ID) {
			skipped++
			continue
		}
		tmdbID, mediaType := extractTMDBIDFromMeta(m.MetaJSON, libraryType)
		if tmdbID == "" {
			skipped++
			continue
		}
		credits, err := scraper.FetchTMDBCredits(tmdbID, mediaType, "zh-CN", apiKey)
		if err != nil {
			failed++
			continue
		}
		n, err := caststore.ImportCredits(h.App.DB, m.ID, credits, avatarBase)
		if err != nil {
			failed++
			continue
		}
		if n > 0 {
			imported++
		} else {
			skipped++
		}
		// Rate limit: 200-400ms between requests
		time.Sleep(time.Duration(200+rand.Intn(200)) * time.Millisecond)
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":       true,
		"imported": imported,
		"skipped":  skipped,
		"failed":   failed,
		"total":    len(items),
	})
}

func extractTMDBIDFromMeta(metaJSON, libraryType string) (tmdbID, mediaType string) {
	metaJSON = strings.TrimSpace(metaJSON)
	if metaJSON == "" {
		return "", "movie"
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
		return "", "movie"
	}
	scrape, _ := meta["scrape"].(map[string]any)
	if scrape == nil {
		return "", "movie"
	}
	extra, _ := scrape["extra"].(map[string]any)
	if extra != nil {
		if v, ok := extra["tmdb_id"]; ok {
			tmdbID = strings.TrimSpace(fmt.Sprint(v))
		}
		if mt, ok := extra["media_type"].(string); ok && mt != "" {
			mediaType = mt
		}
	}
	if tmdbID == "" {
		if v, ok := scrape["tmdb_id"]; ok {
			tmdbID = strings.TrimSpace(fmt.Sprint(v))
		}
	}
	if mediaType == "" {
		switch strings.ToLower(strings.TrimSpace(libraryType)) {
		case "tv", "anime":
			mediaType = "tv"
		default:
			mediaType = "movie"
		}
	}
	return tmdbID, mediaType
}

func nullIfZero64(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}
