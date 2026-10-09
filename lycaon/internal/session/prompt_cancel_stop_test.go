package session

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
)

func TestAttachPromptCancelStopContextSurvivesTurnDeadline(t *testing.T) {
	m := &Manager{}

	parent, cancelParent := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelParent()

	ctx := m.attachPromptCancel(parent, "sess-1")
	defer m.detachPromptCancel("sess-1")

	stop := hitl.WaitContext(ctx)
	if _, hasDeadline := stop.Deadline(); hasDeadline {
		t.Fatal("stop context must not carry the turn deadline")
	}

	<-parent.Done()
	select {
	case <-stop.Done():
		t.Fatal("stop context canceled by the turn deadline")
	default:
	}

	m.CancelInFlightPrompt("sess-1")
	select {
	case <-stop.Done():
	case <-time.After(time.Second):
		t.Fatal("stop context not canceled by CancelInFlightPrompt")
	}
}

func TestDetachPromptCancelReleasesStopContext(t *testing.T) {
	m := &Manager{}
	ctx := m.attachPromptCancel(context.Background(), "sess-2")
	stop := hitl.WaitContext(ctx)

	m.detachPromptCancel("sess-2")
	select {
	case <-stop.Done():
	case <-time.After(time.Second):
		t.Fatal("detachPromptCancel must release (cancel) the stop context")
	}
}

func TestEngineShutdownCancelsStopContextAndRejectsLaterTurns(t *testing.T) {
	m := &Manager{}
	turn := m.attachPromptCancel(t.Context(), "before-stop")
	m.BeginEngineShutdown()
	select {
	case <-hitl.WaitContext(turn).Done():
	default:
		t.Fatal("engine shutdown left the approval stop context live")
	}
	later := m.attachPromptCancel(t.Context(), "after-stop")
	if later.Err() == nil {
		t.Fatal("new prompt survived engine shutdown")
	}
	if _, _, err := m.engineWork.Begin(t.Context()); err == nil {
		t.Fatal("engine work admitted after shutdown")
	}
}

func TestEngineShutdownWaitsForTurnSettlement(t *testing.T) {
	m := &Manager{}
	_, finish, err := m.engineWork.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin engine work: %v", err)
	}
	m.BeginEngineShutdown()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := m.WaitForEngineShutdown(ctx); err == nil {
		t.Fatal("shutdown drained before turn settlement")
	}
	finish()
	if err := m.WaitForEngineShutdown(t.Context()); err != nil {
		t.Fatalf("drain settled turn: %v", err)
	}
}
