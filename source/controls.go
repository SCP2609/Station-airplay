package main

// Optional shared controls. All state belongs to this YAM service invocation;
// no log follower, personal paths, extra supervisor or global preload.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type controlsConfig struct {
	// Omitted keeps the LED spectrum on; false restores the solid indication
	// frames without opening a PCM tap or reading cover art.
	Spectrum           *bool        `json:"spectrum,omitempty"`
	SpectrumBrightness float64      `json:"spectrum_brightness,omitempty"`
	SpectrumGainR      float64      `json:"spectrum_gain_r,omitempty"`
	SpectrumGainG      float64      `json:"spectrum_gain_g,omitempty"`
	SpectrumGainB      float64      `json:"spectrum_gain_b,omitempty"`
	LEDColorProfile    string       `json:"led_color_profile,omitempty"`
	Voice              bool         `json:"voice"`
	NativeVolume       bool         `json:"native_volume"`
	VolumeBackend      string       `json:"volume_backend,omitempty"`
	VolumeDependency   string       `json:"volume_dependency,omitempty"`
	Ducking            bool         `json:"ducking"`
	LEDs               bool         `json:"leds"`
	LEDLevel           uint8        `json:"led_level,omitempty"`
	NativePlayer       bool         `json:"native_player,omitempty"`
	AliceButton        bool         `json:"alice_button,omitempty"`
	ClockWaveStyle     string       `json:"clock_wave_style,omitempty"`
	ClockMode          string       `json:"clock_mode,omitempty"`
	ClockWaveSeconds   int          `json:"clock_wave_seconds,omitempty"`
	ClockTimeSeconds   int          `json:"clock_time_seconds,omitempty"`
	Group              *groupConfig `json:"group,omitempty"`
}

func (c *controlsConfig) useSpectrum() bool {
	return c != nil && c.LEDs && (c.Spectrum == nil || *c.Spectrum)
}

// volumeBackend values. An empty value means "luna when the runtime API is
// there, station-volume otherwise", so an existing device config keeps working
// after the package upgrade.
const (
	volumeBackendLuna    = "luna"
	volumeBackendPackage = "package"
)

func loadControls(path string) (*controlsConfig, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 16385))
	d.DisallowUnknownFields()
	var c controlsConfig
	if err = d.Decode(&c); err != nil {
		return nil, err
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return nil, errors.New("trailing control configuration")
	}
	if c.SpectrumBrightness < 0 || c.SpectrumBrightness > 2 {
		return nil, errors.New("spectrum_brightness must be between 0 and 2")
	}
	for _, gain := range []float64{c.SpectrumGainR, c.SpectrumGainG, c.SpectrumGainB} {
		if gain < 0 || gain > 4 {
			return nil, errors.New("spectrum_gain_r/g/b must be between 0 and 4")
		}
	}
	if c.LEDColorProfile != "" && c.LEDColorProfile != "red" && c.LEDColorProfile != "blue" {
		return nil, errors.New("led_color_profile must be red or blue")
	}
	if c.VolumeBackend != "" && c.VolumeBackend != volumeBackendLuna && c.VolumeBackend != volumeBackendPackage {
		return nil, errors.New("volume_backend must be luna or package")
	}
	if c.VolumeBackend == volumeBackendLuna && c.VolumeDependency != "" {
		return nil, errors.New("volume_dependency is only valid with volume_backend package")
	}
	if c.VolumeDependency != "" && (!c.NativeVolume || !validVolumeDependency(c.VolumeDependency)) {
		return nil, errors.New("volume_dependency requires native_volume and a simple runtime alias")
	}
	if err := c.validateClock(); err != nil {
		return nil, err
	}
	return &c, nil
}

// loadControlsOptional treats a missing configuration file as "controls
// disabled": the package ships --controls-config, so a host without the file
// must keep working as an audio-only receiver instead of failing to start.
func loadControlsOptional(path string) (*controlsConfig, error) {
	c, err := loadControls(path)
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return c, err
}

