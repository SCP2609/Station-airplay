package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func writeCover(t *testing.T, dir, name string, c color.RGBA) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.Set(x, y, c)
		}
	}
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	return path
}

func parseHex(t *testing.T, s string) (int, int, int) {
	t.Helper()
	if len(s) != 7 || s[0] != '#' {
		t.Fatalf("colour %q is not #RRGGBB", s)
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		t.Fatalf("colour %q: %v", s, err)
	}
	return int(v>>16) & 0xff, int(v>>8) & 0xff, int(v) & 0xff
}

// Two colour clusters in the artwork must reach two groups of zones: the
// indication reflects the cover, not a single averaged tint.
func TestSolidFrameSpreadsCoverPalette(t *testing.T) {
	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			if x < 16 {
				img.Set(x, y, color.RGBA{200, 20, 20, 255})
			} else {
				img.Set(x, y, color.RGBA{20, 20, 200, 255})
			}
		}
	}
	path := filepath.Join(dir, "cover-palette.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()
	cover, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer cover.Close()
	info, err := cover.Stat()
	if err != nil {
		t.Fatal(err)
	}
	dominant, palette, ok := coverPaletteRGB(cover, info.Size(), 6)
	if !ok || len(palette) != 2 {
		t.Fatalf("palette = %v, ok = %t, want two clusters", palette, ok)
	}
	tap := newSpectrumTap(dir)
	tap.gain = [3]float64{1, 1, 1}
	tap.mu.Lock()
	tap.palette = palette
	tap.coverColor, tap.haveCoverColor = dominant, true
	tap.mu.Unlock()
	frame := tap.solidFrame(6, 200)
	if len(frame) != 6 {
		t.Fatalf("frame length %d", len(frame))
	}
	if frame[0] != frame[2] || frame[3] != frame[5] || frame[0] == frame[3] {
		t.Fatalf("palette not spread over the zones: %v", frame)
	}
	for _, s := range frame {
		r, g, b := parseHex(t, s)
		if r < 100 && g < 100 && b < 100 {
			t.Fatalf("palette colour %s is too dim to see", s)
		}
	}
}

func TestAdoptCoverClearsHueOnBlackArtwork(t *testing.T) {
	dir := t.TempDir()
	path := writeCover(t, dir, "cover-black.png", color.RGBA{0, 0, 0, 255})
	tap := newSpectrumTap(dir)
	tap.hue, tap.haveHue = 47, true
	tap.adoptCover(path, "cover-black.png", time.Now())
	if tap.haveHue {
		t.Fatalf("black artwork kept stale hue %.1f", tap.hue)
	}
	if !tap.mono {
		t.Fatal("black artwork was not marked monochrome")
	}
	if tap.luma > .05 {
		t.Fatalf("black artwork luma = %.3f, want near 0", tap.luma)
	}
	if tap.cover != path {
		t.Fatalf("cover = %q, want %q", tap.cover, path)
	}
}

func TestAdoptCoverSetsHueOnColourArtwork(t *testing.T) {
	dir := t.TempDir()
	path := writeCover(t, dir, "cover-blue.png", color.RGBA{0, 80, 200, 255})
	tap := newSpectrumTap(dir)
	tap.adoptCover(path, "cover-blue.png", time.Now())
	if !tap.haveHue || tap.mono {
		t.Fatal("coloured artwork was not adopted as chromatic")
	}
	if tap.hue < 190 || tap.hue > 250 {
		t.Fatalf("hue = %.1f, want blue range", tap.hue)
	}
}

func TestFrameUsesNeutralBarsForMonochromeArtwork(t *testing.T) {
	tap := newSpectrumTap(t.TempDir())
	tap.mono, tap.luma = true, 1
	for _, s := range tap.frame(4, 200) {
		r, g, b := parseHex(t, s)
		if r != g || g != b {
			t.Fatalf("monochrome frame %q is not neutral", s)
		}
	}
}

func TestFrameKeepsArtworkHue(t *testing.T) {
	tap := newSpectrumTap(t.TempDir())
	tap.hue, tap.haveHue = 215, true
	for _, s := range tap.frame(4, 200) {
		r, g, b := parseHex(t, s)
		if !(b > r && b > g) {
			t.Fatalf("frame %q is not blue for hue 215", s)
		}
	}
}

