package session

import (
	"context"
	"testing"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testtool"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// User input interrupts a parked command; the user's turn opens with the
// program's interrupted result, ahead of the user's message.
func TestUserTurnOpensWithTheInterruptedParkedProgram(t *testing.T) {
	memory := store.NewMemory()
	registry := tools.NewStubRegistry()
	mgr := NewHost(memory, Models{Client: llm.NewMockProvider(testMockConfig(t)), Limits: settings.DefaultSessionLimits()}, registry)
	oartest.InstallCloseoutPolicy(t, mgr)
	mgr.Coordinator.Guards.SetToolMetadata(testtool.RegistryInvoker{Registry: registry})
	sess, err := memory.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	database := testdbfixture.Open(t, "waits.db")
	testdbseed.InsertSession(t, database, sess.ID, testdbseed.DefaultProjectID)
	loop := mgr.Coordinator.Runtime.CoordinatorLoop()
	loop.Subscriptions.SetWaitStore(&awaitstore.Store{DB: database})
	t.Cleanup(func() { loop.ForgetSession(context.Background(), sess.ID) })

	const partial = `{"program_id":"program-1","state":"interrupted","status":130,"stdout":"start\n"}`
	_, err = loopwake.ProgramParking{Subscriptions: loop.Subscriptions}.ParkProgram(t.Context(),
		tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sess.ID, ProjectID: testdbseed.DefaultProjectID, ToolCallID: "call-1"}},
		tools.ProgramPark{ProgramID: "program-1", Reason: "sleep 600", Claim: func() bool { return true }, Interrupt: func(context.Context) string { return partial }})
	testutil.FailErr(t, "park program", err)

	_, err = mgr.Submissions.Prompt(t.Context(), sess.ID, "stop waiting")
	testutil.FailErr(t, "run user turn", err)
	msgs, err := mgr.Runner.Transcript.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read transcript", err)
	ended, user := -1, -1
	for i, msg := range msgs {
		switch {
		case msg.Kind == api.MessageKindHostLoopWake && msg.Content == "Wait ended: program_done (interrupted). Code: TOOL_OWNER_INTERRUPTED\n\n"+partial:
			ended = i
		case msg.Origin == api.MessageOriginUser && msg.Content == "stop waiting":
			user = i
		}
	}
	if ended < 0 || user < 0 || ended > user || msgs[ended].Visibility != api.MessageVisibilityInternal {
		t.Fatalf("interrupted program at %d, user message at %d: %+v", ended, user, msgs)
	}
	if again := loop.Deliveries.TakeEndedWaits(sess.ID); len(again) != 0 {
		t.Fatalf("the interrupted program was delivered twice: %+v", again)
	}
}
