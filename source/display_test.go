package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"
)

func TestInitialNativeVolumeDoesNotCreateOverlay(t *testing.T) {
	s := &airplayState{}
	now := time.Now()
	s.observeNativeVolume(35, true, now)
	if s.volume != 35 || !s.volumeUntil.IsZero() {
		t.Fatal("initial readback must establish a baseline without a volume overlay")
	}
	s.observeNativeVolume(30, true, now.Add(time.Second))
	if s.volume != 30 || !s.volumeDown || !s.volumeUntil.After(now.Add(time.Second)) {
		t.Fatal("a real volume change must still create its overlay")
	}
	s.observeNativeVolume(45, false, now.Add(2*time.Second))
	if !s.volumeUntil.IsZero() {
		t.Fatal("an inactive session must not carry an overlay into next playback")
	}
	s.observeNativeVolume(45, true, now.Add(3*time.Second))
	if !s.volumeUntil.IsZero() {
		t.Fatal("session start replayed the baseline volume")
	}
}

func TestNativeClockVolumeLeavesDigitsToFirmware(t *testing.T) {
	withProfile(t, plumProfile)
	now := time.Now()
	s := &airplayState{status: "playing", nativeClockVolume: true}
	s.showVolume(35, now)
	_, frame := s.display(now, time.UTC)
	for name := range frame {
		if name[0] != 'A' && name[0] != 'D' && name[0] != 'G' {
			t.Fatalf("cooperative music request contains its own volume digits: %s", name)
		}
	}
}

func TestLiteVolumeDecreaseUsesLeftDigits(t *testing.T) {
	p := plumProfile
	p.sharedUI = true
	withProfile(t, p)
	s := &airplayState{volume: 40}
	now := time.Now()
	s.showVolume(35, now)
	_, got := s.display(now, time.UTC)
	if want := segmentsFor("35  ", false); !reflect.DeepEqual(got, want) {
		t.Fatalf("decrease must use left digits: %v", got)
	}
	s.showVolume(40, now)
	_, got = s.display(now, time.UTC)
	if want := segmentsFor("  40", false); !reflect.DeepEqual(got, want) {
		t.Fatal("increase must use right digits")
	}
}

func TestSharedDisplayUsesOnlyNativeVolumeForOverlay(t *testing.T) {
	s := &airplayState{status: "stopped"}
	now := time.Now()
	s.showVolume(30, now)
	until := s.volumeUntil
	// A sender's -23 dB maps to 23% in the legacy display, but the
	// shared controller has already confirmed the station's 30% level.
	s.displayMetadata("73736e63", "70766f6c", []byte("-23,0,0,0"), now.Add(100*time.Millisecond), true)
	if s.volume != 30 || !s.volumeUntil.Equal(until) {
		t.Fatalf("sender metadata replaced/retriggered native overlay: volume=%d until=%v", s.volume, s.volumeUntil)
	}
	s.showVolume(30, now.Add(500*time.Millisecond))
	if !s.volumeUntil.Equal(until) {
		t.Fatal("unchanged native feedback extends the overlay")
	}
	s.displayMetadata("73736e63", "70617573", nil, now, true)
	if s.status != "paused" {
		t.Fatal("native volume filtering dropped playback metadata")
	}
	s.displayMetadata("73736e63", "70766f6c", []byte("-15,0,0,0"), now, false)
	if s.volume != 50 {
		t.Fatal("software-volume mode lost sender volume indication")
	}
}

func TestMidiSharedDisplayKeepsItsGeometryAndYieldsToAlice(t *testing.T) {
	testSharedDisplay(t, midiProfile, 24, '1', '4')
}

func TestMidiMusicCapabilityUsesSharedLeaseAndConfiguredMode(t *testing.T) {
	p, ok := profileForDevice(yamDevice{Model: "yandexmidi", Features: []string{
		"display.clock.segment.v1", "display.clock.music.v1",
	}}, true)
	if !ok || !p.sharedUI {
		t.Fatalf("MIDI clock capabilities did not select the shared renderer: %+v", p)
	}
	testSharedDisplay(t, p, 24, '1', '4', "", "wave")
}

