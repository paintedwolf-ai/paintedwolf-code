package session

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// jobLister serves fixed worker jobs for one parent session.
type jobLister struct {
	tasks []api.WorkerTask
}

func (j jobLister) ListBySession(_ context.Context, _, sessionID string, _ ...api.WorkerStatus) ([]api.WorkerTask, error) {
	var out []api.WorkerTask
	for _, task := range j.tasks {
		if task.ParentSessionID == sessionID {
			out = append(out, task)
		}
	}
	return out, nil
}
func (jobLister) ListPendingOutcomes(context.Context) ([]api.WorkerTask, error) { return nil, nil }
func (jobLister) Get(string) (*api.WorkerTask, bool)                            { return nil, false }
func (jobLister) ClaimWorkerBranch(context.Context, string) (*api.WorkerTask, error) {
	return nil, nil
}

func TestCloseoutEvidenceListsDispatchedLegsSinceTheIntent(t *testing.T) {
	st := store.NewMemory()
	mgr := NewManager(st, nil, nil, settings.DefaultSessionLimits())
	parent, err := st.Create(t.Context(), api.CreateSessionRequest{}, "project-1")
	testutil.FailErr(t, "create parent", err)
	intent := time.Unix(1_000, 0).UTC()
	mgr.SetWorkerQueue(jobLister{tasks: []api.WorkerTask{
		{ID: "old", ParentSessionID: parent.ID, ChildSessionID: "child-old", CreatedAt: intent.Add(-time.Minute)},
		{ID: "leg", ParentSessionID: parent.ID, ChildSessionID: "child-leg", LegID: "execute/leg-1", CreatedAt: intent.Add(time.Minute)},
		{ID: "plain", ParentSessionID: parent.ID, ChildSessionID: "child-plain", CreatedAt: intent.Add(2 * time.Minute)},
		{ID: "pending", ParentSessionID: parent.ID, CreatedAt: intent.Add(3 * time.Minute)},
		{ID: "other", ParentSessionID: "someone-else", ChildSessionID: "child-other", CreatedAt: intent.Add(time.Minute)},
	}})

	legs, err := mgr.CloseoutEvidence().WorkerLegs(t.Context(), parent.ID, intent)
	testutil.FailErr(t, "worker legs", err)
	if len(legs) != 2 || legs[0].ChildSessionID != "child-leg" || legs[0].Namespace() != "execute/leg-1" ||
		legs[1].ChildSessionID != "child-plain" || legs[1].Namespace() != "child-plain" {
		t.Fatalf("legs = %+v, want the two legs dispatched since the intent", legs)
	}
	all, err := mgr.CloseoutEvidence().WorkerLegs(t.Context(), parent.ID, time.Time{})
	testutil.FailErr(t, "every leg", err)
	if len(all) != 3 {
		t.Fatalf("legs without an intent = %+v, want every started leg", all)
	}
}
