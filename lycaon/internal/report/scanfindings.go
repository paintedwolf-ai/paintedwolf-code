package report

import (
	"sort"
	"strconv"
	"strings"
)

// Rows group by rule, ordered by severity rank then by how often the rule
// fired. Severity takes the chip treatment because it comes from a scan record
// rather than from model prose, so its rank is known. A group states its first
// files and counts the rest; the scan ledger holds the full set.

// maxFilesPerRule bounds the files one rule names before it starts counting.
const maxFilesPerRule = 10

// ruleGroup is one rule and every listed row under it.
type ruleGroup struct {
	rule     ReportScanRule
	severity string
	rows     []ReportScanRow
}

// groupScanRows keeps each rule's highest severity.
func groupScanRows(rows []ReportScanRow, rules []ReportScanRule) []ruleGroup {
	byID := make(map[string]ReportScanRule, len(rules))
	for _, r := range rules {
		id := r.GroupID
		if id == "" {
			id = strings.TrimSpace(r.ID)
		}
		byID[id] = r
	}

	groups := map[string]*ruleGroup{}
	var order []string
	for _, f := range rows {
		id := f.GroupID
		if id == "" {
			id = strings.TrimSpace(f.RuleID)
		}
		g, ok := groups[id]
		if !ok {
			rule, known := byID[id]
			if !known {
				rule = ReportScanRule{ID: id}
			}
			g = &ruleGroup{rule: rule, severity: f.Severity}
			groups[id] = g
			order = append(order, id)
		}
		if SeverityRank(f.Severity) < SeverityRank(g.severity) {
			g.severity = f.Severity
		}
		g.rows = append(g.rows, f)
	}

	out := make([]ruleGroup, 0, len(order))
	for _, id := range order {
		g := groups[id]
		sort.SliceStable(g.rows, func(i, j int) bool {
			if g.rows[i].File != g.rows[j].File {
				return g.rows[i].File < g.rows[j].File
			}
			return g.rows[i].Line < g.rows[j].Line
		})
		if g.rule.Description == "" {
			g.rule.Description = sharedMessage(g.rows)
		}
		out = append(out, *g)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := SeverityRank(out[i].severity), SeverityRank(out[j].severity)
		if ri != rj {
			return ri < rj
		}
		if len(out[i].rows) != len(out[j].rows) {
			return len(out[i].rows) > len(out[j].rows)
		}
		return out[i].rule.ID < out[j].rule.ID
	})
	return out
}

func sharedMessage(rows []ReportScanRow) string {
	if len(rows) == 0 {
		return ""
	}
	first := strings.TrimSpace(rows[0].Message)
	for _, r := range rows[1:] {
		if strings.TrimSpace(r.Message) != first {
			return ""
		}
	}
	return first
}

func scanBlocks(ms *measurer, rows []ReportScanRow, rules []ReportScanRule, scan *ReportScan) []block {
	out := []block{sectionTitle(ms, sectionScan)}
	if note := scanScopeNote(scan, len(rows), len(rules)); note != "" {
		out = append(out, rowsBlock(textRows(ms, []inlineRun{{Text: note}}, metaProp())...))
	}
	if scan != nil {
		for _, execution := range scan.Executions {
			text := execution.Scanner + " · " + execution.ID + " · " + execution.Status
			if execution.Error != "" {
				text += " · " + execution.Error
			}
			out = append(out, rowsBlock(textRows(ms, []inlineRun{{Text: text}}, metaProp())...))
		}
	}
	if len(rows) == 0 {
		return out
	}

	groups := groupScanRows(rows, rules)
	records := make([]tableRecord, 0, len(groups))
	for _, g := range groups {
		records = append(records, ruleRecord(g))
	}
	t := newTable(ms, []string{"Severity", "Rule or advisory", "Occurrences"}, records, 0, severityTone)
	return append(out, t.blocks()...)
}

// ruleRecord is one group as a table row: the chip, the rule's id with its
// scanner, and how often it fired; then the description and the locations
// beneath, and per-row messages when the rows do not share one.
func ruleRecord(g ruleGroup) tableRecord {
	occurrences := g.rule.Occurrences
	if occurrences <= 0 {
		occurrences = len(g.rows)
	}
	ruleCell := []inlineRun{{Text: g.rule.DisplayID(), Family: familyMono}}
	if scanner := strings.TrimSpace(g.rule.Scanner); scanner != "" {
		ruleCell = append(ruleCell,
			inlineRun{Text: "  ", Family: familySans},
			inlineRun{Text: scanner, Family: familySans, Color: mutedColor},
		)
	}
	rec := tableRecord{cells: [][]inlineRun{
		{{Text: g.severity}},
		ruleCell,
		{{Text: plural(occurrences, "occurrence", "occurrences")}},
	}}
	if g.rule.GroupID != "" {
		rec.extra = append(rec.extra, []inlineRun{{Text: g.rule.GroupID, Family: familyMono, Color: mutedColor}})
	}
	if g.rule.Package != "" {
		rec.extra = append(rec.extra, []inlineRun{{Text: g.rule.Package, Family: familyMono}})
	}
	for _, detail := range g.rule.Details {
		rec.extra = append(rec.extra, []inlineRun{{Text: detail, Family: familySans}})
	}
	if desc := strings.TrimSpace(g.rule.Description); desc != "" {
		rec.extra = append(rec.extra, []inlineRun{{Text: desc, Family: familySans}})
	}
	rec.extra = append(rec.extra, locationRuns(g.rows))
	if g.rule.LocationCount > len(g.rows) {
		rec.extra = append(rec.extra, []inlineRun{{Text: plural(g.rule.LocationCount-len(g.rows), "additional location in scan ledger", "additional locations in scan ledger"), Family: familySans}})
	}
	if g.rule.Description == "" {
		for _, f := range g.rows {
			if msg := strings.TrimSpace(f.Message); msg != "" {
				rec.extra = append(rec.extra, []inlineRun{
					{Text: sourceLocation(f.File, f.Line), Family: familyMono},
					{Text: " — " + msg, Family: familySans},
				})
			}
		}
	}
	return rec
}

