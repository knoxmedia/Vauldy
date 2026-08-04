package handler

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/api/middleware"
	"knox-media/internal/taskcontrol"
)

// TaskControl holds the unified task control services.
type TaskControl struct {
	Registry *taskcontrol.Registry
	Queries  *taskcontrol.QueryService
	Mutations *taskcontrol.MutateService
	Overview  *taskcontrol.OverviewBuilder
	Stream    *taskcontrol.StreamBroker
}

// NewTaskControl creates a TaskControl from the taskcontrol services.
func NewTaskControl(db *sql.DB, registry *taskcontrol.Registry, builder *taskcontrol.ProjectionBuilder, stream *taskcontrol.StreamBroker) *TaskControl {
	return &TaskControl{
		Registry:  registry,
		Queries:   taskcontrol.NewQueryService(builder),
		Mutations: taskcontrol.NewMutateService(db),
		Overview:  taskcontrol.NewOverviewBuilder(builder),
		Stream:    stream,
	}
}

// --- Registry ---

// TaskControlRegistry returns the task type registry.
func (h *Handler) TaskControlRegistry(c *gin.Context) {
	if h.TaskCtrl == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "task_control_unavailable"})
		return
	}
	c.JSON(http.StatusOK, h.TaskCtrl.Registry)
}

// --- Overview ---

// TaskControlOverview returns the computed overview.
func (h *Handler) TaskControlOverview(c *gin.Context) {
	if h.TaskCtrl == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "task_control_unavailable"})
		return
	}
	overview, err := h.TaskCtrl.Overview.Compute(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "overview_failed"})
		return
	}
	c.JSON(http.StatusOK, overview)
}

// --- List ---

// TaskControlList returns a filtered, cursor-paginated list of tasks.
func (h *Handler) TaskControlList(c *gin.Context) {
	if h.TaskCtrl == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "task_control_unavailable"})
		return
	}

	filter := taskcontrol.QueryFilter{
		TaskType: c.Query("task_type"),
		Status:   c.Query("status"),
		Source:   c.Query("source"),
		Capability: c.Query("capability"),
		Owner:    c.Query("owner"),
		Blocker:  c.Query("blocker"),
		Removed:  c.DefaultQuery("removed", "exclude"),
	}
	if v := c.Query("library_id"); v != "" {
		var id int64
		if _, err := fmt.Sscanf(v, "%d", &id); err == nil {
			filter.LibraryID = &id
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_library_id"})
			return
		}
	}
	if v := c.Query("generation"); v != "" {
		var gen int64
		if _, err := fmt.Sscanf(v, "%d", &gen); err == nil {
			filter.Generation = &gen
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_generation"})
			return
		}
	}

	cursor := c.Query("cursor")
	limit := 50
	if v := c.Query("limit"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &limit); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_limit"})
			return
		}
	}

	result, err := h.TaskCtrl.Queries.List(c.Request.Context(), filter, cursor, limit)
	if err != nil {
		if strings.Contains(err.Error(), "invalid_cursor") || strings.Contains(err.Error(), "cursor_filter_mismatch") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_cursor", "message": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list_failed"})
		return
	}
	c.JSON(http.StatusOK, result)
}

// --- Detail ---

// TaskControlDetail returns the expanded detail for a single task.
func (h *Handler) TaskControlDetail(c *gin.Context) {
	if h.TaskCtrl == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "task_control_unavailable"})
		return
	}

	taskID := c.Param("task_id")
	result, err := h.TaskCtrl.Queries.Detail(c.Request.Context(), taskID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "detail_failed"})
		return
	}
	if result == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "task_not_found"})
		return
	}
	c.JSON(http.StatusOK, result)
}

// --- Actions ---

// taskActionBody is the JSON body for a single task action.
type taskActionBody struct {
	Action             string `json:"action"`
	ExpectedRevision   int64  `json:"expected_revision,omitempty"`
	ExpectedGeneration int64  `json:"expected_generation,omitempty"`
	ExpectedRetryRound int    `json:"expected_retry_round,omitempty"`
	Reason             string `json:"reason"`
}

// taskBatchBody is the JSON body for a batch operation.
type taskBatchBody struct {
	OperationID string                 `json:"operation_id"`
	Action      string                 `json:"action"`
	Reason      string                 `json:"reason"`
	Items       []taskcontrol.BatchItem `json:"items"`
}

