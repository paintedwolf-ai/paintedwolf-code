package verification

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/repochange/repochangetest"
	"github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type fixedVerifySessionStore struct{ sess *api.Session }

func (f fixedVerifySessionStore) Get(_ context.Context, id string) (*api.Session, error) {
	if f.sess != nil && f.sess.ID == id {
		return f.sess, nil
	}
	return nil, nil
}

// TestGateStateWatcherLag is the regression test for GB07-F3 / Decision 099:
// an authoritative verification gate must fail closed when watcher delivery
// lags past quiet wait, refuse a stale receipt once settled after an external
// edit, and pass once settled when content is unchanged.
func TestGateStateWatcherLag(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	repochangetest.Watch(t, root)
	repochangetest.Acquire(t)

	initialBytes := []byte("package gate\n\nfunc Verify() bool { return true }\n")
	gatePath := filepath.Join(root, "gate.go")
	testutil.FailErr(t, "write initial gate.go", os.WriteFile(gatePath, initialBytes, 0o600))

	sessions := storetest.New(t, testdbseed.DefaultProjectID)
	sess, err := sessions.Sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	sess.ProjectID = "project"
	sess.WorkspacePath = root

	sessStore := fixedVerifySessionStore{sess: sess}
	m := New(sessStore, sessions.Transcript, scope.New(sessions.Sessions), nil)
	m.SetDataDir(t.TempDir())
	m.SetEvidenceStore(inspector.NewJSONLStore(inspector.DefaultEvidenceDir))
	ledger := sourceledger.New(testdbfixture.Open(t, "store.db"), t.TempDir(), sourceledger.Timing{})
	t.Cleanup(func() { _ = ledger.Snapshots.Close() })
	m.SetSourceObservations(ledger.Inventory)

	// 1. Capture initial snapshot and record a passing verify receipt.
	initRev, initRoot := m.SettledRevision(ctx, root)
	if initRev == "" || initRoot == "" {
		t.Fatalf("initial SettledRevision empty: rev=%q root=%q", initRev, initRoot)
	}
	when := time.Now().UTC()
	m.RecordSourceRunTerminal(ctx, sess, sess.ID, ProducerVerify, tools.SourceRunCapture{
		CheckID:          "chk-verify-lag",
		IsCheck:          true,
		Command:          "echo VERIFY_OK gate.go",
		ExitCode:         0,
		Verdict:          api.SourceVerdictPassed,
		SourceRevision:   initRev,
		SourceRootDigest: initRoot,
		Cwd:              ".",
	}, when)

	history := []api.Message{{Role: api.MessageRoleUser, Content: "Verify gate.go", CreatedAt: when.Add(-time.Second)}}

	if passed, repair, unverified := m.GateState(ctx, sess, history); !passed || repair || unverified {
		t.Fatalf("baseline GateState = (passed=%v repair=%v unverified=%v), want (true, false, false)", passed, repair, unverified)
	}
	if wfPassed, err := m.WorkflowSourceVerifyPassed(ctx, sess.ID); err != nil || !wfPassed {
		t.Fatalf("baseline WorkflowSourceVerifyPassed = (%v, %v), want (true, nil)", wfPassed, err)
	}

	// 2. Hold back watcher delivery past sourcesnapshot's quiet wait, then edit externally.
	restoreDebounce := repochange.UseDebounce(ctx, 5*time.Second)
	restored := false
	restore := func() {
		if !restored {
			restored = true
			restoreDebounce()
		}
	}
	t.Cleanup(restore)

	testutil.FailErr(t, "external edit gate.go", os.WriteFile(gatePath, []byte("package gate\n\nfunc Verify() bool { return false }\n"), 0o600))

	// 3. While watcher delivery is held back, authoritative gate MUST fail closed (unverified, never passed).
	lagPassed, lagRepair, lagUnverified := m.GateState(ctx, sess, history)
	if lagPassed || lagRepair || !lagUnverified {
		t.Fatalf("GateState during watcher lag = (passed=%v repair=%v unverified=%v), want (false, false, true)", lagPassed, lagRepair, lagUnverified)
	}
	if wfPassed, err := m.WorkflowSourceVerifyPassed(ctx, sess.ID); err != nil || wfPassed {
		t.Fatalf("WorkflowSourceVerifyPassed during watcher lag = (%v, %v), want (false, nil)", wfPassed, err)
	}
	// Display sharing the fixed path must report SourceUnknown (never SourceCurrent) while unsettled.
	if obs := m.SourceObservations(ctx, sess, history); obs == nil || len(obs.Checks) != 1 || obs.Checks[0].Match != SourceUnknown {
		t.Fatalf("SourceObservations during watcher lag = %+v, want SourceUnknown", obs)
	}

	// 4. Release the held-back watcher event: gate settles on the new digest and refuses stale receipt (repair).
	restore()
	afterPassed, afterRepair, afterUnverified := m.GateState(ctx, sess, history)
	if afterPassed || !afterRepair || afterUnverified {
		t.Fatalf("GateState after watcher flush = (passed=%v repair=%v unverified=%v), want (false, true, false)", afterPassed, afterRepair, afterUnverified)
	}
	if wfPassed, err := m.WorkflowSourceVerifyPassed(ctx, sess.ID); err != nil || wfPassed {
		t.Fatalf("WorkflowSourceVerifyPassed after watcher flush = (%v, %v), want (false, nil)", wfPassed, err)
	}
	if obs := m.SourceObservations(ctx, sess, history); obs == nil || len(obs.Checks) != 1 || obs.Checks[0].Match != SourceChanged {
		t.Fatalf("SourceObservations after watcher flush = %+v, want SourceChanged", obs)
	}

	// 5. Restoring original bytes and non-content churn settles back to current with no needless refusal.
	testutil.FailErr(t, "restore initial gate.go", os.WriteFile(gatePath, initialBytes, 0o600))
	now := time.Now().Add(2 * time.Second)
	testutil.FailErr(t, "mtime touch gate.go", os.Chtimes(gatePath, now, now))

	if passed, repair, unverified := m.GateState(ctx, sess, history); !passed || repair || unverified {
		t.Fatalf("GateState after restore + mtime churn = (passed=%v repair=%v unverified=%v), want (true, false, false)", passed, repair, unverified)
	}
	if wfPassed, err := m.WorkflowSourceVerifyPassed(ctx, sess.ID); err != nil || !wfPassed {
		t.Fatalf("WorkflowSourceVerifyPassed after restore = (%v, %v), want (true, nil)", wfPassed, err)
	}
}

