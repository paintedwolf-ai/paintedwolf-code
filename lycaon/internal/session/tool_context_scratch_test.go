package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/scratch"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type scratchFixture struct {
	mgr    *Host
	store  *store.Memory
	root   *api.Session
	worker *api.Session
	dir    func(sessionID string) string
}

func newScratchFixture(t *testing.T) scratchFixture {
	t.Helper()
	ctx := t.Context()
	stateRoot := t.TempDir()
	sessions := store.NewMemory()
	mgr := NewHost(sessions, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	mgr.SetScratchFolders(scratch.New(stateRoot))
	root, err := sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create root session", err)
	worker, err := sessions.CreateChild(ctx, root, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create worker session", err)
	canonicalRoot := fspath.CanonicalPath(stateRoot)
	return scratchFixture{
		mgr: mgr, store: sessions, root: root, worker: worker,
		dir: func(id string) string { return filepath.Join(canonicalRoot, "scratch", id) },
	}
}

func (f scratchFixture) toolContext(t *testing.T, sess *api.Session) tools.ToolContext {
	t.Helper()
	tctx, err := f.mgr.ToolContext.Build(t.Context(), sess, toolprofiles.DefaultToolProfileID, inject.Machine{})
	testutil.FailErr(t, "build tool context", err)
	return tctx
}

func TestEverySessionGetsItsOwnScratchFolder(t *testing.T) {
	f := newScratchFixture(t)

	rootCtx := f.toolContext(t, f.root)
	workerCtx := f.toolContext(t, f.worker)
	if rootCtx.Host.SessionScratchDir != f.dir(f.root.ID) {
		t.Fatalf("coordinator scratch = %q, want %q", rootCtx.Host.SessionScratchDir, f.dir(f.root.ID))
	}
	if workerCtx.Host.SessionScratchDir != f.dir(f.worker.ID) {
		t.Fatalf("worker scratch = %q, want its own sibling folder %q", workerCtx.Host.SessionScratchDir, f.dir(f.worker.ID))
	}
	for _, dir := range []string{rootCtx.Host.SessionScratchDir, workerCtx.Host.SessionScratchDir} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("scratch folder %q was not prepared: %v", dir, err)
		}
	}
}

func TestScratchUnavailableLeavesContextWithoutScratch(t *testing.T) {
	f := newScratchFixture(t)
	scratchRoot := filepath.Dir(f.dir(f.root.ID))
	testutil.FailErr(t, "plant link", os.Symlink(t.TempDir(), scratchRoot))

	if dir := f.toolContext(t, f.root).Host.SessionScratchDir; dir != "" {
		t.Fatalf("scratch prepared through a linked root: %q", dir)
	}
}

func TestDeletingAChatRemovesScratchForItsWholeTree(t *testing.T) {
	f := newScratchFixture(t)
	f.toolContext(t, f.root)
	f.toolContext(t, f.worker)
	other, err := f.store.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create unrelated session", err)
	f.toolContext(t, other)

	testutil.FailErr(t, "delete chat", f.mgr.Chats.Delete(t.Context(), f.root.ID))
	for _, id := range []string{f.root.ID, f.worker.ID} {
		if _, err := os.Stat(f.dir(id)); !os.IsNotExist(err) {
			t.Fatalf("scratch for %q survived its chat's deletion: %v", id, err)
		}
	}
	if _, err := os.Stat(f.dir(other.ID)); err != nil {
		t.Fatalf("unrelated chat's scratch was removed: %v", err)
	}
}

func TestReclaimScratchKeepsChatsWithATurnInFlight(t *testing.T) {
	f := newScratchFixture(t)
	f.toolContext(t, f.root)
	f.toolContext(t, f.worker)
	busyFile := filepath.Join(f.dir(f.worker.ID), "draft.md")
	testutil.FailErr(t, "write worker scratch", os.WriteFile(busyFile, []byte("x"), 0o600))

	turn := f.mgr.Runner.Execution.Prompt.Acquire(f.worker.ID)
	turn.Lock()
	err := f.mgr.Runner.Execution.ReclaimScratch(t.Context())
	turn.Unlock()
	testutil.FailErr(t, "reclaim scratch", err)

	if _, err := os.Stat(f.dir(f.root.ID)); !os.IsNotExist(err) {
		t.Fatalf("idle chat's scratch survived reclaim: %v", err)
	}
	if _, err := os.Stat(busyFile); err != nil {
		t.Fatalf("scratch of a session mid-turn was removed: %v", err)
	}
}

