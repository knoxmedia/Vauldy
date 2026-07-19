package pretranscode

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebhookCRUD(t *testing.T) {
	db := newTestDB(t)
	svc := &WebhookService{DB: db}
	w := &Webhook{
		Name: "hook1", URL: "http://example.com/hook",
		Events: []string{"task.completed"}, Headers: map[string]string{"X-Token": "abc"},
		Secret: "s3cret", IsEnabled: true,
	}
	if err := svc.CreateWebhook(w); err != nil {
		t.Fatal(err)
	}
	list, _ := svc.ListWebhooks()
	if len(list) != 1 || list[0].Name != "hook1" {
		t.Errorf("list wrong: %+v", list)
	}
	w.URL = "http://example.com/updated"
	w.Events = []string{"task.completed", "task.failed"}
	_ = svc.UpdateWebhook(w.ID, w)
	list, _ = svc.ListWebhooks()
	if list[0].URL != "http://example.com/updated" || len(list[0].Events) != 2 {
		t.Errorf("update failed: %+v", list[0])
	}
	_ = svc.DeleteWebhook(w.ID)
	list, _ = svc.ListWebhooks()
	if len(list) != 0 {
		t.Errorf("delete failed")
	}
}

func TestWebhookDeliverSuccess(t *testing.T) {
	db := newTestDB(t)
	svc := &WebhookService{DB: db}
	var received int32
	signatures := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&received, 1)
		signatures <- r.Header.Get("X-Knox-Signature")
		body, _ := io.ReadAll(r.Body)
		_ = body
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	w := &Webhook{
		Name: "ok", URL: srv.URL, Events: []string{"task.completed"},
		Secret: "topsecret", IsEnabled: true,
	}
	_ = svc.CreateWebhook(w)

	svc.SendEvent(context.Background(), "task.completed", map[string]any{"task_id": 99})
	// Delivery happens in goroutines; wait briefly.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&received) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if atomic.LoadInt32(&received) == 0 {
		t.Fatalf("webhook was not delivered")
	}
	select {
	case gotSig := <-signatures:
		if gotSig == "" {
			t.Errorf("X-Knox-Signature header missing")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("signature was not received")
	}
	// Log row should exist.
	logs, _ := svc.ListLogs(w.ID, 10)
	if len(logs) == 0 {
		t.Errorf("expected at least one log entry")
	}
}

func TestWebhookRetriesOnFailure(t *testing.T) {
	db := newTestDB(t)
	svc := &WebhookService{DB: db}
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(500)
	}))
	defer srv.Close()
	w := &Webhook{
		Name: "fail", URL: srv.URL, Events: []string{"task.failed"},
		IsEnabled: true,
	}
	_ = svc.CreateWebhook(w)
	svc.SendEvent(context.Background(), "task.failed", map[string]any{"task_id": 1})
	// 4 attempts (initial + 3 retries) with exponential backoff 1s+4s+16s.
	// Wait long enough for all retries; cap at 25s.
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&attempts) >= 4 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if atomic.LoadInt32(&attempts) != 4 {
		t.Errorf("expected 4 attempts (1 + 3 retries), got %d", atomic.LoadInt32(&attempts))
	}
	logs, _ := svc.ListLogs(w.ID, 10)
	if len(logs) == 0 || logs[0].ResponseCode != 500 {
		t.Errorf("expected logged 500 response, got %+v", logs)
	}
}

func TestWebhookDisabledNotDelivered(t *testing.T) {
	db := newTestDB(t)
	svc := &WebhookService{DB: db}
	var received int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&received, 1)
	}))
	defer srv.Close()
	w := &Webhook{Name: "off", URL: srv.URL, Events: []string{"task.completed"}, IsEnabled: false}
	_ = svc.CreateWebhook(w)
	svc.SendEvent(context.Background(), "task.completed", map[string]any{})
	time.Sleep(100 * time.Millisecond)
	if atomic.LoadInt32(&received) != 0 {
		t.Errorf("disabled webhook should not be delivered")
	}
}

func TestWebhookEventFiltering(t *testing.T) {
	db := newTestDB(t)
	svc := &WebhookService{DB: db}
	var received int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&received, 1)
	}))
	defer srv.Close()
	// Webhook subscribed only to task.completed — should not get task.failed.
	w := &Webhook{Name: "filter", URL: srv.URL, Events: []string{"task.completed"}, IsEnabled: true}
	_ = svc.CreateWebhook(w)
	svc.SendEvent(context.Background(), "task.failed", map[string]any{})
	time.Sleep(100 * time.Millisecond)
	if atomic.LoadInt32(&received) != 0 {
		t.Errorf("webhook subscribed to task.completed must not receive task.failed")
	}
}

func TestWebhookPayloadShape(t *testing.T) {
	db := newTestDB(t)
	svc := &WebhookService{DB: db}
	payloads := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var got map[string]any
		_ = json.Unmarshal(body, &got)
		payloads <- got
		w.WriteHeader(200)
	}))
	defer srv.Close()
	w := &Webhook{Name: "shape", URL: srv.URL, Events: []string{"task.completed"}, IsEnabled: true}
	_ = svc.CreateWebhook(w)
	svc.SendEvent(context.Background(), "task.completed", map[string]any{"event": "task.completed", "task_id": float64(42)})
	select {
	case got := <-payloads:
		if got["event"] != "task.completed" || got["task_id"] != float64(42) {
			t.Errorf("payload shape wrong: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("payload was not received")
	}
}
