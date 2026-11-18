package property

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowlifecycle "github.com/lycaon/lycaon/internal/workflow/lifecycle"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"pgregory.net/rapid"
)

// unreadablePark fails the one read that decides whether a run waits on a person.
type unreadablePark struct{ workflowlifecycle.RunReader }

func (unreadablePark) GetScaffoldVars(context.Context, string) (map[string]any, error) {
	return nil, errors.New("scaffold vars unreadable")
}

// orphanCase is one session's generated run state before boot recovery.
type orphanCase struct {
	busy bool
	// cut is a boot cutoff after the runs were created; progressed means a
	// turn ran after it.
	cut, progressed bool
	// touched means recovery idled the session and the person renamed it
	// after the cutoff, which is not work.
	touched         bool
	ambient         bool
	rootStatus      api.WorkflowRunStatus
	child           bool
	pendingInput    bool
	unreadablePark  bool
	createdAfterCut bool
	terminal        []api.WorkflowRunStatus
	reconciles      int
}

func drawOrphanCase(t *rapid.T) orphanCase {
	c := orphanCase{
		busy:           rapid.Bool().Draw(t, "busy"),
		cut:            rapid.Bool().Draw(t, "cut"),
		ambient:        rapid.Bool().Draw(t, "ambient"),
		rootStatus:     rapid.SampledFrom([]api.WorkflowRunStatus{api.WorkflowRunStatusRunning, api.WorkflowRunStatusPaused}).Draw(t, "root_status"),
		pendingInput:   rapid.Bool().Draw(t, "pending_input"),
		unreadablePark: rapid.Bool().Draw(t, "unreadable_park"),
		terminal: rapid.SliceOfN(rapid.SampledFrom([]api.WorkflowRunStatus{
			api.WorkflowRunStatusComplete, api.WorkflowRunStatusFailed, api.WorkflowRunStatusCanceled, api.WorkflowRunStatusInterrupted,
		}), 0, 2).Draw(t, "terminal"),
		reconciles: rapid.IntRange(2, 3).Draw(t, "reconciles"),
	}
	if c.cut {
		c.progressed = rapid.Bool().Draw(t, "progressed")
		c.touched = rapid.Bool().Draw(t, "touched")
		c.createdAfterCut = rapid.Bool().Draw(t, "created_after_cut")
	}
	// Children run under an ambient root, which pauses for them.
	if c.ambient && rapid.Bool().Draw(t, "child") {
		c.child = true
		c.rootStatus = api.WorkflowRunStatusPausedOnChild
	}
	return c
}

// driven reports whether something other than recovery still moves the
// session's newest running run.
func (c orphanCase) driven() bool {
	return c.busy || c.progressed || c.pendingInput || c.unreadablePark || c.createdAfterCut
}

type runSnapshot struct {
	status     map[string]api.WorkflowRunStatus
	boundaries map[string]int
	messages   int
}

func snapshotRuns(t *rapid.T, ctx context.Context, wf *runstate.Repository, sessions *store.SQL, sessionID string, ids []string) runSnapshot {
	snap := runSnapshot{status: map[string]api.WorkflowRunStatus{}, boundaries: map[string]int{}}
	for _, id := range ids {
		run, err := wf.Runs.Get(ctx, id)
		failErr(t, "get run", err)
		snap.status[id] = run.Status
	}
	msgs, err := sessions.GetMessages(ctx, sessionID)
	failErr(t, "messages", err)
	snap.messages = len(msgs)
	for _, msg := range msgs {
		if msg.WorkflowBoundary != nil && msg.WorkflowBoundary.Event == string(api.WorkflowBoundaryKindInterrupted) {
			snap.boundaries[msg.WorkflowRunID]++
		}
	}
	return snap
}

