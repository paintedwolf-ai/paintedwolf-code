package promptloop

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOverflowRecoveryPreservesCanonicalInstruction(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	opener := api.Message{ID: "request", Ord: 2, Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Build and run the game"}
	rows := []api.Message{opener, {ID: "large", Ord: 3, Role: api.MessageRoleAssistant, Content: strings.Repeat("written source ", 3000)}, {ID: "latest", Ord: 4, Role: api.MessageRoleAssistant, Content: "Captured the game"}}
	for _, reloadDropsIntent := range []bool{false, true} {
		model := &modelTurn{}
		if reloadDropsIntent {
			model.Deps.CompactAfterContextOverflow = func(context.Context, *api.Session, []api.Message, int) ([]api.Message, error) { return rows[1:], nil }
		}
		got := model.recoverFromContextOverflow(t.Context(), &api.Session{ID: "s"}, "s", rows, nil, "", "provider", "model", 9000, &failure.ProviderContextTooSmallError{MaxContext: 2048})
		found := 0
		for _, row := range got {
			if row.ID == opener.ID {
				found++
				if row.Content != opener.Content || !row.ContextPinned {
					t.Fatalf("instruction changed: %+v", row)
				}
			}
		}
		if found != 1 {
			t.Fatalf("recovery lost or duplicated instruction: %+v", got)
		}
		if rows[0].ContextPinned {
			t.Fatal("recovery mutated canonical history")
		}
	}
}
