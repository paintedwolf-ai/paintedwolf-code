package report

import (
	"github.com/johnfercher/go-tree/node"
	"github.com/johnfercher/maroto/v2/pkg/components/col"
	"github.com/johnfercher/maroto/v2/pkg/components/line"
	"github.com/johnfercher/maroto/v2/pkg/components/row"
	"github.com/johnfercher/maroto/v2/pkg/consts/linestyle"
	"github.com/johnfercher/maroto/v2/pkg/consts/orientation"
	"github.com/johnfercher/maroto/v2/pkg/core"
	"github.com/johnfercher/maroto/v2/pkg/core/entity"
	"github.com/johnfercher/maroto/v2/pkg/props"
)

// measuredRow is a row paired with its computed height. Heights are explicit
// rather than automatic so the number a page break is decided from is the same
// one the renderer lays out with.
type measuredRow struct {
	row    core.Row
	height float64
}

// block is a run of rows that must stay on one page. repeatHeader reprints a
// table's column head when the block starts a new page; keepWithNext holds a
// heading to the row that follows it; breakAfter ends the page after it;
// contents names the block in the table of contents.
type block struct {
	rows         []measuredRow
	repeatHeader func() []measuredRow
	keepWithNext bool
	breakAfter   bool
	contents     *contentsEntry
}

func (b block) height() float64 {
	var h float64
	for _, r := range b.rows {
		h += r.height
	}
	return h
}

func (b block) firstRowHeight() float64 {
	if len(b.rows) == 0 {
		return 0
	}
	return b.rows[0].height
}

// rowsBlock wraps rows that travel together.
func rowsBlock(rows ...measuredRow) block { return block{rows: rows} }

// keepWithNextBlock wraps rows that must not end a page — headings, mostly.
func keepWithNextBlock(rows ...measuredRow) block {
	return block{rows: rows, keepWithNext: true}
}

// cellSpec is one column of a multi-column row: any component, and the width
// it was laid out against. A chip and a line of text are the same kind of
// thing here.
//
// Widths are millimetres, not grid spans — the grid expresses whole columns
// only, and rounding a measured width onto it costs up to a step per column,
// enough to wrap the identifier the width was measured for. Cells are drawn as
// one band, positioned exactly.
type cellSpec struct {
	comp   core.Component
	width  float64
	height float64
}

// newCell lays a cell's runs out against the width its column will give it.
func newCell(ms *measurer, width float64, runs []inlineRun, prop props.Text) cellSpec {
	text := newInlineText(ms, runs, prop, width-prop.Left-prop.Right)
	return cellSpec{comp: text, width: width, height: text.height()}
}

// newChipCell sets a cell's value as a chip. A chip is shorter than a line of
// text, so it is centred on the line the cells beside it sit on.
func newChipCell(ms *measurer, width float64, label chipLabel, prop props.Text) cellSpec {
	if label.width > width {
		return newCell(ms, width, []inlineRun{{Text: label.text, Family: familySans}}, prop)
	}
	c := newChips(ms, []chipLabel{label}, width)
	c.top, c.bottom = prop.Top, prop.Bottom
	if lift := (ms.fontHeight(prop.Size) - chipHeight(ms)) / 2; lift > 0 {
		c.top += lift
	}
	return cellSpec{comp: c, width: width, height: c.height()}
}

// band draws a row's cells side by side at exact offsets across the measure.
type band struct {
	cells  []cellSpec
	height float64
}

func newBand(cells []cellSpec) *band {
	b := &band{cells: cells}
	for _, c := range cells {
		if c.height > b.height {
			b.height = c.height
		}
	}
	return b
}

func (b *band) SetConfig(*entity.Config) {}

func (b *band) GetHeight(core.Provider, *entity.Cell) float64 { return b.height }

// GetStructure carries every cell, so the row's text stays inspectable.
func (b *band) GetStructure() *node.Node[core.Structure] {
	root := node.New(core.Structure{Type: "band"})
	for _, c := range b.cells {
		root.AddNext(c.comp.GetStructure())
	}
	return root
}

func (b *band) Render(provider core.Provider, cell *entity.Cell) {
	x := cell.X
	for _, c := range b.cells {
		c.comp.Render(provider, &entity.Cell{X: x, Y: cell.Y, Width: c.width, Height: c.height})
		x += c.width
	}
}