// Boot reconcile settles a session's runs once: a running run nothing drives
// ends interrupted with one boundary, everything else is left as it was, and
// reconciling again changes nothing.
func TestOrphanedRunReconcileIsIdempotentAndLeavesNoUndrivenRun(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "interrupted.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	sessStore := store.NewSQL(sqlDB)
	wfStore := workflowpersistence.New(sqlDB)
	runMgr := workflow.NewManager(wfStore, sessStore, nil, nil)
	readable := runMgr.Recovery.Runs

	rapid.Check(t, func(rt *rapid.T) {
		c := drawOrphanCase(rt)
		sess, err := sessStore.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
		failErr(rt, "create session", err)
		var ids []string
		create := func(run *api.WorkflowRun) {
			failErr(rt, "create run", wfStore.State.CreateState(ctx, run, sess.WorkspacePath, nil))
			ids = append(ids, run.ID)
		}
		for _, status := range c.terminal {
			ended := time.Now().UTC()
			create(&api.WorkflowRun{ID: uuid.NewString(), SessionID: sess.ID, WorkflowID: "plan", WorkflowVersion: "1.0.0", Status: status, CurrentPhase: "review", CompletedAt: &ended})
		}
		root := &api.WorkflowRun{ID: uuid.NewString(), SessionID: sess.ID, WorkflowID: "plan", WorkflowVersion: "1.0.0", Status: c.rootStatus, CurrentPhase: "review"}
		if c.ambient {
			root.WorkflowID, root.AttachPolicy = "implement", string(workflowdef.AttachPolicySessionCreate)
		}
		create(root)
		newest := root
		if c.child {
			parent := root.ID
			newest = &api.WorkflowRun{
				ID: uuid.NewString(), SessionID: sess.ID, WorkflowID: "implement", WorkflowVersion: "1.0.0",
				AttachPolicy: string(workflowdef.AttachPolicySessionCreate), ParentRunID: &parent,
				Status: api.WorkflowRunStatusRunning, CurrentPhase: "boot",
			}
			create(newest)
		}
		if c.pendingInput {
			failErr(rt, "pending input", wfStore.State.UpdateVars(ctx, newest, sess.WorkspacePath, map[string]any{
				"user_feedback": map[string]any{"review": map[string]any{"pending": true, "prompt": "Approve this?"}},
			}))
		}
		// The cutoff is the next boot: after the runs, or before them for runs
		// this boot created.
		switch {
		case !c.cut:
			runMgr.Recovery.Before = time.Time{}
		case c.createdAfterCut:
			runMgr.Recovery.Before = time.Now().UTC().Add(-time.Hour)
		default:
			runMgr.Recovery.Before = time.Now().UTC()
		}
		if c.touched {
			failErr(rt, "busy", sessStore.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))
			failErr(rt, "idle", sessStore.SetSessionStatus(ctx, sess.ID, api.SessionStatusIdle))
			failErr(rt, "rename", sessStore.UpdateSession(ctx, sess.ID, func(s *api.Session) { s.Title = "Renamed after restart" }))
		}
		if c.progressed {
			turn, err := sessStore.BeginTurn(ctx, store.TurnStart{SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: store.TurnOriginUser, InputJSON: "{}"})
			failErr(rt, "begin turn", err)
			_, err = sessStore.FinishTurn(ctx, turn.Turn.ID, turn.Attempt.ID, store.TurnStatusComplete, "", "", "")
			failErr(rt, "finish turn", err)
		}
		runMgr.Recovery.Busy = func(context.Context, string) bool { return c.busy }
		runMgr.Recovery.Runs = readable
		if c.unreadablePark {
			runMgr.Recovery.Runs = unreadablePark{readable}
		}
		before := snapshotRuns(rt, ctx, wfStore, sessStore, sess.ID, ids)

		failErr(rt, "reconcile", runMgr.Recovery.ReconcileOrphanedRuns(ctx, sess.ID))
		settled := snapshotRuns(rt, ctx, wfStore, sessStore, sess.ID, ids)
		interrupted := newest.Status == api.WorkflowRunStatusRunning && !(newest == root && c.ambient) && !c.driven()
		want := maps.Clone(before.status)
		if interrupted {
			want[newest.ID] = api.WorkflowRunStatusInterrupted
			if c.child {
				// The ambient parent resumes once its child ends.
				want[root.ID] = api.WorkflowRunStatusRunning
			}
		}
		if !maps.Equal(settled.status, want) {
			rt.Fatalf("after reconcile runs = %v, want %v", settled.status, want)
		}
		for _, id := range ids {
			added := settled.boundaries[id] - before.boundaries[id]
			wantAdded := 0
			if interrupted && id == newest.ID {
				wantAdded = 1
			}
			if added != wantAdded {
				rt.Fatalf("run %s gained %d interrupted boundaries, want %d", id, added, wantAdded)
			}
		}
		for i := 1; i < c.reconciles; i++ {
			failErr(rt, "reconcile again", runMgr.Recovery.ReconcileOrphanedRuns(ctx, sess.ID))
			again := snapshotRuns(rt, ctx, wfStore, sessStore, sess.ID, ids)
			if !maps.Equal(again.status, settled.status) || !maps.Equal(again.boundaries, settled.boundaries) || again.messages != settled.messages {
				rt.Fatalf("reconcile %d changed the store: %+v, after the first %+v", i+1, again, settled)
			}
		}
	})
}
