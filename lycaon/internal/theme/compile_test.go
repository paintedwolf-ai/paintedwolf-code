package theme

import (
	"errors"
	"strings"
	"testing"
)

// minimalTokens returns the required author surface.
func minimalTokens() map[string]string {
	return map[string]string{
		"background":      "#ffffff",
		"text":            "#1c1b1a",
		"accent":          "#b85c38",
		"danger":          "#c2453f",
		"warning":         "#c97a00",
		"status-positive": "#5d7a55",
	}
}

func compileOrFail(t *testing.T, decl Declaration) Compiled {
	t.Helper()
	c, err := Compile(decl)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return c
}

func faultFields(t *testing.T, err error) []string {
	t.Helper()
	var faults *FaultsError
	if !errors.As(err, &faults) {
		t.Fatalf("error = %v, want FaultsError", err)
	}
	out := make([]string, 0, len(faults.Faults))
	for _, f := range faults.Faults {
		out = append(out, f.Field)
	}
	return out
}

// Required colors resolve a complete theme.
func TestMinimalThemeCompilesTotal(t *testing.T) {
	t.Parallel()

	c := compileOrFail(t, Declaration{
		Name: "Minimal", Appearance: AppearanceLight, Tokens: minimalTokens(),
	})
	for _, tok := range BaseTokens() {
		if _, ok := c.Tokens[tok.ID]; !ok {
			t.Errorf("token %s is unresolved", tok.ID)
		}
	}
	for _, scope := range SyntaxScopes() {
		if _, ok := c.Syntax[scope.ID]; !ok {
			t.Errorf("scope %s is unresolved", scope.ID)
		}
	}
}

// Derived tokens follow their declared inputs.
func TestUnsetDerivedTokensFollowTheirInputs(t *testing.T) {
	t.Parallel()

	tokens := minimalTokens()
	// Use hues that isolate derivation from contrast gating.
	tokens["status-positive"] = "#2e7d32"
	tokens["danger"] = "#c62828"
	c := compileOrFail(t, Declaration{
		Name: "Derived", Appearance: AppearanceLight, Tokens: tokens,
	})
	if got := c.Tokens["diff-add-hue"].String(); got != "#2e7d32" {
		t.Errorf("diff-add-hue = %s, want the status-positive it derives from", got)
	}
	if got := c.Tokens["diff-delete-hue"].String(); got != "#c62828" {
		t.Errorf("diff-delete-hue = %s, want the danger it derives from", got)
	}
	if got := c.Tokens["surface"].String(); got != "#ffffff" {
		t.Errorf("surface = %s, want the background it derives from", got)
	}
	if got := c.Tokens["brand-field"].String(); got != BrandFieldDefault.String() {
		t.Errorf("brand-field = %s, want the product square", got)
	}
}

// Unset shadow is body ink on light, true black on dark.
func TestShadowDerivesByAppearance(t *testing.T) {
	t.Parallel()

	light := compileOrFail(t, Declaration{
		Name: "Paper", Appearance: AppearanceLight, Tokens: minimalTokens(),
	})
	if got := light.Tokens["shadow"].String(); got != "#1c1b1a" {
		t.Errorf("light shadow = %s, want body ink", got)
	}

	darkTokens := map[string]string{
		"background":      "#1b1f24",
		"text":            "#e6edf3",
		"accent":          "#6cb6ff",
		"danger":          "#ff7b72",
		"warning":         "#e3b341",
		"status-positive": "#7ee787",
	}
	dark := compileOrFail(t, Declaration{
		Name: "Slate", Appearance: AppearanceDark, Tokens: darkTokens,
	})
	if got := dark.Tokens["shadow"].String(); got != blackInk.String() {
		t.Errorf("dark shadow = %s, want true black", got)
	}
}

// Diff hue overrides do not change status colors.
func TestDiffHuesOverrideWithoutTouchingStatus(t *testing.T) {
	t.Parallel()

	tokens := minimalTokens()
	tokens["diff-add-hue"] = "#3b7dd8"
	tokens["diff-delete-hue"] = "#a06800"
	c := compileOrFail(t, Declaration{
		Name: "Colorblind-safe", Appearance: AppearanceLight, Tokens: tokens,
	})
	if got := c.Tokens["diff-add-hue"].String(); got != "#3b7dd8" {
		t.Errorf("diff-add-hue = %s, want the authored blue", got)
	}
	if got := c.Tokens["status-positive"].String(); got != "#5d7a55" {
		t.Errorf("status-positive = %s — redefining a diff hue must not move success", got)
	}
}

