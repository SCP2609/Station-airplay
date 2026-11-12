package main

import "context"

// mini2LEDStation corrects the colour order of the Mini 2 ring.
//
// The ring LEDs are wired R,G,B and the kernel nodes are named accordingly,
// but Luna's yandexmini_2 profile packs frames as B,G,R. Without this swap an
// orange cover arrived as blue. The Station 2 framebuffer path and the Plum
// profile already use logical RGB and are not wrapped.
type mini2LEDStation struct{ yamControlStation }

func (s mini2LEDStation) Colors(ctx context.Context, v []string) error {
	return s.yamControlStation.Colors(ctx, swapRedBlue(v))
}

// swapRedBlue reverses the R and B bytes of every "#RRGGBB" entry and leaves
// other strings untouched.
func swapRedBlue(colors []string) []string {
	out := make([]string, len(colors))
	for i, c := range colors {
		if len(c) == 7 && c[0] == '#' {
			out[i] = "#" + c[5:7] + c[3:5] + c[1:3]
			continue
		}
		out[i] = c
	}
	return out
}
