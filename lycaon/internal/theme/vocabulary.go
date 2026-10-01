package theme

import "sort"

// FillRule says how a base token gets its value, and whether a theme may set it.
type FillRule string

const (
	// FillRequired has no derived value.
	FillRequired FillRule = "required"
	// FillDerived is computed from other base tokens.
	FillDerived FillRule = "derived-by-default"
	// FillBevel derives edge color; bevel.strength scales its alpha.
	FillBevel FillRule = "bevel-edge"
	// FillChange is fitted to the resolved syntax inks; a theme sets its hue.
	FillChange FillRule = "change-fill"
)

// Token is one base-plane entry.
type Token struct {
	// ID is the author-facing logical id.
	ID string
	// Description appears in the author schema and reference.
	Description string
	Fill        FillRule
	// Opaque requires a fully opaque authored color.
	Opaque bool
	// CSSVar is the rendered custom property.
	CSSVar string
	// derive may read tokens declared above this one.
	derive func(r Resolver, appearance Appearance) Color
}

// Resolver reads an already-resolved token during derivation.
type Resolver func(id string) Color

// blackInk is the dark pole of every derivation.
var blackInk = Color{A: 0xff}

// transparentInk is the zero color, the far end of every alpha derivation.
var transparentInk = Color{}

// whiteInk is the light pole of every derivation.
var whiteInk = Color{R: 0xff, G: 0xff, B: 0xff, A: 0xff}

