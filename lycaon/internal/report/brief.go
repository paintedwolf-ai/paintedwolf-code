package report

import (
	"fmt"
	"strings"
	"time"

	"github.com/johnfercher/maroto/v2/pkg/consts/fontstyle"
	"github.com/johnfercher/maroto/v2/pkg/props"

	"github.com/lycaon/lycaon/internal/reviewcoverage"
)

// The first page answers, in order: how bad, how complete, what is asked of
// the reader, and what was checked. The host writes every word but the ask.

// briefPages is the brief's fixed page count.
const briefPages = 1

func briefBlocks(ms *measurer, input ReportInput) []block {
	completeness := input.Completeness()
	rows := briefMasthead(ms, input)
	rows = append(rows, notAcceptedRows(ms, input)...)
	rows = append(rows, answerRows(ms, input, completeness)...)
	rows = append(rows, gaugeRows(ms, input, completeness)...)
	rows = append(rows, askRows(ms, input)...)
	rows = append(rows, checkRows(ms, input)...)
	rows = append(rows, briefFooterRows(ms, input)...)
	b := rowsBlock(rows...)
	b.breakAfter = true
	return []block{b}
}

// briefMasthead names what this is and when, on one line.
func briefMasthead(ms *measurer, input ReportInput) []measuredRow {
	title := documentTitle(input)
	date := formatDate(input.CompletedAt)
	prop := eyebrowProp()
	prop.Bottom = 0
	right := prop
	right.Style = fontstyle.Normal
	// Cells set text from their left edge, so the date's cell is its own width.
	dateWidth := ms.width(date, cellStyleKey(right)) + tableCellPadX
	return []measuredRow{columnsRow([]cellSpec{
		newCell(ms, contentWidth-dateWidth, []inlineRun{{Text: title}}, prop),
		newCell(ms, dateWidth, []inlineRun{{Text: date}}, right),
	})}
}

func formatDate(raw string) string {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return ""
	}
	return t.UTC().Format("2 January 2006")
}

// notAcceptedRows open a report that failed its own checks by saying so,
// ahead of any rating it states, with what each failed check means.
func notAcceptedRows(ms *measurer, input ReportInput) []measuredRow {
	if len(input.Defects) == 0 {
		return nil
	}
	head := labelProp()
	head.Color = toneHigh.ink
	head.Left, head.Right = panelIndent, panelIndent
	rows := textRows(ms, []inlineRun{{Text: "Report not accepted"}}, head)
	prop := bodyProp()
	prop.Left, prop.Right = panelIndent, panelIndent
	prop.Bottom = spaceBetweenMetaRow
	lines := []string{"This report failed its own checks and is stored as written, so it is incomplete."}
	for _, d := range input.Defects {
		if line := defectLine(d); line != "" {
			lines = append(lines, line)
		}
	}
	for _, line := range lines {
		rows = append(rows, textRows(ms, []inlineRun{{Text: line}}, prop)...)
	}
	return panelRows(rows)
}

// defectLine says what a failed check means for a reader, without the
// identifiers the working summary lists.
func defectLine(d ReportDefect) string {
	n := max(d.Count, len(d.Subjects))
	switch d.Code {
	case DefectClaimUnreported:
		return fmt.Sprintf("%d review %s no conclusion in it.", n, noun(n, "result has", "results have"))
	case DefectInventoryUnaccounted:
		return fmt.Sprintf("%d automated scan result %s no assessment and no set-aside.", n, noun(n, "group has", "groups have"))
	case DefectFenceUnreadable:
		return "Some of what it wrote was not in the report's format and was left out."
	case DefectDocumentInvalid:
		return "Some of its fields break the report format."
	default:
		return ""
	}
}

// answerRows is the opening line: the rating, then how complete the work is.
func answerRows(ms *measurer, input ReportInput, completeness string) []measuredRow {
	var runs []inlineRun
	if b := input.Brief; b != nil && len(b.Levels) > 0 {
		runs = append(runs, inlineRun{Text: ratingAnswer(*b) + " ", Color: briefTone(b.Levels[b.Worst].Tone).ink})
	}
	runs = append(runs, inlineRun{Text: completenessLine(completeness)})
	return textRows(ms, runs, props.Text{
		Family:          familySans,
		Style:           fontstyle.Bold,
		Size:            sizeAnswer,
		Color:           inkColor,
		VerticalPadding: leading(sizeAnswer) * 0.45,
		Top:             spaceAboveSection,
		Bottom:          spaceAboveSection,
	})
}

