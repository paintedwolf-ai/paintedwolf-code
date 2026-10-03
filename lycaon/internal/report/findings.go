package report

import (
	"strconv"
	"strings"

	"github.com/johnfercher/maroto/v2/pkg/consts/fontstyle"
	"github.com/johnfercher/maroto/v2/pkg/props"
)

// Findings are stated twice: a glance in the working summary carrying grade,
// title and one place for each finding that needs attention, and a detail
// entry for every finding carrying impact, action and every place. The
// numbering ties the two together.

// findingsLabel is what this subject calls its assessed conclusions. A
// workflow declares the word; the renderer holds the default.
func findingsLabel(input ReportInput) string {
	if label := strings.TrimSpace(input.FindingsLabel); label != "" {
		return label
	}
	return sectionFindings
}

// findingsGlanceRows is the working summary's table of findings that need
// attention. A numbered marker ties a row to the detail entry that expands it.
func findingsGlanceRows(ms *measurer, input ReportInput) []measuredRow {
	var findings []ReportFinding
	for _, f := range input.Findings {
		if f.NeedsAttention() {
			findings = append(findings, f)
		}
	}
	if len(findings) == 0 {
		return nil
	}

	const (
		markerWidth = 7.0
		sevWidth    = 23.0
		whereWidth  = 46.0
	)
	titleWidth := contentWidth - markerWidth - sevWidth - whereWidth

	head := labelProp()
	rows := []measuredRow{columnsRow([]cellSpec{
		newCell(ms, markerWidth, nil, head),
		newCell(ms, sevWidth, []inlineRun{{Text: gradeHeader(findings)}}, head),
		newCell(ms, titleWidth, []inlineRun{{Text: singular(findingsLabel(input))}}, head),
		newCell(ms, whereWidth, []inlineRun{{Text: "Where"}}, head),
	})}
	rows = append(rows, ruleRow(ruleThin, ruleColor, 0))

	markerProp := metaProp()
	markerProp.Family = familyMono
	markerProp.Top = tableCellPadY
	titleProp := valueProp()
	titleProp.Top = tableCellPadY
	titleProp.Right = tableCellPadX
	whereProp := metaProp()
	whereProp.Family = familyMono
	whereProp.Top = tableCellPadY

	for i, f := range input.Findings {
		if !f.NeedsAttention() {
			continue
		}
		rows = append(rows, columnsRow([]cellSpec{
			newCell(ms, markerWidth, []inlineRun{{Text: strconv.Itoa(i + 1)}}, markerProp),
			gradeCell(ms, sevWidth, f, titleProp),
			newCell(ms, titleWidth, []inlineRun{{Text: f.Title}}, titleProp),
			newCell(ms, whereWidth, []inlineRun{{Text: findingWhere(f, 1)}}, whereProp),
		}), ruleRow(ruleThin, hairlineColor, 0))
	}
	return append(rows, spacerRow(spaceAfterParagraph))
}

// gradeHeader names the grading column for whatever the findings actually
// carry: a workflow that decides rather than scores grades by status alone.
func gradeHeader(findings []ReportFinding) string {
	for _, f := range findings {
		if strings.TrimSpace(f.Severity) != "" {
			return "Severity"
		}
	}
	return "Status"
}

// gradeCell is the finding's severity; else, for a risk kept on purpose, that
// it was accepted; else its status; else its disposition; and nothing when it
// stated none of these.
func gradeCell(ms *measurer, width float64, f ReportFinding, prop props.Text) cellSpec {
	if strings.TrimSpace(f.Severity) != "" {
		return newChipCell(ms, width, severityChip(ms, f), prop)
	}
	if f.Disposition == DispositionAccept {
		return newChipCell(ms, width, newTonedChip(ms, dispositionWord(f.Disposition), toneNeutral), prop)
	}
	if status := strings.TrimSpace(f.Status); status != "" {
		return newChipCell(ms, width, newTonedChip(ms, status, toneNeutral), prop)
	}
	if word := dispositionWord(f.Disposition); word != "" {
		return newChipCell(ms, width, newTonedChip(ms, word, toneNeutral), prop)
	}
	return newCell(ms, width, nil, prop)
}

// findingWhere writes the first places a finding names, and counts the rest.
func findingWhere(f ReportFinding, max int) string {
	var shown []string
	for _, w := range f.Where {
		if len(shown) == max {
			break
		}
		if loc := citationLocation(w); loc != "" {
			shown = append(shown, loc)
		}
	}
	if len(shown) == 0 {
		return ""
	}
	out := strings.Join(shown, " · ")
	if rest := len(f.Where) - len(shown); rest > 0 {
		out += " +" + strconv.Itoa(rest)
	}
	return out
}

