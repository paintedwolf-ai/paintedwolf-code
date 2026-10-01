package browser

import (
	"math"
	"strconv"
	"strings"
)

// DeriveMeasureRelations computes gap, alignment, overlap, edge distance, WCAG contrast,
// and whether the pointer is blocked from each element.
func DeriveMeasureRelations(elements []MeasuredElement, viewportW, viewportH int) []MeasureRelation {
	var out []MeasureRelation
	for _, el := range elements {
		out = append(out, edgeDistances(el, viewportW, viewportH)...)
		if c, ok := contrastRelation(el); ok {
			out = append(out, c)
		}
		if b, ok := pointerBlocked(el); ok {
			out = append(out, b)
		}
	}
	for i := 0; i < len(elements); i++ {
		for j := i + 1; j < len(elements); j++ {
			a, b := elements[i], elements[j]
			out = append(out, pairRelations(a, b)...)
		}
	}
	return out
}

func edgeDistances(el MeasuredElement, vw, vh int) []MeasureRelation {
	r := el.Rect
	return []MeasureRelation{{
		Kind: "distance_to_viewport_edge",
		Of:   el.Selector,
		Detail: map[string]any{
			"top":    roundPx(r.Top),
			"right":  roundPx(float64(vw) - r.Right),
			"bottom": roundPx(float64(vh) - r.Bottom),
			"left":   roundPx(r.Left),
		},
	}}
}

func pairRelations(a, b MeasuredElement) []MeasureRelation {
	var out []MeasureRelation
	out = append(out, MeasureRelation{
		Kind:    "gap",
		Between: []string{a.Selector, b.Selector},
		Value:   gapBetween(a.Rect, b.Rect),
		Detail:  map[string]any{"axis": gapAxis(a.Rect, b.Rect)},
	})
	out = append(out, alignmentRelations(a, b)...)
	if ov := overlapDetail(a.Rect, b.Rect); ov != nil {
		out = append(out, MeasureRelation{
			Kind:    "overlap",
			Between: []string{a.Selector, b.Selector},
			Detail:  ov,
		})
	}
	return out
}

func gapBetween(a, b ElementRect) float64 {
	// Exterior gap: 0 if overlapping or touching on an axis; positive when separated.
	dx := math.Max(0, math.Max(a.Left, b.Left)-math.Min(a.Right, b.Right))
	dy := math.Max(0, math.Max(a.Top, b.Top)-math.Min(a.Bottom, b.Bottom))
	if dx > 0 && dy > 0 {
		// Diagonal separation — report Euclidean gap between closest corners (AABB).
		return roundPx(math.Hypot(dx, dy))
	}
	if dx > 0 {
		return roundPx(dx)
	}
	return roundPx(dy)
}

func gapAxis(a, b ElementRect) string {
	dx := math.Max(0, math.Max(a.Left, b.Left)-math.Min(a.Right, b.Right))
	dy := math.Max(0, math.Max(a.Top, b.Top)-math.Min(a.Bottom, b.Bottom))
	switch {
	case dx > 0 && dy == 0:
		return "horizontal"
	case dy > 0 && dx == 0:
		return "vertical"
	case dx > 0 && dy > 0:
		return "diagonal"
	default:
		return "none"
	}
}

func alignmentRelations(a, b MeasuredElement) []MeasureRelation {
	const tol = 0.5
	var out []MeasureRelation
	ar, br := a.Rect, b.Rect
	check := func(edge string, equal bool) {
		if !equal {
			return
		}
		out = append(out, MeasureRelation{
			Kind:    "alignment",
			Between: []string{a.Selector, b.Selector},
			Detail:  map[string]any{"shared": edge},
		})
	}
	check("left", math.Abs(ar.Left-br.Left) <= tol)
	check("right", math.Abs(ar.Right-br.Right) <= tol)
	check("top", math.Abs(ar.Top-br.Top) <= tol)
	check("bottom", math.Abs(ar.Bottom-br.Bottom) <= tol)
	check("center_x", math.Abs((ar.Left+ar.Right)/2-(br.Left+br.Right)/2) <= tol)
	check("center_y", math.Abs((ar.Top+ar.Bottom)/2-(br.Top+br.Bottom)/2) <= tol)
	return out
}

func overlapDetail(a, b ElementRect) map[string]any {
	left := math.Max(a.Left, b.Left)
	right := math.Min(a.Right, b.Right)
	top := math.Max(a.Top, b.Top)
	bottom := math.Min(a.Bottom, b.Bottom)
	w := right - left
	h := bottom - top
	if w <= 0 || h <= 0 {
		return nil
	}
	return map[string]any{
		"width":  roundPx(w),
		"height": roundPx(h),
		"area":   roundPx(w * h),
	}
}

