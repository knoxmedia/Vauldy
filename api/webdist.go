package api

import (
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"knox-media/internal/webembed"
)

func init() {
	// Browsers reject ES module workers unless served as JavaScript (Go defaults .mjs to text/plain).
	_ = mime.AddExtensionType(".mjs", "application/javascript")
}

type webBundle struct {
	embedFS  fs.FS
	diskRoot string
}

func (b webBundle) available() bool {
	return b.embedFS != nil || b.diskRoot != ""
}

type powerPlayerStatic struct {
	diskDir string
	embedFS fs.FS
}

func (p powerPlayerStatic) available() bool {
	return p.diskDir != "" || p.embedFS != nil
}

// resolveWebBundle prefers go:embed (embedweb build tag), then a on-disk web/dist directory.
func resolveWebBundle() webBundle {
	if fsys := webembed.FS(); fsys != nil {
		return webBundle{embedFS: fsys}
	}
	if root := resolveWebDistDisk(); root != "" {
		return webBundle{diskRoot: root}
	}
	return webBundle{}
}

// resolveWebDistDisk returns web/dist from cwd or next to the running executable.
func resolveWebDistDisk() string {
	candidates := []string{"web/dist"}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "web", "dist"),
			filepath.Join(dir, "..", "web", "dist"),
		)
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			if abs, err := filepath.Abs(c); err == nil {
				return abs
			}
			return c
		}
	}
	return ""
}

func resolvePowerPlayerStatic(bundle webBundle) powerPlayerStatic {
	if bundle.embedFS != nil {
		if sub, err := fs.Sub(bundle.embedFS, "static/powerplayer6"); err == nil {
			if _, err := fs.Stat(sub, "."); err == nil {
				return powerPlayerStatic{embedFS: sub}
			}
		}
	}
	if bundle.diskRoot != "" {
		p := filepath.Join(bundle.diskRoot, "static", "powerplayer6")
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return powerPlayerStatic{diskDir: p}
		}
	}
	return powerPlayerStatic{}
}

func mountWebFrontend(r *gin.Engine, bundle webBundle) {
	if !bundle.available() {
		return
	}
	if bundle.embedFS != nil {
		if assetsFS, err := fs.Sub(bundle.embedFS, "assets"); err == nil {
			r.StaticFS("/assets", http.FS(assetsFS))
		}
		r.NoRoute(func(c *gin.Context) {
			if strings.HasPrefix(c.Request.URL.Path, "/api/") {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
			html, err := fs.ReadFile(bundle.embedFS, "index.html")
			if err != nil {
				c.Status(http.StatusNotFound)
				return
			}
			c.Data(http.StatusOK, "text/html; charset=utf-8", html)
		})
		return
	}
	r.Static("/assets", bundle.diskRoot+"/assets")
	r.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.File(bundle.diskRoot + "/index.html")
	})
}

// mountStaticRoutes serves /static/* with bundled web/dist/static/powerplayer6 taking precedence
// over cfg.Data.Static. Gin cannot register both /static/powerplayer6 and /static as Static().
func mountStaticRoutes(r gin.IRoutes, staticRoot string, pp powerPlayerStatic) {
	r.GET("/static/*filepath", func(c *gin.Context) {
		rel := strings.TrimPrefix(c.Param("filepath"), "/")
		if rel == "" || strings.Contains(rel, "..") {
			c.Status(http.StatusNotFound)
			return
		}
		if pp.available() && (rel == "powerplayer6" || strings.HasPrefix(rel, "powerplayer6/")) {
			sub := strings.TrimPrefix(rel, "powerplayer6")
			sub = strings.TrimPrefix(sub, "/")
			if pp.embedFS != nil {
				serveStaticFromFS(c, pp.embedFS, sub)
				return
			}
			serveStaticFile(c, pp.diskDir, sub)
			return
		}
		serveStaticFile(c, staticRoot, rel)
	})
}

func serveStaticFromFS(c *gin.Context, fsys fs.FS, rel string) {
	rel = filepath.ToSlash(strings.TrimPrefix(rel, "/"))
	if rel == "" || strings.Contains(rel, "..") {
		c.Status(http.StatusNotFound)
		return
	}
	f, err := fsys.Open(rel)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		c.Status(http.StatusNotFound)
		return
	}
	c.FileFromFS(rel, http.FS(fsys))
}

func serveStaticFile(c *gin.Context, root, rel string) {
	path := filepath.Join(root, filepath.FromSlash(rel))
	if !isPathUnderRoot(root, path) {
		c.Status(http.StatusNotFound)
		return
	}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		c.Status(http.StatusNotFound)
		return
	}
	c.File(path)
}

func isPathUnderRoot(root, path string) bool {
	rootAbs, err1 := filepath.Abs(root)
	pathAbs, err2 := filepath.Abs(path)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