func userTurnMessage(role api.MessageRole, visibility api.MessageVisibility) api.Message {
	return api.Message{
		ID:         uuid.NewString(),
		Role:       role,
		Content:    "x",
		Origin:     api.MessageOriginUser,
		Authority:  api.ContentAuthorityUser,
		TrustTier:  api.ContentTrustTierTrusted,
		Visibility: visibility,
		CreatedAt:  time.Now().UTC(),
	}
}

// Writes and client turn lenses share the permanent opener ordinal across host continuations.
func TestToolContextUserTurnTracksSessionCurrentTurn(t *testing.T) {
	ctx := t.Context()
	sessions := store.NewMemory()
	mgr := NewHost(sessions, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)

	sess, err := sessions.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	assertTurn := func(step string, want int) {
		t.Helper()
		tctx, err := mgr.ToolContext.Build(ctx, sess, toolprofiles.DefaultToolProfileID, inject.Machine{})
		if err != nil {
			t.Fatalf("%s: buildToolContext: %v", step, err)
		}
		if tctx.Identity.UserTurn != want {
			t.Fatalf("%s: ToolContext.UserTurn = %d, want %d", step, tctx.Identity.UserTurn, want)
		}
		if tctx.Source.SourceWorkspaceKind != api.SourceWorkspaceKindProject {
			t.Fatalf("%s: workspace kind = %q, want project", step, tctx.Source.SourceWorkspaceKind)
		}
		if tctx.Host.MaxToolSpillBytes != mgr.Limits.Effective(ctx, sess).MaxToolSpillBytes {
			t.Fatal("tool recovery reads must use the session's spill retention bound")
		}
		hydrated, err := sessions.Get(ctx, sess.ID)
		if err != nil {
			t.Fatalf("%s: get session: %v", step, err)
		}
		if hydrated.CurrentTurn != tctx.Identity.UserTurn {
			t.Fatalf("%s: Session.current_turn = %d, ToolContext.UserTurn = %d",
				step, hydrated.CurrentTurn, tctx.Identity.UserTurn)
		}
	}

	assertTurn("before any prompt", 0)

	if err := sessions.AppendMessages(ctx, sess.ID,
		userTurnMessage(api.MessageRoleUser, api.MessageVisibilityTranscript)); err != nil {
		t.Fatalf("append first user turn: %v", err)
	}
	assertTurn("first user turn", 1)

	// A host kick and the assistant's own reply are not new turns.
	if err := sessions.AppendMessages(ctx, sess.ID,
		userTurnMessage(api.MessageRoleUser, api.MessageVisibilityInternal),
		userTurnMessage(api.MessageRoleAssistant, api.MessageVisibilityTranscript)); err != nil {
		t.Fatalf("append host rows: %v", err)
	}
	assertTurn("after a host kick", 1)

	if err := sessions.AppendMessages(ctx, sess.ID,
		userTurnMessage(api.MessageRoleUser, api.MessageVisibilityTranscript)); err != nil {
		t.Fatalf("append second user turn: %v", err)
	}
	assertTurn("second user turn", 2)
}

func TestFilterTouchPathsForProjectDropsMissingPrefix(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "internal", "auth"), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	got := FilterTouchPathsForProject(dir, []string{"internal/**", "lycaon/**", "lycaon-den/**"})
	if len(got) != 1 || got[0] != "internal/**" {
		t.Fatalf("got %v", got)
	}
}

func TestFilterTouchPathsForProjectKeepsRepoWide(t *testing.T) {
	dir := t.TempDir()
	got := FilterTouchPathsForProject(dir, []string{"**"})
	if len(got) != 1 || got[0] != "**" {
		t.Fatalf("got %v", got)
	}
}

