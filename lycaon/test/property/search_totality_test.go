package property

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/pkg/api"
	"pgregory.net/rapid"
)

// Progress markers and phase explain notes, whose text is manifest copy
// rather than anything said in the chat, produce no evidence rows.
var kindProjectionExempt = map[api.MessageKind]bool{
	api.MessageKindProgressUpdate:   true,
	api.MessageKindProgressComplete: true,
	api.MessageKindIndexWarming:     true,
	api.MessageKindWorkflowExplain:  true,
}

func fixtureForKind(kind api.MessageKind) api.Message {
	msg := api.Message{
		ID:        "m-" + string(kind),
		Role:      api.MessageRoleAssistant,
		Kind:      kind,
		CreatedAt: time.Now().UTC(),
		Content:   "body for " + string(kind),
	}
	switch kind {
	case api.MessageKindWorkflowBoundary:
		msg.Role = api.MessageRoleSystem
		msg.WorkflowBoundary = &api.WorkflowBoundaryMeta{Event: "paused", Phase: "review"}
	case api.MessageKindWorkflowFeedback:
		msg.Role = api.MessageRoleSystem
		msg.WorkflowFeedback = &api.WorkflowFeedbackMeta{Answer: "yes"}
		msg.Content = "yes"
	case api.MessageKindBlueprint:
		msg.Blueprint = &api.BlueprintMeta{
			BlueprintPath: "p1", BlueprintTitle: "Plan", Status: api.BlueprintTranscriptStatusProposed,
			Phase: api.BlueprintCardPhaseReady, Revision: 1, RevisionKey: "r1",
		}
	case api.MessageKindDraft:
		msg.DraftStatus = api.DraftStatusCommitted
	case api.MessageKindProgressUpdate:
		msg.Role = api.MessageRoleSystem
		msg.ProgressUpdate = &api.ProgressUpdateMeta{Seq: 1, Initial: true}
	case api.MessageKindProgressComplete:
		msg.Role = api.MessageRoleSystem
		msg.ProgressComplete = &api.ProgressCompleteMeta{Seq: 1}
	case api.MessageKindIndexWarming:
		msg.Role = api.MessageRoleSystem
	case api.MessageKindWorkflowExplain:
		msg.Role = api.MessageRoleSystem
		msg.WorkflowExplain = &api.WorkflowExplainMeta{PhaseID: "ingest", Summary: "A scan runs first", Body: "Why."}
	case api.MessageKindSuperseded:
		msg.Role = api.MessageRoleTool
		msg.Content = `{"proof":{"changed_paths":["src/a.go"]}}`
		msg.WorkerSummary = &api.WorkerSummaryMeta{WorkerID: "j1", Status: api.WorkerSummaryStatusCanceled}
	case api.MessageKindAgentNote:
		msg.Role = api.MessageRoleAssistant
		msg.Grounding = &api.CitationGrounding{
			Traced: true,
			CitedEvidence: []api.CitationGroundingCitedEvidence{{
				Path: "src/a.go", Line: 1, Excerpt: "package main", Verdict: api.CitationVerdictMatched,
			}},
		}
	default:
	}
	return msg
}

func TestSearchProjectionTotalOverMessageKinds(t *testing.T) {
	for _, kind := range api.AllMessageKinds() {
		t.Run(string(kind), func(t *testing.T) {
			if kindProjectionExempt[kind] {
				return
			}
			msg := fixtureForKind(kind)
			rows := append(search.ProjectLifecycleEvidence("proj", "sess", msg), search.ProjectMessageText("proj", "sess", msg)...)
			rows = append(rows, search.ProjectMessage("proj", "sess", msg)...)
			if len(rows) == 0 {
				t.Fatalf("MessageKind %q produced no evidence rows", kind)
			}
		})
	}
}

func TestProjectLifecycleEvidenceNonEmptyForPlanRows(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		name := rapid.StringMatching(`[a-zA-Z0-9][a-zA-Z0-9 ]{0,40}`).Draw(t, "name")
		msg := api.Message{
			ID:        rapid.String().Draw(t, "id"),
			Role:      api.MessageRoleAssistant,
			Kind:      api.MessageKindBlueprint,
			Content:   "plan body",
			Blueprint: &api.BlueprintMeta{BlueprintPath: "p1", BlueprintTitle: name, Status: api.BlueprintTranscriptStatusProposed},
		}
		rows := search.ProjectLifecycleEvidence("proj", "sess", msg)
		if len(rows) == 0 {
			t.Fatal("plan row must project evidence")
		}
	})
}
