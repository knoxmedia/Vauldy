package taskalign

import (
	"context"
	"fmt"
	"math"

	"knox-media/internal/store"
)

// EnsureDomainWaiting creates the domain-side task row for queue-backed task
// types without changing an existing task's state.
func EnsureDomainWaiting(ctx context.Context, exec store.SQLExecutor, taskType string, mediaID int64) error {
	switch taskType {
	case "subtitle", "preview", "atrack", "keyframe":
	case "encrypt":
		// Encrypt task management uses post_ingest_task as its domain record.
		return nil
	default:
		return nil
	}
	if exec == nil {
		return fmt.Errorf("ensure domain task %s: nil database executor", taskType)
	}

	var err error
	switch taskType {
	case "subtitle":
		_, err = exec.ExecContext(ctx, `INSERT OR IGNORE INTO subtitle_task(media_id,status,message,created_at,started_at,finished_at,updated_at) VALUES(?,'pending',NULL,CURRENT_TIMESTAMP,NULL,NULL,CURRENT_TIMESTAMP)`, mediaID)
	case "preview":
		var duration int64
		if err = exec.QueryRowContext(ctx, `SELECT COALESCE(duration,0) FROM media WHERE id=?`, mediaID).Scan(&duration); err == nil {
			intervalSec, thumbCount := previewTaskParameters(duration)
			_, err = exec.ExecContext(ctx, `INSERT INTO preview_task(media_id,status,interval_sec,thumb_count,thumb_width,thumb_height,updated_at) VALUES(?,'waiting',?,?,240,135,CURRENT_TIMESTAMP) ON CONFLICT(media_id) DO NOTHING`, mediaID, intervalSec, thumbCount)
		}
	case "atrack":
		_, err = exec.ExecContext(ctx, `INSERT INTO atrack_task(media_id,status,updated_at) VALUES(?,'waiting',CURRENT_TIMESTAMP) ON CONFLICT(media_id) DO NOTHING`, mediaID)
	case "keyframe":
		_, err = exec.ExecContext(ctx, `INSERT INTO keyframe_task(media_id,status,updated_at) VALUES(?,'waiting',CURRENT_TIMESTAMP) ON CONFLICT(media_id) DO NOTHING`, mediaID)
	}
	if err != nil {
		return fmt.Errorf("ensure domain task %s for media %d: %w", taskType, mediaID, err)
	}
	return nil
}

// previewTaskParameters mirrors preview.TaskParameters without importing the
// preview package, which reaches publication through storage.
func previewTaskParameters(durationSec int64) (intervalSec, countNum int) {
	if durationSec <= 0 {
		durationSec = 600
	}
	intervalSec = int(math.Ceil(float64(durationSec) / 100.0))
	if intervalSec < 5 {
		intervalSec = 5
	}
	countNum = int(math.Ceil(float64(durationSec) / float64(intervalSec)))
	if countNum < 1 {
		countNum = 1
	}
	if countNum > 100 {
		countNum = 100
	}
	return intervalSec, countNum
}
