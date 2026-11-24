package policyindex

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providers/anthropic"
	"github.com/lycaon/lycaon/internal/llm/providers/ollama"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/history"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/prompttest"
	"github.com/lycaon/lycaon/pkg/api"
)

type prefixCoordCtx struct{}

func (prefixCoordCtx) BuildCoordinatorTurnFrame(context.Context, string, *api.Session) (inject.CoordinatorTurnFrame, error) {
	return inject.CoordinatorTurnFrame{RunContext: api.CoordinatorRunContext{
		WorkflowID:    "implement",
		CurrentPhase:  "work",
		AllowedAgents: spawn.AmbientAllowedAgents(),
	}}, nil
}

type toggleRoots struct {
	base    Roots
	applies bool
}

func (r *toggleRoots) Applies(ctx context.Context, surface string, sess *api.Session) bool {
	if !r.applies {
		return false
	}
	return r.base.Applies(ctx, surface, sess)
}

func (r *toggleRoots) Roots(ctx context.Context, sess *api.Session) ([]projectroot.RootRef, error) {
	return r.base.Roots(ctx, sess)
}

func prefixModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..")
}

func prefixAssemblyEngine(t *testing.T, m *Service) *assembly.AssemblyEngine {
	t.Helper()
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: prefixModuleRoot(t)})
	m.SetRenderer(pe)
	eng := &assembly.AssemblyEngine{}
	eng.SetDeps(assembly.AssemblyDeps{
		Prompts:          pe,
		Injects:          prompts.NewInjectRenderer(pe),
		Limits:           func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		CoordinatorFrame: prefixCoordCtx{},
		PromptToolLister: prompttest.CoordinatorTools,
		AgentsMDIndex:    m.Index,
		AgentsMDPolicies: func(ctx context.Context, sess *api.Session, history []api.Message) (assembly.AgentsMDPolicyDelivery, error) {
			delivery, err := m.ResolveCurrent(ctx, sess, history)
			return assembly.AgentsMDPolicyDelivery{Messages: delivery.Messages, ActiveRootID: delivery.ActiveRootID}, err
		},
	})
	return eng
}

func projectAnthropicWire(t *testing.T, msgs []api.Message) (string, string) {
	t.Helper()
	proj := providerwire.ProjectPromptCache(modelcall.CompletionRequest{Messages: msgs}, providerprofile.Anthropic().PromptCache, modelinfo.ModelCapabilities{})
	sys, wireMsgs := anthropic.ProjectMessages(msgs, proj.Breakpoints, false, "sess", "anthropic", "claude-sonnet-4-5")
	sysRaw, err := json.Marshal(sys)
	testutil.FailErr(t, "marshal anthropic system", err)
	msgRaw, err := json.Marshal(wireMsgs)
	testutil.FailErr(t, "marshal anthropic messages", err)
	return string(sysRaw), string(msgRaw)
}

func projectOllamaWire(t *testing.T, msgs []api.Message) string {
	t.Helper()
	wireMsgs := ollama.ProjectMessages(msgs, ollama.MessageProjection{SessionID: "sess", ProviderID: "ollama", Model: "qwen3"})
	raw, err := json.Marshal(wireMsgs)
	testutil.FailErr(t, "marshal ollama messages", err)
	return string(raw)
}

func stripAnthropicCacheControl(t *testing.T, raw string) string {
	t.Helper()
	var msgs []struct {
		Role    string `json:"role"`
		Content []struct {
			Type      string `json:"type"`
			Text      string `json:"text,omitempty"`
			ToolUseID string `json:"tool_use_id,omitempty"`
			Content   string `json:"content,omitempty"`
			ID        string `json:"id,omitempty"`
			Name      string `json:"name,omitempty"`
		} `json:"content"`
	}
	testutil.FailErr(t, "unmarshal anthropic wire", json.Unmarshal([]byte(raw), &msgs))
	out, err := json.Marshal(msgs)
	testutil.FailErr(t, "remarshal anthropic wire", err)
	return string(out)
}

