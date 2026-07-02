package metadatalib

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxProxyImageBytes = 15 << 20 // 15 MiB

// ProxyAllowedHost reports whether rawURL may be fetched through the authenticated image proxy.
func ProxyAllowedHost(rawURL string) bool {
	u, err := url.Parse(normalizeImageURL(rawURL))
	if err != nil || u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return proxyHostAllowed(strings.ToLower(u.Hostname()))
}

func proxyHostAllowed(host string) bool {
	switch {
	case strings.Contains(host, "douban.com"), strings.Contains(host, "doubanio.com"):
		return true
	case strings.Contains(host, "bangumi.tv"), strings.Contains(host, "bgm.tv"):
		return true
	case strings.Contains(host, "fanart.tv"):
		return true
	case strings.Contains(host, "tmdb.org"), strings.Contains(host, "themoviedb.org"):
		return true
	default:
		return false
	}
}

// StreamRemoteImage fetches a remote image with provider-specific Referer headers and writes it to w.
func StreamRemoteImage(w http.ResponseWriter, rawURL string) error {
	rawURL = normalizeImageURL(rawURL)
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	if !proxyHostAllowed(strings.ToLower(u.Hostname())) {
		return fmt.Errorf("host not allowed")
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

	ct := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if ct == "" || !strings.HasPrefix(strings.ToLower(ct), "image/") {
		ct = "image/jpeg"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, err = io.Copy(w, io.LimitReader(resp.Body, maxProxyImageBytes))
	return err
}
