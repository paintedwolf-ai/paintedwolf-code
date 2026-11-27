package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func openWW05WatchStore(t *testing.T, start time.Time) (*Store, *ww02ManualClock, db.Handle, Owner) {
	t.Helper()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session-1", testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, database, "session-2", testdbseed.DefaultProjectID)
	clk := newWW02ManualClock(start)
	store := NewStore(database, clk)
	owner := Owner{ProjectID: testdbseed.DefaultProjectID, SessionID: "session-1"}
	return store, clk, database, owner
}

func runGitCLI(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=WW05 Test",
		"GIT_AUTHOR_EMAIL=ww05@example.invalid",
		"GIT_COMMITTER_NAME=WW05 Test",
		"GIT_COMMITTER_EMAIL=ww05@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func TestWW05_C14_ProcessOutputCursorBurstWrapAndHostTrailer(t *testing.T) {
	base := time.Date(2026, 11, 27, 14, 0, 0, 0, time.UTC)
	store, _, _, owner := openWW05WatchStore(t, base)

	cfg := bgprocess.DefaultConfig()
	cfg.RingBufferBytes = 64
	cfg.MaxRecent = 1
	reg := bgprocess.NewRegistry(cfg, bgprocess.Hooks{})
	reg.Output.SetCaptureProjector(captureprojection.New(&secretmatch.Matcher{}, nil))
	t.Cleanup(func() { _ = reg.Lifecycle.Close(context.Background()) })

	store.SetProcessOutputLookup(func(ctx context.Context, sessionID, handle string, since int64) (bgprocess.ProcessOutputState, error) {
		return reg.Output.InspectProcessOutput(ctx, sessionID, handle, since)
	})
	reg.Output.SetWatchOutputHook(t.Context(), func(ctx context.Context, sessionID, handle string) {
		_ = store.NotifyProcessOutput(ctx, sessionID, handle)
	})

	hostWriteCh := make(chan func([]byte), 1)
	procWriteCh := make(chan func([]byte), 1)
	releaseJob := make(chan struct{})
	h1, err := reg.StartProgram(t.Context(), bgprocess.ProgramJobSpec{
		SessionID:   owner.SessionID,
		ProjectID:   owner.ProjectID,
		OriginTool:  "command",
		CommandLine: "ww05-c14-producer",
		Run: func(_ context.Context, stdout, stderr io.Writer) (bgprocess.ProgramJobResult, error) {
			type hostWriter interface{ WriteHost([]byte) (int, error) }
			hostWriteCh <- func(b []byte) { _, _ = stderr.(hostWriter).WriteHost(b) }
			procWriteCh <- func(b []byte) { _, _ = stdout.Write(b) }
			<-releaseJob
			return bgprocess.ProgramJobResult{ExitCode: 0}, nil
		},
	})
	testutil.FailErr(t, "start c14 producer", err)
	writeHost := <-hostWriteCh
	writeProc := <-procWriteCh

	// 1. Pre-registration bytes: default watch skips pre-reg bytes; cursor=0 catches up immediately.
	preReg := "pre-reg-line-0\n"
	writeProc([]byte(preReg))

	wDefault, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: "process_output", Handle: h1}},
	})
	testutil.FailErr(t, "register default process_output watch", err)
	if wDefault.PendingMatches != 0 {
		t.Fatalf("default watch without cursor matched pre-registration bytes: %+v", wDefault)
	}

	zero := int64(0)
	wPreReg, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Conditions: []awaitstore.Condition{{Kind: "process_output", Handle: h1, Cursor: &zero}},
	})
	testutil.FailErr(t, "register cursor=0 catch-up watch", err)
	if wPreReg.PendingMatches != 1 {
		t.Fatalf("cursor=0 watch missed pre-registration bytes: %+v", wPreReg)
	}
	vPreReg, err := store.Inspect(t.Context(), owner, wPreReg.Handle)
	testutil.FailErr(t, "inspect cursor=0 catch-up watch", err)
	var preEv map[string]any
	testutil.FailErr(t, "decode pre-reg evidence", json.Unmarshal(vPreReg.LatestMatch.Evidence, &preEv))
	if int64(preEv["from_cursor"].(float64)) != 0 || int64(preEv["end_cursor"].(float64)) != int64(len(preReg)) {
		t.Fatalf("unexpected pre-reg range: %+v", preEv)
	}

	// 2. Host-origin trailer isolation (C14-host-trailer-isolation):
	// Identical bytes written via WriteHost advance legacy command_output offsets without firing process_output.
	trailer := `{"status":0,"state":"completed"}` + "\n"
	writeHost([]byte(trailer))
	cmdSnapAfterHost, err := reg.Output.ReadCommandOutput(t.Context(), owner.SessionID, h1, 0)
	testutil.FailErr(t, "read command_output after WriteHost", err)
	wantAfterHost := int64(len(preReg) + len(trailer))
	if cmdSnapAfterHost.Output.Next != wantAfterHost {
		t.Fatalf("legacy command_output Next = %d, want %d", cmdSnapAfterHost.Output.Next, wantAfterHost)
	}
	if vAfterHost, _ := store.Inspect(t.Context(), owner, wDefault.Handle); vAfterHost.PendingMatches != 0 {
		t.Fatalf("WriteHost trailer erroneously fired process_output watch: %+v", vAfterHost)
	}

	// 3. Identical bytes written by process + rapid burst coalesce into ONE unread pending match.
	writeProc([]byte(trailer))
	writeProc([]byte("burst-A\n"))
	writeProc([]byte("burst-B\n"))
	writeProc([]byte("burst-C\n"))

	cmdSnapAfterBurst, err := reg.Output.ReadCommandOutput(t.Context(), owner.SessionID, h1, 0)
	testutil.FailErr(t, "read command_output after burst", err)
	vBurst, err := store.Inspect(t.Context(), owner, wDefault.Handle)
	testutil.FailErr(t, "inspect coalesced burst", err)
	if vBurst.PendingMatches != 1 || vBurst.LatestMatch == nil {
		t.Fatalf("expected 1 coalesced unread burst match, got %+v", vBurst)
	}
	var burstEv map[string]any
	testutil.FailErr(t, "decode burst evidence", json.Unmarshal(vBurst.LatestMatch.Evidence, &burstEv))
	fromCur := int64(burstEv["from_cursor"].(float64))
	endCur := int64(burstEv["end_cursor"].(float64))
	if fromCur != int64(len(preReg)) || endCur != cmdSnapAfterBurst.Output.Next {
		t.Fatalf("coalesced burst range [%d, %d] != expected [%d, %d]", fromCur, endCur, len(preReg), cmdSnapAfterBurst.Output.Next)
	}

	// Collect and commit burst before testing retention wrap.
	b1, has1, err := ReserveCollectBatch(t.Context(), store, owner, "req:c14:burst")
	testutil.FailErr(t, "collect c14 burst", err)
	if !has1 {
		t.Fatal("expected collected burst batch")
	}
	testutil.FailErr(t, "commit c14 burst", CommitCollectBatch(t.Context(), store, owner.SessionID, b1.DeliveryID))

	// 4. Retention wrap (> 64-byte ring buffer) -> explicit truncated gap verified against independent reader.
	writeProc([]byte(strings.Repeat("W", 128)))
	indepState, err := reg.Output.InspectProcessOutput(t.Context(), owner.SessionID, h1, 0)
	testutil.FailErr(t, "independent inspect after wrap", err)
	if !indepState.Truncated {
		t.Fatalf("expected independent reader truncated=true after ring wrap, got %+v", indepState)
	}

	wWrap, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Conditions: []awaitstore.Condition{{Kind: "process_output", Handle: h1, Cursor: &zero}},
	})
	testutil.FailErr(t, "register wrap catch-up watch", err)
	vWrap, err := store.Inspect(t.Context(), owner, wWrap.Handle)
	testutil.FailErr(t, "inspect wrap catch-up watch", err)
	if vWrap.PendingMatches != 1 || vWrap.LatestMatch == nil {
		t.Fatalf("expected 1 match on wrap catch-up watch, got %+v", vWrap)
	}
	var wrapEv map[string]any
	testutil.FailErr(t, "decode wrap evidence", json.Unmarshal(vWrap.LatestMatch.Evidence, &wrapEv))
	if wrapEv["truncated"] != true || int64(wrapEv["end_cursor"].(float64)) != indepState.ProcessEndCursor {
		t.Fatalf("wrap evidence mismatch against reader: ev=%+v reader=%+v", wrapEv, indepState)
	}

	// Drain wDefault pending wrap match before testing silent exit.
	b2, has2, err := ReserveCollectBatch(t.Context(), store, owner, "req:c14:wrap")
	testutil.FailErr(t, "collect c14 wrap", err)
	if !has2 {
		t.Fatal("expected collected wrap batch")
	}
	testutil.FailErr(t, "commit c14 wrap", CommitCollectBatch(t.Context(), store, owner.SessionID, b2.DeliveryID))

	// 5. Silent exit completes live output watch cleanly without emitting an empty match.
	close(releaseJob)
	_, _, _ = reg.Lifecycle.Wait(t.Context(), owner.SessionID, h1)
	testutil.WaitFor(t, "silent exit completes wDefault", func() bool {
		v, iErr := store.Inspect(t.Context(), owner, wDefault.Handle)
		return iErr == nil && v.State == StateTerminal && v.Outcome == OutcomeCompleted
	})
	vFinal, err := store.Inspect(t.Context(), owner, wDefault.Handle)
	testutil.FailErr(t, "inspect wDefault after silent exit", err)
	if vFinal.PendingMatches != 0 {
		t.Fatalf("silent exit should not emit a spurious pending match: %+v", vFinal)
	}

	writeWW02Evidence(t, filepath.Join(t.TempDir(), "c14-host-trailer-isolation.json"), ww02EvidenceBundle{
		SchemaVersion: 1,
		RunID:         "ww05-c14-inprocess",
		Lane:          "in-process",
		ClockDomain:   "manual",
		CaseID:        "C14-host-trailer-isolation",
		Expectations:  map[string]any{"host_trailer_ignored": true, "legacy_offsets_unchanged": true, "truncated_gap": true},
		Observers: map[string]any{
			"source":         map[string]any{"handle": h1, "end_cursor": indepState.ProcessEndCursor, "truncated": indepState.Truncated},
			"storage":        map[string]any{"available": true, "final_state": vFinal.State, "final_outcome": vFinal.Outcome},
			"admission":      map[string]any{"observer": "admission", "clock_domain": "manual"},
			"rendered_input": map[string]any{"observer": "rendered_input", "clock_domain": "manual"},
		},
	})
}

