package execution

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
)

func TestAttachPromptCancelStopContextSurvivesTurnDeadline(t *testing.T) {
	m := &Lifetime{}

	parent, cancelParent := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelParent()

	ctx := m.Attach(parent, "sess-1")
	defer m.Cancel("sess-1")

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

	m.Cancel("sess-1")
	select {
	case <-stop.Done():
	case <-time.After(time.Second):
		t.Fatal("stop context not canceled by CancelInFlightPrompt")
	}
}

func TestDetachPromptCancelReleasesStopContext(t *testing.T) {
	m := &Lifetime{}
	ctx := m.Attach(context.Background(), "sess-2")
	stop := hitl.WaitContext(ctx)

	m.Cancel("sess-2")
	select {
	case <-stop.Done():
	case <-time.After(time.Second):
		t.Fatal("detachPromptCancel must release (cancel) the stop context")
	}
}
