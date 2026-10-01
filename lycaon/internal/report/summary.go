package report

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/johnfercher/maroto/v2/pkg/consts/fontstyle"
)

// The second page is the working summary. Every part is a declared field or a
// host count, never text read out of the narrative.

func summaryBlocks(ms *measurer, input ReportInput) []block {
	var out []block
	out = append(out, boxedListBlocks(ms, "Why the report was not accepted", defectItems(input.Defects))...)
	if rows := summaryProseRows(ms, input); len(rows) > 0 {
		out = append(out, rowsBlock(rows...))
	}
	out = append(out, ratingBlocks(ms, input)...)
	out = append(out, attentionBlocks(ms, input)...)
	out = append(out, claimListBlocks(ms, "Open questions", input.Claims, ClaimOpen)...)
	out = append(out, claimListBlocks(ms, "Overturned by the review", input.Claims, ClaimFailed)...)
	out = append(out, claimListBlocks(ms, "Confirmed by the review", input.Claims, ClaimHeld)...)
	out = append(out, soundBlocks(ms, input)...)
	out = append(out, boxedListBlocks(ms, sectionLimits, notCoveredItems(input))...)
	if len(input.Coverage) > 0 {
		out = append(out, subsectionTitle(ms, "Recorded coverage"))
		var records []tableRecord
		for _, item := range input.Coverage {
			records = append(records, tableRecord{cells: [][]inlineRun{{{Text: item.Subject}}, {{Text: item.Status}}, {{Text: item.Detail}}}})
		}
		out = append(out, newTable(ms, []string{"Subject", "Status", "Recorded facts"}, records, -1, nil).blocks()...)
	}
	if len(out) == 0 {
		return nil
	}
	return append([]block{sectionTitle(ms, sectionSummary)}, out...)
}

// summaryProseRows set the closeout's headline and its summary.
func summaryProseRows(ms *measurer, input ReportInput) []measuredRow {
	var rows []measuredRow
	if headline := strings.TrimSpace(input.Headline); headline != "" {
		prop := bodyProp()
		prop.Style = fontstyle.Bold
		prop.Bottom = spaceBetweenMetaRow
		rows = append(rows, textRows(ms, []inlineRun{{Text: headline}}, prop)...)
	}
	if text := strings.TrimSpace(input.Summary); text != "" {
		prop := bodyProp()
		prop.Bottom = spaceAfterParagraph
		rows = append(rows, textRows(ms, []inlineRun{{Text: text}}, prop)...)
	}
	return rows
}

// ratingBlocks show each rated finding's answers and the level they decided,
// so a reader can check the rating rather than take it.
func ratingBlocks(ms *measurer, input ReportInput) []block {
	b := input.Brief
	if b == nil || len(b.Rated) == 0 {
		return nil
	}
	headers := append([]string{singular(findingsLabel(input))}, b.Dimensions...)
	headers = append(headers, "Level", "Answered by")
	records := make([]tableRecord, 0, len(b.Rated))
	for _, r := range b.Rated {
		cells := [][]inlineRun{{{Text: r.Title}}}
		if r.Number > 0 {
			cells[0] = append([]inlineRun{{Text: strconv.Itoa(r.Number) + ". ", Color: mutedColor}}, cells[0]...)
		}
		for _, a := range r.Answers {
			cells = append(cells, []inlineRun{{Text: a}})
		}
		level := b.Levels[r.Best].Label
		if r.Worst != r.Best {
			level += " to " + strings.ToLower(b.Levels[r.Worst].Label)
		}
		source := "Closeout"
		switch {
		case r.Unreported:
			source = "Not reported"
		case r.Adjudicated:
			source = "Review"
		}
		cells = append(cells, []inlineRun{{Text: level}}, []inlineRun{{Text: source}})
		records = append(records, tableRecord{cells: cells})
	}
	out := []block{subsectionTitle(ms, "Why this rating")}
	return append(out, newTable(ms, headers, records, noChipColumn, nil).blocks()...)
}

// attentionBlocks list the findings that are work to do or risks kept on
// purpose, numbered as the findings section numbers them.
func attentionBlocks(ms *measurer, input ReportInput) []block {
	rows := findingsGlanceRows(ms, input)
	if len(rows) == 0 {
		return nil
	}
	return []block{subsectionTitle(ms, "Needs attention"), rowsBlock(rows...)}
}

// claimListBlocks list the claims of one class with the word the review gave.
func claimListBlocks(ms *measurer, title string, claims []ReportClaim, class string) []block {
	var records []tableRecord
	for _, c := range claims {
		if c.Class != class {
			continue
		}
		status := c.Status
		if c.Dropped {
			status = "Not revisited"
		}
		records = append(records, tableRecord{cells: [][]inlineRun{
			{{Text: chipText(status)}},
			{{Text: claimTitle(c)}},
			{{Text: c.ID, Family: familyMono, Color: mutedColor}},
		}})
	}
	if len(records) == 0 {
		return nil
	}
	tone := func(string) chipTone {
		switch class {
		case ClaimFailed:
			return toneHigh
		case ClaimHeld:
			return toneNeutral
		default:
			return toneMedium
		}
	}
	out := []block{subsectionTitle(ms, title)}
	return append(out, newTable(ms, []string{"Status", "Claim", "Id"}, records, 0, tone).blocks()...)
}