// citationLocation prefers the place a citation names, falling back to the
// evidence handle when it names no path.
func citationLocation(c ReportClaimCitation) string {
	if path := strings.TrimSpace(c.Path); path != "" {
		return sourceLocation(path, c.Line)
	}
	return strings.TrimSpace(c.Handle)
}

// findingsBlocks is the detail: one keep-together entry per finding.
func findingsBlocks(ms *measurer, input ReportInput) []block {
	out := []block{sectionTitle(ms, findingsLabel(input))}
	for i, f := range input.Findings {
		out = append(out, findingBlock(ms, i+1, f))
	}
	return out
}

// findingBlock is one finding entire: its number and title, the chips that
// grade it, then impact, action, and where — each on its own labelled line.
func findingBlock(ms *measurer, number int, f ReportFinding) block {
	rows := textRows(ms, []inlineRun{
		{Text: strconv.Itoa(number) + ". ", Family: familySans, Color: mutedColor},
		{Text: f.Title, Family: familySans},
	}, props.Text{
		Family:          familySans,
		Style:           fontstyle.Bold,
		Size:            sizeSubsection,
		Color:           inkColor,
		VerticalPadding: leading(sizeSubsection),
		Top:             spaceAboveSubsection,
		Bottom:          spaceBetweenMetaRow,
	})

	if chips := findingChips(ms, f); len(chips) > 0 {
		rows = append(rows, chipStripRow(ms, chips, 0, spaceBetweenMetaRow))
	}
	// Every entry sets its labels the same way, so the findings read as one
	// list rather than as entries formatted by their own length.
	for _, line := range [][2]string{{"Impact", f.Impact}, {"Action", f.Action}} {
		if strings.TrimSpace(line[1]) == "" {
			continue
		}
		rows = append(rows, labelledRuns(ms, line[0], []inlineRun{{Text: line[1], Family: familySans}})...)
	}
	if where := allFindingLocations(f); len(where) > 0 {
		rows = append(rows, labelledRuns(ms, "Where", where)...)
	}
	rows = append(rows, spacerRow(spaceAfterParagraph))
	return rowsBlock(rows...)
}

func findingChips(ms *measurer, f ReportFinding) []chipLabel {
	var out []chipLabel
	if word := dispositionWord(f.Disposition); word != "" {
		tone := toneNeutral
		if f.Disposition == DispositionAct {
			tone = toneAccent
		}
		out = append(out, newTonedChip(ms, word, tone))
	}
	if strings.TrimSpace(f.Severity) != "" {
		out = append(out, severityChip(ms, f))
	}
	if status := strings.TrimSpace(f.Status); status != "" {
		out = append(out, newTonedChip(ms, status, toneNeutral))
	}
	if id := strings.TrimSpace(f.ID); id != "" {
		out = append(out, newLiteralChip(ms, id, toneNeutral))
	}
	return out
}

// allFindingLocations lists every place a finding names, in mono.
func allFindingLocations(f ReportFinding) []inlineRun {
	var out []inlineRun
	for _, w := range f.Where {
		loc := citationLocation(w)
		if loc == "" {
			continue
		}
		if len(out) > 0 {
			out = append(out, inlineRun{Text: " · ", Family: familySans, Color: mutedColor})
		}
		out = append(out, inlineRun{Text: loc, Family: familyMono})
	}
	return out
}

// labelledRuns sets a label beside a run of mixed-family text.
func labelledRuns(ms *measurer, label string, runs []inlineRun) []measuredRow {
	lp := labelProp()
	width := ms.width(label, cellStyleKey(lp)) + 2*tableCellPadX
	return []measuredRow{columnsRow([]cellSpec{
		newCell(ms, width, []inlineRun{{Text: label}}, lp),
		newCell(ms, contentWidth-width, runs, valueProp()),
	})}
}

// severityChip sets a finding's severity on the tone its rating level
// declares, else on the tone the word's rank earns.
func severityChip(ms *measurer, f ReportFinding) chipLabel {
	sev := strings.TrimSpace(f.Severity)
	if tone := strings.TrimSpace(f.SeverityTone); tone != "" {
		return newTonedChip(ms, sev, briefTone(tone))
	}
	return newTonedChip(ms, sev, severityTone(sev))
}

// dispositionWord names a disposition for a reader.
func dispositionWord(disposition string) string {
	switch disposition {
	case DispositionAct:
		return "Needs action"
	case DispositionAccept:
		return "Accepted risk"
	case DispositionUnresolved:
		return "Unanswered question"
	case DispositionHeld:
		return "Sound"
	default:
		return ""
	}
}
