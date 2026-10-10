package loading

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workflowfacts"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

type requestReceipts struct {
	Store
	rows []store.TurnLoadReceipt
	err  error
}

func (r requestReceipts) ListTurnLoadReceiptsForTurns(context.Context, []string) ([]store.TurnLoadReceipt, error) {
	return r.rows, r.err
}

type requestWorker struct{ task *api.WorkerTask }

func (w requestWorker) Get(string) (*api.WorkerTask, bool) { return w.task, w.task != nil }

func TestTurnRequestRetainsResolvedWorkflowAndRecordedDecision(t *testing.T) {
	history := []api.Message{{ID: "opening", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "/build", WorkflowRunID: "run"}, {ID: "foreign", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "unrelated", WorkflowRunID: "other"}}
	resolved := workflowfacts.ResolvedWorkflowRequest{RunID: "run", OpeningMessageID: "opening", Text: "Build the requested API"}
	for _, tc := range []struct {
		name string
		rows []store.TurnLoadReceipt
		err  error
		want bool
	}{
		{name: "unrecorded", want: true}, {name: "request only", rows: []store.TurnLoadReceipt{{Trigger: store.TurnLoadTriggerRequest}}, want: true}, {name: "recorded", rows: []store.TurnLoadReceipt{{Trigger: store.TurnLoadTriggerTurn}}}, {name: "unreadable", err: errors.New("database unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{store: requestReceipts{rows: tc.rows, err: tc.err}}
			opening, text, ok := s.Request(t.Context(), promptinput.Input{}, history, "", resolved)
			if ok != tc.want {
				t.Fatalf("request decided=%v", ok)
			}
			if ok && (opening != "opening" || text != resolved.Text) {
				t.Fatalf("request=%s %q", opening, text)
			}
		})
	}
	s := &Service{workerQueue: requestWorker{&api.WorkerTask{ID: "job", ParentSessionID: "parent", ChildSessionID: "child", AgentType: "implementer", Brief: "Original charter", LegID: "leg", Prompt: "Scoped follow-up"}}}
	child := &api.Session{ID: "child", ParentSessionID: "parent", AgentType: "implementer"}
	text, ok := s.WorkerRequest(child, "", "opening", []api.Message{{ID: "opening", WorkerID: "job"}})
	if !ok || text != "Scoped follow-up" {
		t.Fatalf("worker request=%q ok=%v", text, ok)
	}
	child.ParentSessionID = "foreign"
	if _, ok = s.WorkerRequest(child, "job", "", nil); ok {
		t.Fatal("assignment crossed parent session")
	}
}
