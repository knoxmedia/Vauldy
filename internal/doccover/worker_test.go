package doccover

import "testing"

func TestCoverJobTimeoutSec(t *testing.T) {
	if got := coverJobTimeoutSec(nil); got != minCoverJobTimeoutSec {
		t.Fatalf("nil fn: got %d want %d", got, minCoverJobTimeoutSec)
	}
	fn := func() int { return 180 }
	if got := coverJobTimeoutSec(fn); got != minCoverJobTimeoutSec {
		t.Fatalf("180: got %d want %d", got, minCoverJobTimeoutSec)
	}
	fn = func() int { return 900 }
	if got := coverJobTimeoutSec(fn); got != 900 {
		t.Fatalf("900: got %d want 900", got)
	}
}
