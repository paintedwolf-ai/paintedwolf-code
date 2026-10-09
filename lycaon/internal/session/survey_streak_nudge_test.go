package session

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSurveyStreakNudgeNamesTheStreakForTheCoordinatorOnly(t *testing.T) {
	mgr, _ := newTestManager(t)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	testutil.FailErr(t, "install anchor registry", mgr.Guidance.InstallAnchorRegistry())

	nudge := mgr.Nudges.SurveyStreak(t.Context(), &api.Session{ID: "root"}, 6, []string{"git_diff", "read"})
	if nudge.Empty() {
		t.Fatal("coordinator survey streak rendered empty")
	}
	for _, want := range []string{"6 consecutive", "`git_diff`", "`read`", "act on what you have"} {
		if !strings.Contains(nudge.Content, want) {
			t.Fatalf("nudge missing %q:\n%s", want, nudge.Content)
		}
	}
	worker := mgr.Nudges.SurveyStreak(t.Context(), &api.Session{ID: "child", ParentSessionID: "root"}, 6, []string{"read"})
	if !worker.Empty() {
		t.Fatalf("a worker's reads are its job, not a streak: %q", worker.Content)
	}
}

func TestSurveyStreakNudgeStaysQuietOnInspectTurns(t *testing.T) {
	mgr, _ := newTestManager(t)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	testutil.FailErr(t, "install anchor registry", mgr.Guidance.InstallAnchorRegistry())
	ledger := turnload.NewLedger()
	mgr.Loading.SetLedger(ledger)
	mgr.Nudges.SetLedger(ledger)
	sess := &api.Session{ID: "root"}

	ledger.BeginTurn(sess.ID, turnload.TurnOpening{}, turnload.Decision{Kind: turnload.KindInspect})
	if nudge := mgr.Nudges.SurveyStreak(t.Context(), sess, 6, []string{"grep"}); !nudge.Empty() {
		t.Fatalf("an inspect turn's reads are its work, not a streak: %q", nudge.Content)
	}
	ledger.BeginTurn(sess.ID, turnload.TurnOpening{}, turnload.Decision{Kind: turnload.KindChange})
	if nudge := mgr.Nudges.SurveyStreak(t.Context(), sess, 6, []string{"grep"}); nudge.Empty() {
		t.Fatal("a change turn's read-only streak rendered empty")
	}
	ledger.BeginTurn(sess.ID, turnload.TurnOpening{}, turnload.Decision{Kind: turnload.KindInspect})
	ledger.BeginUndecidedTurn(sess.ID, turnload.TurnOpening{})
	if nudge := mgr.Nudges.SurveyStreak(t.Context(), sess, 6, []string{"grep"}); nudge.Empty() {
		t.Fatal("an undecided turn kept the previous turn's kind")
	}
}
