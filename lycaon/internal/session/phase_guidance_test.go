package session

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// A phase the coordinator enters with its own tool call is taught on its next
// model call, even while another kick waits for the next turn start.
func TestPhaseGuidanceIsDeliveredOnTheNextModelCall(t *testing.T) {
	st := store.NewMemory()
	mgr := NewManager(st, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, "project-1")
	testutil.FailErr(t, "create session", err)
	kicks := mgr.ensureCoordinatorRuntime().Kicks()
	staged := anchor.InformRender(anchor.ComposeDone)
	kicks.QueueDeferred(sess.ID, staged)
	if got := kicks.TakePendingKickID(sess.ID); got != staged {
		t.Fatalf("staged = %q", got)
	}
	kicks.QueueDeferredLatest(sess.ID, anchor.PhaseEntered.String(), "coordinator-security-claims")

	msgs, err := mgr.takePhaseGuidance(t.Context(), sess.ID)
	testutil.FailErr(t, "take phase guidance", err)
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "stamp the candidate claims") || msgs[0].Visibility != api.MessageVisibilityInternal {
		t.Fatalf("delivered = %+v, want the claims phase guidance", msgs)
	}
	stored, err := st.GetMessage(t.Context(), sess.ID, msgs[0].ID)
	testutil.FailErr(t, "read delivered guidance", err)
	if stored.Content != msgs[0].Content {
		t.Fatal("phase guidance was not persisted")
	}
	if again, err := mgr.takePhaseGuidance(t.Context(), sess.ID); err != nil || len(again) != 0 {
		t.Fatalf("second take = %+v %v, want the guidance delivered once", again, err)
	}
	if got, ok := kicks.PeekPendingKickID(sess.ID); !ok || got != staged {
		t.Fatalf("the kick staged for the turn start = %q %v", got, ok)
	}
}
