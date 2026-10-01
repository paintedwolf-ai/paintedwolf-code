package report

import (
	"strconv"
	"strings"

	"github.com/johnfercher/maroto/v2/pkg/components/col"
	"github.com/johnfercher/maroto/v2/pkg/components/row"
	"github.com/johnfercher/maroto/v2/pkg/consts/fontstyle"
	"github.com/johnfercher/maroto/v2/pkg/props"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	gmtext "github.com/yuin/goldmark/text"
)

// mdToBlocks parses CommonMark (plus GFM tables) and lays it out. Unknown
// block nodes degrade to their text content rather than disappearing.
func mdToBlocks(ms *measurer, md string) []block {
	if strings.TrimSpace(md) == "" {
		return nil
	}
	source := []byte(md)
	parser := goldmark.New(goldmark.WithExtensions(extension.Table)).Parser()
	doc := parser.Parse(gmtext.NewReader(source))

	r := &mdRenderer{ms: ms, source: source}
	return r.children(doc, 0)
}

type mdRenderer struct {
	ms     *measurer
	source []byte
}

func (r *mdRenderer) children(parent ast.Node, depth int) []block {
	var out []block
	for n := parent.FirstChild(); n != nil; n = n.NextSibling() {
		out = append(out, r.node(n, depth)...)
	}
	return out
}

func (r *mdRenderer) node(n ast.Node, depth int) []block {
	switch t := n.(type) {
	case *ast.Heading:
		return r.heading(t)
	case *ast.Paragraph:
		return r.paragraph(t, depth)
	case *ast.TextBlock:
		return r.paragraph(t, depth)
	case *ast.List:
		return r.list(t, depth)
	case *ast.FencedCodeBlock:
		return r.code(t)
	case *ast.CodeBlock:
		return r.code(t)
	case *ast.Blockquote:
		return r.quote(t, depth)
	case *ast.ThematicBreak:
		return []block{rowsBlock(ruleRow(ruleThin, ruleColor, spaceAroundRule))}
	case *east.Table:
		return r.table(t)
	default:
		if n.Type() == ast.TypeBlock {
			return r.paragraph(n, depth)
		}
		return nil
	}
}

// headingProps returns the type treatment for a heading level. Host sections
// call it too, at level 1, so the two ramps cannot drift apart.
func headingProps(level int) (size, above, below float64, rule bool) {
	switch level {
	case 1:
		return sizeSection, spaceAboveSection, spaceBelowSection, true
	case 2:
		return sizeSubsection, spaceAboveSubsection, spaceBelowSubsection, false
	default:
		return sizeMinorHead, spaceAboveSubsection * minorHeadSpaceRatio, spaceBelowSubsection, false
	}
}

func (r *mdRenderer) heading(n *ast.Heading) []block {
	segs := r.runs(n)
	if len(segs) == 0 {
		return nil
	}
	size, above, below, rule := headingProps(n.Level)
	prop := props.Text{
		Family:          familySans,
		Style:           fontstyle.Bold,
		Size:            size,
		Color:           inkColor,
		VerticalPadding: leading(size),
		Top:             above,
	}
	if !rule {
		prop.Bottom = below
	}

	rows := textRows(r.ms, flattenSegments(segs), prop)
	if rule {
		rows = append(rows, ruleRow(ruleThin, ruleColor, below))
	}
	b := keepWithNextBlock(rows...)
	// The synthesis's own top two levels are what a reader navigates by, at
	// the same rank as the host's sections.
	if n.Level <= 2 {
		b.contents = &contentsEntry{title: plainSegments(segs), level: n.Level}
	}
	return []block{b}
}

func (r *mdRenderer) paragraph(n ast.Node, depth int) []block {
	segs := r.runs(n)
	if len(segs) == 0 {
		return nil
	}
	indent := float64(depth) * listIndent
	var rows []measuredRow
	for i, seg := range segs {
		prop := props.Text{
			Family:          familySans,
			Size:            sizeBody,
			Color:           inkColor,
			VerticalPadding: leading(sizeBody),
			Left:            indent,
		}
		if i == len(segs)-1 {
			prop.Bottom = spaceAfterParagraph
		}
		rows = append(rows, textRows(r.ms, seg, prop)...)
	}
	return proseBlocks(rows)
}

// proseBlocks groups laid-out lines so a page break never strands a single
// line of a paragraph at the top or bottom of a page.
func proseBlocks(rows []measuredRow) []block {
	n := len(rows)
	if n == 0 {
		return nil
	}
	if n <= 3 {
		return []block{rowsBlock(rows...)}
	}
	out := []block{rowsBlock(rows[0], rows[1])}
	for _, r := range rows[2 : n-2] {
		out = append(out, rowsBlock(r))
	}
	return append(out, rowsBlock(rows[n-2], rows[n-1]))
}

