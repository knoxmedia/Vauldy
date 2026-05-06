// Package zapglobal configures zap's global logger for packages that use zap.L().
package zapglobal

import "go.uber.org/zap"

// MustReplaceGlobals installs a production logger as zap.L(). Caller should defer Sync.
func MustReplaceGlobals() *zap.Logger {
	l := zap.Must(zap.NewProduction())
	zap.ReplaceGlobals(l)
	return l
}
