package doctrans

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrependPathEnv(t *testing.T) {
	got := prependPathEnv([]string{"PATH=C:\\Windows", "FOO=bar"}, `C:\lo\program`, `C:\lo\ure\bin`)
	path := ""
	for _, e := range got {
		if strings.HasPrefix(strings.ToUpper(e), "PATH=") {
			path = e[len("PATH="):]
		}
	}
	if !strings.Contains(path, `C:\lo\program`) || !strings.Contains(path, `C:\lo\ure\bin`) {
		t.Fatalf("PATH=%q", path)
	}
	if !strings.HasSuffix(path, `C:\Windows`) {
		t.Fatalf("expected original PATH preserved at end, got %q", path)
	}
}

func TestPrepareLibreOfficeCmdSetsBootstrap(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("portable LO path is Windows-specific in this repo")
	}
	root := filepath.Join("..", "..", "tools", "doctran", "LibreOfficePortable", "App", "libreoffice", "program")
	soffice := filepath.Join(root, "soffice.exe")
	if !fileExists(soffice) {
		t.Skip("portable LibreOffice not installed")
	}
	cmd := exec.Command(soffice, "--version")
	prepareLibreOfficeCmd(cmd, soffice)
	if cmd.Dir != filepath.Clean(root) {
		t.Fatalf("Dir=%q want %q", cmd.Dir, root)
	}
	var ure, uno, sal string
	for _, e := range cmd.Env {
		switch {
		case strings.HasPrefix(e, "URE_BOOTSTRAP="):
			ure = e
		case strings.HasPrefix(e, "UNO_PATH="):
			uno = e
		case strings.HasPrefix(e, "SAL_USE_VCLPLUGIN="):
			sal = e
		}
	}
	if sal != "SAL_USE_VCLPLUGIN=svp" {
		t.Fatalf("SAL=%q", sal)
	}
	if uno != "UNO_PATH="+root {
		t.Fatalf("UNO=%q", uno)
	}
	wantFundamental := filepath.Join(root, "fundamental.ini")
	if !strings.Contains(ure, filepath.ToSlash(wantFundamental)) {
		t.Fatalf("URE=%q want path %s", ure, wantFundamental)
	}
}

func TestLibreOfficePathDirs(t *testing.T) {
	tmp := t.TempDir()
	dirs := libreOfficePathDirs(tmp)
	if len(dirs) != 1 || dirs[0] != filepath.Clean(tmp) {
		t.Fatalf("dirs=%v", dirs)
	}
}