// locationRuns folds a group's locations by file, states the first of them,
// and counts what it did not name.
func locationRuns(rows []ReportScanRow) []inlineRun {
	var out []inlineRun
	files, shown, i := 0, 0, 0
	for i < len(rows) {
		file := rows[i].File
		var lines []string
		for i < len(rows) && rows[i].File == file {
			if rows[i].Line > 0 {
				lines = append(lines, strconv.Itoa(rows[i].Line))
			}
			i++
		}
		files++
		if files > maxFilesPerRule {
			continue
		}
		shown = i
		if len(out) > 0 {
			out = append(out, inlineRun{Text: " · ", Family: familySans, Color: mutedColor})
		}
		out = append(out, inlineRun{Text: file, Family: familyMono, Color: inkColor})
		if len(lines) > 0 {
			out = append(out, inlineRun{Text: " " + strings.Join(lines, ", "), Family: familyMono, Color: mutedColor})
		}
	}
	if rest := len(rows) - shown; rest > 0 {
		out = append(out, inlineRun{
			Text:   " · and " + plural(rest, "location", "locations") + " in " + plural(files-maxFilesPerRule, "further file", "further files"),
			Family: familySans,
			Color:  mutedColor,
		})
	}
	return out
}

// scanScopeNote accounts for every row the scanners reported: what the report
// lists, what the project ignored, what the scan merged, what its own budget
// withheld, and what fell below the report's listing floor.
func scanScopeNote(scan *ReportScan, listed, ruleCount int) string {
	var parts []string
	if listed > 0 {
		parts = append(parts, plural(listed, "row", "rows")+" under "+plural(ruleCount, "rule", "rules"))
	}
	if scan == nil {
		return strings.Join(parts, " · ")
	}
	if scan.Groups > 0 {
		parts = []string{strconv.Itoa(scan.ListedGroups) + " of " + plural(scan.Groups, "group", "groups") + " listed; " + plural(scan.Represented, "stored occurrence represented", "stored occurrences represented") + " by " + plural(listed, "sample row", "sample rows")}
	}
	if scanners := strings.TrimSpace(strings.Join(scan.Scanners, ", ")); scanners != "" {
		parts = append(parts, scanners+" reported "+plural(scan.Total, "finding", "findings"))
	} else if scan.Total > 0 {
		parts = append(parts, plural(scan.Total, "finding reported", "findings reported"))
	}
	if scan.Ignored > 0 {
		parts = append(parts, plural(scan.Ignored, "ignored by this project", "ignored by this project"))
	}
	if scan.Merged > 0 {
		parts = append(parts, plural(scan.Merged, "merged into an advisory", "merged into advisories"))
	}
	if n := scan.WithheldAtIngest(); n > 0 {
		parts = append(parts, plural(n, "withheld at ingest", "withheld at ingest"))
	}
	if n := scan.NotListed(); n > 0 {
		parts = append(parts, plural(n, "not represented by listed groups", "not represented by listed groups"))
	}
	if mix := levelCounts(scan); len(mix) > 0 {
		levels := make([]string, 0, len(mix))
		for _, lc := range mix {
			levels = append(levels, plural(lc.count, lc.level, lc.level))
		}
		parts = append(parts, strings.Join(levels, ", "))
	}
	return strings.Join(parts, " · ")
}

type levelCount struct {
	level string
	count int
}

func levelCounts(scan *ReportScan) []levelCount {
	if scan == nil {
		return nil
	}
	out := make([]levelCount, 0, len(scan.ByLevel))
	for level, count := range scan.ByLevel {
		if count > 0 && strings.TrimSpace(level) != "" {
			out = append(out, levelCount{level: strings.TrimSpace(level), count: count})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := SeverityRank(out[i].level), SeverityRank(out[j].level)
		if ri != rj {
			return ri < rj
		}
		return out[i].level < out[j].level
	})
	return out
}
