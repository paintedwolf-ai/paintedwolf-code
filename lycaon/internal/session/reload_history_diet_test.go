package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/project"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/workflowfacts"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourcerewind"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestReloadHistoryReassemblesDiet(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.HardCeilingTokens = 50000
	cfg.TargetTokens = 8000
	cfg.PruneProtectTailMessages = 4
	mgr, store := newCompactionManager(t, cfg)
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	var msgs []api.Message
	big := strings.Repeat("tool-output-line\n", 800)
	for i := 0; i < 24; i++ {
		msgs = append(msgs,
			api.Message{ID: fmt.Sprintf("u%d", i), Role: api.MessageRoleUser, Content: "go"},
			api.Message{
				ID:        fmt.Sprintf("a%d", i),
				Role:      api.MessageRoleAssistant,
				Content:   "working",
				ToolCalls: []api.ToolCall{{ID: fmt.Sprintf("tc%d", i), Name: "read", Args: map[string]any{"path": "x.go"}}},
			},
			api.Message{
				ID:      fmt.Sprintf("t%d", i),
				Role:    api.MessageRoleTool,
				Content: big,
				ToolResult: &api.ToolResult{
					ToolCallID: fmt.Sprintf("tc%d", i),
					Tool:       "read",
					Content:    big,
				},
			},
		)
	}
	testutil.FailErr(t, "append messages", store.AppendMessages(ctx, sess.ID, msgs...))

	assembled, err := mgr.Runner.History.Reload(ctx, sess.ID, sess, "")
	testutil.FailErr(t, "reloadAssembledHistory", err)
	if len(assembled) == 0 {
		t.Fatal("expected assembled projection")
	}
	for _, msg := range assembled {
		if msg.Role != api.MessageRoleTool {
			continue
		}
		if msg.Content != big {
			t.Fatalf("tool %s rewritten: %d bytes want %d", msg.ID, len(msg.Content), len(big))
		}
	}
}

func TestReloadHistoryDoesNotWriteStore(t *testing.T) {
	cfg := compaction.DefaultCompactionConfig()
	cfg.Enabled = true
	cfg.HardCeilingTokens = 20000
	cfg.TargetTokens = 4000
	cfg.PruneProtectTailMessages = 2
	mgr, store := newCompactionManager(t, cfg)
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	big := strings.Repeat("canonical-tool-body\n", 600)
	testutil.FailErr(t, "append", store.AppendMessages(ctx, sess.ID,
		api.Message{ID: "u1", Role: api.MessageRoleUser, Content: "go"},
		api.Message{
			ID:        "a1",
			Role:      api.MessageRoleAssistant,
			Content:   "ok",
			ToolCalls: []api.ToolCall{{ID: "tc1", Name: "read", Args: map[string]any{"path": "a.go"}}},
		},
		api.Message{
			ID:      "t1",
			Role:    api.MessageRoleTool,
			Content: big,
			ToolResult: &api.ToolResult{
				ToolCallID: "tc1",
				Tool:       "read",
				Content:    big,
			},
		},
		api.Message{ID: "u2", Role: api.MessageRoleUser, Content: "more"},
		api.Message{
			ID:        "a2",
			Role:      api.MessageRoleAssistant,
			Content:   "ok",
			ToolCalls: []api.ToolCall{{ID: "tc2", Name: "read", Args: map[string]any{"path": "b.go"}}},
		},
		api.Message{
			ID:      "t2",
			Role:    api.MessageRoleTool,
			Content: big,
			ToolResult: &api.ToolResult{
				ToolCallID: "tc2",
				Tool:       "read",
				Content:    big,
			},
		},
		api.Message{ID: "u3", Role: api.MessageRoleUser, Content: "again"},
		api.Message{
			ID:        "a3",
			Role:      api.MessageRoleAssistant,
			Content:   "ok",
			ToolCalls: []api.ToolCall{{ID: "tc3", Name: "read", Args: map[string]any{"path": "c.go"}}},
		},
		api.Message{
			ID:      "t3",
			Role:    api.MessageRoleTool,
			Content: big,
			ToolResult: &api.ToolResult{
				ToolCallID: "tc3",
				Tool:       "read",
				Content:    big,
			},
		},
	))

	before, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages before", err)
	beforeLens := make([]int, len(before))
	for i, m := range before {
		beforeLens[i] = len(m.Content)
	}

	assembled, err := mgr.Runner.History.Reload(ctx, sess.ID, sess, "")
	testutil.FailErr(t, "reloadAssembledHistory", err)
	if len(assembled) == 0 {
		t.Fatal("expected assembled projection")
	}

	after, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages after", err)
	if len(after) != len(before) {
		t.Fatalf("store message count changed: before=%d after=%d", len(before), len(after))
	}
	for i := range after {
		if after[i].Content != before[i].Content {
			t.Fatalf("store message %d content mutated after reload assemble", i)
		}
		if len(after[i].Content) != beforeLens[i] {
			t.Fatalf("store message %d length %d want %d", i, len(after[i].Content), beforeLens[i])
		}
	}
}

