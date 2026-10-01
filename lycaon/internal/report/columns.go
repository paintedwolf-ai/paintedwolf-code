package report

import (
	"strings"

	"github.com/johnfercher/maroto/v2/pkg/props"
)

// Column widths come from the cells, measured against the faces the page is
// drawn with. A column of single tokens — a severity, a path, an evidence
// handle — is an identifier and takes its natural width. A column of sentences
// is prose, and is promoted below the row at the full measure when the row
// cannot spare a readable column for it.
//
// The signal is word count and measured width, never the heading: heading text
// is model-authored, so keying on it would set one workflow's tables
// differently from another's.

const (
	// A column holding more than this many words in any one cell is prose.
	proseWordCount = 7

	// Prose kept in the row needs at least this much width to be worth reading
	// as a column; below it, the column is promoted instead.
	proseMinWidth = 62.0

	// No column in the row is narrower than this, so a heading always has room
	// for at least a short word.
	minColWidth = 15.0

	// A column can be squeezed below minColWidth when a table has more columns
	// than the measure can seat, but never to nothing: a cell of no width would
	// place the cell after it behind the one before.
	minCellWidth = 4.0
)

// cellMetrics is what one cell contributes to its column's budget.
type cellMetrics struct {
	// natural is the width the cell needs to sit on a single line.
	natural float64
	// longest is the widest run of text with no space in it — the floor below
	// which the column only ever breaks tokens apart.
	longest float64
	// words is how many space-separated words the cell holds.
	words int
}

// runKeyFor folds one run's family and emphasis into a column's base style.
func runKeyFor(base styleKey, r inlineRun) styleKey {
	k := base
	if r.Family != "" {
		k.family = r.Family
	}
	k.style = combineStyle(base.style, r.Bold, r.Italic)
	return k
}

// measureCell measures one cell's runs against the style its column will use.
// Tokens carry across run boundaries, so `a**b**c` counts as one word and is
// measured in the faces it is actually set in.
func measureCell(ms *measurer, runs []inlineRun, base styleKey) cellMetrics {
	var m cellMetrics
	token, open := 0.0, false

	closeToken := func() {
		if !open {
			return
		}
		if token > m.longest {
			m.longest = token
		}
		m.words++
		token, open = 0, false
	}

	for _, r := range runs {
		key := runKeyFor(base, r)
		m.natural += ms.width(r.Text, key)
		for i, part := range strings.Split(r.Text, " ") {
			if i > 0 {
				closeToken()
			}
			if part == "" {
				continue
			}
			token += ms.width(part, key)
			open = true
		}
	}
	closeToken()
	return m
}

// columnStat is a whole column's budget input.
type columnStat struct {
	natural float64
	longest float64
	words   int
}

func (c *columnStat) fold(m cellMetrics) {
	if m.natural > c.natural {
		c.natural = m.natural
	}
	if m.longest > c.longest {
		c.longest = m.longest
	}
	if m.words > c.words {
		c.words = m.words
	}
}

// isProse reports whether the column holds sentences rather than identifiers.
func (c columnStat) isProse() bool { return c.words > proseWordCount }

// columnPlan is the decided shape of one table: which columns stay in the row
// and how wide, and which are promoted to full-measure lines beneath it.
type columnPlan struct {
	// inRow are source column indices kept in the row, in their original order.
	inRow []int
	// widths are millimetre widths for inRow, index-aligned, summing to the
	// measure. Not grid spans: see cellSpec.
	widths []float64
	// promoted are source column indices set beneath the row, in order.
	promoted []int
	// labelPromoted prefixes each promoted line with its heading, which is
	// what keeps two promoted columns from reading as one paragraph.
	labelPromoted bool
}

