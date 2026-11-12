package main

// Station 2 has no /sys/class/leds ring: its 89-LED ring is an 89x1 XRGB
// framebuffer. maind --platformd owns that framebuffer through its
// led-animator thread and keeps repainting the stock idle pattern, so plain
// framebuffer writes lose the race. This backend pauses only that thread with
// ptrace while AirPlay owns the ring, paints whole frames, and detaches to
// hand the ring back. If the supervisor dies the kernel detaches the tracee,
// so the station's own indication resumes without a reboot.
//
// Station 2 is special: every sysfs model keeps using YAM's /led API.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	station2FBPath    = "/dev/fb0"
	station2FBSize    = "/sys/class/graphics/fb0/virtual_size"
	station2FBDepth   = "/sys/class/graphics/fb0/bits_per_pixel"
	station2LEDs      = 89
	station2CalmShare = 30 // percent of led_level for the steady fill
)

type framebufferLED struct {
	mu            sync.Mutex
	file          *os.File
	bytesPerPixel int
	lastFrame     []byte
	pid, tid      int
	// lease pins every ptrace call to one OS thread. Linux makes the attaching
	// thread the tracer, so attaching in the spectrum goroutine and detaching
	// in the control loop would silently leave led-animator stopped.
	lease   *traceLease
	preview bool
	// attach is a test hook. Production always uses attachAnimator.
	attach func() error
}

type traceLease struct {
	requests chan func() error
	results  chan error
	done     chan struct{}
}

func newTraceLease() *traceLease {
	l := &traceLease{
		requests: make(chan func() error),
		results:  make(chan error),
		done:     make(chan struct{}),
	}
	go l.loop()
	return l
}

func (l *traceLease) loop() {
	defer close(l.done)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	for fn := range l.requests {
		l.results <- fn()
	}
}

func (l *traceLease) run(fn func() error) error {
	select {
	case l.requests <- fn:
		return <-l.results
	case <-l.done:
		return errors.New("ptrace lease is closed")
	}
}

func (l *traceLease) close() {
	close(l.requests)
	<-l.done
}

var errFramebufferBusy = errors.New("station ring is owned by the firmware")

func openStation2LED() (*framebufferLED, error) {
	return openFramebufferLED(station2FBPath, station2FBSize, station2FBDepth)
}

// openFramebufferLED verifies the exact Station 2 geometry before exposing the
// device. Tests substitute temporary paths and disable the ptrace lease.
func openFramebufferLED(path, sizePath, depthPath string) (*framebufferLED, error) {
	size, err := os.ReadFile(sizePath)
	if err != nil {
		return nil, err
	}
	if geometry := strings.TrimSpace(string(size)); geometry != "89,1" {
		return nil, fmt.Errorf("unexpected Station 2 framebuffer geometry %q", geometry)
	}
	depth, err := os.ReadFile(depthPath)
	if err != nil {
		return nil, err
	}
	bytesPerPixel := 0
	switch strings.TrimSpace(string(depth)) {
	case "24":
		bytesPerPixel = 3
	case "32":
		bytesPerPixel = 4
	default:
		return nil, fmt.Errorf("unsupported Station 2 framebuffer depth %q", strings.TrimSpace(string(depth)))
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	led := &framebufferLED{file: f, bytesPerPixel: bytesPerPixel}
	led.attach = led.attachAnimator
	return led, nil
}

// ensureLocked takes the ring before the first write. It is a no-op when the
// test hook is disabled. A missing or foreign led-animator is reported as busy
// instead of racing the vendor indication.
func (f *framebufferLED) ensureLocked() error {
	if f.attach == nil {
		return nil
	}
	if f.tid != 0 && threadMatches(f.pid, f.tid) {
		return nil
	}
	if f.tid != 0 {
		_ = f.detachAnimator()
	}
	if err := f.attach(); err != nil {
		return errFramebufferBusy
	}
	return nil
}

// attachAnimator freezes only the platformd LED thread. SIGSTOP would stop the
// whole process, including buttons and cloud handling; ptrace is per thread.
func (f *framebufferLED) attachAnimator() error {
	_ = f.detachAnimator()
	if f.lease == nil {
		f.lease = newTraceLease()
	}
	return f.lease.run(func() error {
		pid, tid, err := findLEDAnimator()
		if err != nil {
			return err
		}
		if err = syscall.PtraceAttach(tid); err != nil {
			return err
		}
		if err = waitTraceStop(tid); err != nil {
			_ = syscall.PtraceDetach(tid)
			return err
		}
		f.pid, f.tid = pid, tid
		f.lastFrame = nil
		return nil
	})
}

// detachAnimator resumes the station's own indication. It never paints black:
// the firmware repaints its idle frame as soon as it is released.
func (f *framebufferLED) detachAnimator() error {
	if f.tid == 0 {
		return nil
	}
	if f.lease == nil {
		f.pid, f.tid = 0, 0
		f.lastFrame = nil
		return nil
	}
	tid := f.tid
	return f.lease.run(func() error {
		err := syscall.PtraceDetach(tid)
		if err != nil && err != syscall.ESRCH {
			return err
		}
		f.pid, f.tid = 0, 0
		f.lastFrame = nil
		return nil
	})
}

func waitTraceStop(tid int) error {
	for {
		var status syscall.WaitStatus
		_, err := syscall.Wait4(tid, &status, 0, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if status.Stopped() {
			return nil
		}
		if status.Exited() || status.Signaled() {
			return errors.New("led-animator exited while attaching")
		}
	}
}

// findLEDAnimator is intentionally strict: only a maind --platformd process
// with a thread named led-animator qualifies. A wrong PID would freeze an
// unrelated service.
func findLEDAnimator() (int, int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, 0, err
	}
	self := os.Getpid()
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == self {
			continue
		}
		comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
		if err != nil || strings.TrimSpace(string(comm)) != "maind" {
			continue
		}
		cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil || !bytes.Contains(cmdline, []byte("--platformd")) {
			continue
		}
		tasks, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
		if err != nil {
			continue
		}
		for _, task := range tasks {
			tid, err := strconv.Atoi(task.Name())
			if err != nil {
				continue
			}
			name, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/comm", pid, tid))
			if err == nil && strings.TrimSpace(string(name)) == "led-animator" {
				return pid, tid, nil
			}
		}
	}
	return 0, 0, errors.New("led-animator not found")
}

