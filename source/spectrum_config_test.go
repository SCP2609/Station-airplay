package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpectrumConfigBounds(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) (*controlsConfig, error) {
		path := filepath.Join(dir, "controls.json")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		return loadControls(path)
	}
	if _, err := write(`{"voice":true,"spectrum_brightness":2.1}`); err == nil {
		t.Fatal("brightness above 2 accepted")
	}
	if _, err := write(`{"voice":true,"spectrum_gain_r":-0.1}`); err == nil {
		t.Fatal("negative gain accepted")
	}
	if _, err := write(`{"voice":true,"led_color_profile":"green"}`); err == nil {
		t.Fatal("unknown LED profile accepted")
	}
	c, err := write(`{"voice":true,"leds":true,"spectrum":false,"spectrum_brightness":1.25,"spectrum_gain_r":1.5}`)
	if err != nil {
		t.Fatal(err)
	}
	if c.useSpectrum() {
		t.Fatal("explicit spectrum:false still opened the tap")
	}
	c2, err := write(`{"voice":true,"leds":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if !c2.useSpectrum() {
		t.Fatal("omitted spectrum should default to on for LED rings")
	}
}

func TestSpectrumExcludesOnlyBigScreens(t *testing.T) {
	ring := []string{"led.ring.rgb", "led.ring.6"}
	for _, model := range []string{"yandexmini_2", "yandexplum", "plum", "yandexmidi", "bergamot", "mango", "yandexmicro"} {
		if !spectrumSupported(yamDevice{Model: model, Features: ring}) {
			t.Fatalf("%s should support the spectrum", model)
		}
	}
	for _, model := range []string{"yandexstation_2", "yandexstation_3", "orion"} {
		if spectrumSupported(yamDevice{Model: model, Features: ring}) {
			t.Fatalf("%s must stay on native indication", model)
		}
	}
	if spectrumSupported(yamDevice{Model: "yandexmini_2"}) {
		t.Fatal("a ring capability is required")
	}
}

func TestSpectrumDefaultsPerModel(t *testing.T) {
	oldLuna := []string{"led.ring.rgb", "led.ring.6"}
	gain, profile, neutralBlack, swap := spectrumDefaults(yamDevice{Model: "yandexmini_2", Features: oldLuna}, controlsConfig{})
	if profile != "red" || !swap || neutralBlack || gain != [3]float64{1, 1, 1} {
		t.Fatalf("Mini 2 defaults gain=%v profile=%s neutral=%t swap=%t", gain, profile, neutralBlack, swap)
	}
	newLuna := append(append([]string{}, oldLuna...), "led.calibration.v1", "led.color.logical-rgb.v1")
	_, profile, _, swap = spectrumDefaults(yamDevice{Model: "yandexmini_2", Features: newLuna}, controlsConfig{})
	if profile != "" || swap {
		t.Fatalf("Mini 2 still applies the plugin workaround on Luna 0.2.26+: profile=%q swap=%t", profile, swap)
	}
	gain, profile, neutralBlack, swap = spectrumDefaults(yamDevice{Model: "yandexplum"}, controlsConfig{})
	if gain != [3]float64{1.5, 0.5, 0.71} || profile != "" || !neutralBlack || swap {
		t.Fatalf("Lite 2 defaults gain=%v profile=%s neutral=%t swap=%t", gain, profile, neutralBlack, swap)
	}
	gain, _, _, _ = spectrumDefaults(yamDevice{Model: "yandexplum"}, controlsConfig{SpectrumGainR: 2, SpectrumGainG: 2, SpectrumGainB: 2})
	if gain != [3]float64{2, 2, 2} {
		t.Fatalf("explicit gain ignored: %v", gain)
	}
}

func TestSpectrumReceiverConfigRequestsArtworkAndTee(t *testing.T) {
	plain := receiverControlConfig(mini2Profile, &controlsConfig{LEDs: true})
	if !strings.Contains(plain, `output_device = "default"`) {
		t.Fatalf("plain config lost its mixer: %s", plain)
	}
	if !strings.Contains(plain, `include_cover_art = "no"`) {
		t.Fatalf("plain config requested artwork: %s", plain)
	}
	conf := receiverControlConfig(mini2Profile, &controlsConfig{LEDs: true}, true)
	for _, want := range []string{`output_device = "spk_tee"`, `include_cover_art = "yes"`, coverArtDir} {
		if !strings.Contains(conf, want) {
			t.Fatalf("spectrum config lacks %q:\n%s", want, conf)
		}
	}
	if strings.Count(conf, "cover_art_cache_directory") != 1 {
		t.Fatalf("spectrum config duplicated the cache key:\n%s", conf)
	}
	// Solid indication also follows the cover, but it must not reroute audio
	// through the analyser tee.
	off := false
	solid := receiverControlConfig(mini2Profile, &controlsConfig{LEDs: true, Spectrum: &off}, true)
	if !strings.Contains(solid, `include_cover_art = "yes"`) || !strings.Contains(solid, coverArtDir) {
		t.Fatalf("solid artwork config lacks cover art:\n%s", solid)
	}
	if !strings.Contains(solid, `output_device = "default"`) || strings.Contains(solid, "spk_tee") {
		t.Fatalf("solid artwork config rerouted the audio path:\n%s", solid)
	}
}

func TestSpectrumALSAChainKeepsClockAndAnalyserSeparate(t *testing.T) {
	asound := uiAsoundSpectrum("speakers", audioFIFO, spectrumFIFO)
	for _, want := range []string{
		`pcm.spk_file`, `file "` + audioFIFO + `"`,
		`pcm.spk_spectrum_file`, `file "` + spectrumFIFO + `"`,
		`slave.pcm "spk_file"`, `slave.pcm "spk_spectrum_file"`,
	} {
		if !strings.Contains(asound, want) {
			t.Fatalf("ALSA chain lacks %q:\n%s", want, asound)
		}
	}
	if strings.Count(asound, "type file") != 2 {
		t.Fatalf("expected two independent file taps:\n%s", asound)
	}
}