func TestReloadHistoryFailsClosedWithoutSession(t *testing.T) {
	mgr, _ := newCompactionManager(t, compaction.DefaultCompactionConfig())
	_, err := mgr.Runner.History.Reload(context.Background(), "sess", nil, "")
	if err == nil {
		t.Fatal("expected error when sess is nil")
	}
}

// verdictPendingView reports a review_loop phase with an open evidence gate.
type verdictPendingView struct {
	stubWorkflowManifest
	pending bool
}

func (v verdictPendingView) ActiveReviewVerdictPending(context.Context, string) bool {
	return v.pending
}

func (v verdictPendingView) ActivePhaseGuardState(context.Context, string) workflowfacts.WorkflowPhaseGuardState {
	return workflowfacts.WorkflowPhaseGuardState{}
}

func rejectVerdictCloseout(t *testing.T, mgr *Host, sess *api.Session, workersIdle bool) (string, bool) {
	t.Helper()
	reject, blocked := mgr.Coordinator.Guards.MissingVerdict(context.Background(), sess, workersIdle, true)
	return reject.Error(), blocked
}

func TestVerdictCloseoutSkipsWhenInvokeGated(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.SetWorkflowDomains(workflowDomainFixture(verdictPendingView{pending: true}))

	if _, block := mgr.Coordinator.Guards.MissingVerdict(
		context.Background(), sess, true, false,
	); block {
		t.Fatal("expected no verdict hold when invokeAllowed=false")
	}
}

func TestCloseoutBlocksOnMissingReviewVerdict(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.SetWorkflowDomains(workflowDomainFixture(verdictPendingView{pending: true}))

	reject, block := rejectVerdictCloseout(t, mgr, sess, true)
	if !block {
		t.Fatal("expected the closeout to be held while the review verdict is unrecorded")
	}
	if !strings.Contains(reject, "REVIEW_VERDICT_MISSING_BEFORE_CLOSEOUT") {
		t.Fatalf("reject should carry the hint code, got %q", reject)
	}
	if !strings.Contains(reject, "submit_verdict") {
		t.Fatalf("reject should steer to submit_verdict, got %q", reject)
	}
}

func TestCloseoutAllowsWhenVerdictRecorded(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.SetWorkflowDomains(workflowDomainFixture(verdictPendingView{pending: false}))

	if _, block := rejectVerdictCloseout(t, mgr, sess, true); block {
		t.Fatal("a satisfied review gate must let the closeout through")
	}
}

func TestVerdictCloseoutSkipsBusyWorkers(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.SetWorkflowDomains(workflowDomainFixture(verdictPendingView{pending: true}))

	if _, block := rejectVerdictCloseout(t, mgr, sess, false); block {
		t.Fatal("the push must not apply while workers are still in flight")
	}
}

func TestVerdictCloseoutHoldsRegardlessOfSurface(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.SetWorkflowDomains(workflowDomainFixture(verdictPendingView{pending: true}))

	if _, block := rejectVerdictCloseout(t, mgr, sess, true); !block {
		t.Fatal("a pending review verdict must hold the closeout on any surface")
	}
}

func TestVerdictCloseoutBoundedPerPrompt(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.SetWorkflowDomains(workflowDomainFixture(verdictPendingView{pending: true}))
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, block := rejectVerdictCloseout(t, mgr, sess, true); !block {
			t.Fatalf("delay %d should still block", i)
		}
	}
	if _, block := rejectVerdictCloseout(t, mgr, sess, true); block {
		t.Fatal("the bound must let the closeout through after the per-prompt budget")
	}

	mgr.Runner.Closeouts.BeginPrompt(ctx, sess, promptinput.Input{Text: "continue"})
	if _, block := rejectVerdictCloseout(t, mgr, sess, true); !block {
		t.Fatal("a fresh prompt should hold the closeout again")
	}
}

