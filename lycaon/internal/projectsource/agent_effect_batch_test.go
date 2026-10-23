package projectsource

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAgentEffectBatchRetainsIndividualIntentAndDefiniteNonEffects(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	effects := make([]sourceeffect.Plan, 3)
	for i := range effects {
		name := fmt.Sprintf("f%d", i)
		effects[i] = sourceeffect.Plan{
			Record: sourceledger.RecordInput{RecordLocation: sourceledger.RecordLocation{RootID: p.Roots[0].ID, Path: name, EntryKind: sourceledger.EntryKindFile}, ProjectID: p.ID, Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, After: []byte("same"), AfterSHA256: textfile.SHA256([]byte("same"))},
			Change: sourcefeed.Change{ProjectID: p.ID, RootID: p.Roots[0].ID, Path: name, WorkspaceID: p.WorkspaceID(), WorkspaceKind: api.SourceWorkspaceKindProject, Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, AbsPath: filepath.Join(root, name)},
			Target: fseffect.Location{Root: root, Rel: name},
		}
	}
	// Matching bytes on an unattempted destination must not fabricate attribution.
	testutil.FailErr(t, "seed matching non-effect", os.WriteFile(filepath.Join(root, "f2"), []byte("same"), 0o644))
	batch, err := service.PrepareEffects(t.Context(), effects)
	testutil.FailErr(t, "prepare page", err)
	ids := batch.IDs()
	if len(ids) != 3 || ids[0] == ids[1] {
		t.Fatalf("individual identities = %v", ids)
	}
	for _, id := range ids {
		row, found, err := service.Journal.load(t.Context(), id)
		testutil.FailErr(t, "read durable intent", err)
		if !found || row.Status != sourceMutationPrepared {
			t.Fatalf("intent before effects = %+v", row)
		}
	}
	result, err := fseffect.Replace(fseffect.ReplaceRequest{Location: effects[0].Target, Source: strings.NewReader("same"), Mode: 0o644})
	testutil.FailErr(t, "publish first", err)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	refusal := errors.New("second destination refused")
	err = batch.Finish(canceled, []sourceeffect.Completion{{Attempted: true, Result: result}, {Attempted: true, Err: refusal}, {}})
	if !errors.Is(err, refusal) {
		t.Fatalf("partial outcome = %v", err)
	}
	for i, id := range ids {
		row, _, err := service.Journal.load(t.Context(), id)
		testutil.FailErr(t, "read settled intent", err)
		want := sourceMutationFailed
		if i == 0 {
			want = sourceMutationCommitted
		}
		if row.Status != want {
			t.Fatalf("entry %d status = %s", i, row.Status)
		}
		var count int
		testutil.FailErr(t, "count individual attribution", service.Journal.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_operations WHERE operation_key=?`, id).Scan(&count))
		if (i == 0 && count != 1) || (i != 0 && count != 0) {
			t.Fatalf("entry %d attribution count = %d", i, count)
		}
	}
	if err := batch.Finish(t.Context(), nil); err == nil {
		t.Fatal("batch settled twice")
	}
}

func TestAgentEffectBatchAppliedFailureRecoversWithoutReplayingFiles(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	effect := sourceeffect.Plan{
		Record: sourceledger.RecordInput{RecordLocation: sourceledger.RecordLocation{RootID: p.Roots[0].ID, Path: "file", EntryKind: sourceledger.EntryKindFile}, ProjectID: p.ID, Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, After: []byte("original"), AfterSHA256: textfile.SHA256([]byte("original"))},
		Change: sourcefeed.Change{ProjectID: p.ID, RootID: p.Roots[0].ID, Path: "file", WorkspaceID: p.WorkspaceID(), WorkspaceKind: api.SourceWorkspaceKindProject, Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, AbsPath: filepath.Join(root, "file")},
		Target: fseffect.Location{Root: root, Rel: "file"},
	}
	batch, err := service.PrepareEffects(t.Context(), []sourceeffect.Plan{effect})
	testutil.FailErr(t, "prepare page", err)
	ids := batch.IDs()
	result, err := fseffect.Replace(fseffect.ReplaceRequest{Location: effect.Target, Source: strings.NewReader("original"), Mode: 0o644})
	testutil.FailErr(t, "publish source", err)
	_, err = service.Journal.db.ExecContext(t.Context(), `CREATE TRIGGER reject_batch_record BEFORE INSERT ON source_operations BEGIN SELECT RAISE(ABORT,'recording unavailable'); END`)
	testutil.FailErr(t, "inject recording failure", err)
	err = batch.Finish(t.Context(), []sourceeffect.Completion{{Attempted: true, Result: result}})
	var applied *sourceeffect.AppliedError
	if !errors.As(err, &applied) || applied.OperationID != ids[0] {
		t.Fatalf("applied failure=%v", err)
	}
	row, _, err := service.Journal.load(t.Context(), ids[0])
	testutil.FailErr(t, "read applied intent", err)
	if row.Status != sourceMutationFileApplied {
		t.Fatalf("failed settlement status=%s", row.Status)
	}
	testutil.FailErr(t, "make later edit", os.WriteFile(filepath.Join(root, "file"), []byte("later"), 0o644))
	_, err = service.Journal.db.ExecContext(t.Context(), `DROP TRIGGER reject_batch_record`)
	testutil.FailErr(t, "restore recording", err)
	restarted := NewSourceMutationService(service.Journal.db, service.settlement.recorder.(*sourceledger.Store))
	testutil.FailErr(t, "recover applied page", restarted.Recover(t.Context()))
	got, err := os.ReadFile(filepath.Join(root, "file"))
	testutil.FailErr(t, "read later edit", err)
	if string(got) != "later" {
		t.Fatalf("recovery replayed filesystem effect: %q", got)
	}
	var sha string
	testutil.FailErr(t, "read exact original attribution", service.Journal.db.QueryRowContext(t.Context(), `SELECT content_sha256 FROM source_versions WHERE operation_id=(SELECT id FROM source_operations WHERE operation_key=?) AND state='content' ORDER BY seq DESC LIMIT 1`, ids[0]).Scan(&sha))
	if sha != effect.Record.AfterSHA256 {
		t.Fatalf("recovery attributed later content: %s", sha)
	}
}
