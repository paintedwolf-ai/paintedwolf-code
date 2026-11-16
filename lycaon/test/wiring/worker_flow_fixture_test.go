package wiring

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workerops"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workerstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// workerFlow drives one coordinator session through the wired tool registry,
// store, lanes and worker executor. Workers follow scripts queued per agent
// type; the coordinator's calls are made by the test.
type workerFlow struct {
	t       *testing.T
	h       *Harness
	scripts *queuedWorkerScripts
	sess    *api.Session
	dir     string
	actor   workerrecords.Actor
	ctx     context.Context
	call    tools.ToolContext
}

func newWorkerFlow(t *testing.T, name string) *workerFlow {
	t.Helper()
	var h *Harness
	scripts := newQueuedWorkerScripts()
	h = BuildForTest(t, WithLLMClient(scripts.mock(&h)), WithoutCoordinatorLoop())
	off := false
	testutil.FailErr(t, "disable scanners", h.Boards.Snapshot.SecurityScanners.PutGlobal(settings.SecurityScannersUserOverlay{Enabled: &off}))
	ctx := h.OwnerCtx(t, t.Context())
	dir := h.ProjectDir(t, name)
	writeTestProjectApprovalsAllowWrite(t, dir)
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, dir)
	testutil.FailErr(t, "create session", err)
	h.SeedProgress(t, ctx, sess.ID)
	return &workerFlow{
		t: t, h: h, scripts: scripts, sess: sess, dir: dir, ctx: ctx,
		actor: workerrecords.Actor{SessionID: sess.ID, Role: workerrecords.RoleCoordinator, PrincipalID: sess.ID},
		call:  h.CoordinatorToolContext(t, ctx, sess, dir),
	}
}

// seed writes a project file before any worker runs.
func (f *workerFlow) seed(rel, content string) {
	f.t.Helper()
	testutil.FailErr(f.t, "seed "+rel, os.WriteFile(filepath.Join(f.dir, rel), []byte(content), 0o600))
}

// primary reads a file of the project as landed, and whether it exists.
func (f *workerFlow) primary(rel string) (string, bool) {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, rel))
	if os.IsNotExist(err) {
		return "", false
	}
	testutil.FailErr(f.t, "read "+rel, err)
	return string(raw), true
}

func (f *workerFlow) wantPrimary(rel, content string) {
	f.t.Helper()
	if got, _ := f.primary(rel); got != content {
		f.t.Fatalf("%s on the project = %q, want %q", rel, got, content)
	}
}

// run makes one coordinator tool call that must succeed.
func (f *workerFlow) run(tool string, args map[string]any) string {
	f.t.Helper()
	f.call.Identity.ToolCallID = uuid.NewString()
	out, err := f.h.ToolRegistry.Run(f.ctx, tool, args, f.call)
	testutil.FailErr(f.t, tool, err)
	return out
}

// refused makes one coordinator tool call that must be rejected with code.
func (f *workerFlow) refused(tool string, args map[string]any, code string) *toolrejection.ToolReject {
	f.t.Helper()
	f.call.Identity.ToolCallID = uuid.NewString()
	_, err := f.h.ToolRegistry.Run(f.ctx, tool, args, f.call)
	rej := toolrejection.AsToolReject(err)
	if rej == nil || rej.Code != code {
		f.t.Fatalf("%s = %v, want a rejection with code %s", tool, err, code)
	}
	return rej
}

// start starts a worker with task.
func (f *workerFlow) start(name, agentType, goal string, writePaths ...string) {
	f.t.Helper()
	args := TaskToolArgs(agentType, goal, writePaths...)
	args["name"] = name
	if _, write := args["scope"]; !write {
		args["scope"] = map[string]any{"mode": "read"}
	}
	f.run("task", args)
}

// drain runs every admitted round to its end.
func (f *workerFlow) drain() {
	f.t.Helper()
	testutil.FailErr(f.t, "drain workers", DrainPendingWorkerJobs(f.ctx, f.h, f.sess.ProjectID, ""))
}

