package scheduler

import (
	"fmt"
	"maps"
)

// Policy holds the validated, effective scheduler configuration.
// It merges compiled defaults, YAML configuration, and optional database overrides.
type Policy struct {
	// TypeConcurrency maps task type name to its effective concurrency limit.
	TypeConcurrency map[string]int
	// ResourceCapacity maps resource kind to its effective token budget.
	ResourceCapacity map[ResourceKind]int
	// ProviderCapacity maps provider key (e.g. "provider:openai") to its effective token budget.
	ProviderCapacity map[string]int
	// AgingIntervalSec is the number of seconds between aging steps.
	AgingIntervalSec int
	// AgingStep is the priority increment per aging interval.
	AgingStep int
	// RunNowAmount is the priority boost applied by run-now.
	RunNowAmount int
	// RunNowTTLSec is how long a run-now boost remains active (seconds).
	RunNowTTLSec int
	// Provenance tracks the source of each value: "default", "yaml", or "override".
	Provenance map[string]string
	// YAMLConcurrency records the concurrency values supplied by YAML so a
	// cleared database override can fall back to the YAML value instead of the
	// compiled default.
	YAMLConcurrency map[string]int
	// Overrides records the active database override concurrency values. A key
	// present here wins over YAML and compiled defaults.
	Overrides map[string]int
}

// clone returns a deep copy of the policy's mutable maps.
func (p Policy) clone() Policy {
	cp := p
	cp.TypeConcurrency = maps.Clone(p.TypeConcurrency)
	cp.ResourceCapacity = maps.Clone(p.ResourceCapacity)
	cp.ProviderCapacity = maps.Clone(p.ProviderCapacity)
	cp.Provenance = maps.Clone(p.Provenance)
	cp.YAMLConcurrency = maps.Clone(p.YAMLConcurrency)
	cp.Overrides = maps.Clone(p.Overrides)
	return cp
}

// PolicyDefaults returns a Policy populated entirely from compiled defaults.
func PolicyDefaults() Policy {
	tc := make(map[string]int, len(Registry))
	prov := make(map[string]string, len(Registry))
	for name, desc := range Registry {
		if cd, ok := DefaultConcurrency(name); ok {
			tc[name] = cd
			prov["concurrency."+name] = "default"
		}
		_ = desc
	}
	return Policy{
		TypeConcurrency:  tc,
		ResourceCapacity: make(map[ResourceKind]int),
		ProviderCapacity: make(map[string]int),
		AgingIntervalSec: 300,
		AgingStep:        1,
		RunNowAmount:     100,
		RunNowTTLSec:     600,
		Provenance:       prov,
	}
}

// MergeYAML applies YAML scheduler configuration onto the policy.
// YAML values override compiled defaults. Only explicitly provided (non-nil)
// values are applied.
func (p *Policy) MergeYAML(cfg SchedulerYAMLConfig) {
	if p.Provenance == nil {
		p.Provenance = make(map[string]string)
	}
	if p.YAMLConcurrency == nil {
		p.YAMLConcurrency = make(map[string]int)
	}
	// Type concurrency overrides.
	if cfg.TypeConcurrency != nil {
		for typ, n := range cfg.TypeConcurrency {
			p.TypeConcurrency[typ] = n
			p.YAMLConcurrency[typ] = n
			p.Provenance["concurrency."+typ] = "yaml"
		}
	}
	// Resource capacity overrides.
	if cfg.ResourceCapacity != nil {
		for rk, n := range cfg.ResourceCapacity {
			p.ResourceCapacity[ResourceKind(rk)] = n
			p.Provenance["resource."+rk] = "yaml"
		}
	}
	// Provider capacity overrides.
	if cfg.ProviderCapacity != nil {
		for pk, n := range cfg.ProviderCapacity {
			p.ProviderCapacity[pk] = n
			p.Provenance["provider."+pk] = "yaml"
		}
	}
	// Priority overrides.
	if cfg.AgingIntervalSec != nil {
		p.AgingIntervalSec = *cfg.AgingIntervalSec
		p.Provenance["priority.aging_interval_sec"] = "yaml"
	}
	if cfg.AgingStep != nil {
		p.AgingStep = *cfg.AgingStep
		p.Provenance["priority.aging_step"] = "yaml"
	}
	if cfg.RunNowAmount != nil {
		p.RunNowAmount = *cfg.RunNowAmount
		p.Provenance["priority.run_now_amount"] = "yaml"
	}
	if cfg.RunNowTTLSec != nil {
		p.RunNowTTLSec = *cfg.RunNowTTLSec
		p.Provenance["priority.run_now_ttl_sec"] = "yaml"
	}
}

