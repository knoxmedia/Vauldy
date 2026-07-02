package playback

import "testing"

func TestParseHomeStreamQuality(t *testing.T) {
	cases := []struct {
		in             string
		auto           bool
		maxH           int
		maxBps         int64
		bitrateFFmpeg  string
		resolution     string
	}{
		{"auto", true, 0, 0, "", ""},
		{"", true, 0, 0, "", ""},
		{"1080p-30mbps", false, 1080, 30_000_000, "30000k", "1920:1080"},
		{"720p-4mbps", false, 720, 4_000_000, "4000k", "1280:720"},
		{"480p-1_5mbps", false, 480, 1_500_000, "1500k", "854:480"},
		{"4k-200mbps", false, 2160, 200_000_000, "200000k", "3840:2160"},
		{"invalid", true, 0, 0, "", ""},
	}
	for _, tc := range cases {
		got := ParseHomeStreamQuality(tc.in)
		if got.Auto != tc.auto {
			t.Fatalf("ParseHomeStreamQuality(%q).Auto = %v, want %v", tc.in, got.Auto, tc.auto)
		}
		if !tc.auto {
			if got.MaxHeight != tc.maxH || got.MaxBitrateBps != tc.maxBps || got.BitrateFFmpeg != tc.bitrateFFmpeg || got.Resolution != tc.resolution {
				t.Fatalf("ParseHomeStreamQuality(%q) = %+v, want height=%d bps=%d bitrate=%q res=%q",
					tc.in, got, tc.maxH, tc.maxBps, tc.bitrateFFmpeg, tc.resolution)
			}
		}
	}
}

func TestSourceExceedsLimit(t *testing.T) {
	limit := ParseHomeStreamQuality("1080p-30mbps")
	if SourceExceedsLimit(720, 1280, 5_000_000, limit) {
		t.Fatal("720p 5Mbps should not exceed 1080p-30mbps")
	}
	if !SourceExceedsLimit(2160, 3840, 20_000_000, limit) {
		t.Fatal("4K should exceed 1080p height cap")
	}
	if !SourceExceedsLimit(1080, 1920, 50_000_000, limit) {
		t.Fatal("1080p 50Mbps should exceed 30Mbps cap")
	}
	if SourceExceedsLimit(1080, 1920, 0, limit) {
		t.Fatal("unknown source bitrate should not trigger bitrate exceed")
	}
	if SourceExceedsLimit(2160, 3840, 0, HomeStreamLimit{Auto: true}) {
		t.Fatal("auto mode should never force exceed")
	}
}

func TestPickJITParamsExplicit(t *testing.T) {
	limit := ParseHomeStreamQuality("1080p-30mbps")
	bitrate, resolution := PickJITParams(2160, 3840, 2160, limit)
	if bitrate != "30000k" || resolution != "1920:1080" {
		t.Fatalf("PickJITParams 4K explicit = (%q,%q), want (30000k,1920:1080)", bitrate, resolution)
	}
	bitrate, resolution = PickJITParams(720, 1280, 1080, limit)
	if bitrate != "30000k" || resolution != "1280:720" {
		t.Fatalf("PickJITParams 720p explicit = (%q,%q), want (30000k,1280:720)", bitrate, resolution)
	}
}

func TestPickJITParamsAuto(t *testing.T) {
	limit := HomeStreamLimit{Auto: true}
	bitrate, resolution := PickJITParams(1080, 1920, 720, limit)
	if bitrate != "2000k" || resolution != "1280:720" {
		t.Fatalf("PickJITParams auto client cap = (%q,%q), want (2000k,1280:720)", bitrate, resolution)
	}
}
