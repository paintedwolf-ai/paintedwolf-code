package theme

import "fmt"

// ContrastPair is one gated foreground/background relationship.
type ContrastPair struct {
	Foreground string
	Background string
	// Min is the required contrast ratio.
	Min float64
	// Why reaches the author's diagnostic.
	Why string
}

// StatusInkMin is the status-ink contrast floor.
const StatusInkMin = 3.0

// SelectionInkMin is the floor for body ink over a selection band.
const SelectionInkMin = 3.0

// LockupMarkMin is the product-mark contrast floor.
const LockupMarkMin = 3.0

// legibilityFloor is the closed gated pair set.
var legibilityFloor = []ContrastPair{
	{
		Foreground: "text", Background: "background", Min: 4.5,
		Why: "body text must stay readable",
	},
	{
		Foreground: "text", Background: "surface", Min: 4.5,
		Why: "body text on panels must stay readable",
	},
	{
		Foreground: "text-muted", Background: "background", Min: 3,
		Why: "secondary text must stay readable",
	},
	// Chrome surfaces have independent text contrast checks.
	{
		Foreground: "text", Background: "surface-chrome", Min: 4.5,
		Why: "rail and title-bar labels must stay readable",
	},
	{
		Foreground: "text-muted", Background: "surface-chrome", Min: 3,
		Why: "rail section headings must stay readable",
	},
	{
		Foreground: "text-muted", Background: "surface-inset", Min: 3,
		Why: "an unchosen segment's label must stay readable",
	},
	// Danger's hue is gated as well as its ink.
	{
		Foreground: "danger", Background: "surface", Min: 3,
		Why: "destructive states must stay visible",
	},
	{
		Foreground: "danger-text", Background: "surface", Min: StatusInkMin,
		Why: "destructive states must stay readable",
	},
	{
		Foreground: "warning-text", Background: "surface", Min: StatusInkMin,
		Why: "caution states must stay readable",
	},
	{
		Foreground: "status-positive-text", Background: "surface", Min: StatusInkMin,
		Why: "success states must stay readable",
	},
	// Selection bands use a 3:1 text contrast floor.
	{
		Foreground: "text", Background: "selection", Min: SelectionInkMin,
		Why: "a selected row or segment label must stay readable",
	},
	{
		Foreground: "text", Background: "selection-strong", Min: SelectionInkMin,
		Why: "selected text must stay readable",
	},
	{
		Foreground: "on-accent", Background: "accent", Min: 4.5,
		Why: "labels on accent fills must stay readable",
	},
	// Hover fills receive their own label contrast check.
	{
		Foreground: "on-accent", Background: "accent-hover", Min: 4.5,
		Why: "labels must stay readable while the pointer is on them",
	},
	{
		Foreground: "on-accent-warm", Background: "accent-warm", Min: 4.5,
		Why: "labels on warm accent fills must stay readable",
	},
	{
		Foreground: "on-accent-warm", Background: "accent-warm-hover", Min: 4.5,
		Why: "labels must stay readable while the pointer is on them",
	},
	{
		Foreground: "accent-signal", Background: "brand-field", Min: LockupMarkMin,
		Why: "the product lockup bars must stay visible on their plate",
	},
	{
		Foreground: "diff-add-hue", Background: "surface", Min: 3,
		Why: "added lines must be distinguishable from the page",
	},
	{
		Foreground: "diff-delete-hue", Background: "surface", Min: 3,
		Why: "removed lines must be distinguishable from the page",
	},
}

// LegibilityFloor returns the gated pairs.
func LegibilityFloor() []ContrastPair {
	return append([]ContrastPair(nil), legibilityFloor...)
}

// Measurement is one pair's measured ratio, for the advisory report.
type Measurement struct {
	Pair  ContrastPair
	Ratio float64
	// Passes is false when a gated pair is under its minimum.
	Passes bool
}

// CheckLegibility measures the floor against a compiled theme.
func CheckLegibility(compiled Compiled) []Fault {
	var faults []Fault
	for _, m := range MeasureLegibility(compiled) {
		if m.Passes {
			continue
		}
		faults = append(faults, Fault{
			Field: "tokens." + m.Pair.Foreground,
			Message: fmt.Sprintf(
				"contrast with %s is %.2f:1, below the %.1f:1 floor — %s",
				m.Pair.Background, m.Ratio, m.Pair.Min, m.Pair.Why),
		})
	}
	return append(faults, checkChangeFills(compiled)...)
}

// MeasureLegibility measures rendered pairs over the page background.
func MeasureLegibility(compiled Compiled) []Measurement {
	backdrop := compiled.Tokens["background"]
	out := make([]Measurement, 0, len(legibilityFloor))
	for _, pair := range legibilityFloor {
		fg, okFG := compiled.Tokens[pair.Foreground]
		bg, okBG := compiled.Tokens[pair.Background]
		if !okFG || !okBG {
			continue
		}
		ratio := contrastRatio(fg, bg, backdrop)
		out = append(out, Measurement{
			Pair:   pair,
			Ratio:  ratio,
			Passes: ratio >= pair.Min,
		})
	}
	return out
}
