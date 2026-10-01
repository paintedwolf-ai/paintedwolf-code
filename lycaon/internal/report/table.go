package report

import (
	"strings"

	"github.com/johnfercher/maroto/v2/pkg/consts/fontstyle"
	"github.com/johnfercher/maroto/v2/pkg/props"
)

// tableRecord is one row of a table plus any continuation lines the caller
// wants beneath it. Promoted columns add their own; see columns.go.
type tableRecord struct {
	cells [][]inlineRun
	extra [][]inlineRun
}

// table lays out a column head and a set of records. Column widths come from
// the content (planColumns), the head reprints whenever a record starts a new
// page, and each record is separated by a hairline so a row with continuation
// lines still reads as one row.
type table struct {
	ms      *measurer
	headers []string
	records []tableRecord

	// chipColumn sets one in-row column as chips. Host sections use it for
	// values they own; markdown tables never do, since the host does not know
	// what a model's column means.
	chipColumn int

	// extraFamily is the face for caller-supplied continuation lines.
	extraFamily string

	plan  columnPlan
	chips []chipLabel
}

// noChipColumn lays every column out as text.
const noChipColumn = -1

// newTable budgets the table's columns and returns it ready to lay out. The
// chip column is named here rather than set afterwards: a chip's ground is a
// different width from the word inside it, and budgeting for the word leaves
// the difference to come off a neighbour that was only just fitting. tone
// decides each chip's ground from its text; nil keeps every chip neutral.
func newTable(ms *measurer, headers []string, records []tableRecord, chipColumn int, tone func(string) chipTone) *table {
	t := &table{ms: ms, headers: headers, records: records, chipColumn: noChipColumn, extraFamily: familySans}
	if tone == nil {
		tone = func(string) chipTone { return toneNeutral }
	}

	widths := map[int]float64{}
	if chipColumn >= 0 {
		t.chipColumn = chipColumn
		for _, rec := range records {
			label := chipLabel{}
			if chipColumn < len(rec.cells) {
				text := plainRuns(rec.cells[chipColumn])
				label = newTonedChip(ms, text, tone(text))
			}
			t.chips = append(t.chips, label)
			if label.width > widths[chipColumn] {
				widths[chipColumn] = label.width
			}
		}
	}

	t.plan = planColumns(ms, headers, records, cellStyleKey(t.cellProp()), cellStyleKey(t.headProp()), widths)
	if chipColumn >= 0 && !t.plan.holds(chipColumn) {
		// Promotion moved the column out of the row, and prose takes no chip.
		t.chipColumn = noChipColumn
	}
	return t
}

func (t *table) headProp() props.Text {
	return props.Text{
		Family:          familySans,
		Style:           fontstyle.Bold,
		Size:            sizeTableHead,
		Color:           mutedColor,
		VerticalPadding: tableLeading(sizeTableHead),
		Right:           tableCellPadX,
		Bottom:          tableCellPadY,
	}
}

func (t *table) cellProp() props.Text {
	return props.Text{
		Family:          familySans,
		Size:            sizeTableCell,
		Color:           inkColor,
		VerticalPadding: tableLeading(sizeTableCell),
		Right:           tableCellPadX,
		Bottom:          tableCellPadY,
	}
}

// promotedProp is the full-measure treatment for a promoted column, hung under
// the row's second column so the identifiers stay an index down the left.
func (t *table) promotedProp() props.Text {
	return props.Text{
		Family:          familySans,
		Size:            sizeTableCell,
		Color:           inkColor,
		VerticalPadding: tableLeading(sizeTableCell),
		Left:            t.promotedIndent(),
		Bottom:          tableCellPadY,
	}
}

func (t *table) extraProp() props.Text {
	prop := t.promotedProp()
	prop.Family = t.extraFamily
	prop.Color = mutedColor
	return prop
}

// promotedIndent hangs continuation lines under the row's second column, and
// gives that up when doing so would leave too little measure to read.
func (t *table) promotedIndent() float64 {
	if len(t.plan.widths) < 2 {
		return 0
	}
	indent := t.plan.widths[0]
	if contentWidth-indent < proseMinWidth {
		return 0
	}
	return indent
}

func (t *table) headerRows() []measuredRow {
	prop := t.headProp()
	cells := make([]cellSpec, 0, len(t.plan.widths))
	for k, width := range t.plan.widths {
		label := ""
		if src := t.plan.inRow[k]; src < len(t.headers) {
			label = t.headers[src]
		}
		cells = append(cells, newCell(t.ms, width, []inlineRun{{Text: label}}, prop))
	}
	return []measuredRow{
		columnsRow(cells),
		ruleRow(ruleThin, ruleColor, tableCellPadY),
	}
}

// recordRows lays one record out: its in-row columns, then promoted columns at
// the full measure, then the caller's continuation lines.
func (t *table) recordRows(rec tableRecord, index int) []measuredRow {
	rows := []measuredRow{t.identityRow(rec, index)}

	for _, src := range t.plan.promoted {
		if src >= len(rec.cells) || len(rec.cells[src]) == 0 {
			continue
		}
		runs := rec.cells[src]
		if t.plan.labelPromoted && src < len(t.headers) {
			runs = withLabel(t.headers[src], runs)
		}
		rows = append(rows, textRows(t.ms, runs, t.promotedProp())...)
	}

	for _, extra := range rec.extra {
		if len(extra) == 0 {
			continue
		}
		rows = append(rows, textRows(t.ms, extra, t.extraProp())...)
	}
	return rows
}

// identityRow is the record's own line: the columns that stayed in the row.
func (t *table) identityRow(rec tableRecord, index int) measuredRow {
	prop := t.cellProp()

	cells := make([]cellSpec, 0, len(t.plan.widths))
	for k, width := range t.plan.widths {
		src := t.plan.inRow[k]
		if src == t.chipColumn && index < len(t.chips) {
			cells = append(cells, newChipCell(t.ms, width, t.chips[index], prop))
			continue
		}
		var runs []inlineRun
		if src < len(rec.cells) {
			runs = rec.cells[src]
		}
		cells = append(cells, newCell(t.ms, width, runs, prop))
	}
	return columnsRow(cells)
}

// withLabel prefixes a promoted column's line with its heading.
func withLabel(header string, runs []inlineRun) []inlineRun {
	header = strings.TrimSpace(header)
	if header == "" {
		return runs
	}
	out := []inlineRun{
		{Text: header, Bold: true, Family: familySans, Color: mutedColor},
		{Text: "  ", Family: familySans},
	}
	return append(out, runs...)
}

func (t *table) blocks() []block {
	if len(t.records) == 0 || len(t.plan.widths) == 0 {
		return nil
	}

	// The head is its own block, held to the first record. Carrying it inside
	// that record's block would print it twice on a page break, since emit
	// reprints a broken table's head as well.
	out := make([]block, 0, len(t.records)+2)
	out = append(out, keepWithNextBlock(t.headerRows()...))

	for i, rec := range t.records {
		rows := t.recordRows(rec, i)
		if i > 0 {
			rows = append([]measuredRow{spacerRow(spaceAboveRecord)}, rows...)
		}
		if i < len(t.records)-1 {
			rows = append(rows, ruleRow(ruleThin, hairlineColor, 0))
		}
		out = append(out, block{rows: rows, repeatHeader: t.headerRows})
	}

	return append(out, rowsBlock(
		ruleRow(ruleThin, ruleColor, 0),
		spacerRow(spaceAfterParagraph),
	))
}
