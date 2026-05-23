package metadatalib

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"knox-media/internal/scraper"
)

var downloadHTTP = &http.Client{Timeout: 45 * time.Second}

// PersistScrapeImages downloads remote (or copies local /uploads) artwork into the sharded library directory.
// Returns the number of image files saved.
func PersistScrapeImages(root, uploadRoot string, mediaID int64, res *scraper.ScrapeResult) (int, error) {
	if res == nil || mediaID <= 0 {
		return 0, nil
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return 0, fmt.Errorf("metadata library root not configured")
	}
	dir := MediaDir(root, mediaID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	if res.Extra == nil {
		res.Extra = map[string]any{}
	}
	localExtra, _ := res.Extra["local"].(map[string]any)
	if localExtra == nil {
		localExtra = map[string]any{}
	}

	images := collectRemoteImages(res)
	saved := 0
	var firstErr error
	for kind, raw := range images {
		ext := extFromURL(raw, kind)
		filename := kind + ext
		dest := filepath.Join(dir, filename)
		var err error
		if src, ok := resolveUploadsFile(uploadRoot, raw); ok {
			err = copyFile(src, dest)
		} else {
			err = downloadToFile(raw, dest)
		}
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", kind, err)
			}
			continue
		}
		pub := PublicURL(mediaID, filename)
		applyImageURL(res, kind, pub)
		localExtra[kind] = pub
		saved++
	}

	localExtra["dir"] = RelShardDir(mediaID)
	res.Extra["local"] = localExtra
	if saved == 0 && firstErr != nil {
		return 0, firstErr
	}
	return saved, firstErr
}

func collectRemoteImages(res *scraper.ScrapeResult) map[string]string {
	out := make(map[string]string)
	add := func(kind, raw string) {
		raw = normalizeImageURL(strings.TrimSpace(raw))
		if raw == "" || IsLocalMetadataURL(raw) {
			return
		}
		if isLocalUploadsURL(raw) || strings.HasPrefix(raw, "/static/") {
			out[kind] = raw
			return
		}
		if !isRemoteHTTPURL(raw) {
			return
		}
		out[kind] = raw
	}
	add("poster", res.Poster)
	add("backdrop", res.Backdrop)
	add("logo", res.Logo)
	if res.Extra != nil {
		for _, kind := range []string{"poster", "backdrop", "logo"} {
			if v, ok := res.Extra[kind].(string); ok {
				add(kind, v)
			}
		}
	}
	return out
}

func applyImageURL(res *scraper.ScrapeResult, kind, pub string) {
	switch kind {
	case "poster":
		res.Poster = pub
	case "backdrop":
		res.Backdrop = pub
	case "logo":
		res.Logo = pub
	}
	if res.Extra == nil {
		res.Extra = map[string]any{}
	}
	res.Extra[kind] = pub
}

func normalizeImageURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "//") {
		return "https:" + raw
	}
	return raw
}

func isRemoteHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

func isLocalUploadsURL(raw string) bool {
	return strings.HasPrefix(strings.TrimSpace(raw), "/uploads/")
}

func resolveUploadsFile(uploadRoot, raw string) (string, bool) {
	if !isLocalUploadsURL(raw) || strings.TrimSpace(uploadRoot) == "" {
		return "", false
	}
	rel := strings.TrimPrefix(strings.TrimSpace(raw), "/uploads/")
	rel = filepath.FromSlash(rel)
	src := filepath.Join(uploadRoot, rel)
	if _, err := os.Stat(src); err != nil {
		return "", false
	}
	return src, true
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dest + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	return os.Rename(tmp, dest)
}

func downloadToFile(rawURL, dest string) error {
	rawURL = normalizeImageURL(rawURL)
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "image/*,*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	setImageReferer(req, u)
	resp, err := downloadHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	return os.Rename(tmp, dest)
}

func setImageReferer(req *http.Request, u *url.URL) {
	host := strings.ToLower(u.Host)
	switch {
	case strings.Contains(host, "douban.com"), strings.Contains(host, "doubanio.com"):
		req.Header.Set("Referer", "https://movie.douban.com/")
	case strings.Contains(host, "tmdb.org"), strings.Contains(host, "themoviedb.org"):
		req.Header.Set("Referer", "https://www.themoviedb.org/")
	case strings.Contains(host, "bangumi.tv"), strings.Contains(host, "bgm.tv"):
		req.Header.Set("Referer", "https://bangumi.tv/")
	case strings.Contains(host, "fanart.tv"):
		req.Header.Set("Referer", "https://fanart.tv/")
	default:
		req.Header.Set("Referer", u.Scheme+"://"+u.Host+"/")
	}
}

func extFromURL(raw, kind string) string {
	path := raw
	if u, err := url.Parse(normalizeImageURL(raw)); err == nil && u.Path != "" {
		path = u.Path
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif":
		return ext
	}
	if kind == "logo" {
		return ".png"
	}
	return ".jpg"
}