// planColumns budgets a table's measure from its own content. widths overrides
// a column's measured width, for a column drawn as something other than the
// text it holds.
func planColumns(ms *measurer, headers []string, records []tableRecord, base, headBase styleKey, widths map[int]float64) columnPlan {
	n := columnCount(headers, records)
	if n == 0 {
		return columnPlan{}
	}

	stats := make([]columnStat, n)
	for i := 0; i < n; i++ {
		if i < len(headers) {
			// A heading must fit its column but never makes it prose: two
			// words of heading over one-word values is still an identifier.
			m := measureCell(ms, []inlineRun{{Text: headers[i]}}, headBase)
			m.words = 0
			stats[i].fold(m)
		}
	}
	for _, rec := range records {
		for i, cell := range rec.cells {
			if i >= n {
				break
			}
			stats[i].fold(measureCell(ms, cell, base))
		}
	}

	for i, width := range widths {
		if i < 0 || i >= n {
			continue
		}
		// An overridden column is drawn, not set: its width is exact and it
		// never counts as prose.
		stats[i] = columnStat{natural: width, longest: width}
	}

	plan := columnPlan{}
	var proseIdx []int
	for i, s := range stats {
		if s.isProse() {
			proseIdx = append(proseIdx, i)
			continue
		}
		plan.inRow = append(plan.inRow, i)
	}

	// One prose column stays in the row when the identifiers leave it a
	// readable measure. Two never fit, so both are promoted.
	if len(proseIdx) == 1 {
		spare := contentWidth - identifierWidth(stats, plan.inRow)
		if spare >= proseMinWidth {
			plan.inRow = append(plan.inRow, proseIdx[0])
			sortAscending(plan.inRow)
			proseIdx = nil
		}
	}
	plan.promoted = proseIdx
	plan.labelPromoted = len(proseIdx) > 1

	if len(plan.inRow) == 0 {
		// Everything is prose: keep the first column as the row's identity so
		// the promoted lines still hang off something.
		plan.inRow = []int{plan.promoted[0]}
		plan.promoted = plan.promoted[1:]
		plan.labelPromoted = len(plan.promoted) > 1
	}

	plan.widths = widthsFor(stats, plan.inRow)
	return plan
}

// identifierWidth is what the non-prose columns want.
func identifierWidth(stats []columnStat, idx []int) float64 {
	var total float64
	for _, i := range idx {
		total += wantedWidth(stats[i])
	}
	return total
}

// wantedWidth is the width a column needs to set its widest value on one line.
// It is uncapped; columns that overflow the measure are shrunk together in
// widthsFor, never below their own longest token.
func wantedWidth(s columnStat) float64 {
	if w := s.natural + tableCellPadX; w > minColWidth {
		return w
	}
	return minColWidth
}

// widthsFor shares the measure out so the columns sum to it exactly. Slack
// goes to the last column; a shortfall comes off what each column holds above
// its own longest token, since squeezing past that only breaks identifiers.
func widthsFor(stats []columnStat, idx []int) []float64 {
	if len(idx) == 0 {
		return nil
	}
	want := make([]float64, len(idx))
	floor := make([]float64, len(idx))
	var total float64
	for k, i := range idx {
		want[k] = wantedWidth(stats[i])
		floor[k] = stats[i].longest + tableCellPadX
		if floor[k] > want[k] {
			floor[k] = want[k]
		}
		if floor[k] < minColWidth {
			floor[k] = minColWidth
		}
		total += want[k]
	}

	switch {
	case total < contentWidth:
		want[len(want)-1] += contentWidth - total
	case total > contentWidth:
		var slack float64
		for k := range want {
			slack += want[k] - floor[k]
		}
		over := total - contentWidth
		switch {
		case slack <= 0:
			// Every column is at its floor and they still overrun, so something
			// wraps. Take it all from the widest, to wrap one column not two.
			want[widestBy(want)] -= over
		default:
			take := over
			if take > slack {
				take = slack
			}
			for k := range want {
				want[k] -= take * ((want[k] - floor[k]) / slack)
			}
			// Floors alone can still overrun; the widest column absorbs it.
			if over > slack {
				want[widestBy(want)] -= over - slack
			}
		}
	}
	return normalise(want)
}

// normalise keeps every width positive and the total within the measure. A
// table with more columns than the measure can seat drives the shrink past
// zero, and a negative width starts the next cell behind the last one.
func normalise(want []float64) []float64 {
	var total float64
	for k := range want {
		if want[k] < minCellWidth {
			want[k] = minCellWidth
		}
		total += want[k]
	}
	if total <= 0 {
		return want
	}
	if scale := contentWidth / total; scale < 1 {
		for k := range want {
			want[k] *= scale
		}
	}
	return want
}

func widestBy(want []float64) int {
	at, widest := 0, -1.0
	for k := range want {
		if want[k] > widest {
			at, widest = k, want[k]
		}
	}
	return at
}

// holds reports whether a source column stayed in the row.
func (p columnPlan) holds(col int) bool {
	for _, i := range p.inRow {
		if i == col {
			return true
		}
	}
	return false
}

// plainRuns is a cell's text with styling dropped.
func plainRuns(runs []inlineRun) string {
	var b strings.Builder
	for _, r := range runs {
		b.WriteString(r.Text)
	}
	return b.String()
}

func columnCount(headers []string, records []tableRecord) int {
	n := len(headers)
	for _, rec := range records {
		if len(rec.cells) > n {
			n = len(rec.cells)
		}
	}
	if n > gridSize {
		n = gridSize
	}
	return n
}

func sortAscending(v []int) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

// cellStyleKey is the style a table's body cells measure against.
func cellStyleKey(prop props.Text) styleKey {
	return styleKey{family: prop.Family, style: prop.Style, size: prop.Size}
}
