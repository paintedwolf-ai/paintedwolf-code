package session

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type declaredURLWarmCall struct {
	urlSource string
}

// fakeIndexWarmer records warm calls and fires onDone synchronously.
type fakeIndexWarmer struct {
	mu           sync.Mutex
	declaredURLs []declaredURLWarmCall
	cancels      []string
	meta         api.IndexWarmingMeta
	fire         bool
}

func (f *fakeIndexWarmer) CancelSession(sessionID string) {
	f.mu.Lock()
	f.cancels = append(f.cancels, sessionID)
	f.mu.Unlock()
}

func (f *fakeIndexWarmer) WarmDeclaredURLsAsync(urlSource, _, _, _ string, onDone func(api.IndexWarmingMeta)) {
	f.mu.Lock()
	f.declaredURLs = append(f.declaredURLs, declaredURLWarmCall{urlSource: urlSource})
	f.mu.Unlock()
	if f.fire && onDone != nil {
		onDone(f.meta)
	}
}

func (f *fakeIndexWarmer) WarmSearchAsync(_, _, _, _, _ string, _, _ []string, _, _ int, _ bool, onDone func(api.IndexWarmingMeta)) {
	if f.fire && onDone != nil {
		onDone(f.meta)
	}
}

func (f *fakeIndexWarmer) WarmFetchAsync(_, _, _, _, _, _ string, onDone func(api.IndexWarmingMeta)) {
	if f.fire && onDone != nil {
		onDone(f.meta)
	}
}

func (f *fakeIndexWarmer) declaredURLsSeen() []declaredURLWarmCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]declaredURLWarmCall, len(f.declaredURLs))
	copy(out, f.declaredURLs)
	return out
}

func (f *fakeIndexWarmer) cancelsSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.cancels))
	copy(out, f.cancels)
	return out
}

func TestFirstPromptWithDeclaredURLFiresIndexWarmerAndAppendsMessage(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	mgr, store := newTestManager(t)
	warmer := &fakeIndexWarmer{
		fire: true,
		meta: api.IndexWarmingMeta{
			Trigger: "declared_url", Tier: "crawl", Topic: "docs.example",
			Hosts: []string{"docs.example"}, Pages: 3,
		},
	}
	mgr.SetIndexWarmer(warmer)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	userText := "summarize https://docs.example/widget"
	_, err = mgr.Prompt(ctx, sess.ID, userText)
	testutil.FailErr(t, "prompt", err)
	mgr.WaitForPromptCuration(t.Context())

	declaredURLs := warmer.declaredURLsSeen()
	if len(declaredURLs) != 1 || declaredURLs[0].urlSource != userText {
		t.Fatalf("declared URL warms = %+v want urlSource=%q", declaredURLs, userText)
	}
	msgs, err := mgr.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "messages", err)
	var row *api.Message
	for i := range msgs {
		if msgs[i].Kind == api.MessageKindIndexWarming {
			row = &msgs[i]
		}
	}
	if row == nil {
		t.Fatalf("messages = %+v want index_warming row", msgs)
	}
	if row.Role != api.MessageRoleSystem || row.IndexWarming == nil || row.IndexWarming.Pages != 3 {
		t.Fatalf("row = %+v", row)
	}
	if !strings.Contains(row.Content, "1 host") || !strings.Contains(row.Content, "3 pages") ||
		!strings.Contains(row.Content, "docs.example") {
		t.Fatalf("content = %q want prose summary", row.Content)
	}
	if api.IsPromptHistoryMessage(*row) {
		t.Fatal("index_warming row must be excluded from prompt history")
	}
}

func TestEachPromptOffersItsDeclaredURLsToWarming(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	mgr, store := newTestManager(t)
	warmer := &fakeIndexWarmer{}
	mgr.SetIndexWarmer(warmer)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	first := "summarize https://docs.example/widget"
	_, err = mgr.Prompt(ctx, sess.ID, first)
	testutil.FailErr(t, "first prompt", err)
	mgr.WaitForPromptCuration(t.Context())
	_, err = mgr.Prompt(ctx, sess.ID, "also compare https://other.example/widget")
	testutil.FailErr(t, "follow-up prompt", err)
	mgr.WaitForPromptCuration(t.Context())

	declaredURLs := warmer.declaredURLsSeen()
	if len(declaredURLs) != 2 || declaredURLs[0].urlSource != first ||
		declaredURLs[1].urlSource != "also compare https://other.example/widget" {
		t.Fatalf("declared URL warms = %+v want both user prompts", declaredURLs)
	}
}

