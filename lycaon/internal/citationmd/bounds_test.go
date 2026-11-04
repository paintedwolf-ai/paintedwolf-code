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
		"declarations":      "a " + strings.Repeat("<!A ", size/4),
		"multi-line labels": strings.Repeat("[a\nb]: c\n", size/8),
		"escaped labels":    strings.Repeat("[a\\]b]: c\n", size/9),
		"bracket lines":     strings.Repeat("[a.go:0]\n", size/9),
		"list definitions":  strings.Repeat("- [a]: b\n", size/9),
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

// M5: the literal helpers answer under the same budgets, and past one they offer no
// range where a node could begin.
func TestLiteralHelpersRespectBudgets(t *testing.T) {
	t.Parallel()
	over := "```\n[read#1]\n```\n" + tableShape(2730)
	zones := LiteralZones(over)
	if len(zones) != 1 || zones[0].Kind != ZoneEscapedDocument || zones[0].Span != (Span{0, len(over)}) {
		t.Fatalf("zones = %+v", zones)
	}
	if ranges := TextRanges(over); len(ranges) != 0 {
		t.Fatalf("ranges = %+v", ranges)
	}
}

// N5, N6: past any budget a document displays as one literal block, whatever it holds,
// and nothing is taken from it.
func TestOverBudgetDisplaysLiterally(t *testing.T) {
	t.Parallel()
	links := "[x](https://unobs.test/z) https://unobs.test/y <a href=\"https://unobs.test/h\">h</a> ````"
	cases := []struct {
		name   string
		source string
		opts   Options
		code   IssueCode
	}{
		{"source", links + strings.Repeat(" word", 3), Options{MaxSourceBytes: 10}, IssueSourceBudget},
		{"structure", tableShape(2730) + "\n\n" + links, Options{}, IssueStructureBudget},
		{"occurrences", strings.Repeat("[read#1] ", 3) + links, Options{MaxOccurrences: 2}, IssueOccurrenceBudget},
		{"issues", strings.Repeat("[read#x] ", 3) + links, Options{MaxOccurrences: 2}, IssueOccurrenceBudget},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := Parse(tc.source, tc.opts)
			codes := issueCodes(doc)
			if len(codes) == 0 || codes[len(codes)-1] != tc.code {
				t.Fatalf("issues = %v", codes)
			}
			want := "`````\n" + tc.source + "\n`````"
			if got := Render(doc, func(Occurrence) Rendition { return Rendition{Keep: true} }); got != want {
				t.Fatalf("got %.80q", got)
			}
		})
	}
}

// B4: presentation is one linear pass whatever the shape.
func TestRenderWorkIsLinear(t *testing.T) {
	t.Parallel()
	const size = 1 << 20
	for _, unit := range []string{"[a.go:0] ", "[a.go:0]\n", "[read#1] ", "[read#1] x ", "x[read#1]", "[read#1]   ", "- [read#1]\n", "[read#1] [read#x] ---\n"} {
		source := strings.Repeat(unit, size/len(unit))
		doc := Parse(source, Options{MaxOccurrences: size, KnownHandleKind: func(string) bool { return true }})
		_, work := renderCounted(doc, func(Occurrence) Rendition { return Rendition{} })
		if work > 4*len(source) {
			t.Errorf("unit %q took %d lookups for %d bytes", unit, work, len(source))
		}
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
