package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type streamSequenceBuilder struct {
	mu          sync.Mutex
	results     []error
	calls       int
	inFlight    int
	maxInFlight int
	started     chan struct{}
	block       time.Duration
	deadlines   []time.Duration
}

func (b *streamSequenceBuilder) Build(ctx context.Context) (AdminOverviewData, error) {
	b.mu.Lock()
	b.calls++
	call := b.calls
	b.inFlight++
	if b.inFlight > b.maxInFlight {
		b.maxInFlight = b.inFlight
	}
	if deadline, ok := ctx.Deadline(); ok {
		b.deadlines = append(b.deadlines, time.Until(deadline))
	}
	b.mu.Unlock()
	if b.started != nil {
		select {
		case b.started <- struct{}{}:
		default:
		}
	}

	var err error
	if b.block > 0 {
		select {
		case <-time.After(b.block):
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	if err == nil && call <= len(b.results) {
		err = b.results[call-1]
	}

	b.mu.Lock()
	b.inFlight--
	b.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return AdminOverviewData{"call": call}, nil
}

func (b *streamSequenceBuilder) stats() (int, int, []time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls, b.maxInFlight, append([]time.Duration(nil), b.deadlines...)
}

type streamFailureWriter struct {
	gin.ResponseWriter
	writeErr error
	flushErr error
}

type flushErrorRecorder struct {
	*httptest.ResponseRecorder
	err error
}

func (w *flushErrorRecorder) FlushError() error { return w.err }

type shortWriteRecorder struct {
	*httptest.ResponseRecorder
}

func (w *shortWriteRecorder) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return len(p) - 1, nil
}

func (w *streamFailureWriter) Write(p []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.ResponseWriter.Write(p)
}
func (w *streamFailureWriter) FlushError() error { return w.flushErr }

func startAdminOverviewStream(t *testing.T, h *Handler, writer gin.ResponseWriter) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Writer = writer
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/overview/stream", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.AdminOverviewStream(c)
	}()
	return cancel, done
}

func waitStreamDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("stream handler did not stop")
	}
}

func TestAdminOverviewStream_SendsImmediatelyThenSeriallyAtInterval(t *testing.T) {
	gin.SetMode(gin.TestMode)
	builder := &streamSequenceBuilder{started: make(chan struct{}, 8), block: 15 * time.Millisecond}
	recorder := httptest.NewRecorder()
	writer := &streamFailureWriter{ResponseWriter: ginResponseWriter(recorder)}
	h := &Handler{AdminOverviewBuilder: builder, overviewStreamInterval: 5 * time.Millisecond, overviewBuildTimeout: 100 * time.Millisecond}
	cancel, done := startAdminOverviewStream(t, h, writer)

	select {
	case <-builder.started:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("first snapshot was not started immediately")
	}
	for i := 0; i < 2; i++ {
		select {
		case <-builder.started:
		case <-time.After(100 * time.Millisecond):
			t.Fatal("subsequent snapshot was not built")
		}
	}
	cancel()
	waitStreamDone(t, done)

	calls, maxInFlight, deadlines := builder.stats()
	if calls < 3 || maxInFlight != 1 {
		t.Fatalf("calls=%d maxInFlight=%d, want >=3 and 1", calls, maxInFlight)
	}
	for _, d := range deadlines {
		if d <= 0 || d > 100*time.Millisecond {
			t.Fatalf("build deadline=%s, want request-derived timeout", d)
		}
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type=%q", got)
	}
	for key, want := range map[string]string{"Cache-Control": "no-cache", "Connection": "keep-alive", "X-Accel-Buffering": "no"} {
		if got := recorder.Header().Get(key); got != want {
			t.Errorf("%s=%q want %q", key, got, want)
		}
	}
	if !strings.Contains(recorder.Body.String(), "event: overview\ndata: {\"call\":1}\n\n") {
		t.Fatalf("body=%q", recorder.Body.String())
	}
}

func TestAdminOverviewStream_RequestCancellationCancelsBuild(t *testing.T) {
	gin.SetMode(gin.TestMode)
	builder := &streamSequenceBuilder{started: make(chan struct{}, 1), block: time.Second}
	recorder := httptest.NewRecorder()
	h := &Handler{AdminOverviewBuilder: builder, overviewStreamInterval: time.Second, overviewBuildTimeout: time.Second}
	cancel, done := startAdminOverviewStream(t, h, ginResponseWriter(recorder))
	<-builder.started
	cancel()
	waitStreamDone(t, done)
	calls, _, _ := builder.stats()
	if calls != 1 {
		t.Fatalf("calls=%d want 1", calls)
	}
	if body := recorder.Body.String(); body != "" {
		t.Fatalf("body=%q want no error frame after request cancellation", body)
	}
}

func TestAdminOverviewStream_TimeoutContinuesAndSuccessResetsFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	builder := &streamSequenceBuilder{results: []error{context.DeadlineExceeded, errors.New("one"), nil, errors.New("two"), errors.New("three"), errors.New("four")}}
	recorder := httptest.NewRecorder()
	h := &Handler{AdminOverviewBuilder: builder, overviewStreamInterval: time.Millisecond, overviewBuildTimeout: 20 * time.Millisecond}
	_, done := startAdminOverviewStream(t, h, ginResponseWriter(recorder))
	waitStreamDone(t, done)

	calls, _, _ := builder.stats()
	if calls != 6 {
		t.Fatalf("calls=%d want 6; success must reset consecutive failures", calls)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "event: error\ndata: {\"code\":\"admin_overview_timeout\"}\n\n") {
		t.Fatalf("missing stable timeout event in %q", body)
	}
	if strings.Count(body, "event: error\ndata: {\"code\":\"admin_overview_internal\"}\n\n") != 4 {
		t.Fatalf("internal error frames=%d body=%q", strings.Count(body, "admin_overview_internal"), body)
	}
	if !strings.Contains(body, "event: overview\ndata: {\"call\":3}\n\n") {
		t.Fatalf("missing recovery snapshot in %q", body)
	}
}

