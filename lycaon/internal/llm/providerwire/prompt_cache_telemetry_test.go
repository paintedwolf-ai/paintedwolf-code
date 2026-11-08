package providerwire

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/scopedstore"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
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
		{"controls", "prefix_changed", func(f *cacheFixture) { f.req.Think = modelcall.ThinkHigh }},
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
	f.req.MaxTokens = 18952
	f.req.ControlCapture = &modelcall.RequestControlCapture{}
	f.req.ControlCapture.Record([]byte(`{"max_tokens":18952,"temperature":1}`))
	f.note()
	f.req.MaxTokens = 17261
	f.req.ControlCapture = &modelcall.RequestControlCapture{}
	f.req.ControlCapture.Record([]byte(`{"max_tokens":17261,"temperature":1}`))
	if got = f.note(); got.Comparison != "comparable" || got.ControlsChanged {
		t.Fatalf("output budget fit invalidated prefix controls: %+v", got)
	}
}

func TestCacheControlSnapshotsStripOutputCaps(t *testing.T) {
	if got := cacheControlSnapshots(nil); got != nil {
		t.Fatalf("empty controls produced snapshots: %v", got)
	}
	snapshots := cacheControlSnapshots([]map[string]json.RawMessage{
		{
			"max_tokens":            json.RawMessage(`18952`),
			"max_completion_tokens": json.RawMessage(`4096`),
			"temperature":           json.RawMessage(`1`),
			"options":               json.RawMessage(`{"num_predict":512,"temperature":0.7}`),
			"generationConfig":      json.RawMessage(`{"maxOutputTokens":256,"stopSequences":["done"]}`),
		},
		{"options": json.RawMessage(`{"num_predict":512`)},
	})
	if len(snapshots) != 2 {
		t.Fatalf("snapshot count = %d", len(snapshots))
	}
	snapshot := snapshots[0]
	for _, dropped := range []string{"max_tokens", "max_completion_tokens"} {
		if _, ok := snapshot[dropped]; ok {
			t.Fatalf("%s survived filtering", dropped)
		}
	}
	if len(snapshot["temperature"]) == 0 {
		t.Fatal("unrelated control key was dropped")
	}
	var options map[string]any
	testutil.FailErr(t, "decode filtered options", json.Unmarshal(snapshot["options"], &options))
	if _, ok := options["num_predict"]; ok {
		t.Fatal("num_predict survived filtering")
	}
	if options["temperature"] != 0.7 {
		t.Fatalf("options lost unrelated keys: %v", options)
	}
	var generation map[string]any
	testutil.FailErr(t, "decode filtered generationConfig", json.Unmarshal(snapshot["generationConfig"], &generation))
	if _, ok := generation["maxOutputTokens"]; ok {
		t.Fatal("maxOutputTokens survived filtering")
	}
	if generation["stopSequences"] == nil {
		t.Fatal("generationConfig lost unrelated keys")
	}
	if string(snapshots[1]["options"]) != `{"num_predict":512` {
		t.Fatalf("malformed control was rewritten: %s", snapshots[1]["options"])
	}
}

func TestCacheIdentityControlsIgnoreOutputCaps(t *testing.T) {
	f := newCacheFixture()
	f.req.ControlCapture = &modelcall.RequestControlCapture{}
	f.req.ControlCapture.Record([]byte(`{"max_tokens":18952,"options":{"num_predict":512}}`))
	budgetA := cacheRequestIdentity(f.req, f.policy)
	f.req.ControlCapture = &modelcall.RequestControlCapture{}
	f.req.ControlCapture.Record([]byte(`{"max_tokens":17261,"options":{"num_predict":4096}}`))
	budgetB := cacheRequestIdentity(f.req, f.policy)
	if !budgetA.valid || !budgetB.valid || budgetA.controls != budgetB.controls {
		t.Fatal("output budget fit changed the control identity")
	}
	f.req.ControlCapture = &modelcall.RequestControlCapture{}
	f.req.ControlCapture.Record([]byte(`{"max_tokens":17261,"temperature":0}`))
	if budgetC := cacheRequestIdentity(f.req, f.policy); budgetC.controls == budgetB.controls {
		t.Fatal("real control change was ignored")
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

func TestCanonicalRecallKeepsStandingSchemaStableAcrossRecovery(t *testing.T) {
	raw, err := config.Read(config.ToolSchemasDir + "/recall.yaml")
	testutil.FailErr(t, "read shipped recall schema", err)
	entry, err := toolschema.ParseEntry(raw)
	testutil.FailErr(t, "parse shipped recall schema", err)
	f := newCacheFixture()
	f.req.Tools = []tools.ToolMeta{{Name: "recall", Description: entry.Description, ArgsSchema: entry.Schema}}
	f.note()
	before := cacheRequestIdentity(f.req, f.policy)
	f.req.Messages = append(f.req.Messages,
		api.Message{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "recovery", Name: "recall", Args: map[string]any{"history": map[string]any{"through_ord": 500, "message_id": "old"}}}}},
		api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "recall", ToolCallID: "recovery", Content: `{"history":{"entries":[]}}`}, PromptCacheBreakpoint: api.PromptCacheTierHistory})
	if got := f.note(); got.Comparison != "comparable" {
		t.Fatalf("append-only recovery broke cache continuity: %+v", got)
	}
	after := cacheRequestIdentity(f.req, f.policy)
	if !before.valid || !after.valid || before.tools != after.tools || before.standing != after.standing {
		t.Fatal("recovery changed the standing schema or prompt")
	}
	f.req.Messages[len(f.req.Messages)-2].ToolCalls[0].Args["history"] = map[string]any{"through_ord": 500, "message_id": "corrected"}
	if got := f.note(); got.Comparison != "history_changed" {
		t.Fatalf("changed recovery coordinates were not tracked: %+v", got)
	}
}
