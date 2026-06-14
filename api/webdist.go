package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

// resolveWebDist returns the frontend build directory (web/dist) when present.
// Search order: cwd, directory of the running executable, media/ sibling of exe.
func resolveWebDist() string {
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

// bundledPowerPlayerDir is web/dist/static/powerplayer6 copied during `npm run build`.
func bundledPowerPlayerDir(webDist string) string {
	if webDist == "" {
		return ""
	}
	p := filepath.Join(webDist, "static", "powerplayer6")
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		return p
	}
	return ""
}

// mountStaticRoutes serves /static/* with bundled web/dist/static/powerplayer6 taking precedence
// over cfg.Data.Static. Gin cannot register both /static/powerplayer6 and /static as Static().
func mountStaticRoutes(r gin.IRoutes, staticRoot, bundledPP string) {
	r.GET("/static/*filepath", func(c *gin.Context) {
		rel := strings.TrimPrefix(c.Param("filepath"), "/")
		if rel == "" || strings.Contains(rel, "..") {
			c.Status(http.StatusNotFound)
			return
		}
		if bundledPP != "" && (rel == "powerplayer6" || strings.HasPrefix(rel, "powerplayer6/")) {
			sub := strings.TrimPrefix(rel, "powerplayer6")
			sub = strings.TrimPrefix(sub, "/")
			serveStaticFile(c, bundledPP, sub)
			return
		}
		serveStaticFile(c, staticRoot, rel)
	})
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
