package session

import (
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
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	const phaseGuidance = "claims phase fixture"
	testutil.FailErr(t, "register phase fixture", engine.Register("kicks/coordinator-security-claims.md", phaseGuidance))
	mgr.SetPromptEngine(engine)
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, "project-1")
	testutil.FailErr(t, "create session", err)
	kicks := mgr.Coordinator.Runtime.Kicks()
	staged := anchor.InformRender(anchor.ComposeDone)
	kicks.QueueDeferred(sess.ID, staged)
	if got := kicks.TakePendingKickID(sess.ID); got != staged {
		t.Fatalf("staged = %q", got)
	}
	kicks.QueueDeferredLatest(sess.ID, anchor.PhaseEntered.String(), "coordinator-security-claims")

	msgs, err := mgr.Coordinator.Guidance.TakePhase(t.Context(), sess.ID)
	testutil.FailErr(t, "take phase guidance", err)
	if len(msgs) != 1 || msgs[0].Content != phaseGuidance || msgs[0].Visibility != api.MessageVisibilityInternal {
		t.Fatalf("delivered = %+v, want the claims phase guidance", msgs)
	}
	stored, err := st.GetMessage(t.Context(), sess.ID, msgs[0].ID)
	testutil.FailErr(t, "read delivered guidance", err)
	if stored.Content != msgs[0].Content {
		t.Fatal("phase guidance was not persisted")
	}
	if again, err := mgr.Coordinator.Guidance.TakePhase(t.Context(), sess.ID); err != nil || len(again) != 0 {
		t.Fatalf("second take = %+v %v, want the guidance delivered once", again, err)
	}
	if got, ok := kicks.PeekPendingKickID(sess.ID); !ok || got != staged {
		t.Fatalf("the kick staged for the turn start = %q %v", got, ok)
	}
}
