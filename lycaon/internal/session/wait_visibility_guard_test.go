package session

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

type pinnedSurface string

func (s pinnedSurface) PromptTurnSurfaceID(string) string { return string(s) }

func TestBeforeWaitReadsSurfaceAndDurableNotes(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	calls := []api.ToolCall{{ID: "wait-1", Name: "wait"}}
	for _, tc := range []struct {
		name, surface string
		noted         bool
		blocked       bool
	}{
		{"publishing surface owes the text", "implement_routing", false, true},
		{"private surface owes nothing", "implement_synthesis", false, false},
		{"root session on no surface owes nothing", "", false, false},
		{"a committed note settles it", "implement_routing", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr.Coordinator.Guards.SetSurface(pinnedSurface(tc.surface))
			history := []api.Message{{Role: api.MessageRoleAssistant, Content: "Still running", ToolCalls: calls}}
			if tc.noted {
				history = append(history, api.Message{Role: api.MessageRoleAssistant, Kind: api.MessageKindAgentNote, Content: "Visible update"})
			}
			_, _, err := mgr.Coordinator.Guards.BeforeTool(context.Background(), sess, history, "", "wait", map[string]any{"timeout_ms": 60000})
			refusal, rejected := guidance.RefusalFromError(err)
			if tc.blocked {
				if !rejected || refusal.Code() != guard.WaitHiddenTextCode {
					t.Fatalf("expected wait visibility rejection, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("wait should proceed: %v", err)
			}
		})
	}
}
