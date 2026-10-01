package contribution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/theme"
)

// Official Catppuccin flavor palettes from https://catppuccin.com/palette/.
// A shipped port may only paint these hexes. Role names are the upstream identifiers.
var catppuccinPalette = map[string]map[string]string{
	"latte": {
		"rosewater": "#dc8a78", "flamingo": "#dd7878", "pink": "#ea76cb",
		"mauve": "#8839ef", "red": "#d20f39", "maroon": "#e64553",
		"peach": "#fe640b", "yellow": "#df8e1d", "green": "#40a02b",
		"teal": "#179299", "sky": "#04a5e5", "sapphire": "#209fb5",
		"blue": "#1e66f5", "lavender": "#7287fd", "text": "#4c4f69",
		"subtext1": "#5c5f77", "subtext0": "#6c6f85", "overlay2": "#7c7f93",
		"overlay1": "#8c8fa1", "overlay0": "#9ca0b0", "surface2": "#acb0be",
		"surface1": "#bcc0cc", "surface0": "#ccd0da", "base": "#eff1f5",
		"mantle": "#e6e9ef", "crust": "#dce0e8",
	},
	"frappe": {
		"rosewater": "#f2d5cf", "flamingo": "#eebebe", "pink": "#f4b8e4",
		"mauve": "#ca9ee6", "red": "#e78284", "maroon": "#ea999c",
		"peach": "#ef9f76", "yellow": "#e5c890", "green": "#a6d189",
		"teal": "#81c8be", "sky": "#99d1db", "sapphire": "#85c1dc",
		"blue": "#8caaee", "lavender": "#babbf1", "text": "#c6d0f5",
		"subtext1": "#b5bfe2", "subtext0": "#a5adce", "overlay2": "#949cbb",
		"overlay1": "#838ba7", "overlay0": "#737994", "surface2": "#626880",
		"surface1": "#51576d", "surface0": "#414559", "base": "#303446",
		"mantle": "#292c3c", "crust": "#232634",
	},
	"macchiato": {
		"rosewater": "#f4dbd6", "flamingo": "#f0c6c6", "pink": "#f5bde6",
		"mauve": "#c6a0f6", "red": "#ed8796", "maroon": "#ee99a0",
		"peach": "#f5a97f", "yellow": "#eed49f", "green": "#a6da95",
		"teal": "#8bd5ca", "sky": "#91d7e3", "sapphire": "#7dc4e4",
		"blue": "#8aadf4", "lavender": "#b7bdf8", "text": "#cad3f5",
		"subtext1": "#b8c0e0", "subtext0": "#a5adcb", "overlay2": "#939ab7",
		"overlay1": "#8087a2", "overlay0": "#6e738d", "surface2": "#5b6078",
		"surface1": "#494d64", "surface0": "#363a4f", "base": "#24273a",
		"mantle": "#1e2030", "crust": "#181926",
	},
	"mocha": {
		"rosewater": "#f5e0dc", "flamingo": "#f2cdcd", "pink": "#f5c2e7",
		"mauve": "#cba6f7", "red": "#f38ba8", "maroon": "#eba0ac",
		"peach": "#fab387", "yellow": "#f9e2af", "green": "#a6e3a1",
		"teal": "#94e2d5", "sky": "#89dceb", "sapphire": "#74c7ec",
		"blue": "#89b4fa", "lavender": "#b4befe", "text": "#cdd6f4",
		"subtext1": "#bac2de", "subtext0": "#a6adc8", "overlay2": "#9399b2",
		"overlay1": "#7f849c", "overlay0": "#6c7086", "surface2": "#585b70",
		"surface1": "#45475a", "surface0": "#313244", "base": "#1e1e2e",
		"mantle": "#181825", "crust": "#11111b",
	},
}

// Official style-guide roles the host vocabulary can name.
// https://github.com/catppuccin/catppuccin/blob/main/docs/style-guide.md
var catppuccinTokenRoles = map[string]string{
	"background":       "base",
	"surface":          "base",
	"surface-elevated": "surface0",
	"surface-chrome":   "mantle",
	"border":           "surface1",
	"text":             "text",
	"text-muted":       "subtext0",
	"accent":           "mauve",
	"accent-warm":      "pink",
	"accent-text":      "mauve",
	"accent-signal":    "mauve",
	"danger":           "red",
	"warning":          "yellow",
	"status-positive":  "green",
	"status-running":   "peach",
	"draft-accent":     "teal",
	"tint-hue":         "overlay0",
	"caret":            "rosewater",
	"diff-delete-hue":  "red",
	"cost-coordinator": "peach",
	"cost-workers":     "blue",
	"cost-summarizer":  "green",
	"identity-1":       "mauve",
	"identity-2":       "blue",
	"identity-3":       "green",
	"identity-4":       "peach",
	"identity-5":       "teal",
}

