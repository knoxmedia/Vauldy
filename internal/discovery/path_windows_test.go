//go:build windows

package discovery

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsPathErrorRetryabilityDefaults(t *testing.T) {
	for _, err := range []error{
		windows.ERROR_SHARING_VIOLATION,
		windows.ERROR_LOCK_VIOLATION,
		windows.ERROR_DELETE_PENDING,
		windows.ERROR_FILE_NOT_FOUND,
		windows.ERROR_PATH_NOT_FOUND,
	} {
		if !IsRetryablePathError(err) {
			t.Errorf("error %v classified permanent, want retryable", err)
		}
	}
	if IsRetryablePathError(windows.ERROR_ACCESS_DENIED) {
		t.Fatal("ERROR_ACCESS_DENIED classified retryable, want permanent")
	}
}
