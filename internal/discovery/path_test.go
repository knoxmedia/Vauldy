package discovery

import (
	"errors"
	"fmt"
	"os"
	"testing"
)

func TestHandleResolutionUnsupportedIsDistinctAndPermanent(t *testing.T) {
	err := fmt.Errorf("resolve descriptor: %w", ErrHandleResolutionUnsupported)
	if IsRetryablePathError(err) {
		t.Fatalf("unsupported descriptor resolution classified retryable: %v", err)
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsupported descriptor resolution aliases source ENOENT: %v", err)
	}
}

func TestMissingSourceRemainsRetryable(t *testing.T) {
	err := fmt.Errorf("open source: %w", os.ErrNotExist)
	if !IsRetryablePathError(err) {
		t.Fatalf("source ENOENT classified permanent: %v", err)
	}
	if errors.Is(err, ErrHandleResolutionUnsupported) {
		t.Fatalf("source ENOENT aliases unsupported descriptor resolution: %v", err)
	}
}
