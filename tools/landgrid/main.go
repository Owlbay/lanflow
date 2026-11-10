// Command landgrid generates internal/geo/land_data.go: a 1°×1° land
// bitmap rasterised from Natural Earth 1:110m land polygons (public
// domain). Run from the repository root:
//
//	go run ./tools/landgrid
//
// It downloads the GeoJSON into a fresh temporary directory (or reads the
// file given with -in), tests each cell centre against every polygon ring
// with the even-odd rule (so MultiPolygons and holes are handled), and
// writes the bits base64-encoded with a "DO NOT EDIT" header.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	defaultURL = "https://raw.githubusercontent.com/nvkelso/natural-earth-vector/master/geojson/ne_110m_land.geojson"
	cols       = 360
	rows       = 180
)

type featureCollection struct {
	Features []struct {
		Geometry struct {
			Type        string          `json:"type"`
			Coordinates json.RawMessage `json:"coordinates"`
		} `json:"geometry"`
	} `json:"features"`
}

// ring is a closed sequence of [lon, lat] points with its bounding box.
type ring struct {
	pts                    [][2]float64
	minX, minY, maxX, maxY float64
}

func main() {
	in := flag.String("in", "", "local GeoJSON file (default: download Natural Earth 1:110m land)")
	src := flag.String("url", defaultURL, "download URL when -in is not given")
	out := flag.String("out", filepath.Join("internal", "geo", "land_data.go"), "output Go file")
	flag.Parse()

	data, err := load(*in, *src)
	if err != nil {
		log.Fatal(err)
	}
	rings, err := parseRings(data)
	if err != nil {
		log.Fatal(err)
	}
	bits := rasterise(rings)

	land := 0
	for _, b := range bits {
		for ; b != 0; b &= b - 1 {
			land++
		}
	}
	fmt.Fprintf(os.Stderr, "landgrid: %d rings, %d/%d land cells\n", len(rings), land, cols*rows)

	code, err := render(bits, *src, *in != "")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, code, 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stderr, "landgrid: wrote %s (%d bytes)\n", *out, len(code))
}

func load(in, src string) ([]byte, error) {
	if in != "" {
		return os.ReadFile(in)
	}
	dir, err := os.MkdirTemp("", "landgrid-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(src)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", src, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %s", src, resp.Status)
	}
	path := filepath.Join(dir, "land.geojson")
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(f, io.LimitReader(resp.Body, 64<<20)); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func parseRings(data []byte) ([]ring, error) {
	var fc featureCollection
	if err := json.Unmarshal(data, &fc); err != nil {
		return nil, fmt.Errorf("parse GeoJSON: %w", err)
	}
	var rings []ring
	addPolygon := func(poly [][][2]float64) {
		for _, r := range poly {
			if len(r) < 3 {
				continue
			}
			rg := ring{pts: r, minX: r[0][0], maxX: r[0][0], minY: r[0][1], maxY: r[0][1]}
			for _, p := range r {
				rg.minX = min(rg.minX, p[0])
				rg.maxX = max(rg.maxX, p[0])
				rg.minY = min(rg.minY, p[1])
				rg.maxY = max(rg.maxY, p[1])
			}
			rings = append(rings, rg)
		}
	}
	for _, f := range fc.Features {
		switch f.Geometry.Type {
		case "Polygon":
			var poly [][][2]float64
			if err := json.Unmarshal(f.Geometry.Coordinates, &poly); err != nil {
				return nil, fmt.Errorf("parse Polygon: %w", err)
			}
			addPolygon(poly)
		case "MultiPolygon":
			var multi [][][][2]float64
			if err := json.Unmarshal(f.Geometry.Coordinates, &multi); err != nil {
				return nil, fmt.Errorf("parse MultiPolygon: %w", err)
			}
			for _, poly := range multi {
				addPolygon(poly)
			}
		}
	}
	if len(rings) == 0 {
		return nil, fmt.Errorf("no polygons found in GeoJSON")
	}
	return rings, nil
}

// rasterise sets a bit for every cell whose centre is inside an odd number
// of rings (even-odd rule: outer rings add, holes remove).
func rasterise(rings []ring) []byte {
	bits := make([]byte, cols*rows/8)
	for row := 0; row < rows; row++ {
		lat := 89.5 - float64(row)
		for col := 0; col < cols; col++ {
			lon := -179.5 + float64(col)
			inside := false
			for i := range rings {
				r := &rings[i]
				if lon < r.minX || lon > r.maxX || lat < r.minY || lat > r.maxY {
					continue
				}
				if pointInRing(lon, lat, r.pts) {
					inside = !inside
				}
			}
			if inside {
				i := row*cols + col
				bits[i/8] |= 0x80 >> (i % 8)
			}
		}
	}
	return bits
}

// pointInRing is the classic ray-casting test (crossing number parity).
func pointInRing(x, y float64, pts [][2]float64) bool {
	in := false
	n := len(pts)
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		xi, yi := pts[i][0], pts[i][1]
		xj, yj := pts[j][0], pts[j][1]
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			in = !in
		}
	}
	return in
}

func render(bits []byte, src string, local bool) ([]byte, error) {
	enc := base64.StdEncoding.EncodeToString(bits)
	var buf bytes.Buffer
	buf.WriteString("// Code generated by tools/landgrid; DO NOT EDIT.\n")
	buf.WriteString("//\n")
	buf.WriteString("// Land bitmap derived from Natural Earth 1:110m land polygons.\n")
	buf.WriteString("// Made with Natural Earth. Free vector and raster map data @ naturalearthdata.com\n")
	buf.WriteString("// (public domain). Source: ")
	if local {
		buf.WriteString("local copy of ")
	}
	buf.WriteString(src + "\n")
	buf.WriteString("//\n")
	buf.WriteString("// 360×180 cells of 1°×1°, one bit each, MSB first; row 0 is centred on\n")
	buf.WriteString("// 89.5°N, column 0 on -179.5°E. Decoded by internal/geo/land.go.\n\n")
	buf.WriteString("package geo\n\n")
	buf.WriteString("const landGrid = \"\"")
	for i := 0; i < len(enc); i += 76 {
		end := min(i+76, len(enc))
		buf.WriteString(" +\n\t\"" + enc[i:end] + "\"")
	}
	buf.WriteString("\n")
	return format.Source(buf.Bytes())
}
