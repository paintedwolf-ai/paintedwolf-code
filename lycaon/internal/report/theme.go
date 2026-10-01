package report

import "github.com/johnfercher/maroto/v2/pkg/props"

// Design tokens for the printed report. The PDF is not a themed surface, so
// the Daylight palette is resolved here once, for white paper.

// Page geometry, in millimetres. The margins give a 162mm measure — about 83
// characters at body size.
const (
	pageWidthMM  = 210.0
	pageHeightMM = 297.0

	marginLeft   = 24.0
	marginRight  = 24.0
	marginTop    = 18.0
	marginBottom = 16.0

	contentWidth = pageWidthMM - marginLeft - marginRight

	// A 24-column grid, finer than maroto's default 12, so the list gutter
	// lands on it. Table columns do not use the grid — they are positioned in
	// millimetres inside one full-width column (see cellSpec).
	gridSize = 24
	gridUnit = contentWidth / gridSize

	// The running footer sits in the bottom margin, drawn by the page-number
	// facility; it costs the content area nothing. Blocks taller than
	// usableHeight cannot be kept together on one page.
	footerHeight = 12.0
	usableHeight = pageHeightMM - marginTop - marginBottom - footerHeight
)

// Type ramp, in points. One ramp serves both host sections and markdown
// headings, so a synthesis heading cannot outrank the section holding it.
const (
	sizeAnswer     = 24.0
	sizeGaugeLevel = 13.0
	sizeAsk        = 12.0
	sizeSection    = 14.0
	sizeSubsection = 11.5
	sizeMinorHead  = 10.5
	sizeBody       = 10.5
	sizeMeta       = 8.5
	sizeTableHead  = 8.0
	sizeTableCell  = 8.5
	sizeCode       = 8.5
	sizeChip       = 7.8
	sizeEyebrow    = 8.0
)

const ptToMM = 25.4 / 72.0

// Extra space between baselines, as a fraction of the font size. The renderer
// sets baselines exactly one font height apart, so without this every block
// sets solid. Table lines are short and take the tighter ratio.
const (
	leadingRatio      = 0.34
	tableLeadingRatio = 0.22
)

// descenderRatio is how far below the baseline a glyph reaches, as a fraction
// of the font size. Every row reserves this much beneath its text, or the next
// row's background paints over the descenders.
const descenderRatio = 0.24

func leading(sizePt float64) float64 { return sizePt * ptToMM * leadingRatio }

func tableLeading(sizePt float64) float64 { return sizePt * ptToMM * tableLeadingRatio }

// Vertical rhythm, in millimetres. A heading sets the space above it and a
// paragraph the space below it, so adjacent blocks never double up.
const (
	spaceAboveSection    = 7.0
	spaceBelowSection    = 2.6
	spaceAboveSubsection = 5.0
	spaceBelowSubsection = 1.8
	spaceAfterParagraph  = 2.4
	spaceAfterList       = 2.4
	spaceBetweenListItem = 0.8
	spaceBetweenMetaRow  = 1.0
	spaceAroundCode      = 2.0
	spaceAroundRule      = 3.5
	spaceAroundPanel     = 2.6

	// Space a table record opens above itself, so rows read as bands rather
	// than as lines of one block.
	spaceAboveRecord = 1.8

	// Headings below the second level take a fraction of the subsection space.
	minorHeadSpaceRatio = 0.7
)

// Indentation, in millimetres.
const (
	listIndent    = 5.6
	maxListDepth  = 3
	codeIndent    = 3.0
	quoteIndent   = 4.5
	panelIndent   = 4.0
	tableCellPadX = 1.6
	tableCellPadY = 1.4
)

// Chip geometry, in millimetres. See chip.go for how the ground is drawn.
const (
	chipPadX   = 1.6
	chipPadY   = 0.9
	chipGap    = 2.0
	chipMinWid = 13.0
)

