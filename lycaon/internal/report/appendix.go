package report

import (
	"fmt"
	"strings"

	"github.com/johnfercher/maroto/v2/pkg/components/image"
	"github.com/johnfercher/maroto/v2/pkg/consts/extension"
	"github.com/johnfercher/maroto/v2/pkg/consts/fontstyle"
	"github.com/johnfercher/maroto/v2/pkg/props"
)

const (
	maxExcerptLines = 8
	maxExcerptChars = 160

	visualHeight = 62.0
)

type kindGroup struct {
	kind string
	rows []ReportEvidence
}

// groupByKind preserves first-seen kind order.
func groupByKind(evidence []ReportEvidence) []kindGroup {
	var out []kindGroup
	at := map[string]int{}
	for _, e := range evidence {
		kind := strings.ToLower(strings.TrimSpace(e.Kind))
		if kind == "" {
			kind = "other"
		}
		i, ok := at[kind]
		if !ok {
			i = len(out)
			at[kind] = i
			out = append(out, kindGroup{kind: kind})
		}
		out[i].rows = append(out[i].rows, e)
	}
	return out
}

func evidenceAppendixBlocks(ms *measurer, evidence []ReportEvidence, total int, captures []block) []block {
	out := []block{sectionTitle(ms, sectionEvidence)}
	if caption := evidenceCaption(evidence, total); caption != "" {
		out = append(out, rowsBlock(textRows(ms, []inlineRun{{Text: caption}}, metaProp())...))
	}
	for _, g := range groupByKind(evidence) {
		out = append(out, kindBlocks(ms, g)...)
	}
	return append(out, captures...)
}

func evidenceCaption(evidence []ReportEvidence, total int) string {
	n := len(evidence)
	if n == 0 {
		return ""
	}
	var parts []string
	if total > n {
		parts = append(parts, fmt.Sprintf("%d of %s listed", n, plural(total, "record", "records")))
	} else {
		parts = append(parts, plural(n, "record", "records"))
	}
	cited := 0
	for _, e := range evidence {
		if e.Cited() {
			cited++
		}
	}
	switch {
	case cited == n:
		parts = append(parts, "all cited")
	case cited > 0:
		parts = append(parts, plural(cited, "cited", "cited"))
	default:
		parts = append(parts, "none cited")
	}
	groups := groupByKind(evidence)
	if len(groups) > 1 {
		kinds := make([]string, 0, len(groups))
		for _, g := range groups {
			kinds = append(kinds, fmt.Sprintf("%d %s", len(g.rows), g.kind))
		}
		parts = append(parts, strings.Join(kinds, ", "))
	}
	return strings.Join(parts, " · ")
}

func kindBlocks(ms *measurer, g kindGroup) []block {
	if len(g.rows) == 0 {
		return nil
	}
	cols := kindColumns(g.rows)
	kept, notes := partitionEvidenceColumns(cols, g.rows)
	if len(kept) == 0 {
		return nil
	}

	records := make([]tableRecord, 0, len(g.rows))
	for _, e := range g.rows {
		cells := make([][]inlineRun, 0, len(kept))
		for _, c := range kept {
			cells = append(cells, c.runs(e))
		}
		rec := tableRecord{cells: cells}
		for _, line := range excerptLines(e.Excerpt) {
			rec.extra = append(rec.extra, []inlineRun{{Text: line, Family: familyMono}})
		}
		records = append(records, rec)
	}

	headers := make([]string, 0, len(kept))
	for _, c := range kept {
		headers = append(headers, c.header)
	}
	t := newTable(ms, headers, records, noChipColumn, nil)
	t.extraFamily = familyMono

	title := plural(len(g.rows), humanize(g.kind)+" record", humanize(g.kind)+" records")
	if len(notes) > 0 {
		title += " · " + strings.Join(notes, " · ")
	}
	return append([]block{subsectionTitle(ms, title)}, t.blocks()...)
}

type evidenceColumn struct {
	header    string
	runs      func(ReportEvidence) []inlineRun
	plain     func(ReportEvidence) string
	summarise func(value string, n int) string
}

