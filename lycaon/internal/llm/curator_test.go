package llm

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubCuratorProvider struct {
	id        string
	responses []string
	calls     int
	err       error
	// budget overrides the driver's utility call timeout so a test can watch a
	// deadline expire without waiting for a real one.
	budget time.Duration
}

func (s *stubCuratorProvider) ID() string { return s.id }

func (s *stubCuratorProvider) Complete(_ context.Context, _ modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if s.err != nil {
		return nil, s.err
	}
	i := s.calls
	s.calls++
	if len(s.responses) == 0 {
		return &modelcall.Completion{Content: `{}`}, nil
	}
	if i >= len(s.responses) {
		i = len(s.responses) - 1
	}
	return &modelcall.Completion{Content: s.responses[i]}, nil
}

func (s *stubCuratorProvider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk, 1)
	go func() {
		defer close(ch)
		c, err := s.Complete(ctx, req)
		if err != nil {
			return
		}
		ch <- modelcall.StreamChunk{Content: c.Content, Done: true}
	}()
	return ch, nil
}

func (s *stubCuratorProvider) Models() []modelcall.ModelInfo {
	return []modelcall.ModelInfo{{ID: "lite"}}
}

func (s *stubCuratorProvider) Profile() providerprofile.Profile {
	return providerprofile.Profile{UtilityCallTimeout: s.budget}
}

func newTestRegistrySummarizer(t *testing.T, provider modelcall.Provider) *RegistrySummarizer {
	t.Helper()
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	policy := NewInMemoryPolicyStore(ModelPolicy{
		Lite: ModelRef{ProviderID: "lite", Model: "lite"},
	})

	reg := newEmptyRegistry()
	testutil.FailErr(t, "Register", reg.Register(provider))

	return &RegistrySummarizer{
		Registry: reg,
		Policy:   policy,
		Scope:    SettingsScopeGlobal,
	}
}

func snapshotFromRead(t *testing.T, path, lineContent string, line int) evidence.Ledger {
	t.Helper()
	readJSON := fmt.Sprintf(`{"path":%q,"content":%q,"offset":%d,"end_line":%d,"limit":1}`,
		path, lineContent, line, line)
	return ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "read", ID: "c1", Args: map[string]any{"path": path, "offset": line, "limit": 1}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: readJSON}},
	})
}

func TestRegistrySummarizerCurate_resolvesSnapshotSelection(t *testing.T) {
	snapshot := snapshotFromRead(t, "pkg/a.go", "10| func entry() {}", 10)
	provider := &stubCuratorProvider{
		id:        "lite",
		responses: []string{`{"selections":[{"path":"pkg/a.go","line":10,"excerpt":"func entry"}],"gloss":[{"label":"entry helper"}]}`},
	}
	cur := newTestRegistrySummarizer(t, provider)
	got, err := cur.Curate(context.Background(), snapshot, CurationFocus{Target: "entry"}, 3)
	testutil.FailErr(t, "Curate", err)
	if got.Report.Fallback {
		t.Fatal("unexpected fallback")
	}
	if len(got.Selections) != 1 {
		t.Fatalf("selections = %d want 1", len(got.Selections))
	}
	if got.Selections[0].Resolution.Verdict != evidence.VerdictMatched {
		t.Fatalf("verdict = %q", got.Selections[0].Resolution.Verdict)
	}
	if len(got.Gloss) != 1 || got.Gloss[0].Label != "entry helper" {
		t.Fatalf("gloss = %#v", got.Gloss)
	}
}

func TestRegistrySummarizerCurate_dropsFabricatedSelection(t *testing.T) {
	snapshot := snapshotFromRead(t, "pkg/a.go", "10| func entry() {}", 10)
	provider := &stubCuratorProvider{
		id:        "lite",
		responses: []string{`{"selections":[{"path":"other.go","line":1,"excerpt":"missing"}],"gloss":[]}`},
	}
	cur := newTestRegistrySummarizer(t, provider)
	got, err := cur.Curate(context.Background(), snapshot, CurationFocus{Target: "focus"}, 3)
	testutil.FailErr(t, "Curate", err)
	if len(got.Selections) != 0 {
		t.Fatalf("selections = %#v", got.Selections)
	}
	if got.Report.Dropped == 0 {
		t.Fatal("expected dropped count")
	}
}

func TestRegistrySummarizerCurate_snapshotIsolation(t *testing.T) {
	snapshot := snapshotFromRead(t, "current.go", "5| current only", 5)
	provider := &stubCuratorProvider{
		id:        "lite",
		responses: []string{`{"selections":[{"path":"prior.go","line":1,"excerpt":"prior survey"}],"gloss":[]}`},
	}
	cur := newTestRegistrySummarizer(t, provider)
	got, err := cur.Curate(context.Background(), snapshot, CurationFocus{Target: "focus"}, 3)
	testutil.FailErr(t, "Curate", err)
	if len(got.Selections) != 0 {
		t.Fatalf("prior-survey path must not resolve against snapshot: %#v", got.Selections)
	}
}

func TestRegistrySummarizerCurate_liteProviderFallback(t *testing.T) {
	cur := &RegistrySummarizer{}
	snapshot := snapshotFromRead(t, "a.go", "1| x", 1)
	got, err := cur.Curate(context.Background(), snapshot, CurationFocus{Target: "focus"}, 3)
	testutil.FailErr(t, "Curate", err)
	if !got.Report.Fallback || len(got.Selections) != 0 {
		t.Fatalf("fallback = %#v", got)
	}
}

