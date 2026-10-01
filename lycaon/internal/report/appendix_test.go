package report

import (
	"strings"
	"testing"
)

// Records are grouped by what they observed, because a search and a read
// carry different facts, and each group's columns are the facts its rows fill.
func TestKindColumns_FollowTheFactsTheRowsCarry(t *testing.T) {
	searches := []ReportEvidence{
		{Handle: "grep#1", Kind: "grep", Path: "internal/auth", Matches: 16, MatchFiles: 3, TrustTier: "structured", CitedBy: []string{"app-1"}},
		{Handle: "grep#2", Kind: "grep", Path: "internal/store", Matches: 4, MatchFiles: 1, TrustTier: "structured"},
	}
	headers := columnHeaders(kindColumns(searches))
	if strings.Join(headers, ",") != "Handle,Location,Matches,Cited by,Trust" {
		t.Fatalf("search columns = %v, want the search facts and no page column", headers)
	}

	pages := []ReportEvidence{
		{Handle: "web#3", Kind: "web", URL: "https://example.com/a", TrustTier: "observed"},
	}
	headers = columnHeaders(kindColumns(pages))
	if strings.Join(headers, ",") != "Handle,Page,Cited by,Trust" {
		t.Fatalf("page columns = %v, want a page column and no location or matches", headers)
	}
}

func columnHeaders(cols []evidenceColumn) []string {
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		out = append(out, c.header)
	}
	return out
}

// A column with one value on every row is a fact about the group, not a
// column of it.
func TestPartitionEvidenceColumns_ConstantColumnsBecomeCaption(t *testing.T) {
	evidence := []ReportEvidence{
		{Handle: "read#1", Kind: "read", Path: "a.go", Line: 10, TrustTier: "structured", CitedBy: []string{"report"}},
		{Handle: "read#2", Kind: "read", Path: "b.go", Line: 20, TrustTier: "structured"},
	}

	kept, notes := partitionEvidenceColumns(kindColumns(evidence), evidence)
	if got := strings.Join(columnHeaders(kept), ","); got != "Handle,Location,Cited by" {
		t.Fatalf("kept columns = %q, want the varying ones with Trust dropped", got)
	}
	if len(notes) != 1 || notes[0] != "all structured" {
		t.Fatalf("caption notes = %v, want the constant trust tier stated once", notes)
	}
}

// When every record is cited by the same thing, that is a fact about the
// whole group; when none is, the group says so rather than printing blanks.
func TestPartitionEvidenceColumns_UniformCitationIsACaption(t *testing.T) {
	cited := []ReportEvidence{
		{Handle: "read#1", Kind: "read", Path: "a.go", TrustTier: "structured", CitedBy: []string{"report"}},
		{Handle: "read#2", Kind: "read", Path: "b.go", TrustTier: "observed", CitedBy: []string{"report"}},
	}
	kept, notes := partitionEvidenceColumns(kindColumns(cited), cited)
	for _, c := range kept {
		if c.header == "Cited by" {
			t.Fatal("a cited-by column with one value on every row must not be a column")
		}
	}
	if len(notes) != 1 || notes[0] != "all cited by report" {
		t.Fatalf("caption notes = %v, want the group stated as fully cited", notes)
	}

	uncited := []ReportEvidence{
		{Handle: "list#1", Kind: "list", Path: "deploy", TrustTier: "structured"},
		{Handle: "list#2", Kind: "list", Path: "docs", TrustTier: "structured"},
	}
	_, notes = partitionEvidenceColumns(kindColumns(uncited), uncited)
	if strings.Join(notes, "|") != "none cited|all structured" {
		t.Fatalf("caption notes = %v, want the group stated as uncited", notes)
	}
}

// The caption states the sample against the ledger it came from, so 24 rows
// never pass for a 61-record ledger.
func TestEvidenceCaption_StatesSampleAgainstLedger(t *testing.T) {
	rows := []ReportEvidence{
		{Handle: "read#1", Kind: "read", CitedBy: []string{"report"}},
		{Handle: "grep#2", Kind: "grep"},
		{Handle: "grep#3", Kind: "grep"},
	}
	got := evidenceCaption(rows, 61)
	for _, want := range []string{"3 of 61 records listed", "1 cited", "1 read, 2 grep"} {
		if !strings.Contains(got, want) {
			t.Fatalf("caption %q: want substring %q", got, want)
		}
	}
	if got := evidenceCaption(rows, 3); strings.Contains(got, "of") {
		t.Fatalf("caption %q: a complete ledger has no sample to disclaim", got)
	}
}

// An excerpt is shown as the lines the tool returned, indentation kept, and
// never more than a window of them.
func TestExcerptLines_WindowKeepsIndentation(t *testing.T) {
	var lines []string
	for i := 0; i < 12; i++ {
		lines = append(lines, "\tline "+string(rune('a'+i)))
	}
	got := excerptLines(strings.Join(lines, "\n"))
	if len(got) != maxExcerptLines+1 {
		t.Fatalf("excerpt lines = %d, want %d plus the count of what follows", len(got), maxExcerptLines)
	}
	if !strings.HasPrefix(got[0], "    line a") {
		t.Fatalf("first line = %q, want the tab kept as no-break indentation", got[0])
	}
	if got[len(got)-1] != "… 4 more lines" {
		t.Fatalf("last line = %q, want the count of lines not shown", got[len(got)-1])
	}
}

// An excerpt locates a record rather than reproducing it.
func TestClip(t *testing.T) {
	long := strings.Repeat("some words to quote ", 40)
	got := clip(long, 60)
	if len([]rune(got)) > 61 {
		t.Fatalf("clip returned %d runes, want at most 61", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("clip = %q, want a trailing ellipsis when it cut", got)
	}
	if strings.HasSuffix(strings.TrimSuffix(got, "…"), " ") {
		t.Fatalf("clip = %q, want no space before the ellipsis", got)
	}
	if short := clip("kept whole", 60); short != "kept whole" {
		t.Fatalf("clip(short) = %q, want it untouched", short)
	}
}

// A source is named by its page title when a tool saw one, with the address
// beneath, and by its address alone otherwise.
func TestSourcesBlocks_TitleThenAddress(t *testing.T) {
	joined := joinRowValues(sourcesBlocks(testMeasurer(t), []ReportSource{
		{URL: "https://docs.pytorch.org/notes/serialization.html", Title: "Serialization semantics", CitedBy: []string{"report"}},
		{URL: "https://ffmpeg.org/security.html", CitedBy: []string{"challenge"}},
	}))
	for _, want := range []string{"Serialization semantics", "docs.pytorch.org", "https://docs.pytorch.org/notes/serialization.html", "ffmpeg.org", "challenge"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("sources %q: want substring %q", joined, want)
		}
	}
}
