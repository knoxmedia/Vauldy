package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"

	"knox-media/internal/coreiface"
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
	var stepType string
	err = h.App.DB.QueryRowContext(c.Request.Context(), `SELECT s.step_type FROM media_ingest_step s JOIN media m ON m.id=s.media_id AND m.ingest_generation=s.generation WHERE s.id=? AND s.media_id=?`, stepID, mediaID).Scan(&stepType)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusConflict, gin.H{"code": "no_retryable_work", "error": "no retryable work"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	switch stepType {
	case "scrape":
		err = publication.RetryOptionalScrape(c.Request.Context(), h.App.DB, publication.OptionalScrapeRetryRequest{MediaID: mediaID, StepID: stepID, ActorID: middleware.UserID(c), Reason: body.Reason}, h.PublicationCapabilities)
	case "preview", "subtitle":
		err = publication.RetryOptionalPostIngest(c.Request.Context(), h.App.DB, publication.OptionalPostIngestRetryRequest{MediaID: mediaID, StepID: stepID, ActorID: middleware.UserID(c), Reason: body.Reason})
	case "prepare":
		// Community builds omit prepare capability registration; RetryOptionalPrepare
		// fails closed with ErrPrepareCapabilityUnavailable when unavailable.
		err = publication.RetryOptionalPrepare(c.Request.Context(), h.App.DB, publication.OptionalPrepareRetryRequest{MediaID: mediaID, StepID: stepID, ActorID: middleware.UserID(c), Reason: body.Reason}, h.PublicationCapabilities)
	default:
		c.JSON(http.StatusConflict, gin.H{"code": "no_retryable_work", "error": "no retryable work"})
		return
	}
	switch {
	case errors.Is(err, publication.ErrInvalidRetryReason):
		c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_retry_reason", "error": "reason is required"})
	case errors.Is(err, publication.ErrScrapeCapabilityUnavailable):
		c.JSON(http.StatusConflict, gin.H{"code": "scrape_capability_unavailable", "error": "scrape capability unavailable"})
	case errors.Is(err, publication.ErrPrepareCapabilityUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "prepare_capability_unavailable", "error": "prepare capability unavailable"})
	case errors.Is(err, publication.ErrNoRetryableWork):
		c.JSON(http.StatusConflict, gin.H{"code": "no_retryable_work", "error": "no retryable work"})
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
	ctx := c.Request.Context()
	var generation int64
	var state, errorMessage, publishedAt string
	err = h.App.DB.QueryRowContext(ctx, `SELECT ingest_generation,publication_state,publication_error,COALESCE(published_at,'') FROM media WHERE id=?`, id).Scan(&generation, &state, &errorMessage, &publishedAt)
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
	var reason, runStatus, runError, created, updated, finished, terminalReason, snapshot string
	var preserve, policyVersion int
	err = h.App.DB.QueryRowContext(ctx, `SELECT id,generation,reason,status,preserve_visibility,error_message,COALESCE(created_at,''),COALESCE(updated_at,''),COALESCE(finished_at,''),COALESCE(policy_version,1),COALESCE(terminal_reason,''),COALESCE(config_snapshot_json,'') FROM media_ingest_run WHERE media_id=? AND generation=?`, id, generation).Scan(&runID, &runGeneration, &reason, &runStatus, &preserve, &runError, &created, &updated, &finished, &policyVersion, &terminalReason, &snapshot)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "current ingest run not found"})
		return
	}
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	rows, err := h.App.DB.QueryContext(ctx, `SELECT id,step_type,required,status,attempts,max_attempts,COALESCE(available_at,''),COALESCE(lease_owner,''),COALESCE(lease_until,''),last_error,COALESCE(created_at,''),COALESCE(updated_at,''),COALESCE(started_at,''),COALESCE(finished_at,'') FROM media_ingest_step WHERE run_id=? ORDER BY CASE step_type WHEN 'poster' THEN 1 WHEN 'thumbnail' THEN 2 WHEN 'scrape' THEN 3 WHEN 'preview' THEN 4 WHEN 'keyframe' THEN 5 WHEN 'subtitle' THEN 6 WHEN 'atrack' THEN 7 WHEN 'encrypt' THEN 8 WHEN 'prepare' THEN 9 ELSE 99 END,id`, runID)
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
	if err = rows.Err(); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	metadataDiagnostics := boundedMetadataDiagnostics(snapshot)
	dependencies := loadIngestDependencies(ctx, h.App.DB, runID)
	evidence := loadIngestEvidenceSummaries(ctx, h.App.DB, runID)
	adapterUnavailable := adapterUnavailableForSnapshot(h.PublicationCapabilities, snapshot, steps)
	recoveryErrors := loadUnresolvedRecoveryErrors(ctx, h.App.DB, id)

	c.JSON(http.StatusOK, gin.H{
		"media": gin.H{"id": id, "publication_state": state, "publication_error": errorMessage, "published_at": publishedAt, "ingest_generation": generation},
		"run": gin.H{
			"id": runID, "generation": runGeneration, "status": runStatus, "reason": reason, "preserve_visibility": preserve == 1,
			"error": runError, "created_at": created, "updated_at": updated, "finished_at": finished,
			"policy_version": policyVersion, "terminal_reason": terminalReason,
		},
		"steps": steps,
		"metadata_diagnostics": metadataDiagnostics, "dependencies": dependencies, "evidence": evidence,
		"adapter_unavailable": adapterUnavailable, "unresolved_recovery_errors": recoveryErrors,
	})
}

