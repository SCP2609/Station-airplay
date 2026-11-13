package main

import "testing"

func TestSpectrumBrightnessBoostAndDarkException(t *testing.T) {
	tap := newSpectrumTap(t.TempDir())
	tap.haveCoverColor = true
	tap.coverColor = [3]float64{.7, .8, 1}
	tap.state.history = [4]float64{.4, .3, .2, .1}
	before := tap.frame(4, 200)
	tap.brightness = 1.5
	after := tap.frame(4, 200)
	for i, s := range after {
		r, g, b := parseHex(t, s)
		r0, g0, b0 := parseHex(t, before[i])
		if r+g+b <= r0+g0+b0 {
			t.Fatalf("no boost %s -> %s", before[i], s)
		}
	}
	tap.coverColor = [3]float64{}
	tap.brightness = 0
	dark := tap.frame(4, 200)
	tap.brightness = 1.5
	for i, s := range tap.frame(4, 200) {
		if s != dark[i] {
			t.Fatal("dark fallback was boosted")
		}
	}
	tap.coverColor = [3]float64{1, 1, 1}
	tap.brightness = 2
	tap.state.history = [4]float64{1, 1, 1, 1}
	for _, s := range tap.frame(4, 200) {
		parseHex(t, s)
	}
}

func TestSpectrumWhiteBalanceAndNeutralBlack(t *testing.T) {
	tap := newSpectrumTap(t.TempDir())
	tap.haveCoverColor = true
	tap.coverColor = [3]float64{1, 1, 1}
	tap.gain = [3]float64{1.5, 0.5, 0.71}
	tap.state.history = [4]float64{1, 1, 1, 1}
	r, g, b := parseHex(t, tap.frame(1, 200)[0])
	if !(r > g && r > b) {
		t.Fatalf("white balance did not raise red: %d/%d/%d", r, g, b)
	}

	tap.coverColor = [3]float64{}
	tap.neutralBlack = true
	if got := tap.frame(3, 200)[0]; got != "#000000" {
		t.Fatalf("neutral black still emits %s", got)
	}
	tap.neutralBlack = false
	if got := tap.frame(3, 200)[0]; got == "#000000" {
		t.Fatal("Mini 2 black fallback was removed")
	}
}

func TestSpectrumComfortPerModel(t *testing.T) {
	floor, ceiling, saturation := spectrumComfort(yamDevice{Model: "yandexstation_2"})
	if floor != 0.07 || ceiling != 1 || saturation != 1.35 {
		t.Fatalf("Station 2 comfort: %v/%v/%v", floor, ceiling, saturation)
	}
	for _, model := range []string{"yandexplum", "plum"} {
		floor, ceiling, saturation := spectrumComfort(yamDevice{Model: model})
		if floor != 0.07 || ceiling != 1 || saturation != 1 {
			t.Fatalf("%s comfort: %v/%v/%v", model, floor, ceiling, saturation)
		}
	}
	for _, model := range []string{"yandexmini_2", "yandexmidi"} {
		floor, ceiling, saturation := spectrumComfort(yamDevice{Model: model})
		if floor != 0 || ceiling != 1 || saturation != 1 {
			t.Fatalf("%s comfort: %v/%v/%v", model, floor, ceiling, saturation)
		}
	}
}

func TestSpectrumLite2BlackGlow(t *testing.T) {
	tap := newSpectrumTap(t.TempDir())
	tap.haveCoverColor = true
	tap.coverColor = [3]float64{}
	tap.neutralBlack = true
	tap.blackFloor = 0.07
	got := tap.solidFrame(3, 200)[0]
	r, g, b := parseHex(t, got)
	if got == "#000000" || !(b > r && r > g) {
		t.Fatalf("Lite 2 solid black glow is not violet: %s", got)
	}
	tap.state.history = [4]float64{}
	got = tap.frame(1, 200)[0]
	r, g, b = parseHex(t, got)
	if got == "#000000" || !(b > r && r > g) {
		t.Fatalf("Lite 2 animated black glow is not violet: %s", got)
	}

	// Covers saved as #0A0A0A used to escape the exact-zero check and get the
	// 0.45 visibility lift, which read as washed white on the ring.
	for _, near := range [][3]float64{{0x0A / 255.0, 0x0A / 255.0, 0x0A / 255.0}, {0.02, 0.025, 0.03}} {
		tap.coverColor = near
		got = tap.solidFrame(3, 200)[0]
		r, g, b = parseHex(t, got)
		if got == "#000000" || !(b >= r && b > g) {
			t.Fatalf("near-black solid frame is not violet: %s", got)
		}
		if r+g+b > 90 {
			t.Fatalf("near-black solid frame is washed out: %s", got)
		}
		tap.state.history = [4]float64{.3, .2, .1, .1}
		got = tap.frame(1, 200)[0]
		r, g, b = parseHex(t, got)
		if !(b >= r && b > g) || r+g+b > 90 {
			t.Fatalf("near-black animated frame is not dim violet: %s", got)
		}
		tap.state.history = [4]float64{}
	}
}

func TestSpectrumStation2BlackGlowAndCeiling(t *testing.T) {
	tap := newSpectrumTap(t.TempDir())
	tap.haveCoverColor = true
	tap.coverColor = [3]float64{}
	tap.neutralBlack = true
	tap.blackFloor = 0.10
	tap.state.history = [4]float64{}
	got := tap.frame(1, 200)[0]
	r, g, b := parseHex(t, got)
	if got == "#000000" || r+g+b == 0 {
		t.Fatal("black cover stayed dark on Station 2")
	}
	if !(b > r) {
		t.Fatalf("black glow is not cool: %s", got)
	}

	tap.coverColor = [3]float64{1, 1, 1}
	tap.ceiling = 1
	tap.state.history = [4]float64{1, 1, 1, 1}
	r, g, b = parseHex(t, tap.frame(1, 200)[0])
	if r < 230 || g < 230 || b < 230 {
		t.Fatalf("white cover is too dim: %d/%d/%d", r, g, b)
	}
}

func TestSpectrumStation2SaturationKeepsNeutrals(t *testing.T) {
	tap := newSpectrumTap(t.TempDir())
	tap.haveCoverColor = true
	tap.state.history = [4]float64{1, 1, 1, 1}
	tap.coverColor = [3]float64{0x93 / 255.0, 0x79 / 255.0, 0x40 / 255.0}
	r0, g0, b0 := parseHex(t, tap.frame(1, 200)[0])
	tap.satBoost = 1.35
	r1, g1, b1 := parseHex(t, tap.frame(1, 200)[0])
	spread := func(a, b, c int) int {
		m, n := a, a
		for _, v := range []int{b, c} {
			if v > m {
				m = v
			}
			if v < n {
				n = v
			}
		}
		return m - n
	}
	if spread(r1, g1, b1) <= spread(r0, g0, b0) {
		t.Fatalf("saturation did not grow: %d/%d/%d -> %d/%d/%d", r0, g0, b0, r1, g1, b1)
	}
	tap.coverColor = [3]float64{1, 1, 1}
	r2, g2, b2 := parseHex(t, tap.frame(1, 200)[0])
	if r2 != g2 || g2 != b2 {
		t.Fatalf("white was tinted: %d/%d/%d", r2, g2, b2)
	}
}
