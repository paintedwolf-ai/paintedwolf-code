package theme

import (
	"fmt"
	"math"
	"sort"
)

// IconStroke controls every stroked icon slot.
type IconStroke struct {
	// Weight scales each slot's base stroke width.
	Weight float64
	Cap    string
	Join   string
}

// These bounds preserve legibility at the smallest rendered size.
const (
	minIconWeight = 0.5
	maxIconWeight = 2.0
)

var iconCaps = map[string]bool{"butt": true, "round": true, "square": true}
var iconJoins = map[string]bool{"miter": true, "round": true, "bevel": true}

// DefaultIconStroke returns the stock treatment.
func DefaultIconStroke() IconStroke {
	return IconStroke{Weight: 1, Cap: "round", Join: "round"}
}

// IconCaps returns the closed enum, sorted, for the generated schema.
func IconCaps() []string { return sortedKeysOf(iconCaps) }

// IconJoins returns the closed enum, sorted, for the generated schema.
func IconJoins() []string { return sortedKeysOf(iconJoins) }

func sortedKeysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// StrokeDeclaration is the authored form; empty fields keep the default.
type StrokeDeclaration struct {
	Weight *float64
	Cap    string
	Join   string
}

// resolveIconStroke validates a declaration into a total treatment.
func resolveIconStroke(decl *StrokeDeclaration) (IconStroke, []Fault) {
	out := DefaultIconStroke()
	if decl == nil {
		return out, nil
	}
	var faults []Fault
	if decl.Weight != nil {
		w := *decl.Weight
		if math.IsNaN(w) || math.IsInf(w, 0) {
			faults = append(faults, Fault{
				Field: "icons.stroke.weight",
				Message: fmt.Sprintf(
					"weight must be a finite number between %.1f and %.1f",
					minIconWeight, maxIconWeight),
			})
		} else if w < minIconWeight || w > maxIconWeight {
			faults = append(faults, Fault{
				Field: "icons.stroke.weight",
				Message: fmt.Sprintf(
					"%.2f is outside %.1f–%.1f — below the floor a glyph stops "+
						"rendering, above it the shapes fill in",
					w, minIconWeight, maxIconWeight),
			})
		} else {
			out.Weight = w
		}
	}
	if decl.Cap != "" {
		if !iconCaps[decl.Cap] {
			faults = append(faults, Fault{
				Field:   "icons.stroke.cap",
				Message: fmt.Sprintf("%q is not one of %v", decl.Cap, IconCaps()),
			})
		} else {
			out.Cap = decl.Cap
		}
	}
	if decl.Join != "" {
		if !iconJoins[decl.Join] {
			faults = append(faults, Fault{
				Field:   "icons.stroke.join",
				Message: fmt.Sprintf("%q is not one of %v", decl.Join, IconJoins()),
			})
		} else {
			out.Join = decl.Join
		}
	}
	return out, faults
}