func (r *mdRenderer) list(n *ast.List, depth int) []block {
	if depth >= maxListDepth {
		depth = maxListDepth - 1
	}
	markerSpan := 1 + depth
	contentSpan := gridSize - markerSpan
	number := n.Start
	if number == 0 {
		number = 1
	}

	var out []block
	for item := n.FirstChild(); item != nil; item = item.NextSibling() {
		li, ok := item.(*ast.ListItem)
		if !ok {
			continue
		}
		marker := "•"
		if n.IsOrdered() {
			marker = strconv.Itoa(number) + "."
			number++
		}
		out = append(out, r.listItem(li, marker, markerSpan, contentSpan, depth)...)
	}
	if len(out) > 0 {
		out = append(out, rowsBlock(spacerRow(spaceAfterList-spaceBetweenListItem)))
	}
	return out
}

// listItem puts the marker beside the item's first line only; later lines
// leave the marker column empty so wrapped text hangs under text, not bullet.
func (r *mdRenderer) listItem(li *ast.ListItem, marker string, markerSpan, contentSpan, depth int) []block {
	var body ast.Node
	for c := li.FirstChild(); c != nil; c = c.NextSibling() {
		if _, ok := c.(*ast.List); ok {
			continue
		}
		body = c
		break
	}

	var out []block
	if body != nil {
		segs := r.runs(body)
		if len(segs) > 0 {
			out = append(out, r.itemRows(flattenSegments(segs), marker, markerSpan, contentSpan, depth))
		}
	}

	for c := li.FirstChild(); c != nil; c = c.NextSibling() {
		if nested, ok := c.(*ast.List); ok {
			out = append(out, r.list(nested, depth+1)...)
		}
	}
	return out
}

func (r *mdRenderer) itemRows(runs []inlineRun, marker string, markerSpan, contentSpan, depth int) block {
	contentProp := props.Text{
		Family:          familySans,
		Size:            sizeBody,
		Color:           inkColor,
		VerticalPadding: leading(sizeBody),
		Bottom:          spaceBetweenListItem,
	}
	// The marker column supplies the gutter, so content starts flush in its own
	// column and every wrapped line lands on that same left edge.
	lines := newInlineText(r.ms, runs, contentProp, float64(contentSpan)*gridUnit).splitLines()

	markerProp := props.Text{
		Family:          familySans,
		Size:            sizeBody,
		Color:           mutedColor,
		VerticalPadding: leading(sizeBody),
		Left:            float64(depth) * listIndent,
	}

	rows := make([]measuredRow, 0, len(lines))
	for i, ln := range lines {
		height := ln.height()
		rr := row.New(height)
		if i == 0 {
			rr.Add(col.New(markerSpan).Add(newInlineText(
				r.ms,
				[]inlineRun{{Text: marker}},
				markerProp,
				float64(markerSpan)*gridUnit-markerProp.Left,
			)))
		} else {
			rr.Add(col.New(markerSpan))
		}
		rr.Add(col.New(contentSpan).Add(ln))
		rows = append(rows, measuredRow{row: rr, height: height})
	}
	return rowsBlock(rows...)
}

func (r *mdRenderer) code(n ast.Node) []block {
	lines := n.Lines()
	var text []string
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		text = append(text, string(seg.Value(r.source)))
	}
	joined := strings.TrimRight(strings.Join(text, ""), "\n")
	if strings.TrimSpace(joined) == "" {
		return nil
	}

	// Each source line becomes its own row, so Bottom — not VerticalPadding —
	// is what opens the space between them.
	prop := props.Text{
		Family:          familyMono,
		Size:            sizeCode,
		Color:           inkColor,
		VerticalPadding: leading(sizeCode),
		Left:            codeIndent,
		Right:           codeIndent,
		Bottom:          leading(sizeCode),
	}
	fill := &props.Cell{BackgroundColor: surfaceFill}

	rows := []measuredRow{fillRow(spaceAroundCode, surfaceFill)}
	for _, ln := range strings.Split(joined, "\n") {
		text := newInlineText(r.ms, []inlineRun{{Text: preserveIndent(ln)}}, prop, contentWidth-2*codeIndent)
		for _, one := range text.splitLines() {
			h := one.height()
			rr := row.New(h).Add(col.New(gridSize).Add(one))
			rr.WithStyle(fill)
			rows = append(rows, measuredRow{row: rr, height: h})
		}
	}
	rows = append(rows, fillRow(spaceAroundCode, surfaceFill), spacerRow(spaceAfterParagraph))
	return []block{rowsBlock(rows...)}
}

// preserveIndent keeps a code line's indentation through layout. The line
// breaker treats a space as a place to wrap and folds runs of them, which is
// right for prose and wrong for code; leading whitespace and aligned runs
// become no-break spaces, which measure and draw as spaces but never fold.
func preserveIndent(line string) string {
	const nbsp = " "
	line = strings.ReplaceAll(line, "\t", "    ")
	lead := 0
	for lead < len(line) && line[lead] == ' ' {
		lead++
	}
	rest := line[lead:]
	var b strings.Builder
	b.WriteString(strings.Repeat(nbsp, lead))
	run := 0
	for _, ch := range rest {
		if ch == ' ' {
			run++
			continue
		}
		if run > 0 {
			if run == 1 {
				b.WriteByte(' ')
			} else {
				b.WriteString(strings.Repeat(nbsp, run))
			}
			run = 0
		}
		b.WriteRune(ch)
	}
	return b.String()
}