type receiverState struct {
	Active, Available bool
	Connected         bool // receiver-confirmed live AirPlay 2 event channel
	Client            string
	PlayerState       string
	DB                float64
}
type speechEvent struct {
	Phase           string `json:"phase"`
	RequestID       string `json:"request_id"`
	Transcript      string `json:"transcript"`
	Final           bool   `json:"final"`
	InitialSnapshot bool   `json:"initial_snapshot"`
}
type controlReceiver interface {
	State(context.Context) (receiverState, error)
	Command(context.Context, string) error
	Volume(context.Context, float64) error
	Gain(context.Context, uint32, uint32) error
}
type controlStation interface {
	GetVolume(context.Context) (float64, error)
	SetVolume(context.Context, int) (float64, error)
	Colors(context.Context, []string) error
	Off(context.Context) error
	ClaimSpeech(context.Context, string) error
}

// indicationStation is implemented where the ring is not a sysfs colour list.
// Station 2 exposes an 89x1 framebuffer, so the supervisor paints a whole
// frame instead of a list of per-LED colours.
type indicationStation interface {
	Indicate(context.Context, string, uint8) error
}

func validDB(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && (v == -144 || v >= -30 && v <= 0)
}

// The station's percent scale is roughly linear in acoustic output, while the
// AirPlay sender's slider arrives as attenuation in dB. Mapping dB linearly to
// percent made the middle of the sender slider much quieter than the same
// position on the station. This curve lifts the middle while keeping both
// endpoints, and 10% quantization matches the station's own step.
const (
	volumeCurveExponent = 0.8
	volumeStepPercent   = 10
)

func quantizeVolumePercent(percent int) int {
	if percent <= 0 {
		return 0
	}
	step := int(math.Round(float64(percent)/volumeStepPercent)) * volumeStepPercent
	if step < 0 {
		step = 0
	}
	if step > 100 {
		step = 100
	}
	return step
}

func dbPercent(v float64) int {
	return dbPercentStep(v, volumeStepPercent)
}
func dbPercentStep(v float64, step int) int {
	if step <= 0 {
		step = volumeStepPercent
	}
	if v == -144 {
		return 0
	}
	x := (v + 30) / 30
	if x < 0 {
		x = 0
	}
	if x > 1 {
		x = 1
	}
	percent := math.Round(100 * math.Pow(x, volumeCurveExponent))
	return int(math.Round(percent/float64(step))) * step
}
func percentDB(v int) float64 {
	if v <= 0 {
		return -144
	}
	x := math.Pow(float64(v)/100, 1/volumeCurveExponent)
	return x*30 - 30
}
func nativePercent(v float64) (int, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 100 {
		return 0, errors.New("invalid native volume")
	}
	// Station observations stay on the native 5% grid and are not rounded to
	// the sender step: publishing an observed 25% as 30% would raise the
	// station by one step on every session handoff.
	return int(math.Round(v/5)) * 5, nil
}

type sharedController struct {
	volumeStep                  int // Zero retains the historical sender step.
	cfg                         controlsConfig
	receiver                    controlReceiver
	station                     controlStation
	ledCount                    int
	active                      bool
	audioActive                 bool
	client                      string
	pausedUntil                 time.Time
	duckUntil                   time.Time
	ledOwned, ledSuspended      bool
	ledState                    string
	ledRefreshAt, ledQuietUntil time.Time
	spectrum                    *spectrumTap
	ledMu                       sync.Mutex
	ledPlay, ledFailed          atomic.Bool
	observedNative              int
	nativeObserved              bool
	aliceIdle                   bool
	speechFreshUntil            time.Time
	volumeReady                 bool
	lastDB                      float64
	lastNative                  int
	pendingNative               int
	remotePending               bool
	remoteOldDB                 float64
	remoteDeadline              time.Time
	seen                        map[string]time.Time
	broadcast                   func(context.Context, int) error
	native                      nativePauser
	nativeLive                  bool
	nativeArmUntil              time.Time
	nativeNextAt                time.Time
	buttonPauseUntil            time.Time
	buttonCadence               time.Duration
	buttonSupp                  *buttonSuppression
	// buttonSession gates the Luna Alice long-poll. Without a live sender the
	// stock firmware owns the button, and polling would only burn CPU on a
	// small device while competing with the vendor stack.
	buttonSession  atomic.Bool
	rendererUpdate func(rendererControlState)
}

