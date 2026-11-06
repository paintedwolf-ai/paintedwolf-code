package guidance

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/verification"
)

// Every member the document does not take is named with where it sits, and the
// refusal groups repeats: a field placed inside each finding is one entry.
func TestReadCloseoutReportNamesUnreadMembers(t *testing.T) {
	t.Parallel()
	content := "## Report\n\nBody.\n\n```json\n" + `{
		"findings": [
			{"id": "c1", "title": "A", "disposition": "act", "ask": {"do": "x", "effort": "small"}, "statement": "s"},
			{"id": "c2", "title": "B", "disposition": "held", "ask": {"do": "y", "effort": "small"}, "answers": "none"}
		],
		"set_asides": [{"scanner": "s", "paths": ["t/**"], "reason": "fixtures"}]
	}` + "\n```"
	read := readFresh(content, true)
	if read.Draft.Synthesis != "## Report\n\nBody." || len(read.Draft.Document.Findings) != 2 || len(read.Draft.Document.SetAsides) != 1 {
		t.Fatalf("read = %+v, want the answer above the fence and every readable field", read.Draft)
	}
	sample, total := UnreadReportFields(read.Unread)
	want := []string{
		"`findings[].ask` (2): `ask` is a top-level report field",
		"`findings[].statement`: not a report field",
		"`findings[].answers`: want object",
	}
	if total != len(want) || strings.Join(sample, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unread fields = %q (total %d), want %q", sample, total, want)
	}
	issue, ok := ReportFenceUnreadable(read.Unread)
	if !ok || issue.Code != ReportFenceUnreadableCode || issue.Count != len(want) {
		t.Fatalf("refusal = %+v", issue)
	}
}

// A finding that names its id but lost its title stays in the read, so the
// document check can ask for the title; one with neither is empty.
func TestReadCloseoutReportKeepsAnIdentifiedUntitledFinding(t *testing.T) {
	t.Parallel()
	content := "## Report\n\nBody.\n\n```json\n" +
		`{"findings": [{"id": "c1", "disposition": "held"}, {"disposition": "act"}, {"id": "c2", "title": "B", "disposition": "act"}]}` +
		"\n```"
	findings := readFresh(content, true).Draft.Document.Findings
	if len(findings) != 2 || findings[0].ID != "c1" || findings[1].ID != "c2" {
		t.Fatalf("findings = %+v, want the untitled c1 kept beside c2", findings)
	}
}

func TestReadCloseoutReportIgnoresVacuousUnmappedMembers(t *testing.T) {
	t.Parallel()
	read := readFresh("The survey is complete.\n\n```json\n{\"headline\":\"All held.\",\"headline_note\":null,\"empty_list\":[],\"empty_map\":{}}\n```", true)
	if len(read.Unread) > 0 {
		t.Fatalf("expected 0 unread issues for vacuous fields, got: %+v", read.Unread)
	}
	if read.Draft.Document.Headline != "All held." {
		t.Fatalf("headline = %q", read.Draft.Document.Headline)
	}
}

// AC-CLO-12: findings keep their schema, and every typed where entry is validated
// with its field path.
func TestReadCloseoutReportKeepsFindingWhereAssociation(t *testing.T) {
	t.Parallel()
	content := "Report.\n\n```json\n" + `{"findings":[
		{"id":"f1","title":"One","disposition":"act","severity":"high","status":"open","impact":"i","action":"a","answers":{"q":"yes"},"scan_group_ids":["g1"],
		 "where":[{"evidence":"read#1","path":"a.go","line":2,"end_line":5,"excerpt":"x"}]},
		{"id":"f2","title":"Two","disposition":"held","where":[{"path":"b.go","line":9}]}
	]}` + "\n```"
	draft := readFresh(content, true).Draft
	finding := draft.Document.Findings[0]
	if finding.Severity != "high" || finding.Answers["q"] != "yes" || len(finding.ScanGroupIDs) != 1 {
		t.Fatalf("finding fields lost: %+v", finding)
	}
	where := finding.Where[0]
	if where.Evidence != "read#1" || where.Path != "a.go" || where.Line != 2 || where.EndLine != 5 || where.Excerpt != "x" {
		t.Fatalf("where = %+v", where)
	}
	var owners []string
	for _, occurrence := range draft.Occurrences {
		owners = append(owners, occurrence.Owner)
		if occurrence.RepairID != "" {
			t.Fatalf("a document citation has no span to patch: %+v", occurrence)
		}
	}
	if strings.Join(owners, ",") != "findings[0].where[0],findings[1].where[0]" {
		t.Fatalf("owners = %v", owners)
	}
	if draft.CitationCount() != 2 {
		t.Fatalf("citation count = %d", draft.CitationCount())
	}
}

func TestCloseoutDraftAnswerAndReport(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, content string
		answer        bool
	}{
		{"prose", "Done.", true},
		{"a literal answer", "```\nDone\n```", true},
		{"cited prose", "Done [read#1].", true},
		{"citations only", "[read#1] [read#2:3]", false},
		{"directives only", "[verify: targeted - Ran it]\n[artifact:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee]", false},
		{"nothing", " \n", false},
	} {
		if got := ParseCloseoutDraft(tc.content, citationmd.Options{}).HasAnswer(); got != tc.answer {
			t.Errorf("%s: HasAnswer = %v", tc.name, got)
		}
	}
	draft := ParseCloseoutDraft("Done [read#1:2].\n[verify: blocked - No network]", citationmd.Options{})
	report := draft.Report()
	if report.Verification == nil || report.Verification.Method != verification.Blocked {
		t.Fatalf("verification = %+v", report.Verification)
	}
	if len(report.CitedEvidence) != 1 || report.CitedEvidence[0].Evidence != "read#1" || report.CitedEvidence[0].Line != 2 {
		t.Fatalf("cited = %+v", report.CitedEvidence)
	}
}
