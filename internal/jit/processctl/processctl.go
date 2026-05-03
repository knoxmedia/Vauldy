// Package processctl suspends and resumes child encoder processes in an OS-specific way.
package processctl

import "errors"

// ErrUnsupported means Suspend/Resume are not implemented for the build target.
var ErrUnsupported = errors.New("process suspend/resume not supported on this OS")
