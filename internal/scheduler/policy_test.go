package scheduler

import (
	"testing"
)

func TestPolicyDefaultsAllRegisteredTypes(t *testing.T) {
	p := PolicyDefaults()
	for name := range Registry {
		limit, ok := p.TypeConcurrency[name]
		if !ok {
			t.Fatalf("default policy missing type %q", name)
		}
		if limit <= 0 {
			t.Fatalf("default policy type %q concurrency=%d want >0", name, limit)
		}
	}
}

func TestPolicyDefaultsPriorityValues(t *testing.T) {
	p := PolicyDefaults()
	if p.AgingIntervalSec != 300 {
		t.Fatalf("AgingIntervalSec=%d want 300", p.AgingIntervalSec)
	}
	if p.AgingStep != 1 {
		t.Fatalf("AgingStep=%d want 1", p.AgingStep)
	}
	if p.RunNowAmount != 100 {
		t.Fatalf("RunNowAmount=%d want 100", p.RunNowAmount)
	}
	if p.RunNowTTLSec != 600 {
		t.Fatalf("RunNowTTLSec=%d want 600", p.RunNowTTLSec)
	}
}

func TestPolicyDefaultsValidate(t *testing.T) {
	p := PolicyDefaults()
	if err := p.Validate(); err != nil {
		t.Fatalf("PolicyDefaults().Validate(): %v", err)
	}
}

func TestPolicyMergeYAMLConcurrencyOverrides(t *testing.T) {
	p := PolicyDefaults()
	cfg := SchedulerYAMLConfig{
		TypeConcurrency: map[string]int{"ingest": 7, "scrape": 10},
	}
	p.MergeYAML(cfg)
	if p.TypeConcurrency["ingest"] != 7 {
		t.Fatalf("ingest=%d want 7", p.TypeConcurrency["ingest"])
	}
	if p.TypeConcurrency["scrape"] != 10 {
		t.Fatalf("scrape=%d want 10", p.TypeConcurrency["scrape"])
	}
	// Unchanged types retain defaults.
	if p.TypeConcurrency["poster"] != 3 {
		t.Fatalf("poster=%d want 3", p.TypeConcurrency["poster"])
	}
	if p.Provenance["concurrency.ingest"] != "yaml" {
		t.Fatalf("provenance=%q want yaml", p.Provenance["concurrency.ingest"])
	}
	if p.Provenance["concurrency.poster"] != "default" {
		t.Fatalf("provenance=%q want default", p.Provenance["concurrency.poster"])
	}
}

func TestPolicyMergeYAMLResourceCapacity(t *testing.T) {
	p := PolicyDefaults()
	cfg := SchedulerYAMLConfig{
		ResourceCapacity: map[string]int{"cpu": 8, "gpu": 2},
	}
	p.MergeYAML(cfg)
	if p.ResourceCapacity[CPU] != 8 {
		t.Fatalf("cpu=%d want 8", p.ResourceCapacity[CPU])
	}
	if p.ResourceCapacity[GPU] != 2 {
		t.Fatalf("gpu=%d want 2", p.ResourceCapacity[GPU])
	}
	if p.Provenance["resource.cpu"] != "yaml" {
		t.Fatalf("provenance=%q", p.Provenance["resource.cpu"])
	}
}

func TestPolicyMergeYAMLProviderCapacity(t *testing.T) {
	p := PolicyDefaults()
	cfg := SchedulerYAMLConfig{
		ProviderCapacity: map[string]int{"provider:openai": 5},
	}
	p.MergeYAML(cfg)
	if p.ProviderCapacity["provider:openai"] != 5 {
		t.Fatalf("provider:openai=%d want 5", p.ProviderCapacity["provider:openai"])
	}
}

func TestPolicyMergeYAMLPriorityOverrides(t *testing.T) {
	p := PolicyDefaults()
	interval := 60
	step := 2
	amount := 200
	ttl := 1200
	cfg := SchedulerYAMLConfig{
		AgingIntervalSec: &interval,
		AgingStep:        &step,
		RunNowAmount:     &amount,
		RunNowTTLSec:     &ttl,
	}
	p.MergeYAML(cfg)
	if p.AgingIntervalSec != 60 {
		t.Fatalf("AgingIntervalSec=%d want 60", p.AgingIntervalSec)
	}
	if p.AgingStep != 2 {
		t.Fatalf("AgingStep=%d want 2", p.AgingStep)
	}
	if p.RunNowAmount != 200 {
		t.Fatalf("RunNowAmount=%d want 200", p.RunNowAmount)
	}
	if p.RunNowTTLSec != 1200 {
		t.Fatalf("RunNowTTLSec=%d want 1200", p.RunNowTTLSec)
	}
	if p.Provenance["priority.aging_interval_sec"] != "yaml" {
		t.Fatalf("provenance=%q", p.Provenance["priority.aging_interval_sec"])
	}
}

