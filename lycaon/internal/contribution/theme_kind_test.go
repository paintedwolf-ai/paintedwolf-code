package contribution

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/theme"
)

// minimalThemeYAML carries only the required tokens.
const minimalThemeYAML = `id: acme/kit:minimal
name: Minimal
appearance: light
tokens:
  background: "#ffffff"
  text: "#1c1b1a"
  accent: "#b85c38"
  danger: "#c2453f"
  warning: "#c97a00"
  status-positive: "#5d7a55"
`

func TestThemeYAMLRejectsNonFiniteStrokeWeight(t *testing.T) {
	t.Parallel()

	for _, weight := range []string{".nan", ".inf", "-.inf"} {
		t.Run(weight, func(t *testing.T) {
			t.Parallel()
			body := []byte(minimalThemeYAML + "icons:\n  stroke:\n    weight: " + weight + "\n")
			decl, err := DecodeTheme(body)
			testutil.FailErr(t, "decode theme", err)
			if _, err := decl.Compiled(); err == nil {
				t.Fatalf("Compiled accepted stroke weight %s", weight)
			}
		})
	}
}

func TestThemeYAMLBevelStrengthReachesEveryEdge(t *testing.T) {
	t.Parallel()

	decl, err := DecodeTheme([]byte(minimalThemeYAML + "bevel:\n  strength: 0\n"))
	testutil.FailErr(t, "decode theme", err)
	compiled, err := decl.Compiled()
	testutil.FailErr(t, "compile theme", err)
	if compiled.Bevel.Strength != 0 {
		t.Errorf("bevel strength = %g, want the authored 0", compiled.Bevel.Strength)
	}
	for _, tok := range theme.BaseTokens() {
		if tok.Fill == theme.FillBevel && compiled.Tokens[tok.ID].A != 0 {
			t.Errorf("%s = %s under a flat theme, want fully transparent", tok.ID, compiled.Tokens[tok.ID])
		}
	}
}

func TestThemeYAMLBevelCarriesStrengthOnly(t *testing.T) {
	t.Parallel()

	body := []byte(minimalThemeYAML + "bevel:\n  strength: 1\n  highlight: \"#ffffff\"\n")
	if _, err := DecodeTheme(body); err == nil {
		t.Fatal("decode accepted a bevel color; a theme sets strength only")
	}
}

func TestThemeWindowColorRecipeSurvivesYAML(t *testing.T) {
	decl, err := DecodeTheme([]byte(minimalThemeYAML + `window_colors:
  main: "#995522"
  anchors: ["#337799", "#779933"]
  hue_spread: 12
  chroma_min: 0.03
  chroma_max: 0.1
`))
	testutil.FailErr(t, "decode window color theme", err)
	compiled, err := decl.Compiled()
	testutil.FailErr(t, "compile window color theme", err)
	colors := compiled.WindowColors
	if colors.Main.String() != "#995522" || len(colors.Anchors) != 2 || colors.HueSpread != 12 || colors.ChromaMin != 0.03 || colors.ChromaMax != 0.1 {
		t.Fatalf("window recipe = %+v", colors)
	}
	if _, err := DecodeTheme([]byte(minimalThemeYAML + "window_colors:\n  unknown: 1\n")); err == nil {
		t.Fatal("accepted unknown palette field")
	}
}
