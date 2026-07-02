package photoparse

import (
	"os"

	"github.com/rwcarlsen/goexif/exif"
)

func readGPS(filePath string) (lat, lon float64, ok bool) {
	f, err := os.Open(filePath)
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	x, err := exif.Decode(f)
	if err != nil {
		return 0, 0, false
	}
	lat, lon, err = x.LatLong()
	if err != nil {
		return 0, 0, false
	}
	return lat, lon, true
}