// TaskControlActions performs a mutation action on a task.
func (h *Handler) TaskControlActions(c *gin.Context) {
	if h.TaskCtrl == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "task_control_unavailable"})
		return
	}

	taskID := c.Param("task_id")
	if taskID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing_task_id"})
		return
	}

	var body taskActionBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json"})
		return
	}

	if body.Reason == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing_reason"})
		return
	}

	actorID := middleware.UserID(c)

	var err error
	switch body.Action {
	case "abort":
		err = h.TaskCtrl.Mutations.AbortRequest(c.Request.Context(), taskcontrol.AbortRequestParams{
			TaskIdentity: taskID,
			ActorID:      actorID,
			Reason:       body.Reason,
		})
	case "cancel":
		err = h.TaskCtrl.Mutations.Cancel(c.Request.Context(), taskcontrol.CancelParams{
			TaskIdentity: taskID,
			ActorID:      actorID,
			Reason:       body.Reason,
		})
	case "remove":
		err = h.TaskCtrl.Mutations.Remove(c.Request.Context(), taskcontrol.RemoveParams{
			TaskIdentity:     taskID,
			ActorID:          actorID,
			Reason:           body.Reason,
			ExpectedRevision: body.ExpectedRevision,
		})
	case "reset":
		err = h.TaskCtrl.Mutations.Reset(c.Request.Context(), taskcontrol.ResetParams{
			TaskIdentity:       taskID,
			ActorID:            actorID,
			Reason:             body.Reason,
			ExpectedGeneration: body.ExpectedGeneration,
			ExpectedRetryRound: body.ExpectedRetryRound,
			ExpectedRevision:   body.ExpectedRevision,
		})
	case "run_now":
		err = h.TaskCtrl.Mutations.RunNow(c.Request.Context(), taskcontrol.RunNowParams{
			TaskIdentity: taskID,
			ActorID:      actorID,
			Reason:       body.Reason,
		})
	case "skip":
		err = h.TaskCtrl.Mutations.Skip(c.Request.Context(), taskcontrol.SkipParams{
			TaskIdentity: taskID,
			ActorID:      actorID,
			Reason:       body.Reason,
		})
	case "reopen":
		err = h.TaskCtrl.Mutations.Reopen(c.Request.Context(), taskcontrol.ReopenParams{
			TaskIdentity:       taskID,
			ActorID:            actorID,
			Reason:             body.Reason,
			ExpectedRetryRound: body.ExpectedRetryRound,
		})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown_action"})
		return
	}

	if err != nil {
		switch {
		case errors.Is(err, taskcontrol.ErrStaleRevision):
			c.JSON(http.StatusConflict, gin.H{"error": "stale_revision", "message": err.Error()})
		case errors.Is(err, taskcontrol.ErrGenerationMismatch),
			errors.Is(err, taskcontrol.ErrRetryRoundMismatch):
			c.JSON(http.StatusConflict, gin.H{"error": "conflict", "message": err.Error()})
		case errors.Is(err, taskcontrol.ErrInvalidOperation) ||
			errors.Is(err, taskcontrol.ErrNotRunning) ||
			errors.Is(err, taskcontrol.ErrNotTerminal) ||
			errors.Is(err, taskcontrol.ErrNotWaiting) ||
			errors.Is(err, taskcontrol.ErrNotAI):
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_operation", "message": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "action_failed", "message": err.Error()})
		}
		return
	}

	// Re-project the task to return the committed mutation row
	result, detailErr := h.TaskCtrl.Queries.Detail(c.Request.Context(), taskID)
	if detailErr != nil || result == nil {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "action": body.Action, "task_id": taskID})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"action":  body.Action,
		"task_id": taskID,
		"row":     result.Row,
	})
}

// --- Batch ---

// TaskControlBatch performs batch mutation actions.
func (h *Handler) TaskControlBatch(c *gin.Context) {
	if h.TaskCtrl == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "task_control_unavailable"})
		return
	}

	var body taskBatchBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json"})
		return
	}

	if body.OperationID == "" || body.Action == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing_operation_id_or_action"})
		return
	}

	actorID := middleware.UserID(c)
	result, err := h.TaskCtrl.Mutations.Batch(c.Request.Context(), taskcontrol.BatchParams{
		OperationID: body.OperationID,
		Action:      body.Action,
		ActorID:     actorID,
		Reason:      body.Reason,
		Items:       body.Items,
	})
	if err != nil {
		switch {
		case errors.Is(err, taskcontrol.ErrBatchTooLarge),
			errors.Is(err, taskcontrol.ErrBatchInvalidUUID),
			errors.Is(err, taskcontrol.ErrBatchActionMismatch):
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_batch", "message": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "batch_failed", "message": err.Error()})
		}
		return
	}

	c.JSON(http.StatusOK, result)
}

