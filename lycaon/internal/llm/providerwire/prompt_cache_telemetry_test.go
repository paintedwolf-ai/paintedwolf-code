package providerwire

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type cacheFixture struct {
	tracker promptCacheHitTracker
	req     modelcall.CompletionRequest
	policy  providerprofile.PromptCachePolicy
	usage   modelcall.TokenUsage
	now     time.Time
	failed  bool
}

func newCacheFixture() *cacheFixture {
	return &cacheFixture{
		req:    modelcall.CompletionRequest{Model: "model", Messages: tieredMessages(), Debug: modelcall.RequestDebug{SessionID: "session"}},
		policy: providerprofile.PromptCachePolicy{Mode: providerprofile.PromptCacheAutomaticPrefix, ColdAfter: providerprofile.CacheDuration(10 * time.Minute)},
		usage:  modelcall.TokenUsage{Present: true, PromptTokens: 10000}, now: time.Unix(100, 0),
	}
}

func (f *cacheFixture) note() observability.PromptCacheObservation {
	f.now = f.now.Add(2 * time.Second)
	return f.tracker.note(PromptCacheScope("provider", f.req.Model, f.req.Debug), f.req, f.policy, f.usage, f.now, f.now.Add(time.Second), f.failed)
}

func TestCacheWindowDetectsRegressionAfterHitsAndRecovers(t *testing.T) {
	f := newCacheFixture()
	f.usage.CacheReadInputTokens = 9000
	for range 12 {
		if got := f.note(); got.Alert != "" {
			t.Fatalf("healthy read warned: %+v", got)
		}
	}
	f.usage.CacheReadInputTokens = 0
	var got observability.PromptCacheObservation
	for range promptCacheWindowSize {
		got = f.note()
	}
	if got.Alert != "no_reads_reported" || !got.Warn || got.Window.Requests != promptCacheWindowSize {
		t.Fatalf("old hits hid the regression: %+v", got)
	}
	if got = f.note(); got.Warn {
		t.Fatal("unchanged alert warned again")
	}
	f.usage.CacheReadInputTokens = 9000
	for range promptCacheWindowSize {
		got = f.note()
	}
	if got.Alert != "" || got.Window.ReadFraction != 0.9 {
		t.Fatalf("recovery lost: %+v", got)
	}
	f.usage.CacheReadInputTokens = 0
	for range promptCacheWindowSize {
		got = f.note()
	}
	if !got.Warn {
		t.Fatal("new regression did not warn")
	}
}

