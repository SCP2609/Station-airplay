package main

import (
	"bytes"
	"image"
	"io"
	"math"
	"sort"
)

// coverRGB keeps saturation and brightness instead of expanding every tint
// (including cream paper and skin) into a fully saturated HSV ring.
func coverRGB(r io.Reader, size int64) ([3]float64, bool) {
	rgb, _, ok := coverPaletteRGB(r, size, 0)
	return rgb, ok
}

// coverPaletteRGB decodes the artwork once and returns both the dominant
// colour and up to want distinct colours, so a multi-zone indication can follow
// the cover's palette instead of one flat tint.
func coverPaletteRGB(r io.Reader, size int64, want int) ([3]float64, [][3]float64, bool) {
	var zero [3]float64
	if size <= 0 || size > coverArtMaxBytes {
		return zero, nil, false
	}
	data, err := readAllLimited(r, coverArtMaxBytes)
	if err != nil {
		return zero, nil, false
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > coverArtMaxPixels/cfg.Height {
		return zero, nil, false
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return zero, nil, false
	}
	dominant, ok := dominantCoverRGB(img)
	if !ok {
		return zero, nil, false
	}
	return dominant, paletteCoverRGB(img, want), true
}

// paletteCoverRGB keeps the most populated colour clusters of the artwork,
// dropping near-duplicates and near-black ones. The dominant cluster stays
// first, so a single-zone caller still gets the average colour.
func paletteCoverRGB(img image.Image, want int) [][3]float64 {
	if want <= 0 {
		return nil
	}
	type cell struct {
		count int
		sum   [3]float64
	}
	var bins [512]cell
	b := img.Bounds()
	sx, sy := b.Dx()/64, b.Dy()/64
	if sx < 1 {
		sx = 1
	}
	if sy < 1 {
		sy = 1
	}
	for y := b.Min.Y; y < b.Max.Y; y += sy {
		for x := b.Min.X; x < b.Max.X; x += sx {
			r, g, blue, a := img.At(x, y).RGBA()
			if a == 0 {
				continue
			}
			p := [3]float64{float64(r) / float64(a), float64(g) / float64(a), float64(blue) / float64(a)}
			var q [3]int
			for k := range p {
				q[k] = int(p[k] * 8)
				if q[k] > 7 {
					q[k] = 7
				}
			}
			index := q[0]*64 + q[1]*8 + q[2]
			bins[index].count++
			for k := range p {
				bins[index].sum[k] += p[k]
			}
		}
	}
	type entry struct {
		count int
		rgb   [3]float64
	}
	entries := make([]entry, 0, len(bins))
	for i := range bins {
		if bins[i].count == 0 {
			continue
		}
		var rgb [3]float64
		for k := range rgb {
			rgb[k] = bins[i].sum[k] / float64(bins[i].count)
		}
		entries = append(entries, entry{count: bins[i].count, rgb: rgb})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].count > entries[j].count })
	out := make([][3]float64, 0, want)
	for _, e := range entries {
		// Black is absence of light: the indication turns off instead of
		// inventing a dim tint for a dark cover.
		if math.Max(e.rgb[0], math.Max(e.rgb[1], e.rgb[2])) < .12 {
			continue
		}
		duplicate := false
		for _, c := range out {
			d := 0.0
			for k := range c {
				d += (c[k] - e.rgb[k]) * (c[k] - e.rgb[k])
			}
			if d < .20*.20 {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		rgb := e.rgb
		if _, sat, _ := rgbHSV(rgb[0], rgb[1], rgb[2]); sat >= coverNeutralCutoff {
			rgb = boostCoverSaturation(rgb)
		}
		out = append(out, rgb)
		if len(out) == want {
			break
		}
	}
	return out
}

// Select the most populated RGB cell, then average nearby pixels. All pixels
// vote equally, including white and black. Sparse bright logos cannot outweigh
// a neutral background just because the background has no hue.
func dominantCoverRGB(img image.Image) ([3]float64, bool) {
	type cell struct {
		count int
		sum   [3]float64
	}
	var bins [512]cell
	var points [][3]float64
	b := img.Bounds()
	sx, sy := b.Dx()/64, b.Dy()/64
	if sx < 1 {
		sx = 1
	}
	if sy < 1 {
		sy = 1
	}
	for y := b.Min.Y; y < b.Max.Y; y += sy {
		for x := b.Min.X; x < b.Max.X; x += sx {
			r, g, blue, a := img.At(x, y).RGBA()
			if a == 0 {
				continue
			}
			// RGBA is premultiplied. Unpremultiply before quantising.
			p := [3]float64{float64(r) / float64(a), float64(g) / float64(a), float64(blue) / float64(a)}
			var q [3]int
			for k := range p {
				q[k] = int(p[k] * 8)
				if q[k] > 7 {
					q[k] = 7
				}
			}
			index := q[0]*64 + q[1]*8 + q[2]
			bins[index].count++
			for k := range p {
				bins[index].sum[k] += p[k]
			}
			points = append(points, p)
		}
	}
	winner := 0
	for i := range bins {
		if bins[i].count > bins[winner].count {
			winner = i
		}
	}
	if bins[winner].count == 0 {
		return [3]float64{}, false
	}
	var center, result [3]float64
	for k := range center {
		center[k] = bins[winner].sum[k] / float64(bins[winner].count)
	}
	n := 0
	for _, p := range points {
		d := 0.0
		for k := range p {
			d += (p[k] - center[k]) * (p[k] - center[k])
		}
		if d > .16*.16 {
			continue
		}
		n++
		for k := range p {
			result[k] += p[k]
		}
	}
	for k := range result {
		result[k] /= float64(n)
	}
	if math.Max(result[0], math.Max(result[1], result[2])) < .12 {
		return [3]float64{}, true // black is off; no invented brightness floor
	}
	return result, true
}