func TestPolicyMergeYAMLSkipsNilValues(t *testing.T) {
	p := PolicyDefaults()
	origInterval := p.AgingIntervalSec
	cfg := SchedulerYAMLConfig{} // all nil
	p.MergeYAML(cfg)
	if p.AgingIntervalSec != origInterval {
		t.Fatalf("AgingIntervalSec changed to %d want %d", p.AgingIntervalSec, origInterval)
	}
}

func TestPolicyMergeOverrides(t *testing.T) {
	p := PolicyDefaults()
	p.MergeOverrides(map[string]int{"ingest": 10, "scan": 5})
	if p.TypeConcurrency["ingest"] != 10 {
		t.Fatalf("ingest=%d want 10", p.TypeConcurrency["ingest"])
	}
	if p.TypeConcurrency["scan"] != 5 {
		t.Fatalf("scan=%d want 5", p.TypeConcurrency["scan"])
	}
	if p.Provenance["concurrency.ingest"] != "override" {
		t.Fatalf("provenance=%q want override", p.Provenance["concurrency.ingest"])
	}
	if p.Provenance["concurrency.poster"] != "default" {
		t.Fatalf("provenance=%q want default", p.Provenance["concurrency.poster"])
	}
}

func TestPolicyOverrideWinsOverYAML(t *testing.T) {
	p := PolicyDefaults()
	cfg := SchedulerYAMLConfig{
		TypeConcurrency: map[string]int{"ingest": 7},
	}
	p.MergeYAML(cfg)
	p.MergeOverrides(map[string]int{"ingest": 10})
	if p.TypeConcurrency["ingest"] != 10 {
		t.Fatalf("ingest=%d want 10 (override wins)", p.TypeConcurrency["ingest"])
	}
	if p.Provenance["concurrency.ingest"] != "override" {
		t.Fatalf("provenance=%q want override", p.Provenance["concurrency.ingest"])
	}
}

func TestPolicyValidateUnknownTaskType(t *testing.T) {
	p := PolicyDefaults()
	p.TypeConcurrency["nonexistent_xyz"] = 1
	if err := p.Validate(); err == nil {
		t.Fatal("expected error for unknown task type")
	}
}

func TestPolicyValidateNegativeConcurrency(t *testing.T) {
	p := PolicyDefaults()
	p.TypeConcurrency["ingest"] = -1
	if err := p.Validate(); err == nil {
		t.Fatal("expected error for negative concurrency")
	}
}

func TestPolicyValidateUnknownResourceKind(t *testing.T) {
	p := PolicyDefaults()
	p.ResourceCapacity[ResourceKind("bogus")] = 1
	if err := p.Validate(); err == nil {
		t.Fatal("expected error for unknown resource kind")
	}
}

func TestPolicyValidateNegativeResourceCapacity(t *testing.T) {
	p := PolicyDefaults()
	p.ResourceCapacity[CPU] = -1
	if err := p.Validate(); err == nil {
		t.Fatal("expected error for negative resource capacity")
	}
}

func TestPolicyValidateNegativeProviderCapacity(t *testing.T) {
	p := PolicyDefaults()
	p.ProviderCapacity["provider:test"] = -1
	if err := p.Validate(); err == nil {
		t.Fatal("expected error for negative provider capacity")
	}
}

