package main

import (
	"context"
	"testing"
	"time"
)

func TestMidiRendererYieldsToSpeechAndDisconnectedSender(t *testing.T) {
	c, r, _ := testControls()
	c.cfg.LEDs = false // The legacy renderer is the sole display owner.
	ctx, now := context.Background(), time.Now()
	if err := c.tick(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := c.speech(ctx, speechEvent{Phase: "idle"}, now); err != nil {
		t.Fatal(err)
	}
	if !c.rendererState(now).allowsPaint(now) {
		t.Fatal("live session with fresh idle cannot render")
	}
	if err := c.speech(ctx, speechEvent{Phase: "speaking"}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if c.rendererState(now.Add(time.Second)).allowsPaint(now.Add(time.Second)) {
		t.Fatal("renderer overwrites Alice while she speaks")
	}
	if err := c.speech(ctx, speechEvent{Phase: "idle"}, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if !c.rendererState(now.Add(2 * time.Second)).allowsPaint(now.Add(2 * time.Second)) {
		t.Fatal("renderer does not resume after Alice")
	}
	r.s.Active, r.s.Connected, r.s.Available, r.s.Client = false, false, false, ""
	if err := c.tick(ctx, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	ui := c.rendererState(now.Add(3 * time.Second))
	if ui.Session || ui.allowsPaint(now.Add(3*time.Second)) {
		t.Fatal("disconnected sender retains the display")
	}
}

func TestMidiRendererRejectsStaleControlState(t *testing.T) {
	c, _, _ := testControls()
	now := time.Now()
	if err := c.tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	ui := c.rendererState(now)
	if ui.allowsPaint(now.Add(4 * time.Second)) {
		t.Fatal("stalled control loop leaves its renderer writing forever")
	}
}

func TestMidiObservedControllerDoesNotPaintASecondRing(t *testing.T) {
	c, _, station := testControls()
	c.rendererUpdate = func(rendererControlState) {}
	if err := c.tick(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(station.frames) != 0 || station.off != 0 {
		t.Fatal("shared controller competes with legacy renderer")
	}
}

func TestMidiRendererReleasesAttachedButStoppedSender(t *testing.T) {
	c, receiver, _ := testControls()
	receiver.s.Active = false
	receiver.s.Connected = true // Mac can retain its event channel after stopping output.
	now := time.Now()
	if err := c.tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if !c.active {
		t.Fatal("attached sender lost its voice-resume control session")
	}
	if ui := c.rendererState(now); !ui.Attached || ui.Session || ui.allowsPaint(now) {
		t.Fatal("retained event connection kept the renderer over Alice")
	}
}

func TestRendererReceivesConfirmedPhoneVolumeWithoutAnotherPoll(t *testing.T) {
	c, receiver, _ := testControls()
	now := time.Now()
	if err := c.tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	receiver.s.DB = percentDB(30)
	if err := c.tick(context.Background(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	ui := c.rendererState(now.Add(time.Second))
	if !ui.VolumeKnown || ui.Volume != 30 {
		t.Fatalf("confirmed write not published immediately: known=%v volume=%d", ui.VolumeKnown, ui.Volume)
	}
}

func TestLiteSenderCanSetFivePercentVolumeStep(t *testing.T) {
	c, _, _ := testControls()
	c.volumeStep = 5
	if err := c.applySenderVolume(context.Background(), receiverState{DB: percentDB(35)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if ui := c.rendererState(time.Now()); !ui.VolumeKnown || ui.Volume != 35 {
		t.Fatalf("Lite lost native five-percent step: %+v", ui)
	}
}
