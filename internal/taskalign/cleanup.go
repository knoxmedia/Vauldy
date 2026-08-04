package taskalign

import (
	"context"
	"fmt"
	"strings"

	"knox-media/internal/store"
)

// DeleteCurrentGenQueueTasks removes current-generation post_ingest_task rows for
// the given media IDs when status is failed, cancelled, or waiting. Running rows
// are left untouched.
func DeleteCurrentGenQueueTasks(ctx context.Context, exec store.SQLExecutor, taskType string, mediaIDs ...int64) error {
	switch taskType {
	case "subtitle", "subtitle_recognize", "preview", "atrack", "keyframe":
	default:
		return nil
	}
	if exec == nil {
		return fmt.Errorf("delete current-gen queue tasks %s: nil database executor", taskType)
	}
	ids := uniquePositiveIDs(mediaIDs)
	if len(ids) == 0 {
		return nil
	}

	placeholders := make([]string, len(ids))
	args := make([]any, 0, 1+len(ids))
	args = append(args, taskType)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	q := fmt.Sprintf(`
DELETE FROM post_ingest_task
WHERE task_type=?
  AND media_id IN (%s)
  AND status IN ('failed','cancelled','waiting')
  AND generation=(SELECT COALESCE(ingest_generation,0) FROM media WHERE id=post_ingest_task.media_id)`, strings.Join(placeholders, ","))
	if _, err := exec.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("delete current-gen queue tasks %s: %w", taskType, err)
	}
	return nil
}

func uniquePositiveIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