// SchedulerYAMLConfig is the deserialized scheduler section from config.yml.
// Pointer fields enable presence-aware decoding: nil means absent.
type SchedulerYAMLConfig struct {
	TypeConcurrency  map[string]int    `yaml:"concurrency"`
	ResourceCapacity map[string]int    `yaml:"resources"`
	ProviderCapacity map[string]int    `yaml:"providers"`
	AgingIntervalSec *int              `yaml:"aging_interval_sec"`
	AgingStep        *int              `yaml:"aging_step"`
	RunNowAmount     *int              `yaml:"run_now_amount"`
	RunNowTTLSec     *int              `yaml:"run_now_ttl_sec"`
}

// MergeOverrides applies database overrides onto the policy.
// Database overrides win over YAML and compiled defaults.
func (p *Policy) MergeOverrides(overrides map[string]int) {
	if p.Provenance == nil {
		p.Provenance = make(map[string]string)
	}
	if p.Overrides == nil {
		p.Overrides = make(map[string]int)
	}
	for key, val := range overrides {
		p.TypeConcurrency[key] = val
		p.Overrides[key] = val
		p.Provenance["concurrency."+key] = "override"
	}
}

// ClearOverride removes a database override for taskType. The effective value
// falls back to the YAML value when one was supplied, otherwise to the compiled
// default.
func (p *Policy) ClearOverride(taskType string) {
	if p.Overrides == nil {
		p.Overrides = make(map[string]int)
	}
	delete(p.Overrides, taskType)
	if yamlVal, ok := p.YAMLConcurrency[taskType]; ok {
		p.TypeConcurrency[taskType] = yamlVal
		p.Provenance["concurrency."+taskType] = "yaml"
		return
	}
	if def, ok := DefaultConcurrency(taskType); ok {
		p.TypeConcurrency[taskType] = def
		p.Provenance["concurrency."+taskType] = "default"
		return
	}
	delete(p.TypeConcurrency, taskType)
	delete(p.Provenance, "concurrency."+taskType)
}

// Validate checks the complete effective policy for correctness.
// Returns an error describing the first violation found.
func (p Policy) Validate() error {
	// Validate type concurrency: every registered type must have a limit.
	for name := range Registry {
		limit, ok := p.TypeConcurrency[name]
		if !ok {
			return fmt.Errorf("scheduler: registered type %q has no concurrency limit", name)
		}
		if limit < 0 {
			return fmt.Errorf("scheduler: type %q has negative concurrency %d", name, limit)
		}
	}
	// Validate no unknown types in concurrency map.
	for name := range p.TypeConcurrency {
		if _, ok := Registry[name]; !ok {
			return fmt.Errorf("scheduler: unknown task type %q in concurrency map", name)
		}
	}
	// Validate resource capacity.
	for rk, cap := range p.ResourceCapacity {
		if _, ok := AllResourceKinds[rk]; !ok {
			return fmt.Errorf("scheduler: unknown resource kind %q in capacity", rk)
		}
		if cap < 0 {
			return fmt.Errorf("scheduler: resource %q has negative capacity %d", rk, cap)
		}
	}
	// Validate provider capacity.
	for pk, cap := range p.ProviderCapacity {
		if cap < 0 {
			return fmt.Errorf("scheduler: provider %q has negative capacity %d", pk, cap)
		}
	}
	// Validate priority settings.
	if p.AgingIntervalSec <= 0 {
		return fmt.Errorf("scheduler: aging_interval_sec must be positive, got %d", p.AgingIntervalSec)
	}
	if p.AgingStep <= 0 {
		return fmt.Errorf("scheduler: aging_step must be positive, got %d", p.AgingStep)
	}
	if p.RunNowAmount <= 0 {
		return fmt.Errorf("scheduler: run_now_amount must be positive, got %d", p.RunNowAmount)
	}
	if p.RunNowTTLSec <= 0 {
		return fmt.Errorf("scheduler: run_now_ttl_sec must be positive, got %d", p.RunNowTTLSec)
	}
	return nil
}
