package projectsource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

func preparedAgentWrite(t *testing.T) (*SourceMutationService, sourceeffect.Pending, string) {
	t.Helper()
	service, p, root, _ := sourceMutationFixture(t)
	path := filepath.Join(root, "note.txt")
	before, after := []byte("before"), []byte("after")
	testutil.FailErr(t, "seed source", os.WriteFile(path, before, 0o600))
	effect := sourceeffect.Plan{
		Record: sourceledger.RecordInput{ProjectID: p.ID, RootID: p.Roots[0].ID, Path: "note.txt",
			Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
			Before: before, After: after, BeforeSHA256: textfile.SHA256(before), AfterSHA256: textfile.SHA256(after),
			ToolName: "write", ToolCallID: "call-1", EntryKind: sourceledger.EntryKindFile},
		Change: sourcefeed.Change{ProjectID: p.ID, RootID: p.Roots[0].ID, Path: "note.txt",
			WorkspaceID: p.WorkspaceID(), WorkspaceKind: api.SourceWorkspaceKindProject,
			Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent, AbsPath: path},
		Target: fseffect.Location{Root: root, Rel: "note.txt"},
	}
	journal, err := service.PrepareEffect(t.Context(), effect)
	testutil.FailErr(t, "prepare native write", err)
	return service, journal, path
}

func TestRecoverAgentEffectNeverReplaysFileMutation(t *testing.T) {
	for _, body := range []string{"before", "after", "later user edit"} {
		t.Run(body, func(t *testing.T) {
			service, journal, path := preparedAgentWrite(t)
			testutil.FailErr(t, "set state at crash", os.WriteFile(path, []byte(body), 0o600))
			restarted := NewSourceMutationService(service.Journal.db, service.settlement.recorder.(*sourceledger.Store))
			testutil.FailErr(t, "recover interrupted effect", restarted.Recover(t.Context()))
			row, found, err := restarted.Journal.load(t.Context(), journal.ID())
			testutil.FailErr(t, "read recovered journal", err)
			want := sourceMutationDiverged
			if body == "after" {
				want = sourceMutationCommitted
			}
			if !found || row.Status != want {
				t.Fatalf("journal status = %s, want %s", row.Status, want)
			}
			got, err := os.ReadFile(path)
			testutil.FailErr(t, "read file after recovery", err)
			if string(got) != body {
				t.Fatalf("recovery changed source to %q from %q", got, body)
			}
			testutil.FailErr(t, "repeat recovery", restarted.Recover(t.Context()))
			var count int
			testutil.FailErr(t, "count attributed operations", service.Journal.db.QueryRowContext(t.Context(),
				`SELECT count(*) FROM source_operations WHERE operation_key=?`, journal.ID()).Scan(&count))
			if (body == "after" && count != 1) || (body != "after" && count != 0) {
				t.Fatalf("recovered operations = %d for %q", count, body)
			}
		})
	}
}

func TestAgentEffectRecordingFailureRecoversOriginalBytesAfterLaterEdit(t *testing.T) {
	service, journal, path := preparedAgentWrite(t)
	_, err := service.Journal.db.ExecContext(t.Context(), `CREATE TRIGGER reject_agent_record BEFORE INSERT ON source_operations BEGIN SELECT RAISE(ABORT,'recording unavailable'); END`)
	testutil.FailErr(t, "inject recording failure", err)
	testutil.FailErr(t, "apply native effect", os.WriteFile(path, []byte("after"), 0o600))
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	err = journal.Finish(canceled, nil)
	var applied *sourceeffect.AppliedError
	if !errors.As(err, &applied) || applied.OperationID != journal.ID() {
		t.Fatalf("landed effect error = %v", err)
	}
	row, _, err := service.Journal.load(t.Context(), journal.ID())
	testutil.FailErr(t, "read pending journal", err)
	if row.Status != sourceMutationFileApplied {
		t.Fatalf("status = %s, want file_applied", row.Status)
	}
	testutil.FailErr(t, "make later user edit", os.WriteFile(path, []byte("later"), 0o600))
	_, err = service.Journal.db.ExecContext(t.Context(), `DROP TRIGGER reject_agent_record`)
	testutil.FailErr(t, "restore recording", err)
	restarted := NewSourceMutationService(service.Journal.db, service.settlement.recorder.(*sourceledger.Store))
	testutil.FailErr(t, "recover recording", restarted.Recover(t.Context()))
	row, _, err = restarted.Journal.load(t.Context(), journal.ID())
	testutil.FailErr(t, "read settled journal", err)
	if row.Status != sourceMutationCommitted {
		t.Fatalf("status = %s, want committed", row.Status)
	}
	if row.Plan.AgentEffect.Record.Before != nil || row.Plan.AgentEffect.Record.After != nil {
		t.Fatal("completed journal duplicated bodies retained by the source ledger")
	}
	var sha string
	testutil.FailErr(t, "read recorded version", service.Journal.db.QueryRowContext(t.Context(),
		`SELECT content_sha256 FROM source_versions WHERE operation_id=(SELECT id FROM source_operations WHERE operation_key=?) AND state='content' ORDER BY seq DESC LIMIT 1`, journal.ID()).Scan(&sha))
	if sha != textfile.SHA256([]byte("after")) {
		t.Fatalf("recorded content hash = %s", sha)
	}
	got, err := os.ReadFile(path)
	testutil.FailErr(t, "read later edit", err)
	if string(got) != "later" {
		t.Fatalf("recovery overwrote later edit: %q", got)
	}
}

func TestSourceEffectReadmissionRefusesFilesystemReplay(t *testing.T) {
	service, pending, path := preparedAgentWrite(t)
	row, _, err := service.Journal.load(t.Context(), pending.ID())
	testutil.FailErr(t, "read prepared effect", err)
	original := *row.Plan.AgentEffect
	testutil.FailErr(t, "apply prepared bytes", os.WriteFile(path, original.Record.After, 0o600))
	testutil.FailErr(t, "finish first effect", pending.Finish(t.Context(), nil))
	testutil.FailErr(t, "human edits after completion", os.WriteFile(path, []byte("human"), 0o600))
	apply := func() error {
		pending, err := service.PrepareEffect(t.Context(), original)
		if err != nil {
			return err
		}
		return pending.Finish(t.Context(), os.WriteFile(path, original.Record.After, 0o600))
	}
	if err := apply(); !errors.Is(err, ErrSourceMutationConflict) {
		t.Fatalf("readmission error=%v", err)
	}
	var count int
	testutil.FailErr(t, "count source receipts", service.Journal.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_operations WHERE operation_key=?`, pending.ID()).Scan(&count))
	if count != 1 {
		t.Fatalf("source receipts=%d", count)
	}
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read human bytes", err)
	if string(raw) != "human" {
		t.Fatalf("file=%q", raw)
	}
	original.Record.Cause = "different operation"
	_, err = service.PrepareEffect(t.Context(), original)
	if !errors.Is(err, ErrSourceMutationConflict) {
		t.Fatalf("changed input err=%v", err)
	}
}
