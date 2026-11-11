package briefing

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func liveEvidence() ActionEvidence {
	ev := ActionEvidence{Tool: "command", CallRef: "tool_call:tc1", Args: map[string]any{"command": "git push"}, Files: []string{"a.go"}, Project: "/project",
		HostFacts: Excerpt{Text: "pushes to origin"}, Plan: []PlanStep{{Label: "Push", State: "pending"}}}
	ApplyCause(&ev, CausalRead{Found: true, OutputID: "out", Prose: "Pushing.", IntentID: "u1", Intent: "Ship it", CallsInOutput: 1,
		Results: []ResultExcerpt{{Tool: "read", Outcome: "success", Content: "ok", Ref: "message:r1"}}})
	return ev
}

func purposeHash(t *testing.T, profile string, ev ActionEvidence) string {
	t.Helper()
	section, err := PurposeSection(t.Context(), profile, "1", ev)
	testutil.FailErr(t, "build purpose section", err)
	hash, err := section.Manifest.Hash()
	testutil.FailErr(t, "hash purpose section", err)
	return hash
}

// The question is shared: equal evidence in profiles of equal version hashes
// alike only when the profile is the same, so reuse is by identical material.
func TestPurposeSectionIdentityFollowsMaterialAndScope(t *testing.T) {
	base := purposeHash(t, "tool_activity", liveEvidence())
	if again := purposeHash(t, "tool_activity", liveEvidence()); again != base {
		t.Fatal("equal evidence hashed differently")
	}
	scoped := liveEvidence()
	scoped.PhaseScope = "steps: read config; write output"
	if purposeHash(t, "tool_activity", scoped) == base {
		t.Fatal("a narrower scope reused the whole invocation's identity")
	}
	noPlan := liveEvidence()
	noPlan.Plan, noPlan.PlanGap = nil, "no immutable revision"
	if purposeHash(t, "tool_activity", noPlan) == base {
		t.Fatal("an unavailable plan matched an available one")
	}
}

func TestPurposeSectionRecordsGapsInsteadOfInventingInputs(t *testing.T) {
	ev := ActionEvidence{Tool: "command", Args: map[string]any{"command": "ls"}, PlanGap: "no immutable revision"}
	ApplyCause(&ev, CausalRead{})
	section, err := PurposeSection(t.Context(), "source_step", "1", ev)
	testutil.FailErr(t, "build", err)
	reasons := map[string]Item{}
	for _, item := range section.Manifest.Items {
		reasons[item.Key] = item
	}
	for _, key := range []string{"user_intent", "assistant_note", "assistant_reasoning", "tracked_plan"} {
		if reasons[key].Inclusion != Unavailable || reasons[key].Reason == "" || reasons[key].Text != "" {
			t.Fatalf("%s: %+v", key, reasons[key])
		}
	}
	if reasons["action"].Inclusion != IncludedWhole {
		t.Fatalf("action missing: %+v", reasons["action"])
	}
	if strings.Contains(section.Prompt.User, "no immutable revision") {
		t.Fatal("a gap reason reached the model")
	}
}

func TestPurposeSectionClampsAsTheApprovalCardAlwaysHas(t *testing.T) {
	ev := liveEvidence()
	ev.UserIntent.Text = strings.Repeat("goal ", 400)
	section, err := PurposeSection(t.Context(), "approval_rationale", "1", ev)
	testutil.FailErr(t, "build", err)
	for _, item := range section.Manifest.Items {
		if item.Key == "user_intent" && (item.Inclusion != IncludedTruncated || len([]rune(item.Text)) > sectionRunes+1) {
			t.Fatalf("intent not clamped: %d runes, %s", len([]rune(item.Text)), item.Inclusion)
		}
	}
	if section.Manifest.Contract != OneSentence || section.Prompt.MaxTokens != purposeMaxTokens {
		t.Fatalf("contract %+v tokens %d", section.Manifest.Contract, section.Prompt.MaxTokens)
	}
}

func TestReasoningIsRedactedBeforeItsSuffixIsKept(t *testing.T) {
	secret := "ghp_" + strings.Repeat("1", 36)
	filler := strings.Repeat("x", reasoningRunes-10)
	text := "start " + secret + filler
	read := CausalRead{Found: true, OutputID: "out", ReasoningJSON: `{"text":"` + text + `"}`, ReasoningBytes: int64(len(text)), CallsInOutput: 2}
	reasoning, state := ReasoningExcerpt(read)
	if state != ReasoningAvailable || reasoning == nil {
		t.Fatalf("state %s", state)
	}
	if strings.Contains(reasoning.Text, "1111111111") {
		t.Fatal("a secret crossing the suffix cut survived")
	}
	if !reasoning.Truncated || !reasoning.Shared || len([]rune(reasoning.Text)) > reasoningRunes {
		t.Fatalf("flags or bound wrong: %+v", reasoning)
	}
}

func TestReasoningGapsAreOptionalAbsence(t *testing.T) {
	cases := map[ReasoningState]CausalRead{
		ReasoningAbsent:      {Found: true},
		ReasoningOversized:   {Found: true, ReasoningBytes: ReasoningReadBytes + 1},
		ReasoningMalformed:   {Found: true, ReasoningBytes: 5, ReasoningJSON: "{not"},
		ReasoningDetailsOnly: {Found: true, ReasoningBytes: 30, ReasoningJSON: `{"details":[{"opaque":true}]}`},
	}
	for want, read := range cases {
		if reasoning, got := ReasoningExcerpt(read); got != want || reasoning != nil {
			t.Fatalf("%s: got %s %+v", want, got, reasoning)
		}
	}
}
