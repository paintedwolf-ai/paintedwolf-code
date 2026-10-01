package report

import (
	"strings"

	"github.com/johnfercher/go-tree/node"
	"github.com/johnfercher/maroto/v2/pkg/consts/align"
	"github.com/johnfercher/maroto/v2/pkg/consts/fontstyle"
	"github.com/johnfercher/maroto/v2/pkg/core"
	"github.com/johnfercher/maroto/v2/pkg/core/entity"
	"github.com/johnfercher/maroto/v2/pkg/props"
)

// fragmentSlack is the millimetre of headroom each drawn fragment gets, so the
// renderer takes its single-line path rather than re-wrapping text already
// broken here.
const fragmentSlack = 0.5

// inlineRun is one styled slice of a text block. Bold and Italic add to the
// block's base style rather than replacing it, so emphasis inside a heading
// stays bold.
type inlineRun struct {
	Text   string
	Bold   bool
	Italic bool
	Family string
	Color  *props.Color
	// Link makes the run a clickable URI annotation.
	Link string
}

// fragment is a contiguous piece of one run placed on one line.
type fragment struct {
	text  string
	run   int
	x     float64
	width float64
}

// textLine is one laid-out line of fragments.
type textLine struct {
	frags []fragment
	width float64
}

// inlineText draws styled runs as flowed text. Lines are broken at
// construction against a width the caller supplies, so GetHeight answers without
// a provider and pagination is decided up front.
type inlineText struct {
	runs  []inlineRun
	lines []textLine
	prop  props.Text
	base  styleKey
	ms    *measurer
}

// newInlineText lays runs out into width millimetres of text column. width is
// the space available to the text itself; the caller has already subtracted
// any Left and Right padding carried on prop.
func newInlineText(ms *measurer, runs []inlineRun, prop props.Text, width float64) *inlineText {
	t := &inlineText{
		runs: runs,
		prop: prop,
		ms:   ms,
		base: styleKey{family: prop.Family, style: prop.Style, size: prop.Size},
	}
	t.lines = t.layout(width)
	return t
}

// lineCount is the number of laid-out lines, never less than one.
func (t *inlineText) lineCount() int {
	if len(t.lines) == 0 {
		return 1
	}
	return len(t.lines)
}

func (t *inlineText) height() float64 {
	n := float64(t.lineCount())
	return n*t.ms.fontHeight(t.prop.Size) + (n-1)*t.prop.VerticalPadding + t.prop.Top + t.prop.Bottom
}

// splitLines returns one single-line component per laid-out line, so a page
// break can land between any two lines and no row is ever taller than a page.
//
// VerticalPadding opens space inside a block, so it has to become each row's
// own Bottom once every line is a row, or the block sets solid. Bottom also
// never drops below a descender's clearance.
func (t *inlineText) splitLines() []*inlineText {
	out := make([]*inlineText, 0, t.lineCount())
	last := len(t.lines) - 1
	minBottom := t.prop.Size * ptToMM * descenderRatio

	for i, ln := range t.lines {
		prop := t.prop
		prop.Top, prop.Bottom = 0, 0
		if i == 0 {
			prop.Top = t.prop.Top
		}
		if i == last {
			prop.Bottom = t.prop.Bottom
		} else {
			prop.Bottom = t.prop.VerticalPadding
		}
		if prop.Bottom < minBottom {
			prop.Bottom = minBottom
		}
		out = append(out, &inlineText{
			runs:  t.runs,
			lines: []textLine{ln},
			prop:  prop,
			base:  t.base,
			ms:    t.ms,
		})
	}
	if len(out) == 0 {
		out = append(out, t)
	}
	return out
}

func (t *inlineText) runKey(i int) styleKey {
	r := t.runs[i]
	k := t.base
	if r.Family != "" {
		k.family = r.Family
	}
	k.style = combineStyle(t.base.style, r.Bold, r.Italic)
	return k
}

// combineStyle folds a run's emphasis into the block's base style.
func combineStyle(base fontstyle.Type, bold, italic bool) fontstyle.Type {
	b := bold || strings.Contains(string(base), string(fontstyle.Bold))
	i := italic || strings.Contains(string(base), string(fontstyle.Italic))
	var out fontstyle.Type
	if b {
		out += fontstyle.Bold
	}
	if i {
		out += fontstyle.Italic
	}
	return out
}

// wordPiece is one styled slice of a single word.
type wordPiece struct {
	text string
	run  int
}

// wordGroup is a set of pieces with no space between them. It wraps as a unit,
// so a styled span never splits mid-word.
type wordGroup struct {
	pieces []wordPiece
	spaced bool
	width  float64
}

// words splits the runs into wrappable groups, carrying style across the
// boundary so `a**b**c` stays one word.
func (t *inlineText) words() []wordGroup {
	var out []wordGroup
	var cur wordGroup
	open, pending := false, false

	flush := func() {
		if open {
			out = append(out, cur)
			cur = wordGroup{}
			open = false
		}
	}

	for i, r := range t.runs {
		fields := strings.Split(r.Text, " ")
		for j, f := range fields {
			if j > 0 {
				flush()
				pending = true
			}
			if f == "" {
				continue
			}
			if !open {
				cur = wordGroup{spaced: pending && len(out) > 0}
				open, pending = true, false
			}
			cur.pieces = append(cur.pieces, wordPiece{text: f, run: i})
			cur.width += t.ms.width(f, t.runKey(i))
		}
	}
	flush()
	return out
}

// breakAfter lists the characters an over-long token may wrap after. Paths,
// rule ids, evidence handles, and URLs all carry at least one.
const breakAfter = "/-_.:,;=&?+@"