// ratingAnswer states the level, or the range an open answer leaves.
func ratingAnswer(b ReportBrief) string {
	best := b.Levels[b.Best]
	if b.Worst == b.Best {
		return sentence(levelAnswer(best)) + "."
	}
	worst := b.Levels[b.Worst]
	if ratingOpen(b) {
		return "Not rated, possibly " + strings.ToLower(worst.Label) + "."
	}
	return sentence(levelAnswer(best)) + ", possibly " + strings.ToLower(worst.Label) + "."
}

// ratingOpen reports a rating nothing settled: only the scale's mildest level
// is certain, and an open answer could decide a worse one. Stating that mild
// level as the answer would read as a conclusion the review did not reach.
func ratingOpen(b ReportBrief) bool {
	return b.Worst != b.Best && b.Best == len(b.Levels)-1
}

func levelAnswer(l ReportLevel) string {
	if a := strings.TrimSpace(l.Answer); a != "" {
		return a
	}
	return l.Label
}

func completenessLine(level string) string {
	switch level {
	case CompletenessIncomplete:
		return "The check is incomplete."
	case CompletenessMostly:
		return "The check is mostly complete."
	default:
		return "The check is complete."
	}
}

// gaugeRows set the two questions a reader asks, each with its scale, its
// answer, and what that answer means. A workflow that rates nothing gets the
// completeness gauge alone.
func gaugeRows(ms *measurer, input ReportInput, completeness string) []measuredRow {
	var gauges [][]cellSpec
	width := contentWidth / 2
	if b := input.Brief; b != nil && len(b.Levels) > 0 {
		gauges = append(gauges, ratingGauge(ms, width-gaugeGutter, input, *b))
	}
	gauges = append(gauges, completenessGauge(ms, width-gaugeGutter, input, completeness))

	rows := []measuredRow{ruleRow(ruleThin, inkColor, 0)}
	cols := make([]cellSpec, 0, len(gauges)*2)
	for i, g := range gauges {
		if i > 0 {
			cols = append(cols, newCell(ms, gaugeGutter, nil, valueProp()))
		}
		cols = append(cols, stackCell(width-gaugeGutter, g))
	}
	rows = append(rows, columnsRow(cols))
	return append(rows, ruleRow(ruleThin, inkColor, spaceAroundPanel))
}

// gaugeGutter separates the two gauges.
const gaugeGutter = 6.0

func ratingGauge(ms *measurer, width float64, input ReportInput, b ReportBrief) []cellSpec {
	n := len(b.Levels)
	tone := briefTone(b.Levels[b.Worst].Tone)
	// Levels run most severe first; the scale reads mild to severe.
	bar := &scaleBar{steps: n, firm: n - b.Best, soft: n - b.Worst, tone: tone}
	labels := make([]string, 0, n)
	for i := n - 1; i >= 0; i-- {
		labels = append(labels, b.Levels[i].Label)
	}
	chosen := map[string]bool{b.Levels[b.Best].Label: true, b.Levels[b.Worst].Label: true}

	level := b.Levels[b.Best].Label
	means := b.Levels[b.Best].Means
	switch {
	case ratingOpen(b):
		level = "Not rated"
		means = "Open answers leave it anywhere from " + strings.ToLower(b.Levels[b.Best].Label) + " to " + strings.ToLower(b.Levels[b.Worst].Label) + "."
	case b.Worst != b.Best:
		level += " to " + strings.ToLower(b.Levels[b.Worst].Label)
		means += " An open answer could make it " + strings.ToLower(b.Levels[b.Worst].Label) + "."
	}
	cells := []cellSpec{
		newCell(ms, width, []inlineRun{{Text: sentence(b.Question)}}, gaugeQuestionProp()),
		scaleCell(width, bar),
		scaleLabels(ms, width, labels, chosen),
		newCell(ms, width, []inlineRun{{Text: level, Color: tone.ink}}, gaugeLevelProp()),
		newCell(ms, width, []inlineRun{{Text: means}}, gaugeMeansProp()),
	}
	if basis := basisLine(input, b); basis != "" {
		cells = append(cells, newCell(ms, width, []inlineRun{{Text: basis}}, gaugeBasisProp()))
	}
	return cells
}

