package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeControlsFile(t *testing.T, body string) (string, *controlsConfig) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "airplay-controls.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadControls(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, cfg
}

func TestMigrateControlsStation2TurnsOnRingFeatures(t *testing.T) {
	path, cfg := writeControlsFile(t, `{
  "voice": true,
  "native_volume": true,
  "ducking": true,
  "leds": false,
  "led_level": 200,
  "native_player": true,
  "alice_button": true,
  "spectrum": false
}`)
	out, err := migrateControls(path, "yandexstation_2", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !out.LEDs {
		t.Fatal("leds stayed off on Station 2")
	}
	if out.Spectrum == nil || !*out.Spectrum {
		t.Fatal("spectrum stayed off on Station 2")
	}
	if out.DefaultsVersion != controlsDefaultsVersion {
		t.Fatalf("defaults version = %d", out.DefaultsVersion)
	}
	if !out.Voice || !out.NativeVolume || !out.Ducking || !out.NativePlayer || !out.AliceButton {
		t.Fatalf("unrelated flags changed: %+v", out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"defaults_version": 2`) {
		t.Fatalf("file was not stamped: %s", data)
	}
}

func TestMigrateControlsKeepsChangedValue(t *testing.T) {
	path, cfg := writeControlsFile(t, `{"spectrum": true, "leds": false}`)
	out, err := migrateControls(path, "yandexstation_2", cfg)
	if err != nil {
		t.Fatal(err)
	}
	// spectrum already differs from the old default and must stay as it is;
	// leds still equals the old default, so the new feature is enabled.
	if out.Spectrum == nil || !*out.Spectrum {
		t.Fatal("spectrum was rewritten")
	}
	if !out.LEDs {
		t.Fatal("leds default was not upgraded")
	}
}

func TestMigrateControlsOtherModelsKeepDefaults(t *testing.T) {
	path, cfg := writeControlsFile(t, `{"leds": false, "spectrum": false}`)
	out, err := migrateControls(path, "yandexmini_2", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if out.LEDs {
		t.Fatal("Mini 2 LEDs were enabled by a Station 2 rule")
	}
	if out.Spectrum == nil || *out.Spectrum {
		t.Fatal("Mini 2 spectrum was changed")
	}
	if out.DefaultsVersion != controlsDefaultsVersion {
		t.Fatalf("defaults version = %d", out.DefaultsVersion)
	}
}

func TestMigrateControlsIsIdempotent(t *testing.T) {
	path, cfg := writeControlsFile(t, `{"leds": false, "spectrum": false}`)
	if _, err := migrateControls(path, "yandexstation_2", cfg); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := loadControls(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrateControls(path, "yandexstation_2", again); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("second migration rewrote the file:\n%s\n%s", first, second)
	}
}

func TestMigrateControlsWithoutFileIsNoop(t *testing.T) {
	if out, err := migrateControls("", "yandexstation_2", &controlsConfig{}); err != nil || out == nil {
		t.Fatalf("empty path: %v %v", out, err)
	}
}
