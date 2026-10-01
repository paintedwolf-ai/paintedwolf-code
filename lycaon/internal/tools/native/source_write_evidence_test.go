package native

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	wire "github.com/lycaon/lycaon/pkg/api"
)

var errSourceLedgerFixture = errors.New("ledger unavailable")

type failingSourceRecorder struct{}

func (failingSourceRecorder) Record(context.Context, sourceledger.RecordInput) error {
	return errSourceLedgerFixture
}

func (failingSourceRecorder) RecordTx(context.Context, *sql.Tx, sourceledger.RecordInput) error {
	return errSourceLedgerFixture
}

func TestStreamOverwriteRecordsAddressableEvidence(t *testing.T) {
	dir := t.TempDir()
	st := bindLedgerForWrites(t, dir)
	tctx := nativefixture.Context(dir)
	tctx.ProjectID, tctx.SessionID, tctx.UserTurn = "p1", "s1", 1
	tctx.SourceLedger = st
	tctx.SourceMutations = project.NewSourceMutationService(st.LedgerDB(), st)
	path := filepath.Join(dir, "copy.txt")
	testutil.FailErr(t, "seed destination", os.WriteFile(path, []byte("before\n"), 0o644))

	_, err := applyAgentStream(t.Context(), tctx, agentStreamRequest{
		Target: testMutationTarget(path), Source: bytes.NewReader([]byte("after\n")),
		BeforeCommit: func(fseffect.Target, fseffect.Result) error { return nil },
	})
	testutil.FailErr(t, "stream overwrite", err)

	res, err := st.QueryWalk(t.Context(), "p1",
		sourceledger.Baseline{Kind: sourceledger.BaselineSession, SessionID: "s1"},
		10, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "query stream change", err)
	if len(res.Files) != 1 || res.Files[0].Tip.SHA256 != textfile.SHA256([]byte("after\n")) ||
		len(res.Files[0].Effects) != 1 || res.Files[0].Effects[0].Op != wire.SourceChangeOpWrite {
		t.Fatalf("stream evidence = %+v", res.Files)
	}
	diff, err := st.CompareEffect(t.Context(), "p1", res.Files[0].Effects[0].ID)
	testutil.FailErr(t, "diff stream change", err)
	if diff.Before.Content != "before\n" || diff.After.Content != "after\n" {
		t.Fatalf("stream diff = %+v", diff)
	}
}

func TestLargeStreamRecordsDigestAndSizeWithoutRetainingBody(t *testing.T) {
	dir := t.TempDir()
	ledger := bindLedgerForWrites(t, dir)
	tctx := nativefixture.Context(dir)
	tctx.ProjectID, tctx.SessionID = "p1", "s1"
	tctx.SourceLedger = ledger
	tctx.SourceMutations = project.NewSourceMutationService(ledger.LedgerDB(), ledger)
	body := bytes.Repeat([]byte("x"), sourceledger.MaxRevisionContentBytes+1)
	path := filepath.Join(dir, "large.txt")
	_, err := applyAgentStream(t.Context(), tctx, agentStreamRequest{
		Target: testMutationTarget(path), Source: bytes.NewReader(body),
	})
	testutil.FailErr(t, "stream large source", err)
	var hash, capture string
	var size int64
	testutil.FailErr(t, "read large revision", ledger.LedgerDB().QueryRowContext(t.Context(),
		`SELECT content_sha256, byte_size, capture_state FROM source_versions WHERE path='large.txt' AND state='content' ORDER BY seq DESC LIMIT 1`).Scan(&hash, &size, &capture))
	if hash != textfile.SHA256(body) || size != int64(len(body)) || capture != "metadata_only" {
		t.Fatalf("large revision = hash %s, size %d, capture %s", hash, size, capture)
	}
	evidence, err := captureAgentFileAt(testMutationTarget(path).Location)
	testutil.FailErr(t, "capture large source", err)
	if evidence.content != nil || evidence.size != size || evidence.sha256 != hash {
		t.Fatalf("large source capture = %d retained bytes, size %d, hash %s", len(evidence.content), evidence.size, evidence.sha256)
	}
}

