package publication

import "testing"

func TestDefaultMaxAttempts(t *testing.T) {
	cases := []struct {
		step string
		want int
	}{
		{"scrape", DefaultNetworkMaxAttempts},
		{"Scrape", DefaultNetworkMaxAttempts},
		{"poster", DefaultLocalMaxAttempts},
		{"encrypt", DefaultLocalMaxAttempts},
		{"preview", DefaultLocalMaxAttempts},
		{"subtitle", DefaultLocalMaxAttempts},
		{"poster_repair", DefaultLocalMaxAttempts},
		{"prepare", DefaultLocalMaxAttempts},
		{"", DefaultLocalMaxAttempts},
	}
	for _, tc := range cases {
		if got := DefaultMaxAttempts(tc.step); got != tc.want {
			t.Fatalf("DefaultMaxAttempts(%q)=%d want %d", tc.step, got, tc.want)
		}
	}
}