// baseTokens is the vocabulary in resolution order: required decisions first,
// then derivations that may read them.
var baseTokens = []Token{
	{
		ID:          "background",
		Description: "Page backdrop behind every surface.",
		Fill:        FillRequired,
		Opaque:      true,
		CSSVar:      "--den-background",
	},
	{
		ID:          "text",
		Description: "Body ink on background and surface.",
		Fill:        FillRequired,
		CSSVar:      "--den-text",
	},
	{
		ID:          "accent",
		Description: "Accent fill behind on-accent labels (primary buttons).",
		Fill:        FillRequired,
		CSSVar:      "--den-accent",
	},
	{
		ID:          "danger",
		Description: "Destructive and error states.",
		Fill:        FillRequired,
		CSSVar:      "--den-danger",
	},
	{
		ID:          "warning",
		Description: "Caution states, distinct from danger.",
		Fill:        FillRequired,
		CSSVar:      "--den-warning",
	},
	{
		ID:          "status-positive",
		Description: "Success and completion states.",
		Fill:        FillRequired,
		CSSVar:      "--den-status-positive",
	},
	{
		ID:          "surface",
		Description: "Panel and card fill.",
		Fill:        FillDerived,
		CSSVar:      "--den-surface",
		derive:      func(r Resolver, _ Appearance) Color { return r("background") },
	},
	{
		ID:          "surface-elevated",
		Description: "Raised surface (menus, popovers).",
		Fill:        FillDerived,
		CSSVar:      "--den-surface-elevated",
		derive:      func(r Resolver, _ Appearance) Color { return r("surface") },
	},
	{
		ID: "surface-chrome",
		Description: "The plane window chrome sits on: the nav rail, its " +
			"title bar, and the resize seam that carries the divider. " +
			"Recessed or lifted relative to the page is the theme's choice — " +
			"what matters is that chrome reads as frame rather than content. " +
			"Flat with the page when unset. Setting it moves every mark on " +
			"the rail with it, not just the fill: see [Planes](#planes).",
		Fill:   FillDerived,
		Opaque: true,
		CSSVar: "--den-surface-chrome",
		derive: func(r Resolver, _ Appearance) Color { return r("background") },
	},
	{
		ID: "surface-inset",
		Description: "The track a control is recessed into: a segmented " +
			"picker or an editor tab strip. The opposite direction from " +
			"`surface-elevated`. The chosen segment paints `selection` on " +
			"this track; unchosen labels sit on the track itself. Not a " +
			"plane: a track's contents keep their own color rather than " +
			"blending into it, so nothing here follows `--den-plane`. " +
			"Flat with the page when unset.",
		Fill:   FillDerived,
		CSSVar: "--den-surface-inset",
		derive: func(r Resolver, _ Appearance) Color { return r("background") },
	},
	{
		ID: "brand-field",
		Description: "The plate behind the product lockup bars. The product " +
			"black when unset. A palette that wants the bars on the chrome " +
			"sets this to that plane, or transparent. Gated against " +
			"`accent-signal` so the bars cannot vanish into it.",
		Fill:   FillDerived,
		CSSVar: "--den-brand-field",
		derive: func(Resolver, Appearance) Color { return BrandFieldDefault },
	},
	{
		ID:          "text-muted",
		Description: "Secondary ink: captions, metadata, placeholders.",
		Fill:        FillDerived,
		CSSVar:      "--den-text-muted",
		derive:      func(r Resolver, _ Appearance) Color { return mix(r("text"), 62, r("background")) },
	},
	{
		ID: "danger-text",
		Description: "Destructive and error copy on a surface, as opposed to " +
			"the fill. Derived from `danger`, darkened or lightened only as " +
			"far as the legibility floor requires.",
		Fill:   FillDerived,
		CSSVar: "--den-danger-text",
		derive: func(r Resolver, _ Appearance) Color {
			return legibleInk(r("danger"), r("surface"), r("background"), StatusInkMin)
		},
	},
	{
		ID:          "warning-text",
		Description: "Caution copy on a surface, as opposed to the fill.",
		Fill:        FillDerived,
		CSSVar:      "--den-warning-text",
		derive: func(r Resolver, _ Appearance) Color {
			return legibleInk(r("warning"), r("surface"), r("background"), StatusInkMin)
		},
	},
	{
		ID:          "status-positive-text",
		Description: "Success copy on a surface, as opposed to the fill.",
		Fill:        FillDerived,
		CSSVar:      "--den-status-positive-text",
		derive: func(r Resolver, _ Appearance) Color {
			return legibleInk(r("status-positive"), r("surface"), r("background"), StatusInkMin)
		},
	},
	{
		ID:          "border",
		Description: "Hairline rules and control outlines.",
		Fill:        FillDerived,
		CSSVar:      "--den-border",
		derive:      func(r Resolver, _ Appearance) Color { return mix(r("text"), 17, r("background")) },
	},
	{
		ID: "shadow",
		Description: "The ink elevation shadows are cast in. Body ink on " +
			"light; true black on dark.",
		Fill:   FillDerived,
		CSSVar: "--den-shadow",
		derive: func(r Resolver, appearance Appearance) Color {
			// A dark scheme casts shadows in true black, not its own ink.
			if appearance == AppearanceDark {
				return blackInk
			}
			return r("text")
		},
	},
	{
		ID:          "tint-hue",
		Description: "Neutral hover hue; the tint ramp derives from it.",
		Fill:        FillDerived,
		CSSVar:      "--den-tint-hue",
		derive:      func(r Resolver, _ Appearance) Color { return mix(r("text"), 45, r("background")) },
	},
	{
		ID: "surface-offset",
		Description: "Wash a seated block sits on — the project cluster, a " +
			"segmented track. One step off the page: down on light, up on dark.",
		Fill:   FillDerived,
		Opaque: true,
		CSSVar: "--den-surface-offset",
		derive: func(r Resolver, _ Appearance) Color {
			// Neutral hue over the page darkens on light, lifts on dark.
			return mix(r("tint-hue"), 9, r("background"))
		},
	},
	{
		ID: "surface-raised",
		Description: "Plane of something lifted out of a well — the chosen " +
			"segment in a track. Must read above surface-offset, not below it.",
		Fill:   FillDerived,
		Opaque: true,
		CSSVar: "--den-surface-raised",
		derive: func(r Resolver, appearance Appearance) Color {
			// Dark has no plane above the page, so the lift steps out further.
			if appearance == AppearanceDark {
				return mix(r("text"), 9, r("surface-offset"))
			}
			return r("background")
		},
	},
	{
		ID: "bevel-highlight",
		Description: "Lit edge of a seated block: the top of a raised seam and " +
			"the bottom of a well.",
		Fill:   FillBevel,
		CSSVar: "--den-bevel-highlight",
		derive: func(r Resolver, appearance Appearance) Color {
			// The page is lighter than the fill on light, darker on dark.
			if appearance == AppearanceDark {
				return mix(r("text"), 8, transparentInk)
			}
			return r("background")
		},
	},
	{
		ID: "bevel-shadow",
		Description: "Shaded edge of a seated block or a filled plate: under a " +
			"raised seam and the top of a well.",
		Fill:   FillBevel,
		CSSVar: "--den-bevel-shadow",
		derive: func(r Resolver, appearance Appearance) Color {
			// A single hard row needs more alpha than a blurred cast.
			if appearance == AppearanceDark {
				return mix(r("shadow"), 26, transparentInk)
			}
			return mix(r("shadow"), 13, transparentInk)
		},
	},
	{
		ID: "bevel-plate-highlight",
		Description: "Lit edge of a filled plate — a send button, a checked " +
			"box, a merge action. Lighter than the fill beneath it on every " +
			"palette, including a pale accent that takes dark label ink.",
		Fill:   FillBevel,
		CSSVar: "--den-bevel-plate-highlight",
		derive: func(_ Resolver, appearance Appearance) Color {
			// White, not the label ink: a pale accent takes dark labels.
			// Dark fills sit lower, so less of it already reads as a catch.
			if appearance == AppearanceDark {
				return mix(whiteInk, 20, transparentInk)
			}
			return mix(whiteInk, 32, transparentInk)
		},
	},
	{
		ID:          "on-accent",
		Description: "Label ink on an accent fill.",
		Fill:        FillDerived,
		CSSVar:      "--den-on-accent",
		derive:      func(r Resolver, _ Appearance) Color { return readableOn(r("accent"), r("background")) },
	},
	{
		ID:          "accent-warm",
		Description: "Secondary accent fill for call-to-action surfaces.",
		Fill:        FillDerived,
		CSSVar:      "--den-accent-warm",
		derive:      func(r Resolver, _ Appearance) Color { return r("accent") },
	},
	{
		ID: "on-accent-warm",
		Description: "Label ink on a warm accent fill. Its own decision " +
			"rather than `on-accent`'s: a palette is free to put the two " +
			"accents on opposite sides of the luminance line, and then no " +
			"single ink is readable on both.",
		Fill:   FillDerived,
		CSSVar: "--den-on-accent-warm",
		derive: func(r Resolver, _ Appearance) Color { return readableOn(r("accent-warm"), r("background")) },
	},
	{
		ID:          "accent-text",
		Description: "Accent-colored copy on a surface, not a fill.",
		Fill:        FillDerived,
		CSSVar:      "--den-accent-text",
		derive:      func(r Resolver, _ Appearance) Color { return r("accent") },
	},
	{
		ID: "accent-signal",
		Description: "Accent indicators, icons, and links. Also the product " +
			"lockup bars, which must stay visible on `brand-field`.",
		Fill:   FillDerived,
		CSSVar: "--den-accent-signal",
		derive: func(r Resolver, _ Appearance) Color { return r("accent") },
	},
	{
		ID: "accent-hover",
		Description: "The accent fill under the pointer. Deepens toward " +
			"`shadow` when unset, in both schemes: `on-accent` is a light " +
			"ink, so a hover that lifts the fill takes the label with it. " +
			"Gated against `on-accent`.",
		Fill:   FillDerived,
		CSSVar: "--den-accent-hover",
		derive: func(r Resolver, _ Appearance) Color { return mix(r("shadow"), 12, r("accent")) },
	},
	{
		ID: "accent-warm-hover",
		Description: "`accent-warm` under the pointer, on the same terms as " +
			"`accent-hover`.",
		Fill:   FillDerived,
		CSSVar: "--den-accent-warm-hover",
		derive: func(r Resolver, _ Appearance) Color { return mix(r("shadow"), 12, r("accent-warm")) },
	},
	{
		ID: "selection",
		Description: "Fill behind a selected row, rail item, or segment. " +
			"Opaque: it sits on the page or a track, so a theme names the " +
			"color it wants rather than a strength of the accent.",
		Fill:   FillDerived,
		CSSVar: "--den-selection",
		derive: func(r Resolver, _ Appearance) Color { return mix(r("accent-signal"), 10, r("background")) },
	},
	{
		ID: "selection-strong",
		Description: "Fill behind a selected text range. Heavier than a row " +
			"selection, which reads from its whole band; a text range has to " +
			"show its extent character by character.",
		Fill:   FillDerived,
		CSSVar: "--den-selection-strong",
		derive: func(r Resolver, _ Appearance) Color { return mix(r("accent-signal"), 32, r("background")) },
	},
	{
		ID: "caret",
		Description: "Text insertion caret in the editor and inputs. Body ink " +
			"when unset, which is what a browser does on its own; a theme that " +
			"wants the caret to read as its own mark sets it.",
		Fill:   FillDerived,
		CSSVar: "--den-caret",
		derive: func(r Resolver, _ Appearance) Color { return r("text") },
	},
	{
		ID:          "status-running",
		Description: "In-flight work; distinct from both accent and success.",
		Fill:        FillDerived,
		CSSVar:      "--den-status-running",
		derive:      func(r Resolver, _ Appearance) Color { return r("warning") },
	},
	{
		ID:          "draft-accent",
		Description: "Coordinator draft and orchestration rail.",
		Fill:        FillDerived,
		CSSVar:      "--den-draft-accent",
		derive:      func(r Resolver, _ Appearance) Color { return r("status-positive") },
	},
	{
		ID:          "draft-accent-text",
		Description: "Draft-rail copy, quieter than the rail itself.",
		Fill:        FillDerived,
		CSSVar:      "--den-draft-accent-text",
		derive:      func(r Resolver, _ Appearance) Color { return mix(r("draft-accent"), 34, r("text-muted")) },
	},
	{
		ID: "diff-add-hue",
		Description: "Hue for added lines. Separate from status-positive so a " +
			"theme can make diffs colorblind-safe without changing success.",
		Fill:   FillDerived,
		CSSVar: "--den-diff-add-hue",
		derive: func(r Resolver, _ Appearance) Color { return r("status-positive") },
	},
	{
		ID:          "diff-delete-hue",
		Description: "Hue for removed lines. Separate from danger, same reason.",
		Fill:        FillDerived,
		CSSVar:      "--den-diff-delete-hue",
		derive:      func(r Resolver, _ Appearance) Color { return r("danger") },
	},
	{
		ID:          "diff-add-line",
		Description: "Fill behind an added line.",
		Fill:        FillChange,
		CSSVar:      "--den-diff-add-line",
	},
	{
		ID: "diff-add-word",
		Description: "Fill behind the changed words of an added line, painted " +
			"over `diff-add-line`.",
		Fill:   FillChange,
		CSSVar: "--den-diff-add-word",
	},
	{
		ID:          "diff-delete-line",
		Description: "Fill behind a removed line.",
		Fill:        FillChange,
		CSSVar:      "--den-diff-delete-line",
	},
	{
		ID: "diff-delete-word",
		Description: "Fill behind the changed words of a removed line, painted " +
			"over `diff-delete-line`.",
		Fill:   FillChange,
		CSSVar: "--den-diff-delete-word",
	},
	{
		ID:          "cost-coordinator",
		Description: "Cost series: coordinator share.",
		Fill:        FillDerived,
		CSSVar:      "--den-cost-coordinator",
		derive:      func(r Resolver, _ Appearance) Color { return r("warning") },
	},
	{
		ID:          "cost-workers",
		Description: "Cost series: worker share.",
		Fill:        FillDerived,
		CSSVar:      "--den-cost-workers",
		derive:      func(r Resolver, _ Appearance) Color { return r("accent-signal") },
	},
	{
		ID:          "cost-summarizer",
		Description: "Cost series: summarizer share.",
		Fill:        FillDerived,
		CSSVar:      "--den-cost-summarizer",
		derive:      func(r Resolver, _ Appearance) Color { return r("status-positive") },
	},
	{
		ID: "identity-1",
		Description: "Project identity hue 1. The identity set exists to make " +
			"projects distinguishable at a glance, so the hues stay far apart.",
		Fill:   FillDerived,
		CSSVar: "--den-identity-1",
		derive: func(r Resolver, _ Appearance) Color { return r("accent") },
	},
	{
		ID:          "identity-2",
		Description: "Project identity hue 2.",
		Fill:        FillDerived,
		CSSVar:      "--den-identity-2",
		derive:      func(r Resolver, _ Appearance) Color { return r("cost-workers") },
	},
	{
		ID:          "identity-3",
		Description: "Project identity hue 3.",
		Fill:        FillDerived,
		CSSVar:      "--den-identity-3",
		derive:      func(r Resolver, _ Appearance) Color { return r("status-positive") },
	},
	{
		ID:          "identity-4",
		Description: "Project identity hue 4.",
		Fill:        FillDerived,
		CSSVar:      "--den-identity-4",
		derive:      func(r Resolver, _ Appearance) Color { return mix(r("cost-workers"), 50, r("danger")) },
	},
	{
		ID:          "identity-5",
		Description: "Project identity hue 5.",
		Fill:        FillDerived,
		CSSVar:      "--den-identity-5",
		derive:      func(r Resolver, _ Appearance) Color { return r("cost-coordinator") },
	},
}

