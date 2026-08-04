package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWidevineProxyConfigNotExposed(t *testing.T) {
	t.Parallel()

	configType := reflect.TypeOf((*Config)(nil))
	for _, method := range []string{
		"WidevineProxyEnabled",
		"WidevineProxyHeaders",
		"WidevineProxyURL",
		"WidevineProxyTimeout",
	} {
		if _, ok := configType.MethodByName(method); ok {
			t.Fatalf("proxy helper %q should not be exposed", method)
		}
	}

	widevineType := reflect.TypeOf(WidevineConfig{})
	for _, field := range []string{
		"LicenseServerURL",
		"ExtraHeaders",
		"TimeoutSeconds",
	} {
		if _, ok := widevineType.FieldByName(field); ok {
			t.Fatalf("legacy proxy field %q should not exist", field)
		}
	}
}

// --- Task 2: Scheduler YAML configuration tests ---

func TestLoadSchedulerDefaultsWhenAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte("server: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Scheduler.concurrencySet {
		t.Fatal("scheduler concurrency should not be marked as set")
	}
	if c.Scheduler.Concurrency != nil && len(c.Scheduler.Concurrency) > 0 {
		t.Fatalf("scheduler concurrency should be empty when absent, got %v", c.Scheduler.Concurrency)
	}
}

func TestLoadSchedulerExplicitConcurrency(t *testing.T) {
	yaml := `scheduler:
  concurrency:
    ingest: 5
    poster: 4
`
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Scheduler.concurrencySet {
		t.Fatal("scheduler concurrency should be marked as set")
	}
	if c.Scheduler.Concurrency["ingest"] != 5 {
		t.Fatalf("ingest=%d want 5", c.Scheduler.Concurrency["ingest"])
	}
	if c.Scheduler.Concurrency["poster"] != 4 {
		t.Fatalf("poster=%d want 4", c.Scheduler.Concurrency["poster"])
	}
}

func TestLoadSchedulerExplicitZeroIsMeaningful(t *testing.T) {
	yaml := `scheduler:
  concurrency:
    encrypt: 0
`
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Scheduler.Concurrency["encrypt"] != 0 {
		t.Fatalf("encrypt=%d want 0 (explicit zero should survive)", c.Scheduler.Concurrency["encrypt"])
	}
}

func TestLoadSchedulerResources(t *testing.T) {
	yaml := `scheduler:
  resources:
    cpu: 8
    gpu: 2
  providers:
    "provider:openai": 5
`
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Scheduler.resourcesSet {
		t.Fatal("scheduler resources should be marked as set")
	}
	if c.Scheduler.Resources["cpu"] != 8 {
		t.Fatalf("cpu=%d want 8", c.Scheduler.Resources["cpu"])
	}
	if c.Scheduler.Resources["gpu"] != 2 {
		t.Fatalf("gpu=%d want 2", c.Scheduler.Resources["gpu"])
	}
	if c.Scheduler.Providers["provider:openai"] != 5 {
		t.Fatalf("provider:openai=%d want 5", c.Scheduler.Providers["provider:openai"])
	}
}

func TestLoadSchedulerPriority(t *testing.T) {
	yaml := `scheduler:
  priority:
    aging_interval_sec: 60
    aging_step: 2
    run_now_amount: 200
    run_now_ttl_sec: 1200
`
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Scheduler.Priority.agingIntervalSet {
		t.Fatal("aging_interval_sec should be marked as set")
	}
	if *c.Scheduler.Priority.AgingIntervalSec != 60 {
		t.Fatalf("aging_interval_sec=%d want 60", *c.Scheduler.Priority.AgingIntervalSec)
	}
	if *c.Scheduler.Priority.AgingStep != 2 {
		t.Fatalf("aging_step=%d want 2", *c.Scheduler.Priority.AgingStep)
	}
	if *c.Scheduler.Priority.RunNowAmount != 200 {
		t.Fatalf("run_now_amount=%d want 200", *c.Scheduler.Priority.RunNowAmount)
	}
	if *c.Scheduler.Priority.RunNowTTLSec != 1200 {
		t.Fatalf("run_now_ttl_sec=%d want 1200", *c.Scheduler.Priority.RunNowTTLSec)
	}
}

