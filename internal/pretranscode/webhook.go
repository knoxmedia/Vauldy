package pretranscode

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// WebhookService manages webhook configuration and dispatch (SRS 3.4).
type WebhookService struct {
	DB *sql.DB
}

// Webhook mirrors pretranscode_webhook.
type Webhook struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	Events    []string `json:"events"`
	Headers   map[string]string `json:"headers"`
	Secret    string   `json:"secret,omitempty"`
	IsEnabled bool     `json:"is_enabled"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

// WebhookLog mirrors pretranscode_webhook_log.
type WebhookLog struct {
	ID           int64  `json:"id"`
	WebhookID    int64  `json:"webhook_id"`
	Event        string `json:"event"`
	Payload      string `json:"payload"`
	ResponseCode int    `json:"response_code"`
	ResponseBody string `json:"response_body"`
	Error        string `json:"error"`
	RetryCount   int    `json:"retry_count"`
	CreatedAt    string `json:"created_at"`
}

// ListWebhooks returns all configured webhooks.
func (s *WebhookService) ListWebhooks() ([]Webhook, error) {
	rows, err := s.DB.Query(`SELECT id, name, url, events, COALESCE(headers,''), COALESCE(secret,''), is_enabled, COALESCE(created_at,''), COALESCE(updated_at,'')
		FROM pretranscode_webhook ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Webhook
	for rows.Next() {
		var w Webhook
		var eventsJSON, headersJSON string
		var enabled int
		if err := rows.Scan(&w.ID, &w.Name, &w.URL, &eventsJSON, &headersJSON, &w.Secret, &enabled, &w.CreatedAt, &w.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(eventsJSON), &w.Events)
		if headersJSON != "" {
			_ = json.Unmarshal([]byte(headersJSON), &w.Headers)
		}
		w.IsEnabled = enabled == 1
		out = append(out, w)
	}
	return out, nil
}

// CreateWebhook inserts a new webhook.
func (s *WebhookService) CreateWebhook(w *Webhook) error {
	eventsJSON, _ := json.Marshal(w.Events)
	headersJSON, _ := json.Marshal(w.Headers)
	enabled := 1
	if !w.IsEnabled {
		enabled = 0
	}
	res, err := s.DB.Exec(`INSERT INTO pretranscode_webhook (name, url, events, headers, secret, is_enabled) VALUES (?, ?, ?, ?, ?, ?)`,
		w.Name, w.URL, string(eventsJSON), string(headersJSON), w.Secret, enabled)
	if err != nil {
		return err
	}
	w.ID, _ = res.LastInsertId()
	return nil
}

// UpdateWebhook mutates an existing webhook.
func (s *WebhookService) UpdateWebhook(id int64, w *Webhook) error {
	eventsJSON, _ := json.Marshal(w.Events)
	headersJSON, _ := json.Marshal(w.Headers)
	enabled := 1
	if !w.IsEnabled {
		enabled = 0
	}
	_, err := s.DB.Exec(`UPDATE pretranscode_webhook SET name=?, url=?, events=?, headers=?, secret=?, is_enabled=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		w.Name, w.URL, string(eventsJSON), string(headersJSON), w.Secret, enabled, id)
	return err
}

// DeleteWebhook removes a webhook.
func (s *WebhookService) DeleteWebhook(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM pretranscode_webhook WHERE id = ?`, id)
	return err
}