func TestCoordinatorRootPolicyPrefixReuseAcrossProviders(t *testing.T) {
	m, sess, roots := policyFixture(t)
	root := roots.roots[0]
	policySentinel := "ROOT_POLICY_SENTINEL: always run ./task."
	writePolicy(t, root.Path, "AGENTS.md", policySentinel)
	eng := prefixAssemblyEngine(t, m)

	u1 := api.Message{
		ID: "u1", Ord: 1, Role: api.MessageRoleUser, Origin: api.MessageOriginUser,
		Authority: api.ContentAuthorityUser, Content: "inspect repo", ContextPinned: true,
	}
	a1 := api.Message{
		ID: "a1", Ord: 2, Role: api.MessageRoleAssistant, Origin: api.MessageOriginModel,
		ToolCalls: []api.ToolCall{{ID: "c1", Name: "read"}},
	}
	t1 := api.Message{
		ID: "t1", Ord: 3, Role: api.MessageRoleTool, Origin: api.MessageOriginTool,
		Content:    "file1",
		ToolResult: &api.ToolResult{ToolCallID: "c1", Tool: "read", Content: "file1", Invocation: &api.InvocationReceipt{ToolCallID: "c1", Tool: "read"}},
	}
	a2 := api.Message{
		ID: "a2", Ord: 4, Role: api.MessageRoleAssistant, Origin: api.MessageOriginModel,
		ToolCalls: []api.ToolCall{{ID: "c2", Name: "read"}},
	}
	t2 := api.Message{
		ID: "t2", Ord: 5, Role: api.MessageRoleTool, Origin: api.MessageOriginTool,
		Content:    "file2",
		ToolResult: &api.ToolResult{ToolCallID: "c2", Tool: "read", Content: "file2", Invocation: &api.InvocationReceipt{ToolCallID: "c2", Tool: "read"}},
	}

	eng.BeginPromptTurn(sess.ID, "")
	call1, err := eng.BuildCompletionMessages(t.Context(), sess, []api.Message{u1, a1, t1}, nil)
	testutil.FailErr(t, "assemble call 1", err)
	call2, err := eng.BuildCompletionMessages(t.Context(), sess, []api.Message{u1, a1, t1, a2, t2}, nil)
	testutil.FailErr(t, "assemble call 2", err)

	// Anthropic: root policy must NOT be promoted into system; it must appear once in user messages before u1,
	// and lie within the identical prefix of both calls.
	sys1, msgs1 := projectAnthropicWire(t, call1)
	sys2, msgs2 := projectAnthropicWire(t, call2)
	if sys1 != sys2 {
		t.Fatalf("anthropic system preamble changed across tool exchanges")
	}
	if strings.Contains(sys1, policySentinel) {
		t.Fatalf("anthropic system preamble must not promote repository policy: %s", sys1)
	}
	if strings.Count(msgs1, policySentinel) != 1 || strings.Count(msgs2, policySentinel) != 1 {
		t.Fatalf("anthropic policy occurrence count != 1: c1=%d c2=%d", strings.Count(msgs1, policySentinel), strings.Count(msgs2, policySentinel))
	}
	if strings.Index(msgs1, policySentinel) >= strings.Index(msgs1, "inspect repo") {
		t.Fatalf("anthropic policy must precede user history")
	}
	norm1 := stripAnthropicCacheControl(t, msgs1)
	norm2 := stripAnthropicCacheControl(t, msgs2)
	// Trim trailing tail injects after t1 from norm1 to compare the shared prefix through t1.
	t1End := strings.Index(norm1, `"tool_use_id":"c1"`)
	if t1End < 0 {
		t.Fatal("missing t1 in anthropic wire")
	}
	sharedPrefix := norm1[:t1End]
	if !strings.HasPrefix(norm2, sharedPrefix) || !strings.Contains(sharedPrefix, policySentinel) {
		t.Fatalf("anthropic serialized prefix does not cover root policy and first exchange")
	}

	// Ollama (local caller): root policy must be in role "user" before history, once, in identical prefix.
	ol1 := projectOllamaWire(t, call1)
	ol2 := projectOllamaWire(t, call2)
	if strings.Count(ol1, policySentinel) != 1 || strings.Count(ol2, policySentinel) != 1 {
		t.Fatalf("ollama policy occurrence count != 1: c1=%d c2=%d", strings.Count(ol1, policySentinel), strings.Count(ol2, policySentinel))
	}
	// Verify policy is in user role, not system role.
	var olWire1 []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	testutil.FailErr(t, "unmarshal ollama", json.Unmarshal([]byte(ol1), &olWire1))
	policyRole := ""
	for _, m := range olWire1 {
		if strings.Contains(m.Content, policySentinel) {
			policyRole = m.Role
			break
		}
	}
	if policyRole != "user" {
		t.Fatalf("ollama policy wire role = %q, want user", policyRole)
	}
	olT1Idx := strings.Index(ol1, "file1")
	if olT1Idx < 0 || !strings.HasPrefix(ol2, ol1[:olT1Idx]) || !strings.Contains(ol1[:olT1Idx], policySentinel) {
		t.Fatalf("ollama serialized prefix does not cover root policy and first exchange: idx=%d", olT1Idx)
	}
}