// columnsRow builds a row from cells, sized to the tallest one.
func columnsRow(cells []cellSpec) measuredRow {
	b := newBand(cells)
	return measuredRow{
		row:    row.New(b.height).Add(col.New(gridSize).Add(b)),
		height: b.height,
	}
}

// textRows lays prose out at full measure and returns one row per line, so a
// page break can fall between any two lines.
func textRows(ms *measurer, runs []inlineRun, prop props.Text) []measuredRow {
	text := newInlineText(ms, runs, prop, contentWidth-prop.Left-prop.Right)
	lines := text.splitLines()
	out := make([]measuredRow, 0, len(lines))
	for _, ln := range lines {
		h := ln.height()
		out = append(out, measuredRow{
			row:    row.New(h).Add(col.New(gridSize).Add(ln)),
			height: h,
		})
	}
	return out
}

// spacerRow is vertical space with nothing in it.
func spacerRow(height float64) measuredRow {
	return measuredRow{row: row.New(height).Add(col.New(gridSize)), height: height}
}

// fillRow pads a panel with a background.
func fillRow(height float64, color *props.Color) measuredRow {
	r := row.New(height).Add(col.New(gridSize))
	r.WithStyle(&props.Cell{BackgroundColor: color})
	return measuredRow{row: r, height: height}
}

// ruleRow is a horizontal hairline followed by pad millimetres of space.
func ruleRow(thickness float64, color *props.Color, pad float64) measuredRow {
	height := thickness + pad
	r := line.NewRow(height, props.Line{
		Color:         color,
		Style:         linestyle.Solid,
		Thickness:     thickness,
		Orientation:   orientation.Horizontal,
		OffsetPercent: 50,
		SizePercent:   100,
	})
	return measuredRow{row: r, height: height}
}

// docBuilder builds the document and every page-break decision, and records
// the page each block starts on so the contents can name it.
type docBuilder struct {
	m core.Maroto

	page        int
	pageHasRows bool
	// pages is the page each emitted block started on, index-aligned with
	// the blocks handed to emit.
	pages []int
}

// remaining is the unused height on the current page, in millimetres.
// FitlnCurrentPage only answers yes or no for a candidate height, so bisecting
// it reads the document's accumulated state instead of re-deriving it.
func (d *docBuilder) remaining() float64 {
	if !d.m.FitlnCurrentPage(0) {
		return 0
	}
	lo, hi := 0.0, pageHeightMM
	for i := 0; i < 32; i++ {
		mid := (lo + hi) / 2
		if d.m.FitlnCurrentPage(mid) {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo
}

// fillPage consumes the rest of the current page. A page only breaks when a
// row will not fit, so a break is asked for by filling the space before it.
func (d *docBuilder) fillPage() {
	if space := d.remaining(); space > 0 {
		d.m.AddRows(row.New(space).Add(col.New(gridSize)))
	}
	d.page++
	d.pageHasRows = false
}

func (d *docBuilder) addRows(rows []measuredRow) {
	for _, r := range rows {
		// A row the page cannot hold makes the library open a new one.
		if d.pageHasRows && !d.m.FitlnCurrentPage(r.height) {
			d.page++
			d.pageHasRows = false
		}
		d.m.AddRows(r.row)
		d.pageHasRows = true
	}
}

// emit writes blocks into the document and places their page breaks.
func (d *docBuilder) emit(blocks []block) {
	if d.page == 0 {
		d.page = 1
	}
	d.pages = make([]int, len(blocks))
	for i, b := range blocks {
		d.pages[i] = d.page
		if len(b.rows) == 0 {
			continue
		}
		need := b.height()
		if b.keepWithNext && i+1 < len(blocks) {
			need += blocks[i+1].firstRowHeight()
		}

		// A block too tall to fit any page is emitted row by row; forcing a
		// break for it would only buy a blank page.
		if d.pageHasRows && need <= usableHeight && d.remaining() < need {
			d.fillPage()
			if b.repeatHeader != nil {
				d.addRows(b.repeatHeader())
			}
		}
		d.pages[i] = d.page
		d.addRows(b.rows)
		if b.breakAfter && i+1 < len(blocks) {
			d.fillPage()
		}
	}
}