// TestGateStateNoSentinelFallbackVerifiesContentDirectly verifies Decision 110:
// when no sentinel barrier is available (SettleUnproven, e.g. cross-volume root),
// authoritative gate evaluation falls back to VerifyContent directly: passing a
// current receipt and refusing a stale receipt even while watcher delivery is
// held back.
func TestGateStateNoSentinelFallbackVerifiesContentDirectly(t *testing.T) {
	ctx := t.Context()
	restoreDevice := repochange.UseSentinelDeviceCheck(func(string, string) bool { return false })
	t.Cleanup(restoreDevice)

	root := t.TempDir()
	repochangetest.Watch(t, root)
	repochangetest.Acquire(t)

	if got := repochange.SettleRootStatus(ctx, root, 50*time.Millisecond, time.Second); got != repochange.SettleUnproven {
		t.Fatalf("SettleRootStatus without sentinel = %v, want SettleUnproven", got)
	}

	initialBytes := []byte("package gate\n\nfunc Verify() bool { return true }\n")
	gatePath := filepath.Join(root, "gate.go")
	testutil.FailErr(t, "write initial gate.go", os.WriteFile(gatePath, initialBytes, 0o600))

	sessions := storetest.New(t, testdbseed.DefaultProjectID)
	sess, err := sessions.Sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	sess.ProjectID = "project"
	sess.WorkspacePath = root

	sessStore := fixedVerifySessionStore{sess: sess}
	m := New(sessStore, sessions.Transcript, scope.New(sessions.Sessions), nil)
	m.SetDataDir(t.TempDir())
	m.SetEvidenceStore(inspector.NewJSONLStore(inspector.DefaultEvidenceDir))
	ledger := sourceledger.New(testdbfixture.Open(t, "store.db"), t.TempDir(), sourceledger.Timing{})
	t.Cleanup(func() { _ = ledger.Snapshots.Close() })
	m.SetSourceObservations(ledger.Inventory)

	initRev, initRoot := m.SettledRevision(ctx, root)
	if initRev == "" || initRoot == "" {
		t.Fatalf("initial SettledRevision empty under VerifyContent fallback: rev=%q root=%q", initRev, initRoot)
	}
	when := time.Now().UTC()
	m.RecordSourceRunTerminal(ctx, sess, sess.ID, ProducerVerify, tools.SourceRunCapture{
		CheckID:          "chk-no-sentinel",
		IsCheck:          true,
		Command:          "echo VERIFY_OK gate.go",
		ExitCode:         0,
		Verdict:          api.SourceVerdictPassed,
		SourceRevision:   initRev,
		SourceRootDigest: initRoot,
		Cwd:              ".",
	}, when)
	history := []api.Message{{Role: api.MessageRoleUser, Content: "Verify gate.go", CreatedAt: when.Add(-time.Second)}}

	restoreDebounce := repochange.UseDebounce(ctx, 5*time.Second)
	t.Cleanup(restoreDebounce)

	// (b) Current receipt passes directly via VerifyContent without waiting on the held-back watcher.
	if passed, repair, unverified := m.GateState(ctx, sess, history); !passed || repair || unverified {
		t.Fatalf("GateState current receipt under no-sentinel fallback = (passed=%v repair=%v unverified=%v), want (true, false, false)", passed, repair, unverified)
	}
	if wfPassed, err := m.WorkflowSourceVerifyPassed(ctx, sess.ID); err != nil || !wfPassed {
		t.Fatalf("WorkflowSourceVerifyPassed current receipt = (%v, %v), want (true, nil)", wfPassed, err)
	}

	// (a) External edit while watcher is held back is refused via VerifyContent (repair=true, never passed).
	testutil.FailErr(t, "external edit gate.go", os.WriteFile(gatePath, []byte("package gate\n\nfunc Verify() bool { return false }\n"), 0o600))
	if passed, repair, unverified := m.GateState(ctx, sess, history); passed || !repair || unverified {
		t.Fatalf("GateState stale receipt under no-sentinel fallback = (passed=%v repair=%v unverified=%v), want (false, true, false)", passed, repair, unverified)
	}
	if wfPassed, err := m.WorkflowSourceVerifyPassed(ctx, sess.ID); err != nil || wfPassed {
		t.Fatalf("WorkflowSourceVerifyPassed stale receipt = (%v, %v), want (false, nil)", wfPassed, err)
	}

	// Restoring original bytes passes again even while watcher remains held back.
	testutil.FailErr(t, "restore initial gate.go", os.WriteFile(gatePath, initialBytes, 0o600))
	if passed, repair, unverified := m.GateState(ctx, sess, history); !passed || repair || unverified {
		t.Fatalf("GateState after restore under no-sentinel fallback = (passed=%v repair=%v unverified=%v), want (true, false, false)", passed, repair, unverified)
	}
}

