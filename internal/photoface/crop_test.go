package photoface

import "testing"

func TestExpandFaceBBoxForAvatar_expandsAndBiasesUp(t *testing.T) {
	x, y, w, h := 0.4, 0.35, 0.2, 0.25
	_, ey, ew, eh := expandFaceBBoxForAvatar(x, y, w, h)
	if ew <= w || eh <= h {
		t.Fatalf("expected expanded crop, got w=%v h=%v from w=%v h=%v", ew, eh, w, h)
	}
	faceCY := y + h*0.5
	cropCY := ey + eh*0.5
	if cropCY >= faceCY {
		t.Fatalf("expected crop center above face center, faceCY=%v cropCY=%v", faceCY, cropCY)
	}
}

func TestExpandFaceBBoxForAvatar_clampsToImage(t *testing.T) {
	ex, ey, ew, eh := expandFaceBBoxForAvatar(0.02, 0.02, 0.15, 0.18)
	if ex < 0 || ey < 0 || ex+ew > 1.001 || ey+eh > 1.001 {
		t.Fatalf("crop out of bounds: x=%v y=%v w=%v h=%v", ex, ey, ew, eh)
	}
}