func (c *sharedController) speech(ctx context.Context, e speechEvent, now time.Time) error {
	// Enum names come from the public Quasar AliceState schema. A lost IPC
	// observer is UNKNOWN, never fabricated IDLE. Gain also expires in receiver.
	aliceButtonDebug("speech: phase=%q final=%v initial=%v request=%v active=%v", e.Phase, e.Final, e.InitialSnapshot, e.RequestID != "", c.active)
	switch e.Phase {
	case "idle":
		c.aliceIdle = true
		c.speechFreshUntil = now.Add(3 * time.Second)
		c.ledSuspended = false
		if c.cfg.Ducking && !c.duckUntil.IsZero() {
			c.duckUntil = time.Time{}
			return c.receiver.Gain(ctx, 100, 0)
		}
		return nil // ignore retained final ASR on an IDLE snapshot.
	case "listening", "busy", "speaking", "shazam", "recognition":
		c.aliceIdle = false
		c.speechFreshUntil = now.Add(3 * time.Second)
	default:
		c.aliceIdle = false
		c.speechFreshUntil = time.Time{}
		c.ledOwned = false
		c.ledState = ""
		c.ledSuspended = true
		return nil
	}
	if !c.active {
		return nil
	}
	c.ledSuspended = true
	c.ledOwned = false // do NOT send Off over Alice's freshly acquired indication.
	c.ledState = ""
	if c.cfg.Ducking {
		c.duckUntil = now.Add(4 * time.Second)
		if err := c.receiver.Gain(ctx, 20, 4500); err != nil {
			return err
		}
	}
	if e.InitialSnapshot && e.RequestID != "" {
		if c.seen == nil {
			c.seen = make(map[string]time.Time)
		}
		c.seen[e.RequestID] = now
		return nil
	}
	if !c.cfg.Voice || !e.Final || e.RequestID == "" {
		return nil
	}
	command := controlPhrase(e.Transcript)
	if command == "" {
		return nil
	}
	if c.seen == nil {
		c.seen = make(map[string]time.Time)
	}
	for id, at := range c.seen {
		if now.Sub(at) > 30*time.Second {
			delete(c.seen, id)
		}
	}
	if _, duplicate := c.seen[e.RequestID]; duplicate {
		return nil
	}
	if len(c.seen) >= 128 {
		return errors.New("speech deduplication capacity exceeded")
	}
	s, err := c.receiver.State(ctx)
	if err != nil {
		return err
	}
	if !s.Available || s.Client == "" || s.Client != c.client {
		return errors.New("sender remote is unavailable or changed")
	}
	c.seen[e.RequestID] = now // don't replay an ambiguous/timed-out remote command.
	if err = c.station.ClaimSpeech(ctx, e.RequestID); err != nil {
		aliceButtonDebug("speech: claim failed: %v", err)
		return err
	}
	if err = c.voiceCommand(ctx, command); err != nil {
		aliceButtonDebug("speech: command=%s failed: %v", command, err)
		return err
	}
	aliceButtonDebug("speech: command=%s sent", command)
	if command == "Pause" {
		c.pausedUntil = now.Add(30 * time.Minute)
	} else {
		// A resume/skip ends the paused window; otherwise a stale window would
		// keep the indication (and the session) alive after the sender leaves.
		c.pausedUntil = time.Time{}
	}
	if c.cfg.NativePlayer && c.native != nil && command != "Pause" {
		// Alice also acts on "continue"/"next" herself and can restart her own
		// player next to AirPlay. Pause it immediately (Player Pause never
		// touches the sender or her speech output) and keep a bounded handover
		// armed for a late resume after the utterance.
		if err = c.native.Pause(ctx); err != nil {
			aliceButtonDebug("native: immediate pause failed: %v", err)
		} else {
			aliceButtonDebug("native: immediate pause after voice transport")
			c.nativeLive = true
		}
		c.nativeArmUntil = now.Add(12 * time.Second)
		c.nativeNextAt = time.Time{}
	}
	return nil
}

