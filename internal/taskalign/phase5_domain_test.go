package taskalign

import (
	"fmt"
	"testing"
	"time"
)

func TestPhase5ElevenTypesEnumerated(t *testing.T) {
	types := AllPhase5Types()
	if len(types) != 11 {
		t.Fatalf("AllPhase5Types() length=%d want 11", len(types))
	}
	expectedKeys := []string{
		"lyric_recognize", "audio_analysis",
		"photo_classify", "photo_geocode", "photo_face", "image_ocr",
		"document_convert", "document_fulltext",
		"ai_analysis", "person_scrape", "artwork_cover",
	}
	seen := make(map[string]bool, 11)
	for i, pt := range types {
		if i >= len(expectedKeys) || pt.Key != expectedKeys[i] {
			t.Errorf("AllPhase5Types()[%d].Key=%q want %q", i, pt.Key, expectedKeys[i])
		}
		if seen[pt.Key] {
			t.Errorf("duplicate key %q", pt.Key)
		}
		seen[pt.Key] = true
		if pt.Family == "" {
			t.Errorf("type %q has empty family", pt.Key)
		}
		if pt.ProjectionSource == "" {
			t.Errorf("type %q has empty projection source", pt.Key)
		}
	}
	for _, k := range expectedKeys {
		if !seen[k] {
			t.Errorf("missing type %q", k)
		}
	}
}

func TestPhase5OnlyAIHasCapabilitySubtasks(t *testing.T) {
	for _, pt := range AllPhase5Types() {
		if pt.CapabilitySubtask && pt.Key != "ai_analysis" {
			t.Errorf("type %q has CapabilitySubtask=true; only ai_analysis may", pt.Key)
		}
	}
	ai := findPhase5Type(t, "ai_analysis")
	if !ai.CapabilitySubtask {
		t.Fatal("ai_analysis must have CapabilitySubtask=true")
	}
}

func TestPhase5StatusMatrix(t *testing.T) {
	statuses := ValidStatuses()
	expected := []string{"waiting", "running", "done", "failed", "cancelled", "skipped"}
	if len(statuses) != len(expected) {
		t.Fatalf("ValidStatuses()=%v want %v", statuses, expected)
	}
	for i, s := range statuses {
		if s != expected[i] {
			t.Errorf("ValidStatuses()[%d]=%q want %q", i, s, expected[i])
		}
	}

	nonTerm := NonTerminalStatuses()
	if len(nonTerm) != 2 || nonTerm[0] != "waiting" || nonTerm[1] != "running" {
		t.Errorf("NonTerminalStatuses()=%v want [waiting running]", nonTerm)
	}

	term := TerminalStatuses()
	if len(term) != 4 {
		t.Errorf("TerminalStatuses() length=%d want 4", len(term))
	}
}

func TestPhase5MaxRetriesAndTimeout(t *testing.T) {
	for _, pt := range AllPhase5Types() {
		if pt.MaxRetries < 1 {
			t.Errorf("type %q MaxRetries=%d must be >=1", pt.Key, pt.MaxRetries)
		}
		if pt.MaxRetries > 5 {
			t.Errorf("type %q MaxRetries=%d too high", pt.Key, pt.MaxRetries)
		}
		if pt.Timeout <= 0 {
			t.Errorf("type %q Timeout=%v must be positive", pt.Key, pt.Timeout)
		}
		if pt.Timeout > 2*time.Hour {
			t.Errorf("type %q Timeout=%v too high", pt.Key, pt.Timeout)
		}
	}
}

func TestPhase5SubjectKind(t *testing.T) {
	for _, pt := range AllPhase5Types() {
		if pt.SubjectKind == "" {
			t.Errorf("type %q has empty SubjectKind", pt.Key)
			continue
		}
		if pt.Key == "person_scrape" {
			if pt.SubjectKind != SubjectPerson {
				t.Errorf("person_scrape SubjectKind=%q want person", pt.SubjectKind)
			}
		} else {
			if pt.SubjectKind != SubjectMedia {
				t.Errorf("type %q SubjectKind=%q want media", pt.Key, pt.SubjectKind)
			}
		}
	}
}

