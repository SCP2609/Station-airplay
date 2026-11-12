package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
)

type ledColorMatrix [3][3]float64

func loadLEDColorMatrix(dir, profile string) (*ledColorMatrix, error) {
	if profile != "red" && profile != "blue" {
		return nil, fmt.Errorf("unsupported Mini 2 LED colour profile %q", profile)
	}
	data, err := os.ReadFile(filepath.Join(dir, profile+".json"))
	if err != nil {
		return nil, err
	}
	var cfg struct {
		Type   string      `json:"type"`
		Matrix [][]float64 `json:"matrix"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.Type != "matrix" || len(cfg.Matrix) != 3 {
		return nil, fmt.Errorf("invalid LED colour matrix")
	}
	var m ledColorMatrix
	for i, row := range cfg.Matrix {
		if len(row) != 3 {
			return nil, fmt.Errorf("invalid LED colour matrix row")
		}
		for j, x := range row {
			if math.IsNaN(x) || math.IsInf(x, 0) || math.Abs(x) > 16 {
				return nil, fmt.Errorf("invalid LED colour coefficient")
			}
			m[i][j] = x
		}
	}
	return &m, nil
}

// Apply the device's stock colour calibration before Luna's BGR packing.
// Retain each pixel's original peak intensity, so the large green gain does
// not clip loud bands into a permanently flat frame. Pause is not passed here.
func (m *ledColorMatrix) colors(in []string) []string {
	if m == nil {
		return in
	}
	out := append([]string(nil), in...)
	for i, s := range in {
		if len(s) != 7 || s[0] != '#' {
			continue
		}
		v, err := strconv.ParseUint(s[1:], 16, 24)
		if err != nil {
			continue
		}
		rgb := [3]float64{float64(v >> 16), float64((v >> 8) & 255), float64(v & 255)}
		peak := math.Max(rgb[0], math.Max(rgb[1], rgb[2]))
		var c [3]float64
		for k, row := range m {
			for j, x := range row {
				c[k] += x * rgb[j]
			}
			c[k] = math.Max(0, c[k])
		}
		max := math.Max(c[0], math.Max(c[1], c[2]))
		if max > 0 {
			for k := range c {
				c[k] *= peak / max
			}
		}
		out[i] = fmt.Sprintf("#%02X%02X%02X", int(math.Round(c[0])), int(math.Round(c[1])), int(math.Round(c[2])))
	}
	return out
}
