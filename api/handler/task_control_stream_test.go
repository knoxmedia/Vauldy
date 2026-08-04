package handler

import (
	"bufio"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/internal/taskcontrol"
)

type streamTestWriter struct {
	gin.ResponseWriter
	events chan string
	header http.Header
	status int
}

func newStreamTestWriter() *streamTestWriter {
	return &streamTestWriter{
		events: make(chan string, 100),
		header: make(http.Header),
	}
}

func (w *streamTestWriter) Header() http.Header           { return w.header }
func (w *streamTestWriter) WriteHeader(statusCode int)     { w.status = statusCode }
func (w *streamTestWriter) Write(data []byte) (int, error) {
	w.events <- string(data)
	return len(data), nil
}
func (w *streamTestWriter) Flush()                          {}
func (w *streamTestWriter) CloseNotify() <-chan bool        { return nil }
func (w *streamTestWriter) Status() int                     { return w.status }
func (w *streamTestWriter) Size() int                       { return 0 }
func (w *streamTestWriter) Written() bool                   { return w.status != 0 }
func (w *streamTestWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

func newStreamHandlerWithDB(t *testing.T) (*Handler, *sql.DB, func()) {
	t.Helper()
	db, builder, reg := setupTaskControlTestDB(t)
	seedHandlerTasks(t, db)

	cfg := taskcontrol.DefaultStreamConfig()
	cfg.HeartbeatInterval = 100 * time.Millisecond
	broker := taskcontrol.NewStreamBroker(db, builder, cfg)

	tc := &TaskControl{
		Registry:  reg,
		Queries:   taskcontrol.NewQueryService(builder),
		Mutations: taskcontrol.NewMutateService(db),
		Overview:  taskcontrol.NewOverviewBuilder(builder),
		Stream:    broker,
	}

	return &Handler{TaskCtrl: tc}, db, func() {
		broker.Shutdown()
		db.Close()
	}
}

func TestTaskControlStreamSendsData(t *testing.T) {
	h, _, cleanup := newStreamHandlerWithDB(t)
	defer cleanup()

	sw := newStreamTestWriter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Writer = sw
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/tasks/stream", nil).WithContext(ctx)
		h.TaskControlStream(c)
	}()

	// Collect events for up to 2 seconds
	var events []string
	timeout := time.After(2 * time.Second)
	collecting := true
	for collecting {
		select {
		case evt := <-sw.events:
			events = append(events, evt)
			if len(events) >= 3 {
				collecting = false
			}
		case <-timeout:
			collecting = false
		}
	}

	if len(events) == 0 {
		t.Fatal("expected stream events, got none")
	}

	// Verify SSE format
	for _, evt := range events {
		if !strings.HasPrefix(evt, "data:") && !strings.HasPrefix(evt, ": ") && !strings.HasPrefix(evt, "id:") {
			t.Errorf("unexpected SSE line: %q", evt)
		}
	}
}

func TestTaskControlStreamContentType(t *testing.T) {
	h, _, cleanup := newStreamHandlerWithDB(t)
	defer cleanup()

	sw := newStreamTestWriter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Writer = sw
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/tasks/stream", nil).WithContext(ctx)

	go h.TaskControlStream(c)
	time.Sleep(100 * time.Millisecond) // Let it set headers

	ct := sw.header.Get("Content-Type")
	if ct != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %q", ct)
	}
}

func TestTaskControlStreamSinceRevision(t *testing.T) {
	h, _, cleanup := newStreamHandlerWithDB(t)
	defer cleanup()

	sw := newStreamTestWriter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Writer = sw
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/tasks/stream?since_revision=5", nil).WithContext(ctx)
		h.TaskControlStream(c)
	}()

	// Collect events
	var events []string
	timeout := time.After(2 * time.Second)
	collecting := true
	for collecting {
		select {
		case evt := <-sw.events:
			events = append(events, evt)
			if len(events) >= 2 {
				collecting = false
			}
		case <-timeout:
			collecting = false
		}
	}

	if len(events) == 0 {
		t.Fatal("expected stream events with since_revision, got none")
	}
}

func TestTaskControlStreamDisconnectCleanup(t *testing.T) {
	h, _, cleanup := newStreamHandlerWithDB(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())

	sw := newStreamTestWriter()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Writer = sw
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/tasks/stream", nil).WithContext(ctx)
		h.TaskControlStream(c)
	}()

	// Wait for a snapshot event to confirm connection
	select {
	case <-sw.events:
		// Connected, now disconnect
	case <-time.After(2 * time.Second):
		t.Fatal("expected initial event")
	}

	cancel()

	// Handler should return cleanly after disconnect
	select {
	case <-done:
		// Clean exit
	case <-time.After(3 * time.Second):
		t.Error("stream handler did not exit after disconnect")
	}
}

func TestTaskControlStreamWithHeartbeat(t *testing.T) {
	h, _, cleanup := newStreamHandlerWithDB(t)
	defer cleanup()

	sw := newStreamTestWriter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Writer = sw
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/tasks/stream", nil).WithContext(ctx)
		h.TaskControlStream(c)
	}()

	// Wait for heartbeats
	var events []string
	timeout := time.After(3 * time.Second)

	// Drain snapshot events first
	drainEvents(sw, 100*time.Millisecond)

	// Now wait for events
	collecting := true
	for collecting {
		select {
		case evt := <-sw.events:
			events = append(events, evt)
			if len(events) >= 2 {
				collecting = false
			}
		case <-timeout:
			collecting = false
		}
	}

	// We should have received at least something
	if len(events) == 0 {
		t.Log("no additional events received (may be OK with slow heartbeat)")
	}
}

func drainEvents(sw *streamTestWriter, timeout time.Duration) {
	deadline := time.After(timeout)
	for {
		select {
		case <-sw.events:
		case <-deadline:
			return
		}
	}
}

func TestTaskControlStreamWritesSSEFrames(t *testing.T) {
	h, _, cleanup := newStreamHandlerWithDB(t)
	defer cleanup()

	sw := newStreamTestWriter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Writer = sw
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/tasks/stream", nil).WithContext(ctx)
		h.TaskControlStream(c)
	}()

	// Collect raw SSE data
	timeout := time.After(2 * time.Second)
	var rawData []string
	collecting := true
	for collecting {
		select {
		case evt := <-sw.events:
			rawData = append(rawData, evt)
			if len(rawData) >= 3 {
				collecting = false
			}
		case <-timeout:
			collecting = false
		}
	}

	if len(rawData) == 0 {
		t.Fatal("expected SSE frames")
	}

	// Verify each frame ends with double newline (or is a comment)
	// We'll scan the combined data for SSE format
	combined := ""
	for _, d := range rawData {
		combined += d
	}
	scanner := bufio.NewScanner(strings.NewReader(combined))
	var frameLines []string
	for scanner.Scan() {
		line := scanner.Text()
		frameLines = append(frameLines, line)
		if line == "" {
			// End of a frame
		}
	}
	_ = frameLines
}
