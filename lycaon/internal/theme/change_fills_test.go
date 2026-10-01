package theme

import (
	"math"
	"slices"
	"testing"
)

func TestChangeFillsKeepCodeInkLegible(t *testing.T) {
	t.Parallel()

	dark := map[string]string{
		"background": "#1e1e2e", "text": "#cdd6f4", "accent": "#89b4fa",
		"danger": "#f38ba8", "warning": "#f9e2af", "status-positive": "#a6e3a1",
	}
	for name, decl := range map[string]Declaration{
		"light, dim comment": {
			Name: "Dim", Appearance: AppearanceLight, Tokens: minimalTokens(),
			Syntax: map[string]SyntaxDeclaration{
				"keyword": {Color: "#cf222e"}, "comment": {Color: "#9ca0b0"},
			},
		},
		"dark, bright hues": {
			Name: "Bright", Appearance: AppearanceDark, Tokens: dark,
			Syntax: map[string]SyntaxDeclaration{
				"keyword": {Color: "#f38ba8"}, "comment": {Color: "#6c7086"},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := compileOrFail(t, decl)
			plate := flatten(c.Tokens["surface"], c.Tokens["background"])
			for _, family := range changeFamilies {
				line := compositeOver(c.Tokens[family.line], plate)
				word := compositeOver(c.Tokens[family.word], line)
				for _, scope := range syntaxScopes {
					ink := c.Syntax[scope.ID].Color
					page := contrastRatio(ink, plate, plate)
					want := math.Min(CodeInkMin, CodeInkRetention*page)
					for layer, fill := range map[string]Color{"line": line, "word": word} {
						if got := contrastRatio(ink, fill, fill); got < want {
							t.Errorf("%s over %s %s = %.2f:1, want at least %.2f:1",
								scope.ID, family.hue, layer, got, want)
						}
					}
				}
			}
		})
	}
}

func TestChangeFillsTakeTheirShapeWhenInksAllow(t *testing.T) {
	t.Parallel()

	for _, appearance := range Appearances() {
		tokens := minimalTokens()
		if appearance == AppearanceDark {
			tokens["background"], tokens["text"] = "#191817", "#e4e2df"
			tokens["danger"], tokens["status-positive"] = "#e07070", "#7a9e6a"
			tokens["warning"], tokens["accent"] = "#e0a020", "#a85830"
		}
		c := compileOrFail(t, Declaration{Name: "Plain", Appearance: appearance, Tokens: tokens})
		shape := ChangeFillShapeFor(appearance)
		for _, family := range changeFamilies {
			line, word := c.Tokens[family.line], c.Tokens[family.word]
			wantLine := math.Round(255 * shape.Strength * shape.LineShare)
			if math.Abs(float64(line.A)-wantLine) > 1 {
				t.Errorf("%s %s alpha = %d, want %.0f", appearance, family.line, line.A, wantLine)
			}
			total := 1 - (1-float64(line.A)/255)*(1-float64(word.A)/255)
			if math.Abs(total-shape.Strength) > 0.01 {
				t.Errorf("%s %s over its line = %.3f, want %.2f", appearance, family.word, total, shape.Strength)
			}
			hue := c.Tokens[family.hue]
			for _, fill := range []Color{line, word} {
				if fill.R != hue.R || fill.G != hue.G || fill.B != hue.B {
					t.Errorf("%s fill %s is not %s's hue %s", appearance, fill, family.hue, hue)
				}
			}
		}
	}
}

func TestChangeFillsYieldToCodeInk(t *testing.T) {
	t.Parallel()

	plain := compileOrFail(t, Declaration{Name: "Plain", Appearance: AppearanceLight, Tokens: minimalTokens()})
	tight := compileOrFail(t, Declaration{
		Name: "Tight", Appearance: AppearanceLight, Tokens: minimalTokens(),
		Syntax: map[string]SyntaxDeclaration{"function": {Color: "#8250df"}},
	})
	for _, family := range changeFamilies {
		if tight.Tokens[family.word].A >= plain.Tokens[family.word].A {
			t.Errorf("%s = %s with a 5:1 function ink, want weaker than %s",
				family.word, tight.Tokens[family.word], plain.Tokens[family.word])
		}
		if tight.Tokens[family.word].A == 0 {
			t.Errorf("%s vanished; a 5:1 ink leaves room for a visible fill", family.word)
		}
	}
}

func TestAuthoredChangeFillIsRejected(t *testing.T) {
	t.Parallel()

	tokens := minimalTokens()
	tokens["diff-add-line"] = "#2d8a4e33"
	_, err := Compile(Declaration{Name: "Authored", Appearance: AppearanceLight, Tokens: tokens})
	if err == nil {
		t.Fatal("an authored change fill compiled")
	}
	if fields := faultFields(t, err); !slices.Contains(fields, "tokens.diff-add-line") {
		t.Errorf("faults = %v, want tokens.diff-add-line", fields)
	}
}
