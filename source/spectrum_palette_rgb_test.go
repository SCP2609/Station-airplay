package main

import (
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestDominantRGBPreservesBackground(t *testing.T) {
	for _, tc := range []struct {
		name string
		bg   color.RGBA
	}{
		{"cream", color.RGBA{248, 242, 222, 255}},
		{"white", color.RGBA{255, 255, 255, 255}},
		{"black", color.RGBA{0, 0, 0, 255}},
		{"blue", color.RGBA{30, 80, 200, 255}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img := image.NewRGBA(image.Rect(0, 0, 64, 64))
			for y := 0; y < 64; y++ {
				for x := 0; x < 64; x++ {
					c := tc.bg
					if x < 10 {
						c = color.RGBA{230, 15, 15, 255}
					}
					img.SetRGBA(x, y, c)
				}
			}
			rgb, ok := dominantCoverRGB(img)
			if !ok {
				t.Fatal("no palette")
			}
			want := [3]float64{float64(tc.bg.R) / 255, float64(tc.bg.G) / 255, float64(tc.bg.B) / 255}
			for k := range rgb {
				if math.Abs(rgb[k]-want[k]) > .02 {
					t.Fatalf("got %v want %v", rgb, want)
				}
			}
			tap := newSpectrumTap(t.TempDir())
			tap.coverColor = rgb
			tap.haveCoverColor = true
			tap.state.history = [4]float64{1, .8, .5, .2}
			for _, s := range tap.frame(6, 200) {
				r, g, b := parseHex(t, s)
				if tc.name == "black" && (b == 0 || b < r || b > 48 || r > 24 || g > 32) {
					t.Fatalf("black fallback not dark blue-grey: %s", s)
				}
				if tc.name == "cream" && (r-g > 10 || g-b > 25 || b < 1) {
					t.Fatalf("cream became saturated: %s", s)
				}
			}
		})
	}
}

func TestRealCoverRGBFixtures(t *testing.T) {
	dir := os.Getenv("AIRPLAY_COVER_FIXTURES")
	if dir == "" {
		t.Skip("private artwork fixtures not supplied")
	}
	for _, tc := range []struct {
		name, path string
		kind       string
	}{
		{"zemfira", "samples/cover-ce7c3fd0c187f6064b68225123dc086e.jpg", "cream"},
		{"black", "cover-black.jpg", "black"},
		{"blue", "cover.jpg", "blue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := os.Open(filepath.Join(dir, tc.path))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			info, err := f.Stat()
			if err != nil {
				t.Fatal(err)
			}
			rgb, ok := coverRGB(f, info.Size())
			if !ok {
				t.Fatal("decode failed")
			}
			t.Logf("dominant RGB %.3f %.3f %.3f", rgb[0], rgb[1], rgb[2])
			switch tc.kind {
			case "cream":
				if rgb[0] < .8 || rgb[1] < .8 || rgb[2] < .7 || rgb[0]-rgb[2] > .2 {
					t.Fatalf("cream became %v", rgb)
				}
			case "black":
				if rgb != [3]float64{} {
					t.Fatalf("black became %v", rgb)
				}
			case "blue":
				if rgb[2] <= rgb[0] || rgb[2] <= rgb[1] {
					t.Fatalf("blue became %v", rgb)
				}
			}
		})
	}
}
