package doctrans

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"knox-media/internal/config"
)

func docTransEnabled(cfg config.DocTransConfig) bool {
	if cfg.Enabled == nil {
		return true
	}
	return *cfg.Enabled
}

func lookupOnPath() string {
	name := "soffice"
	if runtime.GOOS == "windows" {
		name = "soffice.exe"
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func findSofficeUnder(mediaRoot, rel string) string {
	root := ResolvePath(mediaRoot, rel)
	var foundExe, foundOther string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		base := strings.ToLower(d.Name())
		switch base {
		case "soffice.exe":
			foundExe = path
			return filepath.SkipAll
		case "soffice", "soffice.bin":
			if foundOther == "" {
				foundOther = path
			}
		}
		return nil
	})
	if foundExe != "" {
		return foundExe
	}
	return foundOther
}

func absSofficePath(mediaRoot, soffice string) string {
	soffice = strings.TrimSpace(soffice)
	if soffice == "" {
		return ""
	}
	if !filepath.IsAbs(soffice) {
		if mediaRoot != "" {
			soffice = ResolvePath(mediaRoot, soffice)
		}
		if abs, err := filepath.Abs(soffice); err == nil {
			soffice = abs
		}
	}
	return filepath.Clean(soffice)
}

// libreOfficeExecBinary picks the headless launcher. Portable Windows builds require soffice.com
// for --convert-to; soffice.bin exits without producing output on those installs.
func libreOfficeExecBinary(sofficePath string) string {
	dir := filepath.Dir(sofficePath)
	if runtime.GOOS == "windows" {
		if com := filepath.Join(dir, "soffice.com"); fileExists(com) {
			return com
		}
		if bin := filepath.Join(dir, "soffice.bin"); fileExists(bin) {
			return bin
		}
	}
	return sofficePath
}

func prepareLibreOfficeCmd(cmd *exec.Cmd, sofficePath string) {
	programDir := filepath.Clean(filepath.Dir(sofficePath))
	cmd.Dir = programDir

	env := os.Environ()
	env = append(env, "SAL_USE_VCLPLUGIN=svp")
	if fundamental := filepath.Join(programDir, "fundamental.ini"); fileExists(fundamental) {
		env = append(env, "URE_BOOTSTRAP=vnd.sun.star.pathname:"+filepath.ToSlash(fundamental))
	}
	env = append(env, "UNO_PATH="+programDir)
	env = prependPathEnv(env, libreOfficePathDirs(programDir)...)
	cmd.Env = env
	setHideWindow(cmd)
}

func libreOfficePathDirs(programDir string) []string {
	seen := map[string]struct{}{}
	var dirs []string
	addDir := func(p string) {
		p = filepath.Clean(p)
		if p == "" {
			return
		}
		st, err := os.Stat(p)
		if err != nil || !st.IsDir() {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		dirs = append(dirs, p)
	}
	addDir(programDir)
	for _, rel := range []string{"ure/bin", filepath.Join("..", "ure", "bin")} {
		if abs, err := filepath.Abs(filepath.Join(programDir, rel)); err == nil {
			addDir(abs)
		}
	}
	return dirs
}

func prependPathEnv(env []string, dirs ...string) []string {
	if len(dirs) == 0 {
		return env
	}
	sep := string(os.PathListSeparator)
	extra := strings.Join(dirs, sep)
	for i, e := range env {
		if strings.HasPrefix(strings.ToUpper(e), "PATH=") {
			cur := e[strings.IndexByte(e, '=')+1:]
			if cur == "" {
				env[i] = "PATH=" + extra
			} else {
				env[i] = "PATH=" + extra + sep + cur
			}
			return env
		}
	}
	return append(env, "PATH="+extra)
}

func libreOfficeHeadlessArgs(extra ...string) []string {
	args := []string{
		"--headless", "--norestore", "--nologo", "--nodefault", "--nofirststartwizard",
	}
	return append(args, extra...)
}

func profileURL(dir string) string {
	dir = filepath.ToSlash(filepath.Clean(dir))
	if runtime.GOOS == "windows" {
		if len(dir) >= 2 && dir[1] == ':' {
			return "file:///" + dir
		}
	}
	return "file://" + dir
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func trimOut(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 2000 {
		return "…" + s[len(s)-2000:]
	}
	return s
}
