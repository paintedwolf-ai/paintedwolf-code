package tools

import (
	"testing"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/textfile"
)

func TestMatchesWholeDocumentWindow(t *testing.T) {
	lfDoc := EditorDocumentText{ID: "d1", Revision: 1, Text: "a\nb\nc\n", Encoding: textfile.UTF8, EOL: "lf"}
	lfLines := []string{"a", "b", "c"}
	lfBody := hostmarker.FormatNumberedLines(lfLines, 1)

	if !MatchesWholeDocumentWindow(lfDoc, lfLines, 1, 3, lfBody) {
		t.Fatal("expected exact LF whole window to match")
	}
	if MatchesWholeDocumentWindow(lfDoc, lfLines, 2, 3, lfBody) {
		t.Fatal("expected non-1 startLine to fail")
	}
	if MatchesWholeDocumentWindow(lfDoc, lfLines, 1, 2, lfBody) {
		t.Fatal("expected partial endLine to fail")
	}
	if MatchesWholeDocumentWindow(lfDoc, lfLines, 1, 3, "") {
		t.Fatal("expected empty body to fail")
	}

	crlfLines := []string{"a\r", "b\r", "c\r"}
	crlfBody := hostmarker.FormatNumberedLines(crlfLines, 1)
	if MatchesWholeDocumentWindow(lfDoc, lfLines, 1, 3, crlfBody) {
		t.Fatal("LF document must reject CRLF-suffixed window body")
	}

	crlfDoc := EditorDocumentText{ID: "d2", Revision: 2, Text: "a\nb\nc\n", Encoding: textfile.UTF16LEBOM, EOL: "crlf"}
	if !MatchesWholeDocumentWindow(crlfDoc, lfLines, 1, 3, crlfBody) {
		t.Fatal("expected CRLF UTF-16LE BOM whole window with trailing newline to match")
	}
	if !MatchesWholeDocumentWindow(crlfDoc, lfLines, 1, 3, lfBody) {
		t.Fatal("expected LF-normalized body for CRLF doc to match")
	}

	crlfNoTrail := EditorDocumentText{ID: "d3", Revision: 3, Text: "a\nb", Encoding: textfile.UTF8, EOL: "crlf"}
	noTrailBody := hostmarker.FormatNumberedLines([]string{"a\r", "b"}, 1)
	if !MatchesWholeDocumentWindow(crlfNoTrail, []string{"a", "b"}, 1, 2, noTrailBody) {
		t.Fatal("expected CRLF whole window without trailing newline to match")
	}
	if MatchesWholeDocumentWindow(crlfNoTrail, []string{"a", "b"}, 1, 2, hostmarker.FormatNumberedLines([]string{"a\r", "b\r"}, 1)) {
		t.Fatal("expected CRLF no-trailing-newline doc to reject trailing \\r on last line")
	}
}
