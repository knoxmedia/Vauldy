package config

import (
	"testing"
)

func TestIngestDefaultMaxConcurrent(t *testing.T) {
	c := Config{}
	c.normalizeIngest()
	if c.Ingest.MaxConcurrent != 3 {
		t.Fatalf("default MaxConcurrent=%d want 3", c.Ingest.MaxConcurrent)
	}
}

func TestIngestExplicitValueSurvives(t *testing.T) {
	c := Config{Ingest: IngestConfig{MaxConcurrent: 7, maxConcurrentSet: true}}
	c.normalizeIngest()
	if c.Ingest.MaxConcurrent != 7 {
		t.Fatalf("explicit MaxConcurrent=%d want 7", c.Ingest.MaxConcurrent)
	}
}

func TestIngestZeroRejected(t *testing.T) {
	c := Config{Ingest: IngestConfig{MaxConcurrent: 0, maxConcurrentSet: true}}
	c.normalizeIngest()
	if err := c.Ingest.Validate(); err == nil {
		t.Fatal("expected zero rejected")
	}
}

func TestIngestValidateUpperBound(t *testing.T) {
	c := Config{Ingest: IngestConfig{MaxConcurrent: 33, maxConcurrentSet: true}}
	c.normalizeIngest()
	if err := c.Ingest.Validate(); err == nil {
		t.Fatal("expected 33 rejected")
	}
}

func TestIngestValidateAcceptsBoundary(t *testing.T) {
	for _, v := range []int{1, 32} {
		c := Config{Ingest: IngestConfig{MaxConcurrent: v, maxConcurrentSet: true}}
		c.normalizeIngest()
		if err := c.Ingest.Validate(); err != nil {
			t.Fatalf("MaxConcurrent=%d: %v", v, err)
		}
	}
}

func TestIngestHeartbeatShorterThanLease(t *testing.T) {
	if defaultIngestHeartbeatSeconds() >= defaultIngestLeaseSeconds() {
		t.Fatalf("heartbeat %d >= lease %d", defaultIngestHeartbeatSeconds(), defaultIngestLeaseSeconds())
	}
}

func TestIngestInternalDefaultsArePositive(t *testing.T) {
	for name, v := range map[string]int{
		"lease":        defaultIngestLeaseSeconds(),
		"heartbeat":    defaultIngestHeartbeatSeconds(),
		"stability":    defaultIngestStabilitySeconds(),
		"reconciliation": defaultIngestReconciliationSeconds(),
	} {
		if v <= 0 {
			t.Fatalf("%s=%d want positive", name, v)
		}
	}
}

func TestIngestShippedConfigDoesNotOverrideDefault(t *testing.T) {
	// When shipped YAML has no ingest section, the compiled default (3) applies.
	c := Config{}
	c.normalizeIngest()
	if c.Ingest.MaxConcurrent != compiledIngestDefaultMaxConcurrent {
		t.Fatalf("shipped override MaxConcurrent=%d want %d", c.Ingest.MaxConcurrent, compiledIngestDefaultMaxConcurrent)
	}
}

func TestIngestIndependentOfPostIngest(t *testing.T) {
	c := Config{
		PostIngest: PostIngestConfig{MaxConcurrent: 4, maxConcurrentSet: true},
	}
	c.normalizePostIngest()
	c.normalizeIngest()
	if c.Ingest.MaxConcurrent == c.PostIngest.MaxConcurrent {
		t.Fatalf("ingest MaxConcurrent (%d) must not equal post_ingest MaxConcurrent (%d)", c.Ingest.MaxConcurrent, c.PostIngest.MaxConcurrent)
	}
}
