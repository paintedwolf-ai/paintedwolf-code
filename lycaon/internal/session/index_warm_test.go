package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/policyfacts"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/transcript"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
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
	mgr.Chats.Research.SetWarmer(warmer)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	userText := "summarize https://docs.example/widget"
	_, err = mgr.Submissions.Prompt(ctx, sess.ID, userText)
	testutil.FailErr(t, "prompt", err)
	mgr.Runner.Curation.Wait(t.Context())

	declaredURLs := warmer.declaredURLsSeen()
	if len(declaredURLs) != 1 || declaredURLs[0].urlSource != userText {
		t.Fatalf("declared URL warms = %+v want urlSource=%q", declaredURLs, userText)
	}
	msgs, err := mgr.Runner.Transcript.GetMessages(ctx, sess.ID)
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
	mgr.Chats.Research.SetWarmer(warmer)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	first := "summarize https://docs.example/widget"
	_, err = mgr.Submissions.Prompt(ctx, sess.ID, first)
	testutil.FailErr(t, "first prompt", err)
	mgr.Runner.Curation.Wait(t.Context())
	_, err = mgr.Submissions.Prompt(ctx, sess.ID, "also compare https://other.example/widget")
	testutil.FailErr(t, "follow-up prompt", err)
	mgr.Runner.Curation.Wait(t.Context())

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
	mgr.Chats.Research.SetWarmer(warmer)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	_, err = mgr.Submissions.Prompt(ctx, sess.ID, "read https://docs.example/widget")
	testutil.FailErr(t, "prompt", err)
	mgr.Runner.Curation.Wait(t.Context())
	if len(warmer.declaredURLsSeen()) != 1 {
		t.Fatalf("declared URL warm calls = %v", warmer.declaredURLsSeen())
	}
	msgs, err := mgr.Runner.Transcript.GetMessages(ctx, sess.ID)
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
	mgr.Chats.Research.SetWarmer(warmer)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	_, err = mgr.Submissions.LoopWake(ctx, sess.ID)
	testutil.FailErr(t, "prompt", err)
	if len(warmer.declaredURLsSeen()) != 0 {
		t.Fatalf("host turn warmed declared URLs: %v", warmer.declaredURLsSeen())
	}
	msgs, err := mgr.Runner.Transcript.GetMessages(ctx, sess.ID)
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
	mgr.Chats.Research.SetWarmer(warmer)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	_, err = mgr.Submissions.Prompt(ctx, sess.ID, surface.HostLoopWakeSentinel)
	testutil.FailErr(t, "prompt", err)
	mgr.Runner.Curation.Wait(t.Context())
	if len(warmer.declaredURLsSeen()) != 1 {
		t.Fatalf("first user prompt was not offered to declared-URL warmer: %v", warmer.declaredURLsSeen())
	}
	msgs, err := mgr.Runner.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "messages", err)
	for _, msg := range msgs {
		if msg.Content == surface.HostLoopWakeSentinel && msg.Origin == api.MessageOriginUser && msg.Kind == "" {
			return
		}
	}
	t.Fatal("typed sentinel was not persisted as an ordinary user turn")
}

