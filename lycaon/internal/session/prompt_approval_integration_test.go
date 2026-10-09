//go:build integration

package session_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/approvaloutcome"
	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// recordRequestedLoad records a request_tools load the way the host does, as
// a turn load receipt, so the next turn's restore offers the tool.
func recordRequestedLoad(t *testing.T, st session.Store, sessionID, tool string) {
	t.Helper()
	_, err := st.PutTurnLoadReceipt(context.Background(), store.TurnLoadReceipt{
		SessionID: sessionID,
		Trigger:   store.TurnLoadTriggerRequest,
		Standing:  `{"tools":[{"tool":"` + tool + `","source":"requested"}]}`,
	})
	testutil.FailErr(t, "record requested load", err)
}

func TestPromptAskWriteApproveRunsTool(t *testing.T) {
	ctx := t.Context()
	projectDir := t.TempDir()
	sqlDB := testdbfixture.Open(t, "prompt-approval.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)

	store := store.NewSQL(sqlDB)
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: "write",
		ToolCalls: []llm.MockToolCall{{
			ID: "call_write", Name: "write", Args: map[string]any{"path": settingsoverlay.Rel("blueprints/a.md"), "content": "x"},
		}},
		FollowUpText: "done",
	}}})

	approvalDir := t.TempDir()
	configtest.Overlay(t, map[config.Rel]string{
		config.SecurityApprovals: "rules:\n  - category: tool\n    pattern: write\n    effect: ask\n",
	})
	approvalStore, err := settings.NewApprovalStoreAt(filepath.Join(approvalDir, "global.yaml"))
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	gate := settings.NewRuleApprovalGate(approvalStore, settings.NoSources())

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	sandboxCfg, err := sandbox.LoadConfig()
	testutil.FailErr(t, "load sandbox config", err)
	boundary := sandbox.NewBoundary(sandboxCfg, profiles)

	reg := tools.NewDefaultRegistry()
	if err := reg.Register("write", func(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
		if err := reviewPromptWrite(ctx, args, tc); err != nil {
			return "", err
		}
		return "write ok", nil
	}); err != nil {
		testutil.FailErr(t, "register approval write fixture", err)
	}
	policy := toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), gate)
	exec := toolexecution.NewExecutor(policy, reg, "implement")
	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	hitlMgr := hitl.NewManager(hitl.NewSQLStore(sqlDB), pub, authzcontext.SQLRecorder(sqlDB))
	hitlMgr.SetApprovalAuthorityInstaller(promptApprovalInstaller{})
	exec.Approvals.SetCheckpointManager(hitlMgr, gate)
	toolReg := tools.NewExecutorRegistry(exec, reg)

	postureRegistry, err := session.LoadPostureRegistry()
	testutil.FailErr(t, "session.LoadPostureRegistry failed", err)
	mgr := session.NewManager(store, mock, toolReg, settings.DefaultSessionLimits())
	oartest.InstallCloseoutPolicy(t, mgr)
	mgr.SetProjectRegistry(project.NewSQLRegistry(sqlDB))
	mgr.SetToolInvoker(exec, exec.Metadata)
	wirePromptApprovalRejectFmt(t, mgr, root)
	mgr.SetPostureRegistry(postureRegistry)
	prog := progress.NewMemoryStore()
	mgr.SetProgressStore(prog)
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	recordRequestedLoad(t, store, sess.ID, "write")
	mgr.SetTurnLoads(turnload.NewLedger())
	// Keep progress terminal so the write runs once.
	prog.Set(sess.ID, "## Progress\n- [x] write blueprint stub\n")

	done := make(chan error, 1)
	go func() {
		_, err := mgr.Prompt(ctx, sess.ID, "please write file")
		done <- err
	}()

	var decisionID string
	testutil.WaitFor(t, 15*time.Second, func() bool {
		pending, err := hitlMgr.ListPending(ctx, sess.ID, nil)
		if err == nil && len(pending) == 1 {
			decisionID = pending[0].ID
			return true
		}
		return false
	})
	if _, err := hitlMgr.ResolveApprovalOption(promptApprovalDecider(ctx, t, sqlDB), sess.ID, decisionID, "approve_current_action"); err != nil {
		testutil.FailErr(t, "hitlMgr.ResolveApprovalOption failed", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("prompt err = %v", err)
	}

	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	found := false
	for _, m := range msgs {
		if m.Role == api.MessageRoleTool && strings.Contains(m.Content, "write ok") {
			found = true
		}
	}
	if !found {
		t.Fatalf("messages = %+v", msgs)
	}
}

// promptApprovalDecider answers as the host owner, since resolutions record who decided.
func promptApprovalDecider(ctx context.Context, t *testing.T, sqlDB *db.Store) context.Context {
	t.Helper()
	return people.WithCaller(ctx, people.Person{ID: testdbseed.OwnerID(t, sqlDB), Role: api.PersonRoleOwner})
}

type promptApprovalInstaller struct{}

func (promptApprovalInstaller) InstallApprovalOption(context.Context, string, hitl.ApprovalOption) (func(), error) {
	return func() {}, nil
}

