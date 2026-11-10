package geo

import (
	"encoding/base64"
	"math"
	"sync"
)

// The land bitmap in land_data.go is a 360×180 grid of 1°×1° cells, one
// bit each, most significant bit first. Row 0 is the cell centred on
// 89.5°N (latitude 89..90), column 0 is centred on -179.5° (longitude
// -180..-179). A set bit means the cell centre lies on land according to
// Natural Earth 1:110m land polygons.
const (
	landCols = 360
	landRows = 180
)

var (
	landOnce sync.Once
	landBits []byte
)

func loadLand() {
	b, err := base64.StdEncoding.DecodeString(landGrid)
	if err != nil || len(b) != landCols*landRows/8 {
		landBits = nil // generated data is corrupt: treat everything as water
		return
	}
	landBits = b
}

func onLand(lat, lon float64) bool {
	landOnce.Do(loadLand)
	if landBits == nil || math.IsNaN(lat) || math.IsNaN(lon) {
		return false
	}
	if lat > 90 || lat < -90 {
		return false
	}
	row := int(math.Floor(90 - lat))
	if row >= landRows {
		row = landRows - 1 // lat == -90
	}
	// Wrap longitude into [-180, 180).
	lon = math.Mod(lon+180, 360)
	if lon < 0 {
		lon += 360
	}
	col := int(math.Floor(lon))
	if col >= landCols {
		col = landCols - 1
	}
	i := row*landCols + col
	return landBits[i/8]&(0x80>>(i%8)) != 0
}
