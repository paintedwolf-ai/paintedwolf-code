package session

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/testutil"
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

// AC-MID-04, AC-MID-12: a withheld reason reaches the next request as one durable
// host row whose parts keep their provenance, and it blocks nothing.
func TestWithheldTextBecomesOneStructuredNotice(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	gc := oar.NewGuardContext()
	gc.Session.SessionID = sess.ID
	gc.Session.MidTurnCitationsRequired = true
	gc.Rejection.RejectObservation = guidance.MidTurnObservation(guidance.MidTurnNoteUngroundedCode)
	gc.PutRejectData(guidance.MidTurnNoteUngroundedCode, map[string]any{"observed_handles_sample": "task#1"})
	testutil.FailErr(t, "evaluate", mgr.ToolPolicy.MidTurnNoteFeedback(context.Background(), sess, gc))
	messages, err := mgr.Coordinator.Guidance.TakePolicy(context.Background(), sess.ID)
	testutil.FailErr(t, "take notice", err)
	if len(messages) != 1 || !api.IsInternalHostUserNudge(messages[0]) {
		t.Fatalf("notice rows = %+v", messages)
	}
	notice := messages[0]
	for _, want := range []string{"MESSAGE WITHHELD FROM USER", "has not received", "task#1", "Publish a brief update", "Code: " + guidance.MidTurnNoteUngroundedCode} {
		if !strings.Contains(notice.Content, want) {
			t.Fatalf("notice lacks %q: %s", want, notice.Content)
		}
	}
	if strings.Contains(notice.Content, "Rejected:") {
		t.Fatalf("a non-blocking notice must not read as a refusal: %s", notice.Content)
	}
	if notice.ContentParts[0].Authority != api.ContentAuthoritySystem || notice.ContentParts[0].Origin != api.MessageOriginHost {
		t.Fatalf("host part lost its provenance: %+v", notice.ContentParts[0])
	}
	if again, err := mgr.Coordinator.Guidance.TakePolicy(context.Background(), sess.ID); err != nil || len(again) != 0 {
		t.Fatalf("notice repeated: %+v, %v", again, err)
	}
}
