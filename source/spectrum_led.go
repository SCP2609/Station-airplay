package main

// runSpectrumLEDs paints the real PCM bands at 10 Hz. The control tick still
// owns the gates (Alice idle, speech, volume overlay, pause); it publishes a
// single atomic flag and never blocks on the LED request itself.

import (
	"context"
	"fmt"
	"os"
	"time"
)

func spectrumLEDLevel(cfg controlsConfig) uint8 {
	level := cfg.LEDLevel
	if level == 0 {
		level = 200
	}
	return level
}

func runSpectrumLEDs(ctx context.Context, c *sharedController, tap *spectrumTap, level uint8) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastErr time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !c.ledPlay.Load() {
				continue
			}
			colors := tap.frame(c.ledCount, level)
			if len(colors) == 0 {
				continue
			}
			c.ledMu.Lock()
			if !c.ledPlay.Load() {
				c.ledMu.Unlock()
				continue
			}
			err := c.station.Colors(ctx, colors)
			c.ledMu.Unlock()
			if err != nil {
				c.ledFailed.Store(true)
				if time.Since(lastErr) > 10*time.Second {
					fmt.Fprintf(os.Stderr, "airplay spectrum: LED frame: %v\n", err)
					lastErr = time.Now()
				}
				return
			}
		}
	}
}
