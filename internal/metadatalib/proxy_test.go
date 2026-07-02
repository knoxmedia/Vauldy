package metadatalib

import "testing"

func TestProxyAllowedHost(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://img2.doubanio.com/view/photo/s_ratio_poster/public/p2907242201.jpg", true},
		{"https://movie.douban.com/poster.jpg", true},
		{"https://lain.bgm.tv/pic/cover/l/abc.jpg", true},
		{"https://image.tmdb.org/t/p/w500/x.jpg", true},
		{"https://example.com/poster.jpg", false},
		{"file:///etc/passwd", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := ProxyAllowedHost(tc.url); got != tc.want {
			t.Fatalf("ProxyAllowedHost(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}
