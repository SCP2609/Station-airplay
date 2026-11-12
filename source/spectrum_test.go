package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"testing"
	"time"
)

func parseTestHex(t *testing.T, value string) (float64, float64, float64) {
	t.Helper()
	if len(value) != 7 || value[0] != '#' {
		t.Fatalf("bad colour %q", value)
	}
	var rgb [3]int
	for i := 0; i < 3; i++ {
		n := 0
		for _, c := range value[1+i*2 : 3+i*2] {
			n *= 16
			switch {
			case c >= '0' && c <= '9':
				n += int(c - '0')
			case c >= 'a' && c <= 'f':
				n += int(c-'a') + 10
			case c >= 'A' && c <= 'F':
				n += int(c-'A') + 10
			default:
				t.Fatalf("bad colour %q", value)
			}
		}
		rgb[i] = n
	}
	return float64(rgb[0]), float64(rgb[1]), float64(rgb[2])
}

func TestSpectrumFrameFollowsBandsAndCoverHue(t *testing.T) {
	tap := newSpectrumTap(t.TempDir())
	tap.mu.Lock()
	tap.state.status = "playing"
	tap.state.lastAudio = time.Now()
	tap.state.history = [4]float64{1, 0, 0, 0}
	tap.hue, tap.haveHue = 120, true
	tap.mu.Unlock()

	frame := tap.frame(9, 200)
	if len(frame) != 9 {
		t.Fatalf("frame length %d", len(frame))
	}
	r, g, b := parseTestHex(t, frame[0])
	if g <= r || g <= b || g < 100 {
		t.Fatalf("quiet green band rendered %s", frame[0])
	}
	// Band 0 is the only loud band: the last group must be nearly dark.
	r2, g2, b2 := parseTestHex(t, frame[8])
	if r2+g2+b2 >= r+g+b {
		t.Fatalf("silent band not darker: %s vs %s", frame[8], frame[0])
	}
	for _, value := range frame {
		if value == "#000000" {
			t.Fatalf("frame contains black with a non-zero floor: %v", frame)
		}
	}
}

func TestAppliedSpectrumTuning(t *testing.T) {
	// These are the owner's 2026-11-11 panel values; pin them so a later
	// refactor cannot silently move the deployed ring.
	if coverSaturationBoost != 0.22 {
		t.Fatalf("saturation boost %.2f, want 0.22", coverSaturationBoost)
	}
	if spectrumFloor != 0.065 {
		t.Fatalf("bar floor %.3f, want 0.065", spectrumFloor)
	}
}

func TestSpectrumFrameWithoutArtworkPulsesNeutral(t *testing.T) {
	tap := newSpectrumTap(t.TempDir())
	tap.mu.Lock()
	tap.state.status = "playing"
	tap.state.lastAudio = time.Now()
	tap.state.history = [4]float64{1, .6, .3, .1}
	tap.mu.Unlock()
	frame := tap.frame(6, 200)
	seen := map[string]bool{}
	for _, value := range frame {
		r, g, b := parseTestHex(t, value)
		if r != g || g != b {
			t.Fatalf("fallback %q is not neutral", value)
		}
		seen[value] = true
	}
	if len(seen) < 3 {
		t.Fatalf("neutral fallback is flat: %v", frame)
	}
	r, g, b := parseTestHex(t, frame[0])
	if r+g+b == 0 {
		t.Fatalf("loud band stayed black: %v", frame)
	}
}

func TestPaletteHueReadsRedArtwork(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: 220, G: 20, B: 30, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	hue, ok := paletteHue(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if !ok {
		t.Fatal("red artwork produced no hue")
	}
	if math.Min(hue, 360-hue) > 12 {
		t.Fatalf("red hue = %.1f", hue)
	}
}

func TestPaletteHueRejectsUnusableArtwork(t *testing.T) {
	if _, ok := paletteHue(bytes.NewReader([]byte("not an image")), 12); ok {
		t.Fatal("garbage accepted")
	}
	if _, ok := paletteHue(bytes.NewReader(make([]byte, 16)), coverArtMaxBytes+1); ok {
		t.Fatal("oversize accepted")
	}
}
