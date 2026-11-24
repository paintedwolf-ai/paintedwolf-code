package sessions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/app/security"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/repochange/repochangetest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type recordingActivityPublisher struct {
	mu     sync.Mutex
	events []api.ActivityEvent
}

func (p *recordingActivityPublisher) PublishActivity(_ context.Context, _, _ string, ev api.ActivityEvent) {
	p.mu.Lock()
	p.events = append(p.events, ev)
	p.mu.Unlock()
}

func (p *recordingActivityPublisher) snapshot() []api.ActivityEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]api.ActivityEvent(nil), p.events...)
}

func TestSharedCredentialBuildCoalescesCancelBoundaryAndRevalidation(t *testing.T) {
	repochangetest.Acquire(t)
	files := credentialFilesFixture(t)
	files.startCredentialIndex()
	t.Cleanup(func() { testutil.FailErr(t, "close index", files.closeCredentialIndex(context.Background())) })

	pub := &recordingActivityPublisher{}
	files.index.events = pub
	nowMu := sync.Mutex{}
	currentTime := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	files.index.now = func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return currentTime
	}
	tickCh := make(chan time.Time, 4)
	files.index.after = func(time.Duration) <-chan time.Time { return tickCh }
	completeWatch := true
	files.index.coverage = func(string) repochange.WatchCoverage {
		return repochange.WatchCoverage{Watching: true, Complete: completeWatch}
	}

	root := attachCredentialRoot(t, files)
	testutil.FailErr(t, "mkdir sub", os.MkdirAll(filepath.Join(root, "sub"), 0o755))
	const secretV1 = "tp06-initial-secret-11223344"
	const secretV2 = "tp06-rotated-secret-55667788"
	testutil.FailErr(t, "write env", os.WriteFile(filepath.Join(root, "sub/.env"), []byte("API_KEY="+secretV1+"\n"), 0o600))
	testutil.FailErr(t, "write plain", os.WriteFile(filepath.Join(root, "sub/readme.txt"), []byte("docs\n"), 0o644))

	var phaseMu sync.Mutex
	var phases []secretmatch.ScreenPhaseRecord
	unsub := secretmatch.RegisterPhaseObserver(func(rec secretmatch.ScreenPhaseRecord) {
		phaseMu.Lock()
		phases = append(phases, rec)
		phaseMu.Unlock()
	})
	defer unsub()

	// Hold the first build at the "sub" directory boundary while model screen, tool admission,
	// and background briefing join the same owned build; cancel the briefing caller mid-walk.
	atSubDir := make(chan struct{})
	releaseSubDir := make(chan struct{})
	var subOnce sync.Once
	files.index.onDir = func(_, rel string) {
		if rel == "sub" {
			subOnce.Do(func() {
				close(atSubDir)
				<-releaseSubDir
			})
		}
	}

	secRuntime := &security.Runtime{CredentialFiles: files}
	toolReg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register wait probe", toolReg.Register("wait", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		return "settled", nil
	}))
	exec := toolexecution.NewExecutor(nil, toolReg, "implement")
	tcFor := func(sessionID, callID string) tools.ToolContext {
		return tools.ToolContext{
			Identity: tools.InvocationIdentity{ProjectID: "proj-1", SessionID: sessionID, ToolCallID: callID, Agent: "implement"},
			Effects:  tools.InvocationEffects{CredentialFiles: files},
		}
	}

	modelDone := make(chan error, 1)
	toolDone := make(chan error, 1)
	briefCtx, cancelBrief := context.WithCancel(secretmatch.WithScreenCallID(t.Context(), "call-brief"))
	briefDone := make(chan error, 1)

	go func() {
		_, err := secRuntime.ModelScreeningValues(secretmatch.WithScreenCallID(t.Context(), "call-fg-model"), "proj-1", "sess-fg")
		modelDone <- err
	}()
	go func() {
		_, err := exec.Invoke(t.Context(), "wait", map[string]any{}, tcFor("sess-fg", "tool-1"))
		toolDone <- err
	}()
	go func() {
		_, err := secRuntime.ModelScreeningValues(briefCtx, "proj-1", "sess-brief")
		briefDone <- err
	}()

	<-atSubDir
	// Trigger wait-activity reporting while real build work is held, then cancel the briefing waiter.
	tickCh <- currentTime.Add(credentialWaitShown)
	testutil.WaitFor(t, "indexing_credentials activity published", func() bool {
		for _, ev := range pub.snapshot() {
			if ev.Kind == api.ActivityKindIndexingCredentials && ev.Status == api.ActivityStatusActive {
				return true
			}
		}
		return false
	})
	cancelBrief()
	if err := <-briefDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled briefing err = %v, want Canceled", err)
	}

	files.index.onDir = nil
	close(releaseSubDir)
	testutil.FailErr(t, "model admission", <-modelDone)
	testutil.FailErr(t, "tool admission", <-toolDone)

	// Verify exactly 1 build ran and published evidence, and active wait activity closed to Done.
	builds := filterScreenPhases(&phaseMu, &phases, "credential_build")
	if len(builds) != 1 || builds[0].CredentialFilesRead != 1 {
		t.Fatalf("expected 1 owned build reading 1 credential file, got %+v", builds)
	}
	var sawDoneActivity bool
	for _, ev := range pub.snapshot() {
		if ev.Kind == api.ActivityKindIndexingCredentials && ev.Status == api.ActivityStatusDone {
			sawDoneActivity = true
		}
	}
	if !sawDoneActivity {
		t.Fatalf("expected closed indexing_credentials activity, got %+v", pub.snapshot())
	}

	// Terminal wait against unchanged root performs 0 builds, 0 listings, 0 file reads.
	clearScreenPhases(&phaseMu, &phases)
	_, err := exec.Invoke(t.Context(), "wait", map[string]any{}, tcFor("sess-fg", "tool-2"))
	testutil.FailErr(t, "warm terminal wait", err)
	if got := filterScreenPhases(&phaseMu, &phases, "credential_build"); len(got) != 0 {
		t.Fatalf("warm terminal wait triggered unexpected builds: %+v", got)
	}

	// Contiguous IndexChanged and complete-coverage HeadMoved perform 0 walks and 0 content rereads.
	repochange.WaitQuiet(t.Context(), root, repochange.DefaultDebounce, 2*time.Second)
	testutil.FailErr(t, "drain watcher startup", files.Await(t.Context(), tools.CredentialWait{ProjectID: "proj-1"}))
	var scans atomic.Int32
	files.index.onScan = func(string, string) { scans.Add(1) }
	repochange.Notify(t.Context(), repochange.Event{ProjectDir: root, Kind: repochange.IndexChanged})
	testutil.FailErr(t, "await after index changed", files.Await(t.Context(), tools.CredentialWait{ProjectID: "proj-1"}))
	repochange.Notify(t.Context(), repochange.Event{ProjectDir: root, Kind: repochange.HeadMoved})
	testutil.FailErr(t, "await after head moved", files.Await(t.Context(), tools.CredentialWait{ProjectID: "proj-1"}))
	if scans.Load() != 0 {
		t.Fatalf("contiguous IndexChanged/HeadMoved performed %d scans, want 0", scans.Load())
	}

	// Known sibling checkout edit produces 0 attached-root builds.
	siblingDir := t.TempDir()
	sibEp := repochange.Advance(siblingDir)
	files.index.changed(repochange.Event{
		ProjectDir: siblingDir, Kind: repochange.WorktreeChanged, Paths: []string{".env"},
		EpochBootID: sibEp.BootID, WorktreeEpoch: sibEp.Value,
	})
	testutil.FailErr(t, "await after sibling edit", files.Await(t.Context(), tools.CredentialWait{ProjectID: "proj-1"}))
	if scans.Load() != 0 {
		t.Fatalf("sibling edit triggered %d attached-root scans, want 0", scans.Load())
	}

	// In-root credential edit refreshes screening before next outbound call without leaking plaintext.
	clearScreenPhases(&phaseMu, &phases)
	testutil.FailErr(t, "rotate env", os.WriteFile(filepath.Join(root, "sub/.env"), []byte("API_KEY="+secretV2+"\n"), 0o600))
	ep3 := repochange.Advance(root)
	files.index.changed(repochange.Event{
		ProjectDir: root, Kind: repochange.WorktreeChanged, Paths: []string{"sub/.env"},
		EpochBootID: ep3.BootID, WorktreeEpoch: ep3.Value,
	})
	testutil.FailErr(t, "await rotated env", files.Await(t.Context(), tools.CredentialWait{ProjectID: "proj-1"}))
	vals := files.harvest.FileValues("proj-1")
	var sawActiveV2, sawRetiredV1 bool
	for _, v := range vals {
		sawActiveV2 = sawActiveV2 || (v.Secret() == secretV2 && !v.Retired)
		sawRetiredV1 = sawRetiredV1 || (v.Secret() == secretV1 && v.Retired)
	}
	if !sawActiveV2 || !sawRetiredV1 {
		t.Fatalf("expected active v2 and retired v1 in harvest, got %+v", vals)
	}

	// Empty-paths worktree event and coverage expiry reconcile with a full build.
	clearScreenPhases(&phaseMu, &phases)
	ep4 := repochange.Advance(root)
	files.index.changed(repochange.Event{
		ProjectDir: root, Kind: repochange.WorktreeChanged, Paths: nil,
		EpochBootID: ep4.BootID, WorktreeEpoch: ep4.Value,
	})
	testutil.FailErr(t, "await malformed event", files.Await(t.Context(), tools.CredentialWait{ProjectID: "proj-1"}))
	if got := filterScreenPhases(&phaseMu, &phases, "credential_build"); len(got) != 1 || got[0].Mode != "full" {
		t.Fatalf("malformed event builds = %+v, want 1 full build", got)
	}

	clearScreenPhases(&phaseMu, &phases)
	completeWatch = false
	nowMu.Lock()
	currentTime = currentTime.Add(repochange.CoverageRevalidationInterval + time.Second)
	nowMu.Unlock()
	testutil.FailErr(t, "await coverage expiry", files.Await(t.Context(), tools.CredentialWait{ProjectID: "proj-1"}))
	if got := filterScreenPhases(&phaseMu, &phases, "credential_build"); len(got) != 1 || got[0].Mode != "full" {
		t.Fatalf("coverage expiry builds = %+v, want 1 full build", got)
	}

	// Ready unrelated project progresses without blocking while proj-1's root is held on an epoch gap.
	err = files.queries.CreateProjectRoot(t.Context(), db.CreateProjectRootParams{
		ID: "r-other", ProjectID: "proj-2", Path: t.TempDir(), Label: "other", IsPrimary: 1,
		AddedAt: "2026-01-01T00:00:00Z", Kind: "attached",
	})
	if err != nil {
		// Seed proj-2 row first if foreign key requires it.
		testutil.FailErr(t, "insert proj-2", seedSecondProject(t, files))
		testutil.FailErr(t, "attach proj-2 root", files.queries.CreateProjectRoot(t.Context(), db.CreateProjectRootParams{
			ID: "r-other", ProjectID: "proj-2", Path: t.TempDir(), Label: "other", IsPrimary: 1,
			AddedAt: "2026-01-01T00:00:00Z", Kind: "attached",
		}))
	}
	completeWatch = true
	testutil.FailErr(t, "warm proj-2", files.Await(t.Context(), tools.CredentialWait{ProjectID: "proj-2"}))

	holdProj1 := make(chan struct{})
	inProj1 := make(chan struct{})
	var p1Once sync.Once
	files.index.onDir = func(rPath, _ string) {
		if rPath == root {
			p1Once.Do(func() {
				close(inProj1)
				<-holdProj1
			})
		}
	}
	_ = repochange.Advance(root)
	epGap := repochange.Advance(root)
	files.index.changed(repochange.Event{
		ProjectDir: root, Kind: repochange.WorktreeChanged, Paths: []string{"sub/.env"},
		EpochBootID: epGap.BootID, WorktreeEpoch: epGap.Value,
	})
	<-inProj1
	testutil.FailErr(t, "proj-2 progresses while proj-1 held", files.Await(t.Context(), tools.CredentialWait{ProjectID: "proj-2"}))
	close(holdProj1)
	files.index.onDir = nil
	testutil.FailErr(t, "proj-1 settles epoch gap", files.Await(t.Context(), tools.CredentialWait{ProjectID: "proj-1"}))

	phaseMu.Lock()
	raw, mErr := json.Marshal(phases)
	phaseMu.Unlock()
	testutil.FailErr(t, "marshal phases", mErr)
	if strings.Contains(string(raw), secretV1) || strings.Contains(string(raw), secretV2) {
		t.Fatalf("phase diagnostics leaked secret plaintext: %s", string(raw))
	}
}

func seedSecondProject(t *testing.T, files *credentialFiles) error {
	t.Helper()
	return files.queries.CreateProject(t.Context(), db.CreateProjectParams{
		ID: "proj-2", Name: sql.NullString{String: "P2", Valid: true},
		LastOpenedAt: "2026-01-01T00:00:00Z", CreatedAt: "2026-01-01T00:00:00Z",
	})
}

func filterScreenPhases(mu *sync.Mutex, phases *[]secretmatch.ScreenPhaseRecord, want string) []secretmatch.ScreenPhaseRecord {
	mu.Lock()
	defer mu.Unlock()
	var out []secretmatch.ScreenPhaseRecord
	for _, p := range *phases {
		if p.Phase == want {
			out = append(out, p)
		}
	}
	return out
}

func clearScreenPhases(mu *sync.Mutex, phases *[]secretmatch.ScreenPhaseRecord) {
	mu.Lock()
	*phases = nil
	mu.Unlock()
}
