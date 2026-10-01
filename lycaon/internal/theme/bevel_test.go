package theme

import (
	"math"
	"testing"
)

// paleAccentTokens is a dark palette whose accent takes dark label ink.
func paleAccentTokens() map[string]string {
	return map[string]string{
		"background":      "#2e3440",
		"text":            "#eceff4",
		"accent":          "#88c0d0",
		"danger":          "#bf616a",
		"warning":         "#ebcb8b",
		"status-positive": "#a3be8c",
	}
}

func bevelStrength(v float64) *BevelDeclaration {
	return &BevelDeclaration{Strength: &v}
}

func bevelEdgeIDs() []string {
	var ids []string
	for _, tok := range baseTokens {
		if tok.Fill == FillBevel {
			ids = append(ids, tok.ID)
		}
	}
	return ids
}

func TestUnsetBevelKeepsStockEdges(t *testing.T) {
	t.Parallel()

	unset := compileOrFail(t, Declaration{
		Name: "Unset", Appearance: AppearanceLight, Tokens: minimalTokens(),
	})
	explicit := compileOrFail(t, Declaration{
		Name: "Explicit", Appearance: AppearanceLight, Tokens: minimalTokens(),
		Bevel: bevelStrength(1),
	})
	if unset.Bevel != DefaultBevel() {
		t.Errorf("unset bevel = %+v, want the default", unset.Bevel)
	}
	for _, id := range bevelEdgeIDs() {
		if unset.Tokens[id] != explicit.Tokens[id] {
			t.Errorf("%s = %s unset, %s at strength 1", id, unset.Tokens[id], explicit.Tokens[id])
		}
	}
}

func TestBevelStrengthZeroFlattensEveryEdge(t *testing.T) {
	t.Parallel()

	for appearance, tokens := range map[Appearance]map[string]string{
		AppearanceLight: minimalTokens(),
		AppearanceDark:  paleAccentTokens(),
	} {
		c := compileOrFail(t, Declaration{
			Name: "Flat", Appearance: appearance, Tokens: tokens, Bevel: bevelStrength(0),
		})
		for _, id := range bevelEdgeIDs() {
			if got := c.Tokens[id]; got != transparentInk {
				t.Errorf("%s %s = %s at strength 0, want fully transparent", appearance, id, got)
			}
		}
	}
}

func TestBevelStrengthScalesEdgeAlpha(t *testing.T) {
	t.Parallel()

	stock := compileOrFail(t, Declaration{
		Name: "Stock", Appearance: AppearanceDark, Tokens: paleAccentTokens(),
	})
	soft := compileOrFail(t, Declaration{
		Name: "Soft", Appearance: AppearanceDark, Tokens: paleAccentTokens(),
		Bevel: bevelStrength(0.5),
	})
	for _, id := range bevelEdgeIDs() {
		full, half := stock.Tokens[id], soft.Tokens[id]
		want := uint8(math.Round(float64(full.A) * 0.5))
		if half.A != want {
			t.Errorf("%s alpha = %#x at strength 0.5, want %#x (half of %#x)", id, half.A, want, full.A)
		}
		if half.R != full.R || half.G != full.G || half.B != full.B {
			t.Errorf("%s ink = %s at strength 0.5, want the stock ink of %s", id, half, full)
		}
	}
}

func TestBevelStrengthStopsAtFullAlpha(t *testing.T) {
	t.Parallel()

	c := compileOrFail(t, Declaration{
		Name: "Bold", Appearance: AppearanceLight, Tokens: minimalTokens(),
		Bevel: bevelStrength(2),
	})
	// The light lit edge is already opaque; only its shade can strengthen.
	if got := c.Tokens["bevel-highlight"].String(); got != "#ffffff" {
		t.Errorf("bevel-highlight = %s at strength 2, want it held at opaque white", got)
	}
	if got := c.Tokens["bevel-shadow"].String(); got != "#1c1b1a42" {
		t.Errorf("bevel-shadow = %s at strength 2, want twice the stock alpha", got)
	}
}

func TestBevelStrengthOutsideRangeFaults(t *testing.T) {
	t.Parallel()

	for name, v := range map[string]float64{
		"negative":  -0.1,
		"above":     2.5,
		"nan":       math.NaN(),
		"infinity":  math.Inf(1),
		"-infinity": math.Inf(-1),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := Compile(Declaration{
				Name: "X", Appearance: AppearanceLight, Tokens: minimalTokens(),
				Bevel: bevelStrength(v),
			})
			if err == nil {
				t.Fatalf("Compile accepted bevel strength %v", v)
			}
			fields := faultFields(t, err)
			if len(fields) != 1 || fields[0] != "bevel.strength" {
				t.Errorf("faults %v, want one on bevel.strength", fields)
			}
		})
	}
}

func TestBevelEdgesAreNotAuthorable(t *testing.T) {
	t.Parallel()

	for _, id := range bevelEdgeIDs() {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			tokens := minimalTokens()
			tokens[id] = "#ffffff"
			_, err := Compile(Declaration{Name: "X", Appearance: AppearanceLight, Tokens: tokens})
			if err == nil {
				t.Fatalf("Compile accepted an authored %s", id)
			}
			fields := faultFields(t, err)
			if len(fields) != 1 || fields[0] != "tokens."+id {
				t.Errorf("faults %v, want one on tokens.%s", fields, id)
			}
		})
	}
}

// Lit edges brighten the fill independently of label ink.
func TestPlateHighlightLightensEveryAccent(t *testing.T) {
	t.Parallel()

	cases := map[string]Declaration{
		"deep accent, light ink": {
			Name: "Deep", Appearance: AppearanceLight, Tokens: minimalTokens(),
		},
		"pale accent, dark ink": {
			Name: "Pale", Appearance: AppearanceDark, Tokens: paleAccentTokens(),
		},
	}
	for name, decl := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := compileOrFail(t, decl)
			accent := c.Tokens["accent"]
			lit := compositeOver(c.Tokens["bevel-plate-highlight"], accent)
			if relativeLuminance(lit) <= relativeLuminance(accent) {
				t.Errorf("plate lit edge %s over accent %s renders %s — no lighter than the fill",
					c.Tokens["bevel-plate-highlight"], accent, lit)
			}
		})
	}

	pale := compileOrFail(t, cases["pale accent, dark ink"])
	if relativeLuminance(pale.Tokens["on-accent"]) >= relativeLuminance(pale.Tokens["accent"]) {
		t.Fatalf("on-accent %s is not the dark ink this case exists to cover", pale.Tokens["on-accent"])
	}
}