// worker reads a worker's records and derived state by name.
func (f *workerFlow) worker(name string) workerops.Snapshot {
	f.t.Helper()
	workers, err := f.h.Workers.Records.Workers(f.ctx, f.actor)
	testutil.FailErr(f.t, "list workers", err)
	for _, w := range workers {
		if w.Name == name {
			snap, err := f.h.Boards.WorkerReader().Read(f.ctx, f.actor, w)
			testutil.FailErr(f.t, "read worker "+name, err)
			return snap
		}
	}
	f.t.Fatalf("no worker named %s among %d", name, len(workers))
	return workerops.Snapshot{}
}

func (f *workerFlow) workerCount() int {
	f.t.Helper()
	workers, err := f.h.Workers.Records.Workers(f.ctx, f.actor)
	testutil.FailErr(f.t, "list workers", err)
	return len(workers)
}

func (f *workerFlow) wantState(name string, state workerstate.State, reason workerstate.Reason) workerops.Snapshot {
	f.t.Helper()
	snap := f.worker(name)
	if snap.View.State != state || snap.View.Reason != reason {
		f.t.Fatalf("worker %s is %s (%s), want %s (%s)", name, snap.View.State, snap.View.Reason, state, reason)
	}
	return snap
}

// result returns the worker's round of that ordinal, which must have settled
// with a result.
func (f *workerFlow) result(name string, ordinal int) workerrecords.Round {
	f.t.Helper()
	snap := f.worker(name)
	if len(snap.Rounds) < ordinal {
		f.t.Fatalf("worker %s has %d rounds, want round %d", name, len(snap.Rounds), ordinal)
	}
	round := snap.Rounds[ordinal-1]
	if round.SettledAt == "" || round.ResultID == "" {
		f.t.Fatalf("worker %s round %d has no result: %+v", name, ordinal, round)
	}
	return round
}

// disposition is what the coordinator did with a result.
func (f *workerFlow) disposition(name, resultID string) workerrecords.Disposition {
	f.t.Helper()
	return f.worker(name).Dispositions[resultID]
}

func (f *workerFlow) wantChange(name, resultID, state string) {
	f.t.Helper()
	if got := f.disposition(name, resultID).ChangeState; got != state {
		f.t.Fatalf("changes of %s result %s are %q, want %q", name, resultID, got, state)
	}
}

// lane is the worker's current workspace generation.
func (f *workerFlow) lane(name string) workerrecords.PreparationRecord {
	f.t.Helper()
	prep, ok, err := f.h.Workers.Records.LatestPreparation(f.ctx, f.actor, f.worker(name).Worker.ID)
	testutil.FailErr(f.t, "lane of "+name, err)
	if !ok {
		f.t.Fatalf("worker %s has no lane", name)
	}
	return prep
}

// account updates the checklist with the host's reconciliation token, which
// accounts for every worker finish the coordinator has been shown.
func (f *workerFlow) account(item string) {
	f.t.Helper()
	pending := f.h.Sessions.Manager.Coordinator.ProgressClosure.Reconciliation(f.sess.ID)
	if pending == nil {
		f.t.Fatal("no worker finish awaits acknowledgement")
	}
	args := f.h.ProgressUpdateArgs(f.t, f.ctx, f.sess.ID, "## Progress\n- [x] "+item+"\n")
	args["reconciliation_token"] = pending.Token
	f.run("update_progress", args)
}

// writes is a worker command that replaces a file's content. The content may
// not hold a single quote.
func writes(rel, content string) api.ToolCall {
	format := strings.NewReplacer(`\`, `\\`, "%", "%%", "\n", `\n`).Replace(content)
	return api.ToolCall{ID: uuid.NewString(), Name: "command", Args: map[string]any{"command": "printf '" + format + "' > " + rel}}
}

func reads(rel string) api.ToolCall {
	return api.ToolCall{ID: uuid.NewString(), Name: "command", Args: map[string]any{"command": "cat " + rel}}
}

// delivers is a worker's complete_leg call.
func delivers(status, brief string) api.ToolCall {
	return api.ToolCall{ID: uuid.NewString(), Name: "complete_leg", Args: map[string]any{
		"leg_status": status, "brief": brief, "objectives_met": []any{brief},
	}}
}

func asks(question, class string) api.ToolCall {
	return api.ToolCall{ID: uuid.NewString(), Name: "request_decision", Args: map[string]any{"question": question, "blocker_class": class}}
}