func threadMatches(pid, tid int) bool {
	if pid <= 0 || tid <= 0 {
		return false
	}
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || !bytes.Contains(cmdline, []byte("--platformd")) {
		return false
	}
	name, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/comm", pid, tid))
	return err == nil && strings.TrimSpace(string(name)) == "led-animator"
}

// station2CalmFrame is the steady AirPlay fill on the big ring: the same
// green-cyan family the sysfs stations use, damped so it reads as presence
// rather than a status lamp. Playing frames come from the spectrum tap and
// carry the artwork colour.
func station2CalmFrame(level uint8) []byte {
	if level == 0 {
		level = 200
	}
	value := uint8(uint16(level) * station2CalmShare / 100)
	if value < 8 {
		value = 8
	}
	frame := make([]byte, station2LEDs*3)
	for i := 0; i < station2LEDs; i++ {
		frame[i*3], frame[i*3+1], frame[i*3+2] = 0, value, value/2
	}
	return frame
}

func station2FrameFromColors(colors []string) ([]byte, error) {
	if len(colors) != station2LEDs {
		return nil, fmt.Errorf("Station 2 needs %d LED colours, got %d", station2LEDs, len(colors))
	}
	frame := make([]byte, 0, station2LEDs*3)
	for _, color := range colors {
		r, g, b, err := parseStation2Color(color)
		if err != nil {
			return nil, err
		}
		frame = append(frame, r, g, b)
	}
	return frame, nil
}

func parseStation2Color(value string) (byte, byte, byte, error) {
	if len(value) != 7 || value[0] != '#' {
		return 0, 0, 0, fmt.Errorf("invalid LED colour %q", value)
	}
	n, err := strconv.ParseUint(value[1:], 16, 24)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid LED colour %q", value)
	}
	return byte(n >> 16), byte(n >> 8), byte(n), nil
}

func (f *framebufferLED) encodeRGB(frame []byte) ([]byte, error) {
	if len(frame) != station2LEDs*3 {
		return nil, fmt.Errorf("unexpected Station 2 LED frame length %d", len(frame))
	}
	pixels := make([]byte, 0, station2LEDs*f.bytesPerPixel)
	for i := 0; i < len(frame); i += 3 {
		r, g, b := frame[i], frame[i+1], frame[i+2]
		if f.bytesPerPixel == 4 {
			// Retail Station 2 FBIOGET_VSCREENINFO: R=16, G=8, B=0, A=24.
			// Little-endian packed pixels therefore contain B, G, R, X.
			pixels = append(pixels, b, g, r, 0xff)
		} else {
			pixels = append(pixels, r, g, b)
		}
	}
	return pixels, nil
}

