package toolexecution_test

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"context"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestInstructionWriteUsesOrdinaryApprovalWithExactDiff(t *testing.T) {
	stageApprovals(t, settings.ApprovalEffectAsk)
	project := t.TempDir()
	target := filepath.Join(project, "AGENTS.md")
	before := "Initial instructions\n"
	testutil.FailErr(t, "seed instructions", os.WriteFile(target, []byte(before), 0o644))
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
	testutil.FailErr(t, "create store", err)
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	registry := tools.NewDefaultRegistry()
	writer := &native.WriteTool{Boundary: boundary}
	var executed tools.ToolContext
	testutil.FailErr(t, "register writer", registry.Register("write", func(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
		executed = tc
		return writer.Run(ctx, args, tc)
	}))
	executor := toolexecution.NewExecutor(toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), approvalGate), registry, "implement")
	tc := tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: project, IsPrimary: true}},
			ActiveRootID:        "root",
			SourceWorkspaceKind: api.SourceWorkspaceKindProject},
		Identity: tools.InvocationIdentity{ProjectID: "project",
			SessionID:  "chat",
			ToolCallID: "first",
			Agent:      "implement"},
	}
	for _, after := range []string{"First approved instructions\n", "Second approved instructions\n"} {
		manager := &asyncHITL{requested: make(chan struct{}, 1)}
		executor.Approvals.SetCheckpointManager(t.Context(), manager, approvalGate)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		t.Cleanup(cancel)
		done := make(chan error, 1)
		go func() {
			_, err := executor.Invoke(ctx, "write", map[string]any{"path": "AGENTS.md", "content": after}, tc)
			done <- err
		}()
		select {
		case <-manager.requested:
		case <-ctx.Done():
			t.Fatal("instruction write did not ask")
		}
		pending, err := os.ReadFile(target)
		testutil.FailErr(t, "read pending instructions", err)
		if string(pending) != before {
			t.Fatal("instructions changed before approval")
		}
		request := manager.request
		if request.Kind != api.CheckpointKindToolApproval || !slices.Contains(request.Decision.Gates(), api.GateAgentPolicyChange) {
			t.Fatalf("wrong approval kind: %+v", request)
		}
		plan, err := hitl.CompileCheckpointApprovalPlan(request)
		testutil.FailErr(t, "compile standard approval", err)
		changes := plan.Presentation.FileChanges
		if len(changes) != 1 || changes[0].Before != before || changes[0].After != after || changes[0].RootID != "root" {
			t.Fatalf("incorrect diff: %+v", changes)
		}
		for _, option := range plan.Options {
			if option.Kind == hitl.ApprovalOptionQuiet {
				t.Fatal("instruction approval offered quiet")
			}
			if option.ID == plan.RecommendedOptionID && option.Rung != hitl.ApprovalRungChat {
				t.Fatalf("instruction approval faces %s, want the task lease", option.Rung)
			}
		}
		manager.approve()
		invokeErr := <-done
		cancel()
		testutil.FailErr(t, "complete approved change", invokeErr)
		current, err := os.ReadFile(target)
		testutil.FailErr(t, "read approved instructions", err)
		if string(current) != after {
			t.Fatalf("approved bytes did not land: %q", current)
		}
		before, tc = after, executed
		tc.Identity.ToolCallID = "next"
	}
}

type contentReviewHITL struct {
	asyncHITL
	requests []hitl.CheckpointRequest
	final    string
}

func (m *contentReviewHITL) RequestCheckpoint(_ context.Context, request hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	m.requests = append(m.requests, request)
	return &hitl.CheckpointResponse{CheckpointID: "content-review", Status: hitl.DecisionStatusPending}, nil
}

func (m *contentReviewHITL) PollCheckpoint(_ context.Context, id string) (*hitl.CheckpointResponse, error) {
	return &hitl.CheckpointResponse{CheckpointID: id, Status: hitl.DecisionStatusApproved,
		ContentResult: &hitl.ContentApplyResolve{Decision: api.ContentApplyApprovePartial, FinalAfter: m.final}}, nil
}

