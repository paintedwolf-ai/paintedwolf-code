package batchcontrol

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/pkg/api"
	"reflect"
	"testing"
)

type reconcileFixture struct {
	t        *testing.T
	state    surface.ImplementSessionState
	events   []batch.Event
	anchors  []anchor.ID
	disarmed bool
}

func (f *reconcileFixture) Get(context.Context, string) (*api.Session, error) {
	return &api.Session{ID: "session", ProjectID: "project"}, nil
}
func (f *reconcileFixture) GetMessages(context.Context, string) ([]api.Message, error) {
	return nil, nil
}
func (f *reconcileFixture) ForSession(context.Context, *api.Session) surface.ImplementSessionState {
	return f.state
}
func (f *reconcileFixture) ApplyCoordinatorBatchEvent(_ context.Context, id string, event batch.Event, seq int) error {
	if id != "session" || seq != 7 {
		f.t.Fatalf("batch association %q/%d", id, seq)
	}
	f.events = append(f.events, event)
	return nil
}
func (f *reconcileFixture) WorkflowGateState(context.Context, *api.Session, []api.Message) (bool, bool, bool, bool) {
	return true, true, false, false
}
func (f *reconcileFixture) Emit(_ context.Context, id string, event anchor.ID, _ anchor.Envelope) {
	if id != "session" {
		f.t.Fatalf("guidance session=%q", id)
	}
	f.anchors = append(f.anchors, event)
}
func (f *reconcileFixture) DisarmTimerBackstop(context.Context, string) { f.disarmed = true }

type completedProgress struct{}

func (completedProgress) Get(context.Context, string) string { return "- [x] work" }

func TestReconcileHonorsPendingOverlaysAndInFlightWorkers(t *testing.T) {
	f := &reconcileFixture{t: t, state: surface.ImplementSessionState{BatchSeq: 7, BatchPhase: batch.PhaseIntegrate, PendingOverlayIDs: []string{"overlay"}}}
	s := New(f, f, f, f)
	s.SetWorkflow(f)
	s.SetProgress(completedProgress{})
	s.SetLoop(f)
	s.Reconcile(t.Context(), "session")
	if !reflect.DeepEqual(f.events, []batch.Event{batch.EventOverlaysPendingIdle}) || len(f.anchors) != 0 {
		t.Fatalf("unintegrated overlay advanced synthesis: %v %v", f.events, f.anchors)
	}
	f.events = nil
	f.state.PendingOverlayIDs = nil
	f.state.WorkersInFlight = 1
	s.Reconcile(t.Context(), "session")
	if len(f.events) != 0 {
		t.Fatalf("active worker advanced synthesis: %v", f.events)
	}
	f.state.WorkersInFlight = 0
	s.Reconcile(t.Context(), "session")
	if !reflect.DeepEqual(f.events, []batch.Event{batch.EventSynthesisReady}) || !reflect.DeepEqual(f.anchors, []anchor.ID{anchor.OverlayPromoteComplete}) {
		t.Fatalf("completed integration=%v %v", f.events, f.anchors)
	}
	f.state.BatchPhase = batch.PhaseSynthesize
	s.DisarmTerminal(t.Context(), "session")
	if !f.disarmed {
		t.Fatal("synthesizing batch retained timer backstop")
	}
}