func TestWarmDeclaredURLsWithoutCallbackResultAppendsNothing(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	mgr, store := newTestManager(t)
	warmer := &fakeIndexWarmer{}
	mgr.SetIndexWarmer(warmer)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	_, err = mgr.Prompt(ctx, sess.ID, "read https://docs.example/widget")
	testutil.FailErr(t, "prompt", err)
	mgr.WaitForPromptCuration(t.Context())
	if len(warmer.declaredURLsSeen()) != 1 {
		t.Fatalf("declared URL warm calls = %v", warmer.declaredURLsSeen())
	}
	msgs, err := mgr.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "messages", err)
	for _, msg := range msgs {
		if msg.Kind == api.MessageKindIndexWarming {
			t.Fatalf("unexpected row: %+v", msg)
		}
	}
}

func TestHostTurnsDoNotWarm(t *testing.T) {
	mgr, store := newTestManager(t)
	warmer := &fakeIndexWarmer{fire: true, meta: api.IndexWarmingMeta{Trigger: "declared_url", Pages: 1}}
	mgr.SetIndexWarmer(warmer)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	_, err = mgr.promptHostLoopWake(ctx, sess.ID)
	testutil.FailErr(t, "prompt", err)
	if len(warmer.declaredURLsSeen()) != 0 {
		t.Fatalf("host turn warmed declared URLs: %v", warmer.declaredURLsSeen())
	}
	msgs, err := mgr.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "messages", err)
	for _, msg := range msgs {
		if msg.Kind == api.MessageKindHostLoopWake && msg.Origin == api.MessageOriginHost && msg.HostSignalID == anchor.LoopWake.String() {
			return
		}
	}
	t.Fatal("host loop wake was not persisted with structured identity")
}

func TestHostLoopWakeTextFromUserRemainsUserIntent(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	mgr, store := newTestManager(t)
	warmer := &fakeIndexWarmer{}
	mgr.SetIndexWarmer(warmer)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	_, err = mgr.Prompt(ctx, sess.ID, surface.HostLoopWakeSentinel)
	testutil.FailErr(t, "prompt", err)
	mgr.WaitForPromptCuration(t.Context())
	if len(warmer.declaredURLsSeen()) != 1 {
		t.Fatalf("first user prompt was not offered to declared-URL warmer: %v", warmer.declaredURLsSeen())
	}
	msgs, err := mgr.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "messages", err)
	for _, msg := range msgs {
		if msg.Content == surface.HostLoopWakeSentinel && msg.Origin == api.MessageOriginUser && msg.Kind == "" {
			return
		}
	}
	t.Fatal("typed sentinel was not persisted as an ordinary user turn")
}

func TestIndexWarmingSummaryLine(t *testing.T) {
	got := indexWarmingSummary(api.IndexWarmingMeta{
		Topic: "steam machine", Hosts: []string{"a", "b"}, Pages: 1, DurationMs: 1200,
	})
	if got != "Warmed web index — 2 hosts, 1 page · steam machine" {
		t.Fatalf("summary = %q", got)
	}
	skip := indexWarmingSummary(api.IndexWarmingMeta{Topic: "x", SkipReason: "hourly seed cap"})
	if !strings.Contains(skip, "hourly seed cap") {
		t.Fatalf("summary = %q", skip)
	}
	partial := indexWarmingSummary(api.IndexWarmingMeta{
		Topic: "widget", Hosts: []string{"a.example"}, Pages: 2, SkipReason: "hourly seed cap",
	})
	if strings.Contains(partial, "hourly seed cap") {
		t.Fatalf("summary = %q want no skip when crawl succeeded", partial)
	}
}