func TestTouchPathAppliesToProjectGlob(t *testing.T) {
	dir := t.TempDir()
	py := filepath.Join(dir, "main.py")
	if err := os.WriteFile(py, []byte("x"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	if !TouchPathAppliesToProject(dir, "**/*.py") {
		t.Fatal("expected **/*.py to match")
	}
	if TouchPathAppliesToProject(dir, "lycaon/**") {
		t.Fatal("expected lycaon/** to miss")
	}
}

type turnClockRecorder struct {
	events []api.TurnClock
}

func (r *turnClockRecorder) ObserveTurnClock(event api.TurnClock) {
	r.events = append(r.events, event)
}

func TestPromptStartsWithFreshTurnClock(t *testing.T) {
	mgr, sessions := newTestManager(t)
	root, err := sessions.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create root", err)
	t.Cleanup(func() { progress.ForgetClock(root.ID) })
	observer := &turnClockRecorder{}
	mgr.SetEventPublisher(&events.Publisher{TurnClockObserver: observer})

	for range 2 {
		progress.TurnStarted(root.ID)
		time.Sleep(20 * time.Millisecond)
		progress.TurnFinished(root.ID)
		observer.events = nil
		_, err = mgr.Submissions.Prompt(t.Context(), root.ID, "hello")
		testutil.FailErr(t, "run user prompt", err)
		if len(observer.events) != 3 {
			t.Fatalf("clock edges = %+v, want start, anchor, and finish", observer.events)
		}
		if start := observer.events[0]; !start.Running || start.ActiveMs != 0 || start.OpeningMessageID != "" {
			t.Fatalf("new prompt clock = %+v, want running from zero with no opening yet", start)
		}
		anchor := observer.events[1]
		if !anchor.Running || anchor.OpeningMessageID == "" {
			t.Fatalf("anchored clock = %+v, want running and naming its prompt", anchor)
		}
		opening, err := sessions.GetMessage(t.Context(), root.ID, anchor.OpeningMessageID)
		testutil.FailErr(t, "opening message", err)
		if opening.Role != api.MessageRoleUser || opening.Content != "hello" {
			t.Fatalf("opening message = %+v, want the prompt", opening)
		}
		finish := observer.events[2]
		if finish.Running || finish.SettledAt == "" || finish.OpeningMessageID != anchor.OpeningMessageID {
			t.Fatalf("completed prompt clock = %+v, want paused and settled for the same turn", finish)
		}

		durable, ok, err := sessions.LatestTurnClock(t.Context(), root.ID)
		testutil.FailErr(t, "latest durable clock", err)
		if !ok || durable.OpeningMessageID != anchor.OpeningMessageID || durable.RunningAt != nil || durable.SettledAt == nil {
			t.Fatalf("durable clock = %+v, want the settled turn", durable)
		}
		if durable.ActiveMs != finish.ActiveMs || durable.WorkMs != finish.WorkMs {
			t.Fatalf("durable clock = %+v, published %+v", durable, finish)
		}
	}
}

func TestTurnClockRestoresFromTheStoreAfterRestart(t *testing.T) {
	mgr, sessions := newTestManager(t)
	ctx := t.Context()
	root, err := sessions.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create root", err)
	t.Cleanup(func() { progress.ForgetClock(root.ID) })
	_, err = mgr.Submissions.Prompt(ctx, root.ID, "hello")
	testutil.FailErr(t, "run user prompt", err)
	before := mgr.Runner.Clocks.Read(ctx, root.ID)

	progress.ForgetClock(root.ID)
	after := mgr.Runner.Clocks.Read(ctx, root.ID)
	if after.OpeningMessageID == "" || after.OpeningMessageID != before.OpeningMessageID {
		t.Fatalf("restored clock = %+v, want turn %q", after, before.OpeningMessageID)
	}
	if after.ActiveMs != before.ActiveMs || after.WorkMs != before.WorkMs || after.Running {
		t.Fatalf("restored clock = %+v, before restart %+v", after, before)
	}
	beforeSettled, err := time.Parse(time.RFC3339Nano, before.SettledAt)
	testutil.FailErr(t, "parse settled_at before restart", err)
	afterSettled, err := time.Parse(time.RFC3339Nano, after.SettledAt)
	testutil.FailErr(t, "parse settled_at after restart", err)
	if !afterSettled.Equal(beforeSettled) {
		t.Fatalf("restored settled_at = %s, want %s", afterSettled, beforeSettled)
	}
}

func TestEvaluateVerdictGroundingHostAssemblesMissingCitations(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemory()
	parent, err := st.Create(ctx, api.CreateSessionRequest{}, "project-1")
	testutil.FailErr(t, "create parent", err)
	child, err := st.CreateChild(ctx, parent, api.SpawnChildRequest{
		AgentType: "skeptic", Prompt: "challenge the claims", WorkerJobID: "job-1",
	})
	testutil.FailErr(t, "create child", err)
	testutil.FailErr(t, "record reviewer evidence", st.UpsertEvidenceRecord(ctx, child.ID, evidence.Record{
		Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "auth/session.go",
	}))
	envelope := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
		JobID: "job-1", ChildSessionID: child.ID, AgentType: "skeptic", State: "complete",
		Report: workercompletion.WorkerCompletionReport{LegStatus: "complete", Brief: "Challenged the claims."},
	})
	testutil.FailErr(t, "append worker closeout", st.AppendMessages(ctx, parent.ID,
		api.Message{Role: api.MessageRoleUser, Content: "review this"},
		api.Message{Role: api.MessageRoleTool, WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID: "job-1", LegID: "leg-skeptic", ChildSessionID: child.ID,
			AgentType: "skeptic", Status: api.WorkerSummaryStatusComplete, Envelope: envelope,
			Grounding: &api.CitationGrounding{Traced: true, CitedEvidence: []api.CitationGroundingCitedEvidence{{Handle: "read#1"}}},
		}},
	))

	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetWorkerQueue(jobLister{tasks: []api.WorkerTask{{
		AgentType: "skeptic", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}, ID: "job-1", ParentSessionID: parent.ID, ChildSessionID: child.ID, LegID: "leg-skeptic", CreatedAt: time.Now().UTC(),
	}}})
	eval, err := mgr.Coordinator.Closeout.EvaluateVerdictGrounding(ctx, parent.ID, nil, nil, []string{"skeptic"})
	testutil.FailErr(t, "evaluate verdict grounding", err)
	if eval.Code != "" || eval.Grounding == nil {
		t.Fatalf("evaluation = %+v want accepted grounding", eval)
	}
	if !eval.Grounding.HostAssembled || !eval.Grounding.Traced {
		t.Fatalf("grounding = %+v want traced host assembly", eval.Grounding)
	}
	if eval.Grounding.HintCode != guidance.VerdictCitationsRequiredCode {
		t.Fatalf("hint code = %q", eval.Grounding.HintCode)
	}
	if len(eval.Grounding.CitedEvidence) != 1 || eval.Grounding.CitedEvidence[0].Handle != "leg-skeptic:read#1" {
		t.Fatalf("cited evidence = %+v", eval.Grounding.CitedEvidence)
	}
}

func TestEvaluateVerdictGroundingRejectsInventedCitation(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemory()
	parent, err := st.Create(ctx, api.CreateSessionRequest{}, "project-1")
	testutil.FailErr(t, "create parent", err)
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())

	eval, err := mgr.Coordinator.Closeout.EvaluateVerdictGrounding(ctx, parent.ID,
		[]api.CitationGroundingCitedEvidence{{Handle: "invented:read#99"}}, nil, nil)
	testutil.FailErr(t, "evaluate invented citation", err)
	if eval.Code != guidance.VerdictCitationUngroundedCode || eval.Grounding != nil {
		t.Fatalf("evaluation = %+v want ungrounded rejection", eval)
	}
}