func TestPolicyValidateInvalidPriorityValues(t *testing.T) {
	cases := []struct {
		name   string
		modify func(*Policy)
	}{
		{"aging_interval_sec", func(p *Policy) { p.AgingIntervalSec = 0 }},
		{"aging_interval_sec_neg", func(p *Policy) { p.AgingIntervalSec = -1 }},
		{"aging_step", func(p *Policy) { p.AgingStep = 0 }},
		{"run_now_amount", func(p *Policy) { p.RunNowAmount = 0 }},
		{"run_now_ttl_sec", func(p *Policy) { p.RunNowTTLSec = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := PolicyDefaults()
			tc.modify(&p)
			if err := p.Validate(); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestPolicyValidateZeroConcurrencyIsPaused(t *testing.T) {
	// Zero concurrency means paused (valid), not unlimited.
	p := PolicyDefaults()
	p.TypeConcurrency["encrypt"] = 0
	if err := p.Validate(); err != nil {
		t.Fatalf("zero concurrency should be valid (paused): %v", err)
	}
}

func TestPolicyValidateZeroResourceCapacityIsUnavailable(t *testing.T) {
	// Zero resource capacity means unavailable (valid), yields explicit blocker.
	p := PolicyDefaults()
	p.ResourceCapacity[CPU] = 0
	if err := p.Validate(); err != nil {
		t.Fatalf("zero resource capacity should be valid (unavailable): %v", err)
	}
}

func TestPolicyValidateEmptyPolicy(t *testing.T) {
	// An empty policy has no registered types in TypeConcurrency, which is invalid.
	p := Policy{}
	if err := p.Validate(); err == nil {
		t.Fatal("expected error for empty policy missing registered types")
	}
}

func TestPolicyValidateEmptyOverridesApplied(t *testing.T) {
	// Empty overrides should not break validation.
	p := PolicyDefaults()
	p.MergeOverrides(map[string]int{})
	if err := p.Validate(); err != nil {
		t.Fatalf("empty overrides: %v", err)
	}
}

func TestSchedulerYAMLConfigPointerPresence(t *testing.T) {
	// Verify that nil pointer means "not set" and non-nil means "set".
	var cfg SchedulerYAMLConfig
	if cfg.AgingIntervalSec != nil {
		t.Fatal("nil pointer should mean not set")
	}
	v := 60
	cfg.AgingIntervalSec = &v
	if cfg.AgingIntervalSec == nil || *cfg.AgingIntervalSec != 60 {
		t.Fatal("pointer should be set to 60")
	}
}

func TestPolicyRuntimeOverrideClearFallsBackToYAML(t *testing.T) {
	p := PolicyDefaults()
	p.MergeYAML(SchedulerYAMLConfig{TypeConcurrency: map[string]int{"poster": 5}})
	p.MergeOverrides(map[string]int{"poster": 2})
	if p.TypeConcurrency["poster"] != 2 {
		t.Fatalf("poster=%d want 2", p.TypeConcurrency["poster"])
	}
	if p.Provenance["concurrency.poster"] != "override" {
		t.Fatalf("provenance=%q want override", p.Provenance["concurrency.poster"])
	}
	p.ClearOverride("poster")
	if p.TypeConcurrency["poster"] != 5 {
		t.Fatalf("poster=%d want 5 (YAML fallback)", p.TypeConcurrency["poster"])
	}
	if p.Provenance["concurrency.poster"] != "yaml" {
		t.Fatalf("provenance=%q want yaml", p.Provenance["concurrency.poster"])
	}
}

func TestPolicyRuntimeOverrideClearFallsBackToDefault(t *testing.T) {
	p := PolicyDefaults()
	p.MergeOverrides(map[string]int{"poster": 7})
	p.ClearOverride("poster")
	def, _ := DefaultConcurrency("poster")
	if p.TypeConcurrency["poster"] != def {
		t.Fatalf("poster=%d want default %d", p.TypeConcurrency["poster"], def)
	}
	if p.Provenance["concurrency.poster"] != "default" {
		t.Fatalf("provenance=%q want default", p.Provenance["concurrency.poster"])
	}
}

func TestPolicyRuntimeOverrideTracksLayerInputs(t *testing.T) {
	p := PolicyDefaults()
	p.MergeYAML(SchedulerYAMLConfig{TypeConcurrency: map[string]int{"ingest": 7, "poster": 5}})
	p.MergeOverrides(map[string]int{"ingest": 10})
	if p.YAMLConcurrency["ingest"] != 7 || p.YAMLConcurrency["poster"] != 5 {
		t.Fatalf("YAML layer=%v", p.YAMLConcurrency)
	}
	if p.Overrides["ingest"] != 10 {
		t.Fatalf("override layer=%v", p.Overrides)
	}
	if _, ok := p.Overrides["poster"]; ok {
		t.Fatalf("poster must not be in override layer: %v", p.Overrides)
	}
}