func TestWW05_C15_FileAttributionIncludeOwn(t *testing.T) {
	base := time.Date(2026, 11, 27, 14, 10, 0, 0, time.UTC)
	store, clk, database, owner := openWW05WatchStore(t, base)

	trunkDir := t.TempDir()
	testutil.FailErr(t, "seed app.ts", os.WriteFile(filepath.Join(trunkDir, "app.ts"), []byte("v1\n"), 0o644))
	rootID := testdbseed.InsertProjectRootWithID(t, database, testdbseed.DefaultProjectID, "root-c15", trunkDir)
	ownerPersonID := testdbseed.OwnerID(t, database)
	ledger := sourceledger.New(database, t.TempDir(), sourceledger.Timing{})
	testutil.FailErr(t, "seed initial create", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID:      testdbseed.DefaultProjectID,
		RecordLocation: sourceledger.RecordLocation{RootID: rootID, Path: "app.ts"},
		Op:             api.SourceChangeOpCreate,
		Origin:         api.SourceChangeOriginUser,
		PersonID:       ownerPersonID,
		Cause:          "user_save",
		CaptureQuality: "exact",
		After:          []byte("v1\n"),
	}))

	auth := Authority{
		ProjectDir: trunkDir,
		Roots:      []WatchRootGrant{{Label: "workspace", RootID: rootID, Path: trunkDir}},
	}

	wDefault, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Authority:  auth,
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: "file_change", Root: "workspace", Path: ".", Glob: "**/*.ts"}},
	})
	testutil.FailErr(t, "register default file watch", err)

	wIncludeOwn, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Authority:  auth,
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: "file_change", Root: "workspace", Path: "app.ts", IncludeOwn: true}},
	})
	testutil.FailErr(t, "register include_own file watch", err)

	// 1. Direct edit by watching agent: excluded from wDefault, matched on wIncludeOwn with actor_class="you".
	testutil.FailErr(t, "write own edit", os.WriteFile(filepath.Join(trunkDir, "app.ts"), []byte("v2-own\n"), 0o644))
	effOwn := recordAndFetchLatestEffectID(t, database, ledger, sourceledger.RecordInput{
		ProjectID:      testdbseed.DefaultProjectID,
		RecordLocation: sourceledger.RecordLocation{RootID: rootID, Path: "app.ts"},
		Op:             api.SourceChangeOpWrite,
		Origin:         api.SourceChangeOriginAgent,
		SessionID:      owner.SessionID,
		Turn:           1,
		Cause:          "agent_edit",
		CaptureQuality: "exact",
		Before:         []byte("v1\n"),
		After:          []byte("v2-own\n"),
	})
	testutil.FailErr(t, "reconcile own edit", store.NotifyWorkspaceChange(t.Context(), testdbseed.DefaultProjectID, trunkDir))

	if vDef, _ := store.Inspect(t.Context(), owner, wDefault.Handle); vDef.PendingMatches != 0 {
		t.Fatalf("own direct edit matched default watch (include_own=false): %+v", vDef)
	}
	vOwn, _ := store.Inspect(t.Context(), owner, wIncludeOwn.Handle)
	if vOwn.PendingMatches != 1 || vOwn.LatestMatch == nil {
		t.Fatalf("include_own watch missed own edit: %+v", vOwn)
	}
	var ownEv map[string]any
	testutil.FailErr(t, "decode own evidence", json.Unmarshal(vOwn.LatestMatch.Evidence, &ownEv))
	if ownEv["effect_id"] != effOwn || ownEv["actor_class"] != "you" {
		t.Fatalf("unexpected own attribution: %+v", ownEv)
	}

	// 2. Program started by watching agent (Origin=External, Cause=CauseCommandWindow).
	testutil.FailErr(t, "write program edit", os.WriteFile(filepath.Join(trunkDir, "app.ts"), []byte("v3-prog\n"), 0o644))
	effProg := recordAndFetchLatestEffectID(t, database, ledger, sourceledger.RecordInput{
		ProjectID:      testdbseed.DefaultProjectID,
		RecordLocation: sourceledger.RecordLocation{RootID: rootID, Path: "app.ts"},
		Op:             api.SourceChangeOpWrite,
		Origin:         api.SourceChangeOriginExternal,
		SessionID:      owner.SessionID,
		Cause:          sourceledger.CauseCommandWindow,
		CaptureQuality: "observed",
		Before:         []byte("v2-own\n"),
		After:          []byte("v3-prog\n"),
	})
	testutil.FailErr(t, "reconcile program edit", store.NotifyWorkspaceChange(t.Context(), testdbseed.DefaultProjectID, trunkDir))

	// 3. Other session edit (Origin=Agent, SessionID="session-2").
	testutil.FailErr(t, "write other session edit", os.WriteFile(filepath.Join(trunkDir, "app.ts"), []byte("v4-other-sess\n"), 0o644))
	effOtherSess := recordAndFetchLatestEffectID(t, database, ledger, sourceledger.RecordInput{
		ProjectID:      testdbseed.DefaultProjectID,
		RecordLocation: sourceledger.RecordLocation{RootID: rootID, Path: "app.ts"},
		Op:             api.SourceChangeOpWrite,
		Origin:         api.SourceChangeOriginAgent,
		SessionID:      "session-2",
		Turn:           1,
		Cause:          "agent_edit",
		CaptureQuality: "exact",
		Before:         []byte("v3-prog\n"),
		After:          []byte("v4-other-sess\n"),
	})
	testutil.FailErr(t, "reconcile other session edit", store.NotifyWorkspaceChange(t.Context(), testdbseed.DefaultProjectID, trunkDir))

	// 4. Merged worker overlay (Cause=CauseOverlayPromote).
	testutil.FailErr(t, "write merged overlay edit", os.WriteFile(filepath.Join(trunkDir, "app.ts"), []byte("v5-overlay\n"), 0o644))
	effOverlay := recordAndFetchLatestEffectID(t, database, ledger, sourceledger.RecordInput{
		ProjectID:      testdbseed.DefaultProjectID,
		RecordLocation: sourceledger.RecordLocation{RootID: rootID, Path: "app.ts"},
		Op:             api.SourceChangeOpWrite,
		Origin:         api.SourceChangeOriginAgent,
		SessionID:      owner.SessionID,
		JobID:          "worker-job-c15",
		Turn:           2,
		Cause:          sourceledger.CauseOverlayPromote,
		CaptureQuality: "exact",
		Before:         []byte("v4-other-sess\n"),
		After:          []byte("v5-overlay\n"),
	})
	testutil.FailErr(t, "reconcile merged overlay", store.NotifyWorkspaceChange(t.Context(), testdbseed.DefaultProjectID, trunkDir))

	// 5. Human edit (Origin=User).
	testutil.FailErr(t, "write human edit", os.WriteFile(filepath.Join(trunkDir, "app.ts"), []byte("v6-human\n"), 0o644))
	effHuman := recordAndFetchLatestEffectID(t, database, ledger, sourceledger.RecordInput{
		ProjectID:      testdbseed.DefaultProjectID,
		RecordLocation: sourceledger.RecordLocation{RootID: rootID, Path: "app.ts"},
		Op:             api.SourceChangeOpWrite,
		Origin:         api.SourceChangeOriginUser,
		PersonID:       ownerPersonID,
		Cause:          "user_save",
		CaptureQuality: "exact",
		Before:         []byte("v5-overlay\n"),
		After:          []byte("v6-human\n"),
	})
	testutil.FailErr(t, "reconcile human edit", store.NotifyWorkspaceChange(t.Context(), testdbseed.DefaultProjectID, trunkDir))

	// 6. Unattributed disk write (no ledger entry, reconciled via sampler with zero UI readers).
	testutil.FailErr(t, "write unattributed file", os.WriteFile(filepath.Join(trunkDir, "unattr.ts"), []byte("raw-disk\n"), 0o644))
	clk.Advance(SamplerMinInterval)
	testutil.FailErr(t, "advance sampler for unattributed edit", store.AdvanceDue(t.Context()))

	batch, hasBatch, err := ReserveCollectBatch(t.Context(), store, owner, "req:c15:actors")
	testutil.FailErr(t, "collect c15 attribution batch", err)
	if !hasBatch {
		t.Fatal("expected collected attribution batch")
	}
	var defMatches []map[string]any
	for _, m := range batch.Matches {
		if m.Handle != wDefault.Handle {
			continue
		}
		var ev map[string]any
		testutil.FailErr(t, "decode match evidence", json.Unmarshal(m.Evidence, &ev))
		defMatches = append(defMatches, ev)
	}
	if len(defMatches) != 5 {
		t.Fatalf("expected 5 non-own matches (program, other-session, overlay, human, unattributed), got %d: %+v", len(defMatches), defMatches)
	}

	assertActor := func(idx int, wantEffectID, wantActor, wantPath string) {
		t.Helper()
		ev := defMatches[idx]
		if wantEffectID != "" && ev["effect_id"] != wantEffectID {
			t.Fatalf("match[%d] effect_id=%v, want %s (ev=%+v)", idx, ev["effect_id"], wantEffectID, ev)
		}
		if ev["actor_class"] != wantActor {
			t.Fatalf("match[%d] actor_class=%v, want %s (ev=%+v)", idx, ev["actor_class"], wantActor, ev)
		}
		if ev["path"] != wantPath {
			t.Fatalf("match[%d] path=%v, want %s (ev=%+v)", idx, ev["path"], wantPath, ev)
		}
	}
	assertActor(0, effProg, "external", "app.ts")
	assertActor(1, effOtherSess, "agent", "app.ts")
	assertActor(2, effOverlay, "you", "app.ts")
	assertActor(3, effHuman, "user", "app.ts")
	assertActor(4, "", "unattributed", "unattr.ts")
}