// Coarse syntax scopes fill their children.
func TestSyntaxFoldFillsChildScopes(t *testing.T) {
	t.Parallel()

	c := compileOrFail(t, Declaration{
		Name: "Folded", Appearance: AppearanceLight, Tokens: minimalTokens(),
		Syntax: map[string]SyntaxDeclaration{
			"keyword": {Color: "#c792ea", Bold: true},
		},
	})
	control := c.Syntax["keyword.control"]
	if control.Color.String() != "#c792ea" || !control.Bold {
		t.Errorf("keyword.control = %+v, want the parent's color and flags", control)
	}
	if got := c.Syntax["string"].Color.String(); got != "#1c1b1a" {
		t.Errorf("unset root string = %s, want body text", got)
	}
}

// Authored children override their fold.
func TestAuthoredChildScopeWinsOverParent(t *testing.T) {
	t.Parallel()

	c := compileOrFail(t, Declaration{
		Name: "Specific", Appearance: AppearanceDark, Tokens: minimalTokens(),
		Syntax: map[string]SyntaxDeclaration{
			"keyword":         {Color: "#c792ea"},
			"keyword.control": {Color: "#ff5370", Italic: true},
		},
	})
	if got := c.Syntax["keyword.control"]; got.Color.String() != "#ff5370" || !got.Italic {
		t.Errorf("keyword.control = %+v, want the authored value", got)
	}
	if got := c.Syntax["keyword.module"].Color.String(); got != "#c792ea" {
		t.Errorf("keyword.module = %s, want the parent it folds to", got)
	}
}

func TestClosedVocabulariesReject(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		decl      Declaration
		wantField string
	}{
		{
			name: "unknown token id",
			decl: Declaration{Name: "X", Appearance: AppearanceLight, Tokens: func() map[string]string {
				m := minimalTokens()
				m["chrome-sparkle"] = "#ffffff"
				return m
			}()},
			wantField: "tokens.chrome-sparkle",
		},
		{
			name: "unknown syntax scope",
			decl: Declaration{Name: "X", Appearance: AppearanceLight, Tokens: minimalTokens(),
				Syntax: map[string]SyntaxDeclaration{"keyword.pythonish": {Color: "#ffffff"}}},
			wantField: "syntax.keyword.pythonish",
		},
		{
			name: "missing required token",
			decl: Declaration{Name: "X", Appearance: AppearanceLight, Tokens: func() map[string]string {
				m := minimalTokens()
				delete(m, "danger")
				return m
			}()},
			wantField: "tokens.danger",
		},
		{
			name: "translucent page plane",
			decl: Declaration{Name: "X", Appearance: AppearanceLight, Tokens: func() map[string]string {
				m := minimalTokens()
				m["background"] = "#ffffff80"
				return m
			}()},
			wantField: "tokens.background",
		},
		{
			name: "translucent chrome plane",
			decl: Declaration{Name: "X", Appearance: AppearanceLight, Tokens: func() map[string]string {
				m := minimalTokens()
				m["surface-chrome"] = "#ffffff80"
				return m
			}()},
			wantField: "tokens.surface-chrome",
		},
		{
			name:      "unknown appearance",
			decl:      Declaration{Name: "X", Appearance: "twilight", Tokens: minimalTokens()},
			wantField: "appearance",
		},
		{
			name:      "missing name",
			decl:      Declaration{Appearance: AppearanceLight, Tokens: minimalTokens()},
			wantField: "name",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Compile(tc.decl)
			if err == nil {
				t.Fatal("Compile succeeded, want a fault")
			}
			fields := faultFields(t, err)
			for _, f := range fields {
				if f == tc.wantField {
					return
				}
			}
			t.Errorf("faults %v, want one on %s", fields, tc.wantField)
		})
	}
}

// Theme colors accept only literals.
func TestOnlyLiteralColorsParse(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{
		"var(--den-accent)",
		"color-mix(in srgb, #fff 50%, #000)",
		"red",
		"rgb(1,2,3)",
		"#fff",
		"#12345",
		"url(x.png)",
		"#ffffff; background: url(evil)",
	} {
		t.Run(bad, func(t *testing.T) {
			t.Parallel()
			tokens := minimalTokens()
			tokens["accent"] = bad
			if _, err := Compile(Declaration{
				Name: "X", Appearance: AppearanceLight, Tokens: tokens,
			}); err == nil {
				t.Errorf("Compile accepted %q", bad)
			}
		})
	}
}

// Security indicators require contrast against their surfaces.
func TestLegibilityFloorRejectsInvisibleChrome(t *testing.T) {
	t.Parallel()

	tokens := minimalTokens()
	tokens["danger"] = "#fdfdfd" // all but invisible on white
	_, err := Compile(Declaration{Name: "X", Appearance: AppearanceLight, Tokens: tokens})
	if err == nil {
		t.Fatal("Compile accepted an invisible danger state")
	}
	if !strings.Contains(err.Error(), "destructive states must stay visible") {
		t.Errorf("error = %v, want the floor's reason", err)
	}
}