func (f *framebufferLED) writeLocked(frame []byte) error {
	if f.file == nil {
		return os.ErrClosed
	}
	pixels, err := f.encodeRGB(frame)
	if err != nil {
		return err
	}
	if bytes.Equal(pixels, f.lastFrame) {
		return nil
	}
	n, err := f.file.WriteAt(pixels, 0)
	if err != nil {
		return err
	}
	if n != len(pixels) {
		return io.ErrShortWrite
	}
	f.lastFrame = pixels
	return nil
}

// Show paints the steady fill while a sender is attached but no track is
// playing. The state is kept for the controls bookkeeping; both playing and
// paused use the calm fill until the spectrum tap has audio.
func (f *framebufferLED) Show(_ context.Context, state string, level uint8) error {
	switch state {
	case "playing", "paused":
	default:
		return errors.New("unsupported framebuffer indication state")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.preview {
		return errFramebufferBusy
	}
	if err := f.ensureLocked(); err != nil {
		return err
	}
	return f.writeLocked(station2CalmFrame(level))
}

// Colors paints one colour per LED. The spectrum tap owns this path at 10 Hz
// while a track is playing.
func (f *framebufferLED) Colors(_ context.Context, colors []string) error {
	frame, err := station2FrameFromColors(colors)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.preview {
		return errFramebufferBusy
	}
	if err := f.ensureLocked(); err != nil {
		return err
	}
	return f.writeLocked(frame)
}

// Preview paints a static frame for the calibration panel. The preview owns
// the ring until ClearPreview, so the panel can inspect one colour without the
// spectrum animator replacing it at 10 Hz.
func (f *framebufferLED) Preview(colors []string) error {
	frame, err := station2FrameFromColors(colors)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preview = true
	if err := f.ensureLocked(); err != nil {
		f.preview = false
		return err
	}
	return f.writeLocked(frame)
}

// PreviewActive reports whether the calibration panel currently owns the ring.
func (f *framebufferLED) PreviewActive() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.preview
}

func (f *framebufferLED) ClearPreview() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preview = false
	return f.detachAnimator()
}

const (
	station2PreviewPath = "/run/station-airplay/test-frame.json"
	// A browser tab that never sends "off" must not freeze the ring forever.
	station2PreviewTTL = 5 * time.Minute
)

// watchStation2Preview lets the private colour panel put one 89-LED frame on
// the ring and then remove the file to hand control back. It deliberately
// runs on every Station 2 host; without the file it costs one stat per 200 ms.
func watchStation2Preview(ctx context.Context, fb *framebufferLED, path string) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	last := ""
	for {
		select {
		case <-ctx.Done():
			_ = fb.ClearPreview()
			return
		case <-ticker.C:
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) && last != "" {
				_ = fb.ClearPreview()
				last = ""
			}
			continue
		}
		text := string(data)
		if text == last {
			if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) > station2PreviewTTL {
				_ = fb.ClearPreview()
				_ = os.Remove(path)
				last = ""
			}
			continue
		}
		var payload struct {
			Colors []string `json:"colors"`
		}
		if json.Unmarshal(data, &payload) != nil || len(payload.Colors) != station2LEDs {
			continue
		}
		if err := fb.Preview(payload.Colors); err == nil {
			last = text
		}
	}
}

// Release detaches the LED thread; the station immediately repaints its own
// indication. It is deliberately not a black frame.
func (f *framebufferLED) Release(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.preview {
		return nil
	}
	return f.detachAnimator()
}

func (f *framebufferLED) Off(ctx context.Context) error { return f.Release(ctx) }

func (f *framebufferLED) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preview = false
	err := f.detachAnimator()
	if f.lease != nil {
		f.lease.close()
		f.lease = nil
	}
	if f.file == nil {
		return err
	}
	closeErr := f.file.Close()
	f.file = nil
	if err == nil {
		err = closeErr
	}
	return err
}

// framebufferStation keeps YAM volume/speech and replaces only the ring.
type framebufferStation struct {
	yamControlStation
	fb *framebufferLED
}

func (s framebufferStation) Indicate(ctx context.Context, state string, level uint8) error {
	return s.fb.Show(ctx, state, level)
}

func (s framebufferStation) Colors(ctx context.Context, colors []string) error {
	return s.fb.Colors(ctx, colors)
}

func (s framebufferStation) Off(ctx context.Context) error { return s.fb.Release(ctx) }

func (s framebufferStation) Release(ctx context.Context) error { return s.fb.Release(ctx) }