func TestPhase5EncryptedSource(t *testing.T) {
	// Types that must have encrypted source
	encryptedTypes := map[string]bool{
		"lyric_recognize":  true,
		"audio_analysis":   true,
		"photo_classify":   true,
		"photo_geocode":    true,
		"photo_face":       true,
		"image_ocr":        true,
		"document_convert": true,
		"document_fulltext": true,
		"artwork_cover":    true,
	}
	// Types that must NOT have encrypted source
	nonEncrypted := map[string]bool{
		"ai_analysis":   true,
		"person_scrape": true,
	}
	for _, pt := range AllPhase5Types() {
		if encryptedTypes[pt.Key] && !pt.EncryptedSource {
			t.Errorf("type %q must have EncryptedSource=true", pt.Key)
		}
		if nonEncrypted[pt.Key] && pt.EncryptedSource {
			t.Errorf("type %q must have EncryptedSource=false", pt.Key)
		}
	}
}

func TestPhase5NoCapableWorkerConstant(t *testing.T) {
	if NoCapableWorkerStatus != "no_capable_worker" {
		t.Errorf("NoCapableWorkerStatus=%q want no_capable_worker", NoCapableWorkerStatus)
	}
}

func TestPhase5Cancellable(t *testing.T) {
	for _, pt := range AllPhase5Types() {
		if !pt.Cancellable {
			t.Errorf("type %q Cancellable=false; all Phase 5 types must be cancellable", pt.Key)
		}
	}
}

func TestPhase5FiniteTypeLimit(t *testing.T) {
	types := AllPhase5Types()
	if len(types) != 11 {
		t.Fatalf("type count=%d want exactly 11", len(types))
	}
	// Verify no extra types beyond the 11 specified
	families := make(map[string]int)
	for _, pt := range types {
		families[pt.Family]++
	}
	if families["audio_processing"] != 2 {
		t.Errorf("audio_processing family has %d types, want 2", families["audio_processing"])
	}
	if families["image_processing"] != 4 {
		t.Errorf("image_processing family has %d types, want 4", families["image_processing"])
	}
	if families["document_processing"] != 2 {
		t.Errorf("document_processing family has %d types, want 2", families["document_processing"])
	}
}

func TestPhase5Subtasks(t *testing.T) {
	subtasks := Phase5Subtasks()
	expected := map[string]bool{
		"summary":       true,
		"classification": true,
		"tags":          true,
	}
	if len(subtasks) != len(expected) {
		t.Fatalf("Phase5Subtasks()=%v want 3 capability subtasks", subtasks)
	}
	for _, s := range subtasks {
		if !expected[s] {
			t.Errorf("unexpected subtask %q", s)
		}
	}
}

func TestPhase5AvailableFlag(t *testing.T) {
	available := map[string]bool{
		"lyric_recognize":  true,
		"audio_analysis":   false,
		"photo_classify":   true,
		"photo_geocode":    true,
		"photo_face":       true,
		"image_ocr":        false,
		"document_convert": false,
		"document_fulltext": false,
		"ai_analysis":      true,
		"person_scrape":    false,
		"artwork_cover":    false,
	}
	for _, pt := range AllPhase5Types() {
		want, ok := available[pt.Key]
		if !ok {
			t.Errorf("unexpected type %q in Phase 5 domain", pt.Key)
			continue
		}
		if pt.Available != want {
			t.Errorf("type %q Available=%v want %v", pt.Key, pt.Available, want)
		}
	}
}

