package report

import (
	"strings"
	"testing"

	"github.com/johnfercher/maroto/v2"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A long report opens with a contents page whose numbers come from the same
// pagination the document is laid out with; a short one does without.
func TestContents_NumbersFollowTheLayout(t *testing.T) {
	ms := testMeasurer(t)
	cfg, err := newConfig(ReportInput{ReportHeader: ReportHeader{Title: "T"}}, mustTime(t, "2026-07-08T15:04:05Z"))
	testutil.FailErr(t, "config", err)

	var synthesis strings.Builder
	synthesis.WriteString("## Scope\n\n")
	for i := 0; i < 3; i++ {
		synthesis.WriteString(strings.Repeat("Prose that fills a page. ", 120))
		synthesis.WriteString("\n\n")
	}
	synthesis.WriteString("## Key findings\n\n### A minor heading\n\n")
	for i := 0; i < 3; i++ {
		synthesis.WriteString(strings.Repeat("More prose that fills another page. ", 120))
		synthesis.WriteString("\n\n")
	}
	long := ReportInput{
		ReportHeader: ReportHeader{
			Title:       "Long",
			RunID:       "run_long",
			Project:     "acme/app",
			CompletedAt: "2026-07-08T15:04:05Z",
		},
		Synthesis: synthesis.String(),
		ScanRows:  []ReportScanRow{{Severity: "high", RuleID: "r", File: "a.go", Line: 1, Message: "m"}},
	}

	blocks := layoutBlocks(ms, cfg, long)
	joined := joinRowValues(blocks)
	if !strings.Contains(joined, sectionContents) {
		t.Fatalf("long report has no contents page: %q", joined)
	}
	if strings.Contains(joined, "A minor heading") && strings.Index(joined, "A minor heading") < strings.Index(joined, sectionScan) {
		// The heading appears in the body; it must not appear in the contents.
		contentsEnd := strings.Index(joined, "Scope")
		if idx := strings.Index(joined[:contentsEnd+1], "A minor heading"); idx >= 0 {
			t.Fatal("a third-level heading must not be a contents entry")
		}
	}

	// The pages the contents name are the pages the sections land on.
	d := &docBuilder{m: maroto.New(cfg)}
	d.emit(blocks)
	pageOf := map[string]int{}
	for i, b := range blocks {
		if b.contents != nil {
			pageOf[b.contents.title] = d.pages[i]
		}
	}
	entries := contentsEntries(blocks[len(briefBlocks(ms, long)):], nil)
	if len(entries) == 0 {
		t.Fatal("no anchored blocks found")
	}
	if pageOf[sectionScan] < 3 {
		t.Fatalf("scan section landed on page %d; the fixture was meant to run past two pages", pageOf[sectionScan])
	}
	if pageOf[sectionColophon] < pageOf[sectionScan] {
		t.Fatalf("colophon on page %d before the scan section on page %d", pageOf[sectionColophon], pageOf[sectionScan])
	}

	short := ReportInput{
		ReportHeader: ReportHeader{
			Title:       "Short",
			RunID:       "run_short",
			Project:     "acme/app",
			CompletedAt: "2026-07-08T15:04:05Z",
		},
		Synthesis: "## Done\n\nAll good.",
	}
	if joined := joinRowValues(layoutBlocks(ms, cfg, short)); strings.Contains(joined, sectionContents) {
		t.Fatalf("short report should have no contents page: %q", joined)
	}
}

// The page a block starts on is read from the builder, so the count agrees
// with what the library will print.
func TestDocBuilder_TracksPages(t *testing.T) {
	ms := testMeasurer(t)
	cfg, err := newConfig(ReportInput{ReportHeader: ReportHeader{Title: "T"}}, mustTime(t, "2026-07-08T15:04:05Z"))
	testutil.FailErr(t, "config", err)

	var blocks []block
	for i := 0; i < 400; i++ {
		blocks = append(blocks, rowsBlock(textRows(ms, []inlineRun{{Text: "line"}}, bodyProp())...))
	}
	d := &docBuilder{m: maroto.New(cfg)}
	d.emit(blocks)
	if d.pages[0] != 1 {
		t.Fatalf("first block on page %d, want 1", d.pages[0])
	}
	if d.page < 3 {
		t.Fatalf("400 lines ended on page %d; want the count to advance past the page breaks", d.page)
	}
	for i := 1; i < len(d.pages); i++ {
		if d.pages[i] < d.pages[i-1] {
			t.Fatalf("block %d on page %d after block %d on page %d", i, d.pages[i], i-1, d.pages[i-1])
		}
	}
	doc, err := d.m.Generate()
	testutil.FailErr(t, "generate", err)
	if len(doc.GetBytes()) == 0 {
		t.Fatal("empty document")
	}
}

// A block that ends its page hands the next block a fresh page, not a second
// break: the block after it starts on the following page.
func TestDocBuilder_BreakAfterAdvancesOnePage(t *testing.T) {
	ms := testMeasurer(t)
	cfg, err := newConfig(ReportInput{ReportHeader: ReportHeader{Title: "T"}}, mustTime(t, "2026-07-08T15:04:05Z"))
	testutil.FailErr(t, "config", err)

	line := func() block { return rowsBlock(textRows(ms, []inlineRun{{Text: "line"}}, bodyProp())...) }
	first := line()
	first.breakAfter = true
	blocks := []block{first, line(), line()}
	d := &docBuilder{m: maroto.New(cfg)}
	d.emit(blocks)
	if want := []int{1, 2, 2}; d.pages[0] != want[0] || d.pages[1] != want[1] || d.pages[2] != want[2] {
		t.Fatalf("pages = %v, want %v", d.pages, want)
	}
}

// Code keeps its indentation through the line breaker.
func TestPreserveIndent(t *testing.T) {
	cases := map[string]string{
		"    return x":    "    return x",
		"\tif a {":        "    if a {",
		"a  =  b":         "a  =  b",
		"one space stays": "one space stays",
		"":                "",
	}
	for in, want := range cases {
		if got := preserveIndent(in); got != want {
			t.Fatalf("preserveIndent(%q) = %q, want %q", in, got, want)
		}
	}
}
