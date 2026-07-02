package hwenc

import (
	"reflect"
	"testing"
)

func TestListAvailableHWAccel(t *testing.T) {
	sample := `
 V..... h264_nvenc           NVIDIA NVENC H.264 encoder
 V..... h264_qsv             H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10 (Intel Quick Sync Video)
 V..... h264_amf             AMD AMF H.264 Encoder
 V..... h264_vaapi           H.264/AVC (VAAPI)
 V..... libx264              libx264 H.264
`
	cases := []struct {
		name string
		ctx  hwDetectContext
		want []string
	}{
		{
			name: "hybrid nvidia amd",
			ctx:  hwDetectContext{GOOS: "windows", NvidiaPresent: true, AMDPresent: true},
			want: []string{"nvenc", "amf"},
		},
		{
			name: "hybrid nvidia intel",
			ctx:  hwDetectContext{GOOS: "windows", NvidiaPresent: true, IntelPresent: true},
			want: []string{"nvenc", "qsv"},
		},
		{
			name: "nvidia only",
			ctx:  hwDetectContext{GOOS: "windows", NvidiaPresent: true},
			want: []string{"nvenc"},
		},
		{
			name: "linux intel",
			ctx:  hwDetectContext{GOOS: "linux", IntelPresent: true, RenderNodeOK: true},
			want: []string{"qsv", "vaapi"},
		},
		{
			name: "linux amd",
			ctx:  hwDetectContext{GOOS: "linux", AMDPresent: true, RenderNodeOK: true},
			want: []string{"vaapi", "amf"},
		},
		{
			name: "windows intel only",
			ctx:  hwDetectContext{GOOS: "windows", IntelPresent: true},
			want: []string{"qsv"},
		},
		{
			name: "windows amd only",
			ctx:  hwDetectContext{GOOS: "windows", AMDPresent: true},
			want: []string{"amf"},
		},
		{
			name: "no gpu",
			ctx:  hwDetectContext{GOOS: "linux"},
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := listAvailableHWAccel(sample, tc.ctx)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestDetectHWAccelPriority(t *testing.T) {
	sample := `
 V..... h264_nvenc           NVIDIA NVENC H.264 encoder
 V..... h264_amf             AMD AMF H.264 Encoder
`
	ctx := hwDetectContext{GOOS: "windows", NvidiaPresent: true, AMDPresent: true}
	got := listAvailableHWAccel(sample, ctx)
	want := []string{"nvenc", "amf"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestHardwareAccelToEncoder(t *testing.T) {
	cases := []struct {
		in  string
		id  ID
		ok  bool
	}{
		{"nvenc", H264NVENC, true},
		{"amf", H264AMF, true},
		{"qsv", H264QSV, true},
		{"none", "", false},
		{"unknown", "", false},
	}
	for _, tc := range cases {
		id, ok := HardwareAccelToEncoder(tc.in)
		if ok != tc.ok || id != tc.id {
			t.Fatalf("HardwareAccelToEncoder(%q) = (%q, %v), want (%q, %v)", tc.in, id, ok, tc.id, tc.ok)
		}
	}
}
