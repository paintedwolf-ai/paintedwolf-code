package session

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Bounded stores are zero-value usable.
func newForgetTestManager(t *testing.T) *Manager {
	m, _ := newTestManager(t)
	return m
}

func TestForgetSessionClearsEveryReleasedStore(t *testing.T) {
	m := newForgetTestManager(t)
	var forgot []string
	testutil.FailErr(t, "register approval cleanup", m.RegisterSessionCleanup("approvals", 50, func(_ context.Context, id string) error {
		forgot = append(forgot, id)
		return nil
	}))

	const sid = "sess-1"
	progressStore := progress.NewMemoryStore()
	progressStore.Set(sid, "- [ ] task")
	m.SetProgressStore(progressStore)
	m.ProgressClosure.Arm(t.Context(), sid, "job-closure")
	m.Runner.Closeouts.RecordGroundingFriction(t.Context(), sid)
	m.Runner.Closeouts.NoteCloseoutGroundingReject(t.Context(), sid, "citation", "offender", "draft", nil)
	m.Batch.AcceptSynthesis(t.Context(), sid)
	checkpoints := sessioncheckpoint.New(t.TempDir(), t.TempDir(), sessionstore.NewMemory())
	testutil.FailErr(t, "open checkpoint capture", m.Captures.Capture.Open(t.Context(), checkpoints, sid, "anchor"))
	m.Promotion.SetMergeReconcilePaths(sid, []string{"a.go"})
	m.Runner.History.ObserveTokens(sid, 150, 100)
	m.Promotion.RecordPromotePathStatus(sid, "job", []api.WorkerPromotePathStatus{{Path: "a.go"}})
	m.writeRootRuntime = approvalstate.NewSandboxPathGrantRuntime()
	m.writeRootRuntime.GrantChat(sid, "/opt/cache", "grant-1", "cp-1", nil)

	m.queue.AppendOrdered(sid, "", testutil.HostOwner().ID, "queued follow-up", 0, time.Time{})
	progress.TurnStarted(sid)

	m.DisposeSessionResources(t.Context(), sid)

	m.Captures.Capture.RecordPath(t.Context(), checkpoints, sid, "after.go")
	manifest, err := checkpoints.Load(t.Context(), sid, "anchor")
	testutil.FailErr(t, "load released checkpoint", err)
	if len(manifest.Paths) != 0 {
		t.Fatalf("checkpoint capture survived ForgetSession: %+v", manifest.Paths)
	}

	held := map[string]func() bool{
		"progressClosureExpect": func() bool { _, ok := m.ProgressClosure.Baseline(sid); return ok },
		"coordinatorBatchTurn":  func() bool { return m.Batch.TurnGuard(sid).SynthesisAcceptedThisTurn },
		"mergeReconcile":        func() bool { return m.Promotion.Allowed(sid, "a.go") },
		"compactionTokenCalibration": func() bool {
			return m.Runner.History.Calibration(sid) != (compaction.PromptTokenCalibration{})
		},
		"promotePathStatus": func() bool { return len(m.Promotion.PromotePathBoardLines(sid)) > 0 },
	}
	for name, stillThere := range held {
		if stillThere() {
			t.Errorf("%s survived ForgetSession", name)
		}
	}
	if m.Runner.Closeouts.CloseoutStallState(t.Context(), sid).Active {
		t.Fatal("closeout cycle survived disposal")
	}
	if len(forgot) != 1 || forgot[0] != sid {
		t.Fatalf("approval gate release = %v, want [%s]", forgot, sid)
	}
	if len(m.writeRootRuntime.ListChatGrants(sid)) != 0 {
		t.Fatal("write-root runtime survived ForgetSession")
	}

	if got := m.Drafts.Snapshot(sid); len(got.QueueItems) != 0 || got.Revision != 0 {
		t.Fatalf("queue survived ForgetSession: %+v", got)
	}
	if clock := progress.Clock(sid); clock.Running() {
		t.Fatalf("turn clock survived ForgetSession: %+v", clock)
	}
}

// Stop releases one run; the chat's approvals last until the chat is disposed.
func TestStopKeepsTheChatsApprovedSandboxGrants(t *testing.T) {
	m := newForgetTestManager(t)
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

	testutil.FailErr(t, "stop", m.Chats.ReleaseRuntime(t.Context(), sid))
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
	m := newForgetTestManager(t)
	const job = "job-1"
	m.Workers.Digests.Put(job, "digest")

	m.Workers.Digests.Forget(job)

	if digest := m.Workers.Digests.Take(job); digest != "" {
		t.Error("workerDigests survived ForgetJob")
	}
}
