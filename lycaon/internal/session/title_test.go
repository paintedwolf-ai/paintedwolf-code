package session

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestTrimSessionTitleEnforcesMaxRunes(t *testing.T) {
	long := strings.Repeat("a", maxSessionTitleRunes+10)
	got := trimSessionTitle(long)
	if len([]rune(got)) > maxSessionTitleRunes {
		t.Fatalf("title rune count = %d want <= %d", len([]rune(got)), maxSessionTitleRunes)
	}
	if got != strings.Repeat("a", maxSessionTitleRunes) {
		t.Fatalf("title = %q want %d runes of a", got, maxSessionTitleRunes)
	}
}

func TestTrimSessionTitlePreservesWordBoundary(t *testing.T) {
	got := trimSessionTitle("Secrets Tool Testing Across Real-World Contexts")
	if got != "Secrets Tool Testing Across Real-World" {
		t.Fatalf("title = %q want whole-word truncation", got)
	}
}

func TestTrimSessionTitleStripsQuotesAndTrailingPunct(t *testing.T) {
	got := trimSessionTitle(`  "Fix login bug!"  `)
	if got != "Fix login bug" {
		t.Fatalf("title = %q want %q", got, "Fix login bug")
	}
}

type recordingNamer struct {
	calls  int
	text   string
	system string
	user   string
}

func (r *recordingNamer) Name(_ context.Context, system, user string) (string, error) {
	r.calls++
	r.system = system
	r.user = user
	return r.text, nil
}

func TestNameSessionCallsNamerWhenNotMock(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "")
	ctx := context.Background()
	rec := &recordingNamer{text: "Todo CLI"}
	got := NameSession(ctx, rec, "Build a CLI todo tracker with SQLite persistence")
	if rec.calls != 1 {
		t.Fatalf("Name calls = %d want 1", rec.calls)
	}
	if got != "Todo CLI" {
		t.Fatalf("title = %q want %q", got, "Todo CLI")
	}
	if rec.system == "" {
		t.Fatal("system prompt was empty")
	}
	if rec.user != "Build a CLI todo tracker with SQLite persistence" {
		t.Fatalf("user prompt = %q", rec.user)
	}
}

func TestNameSessionSkipsNamerUnderMock(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	ctx := context.Background()
	rec := &recordingNamer{text: "ignored"}
	got := NameSession(ctx, rec, "Build a CLI todo tracker with SQLite persistence")
	if rec.calls != 0 {
		t.Fatalf("Name calls = %d want 0 under mock", rec.calls)
	}
	want := trimSessionTitle("Build a CLI todo tracker with SQLite persistence")
	if got != want {
		t.Fatalf("title = %q want truncated %q", got, want)
	}
}

func TestSessionNamerUsesPlaneWhenServiceWired(t *testing.T) {
	policy, err := llm.NewPolicyStore()
	if err != nil {
		t.Fatalf("policy store: %v", err)
	}
	svc := &llm.Service{
		Registry: &llm.Registry{},
		Policy:   policy,
		Utility:  llm.NewUtilityPlane(),
	}
	mgr := NewManager(store.NewMemory(), nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.llmSvc = svc

	namer := mgr.sessionNamer(nil, "session_title", "/tmp/proj")
	if _, ok := namer.(llm.PlaneNamer); !ok {
		t.Fatalf("namer type = %T want llm.PlaneNamer", namer)
	}
}

func TestSessionNamerDoesNotBypassTheRoutedUtilityPlane(t *testing.T) {
	mgr := NewManager(store.NewMemory(), nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	if namer := mgr.sessionNamer(nil, "session_title", "/tmp/proj"); namer != nil {
		t.Fatalf("namer type = %T want nil without routed utility service", namer)
	}
}
