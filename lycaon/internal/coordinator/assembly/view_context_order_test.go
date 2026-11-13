package assembly

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// A prompt's view context sits directly after its source-change brief and
// directly before the prompt, and keeps its bytes on every later call.
func TestViewContextSitsDirectlyBeforeItsPrompt(t *testing.T) {
	eng := prefixStabilityEngine(t)
	deps := eng.deps()
	brief := inject.SourceChangeBrief{Files: []inject.SourceChangeFile{{Path: "notes.txt", Actor: "user", Op: "write", At: "14:02 UTC", Effects: 1}}}
	deps.TurnSourceBriefs = func(context.Context, *api.Session) map[string]inject.SourceChangeBrief {
		return map[string]inject.SourceChangeBrief{"u2": brief}
	}
	eng.SetDeps(deps)
	sess := &api.Session{ID: "view-window", AgentType: orchestration.ProfileCoordinator, WorkspacePath: t.TempDir()}
	view := &api.ViewContext{
		Editors:  []api.ViewEditor{{RootID: "r", Path: "apples.txt", DisplayPath: "apples.txt", Selection: &api.ViewSelection{StartLine: 3, EndLine: 5}}},
		Diffs:    []api.ViewPath{},
		Findings: []api.ViewFinding{},
	}
	eng.BeginPromptTurn(sess.ID, "")
	history := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "Count the apples."},
		{ID: "a1", Role: api.MessageRoleAssistant, Content: "There are 3."},
		{ID: "u2", Role: api.MessageRoleUser, Content: "Update these lines.", SourceContext: &api.SourceContext{Locations: []api.NavigationTarget{}, View: view}},
	}
	var previous []api.Message
	for _, observation := range []string{"Read apples.txt.", "Edited apples.txt."} {
		history = append(history, api.Message{Role: api.MessageRoleTool, Content: observation})
		msgs, err := eng.BuildCompletionMessages(context.Background(), sess, history, nil)
		testutil.FailErr(t, "assemble", err)
		viewIndex, briefIndex, openingIndex, count := -1, -1, -1, 0
		for i, msg := range msgs {
			switch {
			case msg.ID == "u2":
				openingIndex = i
			case strings.Contains(msg.Content, "apples.txt") && msg.Role == api.MessageRoleSystem:
				viewIndex = i
				count++
			case strings.Contains(msg.Content, "notes.txt") && msg.Role == api.MessageRoleSystem:
				briefIndex = i
			}
		}
		if count != 1 || viewIndex != openingIndex-1 || briefIndex != viewIndex-1 {
			t.Fatalf("brief=%d view=%d (count %d) opening=%d; want brief, view, prompt in a row", briefIndex, viewIndex, count, openingIndex)
		}
		if msgs[viewIndex].Origin != api.MessageOriginHost || msgs[viewIndex].Authority != api.ContentAuthorityNone {
			t.Fatalf("view block authority = %+v", msgs[viewIndex])
		}
		if previous != nil {
			for i := 0; i <= openingIndex; i++ {
				if previous[i].Content != msgs[i].Content {
					t.Fatalf("row %d changed between calls of one turn", i)
				}
			}
		}
		previous = msgs
	}
}
