package main

// The live control path (--shared-audio --controls-config) previously painted
// one solid colour. This file adds the missing pieces without touching the
// audio path: shairport-sync's ALSA file plugin copies the same PCM into a
// FIFO, and the existing four-band analyser turns it into LED colours. Album
// artwork from the sender supplies the hue when the receiver provides it.
//
// Nothing here invents a signal: the bars follow real PCM, and the hue is only
// taken from an artwork file that shairport-sync actually received.

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	coverArtMaxBytes  = 4 << 20
	coverArtMaxPixels = 4 << 20
	coverArtKeep      = 32
	coverArtMaxAge    = 30 * time.Minute

	// Adopt a new cover quickly. The old two second window kept the previous
	// track's colour on the ring after the artwork had already changed.
	coverAdoptDelay   = 500 * time.Millisecond
	coverScanInterval = 300 * time.Millisecond

	// Owner-tuned on 2026-11-11: +22% saturation and a slightly higher bar
	// floor. Greys and cream still stay neutral.
	coverSaturationBoost = 0.22
	coverNeutralCutoff   = 0.15
	spectrumFloor        = 0.065
)

type spectrumTap struct {
	brightness float64
	// gain is a per-channel white balance applied to real artwork colours,
	// never to the no-artwork neutral fallback.
	gain [3]float64
	// matrix is the device's stock LED calibration loaded by name. swapRB
	// pre-swaps red and blue for hosts whose Luna profile packs BGR.
	matrix *ledColorMatrix
	swapRB bool
	// neutralBlack removes Mini 2's deliberately blue-grey black-cover
	// fallback. Lite 2 shows that fallback as blue, so it stays truly off.
	neutralBlack bool
	mu           sync.Mutex
	state        airplayState
	hue          float64
	haveHue      bool
	cover        string
	dir          string
	rotatedAt    time.Time
	coverScan    time.Time
	// A sender may update the cached artwork several times per track; only a
	// stable newest file is adopted so the ring does not flicker between hues.
	pendingCover string
	pendingSince time.Time
	// mono marks artwork that has no coherent hue (black, white, greyscale).
	// Such a cover is painted with neutral bars instead of the rainbow, which
	// would otherwise show unrelated reds and blues.
	mono           bool
	luma           float64
	coverColor     [3]float64
	haveCoverColor bool
}

func newSpectrumTap(coverDir string) *spectrumTap {
	return &spectrumTap{
		state: airplayState{status: "stopped"},
		dir:   coverDir,
		gain:  [3]float64{1, 1, 1},
	}
}

// run drains the analyser FIFO. shairport-sync blocks if the audio FIFO has
// no reader, so this goroutine must outlive every AirPlay session. Artwork is
// read from the receiver's own cache so the display metadata FIFO keeps a
// single reader.
func (t *spectrumTap) run(ctx context.Context, audioFD int) {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			// A solid-indication model has no analyser FIFO: the tap only
			// follows the cover cache for the artwork colour.
			if audioFD >= 0 {
				if err := readFD(audioFD, func(buf []byte) { t.feedAudio(buf, now) }); err != nil {
					fmt.Fprintf(os.Stderr, "airplay spectrum: audio FIFO: %v\n", err)
				}
			}
			t.rotateAt(now)
		}
	}
}

func (t *spectrumTap) feedAudio(pcm []byte, now time.Time) {
	t.mu.Lock()
	t.state.audio(pcm, now)
	t.mu.Unlock()
}

func (t *spectrumTap) feedMetadata(buf []byte, now time.Time) {
	// The parser callback runs while the stream buffer is being updated; keep
	// the lock only around state, and decode artwork after releasing it.
	var picture []byte
	t.mu.Lock()
	t.state.meta.feed(buf, func(typ, code string, raw []byte) {
		if typ == "73736e63" && code == "50494354" { // ssnc/PICT
			picture = append([]byte(nil), raw...)
			return
		}
		t.state.onMetadata(typ, code, raw, now)
	})
	t.mu.Unlock()
	if len(picture) > 0 {
		t.refreshCover()
	}
}

