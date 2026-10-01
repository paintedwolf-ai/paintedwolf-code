package workflowadmin

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/report"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Citations resolve by handle, by the exact place they name, or by a line
// inside the span a read observed, and every record says what rests on it.
func TestEvidenceFromGrounding_AttributesEveryCitation(t *testing.T) {
	g := &wire.CitationGrounding{
		EvidenceRecords: []wire.CitationGroundingEvidenceRecord{
			{Handle: "sess:grep#1", Kind: "grep", Path: "internal/auth", Fidelity: "structured", MatchCount: 16, MatchPaths: 3},
			{Handle: "sess:read#2", Kind: "read", Path: "internal/auth/session.go", Line: 84, LineEnd: 92, Fidelity: "structured"},
			{Handle: "sess:read#3", Kind: "read", Path: "internal/store/query.go", Line: 214, Fidelity: "structured"},
			{Handle: "sess:list#4", Kind: "list", Path: "deploy", Fidelity: "structured"},
		},
		EvidenceRecordCount: 31,
		CitedEvidence: []wire.CitationGroundingCitedEvidence{
			{Handle: "sess:grep#1"},
			{Path: "internal/store/query.go", Line: 214},
		},
	}
	cites := append(closeoutCitations(g), citation{by: "app-1", path: "internal/auth/session.go", line: 88})

	got, total := evidenceFromGrounding(g, cites)
	if len(got) != 4 {
		t.Fatalf("evidence rows = %d, want every observed record", len(got))
	}
	if total != 31 {
		t.Fatalf("evidence total = %d, want the ledger the sample came from", total)
	}
	want := [][]string{{report.CitedByReport}, {"app-1"}, {report.CitedByReport}, nil}
	for i, row := range got {
		if strings.Join(row.CitedBy, ",") != strings.Join(want[i], ",") {
			t.Fatalf("row %d (%s) cited by %v, want %v", i, row.Handle, row.CitedBy, want[i])
		}
	}
	if got[0].Matches != 16 || got[0].MatchFiles != 3 {
		t.Fatalf("search row = %+v, want its match counts carried", got[0])
	}
	if got[1].LineEnd != 92 {
		t.Fatalf("read row = %+v, want the span it observed", got[1])
	}
}

// Sources are titled where a tool saw the page's title, and every citer is
// named: the report first, then each phase that cited it.
func TestSourcesFromGrounding_TitledAndAttributed(t *testing.T) {
	g := &wire.CitationGrounding{
		CitedURLs: []string{"https://owasp.org/a", "https://ffmpeg.org/security.html"},
		EvidenceRecords: []wire.CitationGroundingEvidenceRecord{{
			Handle: "web#1", Kind: "web",
			URLs:      []string{"https://owasp.org/a"},
			URLTitles: map[string]string{"https://owasp.org/a": "SQL injection"},
		}},
	}
	verdicts := []report.ReportVerdict{{Phase: "challenge", Decision: "CHALLENGED"}}
	urls := map[string][]string{"challenge": {"https://ffmpeg.org/security.html", "https://nvd.example/cve"}}

	got := sourcesFromGrounding(g, verdicts, urls)
	if len(got) != 3 {
		t.Fatalf("sources = %+v, want the report's two and the phase's extra", got)
	}
	if got[0].Title != "SQL injection" || strings.Join(got[0].CitedBy, ",") != report.CitedByReport {
		t.Fatalf("source[0] = %+v, want the observed title and the report as citer", got[0])
	}
	if strings.Join(got[1].CitedBy, ",") != report.CitedByReport+",challenge" {
		t.Fatalf("source[1] cited by %v, want both the report and the phase", got[1].CitedBy)
	}
	if got[2].Title != "" || got[2].URL != "https://nvd.example/cve" {
		t.Fatalf("source[2] = %+v, want the untitled phase-only URL", got[2])
	}
}
