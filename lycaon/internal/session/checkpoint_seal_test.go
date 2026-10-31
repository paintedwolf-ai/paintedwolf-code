package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/session/checkpointcontrol"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/sourcerewind"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"gopkg.in/yaml.v3"
)

// checkpointPreImages is the engine's shared source content store, where a
// checkpoint seals each path's pre-turn bytes.
func checkpointPreImages(mgr *Host) *sourceblob.Store {
	return sourceblob.New(filepath.Join(mgr.Workspace.DataDir, enginepaths.SourceContentDirName))
}

func newCheckpointTestSession(t *testing.T) (*Host, string, string) {
	t.Helper()
	database := testdbfixture.Open(t, "session.db")
	dir := t.TempDir()
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, dir)
	st := sessionstore.NewSQL(database)
	mgr := newTestManagerWithStore(t, st)
	mgr.SetProjectRegistry(project.NewSQLRegistry(database))
	ledger := sourceledger.New(database, filepath.Join(mgr.Workspace.DataDir, "source-content"))
	mgr.SetSourceLedger(ledger, tools.SourceHistory{Files: ledger.History, Comparison: ledger.Comparisons, Git: ledger.Git, Authorship: ledger.Walk}, ledger.Commands, ledger.Git, ledger.Checkpoints, ledger.Inventory)
	mutations := projectsource.NewSourceMutationService(database, ledger)
	mgr.ToolContext.SetSourceMutations(mutations)
	mgr.Chats.Rewinds.SetSourceRewinds(&sourcerewind.Service{Planner: ledger.Comparisons, Mutations: mutations})
	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "bind workspace path", st.UpdateSession(ctx, sess.ID, func(s *api.Session) {
		s.WorkspacePath = dir
	}))
	return mgr, sess.ID, dir
}

func visibleUserMessageIDs(t *testing.T, mgr *Host, sessionID string) []string {
	t.Helper()
	msgs, err := mgr.Runner.Transcript.GetMessages(context.Background(), sessionID)
	testutil.FailErr(t, "get messages", err)
	var out []string
	for _, m := range msgs {
		if m.Role == api.MessageRoleUser && m.Visibility != api.MessageVisibilityInternal {
			out = append(out, m.ID)
		}
	}
	return out
}

// The visible user message identifies its pre-turn checkpoint.
func TestPromptOpensCheckpointForTheVisibleUserMessage(t *testing.T) {
	mgr, sessionID, dir := newCheckpointTestSession(t)
	ctx := context.Background()

	_, err := mgr.Submissions.Prompt(ctx, sessionID, "hello")
	testutil.FailErr(t, "prompt", err)

	anchors := visibleUserMessageIDs(t, mgr, sessionID)
	if len(anchors) != 1 {
		t.Fatalf("visible user messages = %d, want 1", len(anchors))
	}
	store := sessioncheckpoint.New(mgr.Workspace.DataDir, dir, mgr.Coordinator.Context.Sessions.(Store))
	man, err := store.Load(t.Context(), sessionID, anchors[0])
	testutil.FailErr(t, "load checkpoint for the anchor", err)
	if man.AnchorMessageID != anchors[0] {
		t.Fatalf("anchor = %q, want %q", man.AnchorMessageID, anchors[0])
	}
	if len(man.Paths) != 0 {
		t.Fatalf("a freshly opened checkpoint holds no paths yet, got %v", man.Paths)
	}
}

func TestPrimaryMutationCapturesPreTurnBytesIntoTheOpenAnchor(t *testing.T) {
	mgr, sessionID, dir := newCheckpointTestSession(t)
	ctx := context.Background()
	testutil.FailErr(t, "seed file", os.WriteFile(filepath.Join(dir, "foo.go"), []byte("before"), 0o644))

	_, err := mgr.Submissions.Prompt(ctx, sessionID, "hello")
	testutil.FailErr(t, "prompt", err)
	anchor := visibleUserMessageIDs(t, mgr, sessionID)[0]

	// The mutation hook runs before the disk write.
	mgr.Chats.Captures.RecordPrimaryMutation(ctx, sessionID, "foo.go")
	testutil.FailErr(t, "simulate the write", os.WriteFile(filepath.Join(dir, "foo.go"), []byte("after"), 0o644))

	store := sessioncheckpoint.New(mgr.Workspace.DataDir, dir, mgr.Coordinator.Context.Sessions.(Store))
	man, err := store.Load(t.Context(), sessionID, anchor)
	testutil.FailErr(t, "load checkpoint", err)
	entry, ok := man.Paths["foo.go"]
	if !ok {
		t.Fatalf("checkpoint missing the mutated path; paths = %v", man.Paths)
	}
	blob, err := checkpointPreImages(mgr).GetSHA(entry.SHA256)
	testutil.FailErr(t, "read blob", err)
	if string(blob) != "before" {
		t.Fatalf("blob = %q, want the pre-turn bytes", blob)
	}
}