func TestWW05_C15_OverlayGlobSymlinkNoUI(t *testing.T) {
	base := time.Date(2026, 11, 27, 14, 20, 0, 0, time.UTC)
	store, clk, database, coordOwner := openWW05WatchStore(t, base)

	trunkDir := t.TempDir()
	overlayDir := t.TempDir()
	outsideDir := t.TempDir()
	testutil.FailErr(t, "mkdir trunk src/dist", os.MkdirAll(filepath.Join(trunkDir, "src"), 0o755))
	testutil.FailErr(t, "mkdir trunk dist", os.MkdirAll(filepath.Join(trunkDir, "dist"), 0o755))
	testutil.FailErr(t, "mkdir overlay dist", os.MkdirAll(filepath.Join(overlayDir, "dist"), 0o755))

	trunkAuth := Authority{
		ProjectDir: trunkDir,
		Roots:      []WatchRootGrant{{Label: "workspace", RootID: "root-main", Path: trunkDir}},
	}

	// 1. Create, rename, delete, reappear with zero UI readers via sampler reconciliation.
	wLife, err := store.Register(t.Context(), RegisterRequest{
		Owner:      coordOwner,
		Authority:  trunkAuth,
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: "file_change", Root: "workspace", Path: "src", Glob: "**/*.go"}},
	})
	testutil.FailErr(t, "register lifecycle file watch", err)

	aPath := filepath.Join(trunkDir, "src", "a.go")
	bPath := filepath.Join(trunkDir, "src", "b.go")
	testutil.FailErr(t, "create a.go", os.WriteFile(aPath, []byte("package a\n"), 0o644))
	clk.Advance(SamplerMinInterval)
	testutil.FailErr(t, "reconcile create", store.AdvanceDue(t.Context()))

	testutil.FailErr(t, "rename a.go -> b.go", os.Rename(aPath, bPath))
	clk.Advance(SamplerMinInterval)
	testutil.FailErr(t, "reconcile rename", store.AdvanceDue(t.Context()))

	testutil.FailErr(t, "delete b.go", os.Remove(bPath))
	clk.Advance(SamplerMinInterval)
	testutil.FailErr(t, "reconcile delete", store.AdvanceDue(t.Context()))

	testutil.FailErr(t, "reappear b.go", os.WriteFile(bPath, []byte("package b\n"), 0o644))
	clk.Advance(SamplerMinInterval)
	testutil.FailErr(t, "reconcile reappear", store.AdvanceDue(t.Context()))

	lifeBatch, hasLife, err := ReserveCollectBatch(t.Context(), store, coordOwner, "req:c15:life")
	testutil.FailErr(t, "collect lifecycle batch", err)
	if !hasLife || len(lifeBatch.Matches) != 4 {
		t.Fatalf("expected 4 lifecycle matches (create, rename, delete, reappear), got %d", len(lifeBatch.Matches))
	}
	wantOps := []struct {
		op, path, from string
	}{
		{"create", "src/a.go", ""},
		{"rename", "src/b.go", "src/a.go"},
		{"delete", "src/b.go", ""},
		{"create", "src/b.go", ""},
	}
	for i, w := range wantOps {
		var ev map[string]any
		testutil.FailErr(t, "decode life match", json.Unmarshal(lifeBatch.Matches[i].Evidence, &ev))
		if ev["op"] != w.op || ev["path"] != w.path || (w.from != "" && ev["from_path"] != w.from) {
			t.Fatalf("lifecycle[%d] got %+v, want %+v", i, ev, w)
		}
	}
	testutil.FailErr(t, "commit lifecycle batch", CommitCollectBatch(t.Context(), store, coordOwner.SessionID, lifeBatch.DeliveryID))

	// 2. Glob overflow (>256 characters) rejected with typed WATCH_CONDITION_INVALID.
	longGlob := strings.Repeat("a/", 140) + "*.go"
	rootsRef := []projectroot.RootRef{{Label: "workspace", Path: trunkDir}}
	_, globErr := parseFileChangeCondition("conditions.0", map[string]any{
		"root": "workspace",
		"path": "src",
		"glob": longGlob,
	}, rootsRef)
	var valErr *ValidationError
	if !errors.As(globErr, &valErr) || valErr.Code != "WATCH_CONDITION_INVALID" {
		t.Fatalf("expected WATCH_CONDITION_INVALID for glob overflow (>256 chars), got %v", globErr)
	}

	// 3. Symlink boundary: escape selector rejected AND directory watch ignores escaping symlink target writes.
	outsideSecret := filepath.Join(outsideDir, "outside.go")
	testutil.FailErr(t, "write outside file", os.WriteFile(outsideSecret, []byte("secret-v1\n"), 0o644))
	testutil.FailErr(t, "create escape symlink", os.Symlink(outsideSecret, filepath.Join(trunkDir, "src", "escape.go")))
	if _, escErr := store.Register(t.Context(), RegisterRequest{
		Owner:      coordOwner,
		Authority:  trunkAuth,
		Conditions: []awaitstore.Condition{{Kind: "file_change", Root: "workspace", Path: "src/escape.go"}},
	}); escErr == nil {
		t.Fatal("expected symlink escape selector to be rejected")
	}
	// Mutating outside target must never trigger the directory watch.
	testutil.FailErr(t, "mutate outside file", os.WriteFile(outsideSecret, []byte("secret-v2\n"), 0o644))
	clk.Advance(SamplerMaxInterval)
	testutil.FailErr(t, "reconcile after outside symlink write", store.AdvanceDue(t.Context()))
	if vLife, _ := store.Inspect(t.Context(), coordOwner, wLife.Handle); vLife.PendingMatches != 0 {
		t.Fatalf("outside symlink mutation leaked into workspace watch: %+v", vLife)
	}

	// 4. Private worker overlay isolation with identical trunk path (dist/bundle.js) and zero UI readers.
	testdbseed.InsertSession(t, database, "worker-c15", testdbseed.DefaultProjectID)
	_, err = database.ExecContext(t.Context(), `INSERT INTO worker_jobs
		(id, project_id, parent_session_id, child_session_id, agent_type, prompt, brief, status, created_at)
		VALUES ('job-c15', ?, 'session-1', 'worker-c15', 'implementer', 'work', 'work', 'running', '2026-11-27T14:20:00Z')`,
		testdbseed.DefaultProjectID)
	testutil.FailErr(t, "seed worker job-c15", err)
	workerOwner := JobOwner("worker-c15", "job-c15")
	workerOwner.ProjectID = testdbseed.DefaultProjectID
	workerAuth := Authority{
		ProjectDir: overlayDir,
		Roots: []WatchRootGrant{{
			Label:         "workspace",
			RootID:        "root-main",
			Path:          overlayDir,
			BranchID:      "branch-job-c15",
			PrivateBranch: true,
		}},
	}

	wTrunkDist, err := store.Register(t.Context(), RegisterRequest{
		Owner:      coordOwner,
		Authority:  trunkAuth,
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: "file_change", Root: "workspace", Path: "dist"}},
	})
	testutil.FailErr(t, "register trunk dist watch", err)
	wOverlayDist, err := store.Register(t.Context(), RegisterRequest{
		Owner:      workerOwner,
		Authority:  workerAuth,
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: "file_change", Root: "workspace", Path: "dist"}},
	})
	testutil.FailErr(t, "register overlay dist watch", err)

	// Write only in worker overlay -> worker matches, trunk stays 0.
	testutil.FailErr(t, "write overlay bundle", os.WriteFile(filepath.Join(overlayDir, "dist", "bundle.js"), []byte("overlay-v1\n"), 0o644))
	clk.Advance(SamplerMinInterval)
	testutil.FailErr(t, "reconcile overlay write", store.AdvanceDue(t.Context()))
	if vT, _ := store.Inspect(t.Context(), coordOwner, wTrunkDist.Handle); vT.PendingMatches != 0 {
		t.Fatalf("worker overlay leaked into trunk watch: %+v", vT)
	}
	if vO, _ := store.Inspect(t.Context(), workerOwner, wOverlayDist.Handle); vO.PendingMatches != 1 {
		t.Fatalf("worker overlay watch missed overlay write: %+v", vO)
	}

	// Write only in trunk -> trunk matches, worker stays at 1 (no trunk leak into private overlay).
	testutil.FailErr(t, "write trunk bundle", os.WriteFile(filepath.Join(trunkDir, "dist", "bundle.js"), []byte("trunk-v1\n"), 0o644))
	clk.Advance(SamplerMaxInterval)
	testutil.FailErr(t, "reconcile trunk write", store.AdvanceDue(t.Context()))
	if vT, _ := store.Inspect(t.Context(), coordOwner, wTrunkDist.Handle); vT.PendingMatches != 1 {
		t.Fatalf("trunk watch missed trunk write: %+v", vT)
	}
	if vO, _ := store.Inspect(t.Context(), workerOwner, wOverlayDist.Handle); vO.PendingMatches != 1 {
		t.Fatalf("trunk write leaked into private worker overlay watch: %+v", vO)
	}
}

