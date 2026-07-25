package buildinfo

import (
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
)

// These variables are intentionally strings so release builds can inject them
// with go build -ldflags -X. Direct developer builds use explicit defaults.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
	Dirty     = "unknown"
)

type VCSInfo struct {
	Revision      string
	Time          string
	Modified      bool
	ModifiedKnown bool
}

type Info struct {
	Version    string
	Commit     string
	BuildTime  string
	Dirty      bool
	DirtyKnown bool
	VCS        VCSInfo
}

func ParseDirty(raw string) (dirty, known bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	case "", "unknown":
		return false, false
	default:
		// Unknown injected values are treated as dirty for release safety.
		return true, false
	}
}

func Parse(version, commit, buildTime, dirty string, vcs VCSInfo) Info {
	version = strings.TrimSpace(version)
	if version == "" || version == "(devel)" || version == "unknown" {
		version = "dev"
	}
	commit = strings.TrimSpace(commit)
	if commit == "" {
		commit = "unknown"
	}
	buildTime = strings.TrimSpace(buildTime)
	if buildTime == "" {
		buildTime = "unknown"
	}
	parsedDirty, dirtyKnown := ParseDirty(dirty)
	return Info{Version: version, Commit: commit, BuildTime: buildTime, Dirty: parsedDirty, DirtyKnown: dirtyKnown, VCS: vcs}
}

func Current() Info { return Parse(Version, Commit, BuildTime, Dirty, ReadVCS()) }

func ReadVCS() VCSInfo {
	out := VCSInfo{}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return out
	}
	for _, setting := range bi.Settings {
		switch setting.Key {
		case "vcs.revision":
			out.Revision = setting.Value
		case "vcs.time":
			out.Time = setting.Value
		case "vcs.modified":
			out.Modified, out.ModifiedKnown = ParseDirty(setting.Value)
		}
	}
	return out
}

func (i Info) String() string {
	return fmt.Sprintf("version=%s commit=%s build_time=%s dirty=%s vcs_revision=%s vcs_time=%s vcs_modified=%s",
		field(i.Version), field(i.Commit), field(i.BuildTime), boolField(i.Dirty, i.DirtyKnown),
		field(i.VCS.Revision), field(i.VCS.Time), boolField(i.VCS.Modified, i.VCS.ModifiedKnown))
}

func ValidateRelease(i Info, allowDirty bool) error {
	var problems []string
	if i.Version == "" || i.Version == "dev" || i.Version == "unknown" {
		problems = append(problems, "version is missing")
	}
	if i.Commit == "" || i.Commit == "unknown" {
		problems = append(problems, "commit is missing")
	}
	if i.BuildTime == "" || i.BuildTime == "unknown" {
		problems = append(problems, "build time is missing")
	}
	if !allowDirty {
		if !i.DirtyKnown {
			problems = append(problems, "dirty state is unknown")
		} else if i.Dirty {
			problems = append(problems, "dirty build")
		}
		if i.VCS.ModifiedKnown && i.VCS.Modified {
			problems = append(problems, "vcs.modified=true")
		}
	}
	if mismatch(i) {
		problems = append(problems, "injected/VCS revision mismatch")
	}
	if len(problems) != 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func ValidateDevelopment(i Info) []string {
	var warnings []string
	if i.Version == "" || i.Version == "dev" || i.Version == "unknown" {
		warnings = append(warnings, "version metadata missing")
	}
	if i.Commit == "" || i.Commit == "unknown" {
		warnings = append(warnings, "commit metadata missing")
	}
	if i.BuildTime == "" || i.BuildTime == "unknown" {
		warnings = append(warnings, "build time metadata missing")
	}
	if !i.DirtyKnown {
		warnings = append(warnings, "dirty metadata unknown")
	}
	if i.VCS.ModifiedKnown && i.VCS.Modified {
		warnings = append(warnings, "Go VCS reports vcs.modified=true")
	}
	if mismatch(i) {
		warnings = append(warnings, "injected/VCS revision mismatch")
	}
	return warnings
}

func mismatch(i Info) bool {
	return i.Commit != "" && i.Commit != "unknown" && i.VCS.Revision != "" && i.Commit != i.VCS.Revision
}

func field(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return strings.TrimSpace(value)
}

func boolField(value, known bool) string {
	if !known {
		return "unknown"
	}
	if value {
		return "true"
	}
	return "false"
}