func TestRegistrySummarizerCurate_doesNotCacheDownSlot(t *testing.T) {
	provider := &stubCuratorProvider{
		id:        "lite",
		responses: []string{`{"selections":[{"path":"pkg/a.go","line":10,"excerpt":"func entry"}],"gloss":[]}`},
	}
	cur := newTestRegistrySummarizer(t, provider)
	cur.Plane = NewUtilityPlane()
	cur.Plane.noteOutcome("lite", &failure.ProviderUnreachableError{ProviderID: "lite", Model: "lite", Attempts: 1})
	snapshot := snapshotFromRead(t, "pkg/a.go", "10| func entry() {}", 10)

	got, err := cur.Curate(context.Background(), snapshot, CurationFocus{Target: "entry"}, 3)
	testutil.FailErr(t, "Curate down", err)
	if !got.Report.Fallback || len(got.Selections) != 0 {
		t.Fatalf("down slot = %#v", got)
	}
	if provider.calls != 0 {
		t.Fatalf("down slot still called lite (%d)", provider.calls)
	}

	cur.Plane.Reset()
	got, err = cur.Curate(context.Background(), snapshot, CurationFocus{Target: "entry"}, 3)
	testutil.FailErr(t, "Curate after reset", err)
	if got.Report.Fallback || len(got.Selections) == 0 {
		t.Fatalf("after reset = %#v", got)
	}
	if provider.calls != 1 {
		t.Fatalf("calls = %d want 1", provider.calls)
	}
}

func TestRegistrySummarizerCurate_cacheHit(t *testing.T) {
	snapshot := snapshotFromRead(t, "pkg/a.go", "10| func entry() {}", 10)
	provider := &stubCuratorProvider{
		id:        "lite",
		responses: []string{`{"selections":[{"path":"pkg/a.go","line":10,"excerpt":"func entry"}],"gloss":[]}`},
	}
	cur := newTestRegistrySummarizer(t, provider)
	ctx := context.Background()
	first, err := cur.Curate(ctx, snapshot, CurationFocus{Target: "entry"}, 3)
	testutil.FailErr(t, "Curate first", err)
	second, err := cur.Curate(ctx, snapshot, CurationFocus{Target: "entry"}, 3)
	testutil.FailErr(t, "Curate second", err)
	if !second.Report.CacheHit {
		t.Fatal("expected cache hit")
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d want 1", provider.calls)
	}
	if len(first.Selections) != len(second.Selections) {
		t.Fatalf("cache mismatch selections")
	}
}

func TestParseCurationResponse_rejectsFreeformBody(t *testing.T) {
	got, err := parseCurationResponse(`{"selections":[{"path":"a.go","line":1,"body":"whole file dump"}],"gloss":[]}`)
	testutil.FailErr(t, "parse", err)
	if len(got.Selections) != 0 {
		t.Fatalf("selections = %#v", got.Selections)
	}
	got, err = parseCurationResponse(`{"selections":[{"path":"a.go","line":1,"excerpt":"ok"}],"gloss":[{"label":"nav"}]}`)
	testutil.FailErr(t, "parse", err)
	if len(got.Selections) != 1 {
		t.Fatalf("selections = %#v", got.Selections)
	}
}

func TestFenceGloss_dropsEmptyAndMultiline(t *testing.T) {
	out := fenceGloss([]GlossLine{
		{Label: "overview"},
		{Label: "line one\nline two"},
		{Label: "  "},
	})
	if len(out) != 1 || out[0].Label != "overview" {
		t.Fatalf("gloss = %#v", out)
	}
}

func TestRegistrySummarizerCurate_retryUnionOnParseError(t *testing.T) {
	snapshot := snapshotFromRead(t, "pkg/a.go", "10| func entry() {}", 10)
	provider := &stubCuratorProvider{
		id: "lite",
		responses: []string{
			`not json`,
			`{"selections":[{"path":"pkg/a.go","line":10,"excerpt":"func entry"}],"gloss":[]}`,
		},
	}
	cur := newTestRegistrySummarizer(t, provider)
	got, err := cur.Curate(context.Background(), snapshot, CurationFocus{Target: "entry"}, 3)
	testutil.FailErr(t, "Curate", err)
	if len(got.Selections) != 1 {
		t.Fatalf("selections = %#v", got.Selections)
	}
	if got.Report.Retries == 0 {
		t.Fatal("expected retry count")
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls = %d want 2", provider.calls)
	}
}

func TestRegistrySummarizerCurate_usesLiteProvider(t *testing.T) {
	snapshot := snapshotFromRead(t, "pkg/a.go", "10| func entry() {}", 10)
	provider := &stubCuratorProvider{
		id:        "lite",
		responses: []string{`{"selections":[],"gloss":[]}`},
	}
	cur := newTestRegistrySummarizer(t, provider)
	_, err := cur.Curate(context.Background(), snapshot, CurationFocus{Target: "entry"}, 3)
	testutil.FailErr(t, "Curate", err)
	if provider.calls != 1 {
		t.Fatalf("lite provider calls = %d", provider.calls)
	}
}
