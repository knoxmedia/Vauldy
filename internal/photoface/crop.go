package photoface

import (
	"bytes"
	"image"
	_ "image/jpeg"
	"image/jpeg"
	"math"
	"os"
)

// Portrait avatar crop: wider than the detector box (hair/hat, shoulders), similar to cloud album UIs.
const (
	avatarSideScale   = 2.2  // crop side ≈ 2.2× max(face w, face h)
	avatarTopBias     = 0.18 // shift crop center up for forehead / hat
	avatarTopRatio    = 0.58 // more room above face center
	avatarBottomRatio = 0.42
)

// expandFaceBBoxForAvatar widens a normalized face bbox for profile-style thumbnails.
func expandFaceBBoxForAvatar(x, y, w, h float64) (float64, float64, float64, float64) {
	if w <= 0 || h <= 0 {
		return x, y, w, h
	}
	cx := x + w*0.5
	cy := y + h*0.5 - h*avatarTopBias
	side := math.Max(w, h) * avatarSideScale
	x0 := cx - side*0.5
	y0 := cy - side*avatarTopRatio
	x1 := cx + side*0.5
	y1 := cy + side*avatarBottomRatio

	if dx := 0.0 - x0; dx > 0 {
		x1 += dx
		x0 = 0
	}
	if dx := x1 - 1.0; dx > 0 {
		x0 -= dx
		x1 = 1
	}
	if dy := 0.0 - y0; dy > 0 {
		y1 += dy
		y0 = 0
	}
	if dy := y1 - 1.0; dy > 0 {
		y0 -= dy
		y1 = 1
	}
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 > 1 {
		x1 = 1
	}
	if y1 > 1 {
		y1 = 1
	}
	outW := x1 - x0
	outH := y1 - y0
	if outW <= 0 || outH <= 0 {
		return x, y, w, h
	}
	return x0, y0, outW, outH
}

// CropFaceJPEG extracts a face region from a JPEG file using normalized bbox (0..1).
func CropFaceJPEG(srcPath string, bboxX, bboxY, bboxW, bboxH float64, quality int) ([]byte, error) {
	bboxX, bboxY, bboxW, bboxH = expandFaceBBoxForAvatar(bboxX, bboxY, bboxW, bboxH)
	f, err := os.Open(srcPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	w := b.Dx()
	h := b.Dy()
	if w <= 0 || h <= 0 {
		return nil, os.ErrInvalid
	}
	x0 := int(math.Round(bboxX * float64(w)))
	y0 := int(math.Round(bboxY * float64(h)))
	x1 := int(math.Round((bboxX + bboxW) * float64(w)))
	y1 := int(math.Round((bboxY + bboxH) * float64(h)))
	if x0 < b.Min.X {
		x0 = b.Min.X
	}
	if y0 < b.Min.Y {
		y0 = b.Min.Y
	}
	if x1 > b.Max.X {
		x1 = b.Max.X
	}
	if y1 > b.Max.Y {
		y1 = b.Max.Y
	}
	if x1-x0 < 2 || y1-y0 < 2 {
		return nil, os.ErrInvalid
	}
	rect := image.Rect(x0, y0, x1, y1)
	sub, ok := img.(interface {
		SubImage(r image.Rectangle) image.Image
	})
	if !ok {
		return nil, os.ErrInvalid
	}
	cropped := sub.SubImage(rect)
	if quality <= 0 || quality > 100 {
		quality = 85
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, cropped, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
