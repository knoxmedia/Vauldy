package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"

	"knox-media/internal/publication"
)

type optionalScrapeRetryBody struct{ Reason string }

var validPublicationStates = map[string]bool{"processing": true, "published": true, "degraded": true, "failed": true, "cancelled": true}

func (h *Handler) AdminListMedia(c *gin.Context) {
	state := strings.TrimSpace(c.Query("publication_state"))
	if state != "" && !validPublicationStates[state] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid publication_state"})
		return
	}
	h.listMediaObserved(c, nil, state)
}

func (h *Handler) AdminGetMediaIngest(c *gin.Context) { h.writeAdminMediaIngest(c, 0) }

func (h *Handler) AdminRetryMediaIngest(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	err = publication.RetryIngest(c.Request.Context(), h.App.DB, id, h.PublicationPlanner)
	switch {
	case errors.Is(err, publication.ErrIngestNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "ingest not found"})
		return
	case errors.Is(err, publication.ErrNoRetryableWork):
		c.JSON(http.StatusConflict, gin.H{"error": "no retryable work"})
		return
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.writeAdminMediaIngest(c, id)
}

func (h *Handler) AdminRetryOptionalScrape(c *gin.Context) {
	mediaID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || mediaID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid media id"})
		return
	}
	stepID, err := strconv.ParseInt(c.Param("step_id"), 10, 64)
	if err != nil || stepID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid step id"})
		return
	}
	var body optionalScrapeRetryBody
	if err := json.NewDecoder(c.Request.Body).Decode(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	err = publication.RetryOptionalScrape(c.Request.Context(), h.App.DB, publication.OptionalScrapeRetryRequest{MediaID: mediaID, StepID: stepID, ActorID: middleware.UserID(c), Reason: body.Reason}, h.PublicationCapabilities)
	switch {
	case errors.Is(err, publication.ErrInvalidRetryReason):
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_retry_reason", "error": "reason is required"})
	case errors.Is(err, publication.ErrScrapeCapabilityUnavailable):
		c.JSON(http.StatusConflict, gin.H{"code": "scrape_capability_unavailable", "error": "scrape capability unavailable"})
	case errors.Is(err, publication.ErrNoRetryableWork):
		c.JSON(http.StatusConflict, gin.H{"code": "no_retryable_work", "error": "no retryable scrape work"})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusOK, gin.H{"ok": true, "media_id": mediaID, "step_id": stepID})
	}
}
func (h *Handler) writeAdminMediaIngest(c *gin.Context, knownID int64) {
	id := knownID
	var err error
	if id == 0 {
		id, err = strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil || id <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
			return
		}
	}
	var generation int64
	var state, errorMessage, publishedAt string
	err = h.App.DB.QueryRowContext(c.Request.Context(), `SELECT ingest_generation,publication_state,publication_error,COALESCE(published_at,'') FROM media WHERE id=?`, id).Scan(&generation, &state, &errorMessage, &publishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "media not found"})
		return
	}
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	var runID int64
	var runGeneration int64
	var reason, runStatus, runError, created, updated, finished string
	var preserve int
	err = h.App.DB.QueryRowContext(c.Request.Context(), `SELECT id,generation,reason,status,preserve_visibility,error_message,COALESCE(created_at,''),COALESCE(updated_at,''),COALESCE(finished_at,'') FROM media_ingest_run WHERE media_id=? AND generation=?`, id, generation).Scan(&runID, &runGeneration, &reason, &runStatus, &preserve, &runError, &created, &updated, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "current ingest run not found"})
		return
	}
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	rows, err := h.App.DB.QueryContext(c.Request.Context(), `SELECT id,step_type,required,status,attempts,max_attempts,COALESCE(available_at,''),COALESCE(lease_owner,''),COALESCE(lease_until,''),last_error,COALESCE(created_at,''),COALESCE(updated_at,''),COALESCE(started_at,''),COALESCE(finished_at,'') FROM media_ingest_step WHERE run_id=? ORDER BY CASE step_type WHEN 'poster' THEN 1 WHEN 'scrape' THEN 2 WHEN 'preview' THEN 3 WHEN 'keyframe' THEN 4 WHEN 'subtitle' THEN 5 WHEN 'atrack' THEN 6 WHEN 'encrypt' THEN 7 WHEN 'prepare' THEN 8 ELSE 99 END,id`, runID)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	steps := []gin.H{}
	for rows.Next() {
		var sid int64
		var typ, st, available, owner, lease, last, cr, up, start, finish string
		var req, attempts, max int
		if err = rows.Scan(&sid, &typ, &req, &st, &attempts, &max, &available, &owner, &lease, &last, &cr, &up, &start, &finish); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		steps = append(steps, gin.H{"id": sid, "type": typ, "required": req == 1, "status": st, "attempts": attempts, "max_attempts": max, "available_at": available, "lease_owner": owner, "lease_until": lease, "error": last, "created_at": cr, "updated_at": up, "started_at": start, "finished_at": finish})
	}
	c.JSON(http.StatusOK, gin.H{"media": gin.H{"id": id, "publication_state": state, "publication_error": errorMessage, "published_at": publishedAt, "ingest_generation": generation}, "run": gin.H{"id": runID, "generation": runGeneration, "status": runStatus, "reason": reason, "preserve_visibility": preserve == 1, "error": runError, "created_at": created, "updated_at": updated, "finished_at": finished}, "steps": steps})
}