func TestLiteSharedDisplaySendsDefaultMusicPolicy(t *testing.T) {
	p, _ := profileForDevice(yamDevice{Model: "yandexplum", Features: []string{"display.clock.segment.v1", "led.ring.rgb", "led.ring.3", "display.clock.music.v1"}}, true)
	if !p.sharedUI {
		t.Fatal("missing host music policy")
	}
	testSharedDisplay(t, p, 3, '0', '3')
}

func TestLiteSharedDisplayUsesBottomUpStyle(t *testing.T) {
	p, _ := profileForDevice(yamDevice{Model: "yandexplum", Features: []string{"display.clock.segment.v1", "led.ring.rgb", "led.ring.3", "display.clock.music.v1"}}, true)
	testSharedDisplay(t, p, 3, '0', '3', "bottom_up")
}

func TestLiteSharedDisplayShowsPauseInClockMode(t *testing.T) {
	p, _ := profileForDevice(yamDevice{Model: "yandexplum", Features: []string{"display.clock.segment.v1", "led.ring.rgb", "led.ring.3", "display.clock.music.v1"}}, true)
	testSharedDisplay(t, p, 3, '0', '3', "", "clock")
}

func TestMidiIgnoresLiteWaveStyle(t *testing.T) {
	testSharedDisplay(t, midiProfile, 24, '1', '4', "bottom_up")
}