// basisLine says which finding set the level and why, in the phrases the
// workflow declared for its answers.
func basisLine(input ReportInput, b ReportBrief) string {
	basis := strings.TrimSpace(b.Basis)
	if basis == "" || len(b.Rated) == 0 {
		return ""
	}
	label := strings.ToLower(findingsLabel(input))
	if len(b.Rated) == 1 {
		return fmt.Sprintf("Based on 1 %s: %s.", singular(label), basis)
	}
	return fmt.Sprintf("Worst of %d %s: %s.", len(b.Rated), label, basis)
}

// noAskLine answers "what we need from you" when the report asks nothing.
func noAskLine(unreported int) string {
	if unreported > 0 {
		return fmt.Sprintf("No decision was recorded. %d review %s still %s a conclusion.",
			unreported, noun(unreported, "result", "results"), noun(unreported, "needs", "need"))
	}
	return "Nothing. This is for your information."
}

func completenessGauge(ms *measurer, width float64, input ReportInput, level string) []cellSpec {
	steps := []string{"Incomplete", "Mostly", "Complete"}
	firm := map[string]int{CompletenessIncomplete: 1, CompletenessMostly: 2, CompletenessComplete: 3}[level]
	tone := map[string]chipTone{CompletenessIncomplete: toneMedium, CompletenessMostly: toneInfo, CompletenessComplete: toneGood}[level]
	word := map[string]string{CompletenessIncomplete: "Incomplete", CompletenessMostly: "Mostly complete", CompletenessComplete: "Complete"}[level]
	chosen := map[string]bool{steps[firm-1]: true}
	return []cellSpec{
		newCell(ms, width, []inlineRun{{Text: "How complete is the check?"}}, gaugeQuestionProp()),
		scaleCell(width, &scaleBar{steps: 3, firm: firm, soft: firm, tone: tone}),
		scaleLabels(ms, width, steps, chosen),
		newCell(ms, width, []inlineRun{{Text: word, Color: tone.ink}}, gaugeLevelProp()),
		newCell(ms, width, []inlineRun{{Text: completenessReason(input, level)}}, gaugeMeansProp()),
	}
}

// completenessReason names the largest gaps in plain words.
func completenessReason(input ReportInput, level string) string {
	if len(input.Defects) > 0 {
		return "This report failed acceptance checks and is not complete."
	}
	if input.CoverageFacts != nil {
		if input.CoverageReview == nil || reviewcoverage.Validate(*input.CoverageFacts, *input.CoverageReview) != nil {
			return "Coverage has not been accepted for the current review evidence."
		}
		if level == CompletenessMostly {
			return "The review is usable, with material questions still open. See remaining work."
		}
	}
	var parts []string
	for _, kind := range gapOrder {
		for _, g := range input.Gaps {
			if g.Kind != kind || g.Count == 0 {
				continue
			}
			if phrase := gapPhrase(g); phrase != "" {
				parts = append(parts, phrase)
			}
		}
	}
	switch {
	case level == CompletenessComplete:
		return "The planned review is complete. Its scope and limitations are documented below."
	case len(parts) == 0:
		return ""
	}
	if len(parts) > 2 {
		parts = parts[:2]
	}
	out := sentence(strings.Join(parts, ", and ")) + "."
	return out
}

// gapOrder puts the gaps that leave work incomplete first.
var gapOrder = []string{
	GapInventoryUnaccounted, GapClaimsOpen, GapLegsUnfinished, GapScansFailed,
	GapLegsPartial, GapWorkersPartial, GapScansMoved,
}