// artworkHue accepts either the image itself or the cache path shairport-sync
// writes. Recent receivers send the path after writing the file.
func (t *spectrumTap) artworkHue(payload []byte) (float64, bool) {
	if len(payload) >= 4 && (bytes.HasPrefix(payload, []byte{0xff, 0xd8}) || bytes.HasPrefix(payload, []byte{0x89, 'P', 'N', 'G'})) {
		return paletteHue(bytes.NewReader(payload), int64(len(payload)))
	}
	name := strings.TrimSpace(strings.TrimRight(string(payload), "\x00"))
	if name == "" || strings.ContainsAny(name, "\r\n") {
		return 0, false
	}
	if !filepath.IsAbs(name) {
		name = filepath.Join(t.dir, name)
	}
	f, err := os.Open(name)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, false
	}
	hue, ok := paletteHue(f, info.Size())
	if ok {
		t.cover = name
		t.pruneArtwork()
	}
	return hue, ok
}

func (t *spectrumTap) pruneArtwork() {
	entries, err := os.ReadDir(t.dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-coverArtMaxAge)
	remaining := 0
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || info.IsDir() {
			continue
		}
		if info.ModTime().Before(cutoff) || remaining >= coverArtKeep {
			_ = os.Remove(filepath.Join(t.dir, entry.Name()))
			continue
		}
		remaining++
	}
}

func (t *spectrumTap) rotateAt(now time.Time) {
	t.mu.Lock()
	if now.Sub(t.rotatedAt) >= 100*time.Millisecond {
		t.state.rotate()
		t.rotatedAt = now
	}
	scan := now.Sub(t.coverScan) >= coverScanInterval
	if scan {
		t.coverScan = now
	}
	t.mu.Unlock()
	if scan {
		t.refreshCover()
	}
}

// refreshCover follows the receiver's own artwork cache. Some shairport
// builds send a PICT item larger than the metadata buffer keeps, while the
// cached JPEG is already on disk; using the same file keeps the hue honest
// and does not depend on the metadata framing.
func (t *spectrumTap) refreshCover() {
	entries, err := os.ReadDir(t.dir)
	if err != nil {
		return
	}
	newest := ""
	var newestAt time.Time
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newestAt) {
			newestAt, newest = info.ModTime(), entry.Name()
		}
	}
	if newest == "" {
		return
	}
	path := filepath.Join(t.dir, newest)
	t.mu.Lock()
	if t.cover == path {
		t.pendingCover = ""
		t.mu.Unlock()
		return
	}
	if t.pendingCover != path {
		t.pendingCover, t.pendingSince = path, time.Now()
	}
	stable := time.Since(t.pendingSince) >= coverAdoptDelay
	t.mu.Unlock()
	if !stable {
		return
	}
	if time.Since(newestAt) > coverArtMaxAge {
		return
	}
	t.adoptCover(path, newest, newestAt)
}

// adoptCover applies a cached artwork file. A file without a usable hue must
// clear the previous colour: otherwise a black or greyscale cover keeps tinting
// the ring with the previous track.
func (t *spectrumTap) adoptCover(path, newest string, newestAt time.Time) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return
	}
	rgb, decoded := coverRGB(f, info.Size())
	if !decoded {
		return
	}
	hue, sat, value := rgbHSV(rgb[0], rgb[1], rgb[2])
	chromatic := sat >= coverNeutralCutoff
	original := rgb
	if chromatic {
		rgb = boostCoverSaturation(rgb)
	}
	t.mu.Lock()
	t.cover, t.pendingCover = path, ""
	t.coverColor, t.haveCoverColor = rgb, true
	t.hue, t.haveHue, t.mono, t.luma = hue, chromatic, !chromatic, value
	t.mu.Unlock()
	if chromatic {
		fmt.Fprintf(os.Stderr, "airplay spectrum: cover RGB #%02X%02X%02X saturation %.2f -> #%02X%02X%02X (%s)\n",
			int(original[0]*255), int(original[1]*255), int(original[2]*255), sat,
			int(rgb[0]*255), int(rgb[1]*255), int(rgb[2]*255), newest)
	} else {
		fmt.Fprintf(os.Stderr, "airplay spectrum: cover RGB #%02X%02X%02X saturation %.2f (%s)\n",
			int(rgb[0]*255), int(rgb[1]*255), int(rgb[2]*255), sat, newest)
	}
}

// colorize applies the model's stock LED calibration and channel order after
// the palette has been rendered. Both are optional and nil-safe.
func (t *spectrumTap) colorize(in []string) []string {
	t.mu.Lock()
	matrix, swap := t.matrix, t.swapRB
	t.mu.Unlock()
	if matrix != nil {
		in = matrix.colors(in)
	}
	if swap {
		in = swapRedBlue(in)
	}
	return in
}

func (t *spectrumTap) playing(now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state.playing(now)
}

