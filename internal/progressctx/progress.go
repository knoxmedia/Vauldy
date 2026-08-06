// Package progressctx carries a forward-progress callback through a context.
// Long-running task executors (encryption, preview, keyframe, ...) call Report
// while their underlying work advances; dispatchers use the callback to
// distinguish a healthy long-running task from a stalled one without importing
// the task-control packages (which would create an import cycle).
package progressctx

import "context"

type reporterKey struct{}

// WithReporter attaches a progress callback to ctx.
func WithReporter(ctx context.Context, report func()) context.Context {
	return context.WithValue(ctx, reporterKey{}, report)
}

// Report signals that the current task made forward progress. It is a no-op
// when ctx carries no reporter.
func Report(ctx context.Context) {
	if report, ok := ctx.Value(reporterKey{}).(func()); ok && report != nil {
		report()
	}
}
