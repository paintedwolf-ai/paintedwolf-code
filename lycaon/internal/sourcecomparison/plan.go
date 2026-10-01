package sourcecomparison

import (
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/pkg/api"
)

// planRow retains coordinates and source slices, not rendered row objects.
type planRow struct {
	Text                          string
	Kind                          string
	BeforeLine, AfterLine, Column int
	group, changedOffset, peer    int
	visible                       bool
}
type changeGroup struct{ start, end, beforeBytes, afterBytes int }
type inlinePlan struct{ before, after []api.SourceReaderSpan }
type inlineCache struct {
	mu     sync.Mutex
	values *pagedview.Cache[int, inlinePlan]
}

func (d *Document) prepareRows() {
	oldLines := lines(d.beforeText)
	oldLine, newLine := 1, 1
	d.rows = make([]planRow, 0, d.Summary.Rows)
	appendLine := func(kind, text string, group, offset int) {
		row := planRow{Kind: kind, group: group, changedOffset: offset, peer: -1}
		if kind != "insert" {
			row.BeforeLine = oldLine
			oldLine++
		}
		if kind != "delete" {
			row.AfterLine = newLine
			newLine++
		}
		if !d.comparable {
			row.Kind, row.group = "equal", -1
		}
		for {
			length := fragmentLength(text)
			row.Text = text[:length]
			d.rows = append(d.rows, row)
			if length == len(text) {
				break
			}
			size := width(row.Text)
			row.Column += size
			row.changedOffset += size
			text = text[length:]
		}
	}
	for _, hunk := range d.unified.Hunks {
		for oldLine < hunk.FromLine && oldLine <= len(oldLines) {
			appendLine("equal", oldLines[oldLine-1], -1, 0)
		}
		group := changeGroup{start: len(d.rows)}
		oldOffset, newOffset := 0, 0
		for _, line := range hunk.Lines {
			kind := line.Kind.String()
			groupID, offset := len(d.groups), 0
			switch kind {
			case "delete":
				offset = oldOffset
				oldOffset += width(line.Content)
				group.beforeBytes += len(line.Content)
			case "insert":
				offset = newOffset
				newOffset += width(line.Content)
				group.afterBytes += len(line.Content)
			default:
				groupID = -1
			}
			appendLine(kind, line.Content, groupID, offset)
		}
		group.end = len(d.rows)
		d.groups = append(d.groups, group)
	}
	for oldLine <= len(oldLines) {
		appendLine("equal", oldLines[oldLine-1], -1, 0)
	}
	d.pairRows()
	d.beforeRows, d.afterRows = make([]int, d.Summary.Before.Lines), make([]int, d.Summary.After.Lines)
	for i, row := range d.rows {
		if row.Column == 0 {
			if row.BeforeLine > 0 {
				d.beforeRows[row.BeforeLine-1] = i
			}
			if row.AfterLine > 0 {
				d.afterRows[row.AfterLine-1] = i
			}
		}
		if len(d.unified.Hunks) == 0 || !d.comparable {
			d.rows[i].visible = true
			continue
		}
		if row.Kind != "equal" {
			for j := max(0, i-ContextLines); j < min(len(d.rows), i+ContextLines+1); j++ {
				d.rows[j].visible = true
			}
		}
	}
	d.unified.Hunks = nil
}

func (d *Document) pairRows() {
	for at := 0; at < len(d.rows); {
		if d.rows[at].Kind == "equal" {
			at++
			continue
		}
		start := at
		for at < len(d.rows) && d.rows[at].Kind != "equal" {
			at++
		}
		left, right := start, start
		for {
			for left < at && d.rows[left].Kind != "delete" {
				left++
			}
			for right < at && d.rows[right].Kind != "insert" {
				right++
			}
			if left == at || right == at {
				break
			}
			d.rows[left].peer, d.rows[right].peer = right, left
			left++
			right++
		}
	}
}

func (d *Document) row(index int) api.SourceReaderRow {
	row := d.rows[index]
	return api.SourceReaderRow{Index: index, End: index + 1, Kind: row.Kind, Text: row.Text, BeforeLine: row.BeforeLine, AfterLine: row.AfterLine, Column: row.Column, Changed: []api.SourceReaderSpan{}}
}

func (d *Document) changed(index int) []api.SourceReaderSpan {
	row := d.rows[index]
	if row.group < 0 {
		return []api.SourceReaderSpan{}
	}
	group := d.groups[row.group]
	// Full-line changes use the line fill; large groups skip inline refinement.
	if group.beforeBytes+group.afterBytes > inlineRefineLimit || group.beforeBytes == 0 || group.afterBytes == 0 {
		return []api.SourceReaderSpan{}
	}
	d.inline.mu.Lock()
	defer d.inline.mu.Unlock()
	if d.inline.values == nil {
		d.inline.values = pagedview.NewCache[int, inlinePlan](128, inlineReservation(d.Before, d.After))
	}
	plan, ok := d.inline.values.Get(row.group)
	if !ok {
		var before, after strings.Builder
		for _, part := range d.rows[group.start:group.end] {
			if part.Kind == "delete" {
				before.WriteString(part.Text)
			}
			if part.Kind == "insert" {
				after.WriteString(part.Text)
			}
		}
		plan.before, plan.after = inlineChanges(before.String(), after.String())
		d.inline.values.Put(row.group, plan, int64(64+16*(len(plan.before)+len(plan.after))))
	}
	spans := plan.after
	if row.Kind == "delete" {
		spans = plan.before
	}
	if !d.lineKeepsWord(index, spans) {
		return []api.SourceReaderSpan{}
	}
	return inlineRowSpans(spans, row.changedOffset, width(row.Text))
}

// lineKeepsWord reports whether index's source line, across its fragment
// rows, keeps a word outside the changed spans.
func (d *Document) lineKeepsWord(index int, spans []api.SourceReaderSpan) bool {
	start, end := index, index+1
	for start > 0 && d.rows[start].Column > 0 {
		start--
	}
	for end < len(d.rows) && d.rows[end].Column > 0 {
		end++
	}
	var text strings.Builder
	for _, part := range d.rows[start:end] {
		text.WriteString(part.Text)
	}
	return keepsUnchangedWord(text.String(), inlineRowSpans(spans, d.rows[start].changedOffset, width(text.String())))
}
