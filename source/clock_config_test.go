package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClockConfigDefaultsAndModes(t *testing.T) {
	for _, tc := range []struct {
		json string
		want clockMusicPolicy
	}{
		{`{"voice":true}`, clockMusicPolicy{"alternating", 15, 5}},
		{`{"clock_mode":"wave","clock_wave_seconds":2,"clock_time_seconds":3600}`, clockMusicPolicy{"wave", 2, 3600}},
		{`{"clock_mode":"clock","clock_wave_seconds":0,"clock_time_seconds":1}`, clockMusicPolicy{"clock", 15, 1}},
		{`{"clock_mode":"alternating","clock_wave_seconds":3600,"clock_time_seconds":20}`, clockMusicPolicy{"alternating", 3600, 20}},
	} {
		t.Run(tc.json, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "controls.json")
			if err := os.WriteFile(path, []byte(tc.json), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadControls(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.clockPolicy(); got != tc.want {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
}

func TestClockWaveStyleLoads(t *testing.T) {
	for _, tc := range []struct {
		name string
		json string
		want string
	}{
		{"absent", `{}`, ""},
		{"empty", `{"clock_wave_style":""}`, ""},
		{"center", `{"clock_wave_style":"center"}`, "center"},
		{"bottom_up", `{"clock_wave_style":"bottom_up"}`, "bottom_up"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "controls.json")
			if err := os.WriteFile(path, []byte(tc.json), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadControls(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.ClockWaveStyle != tc.want {
				t.Fatalf("ClockWaveStyle = %q, want %q", cfg.ClockWaveStyle, tc.want)
			}
		})
	}
}

func TestClockConfigRejectsInvalidValues(t *testing.T) {
	for _, tc := range []struct{ json, field string }{
		{`{"clock_mode":"unknown"}`, "clock_mode"},
		{`{"clock_wave_seconds":-1}`, "clock_wave_seconds"},
		{`{"clock_time_seconds":-1}`, "clock_time_seconds"},
		{`{"clock_wave_seconds":3601}`, "clock_wave_seconds"},
		{`{"clock_time_seconds":3601}`, "clock_time_seconds"},
		{`{"clock_wave_style":"diagonal"}`, "clock_wave_style"},
		{`{"clock_wave_style":15}`, "clock_wave_style"},
		{`{"clock_mode":"clock","not_a_setting":true}`, "unknown field"},
	} {
		t.Run(tc.json, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "controls.json")
			if err := os.WriteFile(path, []byte(tc.json), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadControls(path); err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("expected %s error, got %v", tc.field, err)
			}
		})
	}
}
