package verification

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// TestGateStateWatcherLag proves GB10 I9 (Decisions 098 & 099, first case
// GB07-F3): an authoritative verification gate must never pass a stale receipt
// while watcher delivery is held back after an external edit, and must flip
// cleanly once flushed.
func TestGateStateWatcherLag(t *testing.T) {
	ctx := t.Context()
	sessions := storetest.New(t, testdbseed.DefaultProjectID)
	sess, err := sessions.Sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	sess.ProjectID = testdbseed.DefaultProjectID
	sess.WorkspacePath = t.TempDir()

	m := New(sessions.Sessions, sessions.Transcript, scope.New(sessions.Sessions), nil)
	m.SetDataDir(t.TempDir())
	m.SetEvidenceStore(inspector.NewJSONLStore(inspector.DefaultEvidenceDir))
	ledger := sourceledger.New(testdbfixture.Open(t, "store.db"), t.TempDir(), sourceledger.Timing{})
	m.SetSourceObservations(ledger.Inventory)

	srcPath := filepath.Join(sess.WorkspacePath, "source.txt")
	testutil.FailErr(t, "write initial source", os.WriteFile(srcPath, []byte("initial-verified-content\n"), 0o600))
	repochange.Advance(sess.WorkspacePath)

	// 1. Capture initial exact revision and record a passing verify receipt.
	revision, root := m.Revision(ctx, sess.WorkspacePath)
	if revision == "" || root == "" {
		t.Fatal("initial exact source observation unavailable")
	}
	when := time.Now().UTC()
	record := checkRecord("verify-check-1", revision, root, when)
	record.RunID = sess.ID
	testutil.FailErr(t, "persist passing verify receipt", m.evidenceStore.Append(ctx, m.EvidenceRootFor(sess), record))
	history := []api.Message{{Role: api.MessageRoleUser, Content: "Verify source", CreatedAt: when.Add(-time.Second)}}
	testutil.FailErr(t, "append user message", sessions.Transcript.AppendMessages(ctx, sess.ID, history[0]))

	// Baseline control: before external edit, GateState passes.
	passed, repair, unverified := m.GateState(ctx, sess, history)
	if !passed || repair || unverified {
		t.Fatalf("baseline GateState = (passed=%v, repair=%v, unverified=%v), want (true, false, false)", passed, repair, unverified)
	}

	// 2. Hold back watcher delivery and edit the verified file externally.
	restore := repochange.UseDebounce(ctx, 30*time.Second)
	defer restore()
	testutil.FailErr(t, "external edit during watcher lag", os.WriteFile(srcPath, []byte("externally-mutated-after-verify\n"), 0o600))

	// 3. Evaluate authoritative gates while the watcher event is still held back:
	// neither GateState nor WorkflowSourceVerifyPassed may pass the stale receipt.
	lagPassed, lagRepair, lagUnverified := m.GateState(ctx, sess, history)
	if lagPassed {
		t.Fatalf("GB07-F3 false accept: GateState passed stale receipt while watcher delivery was held back (passed=%v, repair=%v, unverified=%v)",
			lagPassed, lagRepair, lagUnverified)
	}
	wfPassed, err := m.WorkflowSourceVerifyPassed(ctx, sess.ID)
	testutil.FailErr(t, "WorkflowSourceVerifyPassed during watcher lag", err)
	if wfPassed {
		t.Fatalf("GB07-F3 false accept: WorkflowSourceVerifyPassed returned true while watcher delivery was held back")
	}

	// 4. Release debounce and flush watcher: gate must now report stale/repair.
	restore()
	repochange.Advance(sess.WorkspacePath)
	afterPassed, afterRepair, afterUnverified := m.GateState(ctx, sess, history)
	if afterPassed || !afterRepair {
		t.Fatalf("post-flush GateState = (passed=%v, repair=%v, unverified=%v), want (false, true, false)",
			afterPassed, afterRepair, afterUnverified)
	}
}
