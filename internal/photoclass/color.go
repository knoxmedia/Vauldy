package photoclass

import (
	"image"
	_ "image/jpeg"
	"math"
	"os"
)

type colorStats struct {
	avgR, avgG, avgB float64
	avgSat           float64
	avgBright        float64
}

func analyzeColor(imagePath string) (colorStats, bool) {
	f, err := os.Open(imagePath)
	if err != nil {
		return colorStats{}, false
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return colorStats{}, false
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return colorStats{}, false
	}
	step := 4
	if w/step < 8 {
		step = 1
	}
	var n int
	var sumR, sumG, sumB, sumSat, sumBright float64
	for y := b.Min.Y; y < b.Max.Y; y += step {
		for x := b.Min.X; x < b.Max.X; x += step {
			r16, g16, b16, _ := img.At(x, y).RGBA()
			r := float64(r16) / 65535
			g := float64(g16) / 65535
			bl := float64(b16) / 65535
			maxC := math.Max(r, math.Max(g, bl))
			minC := math.Min(r, math.Min(g, bl))
			sat := 0.0
			if maxC > 0.001 {
				sat = (maxC - minC) / maxC
			}
			bright := (r + g + bl) / 3
			sumR += r
			sumG += g
			sumB += bl
			sumSat += sat
			sumBright += bright
			n++
		}
	}
	if n == 0 {
		return colorStats{}, false
	}
	fn := float64(n)
	return colorStats{
		avgR: sumR / fn, avgG: sumG / fn, avgB: sumB / fn,
		avgSat: sumSat / fn, avgBright: sumBright / fn,
	}, true
}

func colorTagsFromStats(s colorStats) []string {
	var tags []string
	if s.avgSat < 0.12 {
		tags = append(tags, "黑白")
	} else {
		if s.avgR > s.avgB+0.06 {
			tags = append(tags, "暖色系")
		}
		if s.avgB > s.avgR+0.06 {
			tags = append(tags, "冷色系")
		}
		if s.avgSat > 0.45 {
			tags = append(tags, "高饱和度")
		}
	}
	return tags
}
