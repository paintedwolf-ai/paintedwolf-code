package session

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/governance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/progress"
	queuestore "github.com/lycaon/lycaon/internal/queue"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Bounded stores are zero-value usable.
func newForgetTestManager() *Manager {
	return &Manager{}
}

func TestForgetSessionClearsEveryReleasedStore(t *testing.T) {
	m := newForgetTestManager()
	var forgot []string
	testutil.FailErr(t, "register approval cleanup", m.RegisterSessionCleanup("approvals", 50, func(_ context.Context, id string) error {
		forgot = append(forgot, id)
		return nil
	}))

	const sid = "sess-1"
	m.progressClosureExpect.Store(sid, guard.ProgressClosureBaseline{Closed: 1})
	seedCloseoutState(&m.closeout, sid, sid)
	m.coordinatorBatchTurn.Store(sid, true)
	checkpoints := sessioncheckpoint.New(t.TempDir(), t.TempDir(), sessionstore.NewMemory())
	testutil.FailErr(t, "open checkpoint capture", m.checkpointCapture.Open(t.Context(), checkpoints, sid, "anchor"))
	m.mergeReconcile.Store(sid, map[string]struct{}{"a.go": {}})
	m.compactionTokenCalibration.Store(sid, compaction.PromptTokenCalibration{
		ReportedPromptTokens: 150, TranscriptEstimate: 100,
	})
	m.promotePathStatus.Store(sid, &promotePathSessionStore{})
	m.agentsMDCache.Store(sid, &governance.AgentsMDSessionState{})
	m.writeRootRuntime = approvalstate.NewSandboxPathGrantRuntime()
	m.writeRootRuntime.GrantChat(sid, "/opt/cache", "grant-1", "cp-1", nil)
	m.queue = queuestore.New()
	m.queue.AppendOrdered(sid, "", testutil.HostOwner().ID, "queued follow-up", 0, time.Time{})
	progress.TurnStarted(sid)

	m.DisposeSessionResources(t.Context(), sid)

	m.checkpointCapture.RecordPath(t.Context(), checkpoints, sid, "after.go")
	manifest, err := checkpoints.Load(t.Context(), sid, "anchor")
	testutil.FailErr(t, "load released checkpoint", err)
	if len(manifest.Paths) != 0 {
		t.Fatalf("checkpoint capture survived ForgetSession: %+v", manifest.Paths)
	}

	held := map[string]func() bool{
		"progressClosureExpect": func() bool { _, ok := m.progressClosureExpect.Load(sid); return ok },
		"closeout prompt":       func() bool { _, ok := m.closeout.prompts.Load(sid); return ok },
		"closeout cycle":        func() bool { _, ok := m.closeout.cycles.Load(sid); return ok },
		"coordinatorBatchTurn":  func() bool { _, ok := m.coordinatorBatchTurn.Load(sid); return ok },
		"mergeReconcile":        func() bool { _, ok := m.mergeReconcile.Load(sid); return ok },
		"compactionTokenCalibration": func() bool {
			_, ok := m.compactionTokenCalibration.Load(sid)
			return ok
		},
		"promotePathStatus": func() bool { _, ok := m.promotePathStatus.Load(sid); return ok },
		"agentsMDCache":     func() bool { _, ok := m.agentsMDCache.Load(sid); return ok },
	}
	for name, stillThere := range held {
		if stillThere() {
			t.Errorf("%s survived ForgetSession", name)
		}
	}
	if len(forgot) != 1 || forgot[0] != sid {
		t.Fatalf("approval gate release = %v, want [%s]", forgot, sid)
	}
	if len(m.writeRootRuntime.ListChatGrants(sid)) != 0 {
		t.Fatal("write-root runtime survived ForgetSession")
	}

	if got := m.QueueSnapshot(sid); len(got.QueueItems) != 0 || got.Revision != 0 {
		t.Fatalf("queue survived ForgetSession: %+v", got)
	}
	if clock := progress.Clock(sid); clock.Running() {
		t.Fatalf("turn clock survived ForgetSession: %+v", clock)
	}
}

// Stop releases one run; the chat's approvals last until the chat is disposed.
func TestStopKeepsTheChatsApprovedSandboxGrants(t *testing.T) {
	m := newForgetTestManager()
	var released, disposed []string
	testutil.FailErr(t, "register run cleanup", m.RegisterSessionCleanup("approval-run", 50, func(_ context.Context, id string) error {
		released = append(released, id)
		return nil
	}))
	testutil.FailErr(t, "register disposal", m.RegisterSessionDisposal("approvals", 50, func(_ context.Context, id string) error {
		disposed = append(disposed, id)
		return nil
	}))
	const sid = "sess-stop"
	m.writeRootRuntime = approvalstate.NewSandboxPathGrantRuntime()
	m.writeRootRuntime.GrantChat(sid, "/opt/cache", "grant-1", "cp-1", nil)
	m.writeRootRuntime.GrantSessionWriteRoot(sid, "/opt/derived")

	testutil.FailErr(t, "stop", m.releaseSessionRuntime(t.Context(), sid))
	if roots := m.writeRootRuntime.SessionWriteRoots(sid); len(roots) != 1 || roots[0] != "/opt/cache" {
		t.Fatalf("roots after Stop = %v, want only the approved grant", roots)
	}
	if len(released) != 1 || len(disposed) != 0 {
		t.Fatalf("Stop ran release %v and disposal %v, want release only", released, disposed)
	}

	m.DisposeSessionResources(t.Context(), sid)
	if roots := m.writeRootRuntime.SessionWriteRoots(sid); len(roots) != 0 {
		t.Fatalf("roots after delete = %v", roots)
	}
	if len(disposed) != 1 || disposed[0] != sid {
		t.Fatalf("disposal = %v, want [%s]", disposed, sid)
	}
}

func TestForgetJobClearsUnclaimedWakePayloads(t *testing.T) {
	m := newForgetTestManager()
	const job = "job-1"
	m.workerDigests.Store(job, "digest")

	m.ForgetJob(job)

	if _, ok := m.workerDigests.Load(job); ok {
		t.Error("workerDigests survived ForgetJob")
	}
}