// frame renders n colours from the four real bands. With artwork the hue is
// shared and slightly fanned out; without it the bars pulse neutral from black
// to white instead of showing an unrelated rainbow.
// solidFrame paints one artwork colour on every zone. It is the indication for
// models that show a solid frame instead of the PCM spectrum, so a logo light
// still follows the cover. It returns nil while no artwork is known, which lets
// the caller keep its own fallback frame.
func (t *spectrumTap) solidFrame(n int, level uint8) []string {
	if n <= 0 {
		return nil
	}
	t.mu.Lock()
	rgb, haveColor := t.coverColor, t.haveCoverColor
	gain, neutralBlack := t.gain, t.neutralBlack
	t.mu.Unlock()
	if !haveColor {
		return nil
	}
	scale := float64(level) / 200
	if scale <= 0 {
		scale = 1
	}
	if scale > 1 {
		scale = 1
	}
	// Match the spectrum frame: a black cover stays dark instead of inventing
	// a tint, except on the model whose stock fallback is deliberately blue.
	if rgb == [3]float64{} && !neutralBlack {
		rgb = [3]float64{24.0 / 255, 32.0 / 255, 48.0 / 255}
	}
	r := math.Min(1, math.Max(0, rgb[0]*gain[0]*scale))
	g := math.Min(1, math.Max(0, rgb[1]*gain[1]*scale))
	b := math.Min(1, math.Max(0, rgb[2]*gain[2]*scale))
	color := fmt.Sprintf("#%02X%02X%02X", int(r*255), int(g*255), int(b*255))
	frame := make([]string, n)
	for i := range frame {
		frame[i] = color
	}
	return frame
}

func (t *spectrumTap) frame(n int, level uint8) []string {
	if n <= 0 {
		return nil
	}
	t.mu.Lock()
	history := t.state.history
	hue, haveHue, mono, luma := t.hue, t.haveHue, t.mono, t.luma
	rgb, haveColor := t.coverColor, t.haveCoverColor
	brightness := t.brightness
	gain, neutralBlack := t.gain, t.neutralBlack
	darkCover := haveColor && rgb == [3]float64{}
	t.mu.Unlock()

	scale := float64(level) / 200
	if scale <= 0 {
		scale = 1
	}
	if scale > 1 {
		scale = 1
	}
	frame := make([]string, n)
	for i := 0; i < n; i++ {
		band := 0
		if n > 1 {
			band = i * 3 / (n - 1)
		}
		if band > 3 {
			band = 3
		}
		value := (spectrumFloor + math.Pow(history[band], 1.3)*.9) * scale
		// Boost spectrum only, keeping one common RGB multiplier. Black artwork
		// retains its deliberately dim fallback; pause does not use this path.
		if brightness > 0 && !darkCover {
			value *= brightness
		}
		value = math.Min(1, value)

		switch {
		case haveColor:
			// Mini 2 keeps its deliberately dim blue-grey black fallback.
			// Lite 2 shows that as blue, so it uses true black instead.
			if rgb == [3]float64{} && !neutralBlack {
				rgb = [3]float64{24.0 / 255, 32.0 / 255, 48.0 / 255}
			}
			// Keep the real RGB ratios, apply the per-model white balance, and
			// let PCM control only the intensity.
			r := math.Min(1, math.Max(0, rgb[0]*gain[0]*value))
			g := math.Min(1, math.Max(0, rgb[1]*gain[1]*value))
			b := math.Min(1, math.Max(0, rgb[2]*gain[2]*value))
			frame[i] = fmt.Sprintf("#%02X%02X%02X", int(r*255), int(g*255), int(b*255))
		case haveHue:
			// A small fan keeps neighbouring bands distinguishable while the
			// whole ring still reads as the artwork's colour.
			frame[i] = hsv(hue+float64(band-1)*14, value)
		case mono:
			// Black, white and greyscale artwork has no hue. Paint neutral bars
			// scaled by the artwork's own brightness: a black cover stays dim,
			// a white one stays white, and neither shows unrelated colours.
			g := value * luma * luma
			if luma < .12 {
				g = 0 // Black is absence of light, not a tinted grey floor.
			}
			if g > 1 {
				g = 1
			}
			v := int(g * 255)
			frame[i] = fmt.Sprintf("#%02X%02X%02X", v, v, v)
		default:
			// No artwork at all: a neutral black-to-white pulse. The old
			// rotating rainbow showed unrelated colours before the first
			// cover arrived.
			v := int(value * 255)
			frame[i] = fmt.Sprintf("#%02X%02X%02X", v, v, v)
		}
	}
	return t.colorize(frame)
}

