package briefing

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/testutil"
)

func liveEvidence() ActionEvidence {
	ev := ActionEvidence{Action: ActionFacts{Tool: "command", CallRef: "tool_call:tc1", Args: map[string]any{"command": "git push"}, Files: []string{"a.go"}, Project: "/project"},
		HostFacts: Excerpt{Text: "pushes to origin"}, Plan: PlanInput{Steps: []PlanStep{{Label: "Push", State: "pending"}}}}
	ApplyCause(&ev, CausalRead{Found: true, OutputID: "out", Prose: "Pushing.", IntentID: "u1", Intent: "Ship it", CallsInOutput: 1,
		Results: []ResultExcerpt{{Tool: "read", Outcome: "success", Content: "ok", Ref: "message:r1"}}})
	return ev
}

func purposeHash(t *testing.T, ev ActionEvidence) string {
	t.Helper()
	section, err := PurposeSection(t.Context(), ev)
	testutil.FailErr(t, "build purpose section", err)
	hash, err := section.Manifest.Hash()
	testutil.FailErr(t, "hash purpose section", err)
	return hash
}

// Whoever asks, equal material about the same scope is one question, so an
// approval's sentence is a stored result for a later step or activity.
func TestPurposeSectionIdentityFollowsMaterialAndScope(t *testing.T) {
	base := purposeHash(t, liveEvidence())
	if again := purposeHash(t, liveEvidence()); again != base {
		t.Fatal("equal evidence hashed differently")
	}
	scoped := liveEvidence()
	scoped.PhaseScope = "steps: read config; write output"
	if purposeHash(t, scoped) == base {
		t.Fatal("a narrower scope reused the whole invocation's identity")
	}
	noPlan := liveEvidence()
	noPlan.Plan = PlanInput{Gap: "no immutable revision"}
	if purposeHash(t, noPlan) == base {
		t.Fatal("an unavailable plan matched an available one")
	}
}

func TestPurposeSectionRecordsGapsInsteadOfInventingInputs(t *testing.T) {
	ev := ActionEvidence{Action: ActionFacts{Tool: "command", Args: map[string]any{"command": "ls"}}, Plan: PlanInput{Gap: "no immutable revision"}}
	ApplyCause(&ev, CausalRead{})
	section, err := PurposeSection(t.Context(), ev)
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
	section, err := PurposeSection(t.Context(), ev)
	testutil.FailErr(t, "build", err)
	for _, item := range section.Manifest.Items {
		if item.Key == "user_intent" && (item.Inclusion != IncludedTruncated || len([]rune(item.Text)) > sectionRunes+1) {
			t.Fatalf("intent not clamped: %d runes, %s", len([]rune(item.Text)), item.Inclusion)
		}
	}
	if !reflect.DeepEqual(section.Manifest.Contract, OneSentence) || section.Prompt.MaxTokens != purposeMaxTokens {
		t.Fatalf("contract %+v tokens %d", section.Manifest.Contract, section.Prompt.MaxTokens)
	}
}

// tailRead is what the store returns for text: its length and at most the
// last ReasoningTailChars characters.
func tailRead(text string) ReasoningRead {
	runes := []rune(text)
	tail := runes[max(0, len(runes)-ReasoningTailChars):]
	return ReasoningRead{Present: true, TextChars: int64(len(runes)), Tail: string(tail)}
}

func TestReasoningIsRedactedBeforeItsSuffixIsKept(t *testing.T) {
	secret := "ghp_" + strings.Repeat("1", 36)
	filler := strings.Repeat("x", reasoningRunes-10)
	got := ReasoningExcerpt(tailRead("start "+secret+" "+filler), "message:out", true)
	if got.State != ReasoningAvailable || got.Excerpt == nil {
		t.Fatalf("state %s", got.State)
	}
	if strings.Contains(got.Excerpt.Text, "1111111111") {
		t.Fatal("a secret crossing the suffix cut survived")
	}
	if !got.Excerpt.Truncated || !got.Excerpt.Shared || got.Excerpt.Ref != "message:out" || len([]rune(got.Excerpt.Text)) > reasoningRunes {
		t.Fatalf("flags or bound wrong: %+v", got.Excerpt)
	}
}

// A secret the catalog matches is redacted before the cut wherever it sits:
// whole-text redaction for traces the tail holds whole, and any secret that
// fits in the window before the excerpt for longer traces.
func TestRedactionPrecedesTheCut(t *testing.T) {
	var secrets []string
	release := observability.SetCaptureRedactor(func(s string) string {
		for _, secret := range secrets {
			s = strings.ReplaceAll(s, secret, "[catalog]")
		}
		return s
	})
	defer release()
	place := func(length, secretLen, start int) (string, string) {
		secret := strings.Repeat("S", secretLen)
		secrets = []string{secret}
		return strings.Repeat("x", start) + secret + strings.Repeat("x", length-start-secretLen), secret
	}
	short := 5000
	for _, start := range []int{short - 100 - 50, short - reasoningRunes - 20, short - 2000} {
		text, _ := place(short, 50, start)
		got := ReasoningExcerpt(tailRead(text), "message:out", false)
		redacted := []rune(observability.RedactCaptureText(text))
		if want := string(redacted[len(redacted)-reasoningRunes:]); got.Excerpt == nil || got.Excerpt.Text != want {
			t.Fatalf("short trace, secret at %d: excerpt is not the end of the whole redacted text", start)
		}
	}
	long := 40000
	for _, secretLen := range []int{50, 5000, ReasoningTailChars - reasoningRunes} {
		for _, start := range []int{long - reasoningRunes - secretLen/2, long - ReasoningTailChars, long - ReasoningTailChars - 10} {
			if start+secretLen > long {
				continue
			}
			text, _ := place(long, secretLen, start)
			got := ReasoningExcerpt(tailRead(text), "message:out", false)
			if got.Excerpt == nil || !got.Excerpt.Truncated || strings.Contains(got.Excerpt.Text, "S") {
				t.Fatalf("long trace, %d-character secret at %d reached the excerpt: %+v", secretLen, start, got)
			}
		}
	}
}

func TestReasoningGapsAreOptionalAbsence(t *testing.T) {
	cases := []struct {
		want ReasoningState
		read ReasoningRead
	}{
		{ReasoningAbsent, ReasoningRead{}},
		{ReasoningAbsent, ReasoningRead{Present: true}},
		{ReasoningAbsent, ReasoningRead{Present: true, TextChars: 3, Tail: "   "}},
		{ReasoningOversized, ReasoningRead{Present: true, OverCeiling: true}},
		{ReasoningDetailsOnly, ReasoningRead{Present: true, Details: 1}},
		{ReasoningMalformed, ReasoningRead{Present: true, TextChars: 4, Tail: "ok \xff"}},
	}
	for _, tc := range cases {
		got := ReasoningExcerpt(tc.read, "message:out", false)
		if got.State != tc.want || got.Excerpt != nil {
			t.Fatalf("%+v: got %+v, want %s", tc.read, got, tc.want)
		}
		if input := got.Input(); input.Excerpt != nil || (input.Gap == "") != (tc.want == ReasoningAbsent) {
			t.Fatalf("%s gave input %+v", tc.want, input)
		}
	}
}