var catppuccinSyntaxRoles = map[string]string{
	"keyword":            "mauve",
	"string":             "green",
	"number":             "peach",
	"constant":           "peach",
	"variable":           "text",
	"type":               "yellow",
	"function":           "blue",
	"property":           "blue",
	"operator":           "sky",
	"punctuation":        "overlay2",
	"tag":                "blue",
	"meta":               "yellow",
	"invalid":            "red",
	"link":               "blue",
	"heading":            "blue",
	"inserted":           "green",
	"deleted":            "red",
	"quote":              "overlay2",
	"comment":            "overlay2",
	"string.escape":      "pink",
	"string.regexp":      "pink",
	"variable.parameter": "maroon",
	"variable.special":   "red",
	"keyword.self":       "red",
	"function.macro":     "rosewater",
	"property.attribute": "yellow",
}

func TestCatppuccinPortsMatchOfficialPalette(t *testing.T) {
	t.Parallel()

	for _, path := range shippedCatppuccinUnits(t) {
		flavor := strings.TrimPrefix(strings.TrimSuffix(filepath.Base(path), ".yaml"), "catppuccin-")
		palette, ok := catppuccinPalette[flavor]
		if !ok {
			t.Errorf("%s: unknown flavor %q", filepath.Base(path), flavor)
			continue
		}
		body, err := os.ReadFile(path) // #nosec G304 -- bundled pack path under test
		if err != nil {
			testutil.FailErr(t, "read "+filepath.Base(path), err)
		}
		decl, err := DecodeTheme(body)
		if err != nil {
			testutil.FailErr(t, "decode "+filepath.Base(path), err)
		}
		compiled, err := decl.Compiled()
		if err != nil {
			testutil.FailErr(t, "compile "+filepath.Base(path), err)
		}

		assertCatppuccinRoles(t, compiled, flavor, palette)
		assertAuthoredHexes(t, decl, flavor, palette)
	}
}

func assertCatppuccinRoles(t *testing.T, compiled theme.Compiled, flavor string, palette map[string]string) {
	t.Helper()

	tokenRoles := make(map[string]string, len(catppuccinTokenRoles)+3)
	for id, name := range catppuccinTokenRoles {
		tokenRoles[id] = name
	}
	// Blocks take the first rung off the page and the lift takes the next, so
	// selection moves up one to stay visible against a block it marks.
	if flavor == "latte" {
		// Latte steps down from base, so the page is its own lift. Teal
		// separates adds from red under deuteranopia.
		tokenRoles["surface-offset"] = "crust"
		tokenRoles["surface-raised"] = "base"
		tokenRoles["selection"] = "surface0"
		tokenRoles["selection-strong"] = "surface1"
		tokenRoles["diff-add-hue"] = "teal"
		tokenRoles["brand-field"] = "mantle"
	} else {
		tokenRoles["surface-offset"] = "surface0"
		tokenRoles["surface-raised"] = "surface1"
		tokenRoles["selection"] = "surface1"
		tokenRoles["selection-strong"] = "surface2"
		tokenRoles["diff-add-hue"] = "green"
	}

	for id, name := range tokenRoles {
		want := palette[name]
		if got := compiled.Tokens[id].String(); got != want {
			t.Errorf("%s: token %s = %s, official %s is %s", compiled.Name, id, got, name, want)
		}
	}
	for id, name := range catppuccinSyntaxRoles {
		want := palette[name]
		if got := compiled.Syntax[id].Color.String(); got != want {
			t.Errorf("%s: syntax %s = %s, official %s is %s", compiled.Name, id, got, name, want)
		}
	}
	for _, id := range []string{"comment", "comment.doc", "quote"} {
		if !compiled.Syntax[id].Italic {
			t.Errorf("%s: syntax %s lost its italic", compiled.Name, id)
		}
	}
	if !compiled.Syntax["heading"].Bold {
		t.Errorf("%s: headings lost their weight", compiled.Name)
	}
}

func assertAuthoredHexes(t *testing.T, decl *Theme, flavor string, palette map[string]string) {
	t.Helper()

	allowed := make(map[string]bool, len(palette))
	for _, hex := range palette {
		allowed[hex] = true
	}

	for id, hex := range decl.Tokens {
		if !allowed[strings.ToLower(hex)] {
			t.Errorf("%s: token %s = %s, not in the official %s palette", decl.Name, id, hex, flavor)
		}
	}
	for id, entry := range decl.Syntax {
		if entry.Color == "" {
			continue
		}
		if !allowed[strings.ToLower(entry.Color)] {
			t.Errorf("%s: syntax %s = %s, not in the official %s palette", decl.Name, id, entry.Color, flavor)
		}
	}
}

func shippedCatppuccinUnits(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, path := range shippedThemeUnits(t) {
		if strings.HasPrefix(filepath.Base(path), "catppuccin-") {
			out = append(out, path)
		}
	}
	if len(out) != 4 {
		t.Fatalf("catppuccin units = %d, want latte, frappe, macchiato, mocha", len(out))
	}
	return out
}
