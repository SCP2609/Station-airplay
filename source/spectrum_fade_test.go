package main

import (
	"testing"
	"time"
)

func TestArtworkFadeEasesCoverChange(t *testing.T) {
	tap := newSpectrumTap(t.TempDir())
	tap.haveCoverColor = true
	tap.coverColor = [3]float64{1, 0, 0}
	tap.palette = [][3]float64{{1, 0, 0}}
	tap.coverGen++
	base := time.Unix(1000, 0)
	rgb, pal := tap.easedArtwork(base)
	if rgb != [3]float64{1, 0, 0} || len(pal) != 1 {
		t.Fatalf("first cover should appear at once: %v %v", rgb, pal)
	}

	tap.coverColor = [3]float64{0, 0, 1}
	tap.palette = [][3]float64{{0, 0, 1}}
	tap.coverGen++
	if rgb, _ := tap.easedArtwork(base); rgb != [3]float64{1, 0, 0} {
		t.Fatalf("new cover applied in the same frame: %v", rgb)
	}
	rgb, pal = tap.easedArtwork(base.Add(300 * time.Millisecond))
	if !(rgb[0] > 0.1 && rgb[2] > 0.1) {
		t.Fatalf("no intermediate colour: %v", rgb)
	}
	if len(pal) != 1 || !(pal[0][0] > 0.1 && pal[0][2] > 0.1) {
		t.Fatalf("palette did not fade: %v", pal)
	}
	if rgb, _ := tap.easedArtwork(base.Add(artworkFade)); rgb != [3]float64{0, 0, 1} {
		t.Fatalf("fade did not settle: %v", rgb)
	}
}

func TestArtworkFadePaletteResizeStartsFromShownColour(t *testing.T) {
	tap := newSpectrumTap(t.TempDir())
	tap.haveCoverColor = true
	base := time.Unix(2000, 0)
	tap.coverColor = [3]float64{1, 0, 0}
	tap.palette = [][3]float64{{1, 0, 0}}
	tap.coverGen++
	tap.easedArtwork(base)

	tap.coverColor = [3]float64{0, 1, 0}
	tap.palette = [][3]float64{{0, 1, 0}, {0, 0, 1}}
	tap.coverGen++
	tap.easedArtwork(base)
	_, pal := tap.easedArtwork(base.Add(60 * time.Millisecond))
	if len(pal) != 2 {
		t.Fatalf("palette length %d", len(pal))
	}
	// Both clusters start from the red that was on the ring, then move to
	// their own targets: the first to green, the second to blue.
	if !(pal[0][0] > 0.5 && pal[0][1] > 0 && pal[1][0] > 0.5 && pal[1][2] > 0) {
		t.Fatalf("resized palette did not start from the shown colour: %v", pal)
	}
}
