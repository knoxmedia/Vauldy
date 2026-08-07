package hwenc

import (
	"strings"
	"testing"
)

func TestBuildInstantVideoArgsSoftware(t *testing.T) {
	args := BuildInstantVideoArgs(InstantVideoPlan{
		Encoder:    Libx264,
		Mode:       PipelineSoftware,
		Resolution: "1280x720",
		Bitrate:    "2000k",
		X264Preset: "veryfast",
		CRF:        23,
		SessionGOP: true,
	})
	all := strings.Join(args, " ")
	for _, want := range []string{"libx264", "scale=1280:720", "-g:v:0", "72"} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %q in %s", want, all)
		}
	}
}

func TestBuildInstantVideoArgsNVENCFull(t *testing.T) {
	args := BuildInstantVideoArgs(InstantVideoPlan{
		Encoder:    H264NVENC,
		Mode:       PipelineHWFull,
		Resolution: "1280x720",
		Bitrate:    "2000k",
		X264Preset: "veryfast",
		SessionGOP: false,
	})
	all := strings.Join(args, " ")
	for _, want := range []string{"h264_nvenc", "scale_cuda=1280:720", "-noautoscale", "-preset", "p2"} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %q in %s", want, all)
		}
	}
}

func TestBuildInstantVideoArgsNVENCDisableAutoScaleOnlyFullHW(t *testing.T) {
	encodeOnly := BuildInstantVideoArgs(InstantVideoPlan{
		Encoder:    H264NVENC,
		Mode:       PipelineHWEncodeOnly,
		Resolution: "1280x720",
		Bitrate:    "2000k",
	})
	software := BuildInstantVideoArgs(InstantVideoPlan{
		Encoder:    Libx264,
		Mode:       PipelineSoftware,
		Resolution: "1280x720",
		Bitrate:    "2000k",
	})
	for name, args := range map[string][]string{"encode_only": encodeOnly, "software": software} {
		if strings.Contains(strings.Join(args, " "), "-noautoscale") {
			t.Fatalf("expected no -noautoscale for %s, got %s", name, strings.Join(args, " "))
		}
	}
}

func TestBuildInstantVideoArgsQSVAutoScaleDisabled(t *testing.T) {
	args := BuildInstantVideoArgs(InstantVideoPlan{
		Encoder:    H264QSV,
		Mode:       PipelineHWFull,
		Resolution: "1280x720",
		Bitrate:    "2000k",
	})
	all := strings.Join(args, " ")
	for _, want := range []string{"h264_qsv", "scale_qsv=1280:720", "-noautoscale"} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %q in %s", want, all)
		}
	}
}

func TestInputAccelArgs(t *testing.T) {
	nv := strings.Join(InputAccelArgs(H264NVENC), " ")
	if !strings.Contains(nv, "cuda") {
		t.Fatalf("expected cuda hwaccel, got %q", nv)
	}
	va := strings.Join(InputAccelArgs(H264VAAPI), " ")
	if !strings.Contains(va, "vaapi_device") {
		t.Fatalf("expected vaapi device, got %q", va)
	}
}

func TestPipelineModeForInput(t *testing.T) {
	if PipelineModeForInput(true, true) != PipelineHWFull {
		t.Fatal("local file should use full HW pipeline")
	}
	if PipelineModeForInput(true, false) != PipelineHWEncodeOnly {
		t.Fatal("pipe input should use encode-only HW pipeline")
	}
	if PipelineModeForInput(false, true) != PipelineSoftware {
		t.Fatal("disabled HW should use software")
	}
}