func TestNavigationCandidateRechecksOnlyTheSelectedStoredAddress(t *testing.T) {
	registry := project.NewMemoryRegistry()
	p, err := registry.Create(t.Context(), project.CreateParams{Roots: []project.AttachRootParams{{Path: t.TempDir()}}})
	testutil.FailErr(t, "create project", err)
	mem := sessionstore.NewMemory()
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{}, p.ID)
	testutil.FailErr(t, "create session", err)
	mgr := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetProjectRegistry(registry)
	ref := api.NavigationReference{ID: "ref-0", Syntax: "code", Mention: "same.go", ProjectID: p.ID, Path: "same.go", Status: api.NavigationAmbiguous, Line: 12, EndLine: 20,
		Candidates: []api.NavigationTarget{
			{ProjectID: p.ID, RootID: p.Roots[0].ID, Path: "a/same.go", EntryKind: "file"},
			{ProjectID: p.ID, RootID: p.Roots[0].ID, Path: "b/same.go", EntryKind: "file", WorkerID: "worker-1"},
			{ProjectID: "other-project", RootID: p.Roots[0].ID, Path: "same.go", EntryKind: "file"},
		},
	}
	const content = "See `same.go`."
	testutil.FailErr(t, "append ambiguous message", mem.AppendMessages(t.Context(), sess.ID, api.Message{ID: "m", Role: api.MessageRoleAssistant, Content: content, NavigationRefs: []api.NavigationReference{ref}}))
	index := 1
	req := api.ResolveMessageNavigationRequest{MessageID: "m", ContentSHA256: sessionstore.NavigationContentHash(content), ReferenceID: ref.ID, CandidateIndex: &index}
	calls := 0
	resolve := func(_ context.Context, _ *project.Project, _ api.Message, refs []api.NavigationReference) []api.NavigationReference {
		calls++
		if len(refs) != 1 || refs[0].Path != "b/same.go" || refs[0].WorkerID != "worker-1" || refs[0].Line != 12 || refs[0].EndLine != 20 || len(refs[0].Candidates) != 0 {
			t.Fatalf("selected references = %+v", refs)
		}
		refs[0].Deleted = true
		return refs
	}
	out, err := mgr.Runner.Transcript.ResolveNavigation(t.Context(), sess.ID, req, resolve)
	testutil.FailErr(t, "resolve selected candidate", err)
	if !out.References[0].Deleted || calls != 1 {
		t.Fatalf("resolution = %+v, calls = %d", out, calls)
	}
	stored, err := mem.GetMessage(t.Context(), sess.ID, "m")
	testutil.FailErr(t, "read original reference", err)
	if stored.NavigationRefs[0].Status != api.NavigationAmbiguous || len(stored.NavigationRefs[0].Candidates) != 3 {
		t.Fatal("candidate selection rewrote durable binding")
	}
	for _, invalid := range []int{-1, 2, 3, 32} {
		req.CandidateIndex = &invalid
		if _, err := mgr.Runner.Transcript.ResolveNavigation(t.Context(), sess.ID, req, resolve); !errors.Is(err, transcript.ErrNavigationCandidateInvalid) {
			t.Fatalf("candidate %d = %v", invalid, err)
		}
	}
	if calls != 1 {
		t.Fatal("invalid candidate reached path resolution")
	}
}

func TestAssistantCommitAndNotePersistNavigationReferences(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	testutil.FailErr(t, "make scripts folder", os.Mkdir(filepath.Join(root, "scripts"), 0o755))
	testutil.FailErr(t, "write findings", os.WriteFile(filepath.Join(root, "FINDINGS.md"), []byte("x"), 0o600))
	registry := project.NewMemoryRegistry()
	p, err := registry.Create(ctx, project.CreateParams{Roots: []project.AttachRootParams{{Path: root}}})
	testutil.FailErr(t, "create project", err)
	mem := sessionstore.NewMemory()
	sess, err := mem.Create(ctx, api.CreateSessionRequest{}, p.ID)
	testutil.FailErr(t, "create session", err)
	mgr := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetProjectRegistry(registry)

	testutil.FailErr(t, "append agent note", mgr.Runner.Transcript.Append(ctx, sess.ID, api.Message{
		ID: "note-1", Role: api.MessageRoleAssistant, Kind: api.MessageKindAgentNote,
		Content: "See scripts/.", Visibility: api.MessageVisibilityTranscript,
	}))
	testutil.FailErr(t, "append provisional answer", mgr.Runner.Transcript.Append(ctx, sess.ID, api.Message{
		ID: "answer-1", Role: api.MessageRoleAssistant, Content: "See FINDINGS.md.",
		SourceContext: &api.SourceContext{Locations: []api.NavigationTarget{{ProjectID: p.ID, RootID: p.Roots[0].ID, Path: "FINDINGS.md", EntryKind: api.NavigationEntryKindFile}}},
		Visibility:    api.MessageVisibilityInternal,
	}))
	testutil.FailErr(t, "commit provisional answer", mgr.Runner.Transcript.Update(ctx, sess.ID, "answer-1", api.Message{
		ID: "answer-1", Role: api.MessageRoleAssistant, Content: "See FINDINGS.md.",
		Visibility: api.MessageVisibilityTranscript,
	}))
	msgs, err := mem.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "read messages", err)
	if len(msgs) != 2 || len(msgs[0].NavigationRefs) != 1 || len(msgs[1].NavigationRefs) != 1 {
		t.Fatalf("messages = %+v", msgs)
	}
	if msgs[0].NavigationRefs[0].Status != api.NavigationPending || msgs[1].NavigationRefs[0].Path != "FINDINGS.md" {
		t.Fatalf("navigation refs = %+v / %+v", msgs[0].NavigationRefs, msgs[1].NavigationRefs)
	}
}

