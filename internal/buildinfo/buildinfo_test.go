package buildinfo

import (
	"os"
	"strings"
	"testing"
)

func TestBuildInfoDefaults(t *testing.T) {
	got := Parse("", "", "", "", VCSInfo{})
	if got.Version != "dev" || got.Commit != "unknown" || got.BuildTime != "unknown" {
		t.Fatalf("defaults = %+v", got)
	}
	if got.Dirty || got.DirtyKnown {
		t.Fatalf("default dirty state = dirty:%v known:%v", got.Dirty, got.DirtyKnown)
	}
}

func TestBuildInfoDirtyParsing(t *testing.T) {
	tests := []struct {
		raw          string
		dirty, known bool
	}{
		{"true", true, true}, {"1", true, true}, {"false", false, true}, {"0", false, true},
		{"unexpected", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			dirty, known := ParseDirty(tt.raw)
			if dirty != tt.dirty || known != tt.known {
				t.Fatalf("got (%v,%v)", dirty, known)
			}
		})
	}
}

func TestBuildInfoValidateReleaseRejectsDirty(t *testing.T) {
	got := Parse("v1.2.3", strings.Repeat("a", 40), "2026-07-22T01:02:03Z", "true", VCSInfo{})
	if err := ValidateRelease(got, false); err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("err=%v", err)
	}
}

func TestBuildInfoRejectsVCSModified(t *testing.T) {
	got := Parse("v1.2.3", strings.Repeat("a", 40), "2026-07-22T01:02:03Z", "false", VCSInfo{Modified: true, ModifiedKnown: true})
	if err := ValidateRelease(got, false); err == nil || !strings.Contains(err.Error(), "vcs.modified") {
		t.Fatalf("err=%v", err)
	}
}

func TestBuildInfoWarnsOnInjectedVCSRevisionMismatch(t *testing.T) {
	got := Parse("v1.2.3", strings.Repeat("a", 40), "2026-07-22T01:02:03Z", "false", VCSInfo{Revision: strings.Repeat("b", 40)})
	warnings := ValidateDevelopment(got)
	if len(warnings) == 0 || !strings.Contains(strings.Join(warnings, " "), "revision mismatch") {
		t.Fatalf("warnings=%v", warnings)
	}
}

func TestBuildInfoValidateDevelopmentWarnsMissingMetadata(t *testing.T) {
	warnings := ValidateDevelopment(Parse("", "", "", "", VCSInfo{}))
	joined := strings.Join(warnings, " ")
	for _, want := range []string{"version", "commit", "build time", "dirty"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("warnings %q missing %q", joined, want)
		}
	}
}

func TestBuildInfoStringContainsStableFields(t *testing.T) {
	got := Parse("v1.2.3", "abc123", "2026-07-22T01:02:03Z", "0", VCSInfo{Revision: "abc123", Time: "2026-07-22T01:00:00Z", ModifiedKnown: true})
	text := got.String()
	for _, want := range []string{"version=v1.2.3", "commit=abc123", "build_time=2026-07-22T01:02:03Z", "dirty=false", "vcs_revision=abc123", "vcs_time=2026-07-22T01:00:00Z", "vcs_modified=false"} {
		if !strings.Contains(text, want) {
			t.Fatalf("String()=%q missing %q", text, want)
		}
	}
}

func TestBuildInfoInjectedVariables(t *testing.T) {
	if os.Getenv("KNOX_TEST_INJECTED_BUILDINFO") == "" {
		t.Skip("ldflags smoke only")
	}
	if Version != "v9.8.7" || Commit != "0123456789abcdef0123456789abcdef01234567" || BuildTime != "2026-07-22T02:03:04Z" || Dirty != "1" {
		t.Fatalf("injected values = %q %q %q %q", Version, Commit, BuildTime, Dirty)
	}
	if got := Current(); !got.Dirty || !got.DirtyKnown {
		t.Fatalf("parsed injected dirty = %+v", got)
	}
}

func TestBuildScriptsInjectAllMetadata(t *testing.T) {
	t.Skip("Knox release buildinfo wiring is not part of the Vauldy community sync")
}

func TestBuildInfoAllowDirtyStillRejectsMissingMetadata(t *testing.T) {
	got := Parse("dev", "unknown", "unknown", "unknown", VCSInfo{Modified: true, ModifiedKnown: true})
	if err := ValidateRelease(got, true); err == nil || !strings.Contains(err.Error(), "version") || !strings.Contains(err.Error(), "commit") {
		t.Fatalf("err=%v", err)
	}
}

func TestBuildInfoAllowDirtySkipsOnlyDirtySignals(t *testing.T) {
	commit := strings.Repeat("a", 40)
	got := Parse("v1.2.3", commit, "2026-07-22T01:02:03Z", "true", VCSInfo{Revision: commit, Modified: true, ModifiedKnown: true})
	if err := ValidateRelease(got, true); err != nil {
		t.Fatalf("err=%v", err)
	}
	got.VCS.Revision = strings.Repeat("b", 40)
	if err := ValidateRelease(got, true); err == nil || !strings.Contains(err.Error(), "revision mismatch") {
		t.Fatalf("err=%v", err)
	}
}

func TestBuildScriptsExecuteReleaseChecker(t *testing.T) {
	t.Skip("Knox release buildinfo wiring is not part of the Vauldy community sync")
}

func TestDockerBuildVerifiesActualGitSource(t *testing.T) {
	t.Skip("Knox release buildinfo wiring is not part of the Vauldy community sync")
}

func TestPowerShellBuildUsesUniqueCheckerAndRestoresEnvironment(t *testing.T) {
	t.Skip("Knox release buildinfo wiring is not part of the Vauldy community sync")
}