func TestPromptAskWriteRejectSurfacesApprovalDenied(t *testing.T) {
	ctx := t.Context()
	projectDir := t.TempDir()
	sqlDB := testdbfixture.Open(t, "prompt-approval-reject.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)

	store := store.NewSQL(sqlDB)
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: "write",
		ToolCalls: []llm.MockToolCall{{
			ID: "call_write", Name: "write", Args: map[string]any{"path": settingsoverlay.Rel("blueprints/a.md"), "content": "x"},
		}},
		FollowUpText: "done",
	}}})

	approvalDir := t.TempDir()
	configtest.Overlay(t, map[config.Rel]string{
		config.SecurityApprovals: "rules:\n  - category: tool\n    pattern: write\n    effect: ask\n",
	})
	approvalStore, err := settings.NewApprovalStoreAt(filepath.Join(approvalDir, "global.yaml"))
	testutil.FailErr(t, "settings.NewApprovalStoreAt failed", err)
	gate := settings.NewRuleApprovalGate(approvalStore, settings.NoSources())

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	sandboxCfg, err := sandbox.LoadConfig()
	testutil.FailErr(t, "load sandbox config", err)
	boundary := sandbox.NewBoundary(sandboxCfg, profiles)

	reg := tools.NewDefaultRegistry()
	var mutations atomic.Int32
	if err := reg.Register("write", func(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
		if err := reviewPromptWrite(ctx, args, tc); err != nil {
			return "", err
		}
		mutations.Add(1)
		return "write ok", nil
	}); err != nil {
		testutil.FailErr(t, "register rejection write fixture", err)
	}
	policy := toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), gate)
	exec := toolexecution.NewExecutor(policy, reg, "implement")
	outcomeCfg, err := approvaloutcome.Load()
	testutil.FailErr(t, "load approval-outcome catalog", err)
	outcomes := outcomeRenderer{cat: approvaloutcome.NewCatalog(outcomeCfg)}
	exec.Approvals.SetApprovalOutcomeRenderer(outcomes)
	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	hitlMgr := hitl.NewManager(hitl.NewSQLStore(sqlDB), pub, authzcontext.SQLRecorder(sqlDB))
	exec.Approvals.SetCheckpointManager(hitlMgr, gate)
	toolReg := tools.NewExecutorRegistry(exec, reg)

	postureRegistry, err := session.LoadPostureRegistry()
	testutil.FailErr(t, "session.LoadPostureRegistry failed", err)
	mgr := session.NewManager(store, mock, toolReg, settings.DefaultSessionLimits())
	oartest.InstallCloseoutPolicy(t, mgr)
	mgr.SetProjectRegistry(project.NewSQLRegistry(sqlDB))
	mgr.SetToolInvoker(exec, exec.Metadata)
	wirePromptApprovalRejectFmt(t, mgr, root)
	mgr.SetPostureRegistry(postureRegistry)
	prog := progress.NewMemoryStore()
	mgr.SetProgressStore(prog)
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	recordRequestedLoad(t, store, sess.ID, "write")
	mgr.SetTurnLoads(turnload.NewLedger())
	// Keep progress terminal so the write runs once.
	prog.Set(sess.ID, "## Progress\n- [x] write blueprint stub\n")

	done := make(chan error, 1)
	go func() {
		_, err := mgr.Prompt(ctx, sess.ID, "please write file")
		done <- err
	}()

	var decisionID string
	testutil.WaitFor(t, 15*time.Second, func() bool {
		pending, err := hitlMgr.ListPending(ctx, sess.ID, nil)
		if err == nil && len(pending) == 1 {
			decisionID = pending[0].ID
			return true
		}
		return false
	})
	if _, err := hitlMgr.ResolveCheckpoint(promptApprovalDecider(ctx, t, sqlDB), sess.ID, decisionID, api.CheckpointKindToolApproval, &hitl.DecisionResult{Approved: false}, nil); err != nil {
		testutil.FailErr(t, "hitlMgr.ResolveCheckpoint failed", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("prompt err = %v", err)
	}

	if mutations.Load() != 0 {
		t.Fatal("rejected file change reached mutation")
	}

	denied, err := hitlMgr.SessionApprovalDenied(ctx, sess.ID)
	if err != nil || !denied {
		t.Fatalf("SessionApprovalDenied = %v err=%v", denied, err)
	}

	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	wantDenied := outcomes.ApprovalOutcome(approvaloutcome.CodeApprovalDenied, nil)
	foundDenied := false
	for _, m := range msgs {
		if m.Role == api.MessageRoleTool && m.Content == wantDenied {
			foundDenied = true
		}
		if m.Role == api.MessageRoleTool && strings.Contains(m.Content, "write ok") {
			t.Fatal("rejected file change reported success")
		}
	}
	if !foundDenied {
		t.Fatalf("expected approval denied tool message, got %+v", msgs)
	}
}

func reviewPromptWrite(ctx context.Context, args map[string]any, tc tools.ToolContext) error {
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)
	return tc.ReviewFileChanges(ctx, tools.FileChange{
		Path:    filepath.Join(tc.ActiveRootPath(), path),
		Preview: api.ApprovalFileChange{Path: path, Operation: "create", After: content},
	})
}

type outcomeRenderer struct {
	cat *approvaloutcome.Catalog
}

func (o outcomeRenderer) ApprovalOutcome(code string, ctx map[string]any) string {
	return o.cat.Message(code, ctx)
}

func wirePromptApprovalRejectFmt(t *testing.T, mgr *session.Manager, root string) {
	t.Helper()
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	hintCfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint registry", err)
	mgr.Rejections.SetRejectFormatter(guidance.NewStaticRejectFormatter(hintCfg))
}