// Each prompt retains its own pre-turn bytes.
func TestEachPromptOpensItsOwnAnchorAndOlderAnchorsKeepTheirBytes(t *testing.T) {
	mgr, sessionID, dir := newCheckpointTestSession(t)
	ctx := context.Background()
	path := filepath.Join(dir, "foo.go")
	testutil.FailErr(t, "seed v1", os.WriteFile(path, []byte("v1"), 0o644))

	_, err := mgr.Submissions.Prompt(ctx, sessionID, "first ask")
	testutil.FailErr(t, "first prompt", err)
	mgr.Chats.Captures.RecordPrimaryMutation(ctx, sessionID, "foo.go")
	testutil.FailErr(t, "write v2", os.WriteFile(path, []byte("v2"), 0o644))

	_, err = mgr.Submissions.Prompt(ctx, sessionID, "second ask")
	testutil.FailErr(t, "second prompt", err)
	mgr.Chats.Captures.RecordPrimaryMutation(ctx, sessionID, "foo.go")
	testutil.FailErr(t, "write v3", os.WriteFile(path, []byte("v3"), 0o644))

	anchors := visibleUserMessageIDs(t, mgr, sessionID)
	if len(anchors) != 2 {
		t.Fatalf("anchors = %d, want 2", len(anchors))
	}
	store := sessioncheckpoint.New(mgr.Workspace.DataDir, dir, mgr.Coordinator.Context.Sessions.(Store))
	for i, want := range []string{"v1", "v2"} {
		man, err := store.Load(t.Context(), sessionID, anchors[i])
		testutil.FailErr(t, "load anchor", err)
		entry, ok := man.Paths["foo.go"]
		if !ok {
			t.Fatalf("anchor %d missing foo.go", i)
		}
		blob, err := checkpointPreImages(mgr).GetSHA(entry.SHA256)
		testutil.FailErr(t, "read blob", err)
		if string(blob) != want {
			t.Fatalf("anchor %d blob = %q, want %q", i, blob, want)
		}
	}
}

// Ledger overflow marks the checkpoint incomplete.

func TestUnderCapTurnLeavesTheCheckpointComplete(t *testing.T) {
	mgr, sessionID, dir := newCheckpointTestSession(t)
	ctx := context.Background()
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(dir, "foo.go"), []byte("before"), 0o644))

	_, err := mgr.Submissions.Prompt(ctx, sessionID, "touch one file")
	testutil.FailErr(t, "prompt", err)
	anchor := visibleUserMessageIDs(t, mgr, sessionID)[0]
	mgr.Chats.Captures.RecordPrimaryMutation(ctx, sessionID, "foo.go")

	man, err := sessioncheckpoint.New(mgr.Workspace.DataDir, dir, mgr.Coordinator.Context.Sessions.(Store)).Load(t.Context(), sessionID, anchor)
	testutil.FailErr(t, "load manifest", err)
	if man.Truncated {
		t.Fatal("an ordinary turn must not be reported as truncated")
	}
}

func checkpointCaller(t *testing.T, mgr *Host) context.Context {
	t.Helper()
	owner, err := mgr.Coordinator.Context.Sessions.(Store).HostOwner(t.Context())
	testutil.FailErr(t, "host owner", err)
	return people.WithCaller(t.Context(), owner)
}

func rewindTest(t *testing.T, mgr *Host, ctx context.Context, operationID, sessionID, anchor string) (*checkpointcontrol.RewindResult, error) {
	t.Helper()
	if _, ok := people.Caller(ctx); !ok {
		owner, err := mgr.Coordinator.Context.Sessions.(Store).HostOwner(ctx)
		testutil.FailErr(t, "host owner", err)
		ctx = people.WithCaller(ctx, owner)
	}
	preview, err := mgr.Chats.Rewinds.PreviewRewind(ctx, sessionID, anchor)
	digest := ""
	if err == nil {
		digest = preview.PlanDigest
	}
	return mgr.Chats.Rewinds.RewindToPrompt(ctx, operationID, sessionID, anchor, digest)
}

