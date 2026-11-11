package guidance

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/citationmd"
)

func readFresh(content string, reportPhase bool) CloseoutRead {
	return ReadCloseoutReport(content, RetainedCloseout{}, reportPhase, citationmd.Options{})
}

func TestReadCloseoutReportFencePlacement(t *testing.T) {
	const fence = "```json\n{\"headline\":\"Done\",\"limits\":[\"Scope\"]}\n```"
	for _, tc := range []struct {
		name, input, narrative string
		reportPhase, document  bool
	}{
		{"report phase", "Done [read#2].\n\n" + fence, "Done.", true, true},
		{"report phase in whole-response wrapper", "````markdown\nDone [read#2].\n\n" + fence + "\n````", "Done.", true, true},
		{"tilde wrapper", "~~~md\nDone [read#2].\n\n" + fence + "\n~~~", "Done.", true, true},
		{"AC-CLO-15 ordinary turn keeps the example", "Done [read#2].\n\n" + fence, "Done. " + strings.ReplaceAll(fence, "\n", " "), false, false},
		{"earlier fence is an example", "Shape:\n\n" + fence + "\n\nUse it.", "Shape: " + strings.ReplaceAll(fence, "\n", " ") + " Use it.", true, false},
		{"other info string", "Done.\n\n```js\n{\"headline\":\"Done\"}\n```", "Done. ```js {\"headline\":\"Done\"} ```", true, false},
		{"unclosed fence stays literal", "Done.\n\n```json\n{\"headline\":\"Done\"}", "Done. ```json {\"headline\":\"Done\"}", true, false},
		{"quoted fence stays literal", "Done.\n\n> ```json\n> {\"headline\":\"Done\"}\n> ```", "Done. > ```json > {\"headline\":\"Done\"} > ```", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := readFresh(tc.input, tc.reportPhase)
			if got := citationmd.Narrative(read.Draft.Parsed); got != tc.narrative {
				t.Fatalf("narrative = %q, want %q", got, tc.narrative)
			}
			if (read.Draft.Document != nil) != tc.document {
				t.Fatalf("document = %+v, want present=%v", read.Draft.Document, tc.document)
			}
			if len(read.Unread) != 0 {
				t.Fatalf("unread = %+v", read.Unread)
			}
		})
	}
}

// AC-CLO-13: a fence is one document type, and retired members are refused by name.
func TestReadCloseoutReportRefusesRetiredFenceMembers(t *testing.T) {
	content := "Answer.\n\n```json\n" + `{
		"synthesis": "Answer again.",
		"cited_evidence": [{"evidence": "read#2"}],
		"cited_urls": ["https://example.test/"],
		"artifact_ids": ["aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"],
		"verification": {"method": "targeted", "reason": "Ran it"},
		"headline": "Kept"
	}` + "\n```"
	read := readFresh(content, true)
	if read.Draft.Document == nil || read.Draft.Document.Headline != "Kept" {
		t.Fatalf("document = %+v, want the readable member kept", read.Draft.Document)
	}
	if len(read.Draft.CitedEvidence) != 0 || len(read.Draft.CitedURLs) != 0 || len(read.Draft.ArtifactIDs) != 0 || read.Draft.Verification != nil {
		t.Fatalf("a retired member reached typed state: %+v", read.Draft)
	}
	issue, ok := ReportFenceUnreadable(read.Unread)
	if !ok || issue.Code != ReportFenceUnreadableCode || issue.Count != 5 {
		t.Fatalf("issue = %+v ok=%v, want one refusal naming five members", issue, ok)
	}
	sample := strings.Join(issue.Offenders, "\n")
	for _, want := range []string{
		"`synthesis`: the answer is the Markdown above the fence",
		"`cited_evidence`: written inline",
		"`cited_urls`: written inline",
		"`artifact_ids`: written inline",
		"`verification`: written inline",
	} {
		if !strings.Contains(sample, want) {
			t.Fatalf("offenders %q lack %q", sample, want)
		}
	}
}

func TestReadCloseoutReportRefusesAFenceThatIsNotAnObject(t *testing.T) {
	read := readFresh("Answer.\n\n```json\n[1, 2]\n```", true)
	if read.Draft.Document != nil {
		t.Fatalf("document = %+v", read.Draft.Document)
	}
	issue, ok := ReportFenceUnreadable(read.Unread)
	if !ok || !strings.Contains(issue.Offenders[0], "one JSON object") {
		t.Fatalf("issue = %+v ok=%v", issue, ok)
	}
}

// D14: the kick lists document members only and echoes only document members.
func TestReportFenceAdvertisesOnlyDocumentMembers(t *testing.T) {
	allowed := strings.Join(AllowedReportFenceKeys(), ",")
	for _, retired := range retiredFenceMembers {
		if strings.Contains(allowed, retired) {
			t.Fatalf("allowed keys %q advertise retired %q", allowed, retired)
		}
	}
	for _, member := range []string{"headline", "summary", "rating", "findings", "limits", "ask", "set_asides"} {
		if !strings.Contains(allowed, member) {
			t.Fatalf("allowed keys %q lack %q", allowed, member)
		}
	}
	draft := readFresh("Answer [read#2].\n[verify: targeted - Ran it]\n\n```json\n{\"headline\":\"H\",\"rating\":{\"level\":\"low\",\"why\":\"w\"}}\n```", true).Draft
	echoed := ReportDocumentFence(draft)
	for _, retired := range retiredFenceMembers {
		if strings.Contains(echoed, retired) {
			t.Fatalf("fence %q echoes retired %q", echoed, retired)
		}
	}
	if !strings.Contains(echoed, `"rating"`) || !strings.Contains(echoed, `"headline"`) {
		t.Fatalf("fence %q lost document members", echoed)
	}
	if got := ReportDocumentFence(CloseoutDraft{}); got != "" {
		t.Fatalf("no document must render nothing, never a fence that clears fields: %q", got)
	}
}

// A fence alone answers nothing on a report phase, and is a literal answer elsewhere.
func TestFenceOnlyDraftHasAnswerOnlyOutsideReportPhases(t *testing.T) {
	const fence = "```json\n{\"headline\":\"H\"}\n```"
	if readFresh(fence, true).Draft.HasAnswer() {
		t.Fatal("a document with nothing above it has no answer")
	}
	if !readFresh(fence, false).Draft.HasAnswer() {
		t.Fatal("outside a report phase the fence is literal Markdown")
	}
}

func TestCloseoutPreservesInlineReportShapedExamples(t *testing.T) {
	for _, payload := range []string{`{"findings":[]}`, `{"verification":{"method":"inspection","reason":"Example"}}`, `{"cited_evidence":[]}`} {
		content := "Here is an example:\n\n```json\n" + payload + "\n```\n\nUse this shape in your document."
		for _, reportPhase := range []bool{false, true} {
			read := readFresh(content, reportPhase)
			if read.Draft.Synthesis != content || len(read.Unread) != 0 || read.Draft.Verification != nil || read.Draft.Document != nil {
				t.Fatalf("example was consumed as metadata: %+v", read)
			}
		}
	}
}
