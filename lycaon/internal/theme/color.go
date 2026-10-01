// Package theme compiles declarative appearance tokens and glyphs.
package theme

import (
	"fmt"
	"math"
	"strings"
)

// Color is a resolved sRGB color with straight alpha.
type Color struct {
	R, G, B uint8
	// A is 255 for an opaque color.
	A uint8
}

// ParseColor reads the one literal form the vocabulary admits.
func ParseColor(s string) (Color, error) {
	raw := strings.TrimSpace(s)
	if !strings.HasPrefix(raw, "#") {
		return Color{}, fmt.Errorf("color %q: must be #rrggbb or #rrggbbaa", s)
	}
	digits := raw[1:]
	if len(digits) != 6 && len(digits) != 8 {
		return Color{}, fmt.Errorf("color %q: must be #rrggbb or #rrggbbaa", s)
	}
	var out [4]uint8
	out[3] = 0xff
	for i := 0; i < len(digits); i += 2 {
		hi, err := hexDigit(digits[i])
		if err != nil {
			return Color{}, fmt.Errorf("color %q: %w", s, err)
		}
		lo, err := hexDigit(digits[i+1])
		if err != nil {
			return Color{}, fmt.Errorf("color %q: %w", s, err)
		}
		out[i/2] = hi<<4 | lo
	}
	return Color{R: out[0], G: out[1], B: out[2], A: out[3]}, nil
}

func hexDigit(c byte) (uint8, error) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', nil
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, nil
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, nil
	}
	return 0, fmt.Errorf("%q is not a hex digit", string(c))
}

// String renders the canonical hex literal.
func (c Color) String() string {
	if c.A == 0xff {
		return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
	}
	return fmt.Sprintf("#%02x%02x%02x%02x", c.R, c.G, c.B, c.A)
}

// mix matches premultiplied `color-mix(in srgb, ...)` behavior.
func mix(a Color, pct float64, b Color) Color {
	w := clamp01(pct / 100)
	aw := w * float64(a.A) / 255
	bw := (1 - w) * float64(b.A) / 255
	alpha := aw + bw
	if alpha == 0 {
		return Color{}
	}
	return Color{
		R: premultipliedChannel(a.R, aw, b.R, bw, alpha),
		G: premultipliedChannel(a.G, aw, b.G, bw, alpha),
		B: premultipliedChannel(a.B, aw, b.B, bw, alpha),
		A: uint8(math.Round(alpha * 255)),
	}
}

func premultipliedChannel(a uint8, aw float64, b uint8, bw, alpha float64) uint8 {
	return uint8(math.Round((float64(a)*aw + float64(b)*bw) / alpha))
}

func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	}
	return v
}

// flatten composites over an opaque backdrop.
func flatten(fg, backdrop Color) Color {
	base := Color{R: backdrop.R, G: backdrop.G, B: backdrop.B, A: 0xff}
	return compositeOver(fg, base)
}

func compositeOver(fg, bg Color) Color {
	fa := float64(fg.A) / 255
	ba := float64(bg.A) / 255
	alpha := fa + ba*(1-fa)
	if alpha == 0 {
		return Color{}
	}
	fw := fa
	bw := ba * (1 - fa)
	return Color{
		R: premultipliedChannel(fg.R, fw, bg.R, bw, alpha),
		G: premultipliedChannel(fg.G, fw, bg.G, bw, alpha),
		B: premultipliedChannel(fg.B, fw, bg.B, bw, alpha),
		A: uint8(math.Round(alpha * 255)),
	}
}

// relativeLuminance is WCAG 2.1 relative luminance over linearized sRGB.
func relativeLuminance(c Color) float64 {
	lin := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}

// contrastRatio measures the fully composited foreground and background.
func contrastRatio(fg, bg, backdrop Color) float64 {
	renderedBG := flatten(bg, backdrop)
	renderedFG := compositeOver(fg, renderedBG)
	l1 := relativeLuminance(renderedFG)
	l2 := relativeLuminance(renderedBG)
	if l1 < l2 {
		l1, l2 = l2, l1
	}
	return (l1 + 0.05) / (l2 + 0.05)
}

// legibleInkSteps resolves beyond 8-bit channel precision.
const legibleInkSteps = 24

// legibleInk finds the smallest pole mix that clears min.
func legibleInk(hue, bg, backdrop Color, min float64) Color {
	if contrastRatio(hue, bg, backdrop) >= min {
		return hue
	}
	// Choose the pole farther from the rendered backdrop.
	pole := whiteInk
	if relativeLuminance(flatten(bg, backdrop)) > 0.18 {
		pole = blackInk
	}
	// Return the strongest candidate when no pole clears the floor.
	if contrastRatio(pole, bg, backdrop) < min {
		return pole
	}
	lo, hi := 0.0, 1.0
	for i := 0; i < legibleInkSteps; i++ {
		mid := (lo + hi) / 2
		if contrastRatio(mix(pole, mid*100, hue), bg, backdrop) >= min {
			hi = mid
			continue
		}
		lo = mid
	}
	return mix(pole, hi*100, hue)
}