func recordRewindTestEffect(t *testing.T, mgr *Host, sessionID, path string, before, after []byte, op api.SourceChangeOp) {
	t.Helper()
	ctx := t.Context()
	p, err := rewindFixtureProject(t, mgr, ctx, sessionID)
	testutil.FailErr(t, "resolve project", err)
	turnSource, err := mgr.Coordinator.Context.Sessions.(Store).ReadSourceTurn(ctx, sessionID, 0)
	turn := turnSource.Number
	testutil.FailErr(t, "resolve turn", err)
	testutil.FailErr(t, "record source effect", mgr.ToolContext.SourceLedger.Record(ctx, sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: p.Roots[0].ID, Path: path}, ProjectID: p.ID, Op: op,
		Origin: api.SourceChangeOriginAgent, SessionID: sessionID, Turn: turn, Before: before, After: after}))
}

func rewindFixtureProject(t *testing.T, mgr *Host, ctx context.Context, id string) (*project.Project, error) {
	t.Helper()
	sess, err := mgr.Coordinator.Context.Sessions.(Store).Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return mgr.Coordinator.Tools.Projects.Get(ctx, sess.ProjectID)
}

func rewindFixtureHasMessage(messages []api.Message, id string) (api.Message, bool) {
	for _, message := range messages {
		if message.ID == id {
			return message, true
		}
	}
	return api.Message{}, false
}

func TestPromptExecutionAppliesCloseoutBoundaryBeforeModel(t *testing.T) {
	for _, host := range []bool{false, true} {
		name := "new intent"
		if host {
			name = "host continuation"
		}
		t.Run(name, func(t *testing.T) {
			mgr, sessions := newTestManager(t)
			stopped := &failure.ProviderEmptyCompletionError{ProviderID: "fixture"}
			mgr.Coordinator.Model.LLM = failingTurnClient{failure: stopped}
			sess, err := sessions.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create closeout session", err)
			mgr.Runner.Closeouts.NoteCloseoutGroundingReject(t.Context(), sess.ID, "citation", "offender", "draft", nil)
			want := guidance.RetainedCloseout{}
			if host {
				want = guidance.RetainedCloseout{Attempt: 1}
				_, err = mgr.Submissions.LoopWake(t.Context(), sess.ID)
			} else {
				_, err = mgr.Submissions.Prompt(t.Context(), sess.ID, "New direction")
			}
			if !errors.Is(err, stopped) {
				t.Fatalf("prompt did not reach model: %v", err)
			}
			if got := mgr.Runner.Closeouts.CloseoutStallState(t.Context(), sess.ID); !reflect.DeepEqual(got, want) {
				t.Fatalf("closeout boundary = %+v want %+v", got, want)
			}
		})
	}
}

const (
	openPlanFixture     = "## Progress\n- [x] survey\n- [ ] review"
	terminalPlanFixture = "## Progress\n- [x] survey\n- [x] review"
)

// implementSurfaceUniverse lists every implement-family coordinator surface.
var implementSurfaceUniverse = []string{
	spawn.SurfaceImplementSynthesis,
	spawn.SurfaceImplementRouting,
	surface.SurfaceImplementDispatch,
	surface.SurfaceImplementOverlayPromote,
	surface.SurfaceImplementPark,
	toolcontract.SurfaceImplementInvestigate,
}