func TestLegibilityFloorRejectsUnreadableBody(t *testing.T) {
	t.Parallel()

	tokens := minimalTokens()
	tokens["text"] = "#f4f4f4"
	if _, err := Compile(Declaration{
		Name: "X", Appearance: AppearanceLight, Tokens: tokens,
	}); err == nil {
		t.Fatal("Compile accepted unreadable body text")
	}
}

// Accent derives legible label ink.
func TestOnAccentDerivesLegibly(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ accent, want string }{
		{accent: "#101010", want: "#ffffff"},
		{accent: "#f5f5f5", want: "#141312"},
	} {
		tokens := minimalTokens()
		tokens["accent"] = tc.accent
		// Dark accent fails the lockup pair unless the signal is set.
		if tc.accent == "#101010" {
			tokens["accent-signal"] = "#b85c38"
		}
		c := compileOrFail(t, Declaration{
			Name: "X", Appearance: AppearanceLight, Tokens: tokens,
		})
		if got := c.Tokens["on-accent"].String(); got != tc.want {
			t.Errorf("accent %s → on-accent %s, want %s", tc.accent, got, tc.want)
		}
	}
}

func TestOnAccentUsesRenderedFill(t *testing.T) {
	t.Parallel()

	white := Color{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	halfBlack := Color{A: 0x80}
	if got := readableOn(halfBlack, white).String(); got != "#141312" {
		t.Errorf("on translucent accent = %s, want dark stock ink", got)
	}
}

func TestContrastRatioMatchesKnownValues(t *testing.T) {
	t.Parallel()

	white := Color{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	black := Color{R: 0, G: 0, B: 0, A: 0xff}
	if got := contrastRatio(white, black, white); got < 20.9 || got > 21.1 {
		t.Errorf("white on black = %.2f, want 21", got)
	}
	if got := contrastRatio(white, white, white); got < 0.99 || got > 1.01 {
		t.Errorf("white on white = %.2f, want 1", got)
	}
}

// Translucent values are measured after compositing.
func TestContrastCompositesAlphaOverBackdrop(t *testing.T) {
	t.Parallel()

	white := Color{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	halfBlack := Color{R: 0, G: 0, B: 0, A: 0x80}
	opaque := contrastRatio(Color{R: 0, G: 0, B: 0, A: 0xff}, white, white)
	faded := contrastRatio(halfBlack, white, white)
	if faded >= opaque {
		t.Errorf("translucent ink measured %.2f, opaque %.2f — alpha was ignored", faded, opaque)
	}
}

func TestContrastCompositesForegroundOverRenderedBackground(t *testing.T) {
	t.Parallel()

	white := Color{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	halfWhite := Color{R: 0xff, G: 0xff, B: 0xff, A: 0x80}
	halfBlack := Color{R: 0, G: 0, B: 0, A: 0x80}
	ratio := contrastRatio(halfWhite, halfBlack, white)
	if ratio < 2.17 || ratio > 2.19 {
		t.Errorf("half-white on half-black on white = %.3f, want about 2.177", ratio)
	}
}

func TestLegibleInkUsesRenderedBackground(t *testing.T) {
	t.Parallel()

	white := Color{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	halfBlack := Color{A: 0x80}
	gray := Color{R: 0x77, G: 0x77, B: 0x77, A: 0xff}
	ink := legibleInk(gray, halfBlack, white, StatusInkMin)
	if ratio := contrastRatio(ink, halfBlack, white); ratio < StatusInkMin {
		t.Errorf("derived ink ratio = %.2f, want at least %.1f", ratio, StatusInkMin)
	}
}

func TestMixUsesCSSPremultipliedAlpha(t *testing.T) {
	t.Parallel()

	transparentRed := Color{R: 0xff, A: 0}
	opaqueBlue := Color{B: 0xff, A: 0xff}
	if got := mix(transparentRed, 50, opaqueBlue); got != (Color{B: 0xff, A: 0x80}) {
		t.Errorf("transparent red mixed with blue = %s, want #0000ff80", got.String())
	}

	halfRed := Color{R: 0xff, A: 0x80}
	halfBlue := Color{B: 0xff, A: 0x80}
	if got := mix(halfRed, 50, halfBlue); got != (Color{R: 0x80, B: 0x80, A: 0x80}) {
		t.Errorf("half red mixed with half blue = %s, want #80008080", got.String())
	}
}
