package main

import "time"

// The control goroutine publishes values, never its mutable controller, to
// the legacy renderer. The lease makes a failed/stalled observer fail closed.
type rendererControlState struct {
	Session     bool // Receiver reports active audio.
	Attached    bool // Sender may still be reachable with its audio paused.
	Paint       bool
	VolumeKnown bool
	Volume      int
	Expires     time.Time
}

func (s rendererControlState) allowsPaint(now time.Time) bool {
	return s.Session && s.Paint && now.Before(s.Expires)
}

func (s rendererControlState) allowsPause(now time.Time) bool {
	return s.Attached && s.Paint && now.Before(s.Expires)
}

func (c *sharedController) rendererState(now time.Time) rendererControlState {
	return rendererControlState{
		Session:     c.audioActive,
		Attached:    c.active,
		Paint:       c.aliceIdle && !c.ledSuspended && now.Before(c.speechFreshUntil),
		VolumeKnown: c.nativeObserved,
		Volume:      c.observedNative,
		Expires:     now.Add(2 * time.Second),
	}
}