func boundedMetadataDiagnostics(raw string) []gin.H {
	var snap struct {
		Metadata publication.MetadataAttempt `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(raw), &snap); err != nil {
		return []gin.H{}
	}
	out := make([]gin.H, 0, len(snap.Metadata.Errors))
	total := 0
	for _, diag := range snap.Metadata.Errors {
		source := truncateUTF8Bound(strings.TrimSpace(diag.Source), maxPublicationDiagnosticMessage)
		message := truncateUTF8Bound(strings.TrimSpace(diag.Message), maxPublicationDiagnosticMessage)
		if source == "" && message == "" {
			continue
		}
		entryLen := len(source) + len(message)
		if len(out) >= maxPublicationMetadataErrorCount || total+entryLen > maxPublicationMetadataErrorsBytes {
			break
		}
		out = append(out, gin.H{"source": source, "message": message})
		total += entryLen
	}
	return out
}

func loadIngestDependencies(ctx context.Context, db *sql.DB, runID int64) []gin.H {
	rows, err := db.QueryContext(ctx, `
SELECT s.step_type, d.dependency_kind, COALESCE(ds.step_type,'')
FROM media_ingest_step_dependency d
JOIN media_ingest_step s ON s.id=d.step_id
LEFT JOIN media_ingest_step ds ON ds.id=d.depends_on_step_id
WHERE s.run_id=?
ORDER BY s.id, d.dependency_kind`, runID)
	if err != nil {
		return []gin.H{}
	}
	defer rows.Close()
	out := make([]gin.H, 0)
	for rows.Next() {
		var step, kind, dependsOn string
		if err := rows.Scan(&step, &kind, &dependsOn); err != nil {
			return []gin.H{}
		}
		item := gin.H{"step": step, "kind": kind}
		if dependsOn != "" {
			item["depends_on"] = dependsOn
		}
		out = append(out, item)
	}
	return out
}

func loadIngestEvidenceSummaries(ctx context.Context, db *sql.DB, runID int64) []gin.H {
	rows, err := db.QueryContext(ctx, `SELECT kind, stage_id, COALESCE(verified_at,'') FROM media_ingest_evidence WHERE run_id=? ORDER BY id LIMIT 50`, runID)
	if err != nil {
		return []gin.H{}
	}
	defer rows.Close()
	out := make([]gin.H, 0)
	for rows.Next() {
		var kind, stageID, verified string
		if err := rows.Scan(&kind, &stageID, &verified); err != nil {
			return []gin.H{}
		}
		out = append(out, gin.H{"kind": kind, "stage_id": stageID, "verified_at": verified})
	}
	return out
}

func adapterUnavailableForSnapshot(registry coreiface.CapabilityRegistry, snapshot string, steps []gin.H) []string {
	if registry == nil {
		return []string{}
	}
	names := optionalStepTypesFromSnapshot(snapshot)
	if len(names) == 0 {
		for _, step := range steps {
			required, _ := step["required"].(bool)
			typ, _ := step["type"].(string)
			if !required && typ != "" {
				names = append(names, typ)
			}
		}
	}
	out := make([]string, 0)
	seen := map[string]bool{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if !registry.Available(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func loadUnresolvedRecoveryErrors(ctx context.Context, db *sql.DB, mediaID int64) []string {
	rows, err := db.QueryContext(ctx, `
SELECT recovery_error FROM (
  SELECT recovery_error, updated_at FROM media_asset_stage_journal WHERE media_id=? AND TRIM(recovery_error)<>'' AND state<>'committed'
  UNION ALL
  SELECT recovery_error, updated_at FROM media_encryption_stage_journal WHERE media_id=? AND TRIM(recovery_error)<>'' AND state<>'committed'
  UNION ALL
  SELECT recovery_error, updated_at FROM poster_repair_stage WHERE media_id=? AND TRIM(recovery_error)<>'' AND state<>'committed'
) ORDER BY updated_at DESC LIMIT 8`, mediaID, mediaID, mediaID)
	if err != nil {
		return []string{}
	}
	defer rows.Close()
	out := make([]string, 0)
	total := 0
	for rows.Next() {
		var msg string
		if err := rows.Scan(&msg); err != nil {
			return out
		}
		msg = truncateUTF8Bound(strings.TrimSpace(msg), maxPublicationDiagnosticMessage)
		if msg == "" || total+len(msg) > maxPublicationMetadataErrorsBytes {
			continue
		}
		out = append(out, msg)
		total += len(msg)
	}
	return out
}
