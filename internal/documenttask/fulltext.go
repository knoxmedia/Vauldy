package documenttask

import (
	"time"
)

// FulltextMode describes how text was extracted.
type FulltextMode string

const (
	FulltextNative FulltextMode = "native"
	FulltextOCR    FulltextMode = "ocr"
	FulltextHybrid FulltextMode = "hybrid"
)

// FulltextTask represents a durable full-text extraction task.
type FulltextTask struct {
	ID           int64
	MediaID      int64
	Status       Status
	LeaseOwner   string
	LeaseUntil   time.Time
	Generation   int64
	RetryRound   int
	Attempts     int
	MaxAttempts  int
	MaxPages     int
	MaxBytes     int64
	Mode         FulltextMode
	Language     string
	Engine       string
	EngineVersion string
	SourceHash   string
	TextHash     string
	PageCount    int
	PageCoverage int
	TextPreview  string
	TextSize     int64
	FTSEntity    string
	LastError    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	StartedAt    *time.Time
	FinishedAt   *time.Time
}

// FulltextResult is the output of a successful full-text extraction.
type FulltextResult struct {
	Text        string
	TextSize    int64
	TextHash    string
	TextPreview string
	PageCount   int
	PageCoverage int
	Mode        FulltextMode
	Language    string
}

// FulltextInput is the input for full-text extraction.
type FulltextInput struct {
	MediaID       int64
	SourcePath    string
	Generation    int64
	Language      string
	MaxPages      int
	MaxBytes      int64
	MaxMinutes    int
	DocumentKind  string
}
