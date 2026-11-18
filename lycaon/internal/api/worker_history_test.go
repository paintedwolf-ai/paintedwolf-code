package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testtool"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/internal/tools/toolstest"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workerrecords"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type workerHistoryTestEnv struct {
	srv       *Server
	sqlDB     *db.Store
	store     *store.Repositories
	projects  *project.SQLRegistry
	queue     *worker.SQLQueue
	records   *workerrecords.Store
	projectID string
	sessionID string
	childID   string
}

func newWorkerHistoryTestEnv(t *testing.T) *workerHistoryTestEnv {
	t.Helper()
	ctx := t.Context()
	sqlDB := testdbfixture.Open(t, "worker-history.db")
	st := store.NewSQL(sqlDB)
	projReg := project.NewSQLRegistry(sqlDB)
	queue := worker.NewSQLServices(sqlDB, 1).Queue
	records := workerrecords.New(sqlDB)

	reg := toolstest.NewRegistry()
	mock := llm.NewMockProvider(testMockConfig(t))
	mgr := session.NewHost(st, session.Models{
		Client: mock,
		Limits: settings.DefaultSessionLimits(),
	}, reg)
	mgr.SetDataDir(t.TempDir())
	mgr.SetProjectRegistry(projReg)
	mgr.Coordinator.Guards.SetToolMetadata(testtool.RegistryInvoker{Registry: reg})
	wireTestBindingRegistry(t)
	testutil.FailErr(t, "install anchor registry", mgr.Coordinator.Guidance.InstallAnchorRegistry())

	rootDir := resolveTestPath(t, t.TempDir())
	proj, err := projReg.Create(ctx, project.CreateParams{
		Roots: []project.AttachRootParams{{Path: rootDir, IsPrimary: ptrBoolForTest(true)}},
	})
	testutil.FailErr(t, "create project", err)
	sess, err := st.Sessions.Create(ctx, wire.CreateSessionRequest{}, proj.ID)
	testutil.FailErr(t, "create session", err)
	child, err := st.Sessions.CreateChild(ctx, sess, wire.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create child session", err)

	srv := newServerForTest(t, Dependencies{
		Core: CoreDependencies{
			Database: sqlDB,
			Store:    st,
			Projects: projReg,
			Sessions: mgr,
		},
		Workflow: WorkflowDependencies{
			Workers: queue,
		},
	})

	return &workerHistoryTestEnv{
		srv:       srv,
		sqlDB:     sqlDB,
		store:     st,
		projects:  projReg,
		queue:     queue,
		records:   records,
		projectID: proj.ID,
		sessionID: sess.ID,
		childID:   child.ID,
	}
}

func (e *workerHistoryTestEnv) getHistory(t *testing.T, query url.Values) (*httptest.ResponseRecorder, wire.WorkerHistoryResponse) {
	t.Helper()
	req := newAuthedRequest(http.MethodGet, "/v1/workers/history?"+query.Encode(), nil)
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	var out wire.WorkerHistoryResponse
	if rec.Code == http.StatusOK {
		testutil.FailErr(t, "decode history response", json.Unmarshal(rec.Body.Bytes(), &out))
	}
	return rec, out
}

func TestGetWorkerHistoryValidatesQueryAndScope(t *testing.T) {
	env := newWorkerHistoryTestEnv(t)
	otherSession := uuid.NewString()

	cases := []struct {
		name string
		q    url.Values
		code int
	}{
		{
			name: "missing session_id",
			q:    url.Values{"project_id": {env.projectID}, "worker": {"scout"}},
			code: http.StatusBadRequest,
		},
		{
			name: "invalid session_id uuid",
			q:    url.Values{"project_id": {env.projectID}, "session_id": {"not-a-uuid"}, "worker": {"scout"}},
			code: http.StatusBadRequest,
		},
		{
			name: "missing worker",
			q:    url.Values{"project_id": {env.projectID}, "session_id": {env.sessionID}},
			code: http.StatusBadRequest,
		},
		{
			name: "non-positive before",
			q:    url.Values{"project_id": {env.projectID}, "session_id": {env.sessionID}, "worker": {"scout"}, "before": {"0"}},
			code: http.StatusBadRequest,
		},
		{
			name: "foreign before cursor",
			q:    url.Values{"project_id": {env.projectID}, "session_id": {env.sessionID}, "worker": {"scout"}, "before": {"not-a-cursor"}},
			code: http.StatusBadRequest,
		},
		{
			name: "limit above max",
			q:    url.Values{"project_id": {env.projectID}, "session_id": {env.sessionID}, "worker": {"scout"}, "limit": {"101"}},
			code: http.StatusBadRequest,
		},
		{
			name: "session not found",
			q:    url.Values{"project_id": {env.projectID}, "session_id": {otherSession}, "worker": {"scout"}},
			code: http.StatusNotFound,
		},
		{
			name: "unknown worker",
			q:    url.Values{"project_id": {env.projectID}, "session_id": {env.sessionID}, "worker": {"missing"}},
			code: http.StatusNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, _ := env.getHistory(t, tc.q)
			if rec.Code != tc.code {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.code, rec.Body.String())
			}
		})
	}
}