func gapPhrase(g ReportGap) string {
	switch g.Kind {
	case GapInventoryUnaccounted:
		if g.Of > 0 && g.Count < g.Of {
			return fmt.Sprintf("the review didn't use %d of %d automated scan result groups", g.Count, g.Of)
		}
		return "the review didn't use the automated scan results"
	case GapClaimsOpen:
		return fmt.Sprintf("%d %s still open", g.Count, noun(g.Count, "question is", "questions are"))
	case GapLegsUnfinished:
		return fmt.Sprintf("%d planned %s checked", g.Count, noun(g.Count, "area wasn't", "areas weren't"))
	case GapScansFailed:
		return fmt.Sprintf("%d %s", g.Count, noun(g.Count, "scan failed", "scans failed"))
	case GapLegsPartial:
		return fmt.Sprintf("%d %s only partly checked", g.Count, noun(g.Count, "area was", "areas were"))
	case GapWorkersPartial:
		return fmt.Sprintf("%d supporting %s short", g.Count, noun(g.Count, "task stopped", "tasks stopped"))
	case GapScansMoved:
		return fmt.Sprintf("%d %s files that changed while %s ran", g.Count, noun(g.Count, "scan missed", "scans missed"), noun(g.Count, "it", "they"))
	default:
		return ""
	}
}

// askRows state the one decision asked of the reader, that none was recorded
// while review conclusions are still unstated, or that none is needed.
func askRows(ms *measurer, input ReportInput) []measuredRow {
	head := labelProp()
	head.Color = accentColor
	head.Left, head.Right = panelIndent, panelIndent
	rows := textRows(ms, []inlineRun{{Text: "What we need from you"}}, head)

	doProp := props.Text{
		Family: familySans, Style: fontstyle.Bold, Size: sizeAsk, Color: inkColor,
		VerticalPadding: leading(sizeAsk) * 0.6, Left: panelIndent, Right: panelIndent, Bottom: spaceBetweenMetaRow,
	}
	ask := input.Ask
	if ask == nil || strings.TrimSpace(ask.Do) == "" {
		rows = append(rows, textRows(ms, []inlineRun{{Text: noAskLine(input.UnreportedClaims)}}, doProp)...)
		return append(panelRows(rows), spacerRow(spaceAroundPanel))
	}
	effort := "Effort: " + sentence(ask.Effort)
	effortProp := metaProp()
	effortProp.Right = panelIndent
	effortWidth := ms.width(effort, cellStyleKey(effortProp)) + panelIndent + tableCellPadX*2
	rows = append(rows, columnsRow([]cellSpec{
		newCell(ms, contentWidth-effortWidth, []inlineRun{{Text: ask.Do}}, doProp),
		newCell(ms, effortWidth, []inlineRun{{Text: effort}}, effortProp),
	}))
	if why := strings.TrimSpace(ask.Why); why != "" {
		whyProp := bodyProp()
		whyProp.Left, whyProp.Right = panelIndent, panelIndent
		whyProp.Bottom = 0
		rows = append(rows, textRows(ms, []inlineRun{{Text: "Why: ", Color: mutedColor}, {Text: why}}, whyProp)...)
	}
	return append(panelRows(rows), spacerRow(spaceAroundPanel))
}

// checkRows list what the work covered, each with its state in plain words.
func checkRows(ms *measurer, input ReportInput) []measuredRow {
	if len(input.Checks) == 0 {
		return nil
	}
	rows := textRows(ms, []inlineRun{{Text: "What was checked"}}, briefSectionProp())
	rows = append(rows, ruleRow(ruleThin, ruleColor, 0))
	subjectProp := valueProp()
	subjectProp.Size = sizeBody
	subjectProp.VerticalPadding = tableLeading(sizeBody)
	subjectProp.Top, subjectProp.Bottom = tableCellPadY, tableCellPadY
	stateProp := subjectProp
	for _, c := range input.Checks {
		subject, state, tone := checkWords(c)
		stateWidth := ms.width(state, cellStyleKey(stateProp)) + tableCellPadX*2
		rows = append(rows, columnsRow([]cellSpec{
			newCell(ms, contentWidth-stateWidth, []inlineRun{{Text: subject}}, subjectProp),
			newCell(ms, stateWidth, []inlineRun{{Text: state, Color: tone}}, stateProp),
		}), ruleRow(ruleThin, hairlineColor, 0))
	}
	return rows
}

