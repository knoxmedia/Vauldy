package keyframes

import "testing"

func TestParseKeyframePacketEntriesWithPos(t *testing.T) {
	csv := "0.000000,0,K_\n0.040000,4096,__\n2.000000,65536,K_\n"
	got := parseKeyframePacketEntries(csv)
	if len(got) != 2 {
		t.Fatalf("len=%d want 2 (%v)", len(got), got)
	}
	if got[0].PTS != 0 || got[0].Pos != 0 {
		t.Fatalf("first=%+v", got[0])
	}
	if got[1].PTS != 2 || got[1].Pos != 65536 {
		t.Fatalf("second=%+v", got[1])
	}
}

func TestPlanEncryptedSeekUsesNearestKeyframe(t *testing.T) {
	m := &Meta{
		PTS: []float64{0, 2, 4, 6},
		Pos: []int64{0, 50000, 100000, 150000},
	}
	off, ss, ok := m.PlanEncryptedSeek(5.5)
	if !ok {
		t.Fatal("expected ok")
	}
	if off != 100000 {
		t.Fatalf("pipe offset=%d want 100000", off)
	}
	if ss < 1.49 || ss > 1.51 {
		t.Fatalf("ffmpeg -ss=%v want ~1.5", ss)
	}
}

func TestPlanEncryptedSeekFallsBackWithoutPosIndex(t *testing.T) {
	m := &Meta{PTS: []float64{0, 2, 4}}
	_, ss, ok := m.PlanEncryptedSeek(5)
	if ok {
		t.Fatal("expected fallback when pos missing")
	}
	if ss != 5 {
		t.Fatalf("ss=%v want 5", ss)
	}
}
