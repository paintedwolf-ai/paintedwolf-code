package llm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func setCutoffRules(t *testing.T, rules []CutoffRule) {
	t.Helper()
	prev := activeModelCutoffRules()
	SetModelCutoffRules(rules)
	t.Cleanup(func() { SetModelCutoffRules(prev) })
}

func TestResolveModelCutoffFirstMatchWins(t *testing.T) {
	setCutoffRules(t, []CutoffRule{
		{Match: []string{"kimi-k2p7"}, Cutoff: "2025-10"},
		{Match: []string{"kimi"}, Cutoff: "2025-04"},
	})
	if got := ResolveModelCutoff("accounts/fireworks/models/KIMI-K2P7-code"); got != "2025-10" {
		t.Fatalf("cutoff = %q want specific rule (case-insensitive)", got)
	}
	if got := ResolveModelCutoff("kimi-k2-instruct"); got != "2025-04" {
		t.Fatalf("cutoff = %q want family catch-all", got)
	}
	if got := ResolveModelCutoff("gpt-oss-120b"); got != "" {
		t.Fatalf("cutoff = %q want empty for unmatched model", got)
	}
}

func TestValidateCutoffRules(t *testing.T) {
	if err := ValidateCutoffRules([]CutoffRule{{Match: []string{"kimi"}, Cutoff: "2025-10"}}); err != nil {
		t.Fatalf("valid rule rejected: %v", err)
	}
	if err := ValidateCutoffRules([]CutoffRule{{Cutoff: "2025-10"}}); err == nil {
		t.Fatal("empty match list accepted")
	}
	if err := ValidateCutoffRules([]CutoffRule{{Match: []string{" "}, Cutoff: "2025-10"}}); err == nil {
		t.Fatal("blank match pattern accepted")
	}
	if err := ValidateCutoffRules([]CutoffRule{{Match: []string{"kimi"}, Cutoff: "October 2025"}}); err == nil {
		t.Fatal("non-YYYY-MM cutoff accepted")
	}
}

func TestTodayLine(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	now := time.Date(2026, 7, 2, 18, 53, 0, 0, time.UTC)
	got, err := guidance.RenderTodayLine(context.Background(), now, "")
	testutil.FailErr(t, "TodayLine", err)
	if got != "Today is Thursday, 2026-07-02." {
		t.Fatalf("TodayLine = %q", got)
	}
	withCutoff, err := guidance.RenderTodayLine(context.Background(), now, "2025-10")
	testutil.FailErr(t, "TodayLine cutoff", err)
	if !strings.HasPrefix(withCutoff, "Today is Thursday, 2026-07-02.") {
		t.Fatalf("TodayLine = %q want date prefix", withCutoff)
	}
	if !strings.Contains(withCutoff, "2025-10") || !strings.Contains(withCutoff, "not errors") {
		t.Fatalf("TodayLine = %q want cutoff framing", withCutoff)
	}
	if strings.Contains(withCutoff, "18:53") {
		t.Fatalf("TodayLine = %q must not carry a clock time", withCutoff)
	}
}

// captureProvider records the last CompletionRequest for prompt assertions.
type captureProvider struct {
	id   string
	last modelcall.CompletionRequest
}

func (c *captureProvider) ID() string { return c.id }

func (c *captureProvider) Complete(_ context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	c.last = req
	return &modelcall.Completion{Content: "ok"}, nil
}

func (c *captureProvider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk, 1)
	c.last = req
	ch <- modelcall.StreamChunk{Content: "ok", Done: true}
	close(ch)
	return ch, nil
}

func (c *captureProvider) Models() []modelcall.ModelInfo { return []modelcall.ModelInfo{{ID: "lite"}} }

func (c *captureProvider) Profile() providerprofile.Profile { return providerprofile.Profile{} }

func TestSummarizeInjectsTodayLine(t *testing.T) {
	setCutoffRules(t, []CutoffRule{{Match: []string{"lite"}, Cutoff: "2025-06"}})
	provider := &captureProvider{id: "lite"}
	r := newTestRegistrySummarizer(t, provider)

	_, err := r.Summarize(context.Background(), "You pick seeds.", "Query: x", 100)
	testutil.FailErr(t, "Summarize", err)
	if len(provider.last.Messages) != 2 {
		t.Fatalf("messages = %+v", provider.last.Messages)
	}
	content := provider.last.Messages[0].Content
	if !strings.Contains(content, "Today is ") {
		t.Fatalf("prompt = %q want TodayLine", content)
	}
	if !strings.Contains(content, "2025-06") {
		t.Fatalf("prompt = %q want resolved cutoff", content)
	}
	if !strings.Contains(content, "You pick seeds.") || provider.last.Messages[1].Content != "Query: x" {
		t.Fatalf("messages = %+v lost original content", provider.last.Messages)
	}
	if !transcript.HasAuthorityNotice(provider.last.Messages[0]) || provider.last.Messages[0].Role != api.MessageRoleSystem ||
		provider.last.Messages[1].Role != api.MessageRoleUser {
		t.Fatalf("messages = %+v want authority-bearing system/user roles", provider.last.Messages)
	}
}

func TestCurateCompleteInjectsTodayLine(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	setCutoffRules(t, nil)
	provider := &captureProvider{id: "lite"}
	r := &RegistrySummarizer{}
	_, err := r.curateComplete(context.Background(), provider, "lite", "SYS", "USER")
	testutil.FailErr(t, "curateComplete", err)
	if len(provider.last.Messages) != 2 {
		t.Fatalf("messages = %+v", provider.last.Messages)
	}
	system := provider.last.Messages[0]
	if system.Role != api.MessageRoleSystem || !strings.Contains(system.Content, "Today is ") || !transcript.HasAuthorityNotice(system) {
		t.Fatalf("system = %+v want authority notice and TodayLine", system)
	}
	if !strings.Contains(system.Content, "SYS") {
		t.Fatalf("system = %q lost original prompt", system.Content)
	}
	rf := provider.last.ResponseFormat
	if rf == nil || rf.Type != modelcall.ResponseFormatJSONSchema || rf.Name != "curate" {
		t.Fatalf("ResponseFormat = %+v want curate json_schema", rf)
	}
}
