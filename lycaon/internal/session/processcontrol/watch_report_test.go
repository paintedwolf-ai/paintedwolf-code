package processcontrol

import (
	"context"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/watch"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWatchIntegrationRecordsCommandAndHeldSettlements(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	database := testdbfixture.Open(t, "process_watch.db")
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	testdbseed.InsertSession(t, database, "session", testdbseed.DefaultProjectID)

	f := &completionFixture{waiting: true}
	service := New(f, f, f, f)
	service.SetLoop(f, f)
	clock := testutil.NewFakeClock(now)
	ws := watch.NewStore(database, clock)
	service.SetWatches(ws)

	if _, ok := service.CompletedReport("session", "missing"); ok {
		t.Fatal("expected missing handle to report false")
	}

	owner := watch.Owner{ProjectID: testdbseed.DefaultProjectID, SessionID: "session"}
	cmdWatch, err := ws.Register(t.Context(), watch.RegisterRequest{
		Owner:        owner,
		InvocationID: "inv-cmd-1",
		Conditions:   []awaitstore.Condition{{Kind: "process_done", Handles: []string{"handle"}}},
		ExpiresAt:    now.Add(10 * time.Minute),
	})
	testutil.FailErr(t, "register command watch", err)

	service.HandleCommandCompletion(t.Context(), bgprocess.Completion{
		SessionID:  "session",
		Handle:     "handle",
		OriginTool: "command",
		ExitCode:   1,
		FinishedAt: now,
		Tail:       "exit 1",
	})

	fact, ok := service.CompletedReport("session", "handle")
	if !ok || fact.Handle != "handle" {
		t.Fatalf("CompletedReport = (%+v, %v), want handle", fact, ok)
	}
	snap, err := ws.Inspect(t.Context(), owner, cmdWatch.Handle)
	testutil.FailErr(t, "inspect command watch", err)
	if snap.LatestMatch == nil || snap.LatestMatch.Outcome != "failed" {
		t.Fatalf("expected failed command watch match, got %+v", snap.LatestMatch)
	}

	settledCh := make(chan string, 1)
	heldReg := heldcall.New(
		func(context.Context, string, string, api.BackgroundProcessEvent) {},
		func(sessID, h string) {
			service.HandleHeldCallSettled(sessID, h)
			settledCh <- h
		},
	)
	t.Cleanup(func() { _ = heldReg.Close(context.Background()) })
	service.Held = heldReg

	releaseCh := make(chan struct{})
	out, err := heldReg.Run(t.Context(), heldcall.Spec{
		DisplayTitle: "slow call",
		ProjectID:    testdbseed.DefaultProjectID,
		SessionID:    "session",
		ToolCallID:   "tc-held-1",
		Tool:         "read",
		ArgsDigest:   "a=1",
		Budget:       time.Millisecond,
	}, func(context.Context) heldcall.Settled {
		<-releaseCh
		return heldcall.Settled{
			Tool:    "read",
			Content: "done",
			Outcome: api.ToolResultOutcomeCompleted,
		}
	})
	testutil.FailErr(t, "run held call", err)
	if out.Handle == "" {
		t.Fatal("expected held call handle")
	}

	heldWatch, err := ws.Register(t.Context(), watch.RegisterRequest{
		Owner:        owner,
		InvocationID: "inv-held-1",
		Conditions:   []awaitstore.Condition{{Kind: "process_done", Handles: []string{out.Handle}}},
		ExpiresAt:    now.Add(10 * time.Minute),
	})
	testutil.FailErr(t, "register held watch", err)

	close(releaseCh)
	<-settledCh

	heldSnap, err := ws.Inspect(t.Context(), owner, heldWatch.Handle)
	testutil.FailErr(t, "inspect held watch", err)
	if heldSnap.LatestMatch == nil {
		t.Fatalf("expected held watch match after settlement, got %+v", heldSnap)
	}
}