func claimTitle(c ReportClaim) string {
	if t := strings.TrimSpace(c.Title); t != "" {
		return t
	}
	return c.ID
}

// soundBlocks list the surfaces the closeout examined and found sound, by
// their finding numbers.
func soundBlocks(ms *measurer, input ReportInput) []block {
	var records []tableRecord
	for i, f := range input.Findings {
		if f.Disposition == DispositionHeld {
			records = append(records, tableRecord{cells: [][]inlineRun{
				{{Text: strconv.Itoa(i + 1), Family: familyMono, Color: mutedColor}},
				{{Text: f.Title}},
			}})
		}
	}
	if len(records) == 0 {
		return nil
	}
	out := []block{subsectionTitle(ms, "Examined and sound")}
	return append(out, newTable(ms, []string{"Finding", "What was sound"}, records, noChipColumn, nil).blocks()...)
}

// notCoveredItems states everything the work did not cover: the host's gaps
// with what they touch, the scanner groups set aside and why, the scanner's
// own standing limits, and the areas the closeout declared it did not examine.
func notCoveredItems(input ReportInput) []string {
	var out []string
	for _, kind := range append(append([]string(nil), gapOrder...), GapScansStanding) {
		for _, g := range input.Gaps {
			if g.Kind == kind && g.Count > 0 {
				if item := gapItem(g); item != "" {
					out = append(out, item)
				}
			}
		}
	}
	if inv := input.Inventory; inv != nil && inv.Total > 0 {
		out = append(out, fmt.Sprintf("Scanner inventory: %d of %d result groups assessed by a claim or finding, %d set aside, %d unaccounted.",
			inv.Linked, inv.Total, inv.SetAside, inv.Unaccounted))
		for _, sa := range inv.SetAsides {
			out = append(out, fmt.Sprintf("%s set aside: %s.", plural(sa.Groups, "result group", "result groups"), strings.TrimSuffix(sa.Reason, ".")))
		}
	}
	for _, l := range input.Limits {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, "Not examined, per the closeout: "+strings.TrimSuffix(l, ".")+".")
		}
	}
	return out
}

func gapItem(g ReportGap) string {
	names := strings.Join(g.Names, ", ")
	switch g.Kind {
	case GapInventoryUnaccounted:
		return fmt.Sprintf("%d of %d scanner result groups have no assessment and no set-aside. Unassessed is not cleared.", g.Count, g.Of)
	case GapClaimsOpen:
		return fmt.Sprintf("%s still open: %s.", plural(g.Count, "claim", "claims"), names)
	case GapLegsUnfinished:
		return "Planned areas not checked: " + names + "."
	case GapLegsPartial:
		return "Areas only partly checked: " + names + "."
	case GapWorkersPartial:
		return fmt.Sprintf("%s ended partial: %s.", plural(g.Count, "supporting task", "supporting tasks"), names)
	case GapScansFailed:
		return "Scans that failed: " + names + "."
	case GapScansMoved:
		return fmt.Sprintf("%s: %s changed while the scan ran and %s not rescanned.",
			names, plural(g.Detail, "file", "files"), noun(g.Detail, "was", "were"))
	case GapScansStanding:
		return fmt.Sprintf("Known scanner limits, the same on every run: %s could not fully analyze %s in %s.",
			names, plural(g.Detail, "construct", "constructs"), plural(g.DetailFiles, "file", "files"))
	default:
		return ""
	}
}

// defectItems state each failed check as the check reported it, with a sample
// of what it named.
func defectItems(defects []ReportDefect) []string {
	out := make([]string, 0, len(defects))
	for _, d := range defects {
		item := sentence(strings.TrimSuffix(strings.TrimSpace(d.Reason), ".")) + "."
		if len(d.Subjects) > 0 {
			item += " " + strings.Join(d.Subjects, "; ")
			if more := d.Count - len(d.Subjects); more > 0 {
				item += fmt.Sprintf("; and %d more", more)
			}
			item += "."
		}
		out = append(out, item)
	}
	return out
}

// boxedListBlocks box a list a reader who stops at the summary must still
// see: why the report was not accepted, and what the work did not cover.
func boxedListBlocks(ms *measurer, title string, items []string) []block {
	if len(items) == 0 {
		return nil
	}
	head := eyebrowProp()
	head.Left = panelIndent
	rows := textRows(ms, []inlineRun{{Text: title}}, head)
	for _, item := range items {
		prop := bodyProp()
		prop.Size = sizeTableCell
		prop.VerticalPadding = tableLeading(sizeTableCell)
		prop.Left = panelIndent
		prop.Right = panelIndent
		prop.Bottom = spaceBetweenListItem
		rows = append(rows, textRows(ms, []inlineRun{
			{Text: "– ", Family: familySans, Color: mutedColor},
			{Text: item, Family: familySans},
		}, prop)...)
	}
	return []block{rowsBlock(panelRows(rows)...)}
}
