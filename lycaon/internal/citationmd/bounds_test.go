package citationmd

import (
	"runtime"
	"strings"
	"testing"
)

func tableShape(columns int) string {
	return strings.Repeat("a|", columns) + "\n" + strings.Repeat("-|", columns) + "\n" + strings.Repeat("b\n", columns)
}

func nestedListShape(size int) string {
	var b strings.Builder
	for depth := 0; b.Len() < size; depth++ {
		b.WriteString(strings.Repeat(" ", 2*depth))
		b.WriteString("- a\n")
	}
	return b.String()
}

// allocatedBytes measures one parse. The test is serial, so no parallel test
// shares the counter.
func allocatedBytes(source string) (Document, uint64) {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	doc := Parse(source, Options{})
	runtime.ReadMemStats(&after)
	return doc, after.TotalAlloc - before.TotalAlloc
}

// C1, H9, AC-AST-12: the review's shapes report a structured issue before the
// structural parse, and allocate in proportion to their size, at the full budget.
func TestStructureBudgetShapes(t *testing.T) { //nolint:paralleltest // Reads the process-wide allocation counter.
	const size = MaxSourceBytes
	shapes := map[string]string{
		"C1 table":          tableShape(2730),
		"C1 table at scale": tableShape(size / 6),
		"processing":        "a " + strings.Repeat("<? ", size/3),
		"comment":           "a " + strings.Repeat("<!-- ", size/5),
		"cdata":             "a " + strings.Repeat("<![CDATA[ ", size/10),
		"definitions":       strings.Repeat("[a]: b\n", size/7),
		"bracket pairs":     strings.Repeat("[", size/2) + strings.Repeat("]", size/2),
		"open brackets":     strings.Repeat("[", size),
		"image labels":      strings.Repeat("![a", size/7) + strings.Repeat("](x)", size/7),
		"nested list":       nestedListShape(size),
		"blockquote depth":  strings.Repeat(">", maxContainerDepth+1) + " x",
	}
	for name, source := range shapes {
		if len(source) > MaxSourceBytes {
			source = source[:MaxSourceBytes]
		}
		doc, allocated := allocatedBytes(source)
		if got := issueCodes(doc); len(got) == 0 || got[0] != IssueStructureBudget {
			t.Errorf("%s: issues = %v", name, got)
		}
		if limit := uint64(64*len(source)) + 1<<20; allocated > limit {
			t.Errorf("%s: allocated %d bytes for %d", name, allocated, len(source))
		}
	}
}

// H9: shapes under the budgets are parsed, and stay proportional in allocation.
func TestParsedShapesAllocateInProportion(t *testing.T) { //nolint:paralleltest // Reads the process-wide allocation counter.
	const size = 1 << 20
	shapes := map[string]string{
		"plain":      strings.Repeat("word word word\n", size/15),
		"table":      "a|b\n-|-\n" + strings.Repeat("c|d\n", 1<<13),
		"code spans": strings.Repeat("`a` ", size/4),
		"links":      strings.Repeat("[a][b] ", size/7),
		"html":       strings.Repeat("<div>\n\n", size/7),
		"urls":       strings.Repeat("http://a.b/(", size/12),
	}
	for name, source := range shapes {
		_, allocated := allocatedBytes(source)
		if limit := uint64(1024*len(source)) + 1<<20; allocated > limit {
			t.Errorf("%s: allocated %d bytes for %d", name, allocated, len(source))
		}
	}
}

func TestStructureBudgetLeavesOrdinaryDocumentsAlone(t *testing.T) {
	t.Parallel()
	ordinary := []string{
		strings.Repeat("| a | b | c |\n", 2) + "|---|---|---|\n" + strings.Repeat("| 1 | 2 | 3 |\n", 5000),
		strings.Repeat("- - - -", 40),
		strings.Repeat("- ", maxContainerDepth+1) + "\n",
		strings.Repeat("* ", maxContainerDepth+5),
		strings.Repeat("[r1]: https://x.test/a\n", 200),
		strings.Repeat("<!-- note -->\n\n", 2000),
		strings.Repeat("See <!-- a --> and <? b ?> and <![CDATA[ c ]]>.\n", 2000),
		strings.Repeat("[[1, 2], [3, 4]] ", 2000),
		"```yaml\n" + strings.Repeat("        - item\n", 5000) + "```\n",
	}
	for _, source := range ordinary {
		if doc := Parse(source, Options{}); len(doc.Issues) != 0 {
			t.Errorf("%.30q: issues = %+v", source, doc.Issues)
		}
	}
}

// M5: the literal helpers answer under the same budgets.
func TestLiteralHelpersRespectBudgets(t *testing.T) {
	t.Parallel()
	over := "```\n[read#1]\n```\n" + tableShape(2730)
	zones := LiteralZones(over)
	if len(zones) != 1 || zones[0].Kind != ZoneFence {
		t.Fatalf("zones = %+v", zones)
	}
	ranges := TextRanges(over)
	if len(ranges) != 1 || ranges[0].Start != zones[0].Span.End {
		t.Fatalf("ranges = %+v", ranges)
	}
}

// M6: a source past a budget keeps fence-like regions; the check does not fire on
// a thematic break.
func TestBudgetFallbackKeepsLiterals(t *testing.T) {
	t.Parallel()
	const overflow = "\n\n<! " // Keeps the table shape's block separate from the tail.
	cases := []struct{ name, tail string }{
		{"fence", "```\n[read#1]\n\nx\n```\nAfter [read#2] text"},
		{"quoted fence", "> ~~~\n> [read#1]\n> ~~~\nAfter [read#2] text"},
		{"unclosed fence", "After [read#2] text\n```\n[read#1]\n"},
		{"code span", "After [read#2] text `[read#1]` end"},
		{"code span of two", "After [read#2] text ``a ` [read#1]`` end ` x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			source := tableShape(2730) + overflow + "\n\n" + tc.tail
			doc := Parse(source, Options{})
			if got := issueCodes(doc); len(got) == 0 || got[0] != IssueStructureBudget {
				t.Fatalf("issues = %v", got)
			}
			got := Project(doc)
			if !strings.Contains(got, "[read#1]") || strings.Contains(got, "[read#2]") || !strings.Contains(got, "After") {
				t.Fatalf("presented %.200q", got[max(0, len(got)-200):])
			}
		})
	}
}

// C2: a line near the integer limit is refused by the grammar.
func TestParseLineBound(t *testing.T) {
	t.Parallel()
	cases := []struct {
		source string
		issue  bool
	}{
		{"[read#1:1000000000]", false},
		{"[read#1:1000000001]", true},
		{`[read#1:9223372036854775805 "content of line number 3"]`, true},
		{"[read#1:1-1000000001]", true},
		{"[a.go:9223372036854775807]", true},
		{"[a.go:92233720368547758070]", true},
		{"[a](a.go#L9223372036854775805)", true},
		{"[a](source://r/a.go#L9223372036854775805)", true},
	}
	for _, tc := range cases {
		doc := Parse(tc.source, Options{})
		switch {
		case tc.issue && (len(doc.Issues) != 1 || doc.Issues[0].Code != IssueInvalidRange || len(doc.Occurrences) != 0):
			t.Errorf("%s: issues = %+v, occurrences = %d", tc.source, doc.Issues, len(doc.Occurrences))
		case !tc.issue && (len(doc.Issues) != 0 || len(doc.Occurrences) != 1):
			t.Errorf("%s: issues = %+v", tc.source, doc.Issues)
		}
	}
}
