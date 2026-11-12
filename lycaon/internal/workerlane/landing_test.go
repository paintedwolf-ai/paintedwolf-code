package workerlane_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerlane"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// fakePromoter lands by writing the landing receipt Milestone 14 writes.
type fakePromoter struct {
	db       *db.Store
	promoted []string
	rejected []string
	status   api.WorkerMergeStatus
}

func (p *fakePromoter) PromoteOverlay(ctx context.Context, _, overlayID string, _ api.PromoteOverlayInput) (api.WorkerMergeResult, error) {
	p.promoted = append(p.promoted, overlayID)
	if p.status != api.WorkerMergeStatusMerged {
		return api.WorkerMergeResult{JobID: overlayID, Status: p.status}, nil
	}
	_, err := p.db.ExecContext(ctx, `INSERT INTO landed_changes (id, worker_job_id, canonical_path, created_at) VALUES (?, ?, '/repo', ?)`,
		"landed-"+overlayID, overlayID, db.FormatTime(time.Now().UTC()))
	return api.WorkerMergeResult{JobID: overlayID, Status: api.WorkerMergeStatusMerged}, err
}

func (p *fakePromoter) RejectOverlay(_ context.Context, _, overlayID, reason string) (api.OverlayRejectOutcome, error) {
	p.rejected = append(p.rejected, overlayID)
	return api.OverlayRejectOutcome{OverlayID: overlayID, Reason: reason}, nil
}

type landingFixture struct {
	db       *db.Store
	records  *workerrecords.Store
	ws       *fakeWorkspaces
	lanes    *workerlane.Service
	promoter *fakePromoter
	actor    workerrecords.Actor
	workerID string
}

// newLandingFixture has a worker with a prepared lane and one settled round
// on job-1 with result-1.
func newLandingFixture(t *testing.T) *landingFixture {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "landing.db")
	testdbseed.InsertSession(t, sqlDB, "coord", testdbseed.DefaultProjectID)
	now := db.FormatTime(time.Now().UTC())
	_, err := sqlDB.ExecContext(t.Context(), `INSERT INTO sessions (id, project_id, owner_person_id, posture, status, parent_session_id, created_at, activity_at, updated_at)
		VALUES ('child', ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', 'coord', ?, ?, ?)`, testdbseed.DefaultProjectID, now, now, now)
	testutil.FailErr(t, "insert child", err)
	records := workerrecords.New(sqlDB)
	actor := workerrecords.Actor{SessionID: "coord", Role: workerrecords.RoleCoordinator, PrincipalID: "coord"}
	w, err := records.CreateWorker(t.Context(), actor, workerrecords.Op{ID: "create"}, workerrecords.WorkerSpec{AgentType: "implementer", ScopeMode: workerrecords.ScopeWrite, ChildSessionID: "child"})
	testutil.FailErr(t, "create worker", err)
	ws := &fakeWorkspaces{base: t.TempDir()}
	f := &landingFixture{db: sqlDB, records: records, ws: ws, lanes: workerlane.New(ws, records), promoter: &fakePromoter{db: sqlDB, status: api.WorkerMergeStatusMerged}, actor: actor, workerID: w.WorkerID}
	_, err = f.lanes.Admit(t.Context(), workerlane.Request{Actor: actor, Op: workerrecords.Op{ID: "admit-1"}, WorkerID: w.WorkerID, Facts: workerlane.Facts{Write: true}})
	testutil.FailErr(t, "prepare lane", err)
	f.round(t, "job-1", "result-1", true)
	return f
}

func (f *landingFixture) round(t *testing.T, jobID, resultID string, settle bool) {
	t.Helper()
	now := db.FormatTime(time.Now().UTC())
	_, err := f.db.ExecContext(t.Context(), `INSERT INTO worker_jobs (id, project_id, parent_session_id, child_session_id, agent_type, status, prompt, brief, created_at)
		VALUES (?, ?, 'coord', 'child', 'implementer', 'complete', 'p', 'b', ?)`, jobID, testdbseed.DefaultProjectID, now)
	testutil.FailErr(t, "insert job", err)
	_, err = f.records.AdmitRound(t.Context(), f.actor, workerrecords.Op{ID: "round-" + jobID}, f.workerID, workerrecords.RoundSpec{JobID: jobID, ExecutionTarget: "local"})
	testutil.FailErr(t, "admit round", err)
	if !settle {
		return
	}
	_, err = f.db.ExecContext(t.Context(), `INSERT INTO worker_results (id, worker_job_id, status, created_at) VALUES (?, ?, 'complete', ?)`, resultID, jobID, now)
	testutil.FailErr(t, "insert result", err)
	_, err = f.records.SettleRound(t.Context(), f.actor, workerrecords.Op{ID: "settle-" + jobID}, f.workerID, jobID)
	testutil.FailErr(t, "settle round", err)
}