func kindColumns(rows []ReportEvidence) []evidenceColumn {
	cols := []evidenceColumn{{
		header: "Handle",
		plain:  func(e ReportEvidence) string { return e.Handle },
		runs: func(e ReportEvidence) []inlineRun {
			return []inlineRun{{Text: e.Handle, Family: familyMono}}
		},
	}}
	// The leg is a column only when the records name one, and collapses into
	// the caption when every row shares it.
	if anyRow(rows, func(e ReportEvidence) bool { return strings.TrimSpace(e.Scope) != "" }) {
		cols = append(cols, evidenceColumn{
			header: "Leg",
			plain:  func(e ReportEvidence) string { return shortID(e.Scope) },
			runs: func(e ReportEvidence) []inlineRun {
				return []inlineRun{{Text: shortID(e.Scope), Family: familyMono, Color: mutedColor}}
			},
			summarise: func(value string, _ int) string {
				if value == "" {
					return ""
				}
				return "one leg"
			},
		})
	}
	if anyRow(rows, func(e ReportEvidence) bool { return strings.TrimSpace(e.Path) != "" }) {
		cols = append(cols, evidenceColumn{
			header: "Location",
			plain:  func(e ReportEvidence) string { return sourceSpan(e.Path, e.Line, e.LineEnd) },
			runs: func(e ReportEvidence) []inlineRun {
				return []inlineRun{{Text: sourceSpan(e.Path, e.Line, e.LineEnd), Family: familyMono}}
			},
		})
	}
	if anyRow(rows, func(e ReportEvidence) bool { return strings.TrimSpace(e.URL) != "" }) {
		cols = append(cols, evidenceColumn{
			header: "Page",
			plain:  func(e ReportEvidence) string { return strings.TrimSpace(e.URL) },
			runs: func(e ReportEvidence) []inlineRun {
				return []inlineRun{{Text: strings.TrimSpace(e.URL), Family: familySans, Color: accentColor}}
			},
		})
	}
	if anyRow(rows, func(e ReportEvidence) bool { return e.Matches > 0 }) {
		cols = append(cols, evidenceColumn{
			header: "Matches",
			plain:  matchesLabel,
			runs: func(e ReportEvidence) []inlineRun {
				return []inlineRun{{Text: matchesLabel(e)}}
			},
		})
	}
	cols = append(cols,
		evidenceColumn{
			header: "Cited by",
			plain:  func(e ReportEvidence) string { return strings.Join(e.CitedBy, ", ") },
			runs: func(e ReportEvidence) []inlineRun {
				return []inlineRun{{Text: strings.Join(e.CitedBy, ", "), Family: familyMono, Color: accentColor}}
			},
			summarise: func(value string, n int) string {
				if value == "" {
					return "none cited"
				}
				return "all cited by " + value
			},
		},
		evidenceColumn{
			header: "Trust",
			plain:  func(e ReportEvidence) string { return strings.TrimSpace(e.TrustTier) },
			runs: func(e ReportEvidence) []inlineRun {
				return []inlineRun{{Text: strings.TrimSpace(e.TrustTier)}}
			},
			summarise: func(value string, _ int) string { return "all " + value },
		},
	)
	return cols
}

func anyRow(rows []ReportEvidence, pred func(ReportEvidence) bool) bool {
	for _, e := range rows {
		if pred(e) {
			return true
		}
	}
	return false
}

func matchesLabel(e ReportEvidence) string {
	if e.Matches == 0 {
		return ""
	}
	out := plural(e.Matches, "match", "matches")
	if e.MatchFiles > 0 {
		out += " in " + plural(e.MatchFiles, "file", "files")
	}
	return out
}

// excerptLines clips excerpts to the report measure.
func excerptLines(excerpt string) []string {
	excerpt = strings.TrimRight(excerpt, "\n")
	if strings.TrimSpace(excerpt) == "" {
		return nil
	}
	lines := strings.Split(excerpt, "\n")
	var out []string
	for i, ln := range lines {
		if i == maxExcerptLines {
			out = append(out, fmt.Sprintf("… %d more %s", len(lines)-i, noun(len(lines)-i, "line", "lines")))
			break
		}
		ln = strings.TrimRight(preserveIndent(ln), " ")
		if strings.TrimSpace(ln) == "" {
			continue
		}
		out = append(out, clip(ln, maxExcerptChars))
	}
	return out
}