func TestLoadSchedulerNegativeValueRejected(t *testing.T) {
	cases := []struct {
		name, yaml string
	}{
		{"negative_concurrency", "scheduler:\n  concurrency:\n    ingest: -1\n"},
		{"negative_resource", "scheduler:\n  resources:\n    cpu: -1\n"},
		{"negative_provider", "scheduler:\n  providers:\n    \"provider:test\": -1\n"},
		{"zero_aging_interval", "scheduler:\n  priority:\n    aging_interval_sec: 0\n"},
		{"zero_aging_step", "scheduler:\n  priority:\n    aging_step: 0\n"},
		{"zero_run_now_amount", "scheduler:\n  priority:\n    run_now_amount: 0\n"},
		{"zero_run_now_ttl", "scheduler:\n  priority:\n    run_now_ttl_sec: 0\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestPostIngestLegacyTranslationToScheduler(t *testing.T) {
	// When scheduler section is absent, post_ingest limits should translate
	// to scheduler concurrency keys.
	yaml := `post_ingest:
  max_concurrent: 4
  poster_max_concurrent: 2
  preview_max_concurrent: 1
  subtitle_max_concurrent: 1
  subtitle_timeout_realtime_factor: 2.0
`
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Scheduler.Concurrency["poster"] != 2 {
		t.Fatalf("poster=%d want 2 (from poster_max_concurrent)", c.Scheduler.Concurrency["poster"])
	}
	if c.Scheduler.Concurrency["preview"] != 1 {
		t.Fatalf("preview=%d want 1 (from preview_max_concurrent)", c.Scheduler.Concurrency["preview"])
	}
	if c.Scheduler.Concurrency["subtitle"] != 1 {
		t.Fatalf("subtitle=%d want 1 (from subtitle_max_concurrent)", c.Scheduler.Concurrency["subtitle"])
	}
}

func TestSchedulerYAMLWinsOverLegacyPostIngest(t *testing.T) {
	// When both scheduler and post_ingest define the same key, scheduler wins.
	yaml := `scheduler:
  concurrency:
    poster: 10
post_ingest:
  poster_max_concurrent: 2
  preview_max_concurrent: 1
  subtitle_max_concurrent: 1
  max_concurrent: 4
  subtitle_timeout_realtime_factor: 2.0
`
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Scheduler.Concurrency["poster"] != 10 {
		t.Fatalf("poster=%d want 10 (scheduler wins over post_ingest)", c.Scheduler.Concurrency["poster"])
	}
}

func TestSchedulerConfigEnabledDetection(t *testing.T) {
	cases := []struct {
		name  string
		yaml  string
		want  bool
	}{
		{"absent", "server: {}\n", false},
		{"concurrency_only", "scheduler:\n  concurrency:\n    ingest: 3\n", true},
		{"resources_only", "scheduler:\n  resources:\n    cpu: 4\n", true},
		{"priority_only", "scheduler:\n  priority:\n    aging_interval_sec: 60\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := c.Scheduler.SchedulerEnabled(); got != tc.want {
				t.Fatalf("SchedulerEnabled()=%v want %v", got, tc.want)
			}
		})
	}
}

func TestShippedConfigsSchedulerSection(t *testing.T) {
	for _, path := range []string{"default/config.yml", filepath.Join("..", "..", "config.yml")} {
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Load(%s): %v", path, err)
		}
		// Shipped configs should parse without error even without scheduler section.
		if err := cfg.Scheduler.Validate(); err != nil {
			t.Fatalf("%s scheduler validate: %v", path, err)
		}
	}
}
