package checkpointcontrol

import (
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func prepareJournalFixture(t *testing.T) (*Rewinds, *sessioncheckpoint.Journal, string) {
	t.Helper()
	mgr, repository, ledger, id, root := newRewindControlFixture(t)
	owner, err := repository.HostOwner(t.Context())
	testutil.FailErr(t, "host owner", err)
	ctx := people.WithCaller(t.Context(), owner)
	anchor := uuid.NewString()
	testutil.FailErr(t, "append anchor", repository.AppendMessages(ctx, id, api.Message{ID: anchor, Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser, TrustTier: api.ContentTrustTierTrusted, Content: "change file"}))
	path := filepath.Join(root, "file.txt")
	testutil.FailErr(t, "write turn result", os.WriteFile(path, []byte("after"), 0o640))
	p, err := mgr.rewindProject(ctx, id)
	testutil.FailErr(t, "resolve project", err)
	turn, err := repository.UserTurnOrdinal(ctx, id)
	testutil.FailErr(t, "resolve turn", err)
	testutil.FailErr(t, "record source effect", ledger.Record(ctx, sourceledger.RecordInput{
		RecordLocation: sourceledger.RecordLocation{RootID: p.Roots[0].ID, Path: "file.txt"}, ProjectID: p.ID, Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent, SessionID: id, Turn: turn, Before: []byte("before"), After: []byte("after")}))
	cp := sessioncheckpoint.New(mgr.captures.dataDir, root, repository)
	man := &sessioncheckpoint.Manifest{SessionID: id, AnchorMessageID: anchor, ProjectDir: root}
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
