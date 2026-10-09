package assembly

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Each turn's brief sits directly before the prompt that opened the turn, so
// it reads as what changed up to that turn's start, precedes everything the
// turn observed, and keeps its bytes on every later call.
func TestTurnSourceBriefsSitBeforeTheirOpeningPrompt(t *testing.T) {
	eng := prefixStabilityEngine(t)
	deps := eng.deps()
	brief := inject.SourceChangeBrief{
		Files: []inject.SourceChangeFile{{Path: "notes.txt", Actor: "user", Op: "write", At: "14:02 UTC", Effects: 1}},
	}
	loads := 0
	deps.TurnSourceBriefs = func(context.Context, *api.Session) map[string]inject.SourceChangeBrief {
		loads++
		return map[string]inject.SourceChangeBrief{"u2": brief, "gone": brief}
	}
	eng.SetDeps(deps)
	sess := &api.Session{ID: "source-window", AgentType: orchestration.ProfileCoordinator, WorkspacePath: t.TempDir()}
	ctx := context.Background()
	block := inject.RenderSourceChangesBlock(ctx, deps.Injects, sess.ID, brief)
	if block == "" {
		t.Fatal("source change fixture did not render")
	}
	projected := transcript.Project([]api.Message{{
		Role: api.MessageRoleSystem, Content: block,
		Origin: api.MessageOriginHost, Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierTrusted,
	}})
	block = projected[len(projected)-1].Content
	eng.BeginPromptTurn(sess.ID, "")
	history := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "Count the apples."},
		{ID: "a1", Role: api.MessageRoleAssistant, Content: "There are 3."},
		{ID: "u2", Role: api.MessageRoleUser, Content: "Update the inventory and summarize the result."},
	}
	var previous []api.Message
	for _, observation := range []string{
		"Read notes.txt at the current revision, including the human note.",
		"Edited apples to 4 and read back both files successfully.",
	} {
		history = append(history, api.Message{Role: api.MessageRoleTool, Content: observation})
		msgs, err := eng.BuildCompletionMessages(ctx, sess, history, nil)
		testutil.FailErr(t, "assemble after source observation", err)
		briefIndex, openingIndex, count := -1, -1, 0
		for i, msg := range msgs {
			if strings.Contains(msg.Content, block) {
				briefIndex = i
				count++
				if msg.Origin != api.MessageOriginHost || msg.Authority != api.ContentAuthorityNone {
					t.Fatalf("historical facts changed authority: %+v", msg)
				}
			}
			if msg.ID == "u2" {
				openingIndex = i
			}
		}
		if count != 1 || briefIndex < 0 || openingIndex != briefIndex+1 {
			t.Fatalf("the brief must sit once, right before its turn's prompt: count=%d brief=%d opening=%d", count, briefIndex, openingIndex)
		}
		if promptCacheMarkedIndex(msgs) <= openingIndex {
			t.Fatal("the brief displaced the history cache boundary")
		}
		// Every row through the prompt keeps its bytes from call to call.
		if previous != nil {
			for i := 0; i <= openingIndex; i++ {
				if previous[i].Content != msgs[i].Content {
					t.Fatalf("row %d changed between calls of one turn", i)
				}
			}
		}
		previous = msgs
	}
	if loads != 1 {
		t.Fatalf("briefs loaded %d times in one prompt run, want once", loads)
	}
}

func TestSourceChangeSnapshotNeverAppearsAsDynamicTailState(t *testing.T) {
	for _, brief := range []inject.SourceChangeBrief{
		{},
		{Truncated: true},
		{OtherFiles: 3, OtherEffects: 7},
		{Git: []inject.SourceGitLine{{Kind: "checkout", ToRef: "feature", At: "14:01 UTC"}}},
		{Files: []inject.SourceChangeFile{{Path: "file.txt", Actor: "external", Effects: 1}}},
	} {
		eng := prefixStabilityEngine(t)
		deps := eng.deps()
		calls := 0
		deps.TurnSourceBriefs = func(context.Context, *api.Session) map[string]inject.SourceChangeBrief {
			calls++
			return map[string]inject.SourceChangeBrief{"u1": brief}
		}
		eng.SetDeps(deps)
		sess := &api.Session{ID: "source-tail", AgentType: orchestration.ProfileCoordinator}
		eng.BeginPromptTurn(sess.ID, "")
		_, err := testTurnContext(eng).buildTailSystemInjects(context.Background(), sess, true, inject.CoordinatorTurnFrame{}, nil, nil, eng.cache.LoadTurn(sess.ID))
		testutil.FailErr(t, "assemble dynamic state", err)
		if calls != 0 {
			t.Fatal("a turn-start source snapshot was requested as live tail state")
		}
	}
}