// readableOn picks the stronger stock ink for a fill.
func readableOn(fill, backdrop Color) Color {
	dark := Color{R: 0x14, G: 0x13, B: 0x12, A: 0xff}
	if contrastRatio(whiteInk, fill, backdrop) >= contrastRatio(dark, fill, backdrop) {
		return whiteInk
	}
	return dark
}

var baseByID = func() map[string]Token {
	out := make(map[string]Token, len(baseTokens))
	for _, tok := range baseTokens {
		out[tok.ID] = tok
	}
	return out
}()

// BaseTokens returns the vocabulary in resolution order.
func BaseTokens() []Token {
	return append([]Token(nil), baseTokens...)
}

// BaseToken looks one up by id.
func BaseToken(id string) (Token, bool) {
	tok, ok := baseByID[id]
	return tok, ok
}

// Appearance is the scheme a theme implements.
type Appearance string

const (
	AppearanceLight Appearance = "light"
	AppearanceDark  Appearance = "dark"
)

var appearances = map[Appearance]bool{AppearanceLight: true, AppearanceDark: true}

// ValidAppearance reports whether the value is in the closed enum.
func ValidAppearance(a Appearance) bool { return appearances[a] }

// Appearances returns the closed enum, sorted.
func Appearances() []Appearance {
	out := make([]Appearance, 0, len(appearances))
	for a := range appearances {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
