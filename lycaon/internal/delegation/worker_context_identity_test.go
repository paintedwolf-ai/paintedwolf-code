package delegation_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerPolicyAssemblyPreservesInvocationContext(t *testing.T) {
	for _, delegated := range []bool{false, true} {
		name := "task"
		if delegated {
			name = "delegation"
		}
		t.Run(name, func(t *testing.T) {
			store := delegation.NewMemoryStore()
			sess := &api.Session{ID: "child", ParentSessionID: "parent", AgentType: "plan-writer"}
			job := &api.WorkerTask{ID: "job", ChildSessionID: sess.ID, ParentSessionID: sess.ParentSessionID, AgentType: sess.AgentType}
			if delegated {
				job.DelegationID, job.LegID = "delegation", "leg"
				_, err := store.Create(t.Context(), api.Delegation{ID: job.DelegationID, ProjectID: "project"}, sess.ParentSessionID,
					[]api.Leg{{ID: job.LegID, WorkerID: job.ID, AgentType: sess.AgentType, Status: api.LegStatusDispatched}})
				testutil.FailErr(t, "create delegation", err)
			}
			ctx, cancel := context.WithCancel(workercontext.WithJob(t.Context(), job.ID))
			defer cancel()
			called := false
			loader := &delegation.WorkerContextLoader{
				Store: store, Tasks: contextTaskLookup{task: job},
				AgentsMDChain: func(got context.Context, childID, _ string, _ []string) (api.Message, error) {
					called = true
					if childID != sess.ID || workercontext.Job(got) != job.ID {
						t.Fatalf("policy context lost invocation: child=%q job=%q", childID, workercontext.Job(got))
					}
					cancel()
					if got.Err() != context.Canceled {
						t.Fatal("policy assembly lost invocation cancellation")
					}
					return api.Message{Content: "worker policy"}, nil
				},
			}
			builder := &delegation.CompositeWorkerContext{Delegation: loader}
			got, err := builder.BuildWorkerPromptContext(ctx, sess.ID, sess)
			testutil.FailErr(t, "assemble worker policy", err)
			if !called || got.AgentsMDMessage.Content != "worker policy" {
				t.Fatalf("worker policy missing: called=%v message=%q", called, got.AgentsMDMessage.Content)
			}
		})
	}
}
