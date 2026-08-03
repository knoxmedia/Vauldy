package publication

import "sync"

// PostIngestTempRelease clears lease-bound plaintext temps when a post_ingest
// task's lease ends (cancel/fail/recover-to-waiting). Identity is media/generation/task
// only — lease owner is not required because cancel paths null the lease first.
type PostIngestTempRelease func(mediaID, generation, taskID int64)

var (
	postIngestTempReleaseMu sync.RWMutex
	postIngestTempRelease   PostIngestTempRelease
)

// SetPostIngestTempRelease installs the process-wide release hook (storage registers it).
func SetPostIngestTempRelease(fn PostIngestTempRelease) {
	postIngestTempReleaseMu.Lock()
	postIngestTempRelease = fn
	postIngestTempReleaseMu.Unlock()
}

func invokePostIngestTempRelease(mediaID, generation, taskID int64) {
	postIngestTempReleaseMu.RLock()
	fn := postIngestTempRelease
	postIngestTempReleaseMu.RUnlock()
	if fn != nil && mediaID > 0 && generation > 0 && taskID > 0 {
		fn(mediaID, generation, taskID)
	}
}

// ReleasePostIngestTempAttempt is the exported helper for queue/admin/recover paths.
func ReleasePostIngestTempAttempt(mediaID, generation, taskID int64) {
	invokePostIngestTempRelease(mediaID, generation, taskID)
}
