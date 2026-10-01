package session

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type turnClockRecorder struct {
	events []api.TurnClock
}

func (r *turnClockRecorder) ObserveTurnClock(event api.TurnClock) {
	r.events = append(r.events, event)
}

func TestPromptStartsWithFreshTurnClock(t *testing.T) {
	mgr, sessions := newTestManager(t)
	root, err := sessions.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create root", err)
	t.Cleanup(func() { progress.ForgetClock(root.ID) })
	observer := &turnClockRecorder{}
	mgr.SetEventPublisher(&events.Publisher{TurnClockObserver: observer})

	for range 2 {
		progress.TurnStarted(root.ID)
		time.Sleep(20 * time.Millisecond)
		progress.TurnFinished(root.ID)
		observer.events = nil
		_, err = mgr.Prompt(t.Context(), root.ID, "hello")
		testutil.FailErr(t, "run user prompt", err)
		if len(observer.events) != 3 {
			t.Fatalf("clock edges = %+v, want start, anchor, and finish", observer.events)
		}
		if start := observer.events[0]; !start.Running || start.ActiveMs != 0 || start.OpeningMessageID != "" {
			t.Fatalf("new prompt clock = %+v, want running from zero with no opening yet", start)
		}
		anchor := observer.events[1]
		if !anchor.Running || anchor.OpeningMessageID == "" {
			t.Fatalf("anchored clock = %+v, want running and naming its prompt", anchor)
		}
		opening, err := sessions.GetMessage(t.Context(), root.ID, anchor.OpeningMessageID)
		testutil.FailErr(t, "opening message", err)
		if opening.Role != api.MessageRoleUser || opening.Content != "hello" {
			t.Fatalf("opening message = %+v, want the prompt", opening)
		}
		finish := observer.events[2]
		if finish.Running || finish.SettledAt == "" || finish.OpeningMessageID != anchor.OpeningMessageID {
			t.Fatalf("completed prompt clock = %+v, want paused and settled for the same turn", finish)
		}

		durable, ok, err := sessions.LatestTurnClock(t.Context(), root.ID)
		testutil.FailErr(t, "latest durable clock", err)
		if !ok || durable.OpeningMessageID != anchor.OpeningMessageID || durable.RunningAt != nil || durable.SettledAt == nil {
			t.Fatalf("durable clock = %+v, want the settled turn", durable)
		}
		if durable.ActiveMs != finish.ActiveMs || durable.WorkMs != finish.WorkMs {
			t.Fatalf("durable clock = %+v, published %+v", durable, finish)
		}
	}
}

func TestTurnClockRestoresFromTheStoreAfterRestart(t *testing.T) {
	mgr, sessions := newTestManager(t)
	ctx := t.Context()
	root, err := sessions.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create root", err)
	t.Cleanup(func() { progress.ForgetClock(root.ID) })
	_, err = mgr.Prompt(ctx, root.ID, "hello")
	testutil.FailErr(t, "run user prompt", err)
	before := mgr.TurnClock(ctx, root.ID)

	progress.ForgetClock(root.ID)
	after := mgr.TurnClock(ctx, root.ID)
	if after.OpeningMessageID == "" || after.OpeningMessageID != before.OpeningMessageID {
		t.Fatalf("restored clock = %+v, want turn %q", after, before.OpeningMessageID)
	}
	if after.ActiveMs != before.ActiveMs || after.WorkMs != before.WorkMs || after.Running {
		t.Fatalf("restored clock = %+v, before restart %+v", after, before)
	}
	beforeSettled, err := time.Parse(time.RFC3339Nano, before.SettledAt)
	testutil.FailErr(t, "parse settled_at before restart", err)
	afterSettled, err := time.Parse(time.RFC3339Nano, after.SettledAt)
	testutil.FailErr(t, "parse settled_at after restart", err)
	if !afterSettled.Equal(beforeSettled) {
		t.Fatalf("restored settled_at = %s, want %s", afterSettled, beforeSettled)
	}
}

func TestCheckpointWaitIsActiveButNotWorkTime(t *testing.T) {
	mgr, sessions := newTestManager(t)
	ctx := t.Context()
	root, err := sessions.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create root", err)
	child, err := sessions.CreateChild(ctx, root, api.SpawnChildRequest{AgentType: orchestration.ProfilePathExplorer})
	testutil.FailErr(t, "create child", err)
	t.Cleanup(func() { progress.ForgetClock(root.ID) })

	finish := mgr.beginTurnClock(ctx, child.ID, false)
	resume := mgr.BeginCheckpointWait(ctx, child.ID)
	time.Sleep(40 * time.Millisecond)
	resume()
	finish()
	clock := progress.Clock(root.ID)
	if clock.ActiveMs < 40 || clock.ActiveMs-clock.WorkMs < 30 {
		t.Fatalf("clock = %+v, want the child's approval wait counted as active but not work", clock)
	}
}

func TestTurnClockOpensOnlyForNewRootUserTurn(t *testing.T) {
	mgr, sessions := newTestManager(t)
	ctx := t.Context()
	root, err := sessions.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create root", err)
	child, err := sessions.CreateChild(ctx, root, api.SpawnChildRequest{AgentType: orchestration.ProfilePathExplorer})
	testutil.FailErr(t, "create child", err)
	t.Cleanup(func() { progress.ForgetClock(root.ID) })

	finish := mgr.beginTurnClock(ctx, root.ID, true)
	time.Sleep(20 * time.Millisecond)
	finish()
	banked := progress.Clock(root.ID).ActiveMs
	if banked <= 0 {
		t.Fatal("first user turn should bank elapsed time")
	}
	for _, continuation := range []struct {
		id          string
		newUserTurn bool
	}{
		{root.ID, false},
		{child.ID, true},
	} {
		finish = mgr.beginTurnClock(ctx, continuation.id, continuation.newUserTurn)
		if got := progress.Clock(root.ID); got.ActiveMs < banked || !got.Running() {
			t.Fatalf("continuation clock = %+v, want at least %d and running", got, banked)
		}
		finish()
	}

	finish = mgr.beginTurnClock(ctx, root.ID, true)
	defer finish()
	if got := progress.Clock(root.ID); got.ActiveMs != 0 || !got.Running() {
		t.Fatalf("next user turn clock = %+v, want running from zero", got)
	}
}
