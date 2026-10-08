package toolpolicy

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type staticRuleEvaluator struct {
	outcome *rules.RuleOutcome
	err     error
}

func (s staticRuleEvaluator) Evaluate(context.Context, rules.EvalContext) (*rules.RuleOutcome, error) {
	return s.outcome, s.err
}

func TestEngineEvaluateInvokeDeniedUsesMessage(t *testing.T) {
	eng := NewEngine(EngineDeps{
		Rules: staticRuleEvaluator{outcome: &rules.RuleOutcome{
			Allowed: false,
			Code:    "RULE_DENY",
			Message: "not allowed",
		}},
	})
	err := eng.EvaluateInvoke(context.Background(), &api.Session{ID: "s1"}, "write", nil)
	if refusal, ok := guidance.RefusalFromError(err); !ok || refusal.Code() != "RULE_DENY" || tools.AsToolReject(err).Data["reason"] != "not allowed" {
		t.Fatalf("err = %v", err)
	}
}

func TestEngineEvaluateInvokeDeniedUsesCodeWhenMessageEmpty(t *testing.T) {
	eng := NewEngine(EngineDeps{
		Rules: staticRuleEvaluator{outcome: &rules.RuleOutcome{
			Allowed: false,
			Code:    "RULE_DENY",
		}},
	})
	err := eng.EvaluateInvoke(context.Background(), &api.Session{ID: "s1"}, "write", nil)
	if refusal, ok := guidance.RefusalFromError(err); !ok || refusal.Code() != "RULE_DENY" || tools.AsToolReject(err) == nil {
		t.Fatalf("err = %v", err)
	}
}

func TestEngineEvaluateInvokeSpecDenyUsesToolRejectFormatter(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	formatter := guidance.NewToolRejectFormatter(guidance.NewFeedbackDeduper())
	eng := NewEngine(EngineDeps{
		Rules: staticRuleEvaluator{outcome: &rules.RuleOutcome{
			Allowed:           false,
			Code:              "SPEC_POSTURE_DELEGATION_FORBIDDEN",
			RejectCode:        "SPEC_POSTURE_DELEGATION_FORBIDDEN",
			PhaseRequired:     "1",
			PhaseRequiredName: "Research depth (stub plan file)",
			MinRequired:       "complete the plan workflow before delegation tools",
		}},
		RejectFormatter: formatter,
		BlockPlane:      phaseTestBlockPlane(t),
	})
	err := eng.EvaluateInvoke(context.Background(), &api.Session{ID: "s1", Posture: api.SessionPostureSpec}, "delegate_dispatch", nil)
	if err == nil {
		t.Fatal("expected deny")
	}
	if got := err.Error(); got == "" || got[:3] != ">>>" {
		t.Fatalf("expected compact block error, got %q", got)
	}
}

func TestEngineEvaluateInvokeEvaluatorError(t *testing.T) {
	eng := NewEngine(EngineDeps{
		Rules: staticRuleEvaluator{err: errors.New("eval failed")},
	})
	if err := eng.EvaluateInvoke(context.Background(), &api.Session{ID: "s1"}, "write", nil); err == nil {
		t.Fatal("expected evaluator error")
	}
}

func TestNewEngineNilDepsNonNilEngine(t *testing.T) {
	if NewEngine(EngineDeps{}) == nil {
		t.Fatal("expected non-nil engine wrapper")
	}
}

type listInvoker struct {
	metas []tools.ToolMeta
}

func (l listInvoker) Invoke(context.Context, string, map[string]any, tools.ToolContext) (string, error) {
	return "", nil
}

func (l listInvoker) List(context.Context, platform.ToolFilter) ([]tools.ToolMeta, error) {
	return l.metas, nil
}

type filterRecordingInvoker struct {
	filter platform.ToolFilter
}

func (f *filterRecordingInvoker) Invoke(context.Context, string, map[string]any, tools.ToolContext) (string, error) {
	return "", nil
}

func (f *filterRecordingInvoker) List(_ context.Context, filter platform.ToolFilter) ([]tools.ToolMeta, error) {
	f.filter = filter
	return []tools.ToolMeta{{Name: "read"}}, nil
}

func TestListForPromptUsesBoundCoordinatorRootSnapshot(t *testing.T) {
	invoker := &filterRecordingInvoker{}
	eng := NewEngine(EngineDeps{
		ToolInvoker: invoker,
		ProjectRootCount: func(context.Context, *api.Session) int {
			return 1
		},
	})
	ctx := WithCoordinatorTurnFrame(context.Background(), &inject.CoordinatorTurnFrame{ProjectRootCount: 2})
	_ = eng.ListForPrompt(ctx, &api.Session{ID: "s1"}, "coordinator")
	if invoker.filter.ProjectRootCount != 2 {
		t.Fatalf("project root count = %d want snapshot value 2", invoker.filter.ProjectRootCount)
	}
}