// splitAtSeams divides an oversized group after separator characters, keeping
// each piece's style. Returns nil when the group holds no seam to break on.
func (t *inlineText) splitAtSeams(w wordGroup) []wordGroup {
	var out []wordGroup
	var cur wordGroup

	closeGroup := func() {
		if len(cur.pieces) > 0 {
			out = append(out, cur)
			cur = wordGroup{}
		}
	}

	for _, p := range w.pieces {
		key := t.runKey(p.run)
		start := 0
		for i, ch := range p.text {
			if !strings.ContainsRune(breakAfter, ch) {
				continue
			}
			seg := p.text[start : i+len(string(ch))]
			cur.pieces = append(cur.pieces, wordPiece{text: seg, run: p.run})
			cur.width += t.ms.width(seg, key)
			closeGroup()
			start = i + len(string(ch))
		}
		if start < len(p.text) {
			seg := p.text[start:]
			cur.pieces = append(cur.pieces, wordPiece{text: seg, run: p.run})
			cur.width += t.ms.width(seg, key)
		}
	}
	closeGroup()

	if len(out) <= 1 {
		return nil
	}
	return out
}

// lineFiller accumulates fragments into lines of at most width millimetres.
type lineFiller struct {
	t     *inlineText
	width float64
	lines []textLine
	cur   textLine
	x     float64
}

func (f *lineFiller) push() {
	f.cur.width = f.x
	f.lines = append(f.lines, f.cur)
	f.cur = textLine{}
	f.x = 0
}

// addWord places a group that is known to fit on an empty line.
func (f *lineFiller) addWord(w wordGroup, lead float64) {
	f.x += lead
	for _, p := range w.pieces {
		width := f.t.ms.width(p.text, f.t.runKey(p.run))
		f.cur.frags = append(f.cur.frags, fragment{text: p.text, run: p.run, x: f.x, width: width})
		f.x += width
	}
}

// addWide places a group wider than the whole column: at its seams first, then
// between characters for any piece still too wide.
func (f *lineFiller) addWide(w wordGroup) {
	parts := f.t.splitAtSeams(w)
	if parts == nil {
		f.addOversized(w)
		return
	}
	for _, part := range parts {
		if len(f.cur.frags) > 0 && f.x+part.width > f.width {
			f.push()
		}
		if part.width > f.width {
			f.addOversized(part)
			continue
		}
		f.addWord(part, 0)
	}
}

// addOversized places a group between character boundaries, the last resort
// when no seam yields a narrow enough piece.
func (f *lineFiller) addOversized(w wordGroup) {
	for _, p := range w.pieces {
		key := f.t.runKey(p.run)
		var buf strings.Builder
		start := f.x

		emit := func() {
			if buf.Len() == 0 {
				return
			}
			s := buf.String()
			f.cur.frags = append(f.cur.frags, fragment{
				text: s, run: p.run, x: start, width: f.t.ms.width(s, key),
			})
			buf.Reset()
		}

		for _, ch := range p.text {
			cw := f.t.ms.width(string(ch), key)
			if f.x+cw > f.width && (len(f.cur.frags) > 0 || buf.Len() > 0) {
				emit()
				f.push()
			}
			if buf.Len() == 0 {
				start = f.x
			}
			buf.WriteRune(ch)
			f.x += cw
		}
		emit()
	}
}

func (t *inlineText) layout(width float64) []textLine {
	if width <= 0 {
		width = gridUnit
	}
	words := t.words()
	if len(words) == 0 {
		return nil
	}

	f := &lineFiller{t: t, width: width}
	spaceW := t.ms.spaceWidth(t.base)

	for _, w := range words {
		lead := 0.0
		if len(f.cur.frags) > 0 && w.spaced {
			lead = spaceW
		}
		if len(f.cur.frags) > 0 && f.x+lead+w.width > width {
			f.push()
			lead = 0
		}
		if w.width > width {
			f.addWide(w)
			continue
		}
		f.addWord(w, lead)
	}
	if len(f.cur.frags) > 0 || len(f.lines) == 0 {
		f.push()
	}
	return f.lines
}

// SetConfig is empty: every prop this component reads is set at
// construction, so document defaults must not reach the finished layout.
func (t *inlineText) SetConfig(*entity.Config) {}

func (t *inlineText) GetHeight(core.Provider, *entity.Cell) float64 { return t.height() }

func (t *inlineText) GetStructure() *node.Node[core.Structure] {
	return node.New(core.Structure{Type: "inline_text", Value: t.plain()})
}

// plain is the block's text with styling dropped.
func (t *inlineText) plain() string {
	var b strings.Builder
	for _, r := range t.runs {
		b.WriteString(r.Text)
	}
	return b.String()
}

func (t *inlineText) Render(provider core.Provider, cell *entity.Cell) {
	fh := t.ms.fontHeight(t.prop.Size)
	pitch := fh + t.prop.VerticalPadding

	for i, ln := range t.lines {
		dy := t.prop.Top + float64(i)*pitch
		for _, fr := range ln.frags {
			key := t.runKey(fr.run)
			prop := t.prop
			prop.Top, prop.Bottom, prop.Left, prop.Right = 0, 0, 0, 0
			prop.VerticalPadding = 0
			prop.Align = align.Left
			prop.Family = key.family
			prop.Style = key.style
			if c := t.runs[fr.run].Color; c != nil {
				prop.Color = c
			}
			if link := t.runs[fr.run].Link; link != "" {
				prop.Hyperlink = &link
			}
			provider.AddText(fr.text, &entity.Cell{
				X:      cell.X + t.prop.Left + fr.x,
				Y:      cell.Y + dy,
				Width:  fr.width + fragmentSlack,
				Height: fh,
			}, &prop)
		}
	}
}
