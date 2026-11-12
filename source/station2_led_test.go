package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openTestStation2LED(t *testing.T, depth string, bpp int) (*framebufferLED, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fb0")
	if err := os.WriteFile(path, make([]byte, station2LEDs*bpp), 0o600); err != nil {
		t.Fatal(err)
	}
	size := filepath.Join(dir, "virtual_size")
	if err := os.WriteFile(size, []byte("89,1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	depthPath := filepath.Join(dir, "bits_per_pixel")
	if err := os.WriteFile(depthPath, []byte(depth+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fb, err := openFramebufferLED(path, size, depthPath)
	if err != nil {
		t.Fatal(err)
	}
	// Unit tests have no maind --platformd to lease. Keep the paint path and
	// switch off only the ptrace step.
	fb.attach = nil
	t.Cleanup(func() { _ = fb.Close() })
	return fb, path
}

func TestStation2ColorsEncodeRGBW(t *testing.T) {
	fb, path := openTestStation2LED(t, "32", 4)
	colors := make([]string, station2LEDs)
	for i := range colors {
		colors[i] = "#112233"
	}
	if err := fb.Colors(context.Background(), colors); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// R, G, B, W: the white die stays off for a coloured frame.
	want := []byte{0x11, 0x22, 0x33, 0x00}
	if !bytes.Equal(got[:4], want) || !bytes.Equal(got[len(got)-4:], want) {
		t.Fatalf("RGBW pixel: got % x want % x", got[:4], want)
	}
}

func TestStation2ColorsEncodePackedRGB(t *testing.T) {
	fb, path := openTestStation2LED(t, "24", 3)
	colors := make([]string, station2LEDs)
	for i := range colors {
		colors[i] = "#112233"
	}
	if err := fb.Colors(context.Background(), colors); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got[:3], []byte{0x11, 0x22, 0x33}) {
		t.Fatalf("packed RGB pixel: % x", got[:3])
	}
}

func TestStation2CalmFillIsSteadyAndDim(t *testing.T) {
	fb, path := openTestStation2LED(t, "32", 4)
	if err := fb.Show(context.Background(), "paused", 200); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	// led_level 200 * 30% = 60; the sysfs stations' #00GGGG/2 family.
	if !bytes.Equal(got[:4], []byte{0x00, 60, 30, 0x00}) {
		t.Fatalf("calm pixel: % x", got[:4])
	}
	// The same frame must not be rewritten every tick.
	before := append([]byte(nil), got...)
	if err := fb.Show(context.Background(), "paused", 200); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(path)
	if !bytes.Equal(before, again) {
		t.Fatal("steady fill was rewritten")
	}
}

func TestStation2ReleaseHandsTheRingBackWithoutBlack(t *testing.T) {
	fb, path := openTestStation2LED(t, "32", 4)
	if err := fb.Show(context.Background(), "paused", 200); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := fb.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("release painted over the station's frame")
	}
}

func TestStation2BusyLeaseDoesNotWrite(t *testing.T) {
	fb, path := openTestStation2LED(t, "32", 4)
	fb.attach = func() error { return errors.New("ptrace denied") }
	if err := fb.Show(context.Background(), "paused", 200); !errors.Is(err, errFramebufferBusy) {
		t.Fatalf("Show error = %v", err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, make([]byte, station2LEDs*4)) {
		t.Fatal("painted without the ring lease")
	}
}

func TestStation2GeometryAndDepthAreExact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fb0")
	_ = os.WriteFile(path, make([]byte, 32), 0o600)
	size := filepath.Join(dir, "virtual_size")
	depth := filepath.Join(dir, "bits_per_pixel")
	_ = os.WriteFile(depth, []byte("32\n"), 0o600)
	for _, geometry := range []string{"8,1", "89,2", "89,1,extra", ""} {
		if err := os.WriteFile(size, []byte(geometry), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := openFramebufferLED(path, size, depth); err == nil {
			t.Fatalf("geometry %q accepted", geometry)
		}
	}
	if err := os.WriteFile(size, []byte("89,1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, unsupported := range []string{"16", "24x", ""} {
		if err := os.WriteFile(depth, []byte(unsupported), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := openFramebufferLED(path, size, depth); err == nil {
			t.Fatalf("depth %q accepted", unsupported)
		}
	}
	if err := os.WriteFile(depth, []byte("24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if fb, err := openFramebufferLED(path, size, depth); err != nil {
		t.Fatal(err)
	} else {
		_ = fb.Close()
	}
}

func TestStation2ColorsRequireTheWholeRing(t *testing.T) {
	fb, _ := openTestStation2LED(t, "32", 4)
	if err := fb.Colors(context.Background(), []string{"#000000"}); err == nil {
		t.Fatal("short frame accepted")
	}
	if err := fb.Show(context.Background(), "unknown", 200); err == nil {
		t.Fatal("unknown state accepted")
	}
}

// The controls loop must drive the framebuffer station by state, not by the
// sysfs colour list, and it must release the ring when the sender leaves.
func TestFramebufferIndicationFollowsPlaybackState(t *testing.T) {
	r := &mockReceiver{s: receiverState{Active: true, Available: true, Client: "mock-sender", DB: -15}}
	base := &mockStation{v: 70}
	ind := &mockIndicationStation{mockStation: base}
	c := &sharedController{
		cfg:      controlsConfig{Voice: true, LEDs: true, LEDLevel: 200},
		receiver: r,
		station:  ind,
		ledCount: 1,
	}
	c.aliceIdle = true
	c.speechFreshUntil = time.Now().Add(time.Hour)
	ctx, now := context.Background(), time.Now()
	if err := c.tick(ctx, now); err != nil {
		t.Fatal(err)
	}
	if len(ind.states) != 1 || ind.states[0] != "playing" || ind.levels[0] != 200 {
		t.Fatalf("playing indication %v %v", ind.states, ind.levels)
	}
	if len(base.frames) != 0 {
		t.Fatal("framebuffer station used the sysfs colour path")
	}
	_ = c.speech(ctx, speechEvent{Phase: "recognition", RequestID: "1", Final: true, Transcript: "стоп"}, now)
	_ = c.speech(ctx, speechEvent{Phase: "idle"}, now)
	if err := c.tick(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(ind.states) != 2 || ind.states[1] != "paused" {
		t.Fatalf("paused indication %v", ind.states)
	}
	_ = c.speech(ctx, speechEvent{Phase: "recognition", RequestID: "2", Final: true, Transcript: "продолжи"}, now.Add(2*time.Second))
	_ = c.speech(ctx, speechEvent{Phase: "idle"}, now.Add(2*time.Second))
	if err := c.tick(ctx, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(ind.states) != 3 || ind.states[2] != "playing" {
		t.Fatalf("resumed indication %v", ind.states)
	}
	r.s.Available = false
	r.s.Active = false
	r.s.Client = ""
	if err := c.tick(ctx, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if ind.off == 0 {
		t.Fatal("framebuffer ring not released after the sender left")
	}
}

type mockIndicationStation struct {
	*mockStation
	states []string
	levels []uint8
}

func (s *mockIndicationStation) Indicate(_ context.Context, state string, level uint8) error {
	s.states = append(s.states, state)
	s.levels = append(s.levels, level)
	return nil
}

func TestStation2PreviewHoldsTheRingUntilCleared(t *testing.T) {
	fb, path := openTestStation2LED(t, "32", 4)
	colors := make([]string, station2LEDs)
	for i := range colors {
		colors[i] = "#FF0000"
	}
	if err := fb.Preview(colors); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{0xff, 0x00, 0x00, 0x00}; !bytes.Equal(got[:4], want) {
		t.Fatalf("preview pixel: got % x want % x", got[:4], want)
	}
	if !fb.PreviewActive() {
		t.Fatal("preview lease is not active")
	}
	if err := fb.Colors(context.Background(), colors); !errors.Is(err, errFramebufferBusy) {
		t.Fatalf("spectrum frame during preview: got %v want %v", err, errFramebufferBusy)
	}
	if err := fb.Show(context.Background(), "playing", 128); !errors.Is(err, errFramebufferBusy) {
		t.Fatalf("calm fill during preview: got %v want %v", err, errFramebufferBusy)
	}
	if err := fb.Release(context.Background()); err != nil {
		t.Fatalf("release during preview: %v", err)
	}
	if !fb.PreviewActive() {
		t.Fatal("release dropped the preview lease")
	}
	if err := fb.ClearPreview(); err != nil {
		t.Fatal(err)
	}
	if fb.PreviewActive() {
		t.Fatal("clear left the preview lease active")
	}
	if err := fb.Colors(context.Background(), colors); err != nil {
		t.Fatalf("spectrum frame after clear: %v", err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStation2PreviewWatcherTakesAndReturnsTheRing(t *testing.T) {
	fb, _ := openTestStation2LED(t, "32", 4)
	frame := filepath.Join(t.TempDir(), "frame.json")
	colors := make([]string, station2LEDs)
	for i := range colors {
		colors[i] = "#00FF00"
	}
	payload, err := json.Marshal(map[string]any{"colors": colors})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(frame, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		watchStation2Preview(ctx, fb, frame)
		close(done)
	}()
	waitFor(t, "the preview lease", fb.PreviewActive)
	if err := os.Remove(frame); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the preview release", func() bool { return !fb.PreviewActive() })
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not stop")
	}
}

func TestStation2PreviewWatcherExpiresForgottenFrame(t *testing.T) {
	fb, _ := openTestStation2LED(t, "32", 4)
	frame := filepath.Join(t.TempDir(), "frame.json")
	colors := make([]string, station2LEDs)
	for i := range colors {
		colors[i] = "#0000FF"
	}
	payload, err := json.Marshal(map[string]any{"colors": colors})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(frame, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * station2PreviewTTL)
	if err := os.Chtimes(frame, old, old); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchStation2Preview(ctx, fb, frame)
	waitFor(t, "the stale preview to be dropped", func() bool {
		_, statErr := os.Stat(frame)
		return os.IsNotExist(statErr) && !fb.PreviewActive()
	})
}