func TestAdoptCoverBoostsChromaticSaturation(t *testing.T) {
	dir := t.TempDir()
	path := writeCover(t, dir, "cover-blue-boost.png", color.RGBA{30, 80, 200, 255})
	tap := newSpectrumTap(dir)
	tap.adoptCover(path, "cover-blue-boost.png", time.Now())

	want := [3]float64{30.0 / 255, 80.0 / 255, 200.0 / 255}
	_, before, _ := rgbHSV(want[0], want[1], want[2])
	hue, after, value := rgbHSV(tap.coverColor[0], tap.coverColor[1], tap.coverColor[2])
	if after <= before {
		t.Fatalf("saturation %.3f was not boosted from %.3f", after, before)
	}
	if after > 1 {
		t.Fatalf("saturation %.3f exceeds 1", after)
	}
	if math.Abs(value-want[2]) > 1e-9 {
		t.Fatalf("value %.4f changed from %.4f", value, want[2])
	}
	if hue < 190 || hue > 250 {
		t.Fatalf("hue %.1f moved out of the blue range", hue)
	}
	if !tap.haveHue || tap.mono {
		t.Fatal("boosted artwork is no longer chromatic")
	}
}

func TestAdoptCoverKeepsNeutralArtworkUnboosted(t *testing.T) {
	dir := t.TempDir()
	path := writeCover(t, dir, "cover-grey.png", color.RGBA{128, 128, 128, 255})
	tap := newSpectrumTap(dir)
	tap.adoptCover(path, "cover-grey.png", time.Now())
	for _, c := range tap.coverColor {
		if math.Abs(c-128.0/255) > 1e-9 {
			t.Fatalf("grey artwork was tinted: %v", tap.coverColor)
		}
	}
	if tap.haveHue || !tap.mono {
		t.Fatal("grey artwork was classified as chromatic")
	}
}

func TestCoverAdoptionWindowIsShort(t *testing.T) {
	if coverAdoptDelay > 600*time.Millisecond {
		t.Fatalf("cover adoption delay %s is still slow", coverAdoptDelay)
	}
	if coverScanInterval >= coverAdoptDelay {
		t.Fatalf("scan interval %s cannot satisfy delay %s", coverScanInterval, coverAdoptDelay)
	}
}

func TestPaletteTintRejectsGreyButReportsLuma(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, color.RGBA{128, 128, 128, 255})
		}
	}
	hue, luma, chromatic, decoded := paletteTintImage(img)
	if !decoded {
		t.Fatal("grey image did not decode")
	}
	if chromatic {
		t.Fatalf("grey image reported hue %.1f", hue)
	}
	if luma < .4 || luma > .6 {
		t.Fatalf("grey luma = %.3f, want about .5", luma)
	}
}

func TestPaletteTintKeepsColourHue(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, color.RGBA{0, 80, 200, 255})
		}
	}
	hue, _, chromatic, decoded := paletteTintImage(img)
	if !decoded || !chromatic {
		t.Fatal("blue image was not chromatic")
	}
	if hue < 190 || hue > 250 {
		t.Fatalf("hue = %.1f, want blue range", hue)
	}
}

func TestCoverBackgroundWinsOverSmallLogo(t *testing.T) {
	for _, tc := range []struct {
		name      string
		bg        color.RGBA
		wantBlack bool
	}{
		{"white", color.RGBA{255, 255, 255, 255}, false},
		{"black", color.RGBA{0, 0, 0, 255}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img := image.NewRGBA(image.Rect(0, 0, 64, 64))
			for y := 0; y < 64; y++ {
				for x := 0; x < 64; x++ {
					c := tc.bg
					if x < 8 {
						c = color.RGBA{220, 15, 15, 255}
					}
					img.Set(x, y, c)
				}
			}
			_, luma, chromatic, decoded := paletteTintImage(img)
			if !decoded || chromatic {
				t.Fatal("small red logo replaced neutral background")
			}
			tap := newSpectrumTap(t.TempDir())
			tap.mono = true
			tap.luma = luma
			tap.state.history = [4]float64{1, .7, .4, .2}
			for _, s := range tap.frame(6, 200) {
				r, g, b := parseHex(t, s)
				if r != g || g != b {
					t.Fatalf("not neutral: %s", s)
				}
				if tc.wantBlack && r != 0 {
					t.Fatalf("black background still emits light: %s", s)
				}
				if !tc.wantBlack && r == 0 {
					t.Fatal("white background is off")
				}
			}
		})
	}
}

func TestBlackCoverHasNoBrightnessFloor(t *testing.T) {
	tap := newSpectrumTap(t.TempDir())
	tap.mono = true
	tap.luma = .09
	tap.state.history = [4]float64{1, 1, 1, 1}
	for _, s := range tap.frame(6, 200) {
		if s != "#000000" {
			t.Fatalf("black emits %s", s)
		}
	}
}