func testSharedDisplay(t *testing.T, profile stationProfile, leds int, firstDigit, lastDigit byte, styles ...string) {
	t.Helper()
	withProfile(t, profile)
	style := ""
	if len(styles) > 0 {
		style = styles[0]
	}
	controls := &controlsConfig{LEDs: true, NativeVolume: true}
	if len(styles) > 1 {
		controls.ClockMode = styles[1]
	}
	controls.ClockWaveStyle = style
	events := make(chan string, 128)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind := r.URL.Path
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.URL.Path == "/led" {
			var colors []string
			_ = json.Unmarshal(body["colors"], &colors)
			if len(colors) != leds {
				t.Errorf("renderer LED count: got %d, want %d", len(colors), leds)
			}
		}
		if r.URL.Path == "/clock" {
			if string(body["off"]) == "true" {
				kind = "clock-off"
			} else {
				kind = "clock-frame"
				var segments map[string]uint8
				_ = json.Unmarshal(body["segments"], &segments)
				if reflect.DeepEqual(segments, segmentsFor("PAUS", false)) {
					kind = "clock-pause"
					var policy clockMusicPolicy
					if err := json.Unmarshal(body["music"], &policy); err != nil {
						t.Error(err)
					}
					if policy.Mode != "wave" {
						t.Errorf("pause must keep native time hidden during its short display: %+v", policy)
					}
				} else if profile.sharedUI {
					var policy clockMusicPolicy
					if err := json.Unmarshal(body["music"], &policy); err != nil {
						t.Error(err)
					}
					if policy != controls.clockPolicy() {
						t.Errorf("wrong music policy: %+v", policy)
					}
				} else if _, ok := body["music"]; ok {
					t.Error("policy sent to an unsupported host/profile")
				}
				if segments != nil {
					if style == "bottom_up" && kind == "clock-frame" {
						bottom := segments[segment('D', 0)]
						middle := segments[segment('G', 0)]
						if profile.sharedUI {
							if bottom == 0 || middle != 0 {
								t.Errorf("silent PCM should draw bottom baseline: %v", segments)
							}
						} else if middle == 0 || bottom != 0 {
							t.Errorf("other profile lost centered style: %v", segments)
						}
					}
					for digit := range segments {
						if len(digit) != 2 || digit[1] < firstDigit || digit[1] > lastDigit {
							t.Errorf("wrong segment %q", digit)
						}
					}
				}
			}
		}
		select {
		case events <- kind:
		default:
			t.Error("display request flood")
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	api := &yamAPI{base: server.URL, tokenHeader: "Authorization", token: "test", client: server.Client()}
	meta, err := ensureFIFO(filepath.Join(t.TempDir(), "metadata"))
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(meta)
	pcm := make(chan visualizerPCM, 4)
	updates := make(chan rendererControlState, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- renderDisplay(ctx, api, meta, nil, displayInput{profile: profile, controls: controls, pcm: pcm, updates: updates})
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	waitFor := func(want string, timeout time.Duration) {
		t.Helper()
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		for {
			select {
			case got := <-events:
				if got == want {
					return
				}
			case <-timer.C:
				t.Fatalf("display did not emit %s", want)
			}
		}
	}
	wait := func(want string) {
		t.Helper()
		waitFor(want, time.Second)
	}
	update := rendererControlState{Session: true, Attached: true, Paint: true, Expires: time.Now().Add(time.Minute)}
	updates <- update
	pcm <- visualizerPCM{data: make([]byte, 4096), at: time.Now()}
	wait("/led")
	wait("clock-frame")
	if controls.ClockMode == "clock" {
		timer := time.NewTimer(150 * time.Millisecond)
	clockIdle:
		for {
			select {
			case got := <-events:
				if got == "clock-frame" {
					timer.Stop()
					t.Fatal("native clock mode repeatedly requests the display")
				}
			case <-timer.C:
				break clockIdle
			}
		}
	}
	if profile.sharedUI {
		// Pausing stops PCM and can clear Active without closing the event channel.
		pcm <- visualizerPCM{data: make([]byte, 4096), at: time.Now().Add(-2 * time.Second)}
		update.Session = false
		updates <- update
		if _, err := syscall.Write(meta, []byte("<item><type>73736e63</type><code>70617573</code><length>0</length></item>")); err != nil {
			t.Fatal(err)
		}
		wait("clock-pause")
		update.Paint = false
		updates <- update
		wait("clock-off")
		select {
		case got := <-events:
			if got == "clock-pause" {
				t.Fatal("paused music overwrites Alice")
			}
		case <-time.After(100 * time.Millisecond):
		}
		update.Paint = true
		updates <- update
		wait("clock-pause")
		if controls.ClockMode == "clock" {
			waitFor("clock-off", 3*time.Second)
			if _, err := syscall.Write(meta, []byte("<item><type>73736e63</type><code>70617573</code><length>0</length></item>")); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-events:
				if got == "clock-pause" {
					t.Fatal("duplicate pause metadata prolonged the display")
				}
			case <-time.After(150 * time.Millisecond):
			}
		}
		update.Attached = false
		updates <- update
		if controls.ClockMode != "clock" {
			wait("clock-off")
		}
		if _, err := syscall.Write(meta, []byte("<item><type>73736e63</type><code>70726573</code><length>0</length></item>")); err != nil {
			t.Fatal(err)
		}
		pcm <- visualizerPCM{data: make([]byte, 4096), at: time.Now()}
		update.Session = true
		update.Attached = true
		updates <- update
		wait("clock-frame")
	}
	update.Paint = false
	updates <- update
	wait("/led/off")
	wait("clock-off")
	select {
	case got := <-events:
		if got == "/led" || got == "clock-frame" {
			t.Fatalf("renderer overwrites Alice: %s", got)
		}
	case <-time.After(100 * time.Millisecond):
	}
	update.Paint = true
	updates <- update
	wait("/led")
	wait("clock-frame")
	update.Session = false
	update.Attached = false
	updates <- update
	wait("/led/off")
	wait("clock-off")
	update.Session = true
	update.Expires = time.Now().Add(-time.Second)
	updates <- update
	pcm <- visualizerPCM{data: make([]byte, 4096), at: time.Now()}
	select {
	case got := <-events:
		if got == "/led" || got == "clock-frame" {
			t.Fatalf("renderer paints with an expired controller lease: %s", got)
		}
	case <-time.After(100 * time.Millisecond):
	}
}

func TestMini3SharedDisplayUsesClockModesAndStyles(t *testing.T) {
	for _, style := range []string{"", "center", "bottom_up"} {
		t.Run(style, func(t *testing.T) {
			p, _ := profileForDevice(yamDevice{Model: "yandexmini3", Features: []string{"display.clock.segment.v1", "display.clock.music.v1", "led.ring.rgb", "led.ring.6"}}, false)
			if !p.sharedUI {
				t.Fatal("Mini3 missing cooperative clock")
			}
			testSharedDisplay(t, p, 6, '0', '3', style)
		})
	}
}
