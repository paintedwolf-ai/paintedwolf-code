package workerworkspace

import (
	"context"
	"github.com/lycaon/lycaon/internal/call"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

type reservationRows struct {
	Calls
	rows    []call.ActiveReservation
	session string
}

func (r *reservationRows) ListActiveReservations(_ context.Context, id string) ([]call.ActiveReservation, error) {
	r.session = id
	return r.rows, nil
}

type reservationJobs struct {
	Tasks
	jobs map[string]*api.WorkerTask
}

func (j reservationJobs) Get(id string) (*api.WorkerTask, bool) { t, ok := j.jobs[id]; return t, ok }

type reservationSessions struct{}

func (reservationSessions) Get(context.Context, string) (*api.Session, error) {
	return &api.Session{ID: "child", ParentSessionID: "parent"}, nil
}

func TestPeerReservationsHideOwnHoldsAndRetainSiblingLabels(t *testing.T) {
	rows := &reservationRows{rows: []call.ActiveReservation{{Path: " own.txt ", Agent: " own "}, {Path: "peer.txt", Agent: "peer"}, {Path: "brief.txt", Agent: "brief"}, {Path: "invalid.txt"}, {Agent: "peer"}}}
	jobs := reservationJobs{jobs: map[string]*api.WorkerTask{"own": {ID: "own", LegID: "local"}, "peer": {ID: "peer", LegID: "review"}, "brief": {ID: "brief", Brief: "inspect changed bytes\nmore details"}}}
	service := New(reservationSessions{}, nil, nil)
	service.calls = rows
	service.SetTasks(jobs)
	ctx := workercontext.WithJob(t.Context(), "own")
	peers := service.PeerReservations(ctx, "child")
	if rows.session != "parent" || len(peers) != 2 || peers[0].Path != "peer.txt" || peers[0].JobID != "peer" || peers[0].LegLabel != "review" || peers[1].LegLabel != "inspect changed bytes" {
		t.Fatalf("peer holds=%+v lookup=%q", peers, rows.session)
	}
	board := service.ReservationEntries(" parent ")
	if rows.session != "parent" || len(board) != 3 || board[0].Path != "own.txt" || board[0].JobID != "own" || board[0].LegLabel != "local" {
		t.Fatalf("board holds=%+v lookup=%q", board, rows.session)
	}
}
