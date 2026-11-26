package toolexecution

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/capabilityrequest"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestWriteRootPreflightUsesCompiledCapability(t *testing.T) {
	for _, tool := range []string{"command", "terminal_open", "catalog_tool"} {
		for _, enabled := range []bool{false, true} {
			root := t.TempDir()
			calls := 0
			executor := func() *Executor {
				e := NewExecutor(nil, nil, "")
				e.Boundary.writeRootPreflight = func(_ context.Context, gotTool string, _ map[string]any, _ tools.ToolContext, gotRoot string) (bool, bool, string, error) {
					calls++
					if gotTool != tool || gotRoot != root {
						t.Fatalf("preflight = %q, %q; want %q, %q", gotTool, gotRoot, tool, root)
					}
					return true, false, "", nil
				}
				return e
			}()
			tc := tools.ToolContext{}
			if enabled {
				tc.Invocation.Contract.Capabilities = toolcontract.CapabilityWriteRoot
			}
			err := executor.Boundary.preflightWriteRoot(t.Context(), tool, map[string]any{
				"capability_request": map[string]any{"write_root": root},
			}, &tc)
			testutil.FailErr(t, "preflight "+tool, err)
			if (calls == 1) != enabled {
				t.Fatalf("%s capability=%v called preflight %d times", tool, enabled, calls)
			}
		}
	}
}

func TestTerminalWriteRootCannotAuthorizeInstructionChanges(t *testing.T) {
	executor := func() *Executor {
		e := NewExecutor(nil, nil, "")
		e.Boundary.writeRootPreflight = func(context.Context, string, map[string]any, tools.ToolContext, string) (bool, bool, string, error) {
			t.Fatal("instruction changes reached ordinary write-root authorization")
			return true, false, "", nil
		}
		return e
	}()
	project := t.TempDir()
	contract, ok := toolcontract.Lookup("terminal_open")
	if !ok {
		t.Fatal("compiled terminal contract unavailable")
	}
	tc := tools.ToolContext{
		Invocation: tools.Invocation{Contract: contract},
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: project, IsPrimary: true}},
			ActiveRootID: "root"},
	}
	err := executor.Boundary.preflightWriteRoot(t.Context(), "terminal_open", map[string]any{
		"capability_request": map[string]any{"write_root": filepath.Join(project, "AGENTS.md")},
	}, &tc)
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "POLICY_WRITE_REQUIRES_COMMAND" {
		t.Fatalf("terminal policy write = %v, want policy-write refusal", err)
	}
	if len(tc.Files.PolicyWriteGrants) != 0 {
		t.Fatal("refused terminal received policy-write authority")
	}
}

func TestRedirectReviewRefreshesAnExistingNarrowGrant(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "result")
	e := NewExecutor(nil, nil, "")
	e.Boundary.SetSessionWriteRootOverlay(func(context.Context, string, string) []string { return []string{target} })
	e.Boundary.SetWriteRootPreflight(func(context.Context, string, map[string]any, tools.ToolContext, string) (bool, bool, string, error) {
		t.Fatal("redirect asked for a broader root")
		return false, false, "", nil
	})
	tc, err := e.Boundary.ReviewRedirectRoot(t.Context(), tools.ToolContext{}, ">", target, parent)
	testutil.FailErr(t, "refresh grant", err)
	if len(tc.Files.GrantedWriteRoots) != 1 || tc.Files.GrantedWriteRoots[0] != target {
		t.Fatalf("refreshed grants = %v", tc.Files.GrantedWriteRoots)
	}
}

