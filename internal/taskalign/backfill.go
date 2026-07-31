package taskalign

import (
	"context"
	"database/sql"
	"fmt"
)

// BackfillResult summarizes an idempotent domain-row repair pass.
type BackfillResult struct {
	Created int
	ByType  map[string]int
}

// BackfillMissingDomainTasks creates domain waiting/pending rows for current-
// generation post_ingest tasks that are waiting, failed, or cancelled but have
// no domain row. Queue rows are not modified. Encrypt is skipped.
func BackfillMissingDomainTasks(ctx context.Context, db *sql.DB) (BackfillResult, error) {
	result := BackfillResult{ByType: map[string]int{}}
	if db == nil {
		return result, fmt.Errorf("backfill domain tasks: nil database")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	rows, err := db.QueryContext(ctx, `
		SELECT q.media_id, q.task_type
		FROM post_ingest_task q
		JOIN media m ON m.id = q.media_id
		WHERE q.task_type IN ('subtitle','preview','atrack','keyframe')
		  AND q.status IN ('waiting','failed','cancelled')
		  AND q.generation = COALESCE(m.ingest_generation, 0)
		  AND q.id = (
			SELECT MAX(latest.id)
			FROM post_ingest_task latest
			WHERE latest.media_id = q.media_id
			  AND latest.task_type = q.task_type
			  AND latest.generation = q.generation
		  )
		  AND (
			(q.task_type = 'subtitle' AND NOT EXISTS (SELECT 1 FROM subtitle_task s WHERE s.media_id = q.media_id))
			OR (q.task_type = 'preview' AND NOT EXISTS (SELECT 1 FROM preview_task p WHERE p.media_id = q.media_id))
			OR (q.task_type = 'atrack' AND NOT EXISTS (SELECT 1 FROM atrack_task a WHERE a.media_id = q.media_id))
			OR (q.task_type = 'keyframe' AND NOT EXISTS (SELECT 1 FROM keyframe_task k WHERE k.media_id = q.media_id))
		  )
		ORDER BY q.task_type, q.media_id`)
	if err != nil {
		return result, fmt.Errorf("backfill domain tasks: query: %w", err)
	}
	defer rows.Close()

	type item struct {
		mediaID  int64
		taskType string
	}
	var missing []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.mediaID, &it.taskType); err != nil {
			return result, fmt.Errorf("backfill domain tasks: scan: %w", err)
		}
		missing = append(missing, it)
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("backfill domain tasks: rows: %w", err)
	}

	for _, it := range missing {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := EnsureDomainWaiting(ctx, db, it.taskType, it.mediaID); err != nil {
			return result, err
		}
		result.Created++
		result.ByType[it.taskType]++
	}
	return result, nil
}