func contrastRelation(el MeasuredElement) (MeasureRelation, bool) {
	fg := strings.TrimSpace(el.Styles["color"])
	bg := strings.TrimSpace(el.Styles["background-color"])
	if fg == "" || bg == "" {
		return MeasureRelation{}, false
	}
	ratio, ok := wcagContrastRatio(fg, bg)
	if !ok {
		return MeasureRelation{}, false
	}
	return MeasureRelation{
		Kind:  "contrast",
		Of:    el.Selector,
		Value: roundPx(ratio*100) / 100,
		Detail: map[string]any{
			"foreground":     fg,
			"background":     bg,
			"wcag_aa_normal": ratio >= 4.5,
			"wcag_aa_large":  ratio >= 3.0,
		},
	}, true
}

func wcagContrastRatio(fg, bg string) (float64, bool) {
	fr, fgok := parseCSSColor(fg)
	br, bgok := parseCSSColor(bg)
	if !fgok || !bgok {
		return 0, false
	}
	l1 := relativeLuminance(fr)
	l2 := relativeLuminance(br)
	lighter := math.Max(l1, l2)
	darker := math.Min(l1, l2)
	return (lighter + 0.05) / (darker + 0.05), true
}

type rgb struct{ R, G, B float64 }

// parseCSSColor parses an opaque computed color. A translucent color reports
// false: its contrast depends on the backdrop, which the measure does not see.
func parseCSSColor(s string) (rgb, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	if strings.HasPrefix(s, "#") {
		return parseHexColor(strings.TrimPrefix(s, "#"))
	}
	if strings.HasPrefix(s, "rgb(") || strings.HasPrefix(s, "rgba(") {
		open, closing := strings.Index(s, "("), strings.LastIndex(s, ")")
		if closing < open {
			return rgb{}, false
		}
		return parseRGBFunction(s[open+1 : closing])
	}
	return rgb{}, false
}

func parseHexColor(hex string) (rgb, bool) {
	var channels []string
	switch len(hex) {
	case 3, 4:
		for i := range hex {
			channels = append(channels, string([]byte{hex[i], hex[i]}))
		}
	case 6, 8:
		for i := 0; i < len(hex); i += 2 {
			channels = append(channels, hex[i:i+2])
		}
	default:
		return rgb{}, false
	}
	values := make([]float64, len(channels))
	for i, channel := range channels {
		v, err := strconv.ParseUint(channel, 16, 8)
		if err != nil {
			return rgb{}, false
		}
		values[i] = float64(v) / 255
	}
	if len(values) == 4 && values[3] < 1 {
		return rgb{}, false
	}
	return rgb{values[0], values[1], values[2]}, true
}

// parseRGBFunction reads "r, g, b[, a]" or "r g b[ / a]" with 0–255 channels.
func parseRGBFunction(inner string) (rgb, bool) {
	fields := strings.FieldsFunc(inner, func(r rune) bool { return r == ',' || r == '/' || r == ' ' })
	if len(fields) != 3 && len(fields) != 4 {
		return rgb{}, false
	}
	var channels [3]float64
	for i := range channels {
		v, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			return rgb{}, false
		}
		channels[i] = v / 255
	}
	if len(fields) == 4 {
		alpha, err := parseAlpha(fields[3])
		if err != nil || alpha < 1 {
			return rgb{}, false
		}
	}
	return rgb{channels[0], channels[1], channels[2]}, true
}

func parseAlpha(field string) (float64, error) {
	if pct, ok := strings.CutSuffix(field, "%"); ok {
		v, err := strconv.ParseFloat(pct, 64)
		return v / 100, err
	}
	return strconv.ParseFloat(field, 64)
}

func relativeLuminance(c rgb) float64 {
	lin := func(v float64) float64 {
		if v <= 0.03928 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}

// pointerBlocked reports a rendered, in-view element the pointer cannot reach at any
// sampled point, and the element that covers or clips it.
func pointerBlocked(el MeasuredElement) (MeasureRelation, bool) {
	var by *ElementBrief
	switch el.Reach.State {
	case ReachCovered:
		by = el.Reach.CoveredBy
	case ReachClipped:
		by = el.Reach.ClippedBy
	default:
		return MeasureRelation{}, false
	}
	detail := map[string]any{"state": el.Reach.State}
	if by != nil {
		detail["by"] = by
	}
	return MeasureRelation{Kind: "pointer_blocked", Of: el.Selector, Detail: detail}, true
}
