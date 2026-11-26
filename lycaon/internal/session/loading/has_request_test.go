package loading

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/workflowfacts"
	"github.com/lycaon/lycaon/pkg/api"
)

type resolvedWorkflow workflowfacts.ResolvedWorkflowRequest

func (w resolvedWorkflow) ResolvedRequest(context.Context, string) workflowfacts.ResolvedWorkflowRequest {
	return workflowfacts.ResolvedWorkflowRequest(w)
}

// A host turn continues a request only when Request could name one, so the
// two agree on every chat shape.
func TestHasRequestMatchesTheRequestATurnWouldDecideFor(t *testing.T) {
	coordinator := &api.Session{ID: "chat"}
	instruction := api.Message{ID: "opening", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Map this codebase"}
	wake := promptinput.Input{HostSignal: &promptinput.HostSignal{Kind: api.MessageKindHostLoopWake, ID: "loop_wake"}}
	hostNote := api.Message{ID: "note", Role: api.MessageRoleSystem, Origin: api.MessageOriginHost, Content: "Phase entered"}
	parentStart := api.Message{ID: "parent-start", Role: api.MessageRoleSystem, Origin: api.MessageOriginHost, WorkflowRunID: "parent-run", Content: "Workflow started"}
	for _, tc := range []struct {
		name     string
		sess     *api.Session
		history  []api.Message
		resolved workflowfacts.ResolvedWorkflowRequest
		want     bool
	}{
		{name: "empty chat", sess: coordinator},
		{name: "host messages only", sess: coordinator, history: []api.Message{hostNote}},
		{name: "user instruction", sess: coordinator, history: []api.Message{hostNote, instruction}, want: true},
		{name: "resolved workflow request", sess: coordinator, history: []api.Message{hostNote}, resolved: workflowfacts.ResolvedWorkflowRequest{RunID: "run", OpeningMessageID: "start", Text: "Build the requested API"}, want: true},
		// An approved plan's implement child inherits the plan's request; the
		// policy opens it at the parent's start, which carries the parent's run.
		{name: "inherited child request", sess: coordinator, history: []api.Message{parentStart, hostNote}, resolved: workflowfacts.ResolvedWorkflowRequest{RunID: "child-run", OpeningMessageID: parentStart.ID, Text: "Plan the change"}, want: true},
		{name: "inherited request with no opening", sess: coordinator, history: []api.Message{parentStart, hostNote}, resolved: workflowfacts.ResolvedWorkflowRequest{RunID: "child-run", Text: "Plan the change"}},
		{name: "worker leg", sess: &api.Session{ID: "child", ParentSessionID: "chat"}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{workflows: resolvedWorkflow(tc.resolved), turnStore: requestReceipts{}}
			if got := s.HasRequest(t.Context(), tc.sess, tc.history); got != tc.want {
				t.Fatalf("HasRequest = %v, want %v", got, tc.want)
			}
			if tc.sess.IsWorkerChild() {
				return
			}
			_, _, decides := s.Request(t.Context(), wake, tc.history, "", s.ResolvedRequest(t.Context(), tc.sess))
			if decides != tc.want {
				t.Fatalf("Request decides = %v, HasRequest = %v", decides, tc.want)
			}
		})
	}
}
