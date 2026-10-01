package theme

import (
	"fmt"
	"math"
)

// Bevel is how strongly raised and recessed controls show their edges.
type Bevel struct {
	// Strength multiplies the alpha of every bevel-edge token.
	Strength float64
}

// Zero is flat; the ceiling doubles the product's edges.
const (
	minBevelStrength = 0.0
	maxBevelStrength = 2.0
)

// DefaultBevel returns the stock edges.
func DefaultBevel() Bevel { return Bevel{Strength: 1} }

// BevelStrengthRange returns the authorable bounds, for the generated schema.
func BevelStrengthRange() (minimum, maximum float64) {
	return minBevelStrength, maxBevelStrength
}

// BevelDeclaration is the authored form; an unset strength keeps the default.
type BevelDeclaration struct {
	Strength *float64
}

// resolveBevel validates a declaration into a total strength.
func resolveBevel(decl *BevelDeclaration) (Bevel, []Fault) {
	out := DefaultBevel()
	if decl == nil || decl.Strength == nil {
		return out, nil
	}
	s := *decl.Strength
	if math.IsNaN(s) || math.IsInf(s, 0) {
		return out, []Fault{{
			Field: "bevel.strength",
			Message: fmt.Sprintf("strength must be a finite number between %g and %g",
				minBevelStrength, maxBevelStrength),
		}}
	}
	if s < minBevelStrength || s > maxBevelStrength {
		return out, []Fault{{
			Field: "bevel.strength",
			Message: fmt.Sprintf("%.2f is outside %g–%g — %g is flat, %g doubles the product's edges",
				s, minBevelStrength, maxBevelStrength, minBevelStrength, maxBevelStrength),
		}}
	}
	out.Strength = s
	return out, nil
}

// scale applies the strength to one edge. An edge already at full alpha
// cannot strengthen further.
func (b Bevel) scale(edge Color) Color {
	alpha := math.Round(float64(edge.A) * b.Strength)
	if alpha <= 0 {
		return transparentInk
	}
	edge.A = uint8(math.Min(alpha, 0xff))
	return edge
}