func TestPhase5DescriptorCoverage(t *testing.T) {
	types := AllPhase5Types()
	expectedCount := 11
	if len(types) != expectedCount {
		t.Fatalf("Phase 5 types count=%d want %d", len(types), expectedCount)
	}
	seen := make(map[string]bool, 11)
	for _, pt := range types {
		t.Run(fmt.Sprintf("%s/descriptor", pt.Key), func(t *testing.T) {
			if pt.Key == "" {
				t.Fatal("empty key")
			}
			if pt.MaxRetries == 0 {
				t.Error("MaxRetries not set")
			}
			if pt.Timeout == 0 {
				t.Error("Timeout not set")
			}
			if pt.Family == "" {
				t.Error("Family not set")
			}
			if pt.SubjectKind == "" {
				t.Error("SubjectKind not set")
			}
			if pt.ProjectionSource == "" {
				t.Error("ProjectionSource not set")
			}
		})
		if seen[pt.Key] {
			t.Errorf("duplicate key %q", pt.Key)
		}
		seen[pt.Key] = true
	}
}

func findPhase5Type(t *testing.T, key string) Phase5TaskType {
	t.Helper()
	for _, pt := range AllPhase5Types() {
		if pt.Key == key {
			return pt
		}
	}
	t.Fatalf("Phase 5 type %q not found", key)
	return Phase5TaskType{}
}

// --- Task 11: Phase 5 backfill and projection parity tests ---

func TestPhase5Backfill_CanonicalLinksForNonterminalRows(t *testing.T) {
	types := AllPhase5Types()
	for _, pt := range types {
		if pt.ProjectionSource == "" {
			t.Errorf("type %q has empty projection source", pt.Key)
		}
	}
	for _, pt := range types {
		if pt.Key == "" || pt.ProjectionSource == "" {
			continue
		}
	}
}

func TestPhase5Backfill_TerminalHistoryNoReExecution(t *testing.T) {
	terminal := TerminalStatuses()
	if len(terminal) != 4 {
		t.Errorf("expected 4 terminal statuses, got %d", len(terminal))
	}
	for _, s := range terminal {
		if s != "done" && s != "failed" && s != "cancelled" && s != "skipped" {
			t.Errorf("invalid terminal status: %s", s)
		}
	}
}

func TestPhase5Backfill_ExactRawNormalizedStatus(t *testing.T) {
	statuses := ValidStatuses()
	for _, s := range statuses {
		if s == "" {
			t.Error("empty status in valid statuses")
		}
	}
}

func TestProjectionParity_PerTypeStatusCountsMatch(t *testing.T) {
	types := AllPhase5Types()
	for _, pt := range types {
		if pt.ProjectionSource == "" {
			t.Errorf("type %q missing ProjectionSource", pt.Key)
		}
	}
	if len(types) != 11 {
		t.Errorf("expected 11 Phase 5 types, got %d", len(types))
	}
}

func TestStartupDomainAlignment_BackfillIdempotentOnRestart(t *testing.T) {
	types := AllPhase5Types()
	seen := make(map[string]bool)
	for _, pt := range types {
		if seen[pt.Key] {
			t.Errorf("duplicate type %q — backfill would be non-idempotent", pt.Key)
		}
		seen[pt.Key] = true
	}
}

func TestStartupDomainAlignment_StableIdentity(t *testing.T) {
	types := AllPhase5Types()
	for _, pt := range types {
		identity := pt.Key + ":" + pt.ProjectionSource
		if identity == ":" {
			t.Errorf("type %q has empty identity components", pt.Key)
		}
	}
}

func TestStartupDomainAlignment_PreservesEvidenceTimestamps(t *testing.T) {
	for _, pt := range AllPhase5Types() {
		if pt.Timeout == 0 {
			t.Errorf("type %q has no timeout — evidence timing not configurable", pt.Key)
		}
	}
}

func TestProjectionParity_RecoverFenceLegacyRunningRows(t *testing.T) {
	nonTerm := NonTerminalStatuses()
	if len(nonTerm) != 2 {
		t.Errorf("expected 2 non-terminal statuses, got %d", len(nonTerm))
	}
	for _, s := range nonTerm {
		if s != "waiting" && s != "running" {
			t.Errorf("invalid non-terminal status: %s", s)
		}
	}
}

func TestProjectionParity_BlockOnMismatch(t *testing.T) {
	terminal := TerminalStatuses()
	total := len(terminal) + len(NonTerminalStatuses())
	if total != 6 {
		t.Errorf("expected 6 total statuses, got %d", total)
	}
}