func TestWW05_C16_GitLoosePackedWorktree(t *testing.T) {
	base := time.Date(2026, 11, 27, 14, 30, 0, 0, time.UTC)
	store, clk, database, owner := openWW05WatchStore(t, base)

	mainRepo := t.TempDir()
	runGitCLI(t, mainRepo, "init", "-b", "main")
	testutil.FailErr(t, "write readme", os.WriteFile(filepath.Join(mainRepo, "README.md"), []byte("init\n"), 0o644))
	runGitCLI(t, mainRepo, "add", "README.md")
	runGitCLI(t, mainRepo, "commit", "-m", "initial")
	oid1 := runGitCLI(t, mainRepo, "rev-parse", "refs/heads/main")

	// Create a real linked worktree for worktree-local ref isolation testing.
	wtParent := t.TempDir()
	wtDir := filepath.Join(wtParent, "wt-1")
	runGitCLI(t, mainRepo, "worktree", "add", "-b", "wt-branch", wtDir)

	mainRootID := testdbseed.InsertProjectRootWithID(t, database, testdbseed.DefaultProjectID, "root-main-git", mainRepo)
	mainAuth := Authority{
		ProjectDir: mainRepo,
		Roots:      []WatchRootGrant{{Label: "mainrepo", RootID: mainRootID, Path: mainRepo}},
	}
	wtAuth := Authority{
		ProjectDir: wtDir,
		Roots:      []WatchRootGrant{{Label: "wtrepo", RootID: "root-wt-git", Path: wtDir, BranchID: "wt-1"}},
	}

	wMain, err := store.Register(t.Context(), RegisterRequest{
		Owner:     owner,
		Authority: mainAuth,
		Repeat:    true,
		Conditions: []awaitstore.Condition{
			{Kind: "git_ref", Root: "mainrepo", Ref: "refs/heads/main"},
			{Kind: "git_ref", Root: "mainrepo", Ref: "refs/heads/ephemeral"},
			{Kind: "git_ref", Root: "mainrepo", Ref: "refs/worktree/local-mark"},
		},
	})
	testutil.FailErr(t, "register mainrepo git_ref watch", err)
	if wMain.PendingMatches != 0 {
		t.Fatalf("baseline git_ref registration must not match immediately: %+v", wMain)
	}

	wWT, err := store.Register(t.Context(), RegisterRequest{
		Owner:     owner,
		Authority: wtAuth,
		Repeat:    true,
		Conditions: []awaitstore.Condition{
			{Kind: "git_ref", Root: "wtrepo", Ref: "refs/worktree/local-mark"},
		},
	})
	testutil.FailErr(t, "register wtrepo git_ref watch", err)

	// 1. Loose ref update on refs/heads/main -> verified against independent git rev-parse.
	testutil.FailErr(t, "write step2", os.WriteFile(filepath.Join(mainRepo, "README.md"), []byte("step2\n"), 0o644))
	runGitCLI(t, mainRepo, "add", "README.md")
	runGitCLI(t, mainRepo, "commit", "-m", "loose commit")
	oid2 := runGitCLI(t, mainRepo, "rev-parse", "refs/heads/main")
	clk.Advance(SamplerMinInterval)
	testutil.FailErr(t, "reconcile loose ref", store.AdvanceDue(t.Context()))

	// 2. Pack refs, then advance packed ref (no loose ref file).
	runGitCLI(t, mainRepo, "pack-refs", "--all", "--prune")
	testutil.FailErr(t, "write step3", os.WriteFile(filepath.Join(mainRepo, "README.md"), []byte("step3\n"), 0o644))
	runGitCLI(t, mainRepo, "add", "README.md")
	runGitCLI(t, mainRepo, "commit", "-m", "packed commit")
	oid3 := runGitCLI(t, mainRepo, "rev-parse", "refs/heads/main")
	runGitCLI(t, mainRepo, "pack-refs", "--all", "--prune")
	if _, statErr := os.Stat(filepath.Join(mainRepo, ".git", "refs", "heads", "main")); !os.IsNotExist(statErr) {
		t.Fatalf("expected refs/heads/main to be packed (no loose file), stat err=%v", statErr)
	}
	clk.Advance(SamplerMinInterval)
	testutil.FailErr(t, "reconcile packed ref", store.AdvanceDue(t.Context()))

	// 3. Create, delete, and recreate refs/heads/ephemeral.
	runGitCLI(t, mainRepo, "update-ref", "refs/heads/ephemeral", oid2)
	clk.Advance(SamplerMinInterval)
	testutil.FailErr(t, "reconcile ephemeral create", store.AdvanceDue(t.Context()))

	runGitCLI(t, mainRepo, "update-ref", "-d", "refs/heads/ephemeral")
	clk.Advance(SamplerMinInterval)
	testutil.FailErr(t, "reconcile ephemeral delete", store.AdvanceDue(t.Context()))

	runGitCLI(t, mainRepo, "update-ref", "refs/heads/ephemeral", oid3)
	clk.Advance(SamplerMinInterval)
	testutil.FailErr(t, "reconcile ephemeral recreate", store.AdvanceDue(t.Context()))

	// 4. Worktree-local ref isolation: mutating refs/worktree/local-mark in wtDir triggers ONLY wWT, not wMain.
	runGitCLI(t, wtDir, "update-ref", "refs/worktree/local-mark", oid3)
	wtOID := runGitCLI(t, wtDir, "rev-parse", "refs/worktree/local-mark")
	if wtOID != oid3 {
		t.Fatalf("unexpected worktree ref oid %s != %s", wtOID, oid3)
	}
	clk.Advance(SamplerMaxInterval)
	testutil.FailErr(t, "reconcile worktree-local ref", store.AdvanceDue(t.Context()))

	// 5. Resync after restart (Recover): advance main while down, recover, reconcile -> truthful before/after OIDs.
	testutil.FailErr(t, "write step4", os.WriteFile(filepath.Join(mainRepo, "README.md"), []byte("step4\n"), 0o644))
	runGitCLI(t, mainRepo, "add", "README.md")
	runGitCLI(t, mainRepo, "commit", "-m", "post-restart resync commit")
	oid4 := runGitCLI(t, mainRepo, "rev-parse", "refs/heads/main")
	testutil.FailErr(t, "recover store for git resync", store.Recover(t.Context()))
	clk.Advance(SamplerMinInterval)
	testutil.FailErr(t, "reconcile after recover", store.AdvanceDue(t.Context()))

	batch, hasBatch, err := ReserveCollectBatch(t.Context(), store, owner, "req:c16:git")
	testutil.FailErr(t, "collect c16 git matches", err)
	if !hasBatch {
		t.Fatal("expected collected git_ref matches")
	}

	var mainEvs, wtEvs []map[string]any
	for _, m := range batch.Matches {
		var ev map[string]any
		testutil.FailErr(t, "decode git evidence", json.Unmarshal(m.Evidence, &ev))
		switch m.Handle {
		case wMain.Handle:
			mainEvs = append(mainEvs, ev)
		case wWT.Handle:
			wtEvs = append(wtEvs, ev)
		}
	}

	// wWT should have exactly 1 match for refs/worktree/local-mark.
	if len(wtEvs) != 1 || wtEvs[0]["ref"] != "refs/worktree/local-mark" || wtEvs[0]["after_oid"] != oid3 {
		t.Fatalf("unexpected worktree-local ref matches: %+v", wtEvs)
	}
	// wMain should NEVER have seen refs/worktree/local-mark, and should have 6 exact transitions:
	// loose (oid1->oid2), packed (oid2->oid3), ephemeral create (->oid2), ephemeral delete (oid2->""), ephemeral recreate (->oid3), resync (oid3->oid4).
	if len(mainEvs) != 6 {
		t.Fatalf("expected 6 mainrepo git_ref transitions, got %d: %+v", len(mainEvs), mainEvs)
	}
	for _, ev := range mainEvs {
		if ev["ref"] == "refs/worktree/local-mark" {
			t.Fatalf("worktree-local ref leaked into mainrepo watch: %+v", ev)
		}
	}
	if mainEvs[0]["before_oid"] != oid1 || mainEvs[0]["after_oid"] != oid2 {
		t.Fatalf("loose transition mismatch: %+v", mainEvs[0])
	}
	if mainEvs[1]["before_oid"] != oid2 || mainEvs[1]["after_oid"] != oid3 {
		t.Fatalf("packed transition mismatch: %+v", mainEvs[1])
	}
	if mainEvs[2]["ref"] != "refs/heads/ephemeral" || mainEvs[2]["after_oid"] != oid2 || mainEvs[2]["deleted"] != false {
		t.Fatalf("ephemeral create mismatch: %+v", mainEvs[2])
	}
	if mainEvs[3]["ref"] != "refs/heads/ephemeral" || mainEvs[3]["before_oid"] != oid2 || mainEvs[3]["deleted"] != true {
		t.Fatalf("ephemeral delete mismatch: %+v", mainEvs[3])
	}
	if mainEvs[4]["ref"] != "refs/heads/ephemeral" || mainEvs[4]["after_oid"] != oid3 || mainEvs[4]["deleted"] != false {
		t.Fatalf("ephemeral recreate mismatch: %+v", mainEvs[4])
	}
	if mainEvs[5]["before_oid"] != oid3 || mainEvs[5]["after_oid"] != oid4 {
		t.Fatalf("post-restart resync mismatch: %+v", mainEvs[5])
	}
}

