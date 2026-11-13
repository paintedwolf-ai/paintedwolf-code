package delegations

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/app/persistence"
	"github.com/lycaon/lycaon/internal/app/sessions"
	"github.com/lycaon/lycaon/internal/session"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerReportPartialLegSkipsCoverageValidation(t *testing.T) {
	database := testdbfixture.Open(t, "worker-report-partial.db")
	testdbseed.InsertSession(t, database, "parent", testdbseed.DefaultProjectID)
	runtime := New(database, nil, worker.WorkersConfig{})
	id, err := runtime.Queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{ParentSessionID: "parent", AgentType: "implementer", Prompt: "Inspect source", Brief: "Inspect source", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "enqueue partial-leg worker", err)
	calls := 0
	validate := func(ctx context.Context, task *api.WorkerTask, review *api.CoverageReview) error {
		calls++
		return errors.New("coverage validation must be skipped for partial legs")
	}
	invocation := tools.ToolContext{Identity: tools.InvocationIdentity{WorkerJobID: id, SessionID: "child"}}
	record, err := runtime.DecodeCompleteLeg(t.Context(), map[string]any{"leg_status": "partial", "brief": "Observed result"}, invocation, validate)
	if record != nil || err == nil || !strings.Contains(err.Error(), "worker report services unavailable") || calls != 0 {
		t.Fatalf("partial leg misrouted: %+v, %v, calls=%d", record, err, calls)
	}
}

func TestWorkerReportRejectsForeignChildSession(t *testing.T) {
	database := testdbfixture.Open(t, "worker-report-identity.db")
	testdbseed.InsertSession(t, database, "parent", testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, database, "child", testdbseed.DefaultProjectID)
	runtime := New(database, nil, worker.WorkersConfig{})
	id, err := runtime.Queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{ParentSessionID: "parent", AgentType: "implementer", Prompt: "Inspect source", Brief: "Inspect source", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "enqueue mismatch worker", err)
	runtime.SetDependencies(Dependencies{
		Storage:  persistence.Runtime{Sessions: sessionstore.NewSQL(database)},
		Sessions: &sessions.Runtime{Manager: &session.Host{}},
	})
	invocation := tools.ToolContext{Identity: tools.InvocationIdentity{WorkerJobID: id, SessionID: "child"}}
	record, err := runtime.DecodeCompleteLeg(t.Context(), map[string]any{"leg_status": "partial", "brief": "Observed result"}, invocation, nil)
	if record != nil || err == nil || !strings.Contains(err.Error(), "does not match job") {
		t.Fatalf("foreign child session accepted: %+v, %v", record, err)
	}
}

func TestWorkerReportRequiresDurableChildSession(t *testing.T) {
	database := testdbfixture.Open(t, "worker-report-ghost.db")
	testdbseed.InsertSession(t, database, "parent", testdbseed.DefaultProjectID)
	runtime := New(database, nil, worker.WorkersConfig{})
	id, err := runtime.Queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{ParentSessionID: "parent", AgentType: "implementer", Prompt: "Inspect source", Brief: "Inspect source", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "enqueue ghost worker", err)
	runtime.SetDependencies(Dependencies{
		Storage:  persistence.Runtime{Sessions: sessionstore.NewSQL(database)},
		Sessions: &sessions.Runtime{Manager: &session.Host{}},
	})
	invocation := tools.ToolContext{Identity: tools.InvocationIdentity{WorkerJobID: id, SessionID: "ghost"}}
	record, err := runtime.DecodeCompleteLeg(t.Context(), map[string]any{"leg_status": "partial", "brief": "Observed result"}, invocation, nil)
	if record != nil || !errors.Is(err, sessionstore.ErrSessionNotFound) {
		t.Fatalf("missing child session misreported: %+v, %v", record, err)
	}
}

func TestWorkerReportAssemblesSummaryInputFromDurableChild(t *testing.T) {
	database := testdbfixture.Open(t, "worker-report-deep.db")
	testdbseed.InsertSession(t, database, "parent", testdbseed.DefaultProjectID)
	store := sessionstore.NewSQL(database)
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	child, err := store.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create child session", err)
	host := session.NewHost(store, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	runtime := New(database, nil, worker.WorkersConfig{})
	id, err := runtime.Queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{ParentSessionID: "parent", AgentType: "implementer", Prompt: "Inspect source", Brief: "Inspect source", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "enqueue deep worker", err)
	testutil.FailErr(t, "bind child session", runtime.Queue.SetChildSessionID(t.Context(), id, child.ID))
	runtime.SetDependencies(Dependencies{
		Storage:  persistence.Runtime{Sessions: store},
		Sessions: &sessions.Runtime{Manager: host},
	})
	invocation := tools.ToolContext{Identity: tools.InvocationIdentity{WorkerJobID: id, SessionID: child.ID}}
	record, err := runtime.DecodeCompleteLeg(t.Context(), map[string]any{"leg_status": "partial", "brief": "Observed result"}, invocation, nil)
	if err != nil && (strings.Contains(err.Error(), "services unavailable") || strings.Contains(err.Error(), "does not match job") || errors.Is(err, sessionstore.ErrSessionNotFound)) {
		t.Fatalf("durable input assembly failed: %v", err)
	}
	if err == nil && record == nil {
		t.Fatalf("acceptance returned neither record nor error: %+v", record)
	}
}
