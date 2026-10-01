package theme

import (
	"fmt"
	"math"
)

// WindowColors defines a perceptual color family for simultaneous editor windows.
type WindowColors struct {
	Main      Color
	Anchors   []Color
	HueSpread float64
	ChromaMin float64
	ChromaMax float64
}

// WindowColorsDeclaration leaves omitted decisions to the theme's own palette.
type WindowColorsDeclaration struct {
	Main      string   `yaml:"main,omitempty"`
	Anchors   []string `yaml:"anchors,omitempty"`
	HueSpread *float64 `yaml:"hue_spread,omitempty"`
	ChromaMin *float64 `yaml:"chroma_min,omitempty"`
	ChromaMax *float64 `yaml:"chroma_max,omitempty"`
}

func resolveWindowColors(decl *WindowColorsDeclaration, tokens map[string]Color) (WindowColors, []Fault) {
	out := WindowColors{Main: tokens["accent-signal"], Anchors: []Color{tokens["accent"], tokens["status-positive"], tokens["cost-workers"]},
		HueSpread: 24, ChromaMin: 0.04, ChromaMax: 0.16}
	if decl == nil {
		return out, nil
	}
	var faults []Fault
	if decl.Main != "" {
		color, err := ParseColor(decl.Main)
		if err != nil || color.A != 255 {
			faults = append(faults, Fault{Field: "window_colors.main", Message: "expected an opaque hex color"})
		} else {
			out.Main = color
		}
	}
	if decl.Anchors != nil {
		out.Anchors = nil
		if len(decl.Anchors) < 1 || len(decl.Anchors) > 12 {
			faults = append(faults, Fault{Field: "window_colors.anchors", Message: "supply between 1 and 12 opaque colors"})
		}
		for i, raw := range decl.Anchors {
			color, err := ParseColor(raw)
			if err != nil || color.A != 255 {
				faults = append(faults, Fault{Field: fmt.Sprintf("window_colors.anchors.%d", i), Message: "expected an opaque hex color"})
				continue
			}
			out.Anchors = append(out.Anchors, color)
		}
	}
	for _, field := range []struct {
		name    string
		value   *float64
		target  *float64
		maximum float64
	}{
		{"hue_spread", decl.HueSpread, &out.HueSpread, 180},
		{"chroma_min", decl.ChromaMin, &out.ChromaMin, 0.4},
		{"chroma_max", decl.ChromaMax, &out.ChromaMax, 0.4},
	} {
		if field.value == nil {
			continue
		}
		v := *field.value
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > field.maximum {
			faults = append(faults, Fault{Field: "window_colors." + field.name, Message: fmt.Sprintf("must be finite and between 0 and %g", field.maximum)})
		} else {
			*field.target = v
		}
	}
	if out.ChromaMin > out.ChromaMax {
		faults = append(faults, Fault{Field: "window_colors.chroma_min", Message: "must not exceed chroma_max"})
	}
	return out, faults
}
