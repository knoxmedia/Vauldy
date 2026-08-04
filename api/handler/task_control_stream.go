package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// TaskControlStream serves an SSE stream of task projection events.
// The stream sends an initial snapshot followed by delta events and
// periodic heartbeats. Supports Last-Event-ID resume via since_revision.
func (h *Handler) TaskControlStream(c *gin.Context) {
	if h.TaskCtrl == nil || h.TaskCtrl.Stream == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "task_control_unavailable"})
		return
	}

	w := c.Writer
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	// Parse since_revision query parameter for Last-Event-ID resume
	sinceRevision := int64(0)
	if v := c.Query("since_revision"); v != "" {
		if rev, err := strconv.ParseInt(v, 10, 64); err == nil {
			sinceRevision = rev
		}
	}

	sub, err := h.TaskCtrl.Stream.Subscribe(c.Request.Context(), sinceRevision)
	if err != nil {
		c.Status(http.StatusServiceUnavailable)
		return
	}
	defer h.TaskCtrl.Stream.Unsubscribe(sub.ID)

	// Stream events to the client
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case evt, ok := <-sub.Events:
			if !ok {
				return
			}
			if !writeSSEEvent(w, evt) {
				return
			}
		case <-sub.Done:
			return
		}
	}
}

// writeSSEEvent writes a single SSE event frame to the response writer.
func writeSSEEvent(w gin.ResponseWriter, evt interface{}) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			ok = false
		}
	}()

	type sseEvent struct {
		ID       int64       `json:"id"`
		Revision int64       `json:"revision"`
		Type     string      `json:"type"`
		Payload  interface{} `json:"payload,omitempty"`
	}

	// Convert the event
	var se sseEvent
	switch e := evt.(type) {
	case sseEvent:
		se = e
	default:
		// Try to marshal as generic event
		data, err := json.Marshal(evt)
		if err != nil {
			return false
		}
		if _, err := w.Write([]byte("data: " + string(data) + "\n\n")); err != nil {
			return false
		}
		return flushSSE(w) == nil
	}

	data, err := json.Marshal(se)
	if err != nil {
		return false
	}

	frame := "data: " + string(data) + "\n"
	if se.ID > 0 {
		frame = "id: " + strconv.FormatInt(se.ID, 10) + "\n" + frame
	}
	if se.Type == "heartbeat" && se.Payload == nil {
		frame = ": heartbeat\n\n"
		if _, err := w.Write([]byte(frame)); err != nil {
			return false
		}
		return flushSSE(w) == nil
	}

	frame += "\n"
	if _, err := w.Write([]byte(frame)); err != nil {
		return false
	}

	if err := flushSSE(w); err != nil {
		return false
	}
	return true
}

// flushSSE flushes the response writer after checking for a deadline.
func flushSSE(w gin.ResponseWriter) error {
	// Check write deadline before flushing
	if err := checkWriteDeadline(w); err != nil {
		return err
	}
	return flushResponse(w)
}

// checkWriteDeadline checks if the underlying connection has a write deadline set.
func checkWriteDeadline(w gin.ResponseWriter) error {
	type deadlineWriter interface {
		WriteDeadline() (time.Time, bool)
	}
	if dw, ok := w.(deadlineWriter); ok {
		if deadline, set := dw.WriteDeadline(); set && time.Now().After(deadline) {
			return http.ErrHandlerTimeout
		}
	}
	return nil
}
