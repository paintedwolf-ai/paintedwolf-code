package report

import (
	"strconv"
	"strings"
)

// Identifiers are printed whole here; the running footer and appendix shorten
// them.

// productSite is where a reader who was not in the chat can find the app.
const (
	productSite      = "https://paintedwolf.ai"
	productSiteLabel = "paintedwolf.ai"
)

func colophonBlocks(ms *measurer, input ReportInput) []block {
	started := ""
	if input.StartedAt != "" {
		started = formatTimestamp(input.StartedAt)
	}
	facts := []fact{
		{label: "Produced by", value: []inlineRun{
			{Text: "Painted Wolf Code, "},
			{Text: productSiteLabel, Link: productSite},
		}},
		textFact("Workflow", workflowLabel(input)),
		textFact("Run", input.RunID),
		textFact("Project", input.Project),
		textFact("Head", input.HeadSHA),
		textFact("Started", started),
		textFact("Completed", formatTimestamp(input.CompletedAt)),
		textFact("Model", modelLabel(input.Workforce)),
		textFact("Workers", agentsLabel(input.Workforce)),
		textFact("Scanners", scannersLabel(input.Scan)),
		textFact("Evidence", evidenceLabel(input)),
	}

	rows := factTable(ms, facts)
	rows = append(rows, spacerRow(spaceAfterParagraph))
	rows = append(rows, textRows(ms, []inlineRun{{Text: renderingNote}}, metaProp())...)
	return []block{sectionTitle(ms, sectionColophon), rowsBlock(rows...)}
}

// renderingNote tells a reader what kind of artefact this is.
const renderingNote = "This document is a projection of the run's durable records — its closeout, review verdicts, " +
	"scan findings, evidence ledger, and visuals — rendered deterministically: the same records produce " +
	"byte-identical output. The records, not the document, are the record of the outcome."

func workflowLabel(input ReportInput) string {
	if input.Workflow == nil {
		return ""
	}
	id := strings.TrimSpace(input.Workflow.ID)
	if id == "" {
		return ""
	}
	if v := strings.TrimSpace(input.Workflow.Version); v != "" {
		return id + " " + v
	}
	return id
}

func modelLabel(w *ReportWorkforce) string {
	if w == nil {
		return ""
	}
	model := strings.TrimSpace(w.Model)
	provider := strings.TrimSpace(w.Provider)
	switch {
	case model != "" && provider != "":
		return model + " via " + provider
	case model != "":
		return model
	default:
		return provider
	}
}

func agentsLabel(w *ReportWorkforce) string {
	if w == nil || len(w.Agents) == 0 {
		return ""
	}
	parts := make([]string, 0, len(w.Agents))
	total := 0
	for _, a := range w.Agents {
		if a.Legs <= 0 || strings.TrimSpace(a.Type) == "" {
			continue
		}
		total += a.Legs
		parts = append(parts, strings.TrimSpace(a.Type)+" ×"+strconv.Itoa(a.Legs))
	}
	if len(parts) == 0 {
		return ""
	}
	return plural(total, "leg", "legs") + ": " + strings.Join(parts, ", ")
}

func scannersLabel(scan *ReportScan) string {
	if scan == nil {
		return ""
	}
	return strings.Join(scan.Scanners, ", ")
}

func evidenceLabel(input ReportInput) string {
	n := len(input.Evidence)
	if n == 0 {
		return ""
	}
	if input.EvidenceTotal > n {
		return plural(input.EvidenceTotal, "record", "records") + " observed, " + strconv.Itoa(n) + " listed"
	}
	return plural(n, "record", "records") + " observed"
}
