package guidance

import "testing"

func TestCloseoutReportFencePlacement(t *testing.T) {
	fence := "```json\n{\"cited_evidence\":[{\"evidence\":\"read#2\"}],\"artifact_ids\":[\"artifact\"],\"verification\":{\"method\":\"inspection\",\"reason\":\"Read the source\"}}\n```"
	for _, tc := range []struct{ name, input, body string }{
		{"leading", fence + "\n\nDone.", "Done."},
		{"signoff", "Done.\n\n" + fence + "\n\nThanks.", "Done.\n\nThanks."},
		{"markdown wrapper", "````markdown\nDone.\n\n" + fence + "\n````", "Done."},
		{"tilde wrapper", "~~~md\nDone.\n\n" + fence + "\n~~~", "Done."},
		{"leading wrapped", "````md\n" + fence + "\nDone.\n````", "Done."},
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