// Previous normally restarts the current song on the sender. A second press
// shortly afterwards selects the preceding item. Restart deliberately sends
// only one press; neither operation enables repeat mode.
func (c *sharedController) voiceCommand(ctx context.Context, command string) error {
	if command == "Restart" {
		return c.receiver.Command(ctx, "Previous")
	}
	if err := c.receiver.Command(ctx, command); err != nil {
		return err
	}
	if command != "Previous" {
		return nil
	}
	timer := time.NewTimer(350 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	// Never deliver the second press to a new or disconnected sender.
	s, err := c.receiver.State(ctx)
	if err != nil {
		return err
	}
	if !s.Available || s.Client == "" || s.Client != c.client {
		return errors.New("sender changed during previous-track command")
	}
	return c.receiver.Command(ctx, "Previous")
}

// button handles one short Alice-button press. It acts only while a sender owns
// the AirPlay session and Alice is not in a conversation, so the stock button
// keeps its meaning everywhere else. The stock short press reaches Quasar's own
// player first; a bounded native pause keeps that player from resuming next to
// AirPlay without grabbing or remapping the keypad.
func (c *sharedController) button(ctx context.Context, now time.Time) error {
	if !c.cfg.AliceButton {
		return nil
	}
	if !c.cfg.NativePlayer || c.native == nil {
		return errors.New("Alice button requires the native player handover")
	}
	s, err := c.receiver.State(ctx)
	if err != nil {
		aliceButtonDebug("button: receiver state failed: %v", err)
		return err
	}
	aliceButtonDebug("button: client=%q available=%v active=%v state=%q idle=%v fresh=%v", s.Client, s.Available, s.Active, s.PlayerState, c.aliceIdle, now.Before(c.speechFreshUntil))
	if s.Client == "" || !s.Available {
		aliceButtonDebug("button: no AirPlay session, stock behaviour")
		return nil // no AirPlay session: the firmware owns the button
	}
	if !c.aliceIdle && now.Before(c.speechFreshUntil) {
		aliceButtonDebug("button: dialogue owns the button")
		return nil // listening, speaking or a fresh dialogue owns the button
	}
	var nativeErr error
	if nativeErr = c.native.Pause(ctx); nativeErr != nil {
		aliceButtonDebug("button: native pause failed: %v", nativeErr)
		c.releaseNative()
	} else {
		c.nativeLive = true
		c.startButtonSuppression(ctx, now.Add(aliceButtonPauseHold))
	}
	playing := s.Active
	switch s.PlayerState {
	case "Playing":
		playing = true
	case "Paused", "Stopped":
		playing = false
	}
	command := "Play"
	if playing {
		command = "Pause"
	}
	cmdErr := c.receiver.Command(ctx, command)
	aliceButtonDebug("button: command=%s err=%v", command, cmdErr)
	if cmdErr == nil {
		// A sender that we paused is still a live AirPlay session: keep the
		// indication lit and let the voice transport ("продолжить") resume it
		// instead of letting Alice start her own player. The window is dropped
		// as soon as the sender changes or its control channel disappears.
		if command == "Pause" {
			c.pausedUntil = now.Add(30 * time.Minute)
		} else {
			c.pausedUntil = time.Time{}
		}
	}
	return errors.Join(nativeErr, cmdErr)
}

// buttonSuppression is the bounded cadence that keeps Quasar's own player
// paused after a short Alice-button press. It runs off the 500 ms control loop
// so the sender toggle is not delayed and Alice's player cannot resume between
// ticks; every refresh is a non-blocking write.
type buttonSuppression struct {
	stop chan struct{}
	done chan struct{}
}

func (c *sharedController) startButtonSuppression(ctx context.Context, until time.Time) {
	c.stopButtonSuppression()
	c.buttonPauseUntil = until
	s := &buttonSuppression{stop: make(chan struct{}), done: make(chan struct{})}
	c.buttonSupp = s
	cadence := c.buttonCadence
	if cadence <= 0 {
		cadence = aliceButtonPauseEvery
	}
	aliceButtonDebug("suppression: start cadence=%s hold=%s", cadence, time.Until(until).Round(time.Millisecond))
	// The caller's context belongs to one control-loop event and is cancelled
	// as soon as the press handler returns. The cadence is bounded by its own
	// window and by stopButtonSuppression, so it must not inherit that deadline.
	ctx = context.WithoutCancel(ctx)
	go func() {
		defer close(s.done)
		defer aliceButtonDebug("suppression: ended")
		ticker := time.NewTicker(cadence)
		defer ticker.Stop()
		timer := time.NewTimer(time.Until(until))
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.stop:
				return
			case <-timer.C:
				return
			case <-ticker.C:
				if err := c.native.PauseNow(); err != nil {
					aliceButtonDebug("suppression: pause failed: %v", err)
					return
				}
			}
		}
	}()
}