func TestGetWorkerHistoryProjectsRoundsMessagesAndPreservesUnread(t *testing.T) {
	ctx := t.Context()
	env := newWorkerHistoryTestEnv(t)
	coord := workerrecords.Actor{SessionID: env.sessionID, Role: workerrecords.RoleCoordinator, PrincipalID: env.sessionID}
	child := workerrecords.Actor{SessionID: env.childID, Role: workerrecords.RoleWorker, PrincipalID: env.childID}

	created, err := env.records.CreateWorker(ctx, coord, workerrecords.Op{ID: "op-create"}, workerrecords.WorkerSpec{
		Name:           "scout",
		AgentType:      "implementer",
		ScopeMode:      "write",
		Brief:          "Fix retry backoff",
		ChildSessionID: env.childID,
	})
	testutil.FailErr(t, "create worker", err)

	enqueueTask := func(jobID string, status wire.WorkerStatus) {
		t.Helper()
		var res *wire.WorkerResult
		if status == wire.WorkerStatusComplete {
			res = &wire.WorkerResult{
				Status:           "complete",
				CompletionReport: &wire.WorkerCompletionReport{LegStatus: "complete"},
			}
		}
		_, err := env.queue.Enqueue(ctx, wire.WorkerTask{
			ID:              jobID,
			ProjectID:       env.projectID,
			ParentSessionID: env.sessionID,
			ChildSessionID:  env.childID,
			AgentType:       "implementer",
			Status:          status,
			ExecutionTarget: wire.ExecutionTargetLocal,
			Prompt:          "work " + jobID,
			Brief:           "Fix retry backoff",
			Result:          res,
		})
		testutil.FailErr(t, "enqueue "+jobID, err)
	}

	settleRound := func(jobID, resultID, checkpointID string) {
		t.Helper()
		now := timelayout.Format(time.Now().UTC())
		_, err := env.sqlDB.ExecContext(ctx, `INSERT INTO worker_results (id, worker_job_id, status, created_at) VALUES (?, ?, 'complete', ?)`, resultID, jobID, now)
		testutil.FailErr(t, "insert result "+resultID, err)
		_, err = db.New(env.sqlDB).SettleWorkerRoundByJob(ctx, db.SettleWorkerRoundByJobParams{
			SettledAt:    db.NullString(now),
			CheckpointID: checkpointID,
			JobID:        jobID,
		})
		testutil.FailErr(t, "settle "+jobID, err)
	}

	job1 := uuid.NewString()
	res1 := uuid.NewString()
	enqueueTask(job1, wire.WorkerStatusComplete)
	_, err = env.records.AdmitRound(ctx, coord, workerrecords.Op{ID: "admit-1"}, created.WorkerID, workerrecords.RoundSpec{
		JobID:           job1,
		ExecutionTarget: "local",
	})
	testutil.FailErr(t, "admit round 1", err)
	settleRound(job1, res1, "cp-1")
	_, err = env.records.SetChange(ctx, coord, workerrecords.Op{ID: "shelve-1"}, res1, workerrecords.Change{
		State:        workerrecords.ChangeShelved,
		ShelveReason: "defer until API freeze",
	})
	testutil.FailErr(t, "shelve round 1", err)

	job2 := uuid.NewString()
	res2 := uuid.NewString()
	enqueueTask(job2, wire.WorkerStatusComplete)
	started2, err := env.records.StartRound(ctx, coord, workerrecords.Op{ID: "start-2"}, created.WorkerID, workerrecords.RoundStart{
		JobID:           job2,
		ExecutionTarget: "local",
		Instruction:     "Add jitter to the retry timer",
	})
	testutil.FailErr(t, "start round 2", err)
	notApplied, err := env.records.AppendMessage(ctx, coord, workerrecords.Op{ID: "msg-late"}, created.WorkerID, workerrecords.MessageSpec{
		Body: "Late note arriving as round 2 settled",
	})
	testutil.FailErr(t, "append late instruction", err)
	settleRound(job2, res2, "")

	job3 := uuid.NewString()
	enqueueTask(job3, wire.WorkerStatusRunning)
	started3, err := env.records.StartRound(ctx, coord, workerrecords.Op{ID: "start-3"}, created.WorkerID, workerrecords.RoundStart{
		JobID:           job3,
		ExecutionTarget: "local",
		Instruction:     "Verify zero-duration floor",
	})
	testutil.FailErr(t, "start round 3", err)
	queued, err := env.records.AppendMessage(ctx, coord, workerrecords.Op{ID: "msg-queued"}, created.WorkerID, workerrecords.MessageSpec{
		Body: "Also check negative duration inputs",
	})
	testutil.FailErr(t, "append queued instruction", err)
	wmsg, err := env.records.AppendMessage(ctx, child, workerrecords.Op{ID: "msg-worker"}, created.WorkerID, workerrecords.MessageSpec{
		Body:       "Found negative duration input in caller test",
		NeedsReply: true,
	})
	testutil.FailErr(t, "append worker message", err)

	for _, target := range []string{"scout", created.WorkerID, job1} {
		rec, page := env.getHistory(t, url.Values{
			"project_id": {env.projectID},
			"session_id": {env.sessionID},
			"worker":     {target},
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("target %s status = %d: %s", target, rec.Code, rec.Body.String())
		}
		if page.WorkerID != created.WorkerID || page.Name != "scout" || len(page.Rounds) != 3 || len(page.Messages) != 5 {
			t.Fatalf("target %s history = %+v", target, page)
		}
		if page.Rounds[0].ResultDisposition != wire.WorkerResultShelved || page.Rounds[0].ShelveReason != "defer until API freeze" {
			t.Fatalf("round 1 disposition = %+v", page.Rounds[0])
		}
		if page.Rounds[1].ResultDisposition != wire.WorkerResultUnaccounted || page.Rounds[1].ResultHandle != res2 {
			t.Fatalf("round 2 disposition = %+v", page.Rounds[1])
		}
		if page.Messages[0].Seq != int(started2.InstructionSeq) || page.Messages[0].InstructionStatus != wire.WorkerInstructionStatusApplied {
			t.Fatalf("msg 0 = %+v", page.Messages[0])
		}
		if page.Messages[1].Seq != int(notApplied.Seq) || page.Messages[1].InstructionStatus != wire.WorkerInstructionStatusNotApplied {
			t.Fatalf("msg 1 = %+v", page.Messages[1])
		}
		if page.Messages[2].Seq != int(started3.InstructionSeq) || page.Messages[2].InstructionStatus != wire.WorkerInstructionStatusApplied {
			t.Fatalf("msg 2 = %+v", page.Messages[2])
		}
		if page.Messages[3].Seq != int(queued.Seq) || page.Messages[3].InstructionStatus != wire.WorkerInstructionStatusQueued {
			t.Fatalf("msg 3 = %+v", page.Messages[3])
		}
		if page.Messages[4].Seq != int(wmsg.Seq) || page.Messages[4].SenderRole != wire.WorkerMessageSenderWorker || page.Messages[4].Delivered {
			t.Fatalf("msg 4 = %+v", page.Messages[4])
		}
	}

	// Paging backwards by round and message cursor returns older entries only.
	recOlder, older := env.getHistory(t, url.Values{
		"project_id":   {env.projectID},
		"session_id":   {env.sessionID},
		"worker":       {"scout"},
		"before_round": {"3"},
		"before":       {fmt.Sprintf("%d", started3.InstructionSeq)},
	})
	if recOlder.Code != http.StatusOK || len(older.Rounds) != 2 || len(older.Messages) != 2 {
		t.Fatalf("paged history status=%d rounds=%d msgs=%d", recOlder.Code, len(older.Rounds), len(older.Messages))
	}

	// Viewing history from Den leaves coordinator unread state untouched.
	delivered, err := env.records.DeliveredThrough(ctx, coord, created.WorkerID, env.sessionID)
	testutil.FailErr(t, "delivered through", err)
	if delivered != 0 {
		t.Fatalf("GET /v1/workers/history advanced coordinator delivery watermark to %d", delivered)
	}
}
