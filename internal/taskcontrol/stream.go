package taskcontrol

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// StreamEvent is a single SSE event emitted by the stream broker.
type StreamEvent struct {
	ID       int64       `json:"id"`
	Revision int64       `json:"revision"`
	Type     string      `json:"type"` // "snapshot", "delta", "heartbeat", "resync"
	Payload  interface{} `json:"payload,omitempty"`
}

// StreamSubscriber receives stream events from the broker.
type StreamSubscriber struct {
	ID     string
	Events chan StreamEvent
	Done   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
}

// StreamBroker manages SSE subscribers and coordinates revision-based
// streaming of task projection updates.
type StreamBroker struct {
	mu          sync.RWMutex
	subs        map[string]*StreamSubscriber
	db          *sql.DB
	builder     *ProjectionBuilder
	qs          *QueryService
	nextID      int64
	heartbeat   time.Duration
	maxSubs     int
	maxOverflow int
	running     bool
}

// StreamBrokerConfig holds configuration for the stream broker.
type StreamBrokerConfig struct {
	HeartbeatInterval time.Duration
	MaxSubscribers    int
	SubscriberOverflow int
}

// DefaultStreamConfig returns sensible defaults.
func DefaultStreamConfig() StreamBrokerConfig {
	return StreamBrokerConfig{
		HeartbeatInterval:  30 * time.Second,
		MaxSubscribers:     100,
		SubscriberOverflow: 256,
	}
}

// NewStreamBroker creates a new stream broker.
func NewStreamBroker(db *sql.DB, builder *ProjectionBuilder, cfg StreamBrokerConfig) *StreamBroker {
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 30 * time.Second
	}
	if cfg.MaxSubscribers <= 0 {
		cfg.MaxSubscribers = 100
	}
	if cfg.SubscriberOverflow <= 0 {
		cfg.SubscriberOverflow = 256
	}
	return &StreamBroker{
		db:          db,
		builder:     builder,
		qs:          NewQueryService(builder),
		heartbeat:   cfg.HeartbeatInterval,
		maxSubs:     cfg.MaxSubscribers,
		maxOverflow: cfg.SubscriberOverflow,
		subs:        make(map[string]*StreamSubscriber),
	}
}

// Publish sends a delta event to all subscribers. Called after DB commit.
func (b *StreamBroker) Publish(ctx context.Context, taskIdentity string) error {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if !b.running {
		return nil
	}

	// Get latest projection for the changed task
	row, err := b.builder.Project(ctx, taskIdentity)
	if err != nil {
		return err
	}
	if row == nil {
		return nil
	}

	revision := row.Revision
	event := StreamEvent{
		ID:       b.nextID + 1,
		Revision: revision,
		Type:     "delta",
		Payload:  row,
	}
	_ = event // not yet incrementing -- need to handle atomically

	b.broadcast(event)
	return nil
}

// Subscribe creates a new subscriber and sends an initial snapshot.
func (b *StreamBroker) Subscribe(ctx context.Context, sinceRevision int64) (*StreamSubscriber, error) {
	b.mu.Lock()
	if len(b.subs) >= b.maxSubs {
		b.mu.Unlock()
		return nil, ErrTooManySubscribers
	}

	subCtx, cancel := context.WithCancel(ctx)
	sub := &StreamSubscriber{
		ID:     generateSubscriberID(),
		Events: make(chan StreamEvent, b.maxOverflow),
		Done:   make(chan struct{}),
		ctx:    subCtx,
		cancel: cancel,
	}
	b.subs[sub.ID] = sub
	b.running = true
	b.mu.Unlock()

	// Send initial snapshot async
	go b.sendSnapshot(sub, sinceRevision)

	return sub, nil
}

// Unsubscribe removes a subscriber and cleans up its resources.
func (b *StreamBroker) Unsubscribe(subID string) {
	b.mu.Lock()
	sub, ok := b.subs[subID]
	if ok {
		delete(b.subs, subID)
	}
	b.mu.Unlock()

	if ok {
		sub.cancel()
		close(sub.Done)
	}
}

// StartHeartbeat starts sending periodic heartbeat events to all subscribers.
func (b *StreamBroker) StartHeartbeat(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(b.heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				b.sendHeartbeat()
			}
		}
	}()
}

// Shutdown gracefully stops the broker and disconnects all subscribers.
func (b *StreamBroker) Shutdown() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.running = false
	for id, sub := range b.subs {
		sub.cancel()
		close(sub.Done)
		delete(b.subs, id)
	}
}

func (b *StreamBroker) broadcast(event StreamEvent) {
	for _, sub := range b.subs {
		select {
		case sub.Events <- event:
		default:
			// Slow consumer: drop event (bounded overflow)
		}
	}
}

func (b *StreamBroker) sendSnapshot(sub *StreamSubscriber, sinceRevision int64) {
	defer func() { recover() }()

	ctx, cancel := context.WithTimeout(sub.ctx, 10*time.Second)
	defer cancel()

	// Query all non-removed tasks for the snapshot
	result, err := b.qs.List(ctx, QueryFilter{Removed: "exclude"}, "", 200)
	if err != nil {
		return
	}

	// Send snapshot events with strictly increasing IDs
	for i, item := range result.Items {
		event := StreamEvent{
			ID:       sinceRevision + int64(i) + 1,
			Revision: result.SnapshotRevision,
			Type:     "snapshot",
			Payload:  item,
		}
		select {
		case sub.Events <- event:
		case <-sub.ctx.Done():
			return
		}
	}
}

func (b *StreamBroker) sendHeartbeat() {
	b.mu.RLock()
	defer b.mu.RUnlock()

	event := StreamEvent{
		Type: "heartbeat",
	}
	for _, sub := range b.subs {
		select {
		case sub.Events <- event:
		default:
		}
	}
}

// Errors
var (
	ErrTooManySubscribers = &StreamError{msg: "too many subscribers"}
	ErrStreamShutdown     = &StreamError{msg: "stream broker shut down"}
)

// StreamError is a typed error for stream operations.
type StreamError struct {
	msg string
}

func (e *StreamError) Error() string { return e.msg }

var subscriberIDSeq int64

func generateSubscriberID() string {
	return fmt.Sprintf("sub-%d", atomic.AddInt64(&subscriberIDSeq, 1))
}