// stopButtonSuppression ends the cadence and waits briefly for its goroutine.
// PauseNow is a non-blocking write, so the wait is effectively immediate.
func (c *sharedController) stopButtonSuppression() {
	s := c.buttonSupp
	c.buttonSupp = nil
	c.buttonPauseUntil = time.Time{}
	if s == nil {
		return
	}
	close(s.stop)
	select {
	case <-s.done:
	case <-time.After(time.Second):
	}
}

func (c *sharedController) tick(ctx context.Context, now time.Time) error {
	c.ledPlay.Store(false)
	s, err := c.receiver.State(ctx)
	if err != nil {
		c.buttonSession.Store(false)
		c.audioActive = false
		c.volumeReady = false
		c.ledOwned = false
		c.ledState = ""
		c.ledSuspended = true
		return err
	}
	c.buttonSession.Store(s.Client != "" && s.Available)
	if !validDB(s.DB) {
		return errors.New("invalid AirPlay volume")
	}
	if s.Client != c.client {
		// A changed sender must never receive a stale resume or volume request.
		c.client = s.Client
		c.pausedUntil = time.Time{}
		c.volumeReady = false
		c.remotePending = false
		c.nativeObserved = false
		c.seen = nil
		c.ledSuspended = false
		c.ledState = ""
		// The button handover is sender-independent and already validated by the
		// press; keep it until its own bounded window ends.
		c.nativeArmUntil = time.Time{}
		c.nativeNextAt = time.Time{}
		if c.buttonSupp == nil {
			c.releaseNative()
		}
	}
	if !s.Available || s.Client == "" {
		// The sender's control channel is gone: a remembered pause must not keep
		// the indication lit after AirPlay has left.
		c.pausedUntil = time.Time{}
	}
	wasActive := c.active
	c.audioActive = s.Active
	// An attached AirPlay 2 output owns the session before RECORD/audio starts.
	// Available alone is not enough: classic DACP metadata may be retained after
	// a sender disconnects. Connected comes from the live paired event channel.
	c.active = s.Active || s.Connected && s.Client != "" || s.Available && s.Client != "" && now.Before(c.pausedUntil)
	// The button suppression runs on its own bounded cadence. The control loop
	// only releases it when the window has elapsed in loop time, so a sender
	// that reports Paused still keeps the native player suppressed.
	if c.buttonSupp != nil && !now.Before(c.buttonPauseUntil) {
		if c.nativeArmUntil.IsZero() {
			c.releaseNative()
		} else {
			c.stopButtonSuppression()
		}
	}
	if !c.active {
		c.volumeReady = false
		c.remotePending = false
		c.ledSuspended = false
		if c.buttonSupp == nil {
			c.releaseNative()
		}
		if c.cfg.Ducking && !c.duckUntil.IsZero() {
			c.duckUntil = time.Time{}
			if err = c.receiver.Gain(ctx, 100, 0); err != nil {
				return err
			}
		}
		if c.ledOwned {
			c.ledOwned = false
			c.ledState = ""
			return c.station.Off(ctx)
		}
		return nil
	}
	if !wasActive {
		c.volumeReady = false
	}
	if c.cfg.Ducking && now.Before(c.duckUntil) && now.Before(c.speechFreshUntil) {
		if err = c.receiver.Gain(ctx, 20, 4500); err != nil {
			return err
		}
	}
	if c.cfg.Ducking && !c.duckUntil.IsZero() && !now.Before(c.duckUntil) {
		if err = c.receiver.Gain(ctx, 100, 0); err != nil {
			return err
		}
		c.duckUntil = time.Time{}
	}
	var nativeErr error
	if c.cfg.NativePlayer && c.native != nil && !c.nativeArmUntil.IsZero() {
		switch {
		case !now.Before(c.nativeArmUntil):
			aliceButtonDebug("native: voice handover window expired")
			c.nativeArmUntil = time.Time{}
			c.nativeNextAt = time.Time{}
			if c.buttonSupp == nil {
				c.releaseNative()
			}
		// The bounded window is the freshness proof. Alice's own player can
		// start several seconds after the utterance, so a stale speech event
		// must not hide it: aliceIdle already excludes listening/speaking.
		case c.aliceIdle && !now.Before(c.nativeNextAt):
			c.nativeNextAt = now.Add(time.Second)
			if nativeErr = c.native.Pause(ctx); nativeErr != nil {
				aliceButtonDebug("native: voice handover pause failed: %v", nativeErr)
				c.releaseNative()
			} else {
				aliceButtonDebug("native: voice handover paused native player")
				c.nativeLive = true
			}
		}
	}
	var volumeErr error
	if c.cfg.NativeVolume {
		volumeErr = c.syncVolume(ctx, s, now)
	}
	// Sender feedback must not gate unrelated indication. Repaint slowly after
	// native volume has settled: its overlay can replace our one-shot frame.
	// An observed legacy renderer remains the sole UI owner. Keep LEDs in
	// the config so indication-only setups still subscribe to Alice's state.
	// Alice's idle window grants a repaint right after she stops talking. A
	// running session must keep repainting outside it as well: otherwise the
	// first frame painted before the artwork arrived stays on the LEDs until
	// Alice speaks again, and a new track never recolours them.
	fresh := now.Before(c.speechFreshUntil)
	if c.cfg.LEDs && c.rendererUpdate == nil && c.ledCount > 0 && c.aliceIdle && (fresh || c.active) && !c.ledSuspended && !now.Before(c.ledQuietUntil) {
		state := "playing"
		if !s.Active || now.Before(c.pausedUntil) {
			state = "paused"
		}
		level := c.cfg.LEDLevel
		if level == 0 {
			if _, ok := c.station.(indicationStation); ok {
				level = 200 // Station 2's previous playback frame was 0xcb
			} else {
				level = 16 // retain the original opt-in default
			}
		}
		if state == "playing" && c.spectrum != nil && !c.ledFailed.Load() && c.spectrum.playing(now) {
			// The dedicated animator paints at 10 Hz. The tick only publishes
			// the gate and keeps the same ownership bookkeeping.
			c.ledPlay.Store(true)
			c.ledOwned = true
			c.ledState = "playing"
			c.ledRefreshAt = now.Add(100 * time.Millisecond)
			return errors.Join(nativeErr, volumeErr)
		}
		if ind, ok := c.station.(indicationStation); ok {
			// Framebuffer rings paint a whole frame. Repaint on a state change
			// and slowly afterwards in case Alice redrew her own indication.
			if !c.ledOwned || c.ledState != state || !now.Before(c.ledRefreshAt) {
				if err = ind.Indicate(ctx, state, level); err != nil {
					if errors.Is(err, errFramebufferBusy) {
						// Firmware/Jarvis owns the frame. Do not fight it with a
						// refresh or send Off over its newly painted animation.
						c.ledOwned, c.ledState = false, ""
						return errors.Join(nativeErr, volumeErr)
					}
					aliceButtonDebug("led: indicate failed: %v", err)
					return errors.Join(volumeErr, err)
				}
				aliceButtonDebug("led: framebuffer state=%s level=%d", state, level)
				c.ledOwned, c.ledState = true, state
				c.ledRefreshAt = now.Add(3 * time.Second)
			}
		} else if !c.ledOwned || !now.Before(c.ledRefreshAt) {
			colors := make([]string, c.ledCount)
			for i := range colors {
				colors[i] = fmt.Sprintf("#00%02x%02x", level, level/2)
			}
			// Solid indication follows the cover too; the teal frame above is
			// only the fallback while the artwork is unknown.
			if c.spectrum != nil {
				if art := c.spectrum.solidFrame(c.ledCount, level); art != nil {
					colors = art
				}
			}
			c.ledMu.Lock()
			err = c.station.Colors(ctx, colors)
			c.ledMu.Unlock()
			if err != nil {
				aliceButtonDebug("led: colors failed: %v", err)
				return errors.Join(volumeErr, err)
			}
			aliceButtonDebug("led: ring state=%s level=%d", state, level)
			c.ledOwned = true
			// A solid frame is cheap; repaint often so a new cover and its
			// palette appear without a multi-second lag.
			c.ledRefreshAt = now.Add(500 * time.Millisecond)
		}
	}
	return errors.Join(nativeErr, volumeErr)
}