func TestListForPromptIncludesDeferredWhenInvokeWouldDeny(t *testing.T) {
	inv := listInvoker{metas: []tools.ToolMeta{
		{Name: "read"},
		{Name: "mcp_coropa_intel_search", Source: tools.ToolSourceMCP, SourceID: "coropa"},
	}}
	eng := NewEngine(EngineDeps{
		ToolInvoker: inv,
		Rules:       denyMCPInvoke{},
	})
	listed := eng.ListForPrompt(context.Background(), &api.Session{ID: "s1"}, "coordinator")
	names := make([]string, 0, len(listed))
	for _, meta := range listed {
		names = append(names, meta.Name)
	}
	if !containsName(names, "mcp_coropa_intel_search") || !containsName(names, "read") {
		t.Fatalf("listed = %v want read and MCP", names)
	}
}

// One profile serves both session shapes, so the session decides which of its
// scoped tools reach the schema. Names resolve against the shipped contracts.
func TestListForPromptFiltersBySessionScope(t *testing.T) {
	inv := listInvoker{metas: []tools.ToolMeta{
		{Name: "read"},             // unscoped
		{Name: "surface_note"},     // addressed_session
		{Name: "complete_leg"},     // worker_child
		{Name: "request_decision"}, // worker_child
	}}
	eng := NewEngine(EngineDeps{ToolInvoker: inv})

	addressed := listedNames(eng.ListForPrompt(context.Background(),
		&api.Session{ID: "s1"}, "explore_readonly"))
	wantAddressed := []string{"read", "surface_note"}
	if !reflect.DeepEqual(addressed, wantAddressed) {
		t.Fatalf("addressed session listed %v want %v", addressed, wantAddressed)
	}

	worker := listedNames(eng.ListForPrompt(context.Background(),
		&api.Session{ID: "s2", ParentSessionID: "s1"}, "explore_readonly"))
	wantWorker := []string{"read", "complete_leg", "request_decision"}
	if !reflect.DeepEqual(worker, wantWorker) {
		t.Fatalf("worker child listed %v want %v", worker, wantWorker)
	}
}

func listedNames(metas []tools.ToolMeta) []string {
	out := make([]string, 0, len(metas))
	for _, meta := range metas {
		out = append(out, meta.Name)
	}
	return out
}

type denyMCPInvoke struct{}

func (denyMCPInvoke) Evaluate(_ context.Context, eval rules.EvalContext) (*rules.RuleOutcome, error) {
	if strings.HasPrefix(eval.ToolName, "mcp_") {
		return &rules.RuleOutcome{Allowed: false, Code: "RULE_DENY"}, nil
	}
	return &rules.RuleOutcome{Allowed: true}, nil
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func TestListForPromptFiltersDeniedTools(t *testing.T) {
	inv := listInvoker{metas: []tools.ToolMeta{{Name: "read"}, {Name: "write"}}}
	eng := NewEngine(EngineDeps{
		ToolInvoker: inv,
		Rules:       toolNameRuleEvaluator{},
	})
	listed := eng.ListForPrompt(context.Background(), &api.Session{ID: "s1"}, "coordinator")
	if len(listed) != 1 || listed[0].Name != "read" {
		t.Fatalf("listed = %+v want read only", listed)
	}
}

func TestListForPromptReadsSessionStateOncePerListing(t *testing.T) {
	inv := listInvoker{metas: []tools.ToolMeta{{Name: "read"}, {Name: "write"}, {Name: "grep"}}}
	rootReads := 0
	eng := NewEngine(EngineDeps{
		ToolInvoker: inv,
		Rules:       toolNameRuleEvaluator{},
		ProjectRootCount: func(context.Context, *api.Session) int {
			rootReads++
			return 1
		},
	})
	listed := listedNames(eng.ListForPrompt(context.Background(), &api.Session{ID: "s1"}, "coordinator"))
	if want := []string{"read", "grep"}; !reflect.DeepEqual(listed, want) {
		t.Fatalf("listed = %v want %v", listed, want)
	}
	// One read for the tool filter and one for the shared rule context.
	if rootReads != 2 {
		t.Fatalf("project root reads = %d want 2 for a three-tool listing", rootReads)
	}
}

type toolNameRuleEvaluator struct{}

func (toolNameRuleEvaluator) Evaluate(_ context.Context, eval rules.EvalContext) (*rules.RuleOutcome, error) {
	if eval.ToolName == "write" {
		return &rules.RuleOutcome{Allowed: false, Code: "RULE_DENY"}, nil
	}
	return &rules.RuleOutcome{Allowed: true}, nil
}

func TestListForPromptNilInvokerReturnsNil(t *testing.T) {
	eng := NewEngine(EngineDeps{})
	if got := eng.ListForPrompt(context.Background(), &api.Session{ID: "s1"}, "coordinator"); got != nil {
		t.Fatalf("got %v want nil", got)
	}
}

func TestListInvokeParityEmptyArgs(t *testing.T) {
	inv := listInvoker{metas: []tools.ToolMeta{{Name: "read"}, {Name: "write"}}}
	eng := NewEngine(EngineDeps{
		ToolInvoker: inv,
		Rules:       toolNameRuleEvaluator{},
	})
	listed := eng.ListForPrompt(context.Background(), &api.Session{ID: "s1"}, "coordinator")
	for _, meta := range listed {
		if err := eng.EvaluateInvoke(context.Background(), &api.Session{ID: "s1"}, meta.Name, nil); err != nil {
			t.Fatalf("listed tool %q failed empty-args invoke: %v", meta.Name, err)
		}
	}
}

func TestListSpecHidesDelegateDispatch(t *testing.T) {
	inv := listInvoker{metas: []tools.ToolMeta{{Name: "delegate_dispatch"}, {Name: "read"}}}
	eng := NewEngine(EngineDeps{
		ToolInvoker: inv,
		Rules: staticRuleEvaluator{outcome: &rules.RuleOutcome{
			Allowed: false,
			Code:    "DENY",
		}},
	})
	listed := eng.ListForPrompt(context.Background(), &api.Session{ID: "s1", Posture: api.SessionPostureSpec}, "coordinator")
	for _, meta := range listed {
		if meta.Name == "delegate_dispatch" {
			t.Fatal("delegate_dispatch should be filtered")
		}
	}
}

func phaseTestBlockPlane(t *testing.T) *tools.BlockPlane {
	t.Helper()
	root := testutil.CheckoutRoot(t)
	testutil.FailErr(t, "install anchors", anchorcatalog.InstallFile(filepath.Join(root, "lycaon/config/packs/painted-wolf/platform/host/anchors/catalog.yaml")))
	loader, err := oar.NewLoader(filepath.Join(root, "schemas"))
	testutil.FailErr(t, "create loader", err)
	rules, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "load policy", err)
	pipeline := oar.NewGuardPipeline(rules, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorToolRejected)
	return &tools.BlockPlane{Pipeline: pipeline, Renderer: oar.NewRenderer(nil, nil)}
}