// checkWords writes a check's subject and state for a reader.
func checkWords(c ReportCheck) (subject, state string, color *props.Color) {
	color = inkColor
	switch c.State {
	case CheckPartial:
		color = toneMedium.ink
	case CheckUnchecked:
		color = toneHigh.ink
	}
	switch c.Kind {
	case CheckReview:
		n := c.Held + c.Failed + c.Open
		subject = fmt.Sprintf("%s on %s", sentence(c.Subject), plural(n, "conclusion", "conclusions"))
		var parts []string
		if c.Held > 0 {
			parts = append(parts, fmt.Sprintf("%d confirmed", c.Held))
		}
		if c.Open > 0 {
			parts = append(parts, fmt.Sprintf("%d open", c.Open))
		}
		if c.Failed > 0 {
			parts = append(parts, fmt.Sprintf("%d overturned", c.Failed))
		}
		state = strings.Join(parts, ", ")
	case CheckScans:
		subject = fmt.Sprintf("Automated scans (%d ran)", c.Ran)
		switch {
		case c.Total == 0:
			state = "No results to use"
		case c.Used >= c.Total:
			state = "Results used by the review"
		case c.Used == 0:
			state = "Results not used by the review"
		default:
			state = fmt.Sprintf("%d of %d result groups used", c.Used, c.Total)
		}
		if c.ScansFailed > 0 {
			state += fmt.Sprintf("; %d failed", c.ScansFailed)
		}
	default:
		subject = sentence(c.Subject)
		state = map[string]string{CheckDone: "Checked", CheckPartial: "Partly checked", CheckUnchecked: "Not checked"}[c.State]
	}
	return subject, state, color
}

// briefFooterRows point a reader who wants more to where it starts.
func briefFooterRows(ms *measurer, input ReportInput) []measuredRow {
	n := 0
	for _, f := range input.Findings {
		if f.NeedsAttention() {
			n++
		}
	}
	text := "The working summary starts on the next page."
	if n > 0 {
		label := strings.ToLower(findingsLabel(input))
		if n == 1 {
			label = singular(label)
		}
		text = fmt.Sprintf("%d %s %s attention; the working summary on the next page lists %s.",
			n, label, noun(n, "needs", "need"), noun(n, "it", "them"))
	}
	prop := metaProp()
	prop.Top = spaceAboveSubsection
	return textRows(ms, []inlineRun{{Text: text}}, prop)
}

func briefSectionProp() props.Text {
	return props.Text{
		Family: familySans, Style: fontstyle.Bold, Size: sizeMinorHead, Color: inkColor,
		VerticalPadding: tableLeading(sizeMinorHead), Top: spaceAboveSubsection, Bottom: tableCellPadY,
	}
}

func gaugeQuestionProp() props.Text {
	p := labelProp()
	p.Top = spaceAroundPanel
	p.Bottom = 0
	return p
}

func scaleLabelProp() props.Text {
	p := metaProp()
	p.Size = sizeTableHead
	p.VerticalPadding = tableLeading(sizeTableHead)
	p.Bottom = 0
	return p
}

func gaugeLevelProp() props.Text {
	return props.Text{
		Family: familySans, Style: fontstyle.Bold, Size: sizeGaugeLevel, Color: inkColor,
		VerticalPadding: tableLeading(sizeGaugeLevel), Top: spaceBetweenMetaRow * 2, Bottom: spaceBetweenMetaRow,
	}
}

func gaugeMeansProp() props.Text {
	p := bodyProp()
	p.Bottom = spaceBetweenMetaRow
	return p
}

func gaugeBasisProp() props.Text {
	p := metaProp()
	p.Bottom = spaceAroundPanel
	return p
}

// scaleLabels names each segment under its bar, the chosen ones in ink.
func scaleLabels(ms *measurer, width float64, labels []string, chosen map[string]bool) cellSpec {
	each := width / float64(len(labels))
	cells := make([]cellSpec, 0, len(labels))
	for _, l := range labels {
		p := scaleLabelProp()
		if chosen[l] {
			p.Color = inkColor
			p.Style = fontstyle.Bold
		}
		cells = append(cells, newCell(ms, each, []inlineRun{{Text: l}}, p))
	}
	b := newBand(cells)
	return cellSpec{comp: b, width: width, height: b.height}
}

// sentence capitalizes the first letter of a host-composed phrase.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 'a' - 'A'
	}
	return string(r)
}