func TestInstructionContentReviewAuthorizesOnlyComposedBytes(t *testing.T) {
	stageApprovals(t, settings.ApprovalEffectAsk)
	root := t.TempDir()
	target := filepath.Join(root, "AGENTS.md")
	testutil.FailErr(t, "seed instructions", os.WriteFile(target, []byte("before\n"), 0o644))
	manager := &contentReviewHITL{final: "human-selected change\n"}
	review, err := settings.NewReviewStoreAt(filepath.Join(t.TempDir(), "review.yaml"))
	testutil.FailErr(t, "create review policy", err)
	testutil.FailErr(t, "require content review", review.PutGlobal(settings.ReviewConfig{ReviewPaths: []settings.ContentReviewRule{{Path: "AGENTS.md"}}}))
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	writer := &native.WriteTool{Boundary: boundary, ContentApply: &toolhost.ContentApplyService{Mgr: manager, Review: review}}
	registry := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register writer", registry.Register("write", writer.Run))
	approvalGate := settings.NewBypassApprovalGate()
	executor := toolexecution.NewExecutor(toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), approvalGate), registry, "implement")
	executor.Approvals.SetCheckpointManager(t.Context(), manager, approvalGate)
	_, err = executor.Invoke(t.Context(), "write", map[string]any{"path": "AGENTS.md", "content": "proposed change\n"}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}},
			ActiveRootID: "root"},
		Identity: tools.InvocationIdentity{SessionID: "chat",
			ToolCallID: "content-write",
			Agent:      "implement"},
	})
	testutil.FailErr(t, "apply reviewed instructions", err)
	if len(manager.requests) != 1 || manager.requests[0].Kind != api.CheckpointKindContentApply {
		t.Fatalf("content review required a duplicate approval: %+v", manager.requests)
	}
	current, err := os.ReadFile(target)
	testutil.FailErr(t, "read approved instructions", err)
	if string(current) != manager.final {
		t.Fatalf("wrote unapproved content: %q", current)
	}
}

func reviewWriteFixture(ctx context.Context, args map[string]any, tc tools.ToolContext) error {
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)
	return tc.ReviewFileChanges(ctx, tools.FileChange{Path: filepath.Join(tc.ActiveRootPath(), path), Preview: api.ApprovalFileChange{Path: path, Operation: "write", After: content}})
}

func TestFileChangeApprovalDeduplicatesPathsAndRetainsIndexPreviews(t *testing.T) {
	stageApprovals(t, settings.ApprovalEffectAsk)
	root := t.TempDir()
	path := filepath.Join(root, "AGENTS.md")
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
	testutil.FailErr(t, "create store", err)
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	registry := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register prepared review", registry.Register("write", func(ctx context.Context, _ map[string]any, tc tools.ToolContext) (string, error) {
		return "reviewed", tc.ReviewFileChanges(ctx,
			tools.FileChange{Path: path, Preview: api.ApprovalFileChange{Path: "AGENTS.md", Operation: "write", Before: "before", After: "after"}},
			tools.FileChange{Path: path, Preview: api.ApprovalFileChange{Path: "AGENTS.md", Target: "index", Operation: "write", Before: "before", After: "after"}},
		)
	}))
	executor := toolexecution.NewExecutor(toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), approvalGate), registry, "implement")
	manager := &asyncHITL{requested: make(chan struct{}, 1)}
	executor.Approvals.SetCheckpointManager(t.Context(), manager, approvalGate)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := executor.Invoke(ctx, "write", map[string]any{"path": "AGENTS.md", "content": "after"}, tools.ToolContext{
			Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}},
				ActiveRootID:        "root",
				SourceWorkspaceKind: api.SourceWorkspaceKindProject},
			Identity: tools.InvocationIdentity{ProjectID: "project",
				SessionID:  "chat",
				ToolCallID: "review",
				Agent:      "implement"},
		})
		done <- err
	}()
	select {
	case <-manager.requested:
	case err := <-done:
		t.Fatalf("prepared review completed without asking: %v", err)
	case <-ctx.Done():
		t.Fatal("prepared review did not ask")
	}
	action := manager.request.ProposedAction
	if len(action.Invocation.Files) != 1 || action.Invocation.Files[0] != path || len(action.Invocation.ResolvedFiles) != 1 || action.Invocation.ResolvedFiles[0] != fspath.CanonicalPath(path) || len(action.Mutations.AgentPolicy) != 1 || len(action.Mutations.FileChanges) != 2 || action.Mutations.FileChanges[1].Target != "index" {
		t.Fatalf("prepared review lost or duplicated evidence: %+v", action)
	}
	manager.approve()
	testutil.FailErr(t, "complete review", <-done)
}

