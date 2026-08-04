package publication

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// SubtitleRecognizeExecutable adapts subtitle recognition for planner admission.
type SubtitleRecognizeExecutable struct {
	Recognize func(context.Context, int64) error
}

func (a SubtitleRecognizeExecutable) TaskType() StepType { return StepSubtitleRecognize }

func (a SubtitleRecognizeExecutable) Execute(ctx context.Context, mediaID int64) error {
	if a.Recognize == nil {
		return fmt.Errorf("subtitle_recognize executable: recognize handler is not configured")
	}
	return a.Recognize(ctx, mediaID)
}

// AIAnalysisExecutable is the Phase 1 minimal text/subtitle-result analysis adapter.
type AIAnalysisExecutable struct {
	DB *sql.DB
}

func (a AIAnalysisExecutable) TaskType() StepType { return StepAIAnalysis }

func (a AIAnalysisExecutable) Execute(ctx context.Context, mediaID int64) error {
	if a.DB == nil {
		return fmt.Errorf("ai_analysis executable: database is not configured")
	}
	if mediaID <= 0 {
		return fmt.Errorf("ai_analysis executable: invalid media id")
	}
	rows, err := a.DB.QueryContext(ctx, `SELECT COALESCE(vtt_path,'') FROM media_subtitle WHERE media_id=? AND status='ready'`, mediaID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return err
		}
		if strings.TrimSpace(path) != "" {
			// Phase 1: presence of usable text is enough to admit success.
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// Recognition can complete successfully with no usable artifact; treat as
	// structured no-op success rather than a permanent executor failure.
	return nil
}

// DefaultExecutableAdapters registers typed admission handles for Phase 1 recognition/AI.
func DefaultExecutableAdapters(db *sql.DB, recognize func(context.Context, int64) error) ExecutableAdapterMap {
	return ExecutableAdapterMap{
		StepSubtitleRecognize: SubtitleRecognizeExecutable{Recognize: recognize},
		StepAIAnalysis:        AIAnalysisExecutable{DB: db},
	}
}