// releaseNative ends a handover and terminates the SDK session; it is called
// on expiry, sender change, inactivity and shutdown so no CLI process remains.
func (c *sharedController) releaseNative() {
	c.nativeArmUntil = time.Time{}
	c.nativeNextAt = time.Time{}
	c.stopButtonSuppression()
	if c.native != nil && c.nativeLive {
		c.nativeLive = false
		c.native.Stop()
	}
}

func (c *sharedController) syncVolume(ctx context.Context, s receiverState, now time.Time) error {
	// A known new sender level wins after initial handoff. SetVolume still
	// performs the backend's guarded write and readback; avoid only the
	// redundant native read before that operation in the same tick budget.
	if c.volumeReady && !c.remotePending && s.Available && s.Client != "" && validDB(s.DB) && math.Abs(s.DB-c.lastDB) > 0.01 {
		return c.applySenderVolume(ctx, s, now)
	}
	v, err := c.station.GetVolume(ctx)
	if err != nil {
		c.volumeReady = false
		c.ledQuietUntil = now.Add(3 * time.Second)
		return err
	}
	native, err := nativePercent(v)
	if err != nil {
		c.volumeReady = false
		return err
	}
	if c.nativeObserved && native != c.observedNative {
		c.ledQuietUntil = now.Add(time.Second)
	}
	c.nativeObserved = true
	c.observedNative = native
	if c.remotePending {
		if math.Abs(s.DB-c.lastDB) <= 0.01 {
			c.remotePending = false
		} else if math.Abs(s.DB-c.remoteOldDB) <= 0.01 && now.Before(c.remoteDeadline) {
			return nil
		} else if !now.Before(c.remoteDeadline) {
			c.remotePending = false
			c.volumeReady = false
			return errors.New("sender volume acknowledgement timed out")
		} else {
			c.remotePending = false
		} // A genuinely new phone-side change wins.
	}
	if !c.volumeReady {
		// Start at the station's current native level, never at a remembered
		// handset level. Publish it to the sender without writing native volume.
		// Until its control channel is ready, keep native unchanged and retry.
		if !s.Available || s.Client == "" {
			return nil
		}
		target := percentDB(native)
		if math.Abs(s.DB-target) > 0.01 {
			if err = c.receiver.Volume(ctx, target); err != nil {
				return err
			}
			// Metadata may still contain the old sender value for several ticks.
			// Do not echo that value into native volume while awaiting the ack.
			c.remotePending = true
			c.remoteOldDB = s.DB
			c.remoteDeadline = now.Add(3 * time.Second)
		}
		c.lastDB = target
		c.lastNative = native
		c.pendingNative = -1
		c.volumeReady = true
	} else if math.Abs(s.DB-c.lastDB) > 0.01 {
		return c.applySenderVolume(ctx, s, now)
	} else if native != c.lastNative && !now.Before(c.duckUntil) {
		// Two stable observations suppress native transition/quantisation noise.
		if c.pendingNative == native {
			if !s.Available {
				return nil
			}
			if err = c.receiver.Volume(ctx, percentDB(native)); err != nil {
				return err
			}
			c.remotePending = true
			c.remoteOldDB = s.DB
			c.remoteDeadline = now.Add(3 * time.Second)
			c.lastNative = native
			c.lastDB = percentDB(native)
			c.pendingNative = -1
			if c.broadcast != nil {
				if err = c.broadcast(ctx, native); err != nil {
					return err
				}
			}
		} else {
			c.pendingNative = native
		}
	} else {
		c.pendingNative = -1
	}
	return nil
}

