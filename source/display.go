package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type displayInput struct {
	profile       stationProfile
	controls      *controlsConfig
	pcm           <-chan visualizerPCM
	pcmErrors     <-chan error
	updates       <-chan rendererControlState
	controlErrors <-chan error
	// tap renders artwork-based spectrum colours for the ring. It is nil when
	// the model has no spectrum or the controller already owns the ring.
	tap           *spectrumTap
	spectrumLevel uint8
}

const pauseDisplayDuration = 1800 * time.Millisecond

func (s *airplayState) observeNativeVolume(percent int, active bool, now time.Time) {
	if !s.nativeVolumeKnown || !active {
		s.nativeVolumeKnown = true
		s.volume = percent
		s.volumeUntil = time.Time{}
		return
	}
	s.showVolume(percent, now)
}

func (s *airplayState) displayMetadata(typ, code string, raw []byte, now time.Time, nativeVolume bool) {
	// The shared controller owns the station's percent scale and readback.
	// Legacy pvol uses a different dB-to-percent mapping and would alternate
	// with that value, retriggering the overlay on every feedback message.
	if nativeVolume && typ == "73736e63" && code == "70766f6c" {
		return
	}
	wasPaused := s.status == "paused"
	s.onMetadata(typ, code, raw, now)
	if s.status == "paused" && !wasPaused {
		s.pauseUntil = now.Add(pauseDisplayDuration)
	}
}

// The shared visualizer yields to Alice outside the AirPlay session and while
// the controller reports speech. Standalone Light 2 keeps its legacy mode.
func renderDisplay(ctx context.Context, api *yamAPI, metaFD int, exits <-chan exitInfo, input displayInput) error {
	p, controls := input.profile, input.controls
	defer func() {
		_ = api.ledOff()
		_ = api.clockOff()
	}()
	state := &airplayState{status: "stopped", sampleRate: profileOutputRate(p)}
	if p.sharedUI && controls != nil {
		state.clockWaveStyle = controls.ClockWaveStyle
	}
	state.nativeClockVolume = p.sharedUI && controls != nil && controls.NativeVolume
	var musicPolicy *clockMusicPolicy
	if state.nativeClockVolume {
		policy := controls.clockPolicy()
		musicPolicy = &policy
	} else if p.sharedUI && controls != nil && (controls.ClockMode != "" || controls.ClockWaveSeconds != 0 || controls.ClockTimeSeconds != 0) {
		fmt.Println("clock modes unavailable without native_volume; retaining previous display behavior")
	}
	var controlUI rendererControlState
	released := false
	tz := clockZone()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	lastUIError := time.Time{}
	lastClockError := time.Time{}
	oldRing := ""
	oldFrameKey := ""
	lastRingSent, lastClockSent := time.Time{}, time.Time{}
	for {
		select {
		case <-ctx.Done():
			return nil
		case ex := <-exits:
			return &childFailure{ex.name, ex.err}
		case err := <-input.controlErrors:
			return fmt.Errorf("display controls stopped: %w", err)
		case err := <-input.pcmErrors:
			return fmt.Errorf("audio FIFO: %w", err)
		case update := <-input.updates:
			if musicPolicy != nil && controlUI.Attached && !update.Attached && state.status == "paused" {
				state.status = "stopped"
			}
			controlUI = update
			if update.VolumeKnown {
				if p.sharedUI {
					state.observeNativeVolume(update.Volume, update.Session, time.Now())
				} else if update.Session {
					state.showVolume(update.Volume, time.Now())
				}
			}
		case block := <-input.pcm:
			state.audio(block.data, block.at)
			if input.tap != nil {
				input.tap.feedAudio(block.data, block.at)
			}
		case now := <-tick.C:
			if e := readFD(metaFD, func(buf []byte) {
				state.meta.feed(buf, func(t, c string, raw []byte) {
					state.displayMetadata(t, c, raw, now, controls != nil && controls.NativeVolume)
				})
			}); e != nil {
				return fmt.Errorf("metadata FIFO: %w", e)
			}
			state.rotate()
			paused := musicPolicy != nil && state.status == "paused"
			if isMidiProfile(p) || p.sharedUI {
				paint := state.pcmUIActive(now)
				if state.nativeClockVolume {
					paint = now.Sub(state.lastAudio) < 1800*time.Millisecond
				}
				if controls != nil {
					if paused {
						paint = now.Before(state.pauseUntil) && controlUI.allowsPause(now)
					} else {
						paint = paint && controlUI.allowsPaint(now)
					}
					paint = paint && controls.LEDs
				} else if p.sharedUI {
					paint = false // No controller means no safe lease over Alice.
				}
				if !paint {
					if !released {
						ringErr, clockErr := api.ledOff(), api.clockOff()
						logAPIError("ring", ringErr, &lastUIError)
						logAPIError("clock", clockErr, &lastClockError)
						released = ringErr == nil && clockErr == nil
						oldRing, oldFrameKey = "", ""
					}
					continue
				}
				released = false
			}
			colors := state.ringFrame(now)
			if input.tap != nil && input.spectrumLevel > 0 && !now.Before(state.volumeUntil) && state.status != "paused" && state.playing(now) {
				colors = input.tap.frame(hw.leds, input.spectrumLevel)
			}
			ringKey := strings.Join(colors, ",")
			if ringKey != oldRing || now.Sub(lastRingSent) > 3*time.Second {
				e := api.leds(colors)
				logAPIError("ring", e, &lastUIError)
				if e == nil {
					oldRing = ringKey
					lastRingSent = now
				}
			}
			if musicPolicy != nil && musicPolicy.Mode == "clock" && !paused {
				// Luna only needs the transition to native time. No AirPlay
				// clock frame or lease is active in this mode.
				if oldFrameKey != "native-time" {
					err := api.musicSegments(map[string]uint8{}, *musicPolicy)
					logAPIError("clock", err, &lastClockError)
					if err == nil {
						oldFrameKey = "native-time"
					}
				}
				continue
			}
			timeValue, frame := state.display(now, tz)
			var key string
			if !state.nativeClockVolume {
				if frame == nil {
					key = "time:" + timeValue
				} else {
					b, _ := json.Marshal(frame)
					key = "seg:" + string(b)
				}
			}
			if state.nativeClockVolume || key != oldFrameKey || now.Sub(lastClockSent) >= 1200*time.Millisecond {
				var err error
				if frame == nil {
					err = api.time(timeValue)
				} else if musicPolicy != nil {
					policy := *musicPolicy
					if paused {
						// Keep the clock hidden while the static PAUS frame is visible.
						policy.Mode = "wave"
					}
					err = api.musicSegments(frame, policy)
				} else {
					err = api.segments(frame)
				}
				logAPIError("clock", err, &lastClockError)
				if err == nil {
					oldFrameKey = key
					lastClockSent = now
				}
			}
		}
	}
}
