package hwenc

import (
	"os/exec"
	"reflect"
	"strings"
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

func TestHardwareDecoderAvailableInList(t *testing.T) {
	// 类似 ffmpeg -decoders 的输出片段。
	decoders := strings.ToLower(`
 V..... av1_cuvid            Nvidia CUVID AV1 decoder (codec av1)
 V..... h264_cuvid           Nvidia CUVID H264 decoder (codec h264)
 V..... hevc_cuvid           Nvidia CUVID HEVC decoder (codec hevc)
 VF...D rv40                 RealVideo 4.0
 V..... vc1_cuvid            Nvidia CUVID VC1 decoder (codec vc1)
 V..... vp9_cuvid            Nvidia CUVID VP9 decoder (codec vp9)
`)

	cases := []struct {
		name    string
		codec   string
		encoder ID
		want    bool
	}{
		{"h264 has NVDEC decoder", "h264", H264NVENC, true},
		{"hevc has NVDEC decoder", "hevc", H264NVENC, true},
		{"rv40 has no NVDEC decoder", "rv40", H264NVENC, false},
		{"unknown codec has no NVDEC decoder", "wmv3", H264NVENC, false},
		{"rv40 not downgraded for AMF", "rv40", H264AMF, true},
		{"rv40 not downgraded for VAAPI", "rv40", H264VAAPI, true},
		{"rv40 not downgraded for software", "rv40", Libx264, true},
		{"empty codec is unsupported", "", H264NVENC, false},
		{"qsv checks qsv decoder", "h264", H264QSV, false},
		{"hevc qsv checks qsv decoder", "hevc", HEVCQSV, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hardwareDecoderAvailableInList(decoders, tc.codec, tc.encoder); got != tc.want {
				t.Fatalf("hardwareDecoderAvailableInList(%q, %q) = %v, want %v", tc.codec, tc.encoder, got, tc.want)
			}
		})
	}
}

func TestHardwareDecoderAvailableRealFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not available on PATH")
	}
	// 常见的 NVDEC 支持格式应当存在对应 *_cuvid 解码器。
	if !HardwareDecoderAvailable(ffmpeg, "h264", H264NVENC) {
		t.Fatal("expected h264 to have a NVDEC decoder")
	}
	// RealVideo 没有 NVDEC 解码器，必须识别为不支持，否则全硬件管线会失败。
	if HardwareDecoderAvailable(ffmpeg, "rv40", H264NVENC) {
		t.Fatal("expected rv40 to have no NVDEC decoder")
	}
}