func TestPromptListingDoesNotFireRejectionRules(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	bp := phaseTestBlockPlane(t)
	const code = "SPEC_POSTURE_STATE_FORBIDDEN"
	rule, _ := bp.Pipeline.Rules().Get(code)
	rule.OnFire = []oar.OnFireAction{oar.OnFireIncrementCounter}
	eng := NewEngine(EngineDeps{
		ToolInvoker: listInvoker{metas: []tools.ToolMeta{{Name: "state_update"}}},
		Rules:       staticRuleEvaluator{outcome: &rules.RuleOutcome{Code: code}},
		BlockPlane:  bp,
	})
	sess := &api.Session{ID: "listing", AgentType: "coordinator", Posture: api.SessionPostureSpec}
	if got := eng.ListForPrompt(t.Context(), sess, "coordinator"); len(got) != 0 {
		t.Fatalf("denied tool offered: %v", got)
	}
	if got := bp.Pipeline.Counters().Report(""); len(got) != 0 {
		t.Fatalf("listing invented an occurrence: %v", got)
	}
	err := eng.EvaluateInvoke(t.Context(), sess, "state_update", nil)
	refusal, ok := guidance.RefusalFromError(err)
	if !ok || refusal.Code() != code || refusal.Copy == nil {
		t.Fatalf("actual refusal lost OAR copy: %v", err)
	}
	if got := bp.Pipeline.Counters().Get("", rule.Qualified(), oar.CounterFire); got != 1 {
		t.Fatalf("actual invocation fired %d times", got)
	}
}

func TestEngineEvaluateInvokeAllowsOverlayRemediationOnFormatInvalid(t *testing.T) {
	eng := NewEngine(EngineDeps{
		Rules: staticRuleEvaluator{err: settingsoverlay.ErrFormatInvalid},
	})
	sess := &api.Session{ID: "s1"}

	for _, path := range []string{
		settingsoverlay.Rel(settingsoverlay.FormatFileName),
		settingsoverlay.Rel(settingsoverlay.BasenameIgnores),
		"sub/project/" + settingsoverlay.Rel(settingsoverlay.FormatFileName),
		"sub/project/" + settingsoverlay.Rel(settingsoverlay.BasenameIgnores),
	} {
		for _, tool := range []string{"write", "edit", "replace_lines"} {
			if err := eng.EvaluateInvoke(context.Background(), sess, tool, map[string]any{"path": path}); err != nil {
				t.Errorf("tool %s on %s should be permitted: %v", tool, path, err)
			}
		}
	}

	if err := eng.EvaluateInvoke(context.Background(), sess, "write", map[string]any{"path": "src/main.go"}); !errors.Is(err, settingsoverlay.ErrFormatInvalid) {
		t.Fatalf("expected ErrFormatInvalid for src/main.go, got %v", err)
	}

	if err := eng.EvaluateInvoke(context.Background(), sess, "read", map[string]any{"path": settingsoverlay.Rel(settingsoverlay.FormatFileName)}); !errors.Is(err, settingsoverlay.ErrFormatInvalid) {
		t.Fatalf("expected ErrFormatInvalid for read, got %v", err)
	}
}