func TestSharedTempPreflightCoalescesWriteRootArrayAndAliasesIntoOnePlan(t *testing.T) {
	tempDir, err := os.MkdirTemp("/tmp", "pwc-preflight-exchange-") //nolint:usetesting // Must reside in shared temp.
	testutil.FailErr(t, "create shared temp fixture", err)
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
	canonTemp := fspath.CanonicalPath(tempDir)
	inputFile := filepath.Join(canonTemp, "incoming.csv")
	testutil.FailErr(t, "write input fixture", os.WriteFile(inputFile, []byte("x\n"), 0o600))
	exportDir := filepath.Join(canonTemp, "export")
	exportChild := filepath.Join(exportDir, "nested")
	aliasDir := filepath.Join(canonTemp, "alias-export")
	testutil.FailErr(t, "mkdir export fixture", os.MkdirAll(exportChild, 0o700))
	testutil.FailErr(t, "symlink export alias", os.Symlink(exportDir, aliasDir))

	var writeCalls, readCalls []string
	e := NewExecutor(nil, nil, "")
	e.Boundary.writeRootPreflight = func(ctx context.Context, _ string, _ map[string]any, _ tools.ToolContext, gotRoot string) (bool, bool, string, error) {
		writeCalls = append(writeCalls, fspath.CanonicalPath(gotRoot))
		action := hitl.ProposedAction{Invocation: hitl.ActionInvocation{Tool: "command"}, Scope: hitl.ActionScope{SessionID: "chat-p"}}
		_, decision := gate.Evaluate(gate.Facts{
			Stage: gate.StagePreSpawn,
			Ran:   gate.ProducerFilePath | gate.ProducerLease,
			File:  &gate.FileTarget{Path: exportDir, Mode: gate.ModeWrite, OutsideRoots: true},
		}, gate.PostureBalanced)
		primary, cited, reasons := hitl.PresentDecision(decision)
		grant := hitl.ApprovalGrant{
			ID: "g_write", Scope: hitl.ApprovalGrantScopeChat,
			Predicate:     hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategoryWriteRoot, Pattern: exportDir},
			ChatSessionID: "chat-p", Title: hitl.TitleAllowForThisChat, Coverage: "creating, changing, and removing files in `" + exportDir + "`",
			ExpiresWhen: hitl.ExpiresWhenChatDeleted, ReaskWhen: hitl.ReaskWhenOutsideFolder,
		}
		plan, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
			Kind: hitl.ApprovalSubjectWriteRootSet, Title: "Allow creating, changing, and removing files in " + exportDir,
			Targets: []hitl.ApprovalTarget{{Kind: "shared_temp", Label: "Create, change, and remove files in " + exportDir, Details: map[string]any{"blocked_path": exportDir, "access": "write", "shared_temp": true}}},
		}, hitl.ApprovalPresentation{Action: "System temp access", Impact: "Write files", Gate: primary, Cited: cited}, reasons, []hitl.ApprovalOption{
			hitl.GrantOption(hitl.ApprovalGrantOffer{
				ID: grant.ID, Rung: hitl.ApprovalRungChat, Scope: hitl.ApprovalGrantScopeChat, Title: grant.Title,
				Coverage: grant.Coverage, ExpiresWhen: grant.ExpiresWhen, ReaskWhen: grant.ReaskWhen, Grant: grant,
				Authority: []hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthorityWriteRootChat, Grant: &grant, ChatSessionID: "chat-p", WriteRoots: []string{exportDir}}},
			}),
		}, hitl.FaceContext{})
		if err != nil {
			return false, false, "", err
		}
		return true, false, "", hitl.PrepareApproval(ctx, hitl.CheckpointRequest{SessionID: "chat-p", ProposedAction: &action, ApprovalPlan: plan, Decision: decision})
	}
	e.Boundary.readPathPreflight = func(ctx context.Context, _ string, _ map[string]any, _ tools.ToolContext, gotPath string) (bool, bool, string, error) {
		readCalls = append(readCalls, fspath.CanonicalPath(gotPath))
		action := hitl.ProposedAction{Invocation: hitl.ActionInvocation{Tool: "command"}, Scope: hitl.ActionScope{SessionID: "chat-p"}}
		_, decision := gate.Evaluate(gate.Facts{
			Stage: gate.StagePreSpawn,
			Ran:   gate.ProducerFilePath | gate.ProducerLease,
			File:  &gate.FileTarget{Path: inputFile, Mode: gate.ModeRead, OutsideRoots: true},
		}, gate.PostureBalanced)
		primary, cited, reasons := hitl.PresentDecision(decision)
		grant := hitl.ApprovalGrant{
			ID: "g_read", Scope: hitl.ApprovalGrantScopeChat,
			Predicate:     hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategoryReadPath, Pattern: inputFile},
			ChatSessionID: "chat-p", Title: hitl.TitleAllowForThisChat, Coverage: "reading `" + inputFile + "`",
			ExpiresWhen: hitl.ExpiresWhenChatDeleted, ReaskWhen: hitl.ReaskWhenDifferentPath,
		}
		plan, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
			Kind: hitl.ApprovalSubjectReadPathSet, Title: "Allow reading " + inputFile,
			Targets: []hitl.ApprovalTarget{{Kind: "read_path", Label: "Read " + inputFile, Details: map[string]any{"blocked_path": inputFile, "access": "read", "shared_temp": true}}},
		}, hitl.ApprovalPresentation{Action: "System temp access", Impact: "Read file", Gate: primary, Cited: cited}, reasons, []hitl.ApprovalOption{
			hitl.GrantOption(hitl.ApprovalGrantOffer{
				ID: grant.ID, Rung: hitl.ApprovalRungChat, Scope: hitl.ApprovalGrantScopeChat, Title: grant.Title,
				Coverage: grant.Coverage, ExpiresWhen: grant.ExpiresWhen, ReaskWhen: grant.ReaskWhen, Grant: grant,
				Authority: []hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthorityReadPathChat, Grant: &grant, ChatSessionID: "chat-p", ReadPaths: []string{inputFile}}},
			}),
		}, hitl.FaceContext{})
		if err != nil {
			return false, false, "", err
		}
		return true, false, "", hitl.PrepareApproval(ctx, hitl.CheckpointRequest{SessionID: "chat-p", ProposedAction: &action, ApprovalPlan: plan, Decision: decision})
	}

	tc := tools.ToolContext{}
	tc.Invocation.Contract.Capabilities = toolcontract.CapabilityWriteRoot | toolcontract.CapabilityReadPath
	args := map[string]any{
		"command": "transform",
		"capability_request": map[string]any{
			"read_path":  []any{inputFile, inputFile},
			"write_root": []any{exportDir, exportChild, aliasDir},
		},
	}
	req, reject := capabilityrequest.ParseCapabilityRequest(args)
	if reject != nil {
		t.Fatalf("ParseCapabilityRequest rejected valid request: %+v", reject)
	}
	reviews, err := e.prepareInvocationCapabilities(hitl.WithApprovalPreparation(t.Context()), "command", args, tc, req)
	testutil.FailErr(t, "prepareInvocationCapabilities", err)
	if !slices.Equal(readCalls, []string{inputFile}) || !slices.Equal(writeCalls, []string{exportDir}) {
		t.Fatalf("coalesced calls: reads=%v writes=%v", readCalls, writeCalls)
	}
	if len(reviews) != 2 {
		t.Fatalf("prepared reviews = %d, want 2", len(reviews))
	}
	composed, _, err := hitl.ComposeCapabilityApprovals(hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{Tool: "command", Args: args},
		Scope:      hitl.ActionScope{SessionID: "chat-p"},
	}, reviews)
	testutil.FailErr(t, "compose capability approvals", err)
	if composed.Subject.Title != "Allow this chat to exchange files in system temp?" || len(composed.Subject.Targets) != 2 {
		t.Fatalf("composed subject = %+v", composed.Subject)
	}
	hasExchangeFolder := false
	for _, opt := range composed.Options {
		if opt.Group == hitl.GroupAlsoAllow && opt.Title == "Read and write "+canonTemp+" for this chat" {
			hasExchangeFolder = true
		}
	}
	if !hasExchangeFolder {
		t.Fatalf("composed options missing Also allow exchange folder: %+v", composed.Options)
	}

	if _, reject := capabilityrequest.ParseCapabilityRequest(map[string]any{"capability_request": map[string]any{"write_root": []any{}}}); reject == nil {
		t.Fatal("empty write_root array was accepted")
	}
	tooMany := make([]any, capabilityrequest.MaxWriteRoots+1)
	for i := range tooMany {
		tooMany[i] = exportDir
	}
	if _, reject := capabilityrequest.ParseCapabilityRequest(map[string]any{"capability_request": map[string]any{"write_root": tooMany}}); reject == nil {
		t.Fatal("oversized write_root array was accepted")
	}
}