func (f *landingFixture) change(op string) workerlane.Change {
	return workerlane.Change{Actor: f.actor, Op: workerrecords.Op{ID: op}, ResultID: "result-1"}
}

func TestLandingRecordsReceiptAndRefreshesLane(t *testing.T) {
	f := newLandingFixture(t)
	out, err := f.lanes.Land(t.Context(), f.promoter, f.change("land"), api.PromoteOverlayInput{})
	testutil.FailErr(t, "land", err)
	if out.LaneErr != nil || out.Disposition.ChangeState != workerrecords.ChangeLanded || out.Disposition.LandedChangeID != "landed-job-1" {
		t.Fatalf("outcome = %+v", out)
	}
	if out.Lane.Generation != 2 || out.Lane.Action != workerlane.ActionRefresh || len(f.ws.evicted) != 1 {
		t.Fatalf("lane after landing = %+v, evicted %v", out.Lane, f.ws.evicted)
	}
	// A replay of a committed landing neither promotes nor prepares again.
	again, err := f.lanes.Land(t.Context(), f.promoter, f.change("land"), api.PromoteOverlayInput{})
	testutil.FailErr(t, "replay", err)
	if len(f.promoter.promoted) != 1 || len(f.ws.created) != 2 || again.Lane.Generation != 2 {
		t.Fatalf("replay promoted %v, prepared %d, lane %+v", f.promoter.promoted, len(f.ws.created), again.Lane)
	}
}

// Promotion succeeds, refresh fails: the landing stands and only the
// preparation is retried.
func TestLandingKeepsReceiptWhenRefreshFails(t *testing.T) {
	f := newLandingFixture(t)
	f.ws.failNext = errors.New("disk full")
	out, err := f.lanes.Land(t.Context(), f.promoter, f.change("land"), api.PromoteOverlayInput{})
	testutil.FailErr(t, "land", err)
	if out.LaneErr == nil || out.Disposition.ChangeState != workerrecords.ChangeLanded {
		t.Fatalf("outcome = %+v", out)
	}
	retry, err := f.lanes.Land(t.Context(), f.promoter, f.change("land"), api.PromoteOverlayInput{})
	testutil.FailErr(t, "retry", err)
	if retry.LaneErr != nil || retry.Lane.Generation != 2 || len(f.promoter.promoted) != 1 {
		t.Fatalf("retry = %+v, promoted %v", retry, f.promoter.promoted)
	}
}

func TestRejectionResetsLaneToPriorCheckpoint(t *testing.T) {
	f := newLandingFixture(t)
	change := f.change("reject")
	change.PriorCheckpoint = "cp-earlier"
	out, err := f.lanes.Reject(t.Context(), f.promoter, change, "wrong approach")
	testutil.FailErr(t, "reject", err)
	if out.Disposition.ChangeState != workerrecords.ChangeRejected || out.Lane.Generation != 2 || f.ws.created[1].parent != "cp-earlier" {
		t.Fatalf("outcome = %+v, created %+v", out, f.ws.created)
	}
	_, err = f.lanes.Reject(t.Context(), f.promoter, change, "wrong approach")
	testutil.FailErr(t, "replay", err)
	if len(f.promoter.rejected) != 1 {
		t.Fatalf("rejections = %v, want one discard", f.promoter.rejected)
	}
	_, err = f.lanes.Land(t.Context(), f.promoter, f.change("land-after-reject"), api.PromoteOverlayInput{})
	if !errors.Is(err, workerrecords.ErrDispositionFinal) {
		t.Fatalf("landing a rejected result = %v", err)
	}
}

// Admission first: landing or rejecting an earlier result is refused while a
// round runs, and nothing is queued.
func TestChangesRefusedWhileARoundRuns(t *testing.T) {
	f := newLandingFixture(t)
	f.round(t, "job-2", "", false)
	_, err := f.lanes.Land(t.Context(), f.promoter, f.change("land"), api.PromoteOverlayInput{})
	if !errors.Is(err, workerlane.ErrActiveExecution) {
		t.Fatalf("land during a round = %v", err)
	}
	_, err = f.lanes.Reject(t.Context(), f.promoter, f.change("reject"), "no")
	if !errors.Is(err, workerlane.ErrActiveExecution) {
		t.Fatalf("reject during a round = %v", err)
	}
	if len(f.promoter.promoted)+len(f.promoter.rejected) != 0 {
		t.Fatal("a refused change reached promotion")
	}
	f.promoter.status = api.WorkerMergeStatusPending
	_, err = f.records.SettleRound(t.Context(), f.actor, workerrecords.Op{ID: "settle-job-2"}, f.workerID, "job-2")
	testutil.FailErr(t, "settle", err)
	_, err = f.lanes.Land(t.Context(), f.promoter, f.change("land-pending"), api.PromoteOverlayInput{})
	if !errors.Is(err, workerlane.ErrNotLanded) {
		t.Fatalf("unlanded promotion = %v", err)
	}
}
