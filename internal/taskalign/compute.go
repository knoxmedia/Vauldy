package taskalign

import "database/sql"

type Counts map[string]int64

type Alignment struct {
	ByType map[string]Counts `json:"by_type"`
}

var taskTypes = []string{"subtitle", "preview", "atrack", "keyframe", "encrypt"}
var displayStatuses = []string{"waiting", "running", "done", "failed", "cancelled"}

func emptyAlignment() Alignment {
	alignment := Alignment{ByType: make(map[string]Counts, len(taskTypes))}
	for _, taskType := range taskTypes {
		counts := make(Counts, len(displayStatuses))
		for _, status := range displayStatuses {
			counts[status] = 0
		}
		alignment.ByType[taskType] = counts
	}
	return alignment
}

func Compute(db *sql.DB) (Alignment, error) {
	alignment := emptyAlignment()
	domainStatuses, err := loadDomainStatuses(db)
	if err != nil {
		return alignment, err
	}

	rows, err := db.Query(`
		SELECT q.media_id, q.task_type, q.status
		FROM post_ingest_task q
		JOIN media m ON m.id = q.media_id
		WHERE q.task_type IN ('subtitle','preview','atrack','keyframe','encrypt')
		  AND q.generation = COALESCE(m.ingest_generation, 0)
		  AND q.id = (
			SELECT MAX(latest.id)
			FROM post_ingest_task latest
			WHERE latest.media_id = q.media_id
			  AND latest.task_type = q.task_type
			  AND latest.generation = q.generation
		  )`)
	if err != nil {
		return alignment, err
	}
	defer rows.Close()

	for rows.Next() {
		var mediaID int64
		var taskType, queueStatus string
		if err := rows.Scan(&mediaID, &taskType, &queueStatus); err != nil {
			return alignment, err
		}
		display := Synthesize(queueStatus, domainStatuses[taskType][mediaID], taskType)
		if _, ok := alignment.ByType[taskType][display]; ok {
			alignment.ByType[taskType][display]++
		}
	}
	if err := rows.Err(); err != nil {
		return alignment, err
	}
	return alignment, nil
}

func loadDomainStatuses(db *sql.DB) (map[string]map[int64]string, error) {
	statuses := make(map[string]map[int64]string, 4)
	rows, err := db.Query(`
		SELECT 'subtitle', media_id, COALESCE(status, '') FROM subtitle_task
		UNION ALL
		SELECT 'preview', media_id, COALESCE(status, '') FROM preview_task
		UNION ALL
		SELECT 'atrack', media_id, COALESCE(status, '') FROM atrack_task
		UNION ALL
		SELECT 'keyframe', media_id, COALESCE(status, '') FROM keyframe_task`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var mediaID int64
		var taskType, status string
		if err := rows.Scan(&taskType, &mediaID, &status); err != nil {
			return nil, err
		}
		if statuses[taskType] == nil {
			statuses[taskType] = make(map[int64]string)
		}
		statuses[taskType][mediaID] = status
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return statuses, nil
}