func TestCacheWindowSeparatesColdAndUnobservableRequests(t *testing.T) {
	cases := []struct {
		name, reason string
		change       func(*cacheFixture)
	}{
		{"standing", "prefix_changed", func(f *cacheFixture) { f.req.Messages[0].Content += " changed" }},
		{"schema", "prefix_changed", func(f *cacheFixture) { f.req.Tools = []tools.ToolMeta{{Name: "read"}} }},
		{"controls", "prefix_changed", func(f *cacheFixture) { f.req.MaxTokens = 100 }},
		{"compaction", "history_changed", func(f *cacheFixture) { f.req.Messages[1].Content = "summary" }},
		{"idle", "idle", func(f *cacheFixture) { f.now = f.now.Add(11 * time.Minute) }},
		{"missing usage", "usage_unavailable", func(f *cacheFixture) { f.usage = modelcall.TokenUsage{} }},
		{"incomplete", "usage_unavailable", func(f *cacheFixture) { f.usage.Incomplete = true }},
		{"failure", "request_failed", func(f *cacheFixture) { f.failed = true }},
		{"local", "local_kv", func(f *cacheFixture) { f.policy.Mode = providerprofile.PromptCacheLocalKV }},
		{"none", "uncached", func(f *cacheFixture) { f.policy.Mode = providerprofile.PromptCacheNone }},
		{"invalid schema", "identity_unavailable", func(f *cacheFixture) { f.req.Tools = []tools.ToolMeta{{ArgsSchema: map[string]any{"bad": func() {}}}} }},
		{"retry", "multiple_attempts", func(f *cacheFixture) {
			f.req.ControlCapture = &modelcall.RequestControlCapture{}
			for range 2 {
				f.req.ControlCapture.Record([]byte(`{"max_tokens":100}`))
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newCacheFixture()
			for range 3 {
				f.note()
			}
			tc.change(f)
			got := f.note()
			if got.Comparison != tc.reason || got.Window.Requests != 0 || got.Alert != "" {
				t.Fatalf("observation = %+v", got)
			}
		})
	}
}

func TestCacheWindowKeepsAppendOnlyHistoryAndIgnoresTailAndMetadata(t *testing.T) {
	f := newCacheFixture()
	f.note()
	f.req.Messages[3].Content = "new volatile state"
	f.req.Messages[0].ID = "new projection id"
	f.req.Messages[2].PromptCacheBreakpoint = api.PromptCacheTierNone
	f.req.Messages = append(f.req.Messages[:3], api.Message{Role: api.MessageRoleAssistant, Content: "next", PromptCacheBreakpoint: api.PromptCacheTierHistory})
	got := f.note()
	if got.Comparison != "comparable" || got.HistoryChanged {
		t.Fatalf("append invalidated prefix: %+v", got)
	}
}

func TestCacheWindowStartupAndExplicitWrites(t *testing.T) {
	f := newCacheFixture()
	f.policy = explicitPolicy
	f.usage.CacheCreationInputTokens = 9900
	for range promptCacheMinSamples {
		if got := f.note(); got.Warn {
			t.Fatalf("startup warned early: %+v", got)
		}
	}
	got := f.note()
	if !got.Warn || got.Window.WrittenTokens != 29700 {
		t.Fatalf("repeated writes without reads hidden: %+v", got)
	}
	f.usage.CacheReadInputTokens, f.usage.CacheCreationInputTokens = 9900, 0
	if got = f.note(); got.Alert != "" {
		t.Fatalf("read did not recover: %+v", got)
	}
}

func TestCacheWindowSmallRequestsDoNotWarn(t *testing.T) {
	f := newCacheFixture()
	f.usage.PromptTokens = 100
	for range 12 {
		if got := f.note(); got.Alert != "" {
			t.Fatalf("tiny requests warned: %+v", got)
		}
	}
}

func TestCacheWindowReportsLowFractionWithoutClaimingZeroReads(t *testing.T) {
	f := newCacheFixture()
	f.usage.CacheReadInputTokens = 500
	for range promptCacheMinSamples {
		f.note()
	}
	got := f.note()
	if got.Alert != "low_read_fraction" || !got.Warn || got.Window.ReadFraction != 0.05 {
		t.Fatalf("low fraction not distinguished: %+v", got)
	}
	f.usage = modelcall.TokenUsage{}
	got = f.note()
	if got.Alert != "" || got.UsageReported {
		t.Fatalf("missing usage treated as a miss: %+v", got)
	}
	f.usage = modelcall.TokenUsage{PromptTokens: 10000}
	if got = f.note(); got.Comparison != "baseline_unavailable" {
		t.Fatalf("compared across missing observation: %+v", got)
	}
}

func TestCacheObservationContainsNoPromptOrSchemaBodies(t *testing.T) {
	f := newCacheFixture()
	f.req.Messages[0].Content = "private-standing-text"
	f.req.Tools = []tools.ToolMeta{{Name: "private-tool", Description: "private-description"}}
	f.note()
	f.req.Messages[0].Content += " changed"
	f.req.Tools[0].Description += " changed"
	got := f.note()
	if !got.StandingChanged || !got.ToolsChanged {
		t.Fatal("lost change attribution")
	}
	raw, err := json.Marshal(got)
	testutil.FailErr(t, "encode cache observation", err)
	if strings.Contains(string(raw), "private-") {
		t.Fatalf("capture leaked prompt content: %s", raw)
	}
}

func TestUnmarkedUtilityRequestsCompareTheirActualHistory(t *testing.T) {
	f := newCacheFixture()
	f.req.Messages = []api.Message{{Role: api.MessageRoleUser, Content: "first input"}}
	f.note()
	f.req.Messages[0].Content = "different input"
	if got := f.note(); got.Comparison != "history_changed" {
		t.Fatalf("unrelated utilities compared: %+v", got)
	}
}

func TestCacheWindowLateCompletionCannotReplaceNewerBaseline(t *testing.T) {
	f := newCacheFixture()
	f.note()
	f.req.Messages[0].Content = "new prefix"
	f.note()
	scope := PromptCacheScope("provider", f.req.Model, f.req.Debug)
	older := newCacheFixture().req
	got := f.tracker.note(scope, older, f.policy, f.usage, f.now.Add(-time.Second), f.now.Add(time.Second), false)
	if got.Comparison != "overlapping_requests" {
		t.Fatalf("overlap not classified: %+v", got)
	}
	if got = f.note(); got.StandingChanged {
		t.Fatal("late completion overwrote the newer prefix")
	}
}

func TestCacheTrackerEvictsOldScopes(t *testing.T) {
	f := newCacheFixture()
	for i := range scopedstore.DefaultEntries + 1 {
		f.req.Debug.SessionID = fmt.Sprint(i)
		f.note()
	}
	if f.tracker.sessions.Len() != scopedstore.DefaultEntries {
		t.Fatal("tracker exceeded its bound")
	}
	f.req.Debug.SessionID = "0"
	if got := f.note(); got.Comparison != "first_request" {
		t.Fatal("old scope was not evicted")
	}
}

func TestCacheIdentityIgnoresToolPresentationButTracksReplay(t *testing.T) {
	f := newCacheFixture()
	f.req.Messages = []api.Message{
		{Role: api.MessageRoleSystem, Content: "standing", PromptCacheBreakpoint: api.PromptCacheTierStanding},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "call", Name: "read", Args: map[string]any{"path": "file"}}}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "read", ToolCallID: "call", Content: "result"}, PromptCacheBreakpoint: api.PromptCacheTierHistory},
	}
	f.note()
	f.req.Messages[1].ToolCalls[0].DisplayTitle = "new title"
	f.req.Messages[2].ToolResult.DisplayTitle = "new title"
	f.req.Messages[2].ToolResult.DisplaySubject = "Readable target"
	f.req.Messages[2].ToolResult.AssistantMessageID = "stored-row"
	if got := f.note(); got.Comparison != "comparable" {
		t.Fatalf("presentation changed identity: %+v", got)
	}
	f.req.Messages[1].ToolCalls[0].Args["path"] = "other"
	if got := f.note(); got.Comparison != "history_changed" {
		t.Fatalf("replay edit was missed: %+v", got)
	}
	f.req.Messages[2].ToolResult.Visual = &api.VisualArtifact{ID: "image", Mime: "image/png", Bytes: []byte{1}, Perceive: true}
	if got := f.note(); got.Comparison != "history_changed" {
		t.Fatalf("image was missed: %+v", got)
	}
}
