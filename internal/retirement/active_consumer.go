package retirement

import "sync"

var (
	defaultActiveConsumerMu sync.RWMutex
	defaultActiveConsumer   ActiveConsumerFunc
)

// SetDefaultActiveConsumer registers the process-wide ActiveConsumer used by
// RecomputeRetirementBarrierTx (publication lifecycle recompute). Worker Execute
// continues to use CrashSeams.ActiveConsumer explicitly.
func SetDefaultActiveConsumer(fn ActiveConsumerFunc) {
	defaultActiveConsumerMu.Lock()
	defer defaultActiveConsumerMu.Unlock()
	defaultActiveConsumer = fn
}

func getDefaultActiveConsumer() ActiveConsumerFunc {
	defaultActiveConsumerMu.RLock()
	defer defaultActiveConsumerMu.RUnlock()
	return defaultActiveConsumer
}