// TestGateStateSecondaryStreamFallbackVerifiesContentDirectly verifies that a
// root with an external secondary stream (e.g. a worktree gitdir outside root
// creating pumps[1:] on macOS FSEvents) never passes a stale receipt while the
// watcher is held back and passes a current receipt cleanly.
func TestGateStateSecondaryStreamFallbackVerifiesContentDirectly(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	externalGitDir := t.TempDir()
	testutil.FailErr(t, "write external HEAD", os.WriteFile(filepath.Join(externalGitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o600))
	testutil.FailErr(t, "write gitdir pointer", os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+externalGitDir+"\n"), 0o600))

	initialBytes := []byte("package gate\n\nfunc Verify() bool { return true }\n")
	gatePath := filepath.Join(root, "gate.go")
	testutil.FailErr(t, "write initial gate.go", os.WriteFile(gatePath, initialBytes, 0o600))

	repochangetest.Watch(t, root)
	repochangetest.Acquire(t)

	sessions := storetest.New(t, testdbseed.DefaultProjectID)
	sess, err := sessions.Sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	sess.ProjectID = "project"
	sess.WorkspacePath = root

	sessStore := fixedVerifySessionStore{sess: sess}
	m := New(sessStore, sessions.Transcript, scope.New(sessions.Sessions), nil)
	m.SetDataDir(t.TempDir())
	m.SetEvidenceStore(inspector.NewJSONLStore(inspector.DefaultEvidenceDir))
	ledger := sourceledger.New(testdbfixture.Open(t, "store.db"), t.TempDir(), sourceledger.Timing{})
	t.Cleanup(func() { _ = ledger.Snapshots.Close() })
	m.SetSourceObservations(ledger.Inventory)

	initRev, initRoot := m.SettledRevision(ctx, root)
	if initRev == "" || initRoot == "" {
		t.Fatalf("initial SettledRevision empty: rev=%q root=%q", initRev, initRoot)
	}
	when := time.Now().UTC()
	m.RecordSourceRunTerminal(ctx, sess, sess.ID, ProducerVerify, tools.SourceRunCapture{
		CheckID:          "chk-secondary-stream",
		IsCheck:          true,
		Command:          "echo VERIFY_OK gate.go",
		ExitCode:         0,
		Verdict:          api.SourceVerdictPassed,
		SourceRevision:   initRev,
		SourceRootDigest: initRoot,
		Cwd:              ".",
	}, when)
	history := []api.Message{{Role: api.MessageRoleUser, Content: "Verify gate.go", CreatedAt: when.Add(-time.Second)}}

	restoreDebounce := repochange.UseDebounce(ctx, 5*time.Second)
	t.Cleanup(restoreDebounce)

	// Unchanged file passes cleanly.
	if passed, repair, unverified := m.GateState(ctx, sess, history); !passed || repair || unverified {
		t.Fatalf("GateState current receipt with external gitdir = (passed=%v repair=%v unverified=%v), want (true, false, false)", passed, repair, unverified)
	}

	// External edit while watcher is held back MUST NOT pass (repair on darwin VerifyContent fallback, unverified on linux held debounce).
	testutil.FailErr(t, "external edit gate.go", os.WriteFile(gatePath, []byte("package gate\n\nfunc Verify() bool { return false }\n"), 0o600))
	if passed, _, _ := m.GateState(ctx, sess, history); passed {
		t.Fatal("GateState falsely passed stale receipt with external secondary stream")
	}
	if wfPassed, err := m.WorkflowSourceVerifyPassed(ctx, sess.ID); err != nil || wfPassed {
		t.Fatalf("WorkflowSourceVerifyPassed = (%v, %v), want (false, nil)", wfPassed, err)
	}
}