// TestCloseoutPushCoversEveryReachableProseSurface covers open-progress closeout routing.
func TestCloseoutPushCoversEveryReachableProseSurface(t *testing.T) {
	empty := surface.ImplementSessionState{}

	// Open progress keeps synthesis unreachable.
	if workeroutcomes.BatchReadyForSynthesis(empty, nil, openPlanFixture, false, false) {
		t.Fatal("an open plan must keep the synthesis surface unreachable (G5 gates entry on AllTerminal)")
	}
	if !workeroutcomes.SynthesisBlockedOnlyByOpenProgress(empty, nil, openPlanFixture, false, false) {
		t.Fatal("an otherwise-ready batch with an open plan must be the blocked-only-by-open-progress wrap-up moment")
	}
	if !workeroutcomes.BatchReadyForSynthesis(empty, nil, terminalPlanFixture, false, false) {
		t.Fatal("a terminal plan with all other gates satisfied must reach the synthesis surface")
	}

	assertUniverseCoversRuntimeImplementSurfaces(t)

	var fired []string
	for _, surfaceID := range implementSurfaceUniverse {
		mgr, sess := newSynthesisDelayManager(t)
		mgr.RewindRuntime.Progress.Set(sess.ID, openPlanFixture)

		_, block := rejectCloseout(t, mgr, sess, surfaceID, true)

		reachableWhileOpen := guard.SurfaceFinishesWithUserProse(surfaceID) &&
			surfaceID != spawn.SurfaceImplementSynthesis
		if block != reachableWhileOpen {
			t.Fatalf("surface %q: closeout push fired=%v, want %v (prose-closeout=%v, synthesis-excluded=%v)",
				surfaceID, block, reachableWhileOpen,
				guard.SurfaceFinishesWithUserProse(surfaceID), surfaceID == spawn.SurfaceImplementSynthesis)
		}
		if block {
			fired = append(fired, surfaceID)
		}
	}

	if len(fired) == 0 {
		t.Fatal("the open-plan closeout push fired on no reachable surface — a wrap-up turn could deliver with the plan still open")
	}
}

// assertUniverseCoversRuntimeImplementSurfaces keeps the fixture aligned with runtime surfaces.
func assertUniverseCoversRuntimeImplementSurfaces(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "host", "coordinator-surfaces.yaml"))
	testutil.FailErr(t, "read coordinator-surfaces.yaml", err)
	var doc map[string]any
	testutil.FailErr(t, "parse coordinator-surfaces.yaml", yaml.Unmarshal(data, &doc))

	known := make(map[string]struct{}, len(implementSurfaceUniverse))
	for _, s := range implementSurfaceUniverse {
		known[s] = struct{}{}
	}
	for key := range doc {
		if !strings.HasPrefix(key, "implement_") {
			continue
		}
		if _, ok := known[key]; !ok {
			t.Fatalf("coordinator-surfaces.yaml defines implement surface %q not in implementSurfaceUniverse — add it so the closeout-push contract covers it", key)
		}
	}
}

type cancelableCompactionSummary struct{ started chan struct{} }

func (s cancelableCompactionSummary) Summarize(ctx context.Context, _, _ string, _ int) (string, error) {
	close(s.started)
	<-ctx.Done()
	return "", ctx.Err()
}

func TestManualCompactionDoesNotHoldSessionStopAdmission(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.KeepRecentMessages = 1
	cfg.ChunkTokenThreshold = 1000000
	cfg.TargetTokens = 100
	mgr, st := newCompactionManager(t, cfg)
	started := make(chan struct{})
	mgr.Runner.History.SetCompactor(compaction.NewSimpleCompactor(cfg, cancelableCompactionSummary{started: started}))
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "append history", st.AppendMessages(t.Context(), sess.ID,
		api.Message{ID: "old", Role: api.MessageRoleAssistant, Content: strings.Repeat("Old observations. ", 2000)},
		api.Message{ID: "current", Role: api.MessageRoleUser, Content: "Continue."}))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := mgr.Runner.History.ForceCompact(ctx, sess.ID); done <- err }()
	select {
	case <-started:
	case <-t.Context().Done():
		t.Fatal("summarizer did not start")
	}
	stopEntered := make(chan *lifecycle.Flight, 1)
	go func() { flight, _ := mgr.Chats.Gate.Begin(sess.ID); stopEntered <- flight }()
	select {
	case flight := <-stopEntered:
		mgr.Runner.History.Runner.CancelSession(sess.ID)
		mgr.Chats.Gate.Finish(sess.ID, flight, nil)
	case <-t.Context().Done():
		cancel()
		flight := <-stopEntered
		mgr.Chats.Gate.Finish(sess.ID, flight, nil)
		t.Fatal("manual compaction blocked stop admission")
	}
	if err := <-done; err != nil {
		t.Fatalf("compaction cancellation: %v", err)
	}
	current, err := st.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "read generation", err)
	if current.CompactionGeneration != 1 {
		t.Fatal("cancellation lost the independent baseline")
	}
}

func TestToolPolicyWithoutOptionalDependencies(t *testing.T) {
	mgr := NewHost(sessionstore.NewMemory(), Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	if mgr.Coordinator.Guards.Policy() == nil {
		t.Fatal("expected policy without optional dependencies")
	}
}