func paletteHue(r io.Reader, size int64) (float64, bool) {
	hue, _, chromatic, decoded := paletteTint(r, size)
	return hue, decoded && chromatic
}

// paletteTint reads an artwork and returns a coherent hue (if any), the mean
// brightness and whether the image decoded at all. A greyscale or nearly
// colourless cover decodes but is not chromatic: its per-pixel hue is noise,
// so callers must not paint it as red or as a rainbow.
func paletteTint(r io.Reader, size int64) (hue, luma float64, chromatic, decoded bool) {
	if size <= 0 || size > coverArtMaxBytes {
		return 0, 0, false, false
	}
	data, err := readAllLimited(r, coverArtMaxBytes)
	if err != nil {
		return 0, 0, false, false
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > coverArtMaxPixels {
		return 0, 0, false, false
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return 0, 0, false, false
	}
	return paletteTintImage(img)
}

func paletteTintImage(img image.Image) (hue, luma float64, chromatic, decoded bool) {
	bounds := img.Bounds()
	stepX, stepY := bounds.Dx()/64, bounds.Dy()/64
	if stepX < 1 {
		stepX = 1
	}
	if stepY < 1 {
		stepY = 1
	}
	var sx, sy, weight, sumV float64
	var samples, dark, neutral int
	for y := bounds.Min.Y; y < bounds.Max.Y; y += stepY {
		for x := bounds.Min.X; x < bounds.Max.X; x += stepX {
			r, g, b, _ := img.At(x, y).RGBA()
			h, s, v := rgbHSV(float64(r)/65535, float64(g)/65535, float64(b)/65535)
			sumV += v
			samples++
			if v < .12 {
				dark++
			}
			if s < .10 {
				neutral++
			}
			if s < .15 || v < .12 {
				continue
			}
			w := s * v
			angle := h * math.Pi / 180
			sx += math.Cos(angle) * w
			sy += math.Sin(angle) * w
			weight += w
		}
	}
	if samples == 0 {
		return 0, 0, false, true
	}
	luma = sumV / float64(samples)
	// Count background area before discarding achromatic pixels. A tiny red
	// logo on white or a bright title on black must not tint the whole ring.
	if float64(dark)/float64(samples) >= .60 {
		return 0, 0, false, true
	}
	if float64(neutral)/float64(samples) >= .65 {
		return 0, luma, false, true
	}
	if weight == 0 {
		return 0, luma, false, true
	}
	// A coherent hue has a long resultant vector. Greyscale JPEGs can still
	// pass the per-pixel filter with chroma noise, but their angles cancel out.
	strength := math.Hypot(sx, sy) / weight
	if strength < .20 {
		return 0, luma, false, true
	}
	hue = math.Atan2(sy, sx) * 180 / math.Pi
	if hue < 0 {
		hue += 360
	}
	return hue, luma, true, true
}

// readAllLimited reads at most limit bytes so a malformed or hostile artwork
// cannot allocate an unbounded buffer.
func readAllLimited(r io.Reader, limit int64) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, io.LimitReader(r, limit+1)); err != nil {
		return nil, err
	}
	if int64(buf.Len()) > limit {
		return nil, fmt.Errorf("artwork exceeds %d bytes", limit)
	}
	return buf.Bytes(), nil
}

// boostCoverSaturation raises saturation while keeping the largest channel
// (HSV value) fixed. Cream and grey never reach this function.
func boostCoverSaturation(rgb [3]float64) [3]float64 {
	v := math.Max(rgb[0], math.Max(rgb[1], rgb[2]))
	m := math.Min(rgb[0], math.Min(rgb[1], rgb[2]))
	if v <= 0 || (v-m)/v < coverNeutralCutoff {
		return rgb
	}
	k := 1 + coverSaturationBoost
	for i := range rgb {
		rgb[i] = math.Max(0, v-(v-rgb[i])*k)
	}
	return rgb
}

func rgbHSV(r, g, b float64) (h, s, v float64) {
	max := math.Max(r, math.Max(g, b))
	min := math.Min(r, math.Min(g, b))
	v = max
	if max <= 0 {
		return 0, 0, 0
	}
	s = (max - min) / max
	d := max - min
	if d <= 0 {
		return 0, s, v
	}
	switch max {
	case r:
		h = math.Mod((g-b)/d, 6)
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	h *= 60
	if h < 0 {
		h += 360
	}
	return h, s, v
}