func TestWW05_C17_TimersInProcess(t *testing.T) {
	// Friday 2026-11-27 13:55:00 UTC (08:55 EST in America/New_York).
	base := time.Date(2026, 11, 27, 13, 55, 0, 0, time.UTC)
	store, clk, _, owner := openWW05WatchStore(t, base)

	// 1. Typed rejection of repeating ambient wake timer.
	_, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Delivery:   DeliveryWake,
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: "timer", DelayMS: 5000}},
	})
	var valErr *ValidationError
	if !errors.As(err, &valErr) || valErr.Code != "WATCH_WAKE_NOT_ALLOWED" {
		t.Fatalf("expected WATCH_WAKE_NOT_ALLOWED for repeating wake timer, got %v", err)
	}

	// 2. Delay timer (one-shot 5s).
	wDelay, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Conditions: []awaitstore.Condition{{Kind: "timer", DelayMS: 5000}},
	})
	testutil.FailErr(t, "register delay timer", err)

	// 3. Absolute `at` timer (at base + 30s).
	atTarget := base.Add(30 * time.Second)
	wAt, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Conditions: []awaitstore.Condition{{Kind: "timer", At: atTarget.Format(time.RFC3339)}},
	})
	testutil.FailErr(t, "register absolute at timer", err)

	// 4. Calendar timer (America/New_York weekdays 09:00 = 14:00:00 UTC = base + 5m).
	wCal, err := store.Register(t.Context(), RegisterRequest{
		Owner:     owner,
		ExpiresAt: base.Add(2 * time.Hour),
		Conditions: []awaitstore.Condition{{
			Kind:     "timer",
			TimeZone: "America/New_York",
			Calendar: &awaitstore.CalendarSpec{Days: []string{"weekdays"}, Times: []string{"09:00"}},
		}},
	})
	testutil.FailErr(t, "register calendar timer", err)

	// 5. Tie with expiry: tick at +60s == ExpiresAt -> expiry wins, 0 matches.
	wTie, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		ExpiresAt:  base.Add(time.Minute),
		Conditions: []awaitstore.Condition{{Kind: "timer", DelayMS: 60000}},
	})
	testutil.FailErr(t, "register expiry-tie timer", err)

	// 6. Anchored repeating timer (10s period, drift-free).
	wRep, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Repeat:     true,
		ExpiresAt:  base.Add(2 * time.Hour),
		Conditions: []awaitstore.Condition{{Kind: "timer", DelayMS: 10000}},
	})
	testutil.FailErr(t, "register anchored repeating timer", err)

	// Verify delay timer fires at exact 5s boundary (not at 4.999s).
	clk.Advance(4999 * time.Millisecond)
	testutil.FailErr(t, "advance 4999ms", store.AdvanceDue(t.Context()))
	if v, _ := store.Inspect(t.Context(), owner, wDelay.Handle); v.PendingMatches != 0 {
		t.Fatalf("delay timer fired early: %+v", v)
	}
	clk.Advance(1 * time.Millisecond)
	testutil.FailErr(t, "advance to 5000ms", store.AdvanceDue(t.Context()))
	if v, _ := store.Inspect(t.Context(), owner, wDelay.Handle); v.PendingMatches != 1 || v.State != StateTerminal {
		t.Fatalf("delay timer did not complete at 5000ms: %+v", v)
	}

	// Verify anchored repeat has zero drift: advance to 10.370s, collect, then advance 9.630s (total 20.000s).
	clk.Advance(5*time.Second + 370*time.Millisecond)
	testutil.FailErr(t, "advance to 10.370s", store.AdvanceDue(t.Context()))
	b1, has1, err := ReserveCollectBatch(t.Context(), store, owner, "req:c17:t1")
	testutil.FailErr(t, "collect t1", err)
	if !has1 {
		t.Fatal("expected batch at 10.370s")
	}
	testutil.FailErr(t, "commit t1", CommitCollectBatch(t.Context(), store, owner.SessionID, b1.DeliveryID))

	clk.Advance(9630 * time.Millisecond) // exact base + 20.000s
	testutil.FailErr(t, "advance to 20.000s", store.AdvanceDue(t.Context()))
	vRep2, err := store.Inspect(t.Context(), owner, wRep.Handle)
	testutil.FailErr(t, "inspect drift-free tick 2", err)
	if vRep2.PendingMatches != 1 || vRep2.LatestMatch == nil {
		t.Fatalf("anchored repeating timer drifted past 20.000s: %+v", vRep2)
	}
	var rep2Ev map[string]any
	testutil.FailErr(t, "decode tick 2", json.Unmarshal(vRep2.LatestMatch.Evidence, &rep2Ev))
	if rep2Ev["scheduled_at"] != base.Add(20*time.Second).Format(time.RFC3339Nano) {
		t.Fatalf("tick 2 scheduled_at=%v, want %s", rep2Ev["scheduled_at"], base.Add(20*time.Second).Format(time.RFC3339Nano))
	}
	b2, _, err := ReserveCollectBatch(t.Context(), store, owner, "req:c17:t2")
	testutil.FailErr(t, "collect t2", err)
	testutil.FailErr(t, "commit t2", CommitCollectBatch(t.Context(), store, owner.SessionID, b2.DeliveryID))

	// Verify absolute `at` timer fires at exact base+30s AND clock jump across 3 ticks (+30s, +40s, +50s) + expiry tie at +60s.
	clk.Advance(10 * time.Second) // base + 30s
	testutil.FailErr(t, "advance to 30s", store.AdvanceDue(t.Context()))
	if vAt, _ := store.Inspect(t.Context(), owner, wAt.Handle); vAt.PendingMatches != 1 || vAt.State != StateTerminal {
		t.Fatalf("absolute at timer did not fire at +30s: %+v", vAt)
	}
	b3, _, err := ReserveCollectBatch(t.Context(), store, owner, "req:c17:t3")
	testutil.FailErr(t, "collect t3", err)
	testutil.FailErr(t, "commit t3", CommitCollectBatch(t.Context(), store, owner.SessionID, b3.DeliveryID))

	// Single clock jump of 35s (from +30s to +65s): ticks at +40s, +50s, +60s = 3 ticks (2 missed), AND wTie expires at +60s with 0 matches.
	clk.Advance(35 * time.Second) // base + 65s
	testutil.FailErr(t, "advance across clock jump", store.AdvanceDue(t.Context()))
	if vTie, _ := store.Inspect(t.Context(), owner, wTie.Handle); vTie.State != StateTerminal || vTie.Outcome != OutcomeExpired || vTie.PendingMatches != 0 {
		t.Fatalf("expiry tie must expire with 0 matches: %+v", vTie)
	}
	vJump, err := store.Inspect(t.Context(), owner, wRep.Handle)
	testutil.FailErr(t, "inspect clock jump match", err)
	if vJump.PendingMatches != 1 || vJump.LatestMatch == nil {
		t.Fatalf("expected 1 coalesced match after clock jump, got %+v", vJump)
	}
	var jumpEv map[string]any
	testutil.FailErr(t, "decode jump evidence", json.Unmarshal(vJump.LatestMatch.Evidence, &jumpEv))
	if jumpEv["tick_count"] != float64(3) || jumpEv["missed_ticks"] != float64(2) {
		t.Fatalf("unexpected clock jump evidence: %+v", jumpEv)
	}
	b4, _, err := ReserveCollectBatch(t.Context(), store, owner, "req:c17:jump")
	testutil.FailErr(t, "collect jump", err)
	testutil.FailErr(t, "commit jump", CommitCollectBatch(t.Context(), store, owner.SessionID, b4.DeliveryID))

	// Missed ticks across restart (Recover): jump 45s from +65s to +110s (+70s, +80s, +90s, +100s, +110s = 5 ticks -> 4 missed).
	clk.Advance(45 * time.Second) // base + 110s
	testutil.FailErr(t, "recover across missed ticks", store.Recover(t.Context()))
	vRec, err := store.Inspect(t.Context(), owner, wRep.Handle)
	testutil.FailErr(t, "inspect recovered missed ticks", err)
	if vRec.PendingMatches != 1 || vRec.LatestMatch == nil {
		t.Fatalf("expected 1 coalesced catch-up match after restart, got %+v", vRec)
	}
	var recEv map[string]any
	testutil.FailErr(t, "decode recovery evidence", json.Unmarshal(vRec.LatestMatch.Evidence, &recEv))
	if recEv["tick_count"] != float64(5) || recEv["missed_ticks"] != float64(4) {
		t.Fatalf("unexpected restart catch-up evidence: %+v", recEv)
	}

	// Advance to base + 5m (14:00:00 UTC = 09:00 EST) and verify calendar watch fires.
	clk.Set(base.Add(5 * time.Minute))
	testutil.FailErr(t, "advance to calendar 09:00 EST", store.AdvanceDue(t.Context()))
	if vCal, _ := store.Inspect(t.Context(), owner, wCal.Handle); vCal.PendingMatches != 1 || vCal.State != StateTerminal {
		t.Fatalf("calendar timer did not fire at 09:00 EST (14:00 UTC): %+v", vCal)
	}

	writeWW02Evidence(t, filepath.Join(t.TempDir(), "c17-timers-inprocess.json"), ww02EvidenceBundle{
		SchemaVersion: 1,
		RunID:         "ww05-c17-inprocess",
		Lane:          "in-process",
		ClockDomain:   "manual",
		CaseID:        "C17-timers-inprocess",
		Expectations:  map[string]any{"anchored_drift_free": true, "missed_ticks_counted": true, "repeating_wake_rejected": true},
		Observers: map[string]any{
			"source":         map[string]any{"clock_domain": "manual", "jump_missed_ticks": jumpEv["missed_ticks"], "restart_missed_ticks": recEv["missed_ticks"]},
			"storage":        map[string]any{"available": true},
			"admission":      map[string]any{"observer": "admission", "clock_domain": "manual"},
			"rendered_input": map[string]any{"observer": "rendered_input", "clock_domain": "manual"},
		},
	})
}

