package scheduler

import (
	"testing"
)

// Phase 1-5 executable types that must be registered in the scheduler.
var requiredTaskTypes = []string{
	"ingest", "metadata", "scan",
	"scrape",
	"poster", "poster_repair", "thumbnail",
	"preview",
	"subtitle", "subtitle_recognize", "atrack",
	"keyframe",
	"encrypt",
	"prepare",
	"ai_analysis",
	"retirement",
	// Phase 5 types
	"lyric_recognize", "audio_analysis",
	"photo_classify", "photo_geocode", "photo_face", "image_ocr",
	"document_convert", "document_fulltext",
	"person_scrape", "artwork_cover",
	// Legacy aliases
	"doc_convert", "lyric",
}

// TestSchedulerRegistryAllTypesRegistered asserts that every Phase 1/2
// executable type has a registry entry.
func TestSchedulerRegistryAllTypesRegistered(t *testing.T) {
	for _, tt := range requiredTaskTypes {
		t.Run(tt, func(t *testing.T) {
			desc, ok := Registry[tt]
			if !ok {
				t.Fatalf("task type %q is not in Registry", tt)
			}
			if desc.TaskType != tt {
				t.Fatalf("descriptor TaskType=%q want %q", desc.TaskType, tt)
			}
		})
	}
}

// TestSchedulerRegistryUniqueNames asserts that every descriptor has a unique
// task type name and there are no empty names.
func TestSchedulerRegistryUniqueNames(t *testing.T) {
	seen := make(map[string]bool, len(Registry))
	for name, desc := range Registry {
		if name == "" {
			t.Fatal("empty task type name in registry")
		}
		if desc.TaskType != name {
			t.Fatalf("descriptor TaskType=%q does not match registry key %q", desc.TaskType, name)
		}
		if seen[name] {
			t.Fatalf("duplicate registry entry for %q", name)
		}
		seen[name] = true
	}
}

// TestSchedulerRegistryValidFamilyInheritance asserts that every descriptor
// belongs to a non-empty family.
func TestSchedulerRegistryValidFamilyInheritance(t *testing.T) {
	for name, desc := range Registry {
		if desc.Family == "" {
			t.Fatalf("descriptor %q has empty family", name)
		}
	}
}

// TestSchedulerRegistryNonnegativeRequests asserts that every resource request
// in every descriptor is nonnegative.
func TestSchedulerRegistryNonnegativeRequests(t *testing.T) {
	for name, desc := range Registry {
		for rk, count := range desc.Resources {
			if count < 0 {
				t.Fatalf("descriptor %q resource %q has negative value %d", name, rk, count)
			}
		}
	}
}

// TestSchedulerRegistryNoUnknownResources asserts that every resource kind
// used in descriptors is one of the defined constants.
func TestSchedulerRegistryNoUnknownResources(t *testing.T) {
	for name, desc := range Registry {
		for rk := range desc.Resources {
			if _, ok := AllResourceKinds[rk]; !ok {
				t.Fatalf("descriptor %q has unknown resource kind %q", name, rk)
			}
		}
	}
}

// TestSchedulerRegistryExactlyOneProfileVersion asserts that each descriptor
// has ProfileVersion >= 1.
func TestSchedulerRegistryExactlyOneProfileVersion(t *testing.T) {
	for name, desc := range Registry {
		if desc.ProfileVersion < 1 {
			t.Fatalf("descriptor %q has invalid profile version %d", name, desc.ProfileVersion)
		}
	}
}

// TestSchedulerRegistryResourceKinds asserts all six resource kinds are
// available in the AllResourceKinds set.
func TestSchedulerRegistryResourceKinds(t *testing.T) {
	want := []ResourceKind{CPU, GPU, DiskRead, DiskWrite, Network, ExternalProcess}
	for _, rk := range want {
		if _, ok := AllResourceKinds[rk]; !ok {
			t.Fatalf("AllResourceKinds missing %q", rk)
		}
	}
}

// TestSchedulerDefaultsConcurrencyValues asserts the approved default concurrency
// for key task types.
func TestSchedulerDefaultsConcurrencyValues(t *testing.T) {
	cases := map[string]int{
		"ingest":            3,
		"metadata":          3,
		"scrape":            6,
		"poster":            3,
		"preview":           2,
		"encrypt":           1,
		"atrack":            2,
		"subtitle_recognize": 1,
		"keyframe":          3,
		"prepare":           1,
		"doc_convert":       2,
		"ai_analysis":       3,
		"scan":              1,
		// Phase 5
		"lyric_recognize":   2,
		"audio_analysis":    2,
		"photo_classify":    1,
		"photo_geocode":     2,
		"photo_face":        1,
		"image_ocr":         1,
		"document_convert":  2,
		"document_fulltext": 1,
		"person_scrape":     2,
		"artwork_cover":     2,
	}
	for typ, want := range cases {
		t.Run(typ, func(t *testing.T) {
			got, ok := DefaultConcurrency(typ)
			if !ok {
				t.Fatalf("DefaultConcurrency(%q) not found", typ)
			}
			if got != want {
				t.Fatalf("DefaultConcurrency(%q)=%d want %d", typ, got, want)
			}
		})
	}
}

