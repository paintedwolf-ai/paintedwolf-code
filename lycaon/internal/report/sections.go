package report

import (
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/johnfercher/maroto/v2/pkg/consts/fontstyle"
	"github.com/johnfercher/maroto/v2/pkg/props"
	"github.com/lycaon/lycaon/internal/runeclamp"
)

const (
	sectionContents     = "Contents"
	sectionSummary      = "Summary"
	sectionFindings     = "Findings"
	sectionAssessment   = "Assessment"
	sectionAdjudication = "Adjudication"
	sectionScan         = "Scanner findings"
	sectionEvidence     = "Evidence appendix"
	sectionVisuals      = "Visuals"
	sectionSources      = "Sources"
	sectionColophon     = "About this document"
	sectionLimits       = "Scope and limits"
	seeAppendixPointer  = "see appendix"
)

// CitedByReport names the closeout in a record's cited-by list, beside the
// claim ids and phases that cite it.
const CitedByReport = "report"

// sectionTitle takes the level-1 heading treatment, so a host section and a
// synthesis heading read as the same rank. The block carries the title as a
// contents entry.
func sectionTitle(ms *measurer, title string) block {
	size, above, below, _ := headingProps(1)
	prop := props.Text{
		Family:          familySans,
		Style:           fontstyle.Bold,
		Size:            size,
		Color:           inkColor,
		VerticalPadding: leading(size),
		Top:             above,
	}
	rows := textRows(ms, []inlineRun{{Text: title}}, prop)
	rows = append(rows, ruleRow(ruleThin, ruleColor, below))
	b := keepWithNextBlock(rows...)
	b.contents = &contentsEntry{title: title, level: 1}
	return b
}

// subsectionTitle is the level-2 heading treatment for host sections.
func subsectionTitle(ms *measurer, title string) block {
	size, above, below, _ := headingProps(2)
	return keepWithNextBlock(textRows(ms, []inlineRun{{Text: title}}, props.Text{
		Family:          familySans,
		Style:           fontstyle.Bold,
		Size:            size,
		Color:           inkColor,
		VerticalPadding: leading(size),
		Top:             above,
		Bottom:          below,
	})...)
}

// eyebrowProp is the small tracked label above a title.
func eyebrowProp() props.Text {
	return props.Text{
		Family:          familySans,
		Style:           fontstyle.Bold,
		Size:            sizeEyebrow,
		Color:           mutedColor,
		VerticalPadding: tableLeading(sizeEyebrow),
		Bottom:          spaceBetweenMetaRow,
	}
}