func TestWW05_C18_SourceLossBudgetsAndRevocation(t *testing.T) {
	base := time.Date(2026, 11, 27, 14, 40, 0, 0, time.UTC)
	store, clk, _, owner := openWW05WatchStore(t, base)

	// 1. Feed interruption & resync: >64 files uses persisted digest cursor; clearing in-memory manifest + external change emits outcome="resync".
	resyncDir := t.TempDir()
	for i := range 70 {
		testutil.FailErr(t, "seed resync file", os.WriteFile(filepath.Join(resyncDir, fmt.Sprintf("f%03d.txt", i)), []byte("v1\n"), 0o644))
	}
	resyncAuth := Authority{
		ProjectDir: resyncDir,
		Roots:      []WatchRootGrant{{Label: "workspace", RootID: "root-resync", Path: resyncDir}},
	}
	wResync, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Authority:  resyncAuth,
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: "file_change", Root: "workspace", Path: "."}},
	})
	testutil.FailErr(t, "register resync file watch", err)

	// Simulate feed interruption / restart clearing in-memory manifest while disk changes.
	store.manifests.removeWatch(wResync.WatchID)
	testutil.FailErr(t, "modify file during feed loss", os.WriteFile(filepath.Join(resyncDir, "f000.txt"), []byte("v2-after-loss\n"), 0o644))
	clk.Advance(SamplerMinInterval)
	testutil.FailErr(t, "reconcile resync after feed loss", store.AdvanceDue(t.Context()))
	vResync, err := store.Inspect(t.Context(), owner, wResync.Handle)
	testutil.FailErr(t, "inspect resync watch", err)
	if vResync.PendingMatches != 1 || vResync.LatestMatch == nil || vResync.LatestMatch.Outcome != "resync" {
		t.Fatalf("expected truthful resync match after feed interruption, got %+v", vResync)
	}
	_, err = store.Cancel(t.Context(), owner, wResync.Handle)
	testutil.FailErr(t, "cancel resync watch", err)

	// 2. Grant revocation, redaction, and ZERO source reads after withdrawal (by counter).
	confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
		return true
	})
	t.Cleanup(func() { confine.SetEgressResolver(nil) })

	var httpReads atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpReads.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	parsedURL, err := url.Parse(srv.URL)
	testutil.FailErr(t, "parse http url", err)
	portNum, err := strconv.Atoi(parsedURL.Port())
	testutil.FailErr(t, "parse http port", err)

	wHTTP, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Authority:  Authority{LoopbackGranted: true, LoopbackPorts: []uint16{uint16(portNum)}},
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: "http_ready", URL: srv.URL, Method: "GET", StatusMin: 200, StatusMax: 299}},
	})
	testutil.FailErr(t, "register http_ready watch", err)

	clk.Advance(SamplerMinInterval)
	testutil.FailErr(t, "initial http probe", store.AdvanceDue(t.Context()))
	if httpReads.Load() != 1 {
		t.Fatalf("expected 1 initial HTTP read, got %d", httpReads.Load())
	}

	// Revoke grant -> Inspect and Collect redact URL + mark Restricted; next probe transitions to source_lost.
	var revoked atomic.Bool
	revoked.Store(true)
	store.SetAuthorityChecker(func(_ context.Context, reg Registration) (bool, string) {
		if reg.WatchID == wHTTP.WatchID && revoked.Load() {
			return false, "EGRESS_REVOKED"
		}
		return true, ""
	})
	vRev, err := store.Inspect(t.Context(), owner, wHTTP.Handle)
	testutil.FailErr(t, "inspect revoked http watch", err)
	if vRev.LatestMatch == nil || !vRev.LatestMatch.Restricted || strings.Contains(string(vRev.LatestMatch.Evidence), srv.URL) {
		t.Fatalf("revoked inspect leaked URL or missed Restricted flag: %+v", vRev)
	}
	revBatch, hasRev, err := ReserveCollectBatch(t.Context(), store, owner, "req:c18:revoked")
	testutil.FailErr(t, "collect revoked http batch", err)
	if !hasRev {
		t.Fatal("expected revoked batch")
	}
	foundRedacted := false
	for _, m := range revBatch.Matches {
		if m.Handle == wHTTP.Handle {
			if !m.Restricted || strings.Contains(string(m.Evidence), srv.URL) {
				t.Fatalf("revoked collect batch leaked URL or missed Restricted: %+v", m)
			}
			foundRedacted = true
		}
	}
	if !foundRedacted {
		t.Fatal("did not find redacted match for wHTTP")
	}

	clk.Advance(SamplerMaxInterval)
	testutil.FailErr(t, "advance under revocation", store.AdvanceDue(t.Context()))
	vLost, err := store.Inspect(t.Context(), owner, wHTTP.Handle)
	testutil.FailErr(t, "inspect source_lost after revocation", err)
	if vLost.State != StateTerminal || vLost.Outcome != OutcomeSourceLost {
		t.Fatalf("expected source_lost after revocation, got %+v", vLost)
	}

	readsAfterRevoke := httpReads.Load()
	for range 5 {
		clk.Advance(SamplerMaxInterval)
		testutil.FailErr(t, "advance after withdrawal", store.AdvanceDue(t.Context()))
	}
	if got := httpReads.Load(); got != readsAfterRevoke {
		t.Fatalf("source reads continued after revocation withdrawal: before=%d after=%d", readsAfterRevoke, got)
	}
	revoked.Store(false)

	// 3. Source removal (directory removed) -> source_lost + zero sampler reads after withdrawal.
	vanishDir := t.TempDir()
	testutil.FailErr(t, "seed vanish file", os.WriteFile(filepath.Join(vanishDir, "x.txt"), []byte("hi\n"), 0o644))
	wVanish, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Authority:  Authority{ProjectDir: vanishDir, Roots: []WatchRootGrant{{Label: "vanish", RootID: "root-vanish", Path: vanishDir}}},
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: "file_change", Root: "vanish", Path: "."}},
	})
	testutil.FailErr(t, "register vanish dir watch", err)
	testutil.FailErr(t, "remove watched root dir", os.RemoveAll(vanishDir))
	clk.Advance(SamplerMinInterval)
	testutil.FailErr(t, "reconcile removed root dir", store.AdvanceDue(t.Context()))
	vVanish, err := store.Inspect(t.Context(), owner, wVanish.Handle)
	testutil.FailErr(t, "inspect vanished root watch", err)
	if vVanish.State != StateTerminal || vVanish.Outcome != OutcomeSourceLost {
		t.Fatalf("removed root must transition to source_lost: %+v", vVanish)
	}

	store.sampler.mu.Lock()
	runsAfterVanish := store.sampler.totalRuns
	groupsAfterVanish := len(store.sampler.groupsByID)
	store.sampler.mu.Unlock()
	if groupsAfterVanish != 0 {
		t.Fatalf("expected 0 active sampler groups after withdrawal, got %d", groupsAfterVanish)
	}
	for range 3 {
		clk.Advance(SamplerMaxInterval)
		testutil.FailErr(t, "advance after root removal", store.AdvanceDue(t.Context()))
	}
	store.sampler.mu.Lock()
	runsLater := store.sampler.totalRuns
	store.sampler.mu.Unlock()
	if runsLater != runsAfterVanish {
		t.Fatalf("sampler executed %d extra runs after withdrawal", runsLater-runsAfterVanish)
	}

	// 4. Process output watch withdrawal -> zero ProcessOutputLookup reads after cancel.
	var outputReads atomic.Int64
	store.SetProcessOutputLookup(func(_ context.Context, _ string, _ string, since int64) (bgprocess.ProcessOutputState, error) {
		outputReads.Add(1)
		return bgprocess.ProcessOutputState{NextCursor: since, Running: true}, nil
	})
	wOut, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: "process_output", Handle: "cmd-count"}},
	})
	testutil.FailErr(t, "register counting process_output watch", err)
	_, err = store.Cancel(t.Context(), owner, wOut.Handle)
	testutil.FailErr(t, "cancel process_output watch", err)
	readsAfterCancel := outputReads.Load()
	testutil.FailErr(t, "notify after cancel", store.NotifyProcessOutput(t.Context(), owner.SessionID, "cmd-count"))
	if got := outputReads.Load(); got != readsAfterCancel {
		t.Fatalf("ProcessOutputLookup called %d times after watch cancellation", got-readsAfterCancel)
	}

	writeWW02Evidence(t, filepath.Join(t.TempDir(), "c18-source-loss-budgets.json"), ww02EvidenceBundle{
		SchemaVersion: 1,
		RunID:         "ww05-c18-loss",
		Lane:          "in-process",
		ClockDomain:   "manual",
		CaseID:        "C18-source-loss-budgets",
		Expectations:  map[string]any{"zero_reads_after_withdrawal": true, "redaction_after_revoke": true},
		Observers: map[string]any{
			"source":         map[string]any{"http_reads_delta_after_withdraw": httpReads.Load() - readsAfterRevoke, "output_reads_delta_after_cancel": outputReads.Load() - readsAfterCancel},
			"storage":        map[string]any{"available": true, "active_sampler_groups": groupsAfterVanish},
			"admission":      map[string]any{"observer": "admission", "clock_domain": "manual"},
			"rendered_input": map[string]any{"observer": "rendered_input", "clock_domain": "manual"},
		},
	})
}

