package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestWorkersBoardAndCancelHandlers(t *testing.T) {
	for _, st := range []wire.WorkerStatus{
		wire.WorkerStatusPending,
		wire.WorkerStatusRunning,
		wire.WorkerStatusWaiting,
		wire.WorkerStatusComplete,
		wire.WorkerStatusFailed,
		wire.WorkerStatusCanceled,
		wire.WorkerStatusHeld,
	} {
		if !validWorkerStatus(st) {
			t.Fatalf("validWorkerStatus(%q) = false, want true", st)
		}
	}
	if validWorkerStatus(wire.WorkerStatus("bogus")) {
		t.Fatal("validWorkerStatus accepted unknown status")
	}

	for _, lvl := range []wire.BoardDetailLevel{
		wire.BoardDetailLevelStatus,
		wire.BoardDetailLevelCompact,
		wire.BoardDetailLevelFull,
		wire.BoardDetailLevelForensic,
	} {
		if !validBoardDetailLevel(lvl) {
			t.Fatalf("validBoardDetailLevel(%q) = false, want true", lvl)
		}
	}
	if validBoardDetailLevel(wire.BoardDetailLevel("bogus")) {
		t.Fatal("validBoardDetailLevel accepted unknown level")
	}

	ctx := t.Context()
	rootDir := resolveTestPath(t, t.TempDir())
	projReg := project.NewMemoryRegistry()
	proj, err := project.CreateWithRoot(ctx, projReg, rootDir)
	testutil.FailErr(t, "create project", err)

	sessStore := store.NewMemory()
	coord, err := sessStore.Create(ctx, wire.CreateSessionRequest{ProjectID: proj.ID, Posture: wire.SessionPostureBuild}, proj.ID)
	testutil.FailErr(t, "create session", err)

	queue := worker.NewInMemoryQueue(2)
	_, err = queue.Enqueue(ctx, wire.WorkerTask{
		ID:              "job-pending",
		ParentSessionID: coord.ID,
		ProjectID:       proj.ID,
		WorkspacePath:   rootDir,
		AgentType:       "implementer",
		Prompt:          "implement feature",
		Brief:           "implement feature",
		Status:          wire.WorkerStatusPending,
	})
	testutil.FailErr(t, "enqueue pending job", err)
	_, err = queue.Enqueue(ctx, wire.WorkerTask{
		ID:              "job-complete",
		ParentSessionID: coord.ID,
		ProjectID:       proj.ID,
		WorkspacePath:   rootDir,
		AgentType:       "implementer",
		Prompt:          "done",
		Brief:           "done",
		Status:          wire.WorkerStatusComplete,
	})
	testutil.FailErr(t, "enqueue complete job", err)

	srv := newServerForTest(t, Dependencies{
		Core: CoreDependencies{
			Store:    sessStore,
			Projects: projReg,
		},
		Workflow: WorkflowDependencies{
			Workers:      queue,
			WorkerCancel: &worker.CancelService{Queue: queue},
			Board:        &board.SnapshotBuilder{Workers: queue, Repo: repotest.NewProvider(t)},
		},
	})

	serve := func(method, path string) *httptest.ResponseRecorder {
		req := newAuthedRequest(method, path, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	if rec := serve(http.MethodGet, "/v1/projects/"+proj.ID+"/board"); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing session_id code = %d, want 400", rec.Code)
	}
	if rec := serve(http.MethodGet, "/v1/projects/missing/board?session_id="+coord.ID); rec.Code != http.StatusNotFound {
		t.Fatalf("missing project code = %d, want 404", rec.Code)
	}
	if rec := serve(http.MethodGet, "/v1/projects/"+proj.ID+"/board?session_id=missing"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing session code = %d, want 404", rec.Code)
	}
	if rec := serve(http.MethodGet, "/v1/projects/"+proj.ID+"/board?session_id="+coord.ID+"&detail_level=bogus"); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid detail_level code = %d, want 400", rec.Code)
	}
	if rec := serve(http.MethodGet, "/v1/projects/"+proj.ID+"/board?session_id="+coord.ID+"&detail_level=status"); rec.Code != http.StatusOK {
		t.Fatalf("status board code = %d body = %s", rec.Code, rec.Body.String())
	}
	okBoard := serve(http.MethodGet, "/v1/projects/"+proj.ID+"/board?session_id="+coord.ID+"&detail_level=compact")
	var view wire.BoardView
	if okBoard.Code != http.StatusOK || json.Unmarshal(okBoard.Body.Bytes(), &view) != nil || view.SessionID != coord.ID {
		t.Fatalf("board status = %d body = %s", okBoard.Code, okBoard.Body.String())
	}

	if rec := serve(http.MethodPost, "/v1/workers/missing-job/cancel"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing worker cancel code = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
	if rec := serve(http.MethodPost, "/v1/workers/job-complete/cancel"); rec.Code != http.StatusConflict {
		t.Fatalf("terminal worker cancel code = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if rec := serve(http.MethodPost, "/v1/workers/job-pending/cancel"); rec.Code != http.StatusAccepted {
		t.Fatalf("pending worker cancel code = %d, want 202 (%s)", rec.Code, rec.Body.String())
	}
}
