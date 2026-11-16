package briefing

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func reasoningManifest(template string) Manifest {
	evidence := ReasoningItem(ReasoningInput{Excerpt: &Reasoning{Text: "Check the lockfile first.", Ref: "message:out"}}, "session")
	return Manifest{ProfileID: "tool_activity", ProfileVersion: "1", SectionID: "s", Template: Template{ID: template, Version: "1"}, Contract: OneSentence,
		Items: []Item{evidence.Item}}
}

func TestReasoningIsRefusedOutsideIntentQuestions(t *testing.T) {
	if _, err := reasoningManifest(purposeTemplate.ID).Hash(); err != nil {
		testutil.FailErr(t, "hash an intent question with reasoning", err)
	}
	for _, template := range []string{"activity_change", "step_change", "file_summary", ""} {
		if _, err := reasoningManifest(template).Hash(); err == nil {
			t.Fatalf("template %q hashed a reasoning item", template)
		}
	}
	withoutReasoning := reasoningManifest("activity_change")
	withoutReasoning.Items[0].SourceKind = "message"
	if _, err := withoutReasoning.Hash(); err != nil {
		testutil.FailErr(t, "hash a non-reasoning item", err)
	}
}

func TestPurposeSectionCarriesAvailableReasoning(t *testing.T) {
	ev := liveEvidence()
	ev.Reasoning = ReasoningInput{Excerpt: &Reasoning{Text: "Push before tagging.", Ref: "message:out"}}
	section, err := PurposeSection(t.Context(), ev)
	testutil.FailErr(t, "build purpose section", err)
	var found *Item
	for i := range section.Manifest.Items {
		if section.Manifest.Items[i].SourceKind == SourceReasoning {
			found = &section.Manifest.Items[i]
		}
	}
	if found == nil || found.Inclusion != IncludedWhole || found.Text != "Push before tagging." || found.Ref != "message:out" {
		t.Fatalf("reasoning item %+v", found)
	}
}
