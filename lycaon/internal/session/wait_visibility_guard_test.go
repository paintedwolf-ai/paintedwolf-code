package session

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBeforeWaitUsesOfferedNoteAndCurrentBatch(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	calls := []api.ToolCall{{ID: "wait-1", Name: "wait"}}
	for _, tc := range []struct {
		name                    string
		offered, noted, blocked bool
	}{
		{"note offered", true, false, true},
		{"note unavailable", false, false, false},
		{"note committed", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.offered {
				ctx = tools.WithRecoveryTools(ctx, []string{"wait", "surface_note"})
			}
			history := []api.Message{{Role: api.MessageRoleAssistant, Content: "Still running", ToolCalls: calls}}
			if tc.noted {
				history = append(history, api.Message{Role: api.MessageRoleAssistant, Kind: api.MessageKindAgentNote, Content: "Visible update"})
			}
			_, _, err := mgr.Coordinator.Guards.BeforeTool(ctx, sess, history, "", "wait", map[string]any{"timeout_ms": 60000})
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