// labelProp is the treatment for a small muted label above or beside a value.
func labelProp() props.Text {
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

func valueProp() props.Text {
	return props.Text{
		Family:          familySans,
		Size:            sizeMeta,
		Color:           inkColor,
		VerticalPadding: tableLeading(sizeMeta),
		Bottom:          spaceBetweenMetaRow,
	}
}

// metaProp is the treatment for a muted line of facts.
func metaProp() props.Text {
	p := valueProp()
	p.Color = mutedColor
	return p
}

// bodyProp is running prose at the body size.
func bodyProp() props.Text {
	return props.Text{
		Family:          familySans,
		Size:            sizeBody,
		Color:           inkColor,
		VerticalPadding: leading(sizeBody),
		Bottom:          spaceAfterParagraph,
	}
}

// labelledValue sets a label beside its value when the value fits on one line,
// and stacks them when it does not.
func labelledValue(ms *measurer, label, value string) []measuredRow {
	label = strings.TrimSpace(label)
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	lp, vp := labelProp(), valueProp()
	if label == "" {
		return textRows(ms, []inlineRun{{Text: value}}, vp)
	}

	labelWidth := ms.width(label, cellStyleKey(lp)) + 2*tableCellPadX
	if ms.width(value, cellStyleKey(vp)) <= contentWidth-labelWidth-vp.Right {
		return labelledRuns(ms, label, []inlineRun{{Text: value}})
	}

	stacked := lp
	stacked.Bottom = tableCellPadY / 2
	rows := textRows(ms, []inlineRun{{Text: label}}, stacked)
	body := vp
	body.Size = sizeBody
	body.VerticalPadding = leading(sizeBody)
	body.Bottom = spaceAfterParagraph
	return append(rows, textRows(ms, []inlineRun{{Text: value}}, body)...)
}

// fact is one label/value row of a factTable.
type fact struct {
	label string
	value []inlineRun
}

func textFact(label, value string) fact {
	return fact{label: label, value: []inlineRun{{Text: strings.TrimSpace(value)}}}
}

// factTable sets label/value pairs as one aligned two-column list, the labels
// sharing a width so the values line up. A fact with no value text is dropped.
func factTable(ms *measurer, facts []fact) []measuredRow {
	lp, vp := labelProp(), valueProp()
	labelWidth := 0.0
	kept := make([]fact, 0, len(facts))
	for _, f := range facts {
		if strings.TrimSpace(plainRuns(f.value)) == "" {
			continue
		}
		f.label = strings.TrimSpace(f.label)
		kept = append(kept, f)
		if w := ms.width(f.label, cellStyleKey(lp)) + 2*tableCellPadX; w > labelWidth {
			labelWidth = w
		}
	}
	rows := make([]measuredRow, 0, len(kept))
	for _, f := range kept {
		rows = append(rows, columnsRow([]cellSpec{
			newCell(ms, labelWidth, []inlineRun{{Text: f.label}}, lp),
			newCell(ms, contentWidth-labelWidth, f.value, vp),
		}))
	}
	return rows
}

// panelRows paints a ground behind rows and pads them, so a list reads as one
// boxed statement rather than more prose.
func panelRows(rows []measuredRow) []measuredRow {
	if len(rows) == 0 {
		return nil
	}
	fill := &props.Cell{BackgroundColor: panelFill}
	out := make([]measuredRow, 0, len(rows)+2)
	out = append(out, fillRow(spaceAroundPanel, panelFill))
	for _, r := range rows {
		r.row.WithStyle(fill)
		out = append(out, r)
	}
	return append(out, fillRow(spaceAroundPanel, panelFill), spacerRow(spaceAfterParagraph))
}

// humanize writes a schema key for a reader: `threat_model` → `Threat model`.
// Workflows name their own verdict members, so there is no table to look in.
func humanize(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	name = strings.ReplaceAll(strings.ReplaceAll(name, "_", " "), "-", " ")
	fields := strings.Fields(name)
	if len(fields) == 0 {
		return ""
	}
	first := []rune(fields[0])
	first[0] = unicode.ToUpper(first[0])
	fields[0] = string(first)
	return strings.Join(fields, " ")
}

// sourceLocation joins a path with its line, omitting the line when unset.
func sourceLocation(path string, line int) string {
	path = strings.TrimSpace(path)
	if line > 0 {
		return fmt.Sprintf("%s:%d", path, line)
	}
	return path
}

// sourceSpan joins a path with a line range: `a.go:80-88`, or `a.go:80` when
// the range is one line.
func sourceSpan(path string, start, end int) string {
	if end > start && start > 0 {
		return fmt.Sprintf("%s:%d-%d", strings.TrimSpace(path), start, end)
	}
	return sourceLocation(path, start)
}

// clip shortens an excerpt to a quotation. An evidence row locates a record
// rather than reproducing it.
func clip(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	cut := max
	for i := max; i > max*3/4; i-- {
		if runes[i] == ' ' {
			cut = i
			break
		}
	}
	return strings.TrimRight(string(runes[:cut]), " ,;:") + runeclamp.Marker
}

// SeverityRank orders a severity label; lower is more severe, and a label the
// host has never ranked sorts last. Assembly and the renderer share it so a
// listing floor and a chip tone cannot disagree about what "medium" means.
func SeverityRank(sev string) int {
	switch strings.ToLower(strings.TrimSpace(sev)) {
	case "critical":
		return 0
	case "high", "error":
		return 1
	case "medium", "warning":
		return 2
	case "low":
		return 3
	case "info", "informational", "note":
		return 4
	default:
		return 50
	}
}

// singular writes a section's plural label for a column header. English
// plurals the host itself sets are the only ones it has to undo.
func singular(label string) string {
	switch {
	case strings.HasSuffix(label, "ies"):
		return strings.TrimSuffix(label, "ies") + "y"
	case strings.HasSuffix(label, "s") && !strings.HasSuffix(label, "ss"):
		return strings.TrimSuffix(label, "s")
	default:
		return label
	}
}

// plural writes a count with its noun.
func plural(n int, one, many string) string {
	return fmt.Sprintf("%d %s", n, noun(n, one, many))
}

// noun picks the form a count takes, for the sentences that put the number
// somewhere other than in front of it.
func noun(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// shortID is the leading segment of an identifier, enough to tell two
// subjects apart.
func shortID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// formatTimestamp renders an RFC 3339 stamp for a reader, falling back to the
// raw value if it does not parse.
func formatTimestamp(raw string) string {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	return t.UTC().Format("2 January 2006, 15:04 UTC")
}

// hostOf is the host part of a URL, which is what a reader scans a source
// list by.
func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.TrimPrefix(u.Host, "www.")
}