// TestSchedulerDefaultConcurrencyUnknown asserts that unknown types return 0.
func TestSchedulerDefaultConcurrencyUnknown(t *testing.T) {
	got, ok := DefaultConcurrency("nonexistent_type_xyz")
	if ok {
		t.Fatalf("expected ok=false for unknown type, got %d", got)
	}
	if got != 0 {
		t.Fatalf("expected 0 for unknown type, got %d", got)
	}
}

// TestSchedulerDescriptorResourceProfiles asserts specific resource profiles
// for key task types match the design.
func TestSchedulerDescriptorResourceProfiles(t *testing.T) {
	cases := []struct {
		typ       string
		resources ResourceRequest
	}{
		{"ingest", ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1}},
		{"metadata", ResourceRequest{CPU: 1, DiskRead: 1, ExternalProcess: 1}},
		{"poster", ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1, ExternalProcess: 1}},
		{"preview", ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1, ExternalProcess: 1}},
		{"encrypt", ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1}},
		{"scrape", ResourceRequest{CPU: 1, Network: 1}},
		{"ai_analysis", ResourceRequest{CPU: 1, Network: 1}},
		{"subtitle_recognize", ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1}},
		{"lyric_recognize", ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1}},
		{"audio_analysis", ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1}},
		{"photo_geocode", ResourceRequest{CPU: 1, DiskRead: 1, Network: 1}},
		{"image_ocr", ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1}},
		{"document_convert", ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1, ExternalProcess: 1}},
		{"document_fulltext", ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1, ExternalProcess: 1}},
		{"person_scrape", ResourceRequest{CPU: 1, Network: 1}},
		{"artwork_cover", ResourceRequest{CPU: 1, DiskRead: 1, DiskWrite: 1, ExternalProcess: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.typ, func(t *testing.T) {
			desc, ok := Registry[tc.typ]
			if !ok {
				t.Fatalf("type %q not in registry", tc.typ)
			}
			if len(desc.Resources) != len(tc.resources) {
				t.Fatalf("resource count=%d want %d", len(desc.Resources), len(tc.resources))
			}
			for rk, want := range tc.resources {
				got, exists := desc.Resources[rk]
				if !exists {
					t.Fatalf("missing resource %q", rk)
				}
				if got != want {
					t.Fatalf("resource %q=%d want %d", rk, got, want)
				}
			}
		})
	}
}

// TestSchedulerRegistryProviderEmpty asserts that default descriptors have
// empty provider (local execution, not provider-specific).
func TestSchedulerRegistryProviderEmpty(t *testing.T) {
	for name, desc := range Registry {
		if desc.Provider != "" {
			t.Fatalf("descriptor %q has non-empty provider %q (expected empty for default)", name, desc.Provider)
		}
	}
}

// TestValidateResourceRequest asserts that validation catches unknown kinds
// and negative values.
func TestValidateResourceRequest(t *testing.T) {
	if err := ValidateResourceRequest(ResourceRequest{CPU: 1, DiskRead: 2}); err != nil {
		t.Fatalf("valid request: %v", err)
	}
	if err := ValidateResourceRequest(ResourceRequest{ResourceKind("bogus"): 1}); err == nil {
		t.Fatal("expected error for unknown resource kind")
	}
	if err := ValidateResourceRequest(ResourceRequest{CPU: -1}); err == nil {
		t.Fatal("expected error for negative resource request")
	}
	if err := ValidateResourceRequest(ResourceRequest{}); err != nil {
		t.Fatalf("empty request should be valid: %v", err)
	}
}

// TestSchedulerRegistryAllDescriptorsHaveResources asserts every registered
// type declares at least one resource request.
func TestSchedulerRegistryAllDescriptorsHaveResources(t *testing.T) {
	for name, desc := range Registry {
		if len(desc.Resources) == 0 {
			t.Fatalf("descriptor %q has no resource requests", name)
		}
	}
}

// TestSchedulerRegistryCompiledDefaultsCoversAll asserts that the number of
// compiled defaults matches the number of registry entries.
func TestSchedulerRegistryCompiledDefaultsCoversAll(t *testing.T) {
	if len(compiledDefaults) != len(Registry) {
		t.Fatalf("compiledDefaults=%d Registry=%d count mismatch", len(compiledDefaults), len(Registry))
	}
}