func TestAdminOverviewStream_StopsAfterThreeConsecutiveFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	builder := &streamSequenceBuilder{results: []error{errors.New("one"), errors.New("two"), errors.New("three"), nil}}
	recorder := httptest.NewRecorder()
	h := &Handler{AdminOverviewBuilder: builder, overviewStreamInterval: time.Millisecond, overviewBuildTimeout: 20 * time.Millisecond}
	_, done := startAdminOverviewStream(t, h, ginResponseWriter(recorder))
	waitStreamDone(t, done)
	calls, _, _ := builder.stats()
	if calls != 3 || strings.Count(recorder.Body.String(), "admin_overview_internal") != 3 {
		t.Fatalf("calls=%d body=%q", calls, recorder.Body.String())
	}
}

func TestAdminOverviewStream_StopsOnMarshalFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	builder := overviewBuilderFunc(func(context.Context) (AdminOverviewData, error) {
		return AdminOverviewData{"invalid": make(chan struct{})}, nil
	})
	recorder := httptest.NewRecorder()
	h := &Handler{AdminOverviewBuilder: builder, overviewStreamInterval: time.Millisecond, overviewBuildTimeout: 20 * time.Millisecond}
	_, done := startAdminOverviewStream(t, h, ginResponseWriter(recorder))
	waitStreamDone(t, done)
	if recorder.Body.Len() != 0 {
		t.Fatalf("body=%q want empty after marshal failure", recorder.Body.String())
	}
}

func TestAdminOverviewStream_PropagatesFlushErrorThroughGinWriter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sentinel := errors.New("flush sentinel")
	underlying := &flushErrorRecorder{ResponseRecorder: httptest.NewRecorder(), err: sentinel}
	writer := ginResponseWriterFromHTTP(underlying)
	if _, ok := any(writer).(interface{ Unwrap() http.ResponseWriter }); !ok {
		t.Fatal("Gin response writer does not expose Unwrap")
	}
	if err := flushResponse(writer); !errors.Is(err, sentinel) {
		t.Fatalf("flushResponse error=%v want sentinel", err)
	}

	builder := &streamSequenceBuilder{}
	h := &Handler{AdminOverviewBuilder: builder, overviewStreamInterval: time.Millisecond, overviewBuildTimeout: 20 * time.Millisecond}
	_, done := startAdminOverviewStream(t, h, writer)
	waitStreamDone(t, done)
	calls, _, _ := builder.stats()
	if calls != 1 {
		t.Fatalf("calls=%d want 1 after first flush failure", calls)
	}
}

func TestWriteResponse_ReturnsShortWrite(t *testing.T) {
	writer := &shortWriteRecorder{ResponseRecorder: httptest.NewRecorder()}
	if err := writeResponse(writer, []byte("frame")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("writeResponse error=%v want io.ErrShortWrite", err)
	}
}

func TestAdminOverviewStream_StopsOnShortWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	underlying := &shortWriteRecorder{ResponseRecorder: httptest.NewRecorder()}
	builder := &streamSequenceBuilder{}
	h := &Handler{AdminOverviewBuilder: builder, overviewStreamInterval: time.Millisecond, overviewBuildTimeout: 20 * time.Millisecond}
	_, done := startAdminOverviewStream(t, h, ginResponseWriterFromHTTP(underlying))
	waitStreamDone(t, done)
	calls, _, _ := builder.stats()
	if calls != 1 {
		t.Fatalf("calls=%d want 1 after short write", calls)
	}
}

func TestAdminOverviewStream_StopsOnWriteOrFlushFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		writeErr error
		flushErr error
	}{
		{name: "write", writeErr: errors.New("write failed")},
		{name: "flush", flushErr: errors.New("flush failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			builder := &streamSequenceBuilder{}
			recorder := httptest.NewRecorder()
			base := ginResponseWriter(recorder)
			writer := &streamFailureWriter{ResponseWriter: base, writeErr: tc.writeErr, flushErr: tc.flushErr}
			if tc.writeErr == nil {
				writer.writeErr = nil
			}
			h := &Handler{AdminOverviewBuilder: builder, overviewStreamInterval: time.Millisecond, overviewBuildTimeout: 20 * time.Millisecond}
			_, done := startAdminOverviewStream(t, h, writer)
			waitStreamDone(t, done)
			calls, _, _ := builder.stats()
			if calls != 1 {
				t.Fatalf("calls=%d want 1", calls)
			}
		})
	}
}

func ginResponseWriter(recorder *httptest.ResponseRecorder) gin.ResponseWriter {
	return ginResponseWriterFromHTTP(recorder)
}

func ginResponseWriterFromHTTP(writer http.ResponseWriter) gin.ResponseWriter {
	c, _ := gin.CreateTestContext(writer)
	return c.Writer
}
