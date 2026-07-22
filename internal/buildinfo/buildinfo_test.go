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
	files := map[string][]string{
		"../../build.ps1":  {"git describe --tags --always", "git rev-parse HEAD", "git status --porcelain", "SOURCE_DATE_EPOCH", "internal/buildinfo.Version", "internal/buildinfo.Commit", "internal/buildinfo.BuildTime", "internal/buildinfo.Dirty"},
		"../../Dockerfile": {"SOURCE_DATE_EPOCH", "internal/buildinfo.Version", "internal/buildinfo.Commit", "internal/buildinfo.BuildTime", "internal/buildinfo.Dirty"},
	}
	for path, wants := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(string(data), want) {
				t.Errorf("%s missing %q", path, want)
			}
		}
	}
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
	files := map[string][]string{
		"../../build.ps1":  {"./cmd/buildinfo-check", "--allow-dirty", "& $buildCheck"},
		"../../Dockerfile": {"ARG ALLOW_DIRTY=false", "./cmd/buildinfo-check", "/buildinfo-check", "--allow-dirty"},
	}
	for path, wants := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(string(data), want) {
				t.Errorf("%s missing %q", path, want)
			}
		}
	}
}

func TestDockerBuildVerifiesActualGitSource(t *testing.T) {
	data, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, want := range []string{"apt-get install -y --no-install-recommends git", "test -d .git", "git rev-parse HEAD", "git status --porcelain --untracked-files=all", `"$actual_commit" != "$COMMIT"`, `"$ALLOW_DIRTY" = "false"`} {
		if !strings.Contains(src, want) {
			t.Errorf("Dockerfile missing trusted source check %q", want)
		}
	}
	ignore, err := os.ReadFile("../../.dockerignore")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(ignore), "\n") {
		if strings.TrimSpace(line) == ".git" {
			t.Error(".dockerignore excludes trusted Git metadata")
		}
	}
}

func TestPowerShellBuildUsesUniqueCheckerAndRestoresEnvironment(t *testing.T) {
	data, err := os.ReadFile("../../build.ps1")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, want := range []string{"[guid]::NewGuid()", "$PID", "$savedEnvironment", `Test-Path "Env:$name"`, "try {", "finally {", "Remove-Item $buildCheck", "Set-Item -Path \"Env:$name\"", "Remove-Item -Path \"Env:$name\""} {
		if !strings.Contains(src, want) {
			t.Errorf("build.ps1 missing cleanup/env construct %q", want)
		}
	}
	buildAt := strings.Index(src, "& go build -ldflags $ldflags -o $buildCheck")
	tryAt := strings.LastIndex(src[:buildAt], "try {")
	finallyAt := strings.Index(src[buildAt:], "finally {")
	if buildAt < 0 || tryAt < 0 || finallyAt < 0 {
		t.Errorf("checker build is not enclosed by outer try/finally")
	}
	if strings.Contains(src, "Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED") {
		t.Error("script still blindly removes caller environment")
	}
}
