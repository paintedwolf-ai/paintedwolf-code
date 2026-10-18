package guidance

import "testing"

func TestCloseoutReportFencePlacement(t *testing.T) {
	fence := "```json\n{\"cited_evidence\":[{\"evidence\":\"read#2\"}],\"artifact_ids\":[\"artifact\"],\"verification\":{\"method\":\"inspection\",\"reason\":\"Read the source\"}}\n```"
	for _, tc := range []struct{ name, input, body string }{
		{"markdown wrapper", "````markdown\nDone.\n\n" + fence + "\n````", "Done."},
		{"tilde wrapper", "~~~md\nDone.\n\n" + fence + "\n~~~", "Done."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read, ok := ReadCloseoutReport(tc.input, "")
			if !ok || read.Report.Synthesis != tc.body || len(read.Unread) != 0 || len(read.Report.CitedEvidence) != 1 || read.Report.CitedEvidence[0].Evidence != "read#2" || len(read.Report.ArtifactIDs) != 1 || read.Report.Verification == nil {
				t.Fatalf("read = %+v, ok = %v", read, ok)
			}
		})
	}
}

func TestCloseoutMarkdownWrapperRequiresCompleteFence(t *testing.T) {
	for _, content := range []string{
		"```markdown\nDone.",
		"````markdown\nDone.\n```",
		"```markdown\nDone.\n~~~",
		"```markdown\nDone.\n```\nOutside.",
		"```javascript\nDone.\n```",
	} {
		if got := unwrapOuterMarkdownFence(content); got != content {
			t.Fatalf("rewrote incomplete or non-Markdown wrapper %q to %q", content, got)
		}
	}
}

func TestCloseoutPreservesInlineReportShapedExamples(t *testing.T) {
	for _, payload := range []string{`{"findings":[]}`, `{"verification":{"method":"inspection","reason":"Example"}}`, `{"cited_evidence":[]}`} {
		content := "Here is an example:\n\n```json\n" + payload + "\n```\n\nUse this shape in your document."
		read, ok := ReadCloseoutReport(content, "")
		if !ok || read.Report.Synthesis != content || len(read.Unread) != 0 || read.Report.Verification != nil {
			t.Fatalf("example was consumed as metadata: %+v, ok=%v", read, ok)
		}
	}
}