// Palette, resolved from the Daylight theme for white paper.
var (
	inkColor    = &props.Color{Red: 0x1c, Green: 0x1b, Blue: 0x1a}
	mutedColor  = &props.Color{Red: 0x6e, Green: 0x6c, Blue: 0x67}
	ruleColor   = &props.Color{Red: 0xd3, Green: 0xd2, Blue: 0xcf}
	accentColor = &props.Color{Red: 0x9d, Green: 0x4e, Blue: 0x2b}

	// hairlineColor separates rows inside a table. A record boundary is a
	// lighter statement than a section rule, or a long table reads as a grid.
	hairlineColor = &props.Color{Red: 0xe7, Green: 0xe5, Blue: 0xe2}

	// The report's fills: a code panel's ground, a boxed panel's ground, and
	// the chips'. Every other structure reads from hairlines and spacing.
	surfaceFill = &props.Color{Red: 0xf6, Green: 0xf5, Blue: 0xf3}
	panelFill   = &props.Color{Red: 0xf9, Green: 0xf8, Blue: 0xf6}
	chipFill    = &props.Color{Red: 0xe9, Green: 0xe7, Blue: 0xe3}
)

// chipTone is a chip's ground and ink. Severity tones are keyed by rank, so a
// scanner that grades "warning" and one that grades "medium" take the same
// tone; the words stay the scanner's own.
type chipTone struct {
	fill *props.Color
	ink  *props.Color
}

var (
	toneNeutral  = chipTone{fill: chipFill, ink: inkColor}
	toneAccent   = chipTone{fill: &props.Color{Red: 0xf3, Green: 0xe4, Blue: 0xdc}, ink: &props.Color{Red: 0x86, Green: 0x41, Blue: 0x22}}
	toneCritical = chipTone{fill: &props.Color{Red: 0xf8, Green: 0xde, Blue: 0xdc}, ink: &props.Color{Red: 0x8c, Green: 0x1d, Blue: 0x18}}
	toneHigh     = chipTone{fill: &props.Color{Red: 0xfb, Green: 0xe5, Blue: 0xd6}, ink: &props.Color{Red: 0x96, Green: 0x38, Blue: 0x0f}}
	toneMedium   = chipTone{fill: &props.Color{Red: 0xfa, Green: 0xef, Blue: 0xcb}, ink: &props.Color{Red: 0x7a, Green: 0x52, Blue: 0x00}}
	toneLow      = chipTone{fill: &props.Color{Red: 0xdf, Green: 0xe9, Blue: 0xf7}, ink: &props.Color{Red: 0x24, Green: 0x5a, Blue: 0x9e}}
	toneInfo     = chipTone{fill: &props.Color{Red: 0xe7, Green: 0xea, Blue: 0xee}, ink: &props.Color{Red: 0x4a, Green: 0x58, Blue: 0x66}}
	toneGood     = chipTone{fill: &props.Color{Red: 0xdf, Green: 0xee, Blue: 0xe2}, ink: &props.Color{Red: 0x2a, Green: 0x62, Blue: 0x3d}}
	// toneInk is the plain scale: ink on the chip ground.
	toneInk = chipTone{fill: chipFill, ink: inkColor}
)

// briefTone maps a level's declared tone to its ground and ink. An undeclared
// tone takes plain ink.
func briefTone(tone string) chipTone {
	switch tone {
	case "critical":
		return toneCritical
	case "high":
		return toneHigh
	case "medium":
		return toneMedium
	case "low":
		return toneLow
	case "good":
		return toneGood
	default:
		return toneInk
	}
}

// severityTone maps a severity label's rank to its tone. Unranked labels take
// the neutral chip: the renderer does not guess what a word it has never seen
// means.
func severityTone(level string) chipTone {
	switch SeverityRank(level) {
	case 0:
		return toneCritical
	case 1:
		return toneHigh
	case 2:
		return toneMedium
	case 3:
		return toneLow
	case 4:
		return toneInfo
	default:
		return toneNeutral
	}
}

// ruleThin is the hairline weight, in millimetres.
const ruleThin = 0.2