func TestDeleteRecordsPreImageAndRenameRecordsTip(t *testing.T) {
	dir := t.TempDir()
	st := bindLedgerForWrites(t, dir)
	tctx := nativefixture.Context(dir)
	tctx.ProjectID, tctx.SessionID, tctx.UserTurn = "p1", "s1", 2
	tctx.SourceLedger = st
	tctx.SourceMutations = project.NewSourceMutationService(st.LedgerDB(), st)

	deleted := filepath.Join(dir, "deleted.txt")
	testutil.FailErr(t, "seed deletion", os.WriteFile(deleted, []byte("gone\n"), 0o644))
	testutil.FailErr(t, "delete", removeAgentPath(t.Context(), tctx, testMutationTarget(deleted)))
	deletedChanges, err := st.QueryWalk(t.Context(), "p1",
		sourceledger.Baseline{Kind: sourceledger.BaselineSession, SessionID: "s1"},
		10, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "query deletion", err)
	diff, err := st.CompareEffect(t.Context(), "p1", deletedChanges.Files[0].Effects[0].ID)
	testutil.FailErr(t, "diff deletion", err)
	if diff.Before.Content != "gone\n" || diff.After.Availability != sourceledger.ContentAbsent {
		t.Fatalf("delete diff = %+v", diff)
	}

	from := filepath.Join(dir, "from.txt")
	to := filepath.Join(dir, "to.txt")
	testutil.FailErr(t, "seed rename", os.WriteFile(from, []byte("moved\n"), 0o644))
	testutil.FailErr(t, "rename", renameAgentPath(t.Context(), tctx, testMutationTarget(from), testMutationTarget(to)))
	res, err := st.QueryWalk(t.Context(), "p1",
		sourceledger.Baseline{Kind: sourceledger.BaselineSession, SessionID: "s1"},
		10, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "query rename", err)
	var rename *sourceledger.WalkFile
	for i := range res.Files {
		if res.Files[i].Path == "to.txt" {
			rename = &res.Files[i]
			break
		}
	}
	if rename == nil || rename.Tip.SHA256 != textfile.SHA256([]byte("moved\n")) ||
		len(rename.Effects) != 1 || rename.Effects[0].Op != wire.SourceChangeOpRename || rename.Effects[0].FromPath != "from.txt" {
		t.Fatalf("rename evidence = %+v", res.Files)
	}
}

func TestExternalMutationDoorDoesNotCreateProjectSource(t *testing.T) {
	projectDir := t.TempDir()
	st := bindLedgerForWrites(t, projectDir)
	tctx := nativefixture.Context(projectDir)
	tctx.ProjectID, tctx.SessionID, tctx.UserTurn = "p1", "s1", 3
	tctx.SourceLedger = st
	tctx.SourceMutations = project.NewSourceMutationService(st.LedgerDB(), st)
	external := filepath.Join(t.TempDir(), "host-data.txt")

	testutil.FailErr(t, "external write", applyAgentFile(
		t.Context(), tctx, testMutationTarget(external), []byte("host data\n"), nil, "",
	))
	res, err := st.QueryWalk(t.Context(), "p1",
		sourceledger.Baseline{Kind: sourceledger.BaselineSession, SessionID: "s1"},
		10, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "query project changes", err)
	if len(res.Files) != 0 {
		t.Fatalf("external write entered project source: %+v", res.Files)
	}
}

func TestAgentMutationReturnsLedgerFailureAfterLandedWrite(t *testing.T) {
	dir := t.TempDir()
	tctx := nativefixture.Context(dir)
	tctx.ProjectID = "p1"
	tctx.SourceLedger = failingSourceRecorder{}
	path := filepath.Join(dir, "landed.txt")

	err := applyAgentFile(t.Context(), tctx, testMutationTarget(path), []byte("landed\n"), nil, "")
	if !errors.Is(err, errSourceLedgerFixture) {
		t.Fatalf("error = %v, want ledger failure", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		testutil.FailErr(t, "landed write missing", statErr)
	}
}
