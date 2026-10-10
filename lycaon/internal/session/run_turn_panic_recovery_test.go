package session_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session/turnexecution"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// A panic before the idle-restoring defer is registered still releases the busy session.
func TestRunTurnPanicInBusyWindowSelfHeals(t *testing.T) {
	client := llm.NewMockProvider(&llm.MockConfig{
		Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}},
	})
	fix := setupContextualToolsFixtureWithLLM(t, api.SessionPostureBuild, client)
	ctx := context.Background()

	turnexecution.RunTurnBusyWindowFaultForTest = func() { panic("boom: injected busy-window panic") }
	t.Cleanup(func() { turnexecution.RunTurnBusyWindowFaultForTest = nil })

	_, err := fix.Mgr.Submissions.Prompt(ctx, fix.Sess.ID, "hello")
	if err == nil {
		t.Fatal("Prompt returned no error; want the panic surfaced as a turn error, not swallowed")
	}

	got, getErr := fix.Store.Get(ctx, fix.Sess.ID)
	testutil.FailErr(t, "Get", getErr)
	if got.Status != api.SessionStatusIdle {
		t.Fatalf("status = %s want idle; a panic in the busy window must not leave the session stuck busy", got.Status)
	}

	// A second prompt proves recovery without restarting the manager.
	turnexecution.RunTurnBusyWindowFaultForTest = nil
	if _, err := fix.Mgr.Submissions.Prompt(ctx, fix.Sess.ID, "hello again"); err != nil {
		testutil.FailErr(t, "Prompt after recovery", err)
	}
}
