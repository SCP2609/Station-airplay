package main

import (
	"bytes"
	"image"
	"io"
	"math"
)

// coverRGB keeps saturation and brightness instead of expanding every tint
// (including cream paper and skin) into a fully saturated HSV ring.
func coverRGB(r io.Reader, size int64) ([3]float64, bool) {
	var zero [3]float64
	if size <= 0 || size > coverArtMaxBytes {
		return zero, false
	}
	data, err := readAllLimited(r, coverArtMaxBytes)
	if err != nil {
		return zero, false
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > coverArtMaxPixels/cfg.Height {
		return zero, false
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return zero, false
	}
	return dominantCoverRGB(img)
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