// partitionEvidenceColumns moves constant values into captions.
func partitionEvidenceColumns(cols []evidenceColumn, evidence []ReportEvidence) (kept []evidenceColumn, notes []string) {
	for _, c := range cols {
		first := c.plain(evidence[0])
		constant := true
		for _, e := range evidence[1:] {
			if c.plain(e) != first {
				constant = false
				break
			}
		}
		if !constant {
			kept = append(kept, c)
			continue
		}
		if c.summarise == nil {
			if first != "" {
				kept = append(kept, c)
			}
			continue
		}
		if note := c.summarise(first, len(evidence)); note != "" {
			notes = append(notes, note)
		}
	}
	return kept, notes
}

func sourcesBlocks(ms *measurer, sources []ReportSource) []block {
	records := make([]tableRecord, 0, len(sources))
	for _, s := range sources {
		u := strings.TrimSpace(s.URL)
		if u == "" {
			continue
		}
		title := strings.TrimSpace(s.Title)
		name := title
		if name == "" {
			name = u
		}
		rec := tableRecord{cells: [][]inlineRun{
			{{Text: name, Family: familySans}},
			{{Text: hostOf(u), Family: familySans, Color: mutedColor}},
			{{Text: strings.Join(s.CitedBy, ", "), Family: familyMono, Color: accentColor}},
		}}
		if title != "" {
			rec.extra = [][]inlineRun{{{Text: u, Family: familySans, Color: accentColor}}}
		}
		records = append(records, rec)
	}
	if len(records) == 0 {
		return nil
	}
	t := newTable(ms, []string{"Source", "Host", "Cited by"}, records, noChipColumn, nil)
	return append([]block{sectionTitle(ms, sectionSources)}, t.blocks()...)
}

func visualsBlocks(ms *measurer, renders []block) []block {
	if len(renders) == 0 {
		return nil
	}
	return append([]block{sectionTitle(ms, sectionVisuals)}, renders...)
}

// artifactSectionBlocks deduplicates artifacts by ID.
func artifactSectionBlocks(ms *measurer, arts []ReportArtifact) (renders, captures []block) {
	featured, captured := partitionArtifacts(arts)
	seen := map[string]struct{}{}
	return artifactBlocks(ms, featured, seen), artifactBlocks(ms, captured, seen)
}

func artifactBlocks(ms *measurer, arts []ReportArtifact, seen map[string]struct{}) []block {
	captionProp := props.Text{
		Family:          familySans,
		Style:           fontstyle.Bold,
		Size:            sizeMeta,
		Color:           inkColor,
		VerticalPadding: tableLeading(sizeMeta),
		Top:             spaceAroundCode,
		Bottom:          tableCellPadY,
	}
	pointerProp := captionProp
	pointerProp.Style = fontstyle.Normal
	pointerProp.Color = mutedColor

	var out []block
	for _, a := range arts {
		id := strings.TrimSpace(a.ID)
		if id == "" || len(a.Bytes) == 0 {
			continue
		}
		caption := strings.TrimSpace(a.Caption)
		if caption == "" {
			caption = id
		}

		if _, ok := seen[id]; ok {
			text := fmt.Sprintf("%s — %s", caption, seeAppendixPointer)
			out = append(out, rowsBlock(textRows(ms, []inlineRun{{Text: text}}, pointerProp)...))
			continue
		}
		ext, ok := mimeToExtension(a.Mime)
		if !ok {
			continue
		}
		seen[id] = struct{}{}

		rows := textRows(ms, []inlineRun{{Text: caption}}, captionProp)
		rows = append(rows, measuredRow{
			row:    image.NewFromBytesRow(visualHeight, a.Bytes, ext, props.Rect{Percent: 100, Center: true}),
			height: visualHeight,
		}, spacerRow(spaceAfterParagraph))
		// Keep visuals with their captions.
		out = append(out, rowsBlock(rows...))
	}
	return out
}

func mimeToExtension(mime string) (extension.Type, bool) {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/png":
		return extension.Png, true
	case "image/jpeg":
		return extension.Jpeg, true
	default:
		return "", false
	}
}

// partitionArtifacts separates renders from captures.
func partitionArtifacts(arts []ReportArtifact) (renders, captures []ReportArtifact) {
	for _, a := range arts {
		if len(a.Bytes) == 0 || strings.TrimSpace(a.ID) == "" {
			continue
		}
		if strings.TrimSpace(a.EvidenceHandle) != "" {
			captures = append(captures, a)
			continue
		}
		renders = append(renders, a)
	}
	return renders, captures
}