// ListLogs returns recent webhook delivery logs for a webhook.
func (s *WebhookService) ListLogs(webhookID int64, limit int) ([]WebhookLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.Query(`SELECT id, webhook_id, event, COALESCE(payload,''), COALESCE(response_code,0), COALESCE(response_body,''), COALESCE(error,''), retry_count, COALESCE(created_at,'')
		FROM pretranscode_webhook_log WHERE webhook_id = ? ORDER BY id DESC LIMIT ?`, webhookID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WebhookLog
	for rows.Next() {
		var l WebhookLog
		if err := rows.Scan(&l.ID, &l.WebhookID, &l.Event, &l.Payload, &l.ResponseCode, &l.ResponseBody, &l.Error, &l.RetryCount, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

// SendEvent dispatches an event to all enabled webhooks subscribed to it.
// Implements SRS WS-01..04: POST JSON, 10s timeout, 3 retries with
// exponential backoff (1s/4s/16s), HMAC-SHA256 signature.
func (s *WebhookService) SendEvent(ctx context.Context, event string, payload any) {
	webhooks, err := s.ListWebhooks()
	if err != nil {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	for _, w := range webhooks {
		if !w.IsEnabled || !contains(w.Events, event) {
			continue
		}
		go s.deliver(ctx, w, event, body)
	}
}

// TestWebhook sends a test ping event to a single webhook (SRS WH-05).
func (s *WebhookService) TestWebhook(id int64) error {
	var w Webhook
	var eventsJSON, headersJSON string
	var enabled int
	err := s.DB.QueryRow(`SELECT id, name, url, events, COALESCE(headers,''), COALESCE(secret,''), is_enabled FROM pretranscode_webhook WHERE id=?`, id).
		Scan(&w.ID, &w.Name, &w.URL, &eventsJSON, &headersJSON, &w.Secret, &enabled)
	if err != nil {
		return err
	}
	_ = json.Unmarshal([]byte(eventsJSON), &w.Events)
	if headersJSON != "" {
		_ = json.Unmarshal([]byte(headersJSON), &w.Headers)
	}
	w.IsEnabled = enabled == 1
	body, _ := json.Marshal(map[string]any{"event": "test", "timestamp": time.Now().UTC().Format(time.RFC3339)})
	go s.deliver(context.Background(), w, "test", body)
	return nil
}

func (s *WebhookService) deliver(ctx context.Context, w Webhook, event string, body []byte) {
	delays := []time.Duration{0, 1 * time.Second, 4 * time.Second, 16 * time.Second}
	var lastErr error
	var respCode int
	var respBody string
	for attempt, delay := range delays {
		if delay > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}
		code, b, err := s.post(ctx, w, body)
		if err == nil && code >= 200 && code < 300 {
			respCode, respBody = code, string(b)
			lastErr = nil
			break
		}
		lastErr = err
		if lastErr == nil {
			lastErr = fmt.Errorf("http %d", code)
		}
		respCode, respBody = code, string(b)
		_ = attempt
	}
	logEntry := WebhookLog{
		WebhookID: w.ID, Event: event, Payload: string(body),
		ResponseCode: respCode, ResponseBody: truncate(respBody, 4000),
		RetryCount: len(delays) - 1,
	}
	if lastErr != nil {
		logEntry.Error = lastErr.Error()
	}
	s.insertLog(logEntry)
	if lastErr != nil {
		log.Printf("pretranscode webhook %d deliver failed: %v", w.ID, lastErr)
	}
}

func (s *WebhookService) post(ctx context.Context, w Webhook, body []byte) (int, []byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range w.Headers {
		req.Header.Set(k, v)
	}
	if w.Secret != "" {
		mac := hmac.New(sha256.New, []byte(w.Secret))
		mac.Write(body)
		req.Header.Set("X-Knox-Signature", hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	return resp.StatusCode, b, nil
}

func (s *WebhookService) insertLog(l WebhookLog) {
	_, _ = s.DB.Exec(`INSERT INTO pretranscode_webhook_log (webhook_id, event, payload, response_code, response_body, error, retry_count) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		l.WebhookID, l.Event, l.Payload, l.ResponseCode, l.ResponseBody, l.Error, l.RetryCount)
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// WebhookDispatcherAdapter wraps WebhookService to satisfy the
// WebhookDispatcher interface used by the worker.
type WebhookDispatcherAdapter struct {
	Service *WebhookService
}

func (a *WebhookDispatcherAdapter) SendTaskCompleted(taskID int64) {
	if a == nil || a.Service == nil {
		return
	}
	a.Service.SendEvent(context.Background(), "task.completed", map[string]any{
		"event":   "task.completed",
		"task_id": taskID,
	})
}

func (a *WebhookDispatcherAdapter) SendTaskFailed(taskID int64) {
	if a == nil || a.Service == nil {
		return
	}
	a.Service.SendEvent(context.Background(), "task.failed", map[string]any{
		"event":   "task.failed",
		"task_id": taskID,
	})
}

func (a *WebhookDispatcherAdapter) SendRenditionCompleted(taskID int64, jobID int64, renditionName string) {
	if a == nil || a.Service == nil {
		return
	}
	a.Service.SendEvent(context.Background(), "rendition.completed", map[string]any{
		"event":           "rendition.completed",
		"task_id":         taskID,
		"job_id":          jobID,
		"rendition_name":  renditionName,
	})
}
