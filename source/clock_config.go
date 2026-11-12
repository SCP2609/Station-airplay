package main

import "fmt"

type clockMusicPolicy struct {
	Mode        string `json:"mode"`
	WaveSeconds int    `json:"wave_seconds"`
	TimeSeconds int    `json:"time_seconds"`
}

func (c controlsConfig) clockPolicy() clockMusicPolicy {
	p := clockMusicPolicy{c.ClockMode, c.ClockWaveSeconds, c.ClockTimeSeconds}
	if p.Mode == "" {
		p.Mode = "alternating"
	}
	if p.WaveSeconds == 0 {
		p.WaveSeconds = 15
	}
	if p.TimeSeconds == 0 {
		p.TimeSeconds = 5
	}
	return p
}

func (c controlsConfig) validateClock() error {
	switch c.ClockWaveStyle {
	case "", "center", "bottom_up":
	default:
		return fmt.Errorf("clock_wave_style must be center or bottom_up")
	}
	switch c.ClockMode {
	case "", "alternating", "wave", "clock":
	default:
		return fmt.Errorf("clock_mode must be alternating, wave or clock")
	}
	for _, value := range []struct {
		name    string
		seconds int
	}{
		{"clock_wave_seconds", c.ClockWaveSeconds},
		{"clock_time_seconds", c.ClockTimeSeconds},
	} {
		if value.seconds < 0 || value.seconds > 3600 {
			return fmt.Errorf("%s must be 1..3600 seconds, or 0 for default", value.name)
		}
	}
	return nil
}