func (r *mdRenderer) quote(n *ast.Blockquote, depth int) []block {
	segs := r.runs(n)
	if len(segs) == 0 {
		return nil
	}
	prop := props.Text{
		Family:          familySans,
		Style:           fontstyle.Italic,
		Size:            sizeBody,
		Color:           mutedColor,
		VerticalPadding: leading(sizeBody),
		Left:            quoteIndent + float64(depth)*listIndent,
		Bottom:          spaceAfterParagraph,
	}
	return proseBlocks(textRows(r.ms, flattenSegments(segs), prop))
}

// table renders a GFM table through the same builder the host sections use.
func (r *mdRenderer) table(n *east.Table) []block {
	var headers []string
	var records []tableRecord

	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		switch rowNode := child.(type) {
		case *east.TableHeader:
			for c := rowNode.FirstChild(); c != nil; c = c.NextSibling() {
				headers = append(headers, plainSegments(r.runs(c)))
			}
		case *east.TableRow:
			var cells [][]inlineRun
			for c := rowNode.FirstChild(); c != nil; c = c.NextSibling() {
				cells = append(cells, flattenSegments(r.runs(c)))
			}
			records = append(records, tableRecord{cells: cells})
		}
	}
	if columnCount(headers, records) == 0 {
		return nil
	}

	// A markdown table gets no chip column: the host does not know what a
	// model's columns mean, and a chip is a claim about a value's kind.
	return newTable(r.ms, headers, records, noChipColumn, nil).blocks()
}

// runStyle is the emphasis in force at a point in the inline tree.
type runStyle struct {
	bold   bool
	italic bool
	code   bool
}

// runCollector flattens inline nodes into styled runs, splitting at hard line
// breaks so each break becomes its own laid-out line rather than a stray
// newline inside a PDF string.
type runCollector struct {
	source []byte
	segs   [][]inlineRun
	cur    []inlineRun
}

func (c *runCollector) add(text string, st runStyle, color *props.Color) {
	if text == "" {
		return
	}
	family := familySans
	if st.code {
		family = familyMono
	}
	if n := len(c.cur); n > 0 {
		prev := &c.cur[n-1]
		if prev.Bold == st.bold && prev.Italic == st.italic && prev.Family == family && prev.Color == color {
			prev.Text += text
			return
		}
	}
	c.cur = append(c.cur, inlineRun{
		Text:   text,
		Bold:   st.bold,
		Italic: st.italic,
		Family: family,
		Color:  color,
	})
}

func (c *runCollector) breakLine() {
	c.segs = append(c.segs, c.cur)
	c.cur = nil
}

func (c *runCollector) done() [][]inlineRun {
	if len(c.cur) > 0 {
		c.segs = append(c.segs, c.cur)
		c.cur = nil
	}
	out := make([][]inlineRun, 0, len(c.segs))
	for _, s := range c.segs {
		if len(s) > 0 {
			out = append(out, s)
		}
	}
	return out
}

func (c *runCollector) walk(n ast.Node, st runStyle) {
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		switch t := child.(type) {
		case *ast.Text:
			c.add(string(t.Segment.Value(c.source)), st, nil)
			if t.SoftLineBreak() {
				c.add(" ", st, nil)
			}
			if t.HardLineBreak() {
				c.breakLine()
			}
		case *ast.String:
			c.add(string(t.Value), st, nil)
		case *ast.CodeSpan:
			inner := st
			inner.code = true
			c.walk(t, inner)
		case *ast.Emphasis:
			inner := st
			if t.Level >= 2 {
				inner.bold = true
			} else {
				inner.italic = true
			}
			c.walk(t, inner)
		case *ast.Link:
			c.walk(t, st)
			if dest := string(t.Destination); dest != "" && dest != c.lastText() {
				c.add(" ", st, nil)
				c.add(dest, st, accentColor)
			}
		case *ast.AutoLink:
			c.add(string(t.URL(c.source)), st, accentColor)
		case *ast.RawHTML, *ast.HTMLBlock:
			// Markup carries no print meaning; its text children still walk.
			c.walk(child, st)
		default:
			c.walk(child, st)
		}
	}
}

func (c *runCollector) lastText() string {
	if n := len(c.cur); n > 0 {
		return c.cur[n-1].Text
	}
	return ""
}

func (r *mdRenderer) runs(n ast.Node) [][]inlineRun {
	c := &runCollector{source: r.source}
	c.walk(n, runStyle{})
	return c.done()
}

// flattenSegments joins hard-break segments with a space, for contexts such as
// a heading or a table cell where one line is the only sensible shape.
func flattenSegments(segs [][]inlineRun) []inlineRun {
	var out []inlineRun
	for i, s := range segs {
		if i > 0 && len(out) > 0 {
			out = append(out, inlineRun{Text: " ", Family: familySans})
		}
		out = append(out, s...)
	}
	return out
}

func plainSegments(segs [][]inlineRun) string {
	var b strings.Builder
	for _, r := range flattenSegments(segs) {
		b.WriteString(r.Text)
	}
	return strings.TrimSpace(b.String())
}
