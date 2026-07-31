package publication

import "strings"

const (
	// DefaultLocalMaxAttempts is used for filesystem/ffmpeg-backed ingest work.
	DefaultLocalMaxAttempts = 1
	// DefaultNetworkMaxAttempts is used for network-backed ingest work (scrape).
	DefaultNetworkMaxAttempts = 3
)

// DefaultMaxAttempts returns the enqueue default for a step/task type.
// Local media-processing steps get a single attempt; network steps retry.
// Callers must persist this value on INSERT: SQLite column DEFAULT stays 3 for
// schema compatibility with existing databases and is not the behavioral default.
func DefaultMaxAttempts(stepType string) int {
	switch strings.ToLower(strings.TrimSpace(stepType)) {
	case string(StepScrape):
		return DefaultNetworkMaxAttempts
	default:
		return DefaultLocalMaxAttempts
	}
}