func TestCommandInstructionGrantUsesOneOrdinaryApproval(t *testing.T) {
	stageApprovals(t, settings.ApprovalEffectAsk)
	root := t.TempDir()
	path := filepath.Join(root, "AGENTS.md")
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
	testutil.FailErr(t, "create store", err)
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	reg := tools.NewDefaultRegistry()
	var executed tools.ToolContext
	testutil.FailErr(t, "register command", reg.Register("command", func(_ context.Context, _ map[string]any, tc tools.ToolContext) (string, error) {
		executed = tc
		return "done", nil
	}))
	executor := toolexecution.NewExecutor(toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), approvalGate), reg, "implement")
	manager := &asyncHITL{requested: make(chan struct{}, 2)}
	executor.Approvals.SetCheckpointManager(t.Context(), manager, approvalGate)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := executor.Invoke(ctx, "command", map[string]any{"command": "touch AGENTS.md", "capability_request": map[string]any{"write_root": path}}, tools.ToolContext{
			Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}},
				ActiveRootID: "root"},
			Identity: tools.InvocationIdentity{SessionID: "chat",
				ToolCallID: "policy-command",
				Agent:      "implement"},
		})
		done <- err
	}()
	select {
	case <-manager.requested:
	case <-ctx.Done():
		t.Fatal("policy command did not ask")
	}
	if !slices.Contains(manager.request.Decision.Gates(), api.GateAgentPolicyChange) || manager.request.ProposedAction.Presentation.Command != "touch AGENTS.md" {
		t.Fatalf("incorrect command ask: %+v", manager.request)
	}
	manager.approve()
	testutil.FailErr(t, "execute approved command", <-done)
	if len(manager.requested) != 0 {
		t.Fatal("command required duplicate approvals")
	}
	if len(executed.Files.PolicyWriteGrants) != 1 || executed.Files.PolicyWriteGrants[0].Subtree {
		t.Fatalf("incorrect instruction authority: %+v", executed.Files.PolicyWriteGrants)
	}
	if len(approvalGate.ListGrants("chat")) != 0 {
		t.Fatal("instruction command installed a reusable grant")
	}
}

func TestCoordinatorInvestigateOverlayWriteReachesAgentPolicyApproval(t *testing.T) {
	stageApprovals(t, settings.ApprovalEffectAsk)
	project := t.TempDir()
	overlayDir := filepath.Join(project, settingsoverlay.DirName())
	testutil.FailErr(t, "mkdir overlay", os.MkdirAll(overlayDir, 0o755))
	target := filepath.Join(overlayDir, settingsoverlay.BasenameIgnores)
	before := "version: 1\nfindings: []\n"
	testutil.FailErr(t, "seed ignores", os.WriteFile(target, []byte(before), 0o644))

	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
	testutil.FailErr(t, "create store", err)
	approvalGate := settings.NewRuleApprovalGate(store, settings.NoSources())
	boundary := sandbox.NewBoundary(fixtureSandboxConfig(), fixtureToolProfiles(t))
	scopes, err := sandbox.LoadPathScopes()
	testutil.FailErr(t, "load path scopes", err)
	boundary.SetPathScopes(scopes)
	registry := tools.NewDefaultRegistry()
	writer := &native.WriteTool{Boundary: boundary}
	testutil.FailErr(t, "register writer", registry.Register("write", func(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
		return writer.Run(ctx, args, tc)
	}))
	executor := toolexecution.NewExecutor(toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), approvalGate), registry, "implement")

	after := "version: 1\nfindings:\n  - id: canary-ignore\n    path: test.go\n"
	tc := tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: project, IsPrimary: true}},
			ActiveRootID:        "root",
			SourceWorkspaceKind: api.SourceWorkspaceKindProject},
		Identity: tools.InvocationIdentity{ProjectID: "project",
			SessionID:  "chat",
			ToolCallID: "coord-write-ignore",
			Agent:      "coordinator"},
		Turn: tools.InvocationTurn{TurnSurfaceID: toolcontract.SurfaceImplementInvestigate},
	}

	manager := &asyncHITL{requested: make(chan struct{}, 1)}
	executor.Approvals.SetCheckpointManager(t.Context(), manager, approvalGate)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() {
		_, err := executor.Invoke(ctx, "write", map[string]any{
			"path":    settingsoverlay.Rel(settingsoverlay.BasenameIgnores),
			"content": after,
		}, tc)
		done <- err
	}()
	select {
	case <-manager.requested:
	case err := <-done:
		t.Fatalf("coordinator overlay write completed without asking: %v", err)
	case <-ctx.Done():
		t.Fatal("coordinator overlay write did not ask")
	}
	request := manager.request
	if request.Kind != api.CheckpointKindToolApproval || !slices.Contains(request.Decision.Gates(), api.GateAgentPolicyChange) {
		t.Fatalf("wrong approval kind: %+v", request)
	}
	manager.approve()
	testutil.FailErr(t, "execute approved write", <-done)
	written, err := os.ReadFile(target)
	testutil.FailErr(t, "read written ignores", err)
	if string(written) != after {
		t.Fatalf("content = %q want %q", string(written), after)
	}
	testutil.FailErr(t, "check format after ignores write", settingsoverlay.CheckFormat(project))
}
