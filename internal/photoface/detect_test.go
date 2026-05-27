package photoface

import "testing"

func TestParseDetectOutputWithInsightFaceLogs(t *testing.T) {
	out := []byte(`Applied providers: ['CPUExecutionProvider']
find model: det_500m.onnx
{"faces": [], "engine": "insightface"}
`)
	res, err := parseDetectOutput(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Engine != "insightface" {
		t.Fatalf("engine=%q", res.Engine)
	}
}

func TestParseDetectOutputPureJSON(t *testing.T) {
	out := []byte(`{"faces":[{"bbox":[0,0,0.1,0.1],"embedding":[1,2],"score":0.9}],"engine":"insightface"}`)
	res, err := parseDetectOutput(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Faces) != 1 {
		t.Fatalf("faces=%d", len(res.Faces))
	}
}

func TestParseDetectOutputErrorPayload(t *testing.T) {
	out := []byte(`{"faces":[],"error":"missing dependency"}`)
	res, err := parseDetectOutput(out)
	if err != nil || res == nil || res.Error == "" {
		t.Fatalf("res=%v err=%v", res, err)
	}
}
