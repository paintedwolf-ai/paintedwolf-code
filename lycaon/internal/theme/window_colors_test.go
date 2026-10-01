package theme

import (
	"math"
	"slices"
	"testing"
)

func TestWindowColorsDefaultToThemePalette(t *testing.T) {
	tokens := minimalTokens()
	compiled := compileOrFail(t, Declaration{Name: "Example", Appearance: AppearanceLight, Tokens: tokens})
	if compiled.WindowColors.Main != compiled.Tokens["accent-signal"] || compiled.WindowColors.Anchors[0] != compiled.Tokens["accent"] {
		t.Fatalf("window colors left their theme: %+v", compiled.WindowColors)
	}
}

func TestWindowColorRecipeValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		recipe WindowColorsDeclaration
		field  string
	}{
		{"transparent main", WindowColorsDeclaration{Main: "#ffffff00"}, "window_colors.main"},
		{"empty anchors", WindowColorsDeclaration{Anchors: []string{}}, "window_colors.anchors"},
		{"transparent anchor", WindowColorsDeclaration{Anchors: []string{"#ffffff00"}}, "window_colors.anchors.0"},
		{"expression", WindowColorsDeclaration{Anchors: []string{"var(--accent)"}}, "window_colors.anchors.0"},
		{"nonfinite hue", WindowColorsDeclaration{HueSpread: newFloat(math.NaN())}, "window_colors.hue_spread"},
		{"wide hue", WindowColorsDeclaration{HueSpread: newFloat(181)}, "window_colors.hue_spread"},
		{"inverted chroma", WindowColorsDeclaration{ChromaMin: newFloat(0.3), ChromaMax: newFloat(0.1)}, "window_colors.chroma_min"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile(Declaration{Name: "Example", Appearance: AppearanceLight, Tokens: minimalTokens(), WindowColors: &tc.recipe})
			if err == nil {
				t.Fatal("accepted invalid window colors")
			}
			if !slices.Contains(faultFields(t, err), tc.field) {
				t.Fatalf("fault = %v, want %s", err, tc.field)
			}
		})
	}
}

func newFloat(value float64) *float64 { return &value }