func TestWW05_C18_FileReconcileBudget(t *testing.T) {
	base := time.Date(2026, 11, 27, 14, 50, 0, 0, time.UTC)
	store, clk, _, owner := openWW05WatchStore(t, base)

	// 1. Exceeding 4096 file reconcile cap at registration -> bounded work + explicit coverage_exceeded outcome.
	overDir := t.TempDir()
	for i := range MaxReconcileFiles + 5 {
		testutil.FailErr(t, "seed overflow file", os.WriteFile(filepath.Join(overDir, fmt.Sprintf("f%05d.txt", i)), []byte("x"), 0o644))
	}
	overAuth := Authority{
		ProjectDir: overDir,
		Roots:      []WatchRootGrant{{Label: "over", RootID: "root-over", Path: overDir}},
	}
	wOverInit, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Authority:  overAuth,
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: "file_change", Root: "over", Path: "."}},
	})
	testutil.FailErr(t, "register overflow watch", err)
	vOverInit, err := store.Inspect(t.Context(), owner, wOverInit.Handle)
	testutil.FailErr(t, "inspect overflow init watch", err)
	if vOverInit.State != StateTerminal || vOverInit.Outcome != OutcomeSourceLost || vOverInit.LatestMatch == nil || vOverInit.LatestMatch.Outcome != "coverage_exceeded" {
		t.Fatalf("expected terminal source_lost with coverage_exceeded match at registration, got %+v", vOverInit)
	}
	var initGapEv map[string]any
	testutil.FailErr(t, "decode init gap evidence", json.Unmarshal(vOverInit.LatestMatch.Evidence, &initGapEv))
	if initGapEv["gap"] != "file_budget_exceeded" {
		t.Fatalf("expected gap=file_budget_exceeded, got %+v", initGapEv)
	}

	// 2. Growing past 4096 files during live reconciliation -> bounded work, coverage_exceeded, terminal source_lost, zero further reads.
	growDir := t.TempDir()
	for i := range MaxReconcileFiles - 2 {
		testutil.FailErr(t, "seed grow file", os.WriteFile(filepath.Join(growDir, fmt.Sprintf("g%05d.txt", i)), []byte("y"), 0o644))
	}
	growAuth := Authority{
		ProjectDir: growDir,
		Roots:      []WatchRootGrant{{Label: "grow", RootID: "root-grow", Path: growDir}},
	}
	wGrow, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Authority:  growAuth,
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: "file_change", Root: "grow", Path: "."}},
	})
	testutil.FailErr(t, "register grow watch", err)
	if wGrow.State != "pending" {
		t.Fatalf("expected pending receipt state under 4096 cap, got %+v", wGrow)
	}
	if vGrowInit, iErr := store.Inspect(t.Context(), owner, wGrow.Handle); iErr != nil || vGrowInit.State != StateLive {
		t.Fatalf("expected live watch under 4096 cap, got %+v (err=%v)", vGrowInit, iErr)
	}

	for i := range 10 {
		testutil.FailErr(t, "add overflow file", os.WriteFile(filepath.Join(growDir, fmt.Sprintf("extra%02d.txt", i)), []byte("z"), 0o644))
	}
	clk.Advance(SamplerMinInterval)
	testutil.FailErr(t, "reconcile live overflow", store.AdvanceDue(t.Context()))

	vGrow, err := store.Inspect(t.Context(), owner, wGrow.Handle)
	testutil.FailErr(t, "inspect live overflow watch", err)
	if vGrow.State != StateTerminal || vGrow.Outcome != OutcomeSourceLost || vGrow.LatestMatch == nil || vGrow.LatestMatch.Outcome != "coverage_exceeded" {
		t.Fatalf("expected terminal source_lost with coverage_exceeded after live overflow, got %+v", vGrow)
	}

	store.sampler.mu.Lock()
	runsAfterOverflow := store.sampler.totalRuns
	groupsRemaining := len(store.sampler.groupsByID)
	store.sampler.mu.Unlock()
	if groupsRemaining != 0 {
		t.Fatalf("expected 0 sampler groups after overflow retirement, got %d", groupsRemaining)
	}
	clk.Advance(SamplerMaxInterval)
	testutil.FailErr(t, "advance after overflow retirement", store.AdvanceDue(t.Context()))
	store.sampler.mu.Lock()
	runsAfterExtra := store.sampler.totalRuns
	store.sampler.mu.Unlock()
	if runsAfterExtra != runsAfterOverflow {
		t.Fatalf("sampler continued running after overflow retirement: %d -> %d", runsAfterOverflow, runsAfterExtra)
	}

	writeWW02Evidence(t, filepath.Join(t.TempDir(), "c18-file-reconcile-budget.json"), ww02EvidenceBundle{
		SchemaVersion: 1,
		RunID:         "ww05-c18-budget",
		Lane:          "in-process",
		ClockDomain:   "manual",
		CaseID:        "C18-file-reconcile-budget",
		Expectations:  map[string]any{"max_reconcile_files": MaxReconcileFiles, "explicit_coverage_outcome": "coverage_exceeded"},
		Observers: map[string]any{
			"source":         map[string]any{"init_gap": initGapEv["gap"], "live_overflow_outcome": vGrow.LatestMatch.Outcome},
			"storage":        map[string]any{"available": true, "sampler_groups_remaining": groupsRemaining},
			"admission":      map[string]any{"observer": "admission", "clock_domain": "manual"},
			"rendered_input": map[string]any{"observer": "rendered_input", "clock_domain": "manual"},
		},
	})
}
