package report

import (
	"strings"
	"testing"
)

// runsOf builds a one-run cell in the sans face.
func runsOf(text string) []inlineRun { return []inlineRun{{Text: text}} }

// monoRuns builds a one-run cell in the mono face, as identifiers are set.
func monoRuns(text string) []inlineRun { return []inlineRun{{Text: text, Family: familyMono}} }

// A synthesis table whose last columns are paragraphs must promote them: a
// paragraph does not belong in a column beside a severity word.
func TestPlanColumns_PromotesProseColumns(t *testing.T) {
	ms := testMeasurer(t)
	headers := []string{"Severity", "File", "Line", "Issue", "What to do"}
	records := []tableRecord{
		{cells: [][]inlineRun{
			runsOf("High"),
			monoRuns("internal/auth/session.go"),
			runsOf("88"),
			runsOf("Session token compared with ==. validate() compares the presented bearer token to the stored digest with an ordinary string comparison, which returns as soon as two bytes differ."),
			runsOf("Compare with subtle.ConstantTimeCompare. The digest is already fixed width, so no other change is needed."),
		}},
		{cells: [][]inlineRun{
			runsOf("Medium"),
			monoRuns("internal/api/server.go"),
			runsOf("130"),
			runsOf("Loopback auth bypass by default. When no bearer token is configured and the listener is bound to loopback, authentication is silently omitted."),
			runsOf("Make the bypass opt-in behind an explicit flag."),
		}},
	}

	plan := planColumns(ms, headers, records, sansCellStyle(), headCellStyle(), nil)

	if got := plan.inRow; len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Fatalf("in-row columns = %v, want the three identifier columns [0 1 2]", got)
	}
	if got := plan.promoted; len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Fatalf("promoted columns = %v, want the two prose columns [3 4]", got)
	}
	if !plan.labelPromoted {
		t.Fatal("two promoted columns must carry their headings, or they read as one paragraph")
	}
}

// One prose column may stay in the row when the identifiers leave it room.
func TestPlanColumns_KeepsOneProseColumnWhenItFits(t *testing.T) {
	ms := testMeasurer(t)
	headers := []string{"Option", "Why"}
	records := []tableRecord{
		{cells: [][]inlineRun{
			runsOf("A"),
			runsOf("The only option whose failure mode the producer can observe, at the cost of retry with backoff."),
		}},
	}

	plan := planColumns(ms, headers, records, sansCellStyle(), headCellStyle(), nil)
	if len(plan.promoted) != 0 {
		t.Fatalf("promoted = %v, want the prose column kept in a row this wide", plan.promoted)
	}
	if len(plan.widths) != 2 {
		t.Fatalf("widths = %v, want two columns", plan.widths)
	}
}

// Every in-row column must end up at least as wide as its own longest
// unbreakable token, or the value it was budgeted for wraps anyway.
func TestPlanColumns_InRowColumnsFitTheirLongestToken(t *testing.T) {
	ms := testMeasurer(t)

	cases := map[string]struct {
		headers []string
		records []tableRecord
		chip    map[int]float64
	}{
		"findings with a chip column": {
			headers: []string{"Severity", "Rule", "Location"},
			records: []tableRecord{
				{cells: [][]inlineRun{
					runsOf("high"),
					monoRuns("go.lang.security.audit.crypto.use-of-weak-comparison"),
					monoRuns("internal/auth/session.go:88"),
				}},
				{cells: [][]inlineRun{
					runsOf("medium"),
					monoRuns("go.lang.security.audit.sqli.string-formatted-query"),
					monoRuns("internal/store/query.go:214"),
				}},
			},
			chip: map[int]float64{0: newTonedChip(ms, "medium", toneNeutral).width},
		},
		"evidence ledger": {
			headers: []string{"Cited", "Handle", "Kind", "Location", "Trust"},
			records: []tableRecord{
				{cells: [][]inlineRun{
					runsOf("cited"),
					monoRuns("read#12"),
					runsOf("read"),
					monoRuns("internal/store/query.go:214"),
					runsOf("structured"),
				}},
				{cells: [][]inlineRun{
					runsOf(""),
					monoRuns("grep#4"),
					runsOf("grep"),
					monoRuns("internal/auth"),
					runsOf("observed"),
				}},
			},
			chip: map[int]float64{0: newTonedChip(ms, "cited", toneNeutral).width},
		},
		"three identifier columns, no chips": {
			headers: []string{"Severity", "File", "Line"},
			records: []tableRecord{
				{cells: [][]inlineRun{
					runsOf("Medium"),
					monoRuns(".github/workflows/ci.yml"),
					runsOf("14, 18"),
				}},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			plan := planColumns(ms, tc.headers, tc.records, sansCellStyle(), headCellStyle(), tc.chip)
			prop := (&table{}).cellProp()

			for k, src := range plan.inRow {
				available := plan.widths[k] - prop.Left - prop.Right
				if width, ok := tc.chip[src]; ok {
					if available < width {
						t.Fatalf("column %d (%s) has %.2fmm for a %.2fmm chip",
							src, tc.headers[src], available, width)
					}
					continue
				}
				for _, rec := range tc.records {
					if src >= len(rec.cells) {
						continue
					}
					longest := measureCell(ms, rec.cells[src], sansCellStyle()).longest
					if longest > available+0.01 {
						t.Fatalf("column %d (%s): %.2fmm of measure, but its longest token needs %.2fmm — %q wraps",
							src, tc.headers[src], available, longest,
							strings.TrimSpace(plainRuns(rec.cells[src])))
					}
				}
			}
		})
	}
}

// Columns always cover the measure exactly: a short sum leaves a ragged right
// edge, and a long one overprints the margin.
func TestPlanColumns_WidthsCoverTheMeasure(t *testing.T) {
	ms := testMeasurer(t)
	records := []tableRecord{{cells: [][]inlineRun{
		runsOf("a"), monoRuns("internal/very/long/path/to/a/file.go"), runsOf("1"), runsOf("x"),
	}}}

	plan := planColumns(ms, []string{"A", "B", "C", "D"}, records, sansCellStyle(), headCellStyle(), nil)
	total := 0.0
	for _, w := range plan.widths {
		if w <= 0 {
			t.Fatalf("width %.2f is not a column", w)
		}
		total += w
	}
	if diff := total - contentWidth; diff > 0.01 || diff < -0.01 {
		t.Fatalf("widths %v sum to %.2fmm, want the %.2fmm measure", plan.widths, total, contentWidth)
	}
}

// More columns than the measure can seat still produce a band that reads left
// to right: a width driven past zero starts the next cell behind the last.
func TestPlanColumns_TooManyColumnsStayPositive(t *testing.T) {
	ms := testMeasurer(t)
	var headers []string
	var cells [][]inlineRun
	for i := 0; i < 20; i++ {
		headers = append(headers, "column heading")
		cells = append(cells, monoRuns("internal/some/package/file.go:128"))
	}

	plan := planColumns(ms, headers, []tableRecord{{cells: cells}}, sansCellStyle(), headCellStyle(), nil)
	total := 0.0
	for _, w := range plan.widths {
		if w <= 0 {
			t.Fatalf("widths %v: a column with no width reverses the band", plan.widths)
		}
		total += w
	}
	if total > contentWidth+0.01 {
		t.Fatalf("widths sum to %.2fmm, past the %.2fmm measure", total, contentWidth)
	}
}

func sansCellStyle() styleKey { return cellStyleKey((&table{}).cellProp()) }

func headCellStyle() styleKey { return cellStyleKey((&table{}).headProp()) }
