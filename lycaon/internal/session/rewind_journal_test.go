package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func prepareJournalFixture(t *testing.T) (*Manager, *sessioncheckpoint.Journal, string) {
	t.Helper()
	mgr, id, root := newCheckpointTestSession(t)
	anchor := appendRewindAsk(t, mgr, id)
	path := filepath.Join(root, "file.txt")
	testutil.FailErr(t, "write turn result", os.WriteFile(path, []byte("after"), 0o640))
	recordRewindTestEffect(t, mgr, id, "file.txt", []byte("before"), []byte("after"), api.SourceChangeOpWrite)
	cp := sessioncheckpoint.New(mgr.dataDir, root, mgr.store)
	man := &sessioncheckpoint.Manifest{SessionID: id, AnchorMessageID: anchor, ProjectDir: root}
	ctx := checkpointCaller(t, mgr)
	journal, err := mgr.prepareSourceRewindJournal(ctx, cp, man, uuid.NewString(), id, []string{anchor})
	testutil.FailErr(t, "prepare source journal", err)
	return mgr, journal, path
}

func TestRewindJournalRollsBackAnInterruptedApply(t *testing.T) {
	mgr, journal, path := prepareJournalFixture(t)
	_, err := journal.Apply()
	testutil.FailErr(t, "apply source journal", err)
	reloaded, err := sessioncheckpoint.LoadJournal(journal.Path())
	testutil.FailErr(t, "reload journal", err)
	testutil.FailErr(t, "bind source recovery", mgr.bindSourceRewindJournal(t.Context(), reloaded))
	testutil.FailErr(t, "rollback", reloaded.Rollback())
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read compensated file", err)
	if string(raw) != "after" {
		t.Fatalf("file=%q", raw)
	}
	// Repeated recovery reuses the source receipt.
	testutil.FailErr(t, "repeat rollback", reloaded.Rollback())
}

func TestRewindJournalRefusesToOverwriteDivergedUserBytes(t *testing.T) {
	_, journal, path := prepareJournalFixture(t)
	_, err := journal.Apply()
	testutil.FailErr(t, "apply", err)
	testutil.FailErr(t, "human changes restored file", os.WriteFile(path, []byte("human work"), 0o644))
	if err := journal.Rollback(); err == nil {
		t.Fatal("rollback accepted independent human bytes")
	}
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read human work", err)
	if string(raw) != "human work" {
		t.Fatalf("file=%q", raw)
	}
}

func TestRewindRecoveryFinishesWithoutReplacingLaterHumanBytes(t *testing.T) {
	mgr, journal, path := prepareJournalFixture(t)
	_, err := journal.Apply()
	testutil.FailErr(t, "apply before interruption", err)
	testutil.FailErr(t, "later human edit", os.WriteFile(path, []byte("human after rewind"), 0o644))
	reloaded, err := sessioncheckpoint.LoadJournal(journal.Path())
	testutil.FailErr(t, "load completed source journal", err)
	testutil.FailErr(t, "bind completed journal", mgr.bindSourceRewindJournal(t.Context(), reloaded))
	_, err = reloaded.Apply()
	testutil.FailErr(t, "recover completed source effects", err)
	testutil.FailErr(t, "verify completion marker", reloaded.VerifyTargets())
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read later human edit", err)
	if string(raw) != "human after rewind" {
		t.Fatalf("file=%q", raw)
	}
}
