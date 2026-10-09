package session

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/transcript"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTurnLoadWireProjectsAToolEvent(t *testing.T) {
	engine := decide.Engine{Name: "Painted Wolf Decide", Model: "mmbert-base", Head: "unit-rank"}
	read := turnload.SkillRank{Name: "verify-visual-change", Score: 3.6}
	outcome := turnload.ToolEventOutcome{
		Tool: "capture_page", Need: "Build me a chess game\n\nFirst tool called: capture_page",
		Ranking: turnload.Ranking{Ranks: []turnload.SkillRank{read, {Name: "work-with-containers", Score: 1.2}}, Engine: engine},
		Read:    &read, Engine: engine,
	}
	receipt := store.TurnLoadReceipt{
		ID: 9, SessionID: "s1", Trigger: store.TurnLoadTriggerToolEvent, OpeningMessageID: "user-1", ToolCallID: "call-4",
		Engine:    "Painted Wolf Decide/mmbert-base#unit-rank",
		Decisions: decisionsJSON(t, map[string]any{"tool_event": outcome, "preloaded_skill": &read}),
		ElapsedMs: 640,
	}
	got := transcript.TurnLoadWire(receipt)
	if got.Trigger != api.TurnLoadTriggerToolEvent || got.ToolCallID != "call-4" || got.Abstained {
		t.Fatalf("identity = %+v", got)
	}
	if got.Match == nil || got.Match.Need != "capture_page" || got.Match.By != api.TurnLoadMatchByEngine || len(got.Match.Names) != 1 || got.Match.Names[0] != "verify-visual-change" {
		t.Fatalf("match = %+v", got.Match)
	}
	if got.PreloadedSkill == nil || got.PreloadedSkill.Name != "verify-visual-change" || got.PreloadedSkill.Score != 3.6 {
		t.Fatalf("preloaded skill = %+v", got.PreloadedSkill)
	}
	if got.Engine == nil || got.Engine.Head != "unit-rank" {
		t.Fatalf("engine = %+v", got.Engine)
	}

	// A pointer names the skill without a read; an empty ranking matches nothing.
	pointer := turnload.SkillRank{Name: "commit-in-logical-groups", Score: 3.0}
	receipt.Decisions = decisionsJSON(t, map[string]any{"tool_event": turnload.ToolEventOutcome{Tool: "git_commit", Pointer: &pointer, Engine: engine}})
	got = transcript.TurnLoadWire(receipt)
	if got.PreloadedSkill != nil || got.Match == nil || got.Match.Names[0] != "commit-in-logical-groups" || got.Match.By != api.TurnLoadMatchByEngine {
		t.Fatalf("pointer projection = %+v", got)
	}
	receipt.Decisions = decisionsJSON(t, map[string]any{"tool_event": turnload.ToolEventOutcome{Tool: "git_commit", Engine: engine}})
	got = transcript.TurnLoadWire(receipt)
	if got.Match == nil || got.Match.By != api.TurnLoadMatchByNone || len(got.Match.Names) != 0 {
		t.Fatalf("no-match projection = %+v", got.Match)
	}
}
