package llm

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSummaryUsesInvocationPolicyWithoutChangingSharedBinding(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	provider := &captureProvider{id: "selected"}
	registry := newTestRegistry()
	testutil.FailErr(t, "register provider", registry.Register(provider))
	policy := NewInMemoryPolicyStore(ModelPolicy{Lite: ModelRef{ProviderID: "selected", Model: "global"}})
	primary, active, other := t.TempDir(), t.TempDir(), t.TempDir()
	testutil.FailErr(t, "set primary", policy.PutProject(primary, ModelPolicy{Lite: ModelRef{ProviderID: "selected", Model: "primary"}}))
	testutil.FailErr(t, "set active", policy.PutProject(active, ModelPolicy{Lite: ModelRef{ProviderID: "selected", Model: "active"}}))
	testutil.FailErr(t, "set other", policy.PutProject(other, ModelPolicy{Lite: ModelRef{ProviderID: "selected", Model: "other"}}))
	shared := &RegistrySummarizer{Registry: registry, Policy: policy, Purpose: "compaction"}
	for _, tc := range []struct {
		name  string
		roots []string
		scope SettingsScope
		dir   string
		want  string
	}{
		{name: "primary inherited by worker", roots: []string{primary, t.TempDir()}, want: "primary"},
		{name: "active override", roots: []string{primary, active}, want: "active"},
		{name: "other session", roots: []string{other}, want: "other"},
		{name: "unscoped utility", want: "global"},
		{name: "explicit global", roots: []string{primary}, scope: SettingsScopeGlobal, want: "global"},
		{name: "explicit project", roots: []string{other}, scope: SettingsScopeProject, dir: primary, want: "primary"},
		{name: "bound directory", roots: []string{other}, dir: primary, want: "primary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shared.Scope, shared.ProjectDir = tc.scope, tc.dir
			ctx := curationctx.WithSession(t.Context(), curationctx.Session{SettingsRoots: tc.roots})
			_, err := shared.SummarizeRequired(ctx, "Summarize.", "Observed work.", 256)
			testutil.FailErr(t, "summarize", err)
			if provider.last.Model != tc.want {
				t.Fatalf("model = %q, want %q", provider.last.Model, tc.want)
			}
			if shared.Scope != tc.scope || shared.ProjectDir != tc.dir {
				t.Fatal("invocation changed shared scope")
			}
		})
	}
}

func TestCompactionRoutingAndRevisionFollowProjectPolicy(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	body, err := (compaction.MockSummarizer{Text: "Continue the same work."}).Summarize(compaction.WithSummaryFormat(t.Context()), "", "", 0)
	testutil.FailErr(t, "summary fixture", err)
	provider := &captureProvider{id: "selected", response: body}
	registry := newTestRegistry()
	testutil.FailErr(t, "register provider", registry.Register(provider))
	policy := NewInMemoryPolicyStore(ModelPolicy{Lite: ModelRef{ProviderID: "missing-global", Model: "unavailable"}})
	root := t.TempDir()
	testutil.FailErr(t, "set project", policy.PutProject(root, ModelPolicy{Lite: ModelRef{ProviderID: "selected", Model: "first"}}))
	summarizer := &RegistrySummarizer{Registry: registry, Policy: policy, Purpose: "compaction"}
	cfg := testCompactionConfig()
	cfg.ChunkTokenThreshold, cfg.KeepRecentMessages = 1000000, 1
	engine := compaction.NewSimpleCompactor(cfg, summarizer)
	info := compaction.SessionInfo{ID: "session", ProjectDir: root, SettingsRoots: []string{root}}
	first, err := compaction.AttemptRevision(t.Context(), engine, info)
	testutil.FailErr(t, "first revision", err)
	messages := []compaction.ContextMessage{
		{ID: "old-user", Ord: 1, Role: "user", Content: strings.Repeat("inspect source behavior ", 160)},
		{ID: "old-answer", Ord: 2, Role: "assistant", Content: strings.Repeat("observed execution result ", 160)},
		{ID: "current", Ord: 3, Role: "user", Content: "Continue the work."},
	}
	_, report, err := engine.Compact(t.Context(), info, messages, 120)
	testutil.FailErr(t, "compact project session", err)
	if !report.SessionCompacted || provider.last.Model != "first" {
		t.Fatalf("model = %q, report = %+v", provider.last.Model, report)
	}
	testutil.FailErr(t, "change project", policy.PutProject(root, ModelPolicy{Lite: ModelRef{ProviderID: "selected", Model: "second"}}))
	second, err := compaction.AttemptRevision(t.Context(), engine, info)
	testutil.FailErr(t, "second revision", err)
	if first == second {
		t.Fatal("project model change reused attempt revision")
	}
	_, err = summarizer.SummarizeRequired(curationctx.WithSession(context.Background(), curationctx.Session{SettingsRoots: []string{root}}), "Summarize.", "Observed work.", 256)
	testutil.FailErr(t, "summarize changed project", err)
	if provider.last.Model != "second" {
		t.Fatalf("model = %q", provider.last.Model)
	}
}
