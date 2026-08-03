package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
	taskscheduler "knox-media/internal/scheduler"
)

// schedulerPolicyPayload is the wire shape for the effective scheduler policy.
type schedulerPolicyPayload struct {
	TypeConcurrency  map[string]int    `json:"type_concurrency"`
	ResourceCapacity map[string]int    `json:"resource_capacity"`
	ProviderCapacity map[string]int    `json:"provider_capacity"`
	AgingIntervalSec int               `json:"aging_interval_sec"`
	AgingStep        int               `json:"aging_step"`
	RunNowAmount     int               `json:"run_now_amount"`
	RunNowTTLSec     int               `json:"run_now_ttl_sec"`
	Provenance       map[string]string `json:"provenance"`
}

// schedulerPolicyStatusPayload is the response shape for policy reads/writes.
type schedulerPolicyStatusPayload struct {
	Revision int64                  `json:"revision"`
	Active   bool                   `json:"active"`
	Actor    string                 `json:"actor"`
	Reason   string                 `json:"reason"`
	Policy   schedulerPolicyPayload `json:"policy"`
}

func schedulerPolicyPayloadFrom(p taskscheduler.Policy) schedulerPolicyPayload {
	rc := make(map[string]int, len(p.ResourceCapacity))
	for rk, cap := range p.ResourceCapacity {
		rc[string(rk)] = cap
	}
	return schedulerPolicyPayload{
		TypeConcurrency:  p.TypeConcurrency,
		ResourceCapacity: rc,
		ProviderCapacity: p.ProviderCapacity,
		AgingIntervalSec: p.AgingIntervalSec,
		AgingStep:        p.AgingStep,
		RunNowAmount:     p.RunNowAmount,
		RunNowTTLSec:     p.RunNowTTLSec,
		Provenance:       p.Provenance,
	}
}

// SchedulerAdminGetPolicy returns the active policy revision provenance and the
// effective policy with per-value provenance (override|yaml|default).
func (h *Handler) SchedulerAdminGetPolicy(c *gin.Context) {
	svc := h.SchedulerAdmin
	if svc == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "scheduler_admin_unavailable"})
		return
	}
	status, err := svc.PolicyStatus(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "policy_status_failed"})
		return
	}
	c.JSON(http.StatusOK, schedulerPolicyStatusPayload{
		Revision: status.Revision,
		Active:   status.Active,
		Actor:    status.Actor,
		Reason:   status.Reason,
		Policy:   schedulerPolicyPayloadFrom(status.Policy),
	})
}

// SchedulerAdminPutPolicy fully replaces the runtime override layer
// (concurrency overrides absent from the payload are cleared) using a
// revisioned write.
func (h *Handler) SchedulerAdminPutPolicy(c *gin.Context) {
	h.applyRuntimeOverride(c, true)
}

// SchedulerAdminPatchPolicy merges runtime override changes into the active
// policy using a revisioned write.
func (h *Handler) SchedulerAdminPatchPolicy(c *gin.Context) {
	h.applyRuntimeOverride(c, false)
}

func (h *Handler) applyRuntimeOverride(c *gin.Context, replace bool) {
	svc := h.SchedulerAdmin
	if svc == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "scheduler_admin_unavailable"})
		return
	}
	var body struct {
		ExpectedRevision int64             `json:"expected_revision"`
		Concurrency      map[string]int    `json:"concurrency"`
		ClearConcurrency []string          `json:"clear_concurrency"`
		ResourceCapacity map[string]int    `json:"resource_capacity"`
		ProviderCapacity map[string]int    `json:"provider_capacity"`
		AgingIntervalSec *int              `json:"aging_interval_sec"`
		AgingStep        *int              `json:"aging_step"`
		RunNowAmount     *int              `json:"run_now_amount"`
		RunNowTTLSec     *int              `json:"run_now_ttl_sec"`
		Reason           string            `json:"reason"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json"})
		return
	}
	actor := middleware.Username(c)
	_, err := svc.ApplyRuntimeOverride(c.Request.Context(), taskscheduler.RuntimeOverrideRequest{
		ExpectedRevision: body.ExpectedRevision,
		Replace:          replace,
		Concurrency:      body.Concurrency,
		ClearConcurrency: body.ClearConcurrency,
		ResourceCapacity: body.ResourceCapacity,
		ProviderCapacity: body.ProviderCapacity,
		AgingIntervalSec: body.AgingIntervalSec,
		AgingStep:        body.AgingStep,
		RunNowAmount:     body.RunNowAmount,
		RunNowTTLSec:     body.RunNowTTLSec,
		Author:           actor,
		Reason:           body.Reason,
	})
	if err != nil {
		var conflict taskscheduler.RevisionConflictError
		if errors.As(err, &conflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "revision_conflict", "expected_revision": conflict.Expected, "current_revision": conflict.Current})
			return
		}
		var validation taskscheduler.ValidationError
		if errors.As(err, &validation) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "validation_errors": validation.Errors})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "apply_failed"})
		return
	}
	status, err := svc.PolicyStatus(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "policy_status_failed"})
		return
	}
	// The response reflects the durably activated revision, not merely the
	// accepted write.
	c.JSON(http.StatusOK, schedulerPolicyStatusPayload{
		Revision: status.Revision,
		Active:   status.Active,
		Actor:    status.Actor,
		Reason:   status.Reason,
		Policy:   schedulerPolicyPayloadFrom(status.Policy),
	})
}

// SchedulerAdminControl applies a pause/resume/drain control command. Repeated
// identical commands are idempotent; a stale expected_revision returns 409.
func (h *Handler) SchedulerAdminControl(c *gin.Context) {
	svc := h.SchedulerAdmin
	if svc == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "scheduler_admin_unavailable"})
		return
	}
	var body struct {
		TaskType         string `json:"task_type"`
		Command          string `json:"command"`
		ExpectedRevision *int64 `json:"expected_revision"`
		Reason           string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json"})
		return
	}
	expected := int64(-1)
	if body.ExpectedRevision != nil {
		expected = *body.ExpectedRevision
	}
	res, err := svc.Control(c.Request.Context(), body.TaskType, body.Command, expected, middleware.Username(c), body.Reason)
	if err != nil {
		var cc taskscheduler.ControlConflictError
		if errors.As(err, &cc) {
			c.JSON(http.StatusConflict, gin.H{"error": "revision_conflict", "expected_revision": cc.Expected, "current_revision": cc.Current})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "control_failed", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"task_type":         res.TaskType,
		"state":             res.State,
		"revision":          res.Revision,
		"live_reservations": res.LiveReservations,
		"drained":           res.Drained,
		"actor":             res.Actor,
		"reason":            res.Reason,
	})
}