func (c *sharedController) applySenderVolume(ctx context.Context, s receiverState, now time.Time) error {
	actual, err := c.station.SetVolume(ctx, dbPercentStep(s.DB, c.volumeStep))
	if err != nil {
		c.volumeReady = false
		return err
	}
	native, err := nativePercent(actual)
	if err != nil {
		c.volumeReady = false
		return err
	}
	// SetVolume returned a confirmed readback. Publish it to the renderer
	// now instead of showing the previous level until the next poll.
	c.nativeObserved = true
	c.observedNative = native
	c.lastNative = native
	c.lastDB = s.DB
	c.pendingNative = -1
	c.ledQuietUntil = now.Add(time.Second)
	return nil
}

// Stop must not restore Master, restart Alice, touch clocks, or paint over Alice.
func (c *sharedController) close(ctx context.Context) {
	if c.cfg.Ducking {
		_ = c.receiver.Gain(ctx, 100, 0)
	}
	if c.ledOwned {
		_ = c.station.Off(ctx)
		c.ledOwned = false
		c.ledState = ""
	}
	c.releaseNative()
}

func supportedLEDCount(d yamDevice) int {
	// Only documented YAM capabilities, never a guessed Station2 framebuffer.
	// A station exposes either an RGB ring (led.ring.rgb/led.ring.N) or an RGB
	// logo light (led.logo.rgb/led.logo.N); both answer the same /led API.
	prefix := ""
	switch {
	case feature(d, "led.ring.rgb"):
		prefix = "led.ring."
	case feature(d, "led.logo.rgb"):
		prefix = "led.logo."
	default:
		return 0
	}
	// The zone count is advertised per model (3, 4, 6, 12, 24, 36...). Read the
	// number instead of keeping a list that silently disables new stations.
	count := 0
	for _, f := range d.Features {
		value, ok := strings.CutPrefix(f, prefix)
		if !ok || value == "rgb" {
			continue
		}
		n, err := strconv.Atoi(value)
		if err == nil && n > count {
			count = n
		}
	}
	return count
}

// spectrumSupported keeps artwork and the PCM spectrum off the big-screen
// stations. Every other model uses the capability check, so a station without
// an RGB ring stays on its existing indication.
func spectrumSupported(d yamDevice) bool {
	switch d.Model {
	case "yandexstation_2", "yandexstation_3", "orion":
		return false
	}
	return supportedLEDCount(d) > 0
}
