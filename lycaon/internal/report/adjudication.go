package report

import (
	"strings"
)

// Every verdict the subject recorded is rendered, in phase order. No member
// is named here: schemas are declared per workflow in YAML. A claims-typed
// member is set as records, since each claim traces to its own evidence.

func adjudicationBlocks(ms *measurer, verdicts []ReportVerdict) []block {
	out := []block{sectionTitle(ms, sectionAdjudication)}
	for _, v := range verdicts {
		out = append(out, verdictBlocks(ms, v)...)
	}
	return out
}

// verdictBlocks sets one phase's verdict: its heading, the decision on the
// line that says when it was stamped, its fields, then its claims.
func verdictBlocks(ms *measurer, v ReportVerdict) []block {
	var out []block
	if heading := humanize(v.Phase); heading != "" {
		out = append(out, subsectionTitle(ms, heading))
	}

	if decision := strings.TrimSpace(v.Decision); decision != "" {
		var rows []measuredRow
		rows = append(rows, chipStripRow(ms, []chipLabel{newTonedChip(ms, decision, toneAccent)}, 0, spaceBetweenMetaRow))
		if note := verdictNote(v); note != "" {
			rows = append(rows, textRows(ms, []inlineRun{{Text: note}}, metaProp())...)
		}
		rows = append(rows, spacerRow(spaceBetweenMetaRow))
		out = append(out, rowsBlock(rows...))
	}

	for _, f := range v.Fields {
		rows := labelledValue(ms, humanize(f.Name), f.Value)
		if len(rows) > 0 {
			out = append(out, rowsBlock(rows...))
		}
	}

	if len(v.Claims) > 0 {
		out = append(out, claimsBlocks(ms, v.Claims)...)
		return out
	}
	return append(out, rowsBlock(spacerRow(spaceAfterParagraph)))
}

// verdictNote is the line under a decision: the phase's own label for what it
// was doing, and when it recorded the result.
func verdictNote(v ReportVerdict) string {
	var parts []string
	if label := strings.TrimSpace(v.Label); label != "" {
		parts = append(parts, label)
	}
	if at := strings.TrimSpace(v.RecordedAt); at != "" {
		parts = append(parts, "recorded "+formatTimestamp(at))
	}
	return strings.Join(parts, " · ")
}

// claimsBlocks sets each claim as its own record: the claim id on its ground,
// the status word the phase gave it, the statement at the measure it can get,
// and the evidence it rests on beneath.
func claimsBlocks(ms *measurer, claims []ReportVerdictClaim) []block {
	hasStatus := false
	for _, c := range claims {
		if strings.TrimSpace(c.Status) != "" {
			hasStatus = true
			break
		}
	}

	records := make([]tableRecord, 0, len(claims))
	for _, c := range claims {
		statement := strings.TrimSpace(c.Statement)
		if statement == "" && strings.TrimSpace(c.ID) == "" {
			continue
		}
		cells := [][]inlineRun{{{Text: strings.TrimSpace(c.ID), Family: familyMono}}}
		if hasStatus {
			cells = append(cells, []inlineRun{{Text: strings.TrimSpace(c.Status)}})
		}
		var said []inlineRun
		if title := strings.TrimSpace(c.Title); title != "" {
			said = append(said, inlineRun{Text: strings.TrimSuffix(title, ".") + ". ", Bold: true})
		}
		cells = append(cells, append(said, inlineRun{Text: statement}))
		rec := tableRecord{cells: cells}
		if cites := claimCitationRuns(c.CitedEvidence); len(cites) > 0 {
			rec.extra = [][]inlineRun{cites}
		}
		if len(c.ScanGroupIDs) > 0 {
			rec.extra = append(rec.extra, []inlineRun{{Text: "Inventory groups: " + strings.Join(c.ScanGroupIDs, ", "), Family: familyMono}})
		}
		records = append(records, rec)
	}
	if len(records) == 0 {
		return nil
	}

	headers := []string{"Claim", "Statement"}
	chipColumn := noChipColumn
	if hasStatus {
		headers = []string{"Claim", "Status", "Statement"}
		chipColumn = 1
	}
	t := newTable(ms, headers, records, chipColumn, nil)
	return t.blocks()
}

// claimCitationRuns lists the evidence one claim rests on: handles as
// handles, places as path:line.
func claimCitationRuns(cites []ReportClaimCitation) []inlineRun {
	var out []inlineRun
	for _, c := range cites {
		loc := strings.TrimSpace(c.Handle)
		if loc == "" {
			loc = sourceLocation(c.Path, c.Line)
		}
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