func TestCoordinatorPolicyLifecycleTransitions(t *testing.T) {
	m, sess, baseRoots := policyFixture(t)
	tr := &toggleRoots{base: baseRoots, applies: true}
	m.roots = tr
	root := baseRoots.roots[0]
	other := baseRoots.roots[1]
	eng := prefixAssemblyEngine(t, m)

	writePolicy(t, root.Path, "AGENTS.md", "ROOT_V1")
	writePolicy(t, root.Path, "sub/AGENTS.md", "NESTED_V1")
	writePolicy(t, root.Path, ".paintedwolf/AGENTS.md", "OVERLAY_V1")

	intent := api.Message{
		ID: "u1", Ord: 1, Role: api.MessageRoleUser, Origin: api.MessageOriginUser,
		Authority: api.ContentAuthorityUser, Content: "work in sub", ContextPinned: true,
	}
	subRead := api.Message{
		ID: "t-sub", Ord: 2, Role: api.MessageRoleTool, Origin: api.MessageOriginTool,
		SourceContext: &api.SourceContext{Locations: []api.NavigationTarget{{ProjectID: sess.ProjectID, RootID: root.ID, Path: "sub/file.go", EntryKind: api.NavigationEntryKindFile}}},
		ToolResult:    &api.ToolResult{Tool: "read", ToolCallID: "c-sub", Invocation: &api.InvocationReceipt{Tool: "read", ToolCallID: "c-sub"}},
	}

	// 1. Nested scope preserves ROOT < NESTED < OVERLAY order.
	eng.BeginPromptTurn(sess.ID, "")
	nestedMsgs, err := eng.BuildCompletionMessages(t.Context(), sess, []api.Message{intent, subRead}, nil)
	testutil.FailErr(t, "assemble nested", err)
	_, nestedWire := projectAnthropicWire(t, nestedMsgs)
	iRoot := strings.Index(nestedWire, "ROOT_V1")
	iNested := strings.Index(nestedWire, "NESTED_V1")
	iOverlay := strings.Index(nestedWire, "OVERLAY_V1")
	if !(0 <= iRoot && iRoot < iNested && iNested < iOverlay) {
		t.Fatalf("nested precedence broken: root=%d nested=%d overlay=%d", iRoot, iNested, iOverlay)
	}

	// 2. Edited policy: old binding rejects mutation; next completion delivers ROOT_V2 and admits.
	binding1, err := m.PrepareBinding(t.Context(), sess, nestedMsgs)
	testutil.FailErr(t, "prepare binding1", err)
	writePolicy(t, root.Path, "AGENTS.md", "ROOT_V2")
	policyReject(t, binding1(nestedMsgs)(t.Context(), []agentpolicy.Target{{RootID: root.ID, Path: "sub/file.go", Mutation: true}}), "POLICY_CONTEXT_CHANGED")

	editedMsgs, err := eng.BuildCompletionMessages(t.Context(), sess, []api.Message{intent, subRead}, nil)
	testutil.FailErr(t, "assemble edited", err)
	_, editedWire := projectAnthropicWire(t, editedMsgs)
	if !strings.Contains(editedWire, "ROOT_V2") || strings.Contains(editedWire, "ROOT_V1") {
		t.Fatalf("edited policy not reflected on next completion")
	}
	binding2, err := m.PrepareBinding(t.Context(), sess, editedMsgs)
	testutil.FailErr(t, "prepare binding2", err)
	testutil.FailErr(t, "admit after retry", binding2(editedMsgs)(t.Context(), []agentpolicy.Target{{RootID: root.ID, Path: "sub/file.go", Mutation: true}}))

	// 3. Different worktrees sharing a path string (sub/AGENTS.md in root vs other).
	writePolicy(t, other.Path, "AGENTS.md", "OTHER_ROOT")
	writePolicy(t, other.Path, "sub/AGENTS.md", "OTHER_NESTED")
	sess.WorkspaceRootID = other.ID
	sess.WorkspacePath = other.Path
	wtRead := api.Message{
		ID: "t-wt", Ord: 3, Role: api.MessageRoleTool, Origin: api.MessageOriginTool,
		SourceContext: &api.SourceContext{Locations: []api.NavigationTarget{{ProjectID: sess.ProjectID, RootID: other.ID, Path: "sub/file.go", EntryKind: api.NavigationEntryKindFile}}},
		ToolResult:    &api.ToolResult{Tool: "read", ToolCallID: "c-wt", Invocation: &api.InvocationReceipt{Tool: "read", ToolCallID: "c-wt"}},
	}
	wtMsgs, err := eng.BuildCompletionMessages(t.Context(), sess, []api.Message{intent, wtRead}, nil)
	testutil.FailErr(t, "assemble switched worktree", err)
	_, wtWire := projectAnthropicWire(t, wtMsgs)
	if !strings.Contains(wtWire, "OTHER_ROOT") || !strings.Contains(wtWire, "OTHER_NESTED") {
		t.Fatalf("worktree switch did not deliver target worktree policy")
	}
	policyReject(t, binding2(editedMsgs)(t.Context(), []agentpolicy.Target{{RootID: other.ID, Path: "sub/file.go", Mutation: true}}), "POLICY_CONTEXT_CHANGED")

	// 4. Disabled trust surface omits policy and allows mutation.
	tr.applies = false
	disabledMsgs, err := eng.BuildCompletionMessages(t.Context(), sess, []api.Message{intent, wtRead}, nil)
	testutil.FailErr(t, "assemble disabled", err)
	disabledSys, disabledWire := projectAnthropicWire(t, disabledMsgs)
	if strings.Contains(disabledSys, "AGENTS.md") || strings.Contains(disabledWire, "OTHER_ROOT") {
		t.Fatalf("disabled surface leaked policy")
	}
	disabledBinding, err := m.PrepareBinding(t.Context(), sess, disabledMsgs)
	testutil.FailErr(t, "bind disabled", err)
	testutil.FailErr(t, "admit disabled", disabledBinding(disabledMsgs)(t.Context(), []agentpolicy.Target{{RootID: other.ID, Path: "sub/file.go", Mutation: true}}))
	tr.applies = true

	// 5. Compaction + small context fitting preserves exact manifest coverage.
	sess.WorkspaceRootID = root.ID
	sess.WorkspacePath = root.Path
	sess.AgentType = orchestration.ProfileCoordinator
	compactedSummary := api.Message{
		ID: "summary", Ord: 1, Role: api.MessageRoleAssistant, Origin: api.MessageOriginHost,
		Content: "Compacted earlier work",
		ContentParts: []api.MessageContentPart{{
			Content: "Compacted earlier work", Origin: api.MessageOriginHost,
			Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierTrusted, Source: "compaction_summary",
		}},
		ContextPinned: true,
	}
	compactedMsgs, err := eng.BuildCompletionMessages(t.Context(), sess, []api.Message{compactedSummary, intent, subRead}, nil)
	testutil.FailErr(t, "assemble compacted", err)
	fitter := &history.Service{}
	fitted, err := fitter.FitRequest(t.Context(), sess, []api.Message{compactedSummary, intent, subRead}, modelcall.CompletionRequest{Messages: compactedMsgs}, &policyFitBudget{window: 64000}, m)
	testutil.FailErr(t, "fit compacted request", err)
	manifest := m.ManifestFor(fitted.Messages)
	if len(manifest.Files) == 0 {
		t.Fatal("expected policy manifest coverage after compaction fit")
	}
	_, fittedWire := projectAnthropicWire(t, fitted.Messages)
	if strings.Index(fittedWire, "ROOT_V2") >= strings.Index(fittedWire, "Compacted earlier work") {
		t.Fatalf("root policy must precede compacted history")
	}
}
