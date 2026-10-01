package theme

import (
	"strings"
	"testing"
)

// Empty visibility defaults to shown.
func TestLogomarkDefaultsToShown(t *testing.T) {
	t.Parallel()

	compiled, err := Compile(Declaration{
		Name: "X", Appearance: AppearanceLight, Tokens: minimalTokens(),
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if compiled.Brand.Logomark != LogomarkShown {
		t.Errorf("logomark = %q, want %q", compiled.Brand.Logomark, LogomarkShown)
	}
}

// Hidden is a valid visibility.
func TestLogomarkHiddenIsAccepted(t *testing.T) {
	t.Parallel()

	compiled, err := Compile(Declaration{
		Name: "X", Appearance: AppearanceLight, Tokens: minimalTokens(),
		Logomark: LogomarkHidden,
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if compiled.Brand.Logomark != LogomarkHidden {
		t.Errorf("logomark = %q, want %q", compiled.Brand.Logomark, LogomarkHidden)
	}
}

// Only visibility values are accepted.
func TestLogomarkRejectsAnythingButVisibility(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{
		"replaced",
		"custom",
		"<svg viewBox='0 0 64 64'><rect width='64' height='64'/></svg>",
		"data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=",
		"./mark.svg",
	} {
		t.Run(bad, func(t *testing.T) {
			t.Parallel()
			_, err := Compile(Declaration{
				Name: "X", Appearance: AppearanceLight, Tokens: minimalTokens(),
				Logomark: LogomarkVisibility(bad),
			})
			if err == nil {
				t.Fatalf("Compile accepted logomark %q", bad)
			}
			if !strings.Contains(err.Error(), "never substitute one") {
				t.Errorf("error = %v, want the reason a substitution is refused", err)
			}
		})
	}
}

// The lockup pair uses its named floor.
func TestLockupFloorPairIsTheSignalOnThePlate(t *testing.T) {
	t.Parallel()

	for _, pair := range LegibilityFloor() {
		if pair.Foreground != "accent-signal" || pair.Background != "brand-field" {
			continue
		}
		if pair.Min != LockupMarkMin {
			t.Errorf("lockup floor = %.1f, want LockupMarkMin %.1f", pair.Min, LockupMarkMin)
		}
		if pair.Why == "" {
			t.Error("lockup floor has no author-facing reason")
		}
		return
	}
	t.Fatal("legibility floor has no accent-signal / brand-field pair")
}

func TestLockupFloorRejectsInvisibleBars(t *testing.T) {
	t.Parallel()

	tokens := minimalTokens()
	tokens["accent"] = BrandFieldDefault.String()
	_, err := Compile(Declaration{Name: "X", Appearance: AppearanceDark, Tokens: tokens})
	if err == nil {
		t.Fatal("Compile accepted lockup bars that match the plate")
	}
	if !strings.Contains(err.Error(), "the product lockup bars must stay visible on their plate") {
		t.Errorf("error = %v, want the lockup floor's reason", err)
	}
	fields := faultFields(t, err)
	for _, f := range fields {
		if f == "tokens.accent-signal" {
			return
		}
	}
	t.Errorf("faults %v, want one on tokens.accent-signal", fields)
}

// Hidden marks still satisfy the palette floor.
func TestLockupFloorStillGatesAQuietedMark(t *testing.T) {
	t.Parallel()

	tokens := minimalTokens()
	tokens["accent-signal"] = BrandFieldDefault.String()
	_, err := Compile(Declaration{
		Name: "X", Appearance: AppearanceDark, Tokens: tokens, Logomark: LogomarkHidden,
	})
	if err == nil {
		t.Fatal("Compile accepted an invisible lockup because the mark was hidden")
	}
	if !strings.Contains(err.Error(), "the product lockup bars must stay visible on their plate") {
		t.Errorf("error = %v, want the lockup floor's reason", err)
	}
}

// Lockup bars use the signal token.
func TestLockupFloorAcceptsDarkAccentWithVisibleSignal(t *testing.T) {
	t.Parallel()

	tokens := minimalTokens()
	tokens["accent"] = BrandFieldDefault.String()
	tokens["accent-signal"] = "#b85c38"
	compileOrFail(t, Declaration{Name: "X", Appearance: AppearanceDark, Tokens: tokens})
}

// Transparent plates composite over the page.
func TestLockupFloorCompositesATransparentPlate(t *testing.T) {
	t.Parallel()

	tokens := minimalTokens()
	tokens["background"] = "#191817"
	tokens["text"] = "#e4e2df"
	tokens["brand-field"] = "#00000000"
	tokens["accent-signal"] = "#191817"
	_, err := Compile(Declaration{Name: "X", Appearance: AppearanceDark, Tokens: tokens})
	if err == nil {
		t.Fatal("Compile accepted bars that vanish into a dropped plate")
	}
	if !strings.Contains(err.Error(), "the product lockup bars must stay visible on their plate") {
		t.Errorf("error = %v, want the lockup floor's reason", err)
	}
}

// Saturated signal on a pale plate.
func TestLockupFloorAcceptsSignalOnChromePlate(t *testing.T) {
	t.Parallel()

	tokens := minimalTokens()
	tokens["brand-field"] = "#e6e9ef"
	tokens["accent"] = "#8839ef"
	compileOrFail(t, Declaration{Name: "X", Appearance: AppearanceLight, Tokens: tokens})
}
