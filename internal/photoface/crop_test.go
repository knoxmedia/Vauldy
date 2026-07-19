package photoface

import (
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

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

func TestWriteFaceThumbnailCreatesWorkerOwnedCache(t *testing.T) {
	src := filepath.Join(t.TempDir(), "source.jpg")
	file, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 50, 255})
		}
	}
	if err := jpeg.Encode(file, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	file.Close()
	preview := t.TempDir()
	if err := WriteFaceThumbnail(src, preview, 7, .2, .2, .4, .4); err != nil {
		t.Fatal(err)
	}
	path := ExpectedFaceThumbnailPath(preview, 7)
	if st, err := os.Stat(path); err != nil || st.Size() == 0 {
		t.Fatalf("face thumb missing path=%s err=%v", path, err)
	}
}
