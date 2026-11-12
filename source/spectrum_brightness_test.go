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