// A partial apply panic rolls back within the same request.
func TestRewindApplyPanicRollsBackInPlace(t *testing.T) {
	mgr, sessionID, dir := newCheckpointTestSession(t)
	ctx := checkpointCaller(t, mgr)

	anchor := api.Message{
		ID: "u-two-files", Role: api.MessageRoleUser, Content: "change two files",
		Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser,
		TrustTier: api.ContentTrustTierTrusted,
	}
	testutil.FailErr(t, "append", mgr.Coordinator.Context.Sessions.(Store).AppendMessages(ctx, sessionID, anchor))

	pathA := filepath.Join(dir, "a.txt")
	pathB := filepath.Join(dir, "b.txt")
	testutil.FailErr(t, "seed a", os.WriteFile(pathA, []byte("before-a"), 0o640))
	testutil.FailErr(t, "seed b", os.WriteFile(pathB, []byte("before-b"), 0o640))

	cpStore := sessioncheckpoint.New(mgr.Workspace.DataDir, dir, mgr.Coordinator.Context.Sessions.(Store))
	_, err := cpStore.Open(t.Context(), sessionID, anchor.ID)
	testutil.FailErr(t, "open checkpoint", err)
	testutil.FailErr(t, "capture a", cpStore.CapturePreImage(t.Context(), sessionID, anchor.ID, "a.txt"))
	testutil.FailErr(t, "capture b", cpStore.CapturePreImage(t.Context(), sessionID, anchor.ID, "b.txt"))
	testutil.FailErr(t, "turn writes a", os.WriteFile(pathA, []byte("after-a"), 0o640))
	testutil.FailErr(t, "turn writes b", os.WriteFile(pathB, []byte("after-b"), 0o640))
	recordRewindTestEffect(t, mgr, sessionID, "a.txt", []byte("before-a"), []byte("after-a"), api.SourceChangeOpWrite)
	recordRewindTestEffect(t, mgr, sessionID, "b.txt", []byte("before-b"), []byte("after-b"), api.SourceChangeOpWrite)

	// Apply a.txt before injecting a panic on b.txt.
	originalSource := mgr.Chats.Rewinds.SourceRewinds()
	faultSource := &sourcerewind.Service{Planner: originalSource.Planner, Mutations: &rewindFaultJournal{delegate: originalSource.Mutations, before: func(entryIndex int) {
		if entryIndex == 1 {
			panic("boom: injected rewind apply panic")
		}
	}}}
	mgr.Chats.Rewinds.SetSourceRewinds(faultSource)

	operationID := uuid.NewString()
	_, err = rewindTest(t, mgr, ctx, operationID, sessionID, anchor.ID)
	if err == nil {
		t.Fatal("RewindToPrompt returned no error; want the panic surfaced as a rewind failure, not swallowed")
	}
	if !strings.Contains(err.Error(), "injected rewind apply panic") {
		t.Fatalf("RewindToPrompt err = %v, want it to name the panic", err)
	}

	// Rollback restores the applied path and preserves the untouched path.
	gotA, err := os.ReadFile(pathA)
	testutil.FailErr(t, "read a", err)
	if string(gotA) != "after-a" {
		t.Fatalf("a.txt = %q, want rolled back to after-a (pre-rewind-attempt state)", gotA)
	}
	gotB, err := os.ReadFile(pathB)
	testutil.FailErr(t, "read b", err)
	if string(gotB) != "after-b" {
		t.Fatalf("b.txt = %q, want untouched after-b", gotB)
	}

	msgs, err := mgr.Runner.Transcript.GetMessages(ctx, sessionID)
	testutil.FailErr(t, "get messages", err)
	if _, ok := rewindFixtureHasMessage(msgs, anchor.ID); !ok {
		t.Fatal("a rolled-back rewind must leave the transcript intact")
	}

	op, err := mgr.Coordinator.Context.Sessions.(Store).GetRewindOperation(ctx, operationID)
	testutil.FailErr(t, "get rewind operation", err)
	if op == nil || op.Status != "rolled_back" {
		t.Fatalf("rewind operation status = %+v, want rolled_back", op)
	}

	// Retry in the same process after rollback.
	mgr.Chats.Rewinds.SetSourceRewinds(originalSource)
	result, err := rewindTest(t, mgr, ctx, uuid.NewString(), sessionID, anchor.ID)
	testutil.FailErr(t, "rewind after recovery", err)
	if result == nil {
		t.Fatal("rewind after recovery returned a nil result")
	}
}

type rewindFaultJournal struct {
	delegate sourceeffect.Journal
	before   func(int)
	count    int
}

func (j *rewindFaultJournal) PrepareEffect(ctx context.Context, p sourceeffect.Plan) (sourceeffect.Pending, error) {
	if p.Record.Cause == "session_rewind" {
		n := j.count
		j.count++
		j.before(n)
	}
	return j.delegate.PrepareEffect(ctx, p)
}

func (j *rewindFaultJournal) RemoveEntry(ctx context.Context, r sourceeffect.Removal) (string, error) {
	return j.delegate.RemoveEntry(ctx, r)
}

// RegisterProjectContextForTest wires a project and its trust store.
func RegisterProjectContextForTest(t *testing.T, m *Host, projectDir string) string {
	t.Helper()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	surfaces, err := settings.NewTrustSurfacesStoreAt(
		filepath.Join(t.TempDir(), "trust-surfaces.yaml"))
	testutil.FailErr(t, "trust surfaces store", err)

	reg := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), reg, projectDir)
	testutil.FailErr(t, "CreateWithRoot", err)
	m.SetProjectRegistry(reg)
	m.SetEffectiveCatalogDeps("", extpacks.Active(), surfaces)
	return p.ID
}
