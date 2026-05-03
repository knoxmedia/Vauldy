package config

import (
	"reflect"
	"testing"
)

func TestNormalizeDRMPackagingOrder(t *testing.T) {
	t.Parallel()
	defaults := []string{"shaka", "ffmpeg"}
	if got := NormalizeDRMPackagingOrder(nil); !reflect.DeepEqual(got, defaults) {
		t.Fatalf("nil: got %v want %v", got, defaults)
	}
	if got := NormalizeDRMPackagingOrder([]string{}); !reflect.DeepEqual(got, defaults) {
		t.Fatalf("empty: got %v want %v", got, defaults)
	}
	if got := NormalizeDRMPackagingOrder([]string{"  FFMpeg  ", "Shaka", "ffmpeg"}); !reflect.DeepEqual(got, []string{"ffmpeg", "shaka"}) {
		t.Fatalf("dedupe+order: got %v", got)
	}
	if got := NormalizeDRMPackagingOrder([]string{"foo", "bar", "shaka"}); !reflect.DeepEqual(got, []string{"shaka"}) {
		t.Fatalf("drop unknown: got %v", got)
	}
}