func TestNavigationParsesWorktreeAbsolutePathsWithoutFilesystemValidation(t *testing.T) {
	registry := project.NewMemoryRegistry()
	p, err := registry.Create(t.Context(), project.CreateParams{Roots: []project.AttachRootParams{{Path: t.TempDir()}}})
	testutil.FailErr(t, "project", err)
	mem := sessionstore.NewMemory()
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{}, p.ID)
	testutil.FailErr(t, "session", err)
	branch := filepath.Join(t.TempDir(), "not-materialized")
	testutil.FailErr(t, "worktree binding", mem.PutWorktreeBinding(t.Context(), sessionstore.WorktreeBinding{SessionID: sess.ID, ProjectID: p.ID, Toplevel: p.Roots[0].Path, WorktreePath: branch, Branch: "work", BaseBranch: "main"}))
	mgr := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetProjectRegistry(registry)
	rows := []api.Message{{Role: api.MessageRoleAssistant, Content: "[file](" + filepath.ToSlash(filepath.Join(branch, "src/a.go")) + ")", Visibility: api.MessageVisibilityTranscript}}
	mgr.Runner.Transcript.NavigationRefs(t.Context(), sess.ID, rows)
	if len(rows[0].NavigationRefs) != 1 || rows[0].NavigationRefs[0].Path != "src/a.go" || rows[0].NavigationRefs[0].RootID != p.Roots[0].ID {
		t.Fatalf("references=%+v", rows[0].NavigationRefs)
	}
}

func TestTerminalRefusalsRetainPathsWithoutRecoverableGrants(t *testing.T) {
	gc := &oar.GuardContext{}
	policyfacts.ObserveConfine(gc, confine.Observation{Applied: true, FailedStages: []string{"tool"}, Refusals: confine.SandboxRefusals{Witness: confine.WitnessKernel, Refusals: []confine.SandboxRefusal{
		{Operation: "file-read-data", Target: "/state/store.db", Layer: confine.FloorReadControlPlane, Recovery: confine.RecoverNone},
		{Operation: "file-write-data", Target: "/state/approvals.yaml", Layer: confine.FloorControlPlane, Recovery: confine.RecoverNone},
	}}})
	if !slices.Equal(gc.Refusals.RefusedTerminalReadPaths, []string{"/state/store.db"}) || !slices.Equal(gc.Refusals.RefusedTerminalWritePaths, []string{"/state/approvals.yaml"}) || len(gc.Refusals.RefusedReadGrants) != 0 || len(gc.Refusals.RefusedWriteGrants) != 0 || gc.Refusals.SandboxRefusalWitness != string(confine.WitnessKernel) {
		t.Fatalf("terminal facts=%+v", gc)
	}
}

func TestRefusalWitnessReportsTheObservedLayerWithoutPolicyCombination(t *testing.T) {
	for _, witness := range []confine.RefusalWitness{confine.WitnessKernel, confine.WitnessIncomplete, confine.WitnessUnavailable} {
		for _, failed := range []bool{false, true} {
			gc := &oar.GuardContext{}
			obs := confine.Observation{Applied: true, Refusals: confine.SandboxRefusals{Witness: witness}}
			if failed {
				obs.FailedStages = []string{"tool"}
			}
			policyfacts.ObserveConfine(gc, obs)
			if gc.Refusals.SandboxRefusalWitness != string(witness) {
				t.Fatalf("witness=%s failed=%v facts=%+v", witness, failed, gc)
			}
		}
	}
}
