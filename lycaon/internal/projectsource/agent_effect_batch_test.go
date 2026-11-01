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
	"github.com/lycaon/lycaon/internal/toolcontract"
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
	batch, err := service.PrepareEffects(t.Context(), testSourceOperationMemory(t), effects)
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
	err = batch.Record(canceled, []sourceeffect.Completion{{Attempted: true, Result: result}, {Attempted: true, Err: refusal}, {}})
	testutil.FailErr(t, "record canceled partial page", err)
	for i, id := range ids {
		row, _, err := service.Journal.load(t.Context(), id)
		testutil.FailErr(t, "read durable completion before settlement", err)
		want := sourceMutationFailed
		if i == 0 {
			want = sourceMutationFileApplied
		}
		if row.Status != want {
			t.Fatalf("entry %d recorded status=%s", i, row.Status)
		}
		var count int
		testutil.FailErr(t, "count unsettled attribution", service.Journal.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_operations WHERE operation_key=?`, id).Scan(&count))
		if count != 0 {
			t.Fatalf("entry %d settled before the ledger transaction", i)
		}
	}
	testutil.FailErr(t, "settle canceled page", service.SettleEffects(canceled, []sourceeffect.PendingEffects{batch}))
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
	if err := batch.Record(t.Context(), nil); err == nil {
		t.Fatal("batch settled twice")
	}
}

func batchFilePlan(p *Project, root, name, body string) sourceeffect.Plan {
	return sourceeffect.Plan{
		Record: sourceledger.RecordInput{RecordLocation: sourceledger.RecordLocation{RootID: p.Roots[0].ID, Path: name, EntryKind: sourceledger.EntryKindFile}, ProjectID: p.ID, Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, After: []byte(body), AfterSHA256: textfile.SHA256([]byte(body))},
		Change: sourcefeed.Change{ProjectID: p.ID, RootID: p.Roots[0].ID, Path: name, WorkspaceID: p.WorkspaceID(), WorkspaceKind: api.SourceWorkspaceKindProject, Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, AbsPath: filepath.Join(root, name)},
		Target: fseffect.Location{Root: root, Rel: name},
	}
}

func TestAgentEffectBatchAppliedFailureRecoversWithoutReplayingFiles(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	memory := testSourceOperationMemory(t)
	var batches []sourceeffect.PendingEffects
	var ids, hashes []string
	for i := range 2 {
		body := fmt.Sprintf("original%d", i)
		effect := batchFilePlan(p, root, fmt.Sprintf("f%d", i), body)
		batch, err := service.PrepareEffects(t.Context(), memory, []sourceeffect.Plan{effect})
		testutil.FailErr(t, "prepare separate publication page", err)
		batches = append(batches, batch)
		ids, hashes = append(ids, batch.IDs()[0]), append(hashes, effect.Record.AfterSHA256)
		result, err := fseffect.Replace(fseffect.ReplaceRequest{Location: effect.Target, Source: strings.NewReader(body), Mode: 0o644})
		testutil.FailErr(t, "publish source", err)
		testutil.FailErr(t, "record exact applied marker", batch.Record(t.Context(), []sourceeffect.Completion{{Attempted: true, Result: result}}))
	}
	_, err := service.Journal.db.ExecContext(t.Context(), fmt.Sprintf(`CREATE TRIGGER reject_batch_record BEFORE INSERT ON source_operations WHEN NEW.operation_key='%s' BEGIN SELECT RAISE(ABORT,'recording unavailable'); END`, ids[1]))
	testutil.FailErr(t, "inject second recording failure", err)
	err = service.SettleEffects(t.Context(), batches)
	var applied *sourceeffect.AppliedError
	if !errors.As(err, &applied) || applied.OperationID != ids[0] {
		t.Fatalf("applied failure=%v", err)
	}
	if used := memory.InUse(); used != (toolcontract.ProgramMemoryUsage{}) {
		t.Fatalf("failed settlement retained memory: %+v", used)
	}
	for i, id := range ids {
		row, _, err := service.Journal.load(t.Context(), id)
		testutil.FailErr(t, "read applied intent", err)
		if row.Status != sourceMutationFileApplied {
			t.Fatalf("failed settlement status=%s", row.Status)
		}
		var count int
		testutil.FailErr(t, "count rolled-back attribution", service.Journal.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_operations WHERE operation_key=?`, id).Scan(&count))
		if count != 0 {
			t.Fatalf("partial ledger transaction escaped for %s", id)
		}
		testutil.FailErr(t, "make later edit", os.WriteFile(filepath.Join(root, fmt.Sprintf("f%d", i)), []byte("later"), 0o644))
	}
	_, err = service.Journal.db.ExecContext(t.Context(), `DROP TRIGGER reject_batch_record`)
	testutil.FailErr(t, "restore recording", err)
	restarted := NewSourceMutationService(service.Journal.db, service.settlement.recorder.(*sourceledger.Store))
	testutil.FailErr(t, "recover applied pages", restarted.Recover(t.Context()))
	for i, id := range ids {
		got, err := os.ReadFile(filepath.Join(root, fmt.Sprintf("f%d", i)))
		testutil.FailErr(t, "read later edit", err)
		if string(got) != "later" {
			t.Fatalf("recovery replayed filesystem effect: %q", got)
		}
		var sha string
		testutil.FailErr(t, "read exact original attribution", service.Journal.db.QueryRowContext(t.Context(), `SELECT content_sha256 FROM source_versions WHERE operation_id=(SELECT id FROM source_operations WHERE operation_key=?) AND state='content' ORDER BY seq DESC LIMIT 1`, id).Scan(&sha))
		if sha != hashes[i] {
			t.Fatalf("recovery attributed later content: %s", sha)
		}
	}
}

func TestAgentEffectPagesKeepPreparedBytesUntilCombinedSettlement(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	memory := testSourceOperationMemory(t)
	var batches []sourceeffect.PendingEffects
	var ids []string
	for i := range 2 {
		effect := batchFilePlan(p, root, fmt.Sprintf("f%d", i), "original")
		batch, err := service.PrepareEffects(t.Context(), memory, []sourceeffect.Plan{effect})
		testutil.FailErr(t, "prepare page", err)
		ids = append(ids, batch.IDs()[0])
		result, err := fseffect.Replace(fseffect.ReplaceRequest{Location: effect.Target, Source: strings.NewReader("original"), Mode: 0o644})
		testutil.FailErr(t, "publish source", err)
		testutil.FailErr(t, "record page", batch.Record(t.Context(), []sourceeffect.Completion{{Attempted: true, Result: result}}))
		copy(effect.Record.After, "changed!")
		batches = append(batches, batch)
	}
	if err := service.SettleEffects(t.Context(), []sourceeffect.PendingEffects{batches[0], batches[0]}); !errors.Is(err, ErrSourceMutationConflict) {
		t.Fatalf("duplicate settlement token error=%v", err)
	}
	testutil.FailErr(t, "settle original pages", service.SettleEffects(t.Context(), batches))
	for _, id := range ids {
		var sha string
		testutil.FailErr(t, "read prepared attribution", service.Journal.db.QueryRowContext(t.Context(), `SELECT content_sha256 FROM source_versions WHERE operation_id=(SELECT id FROM source_operations WHERE operation_key=?) AND state='content' ORDER BY seq DESC LIMIT 1`, id).Scan(&sha))
		if sha != textfile.SHA256([]byte("original")) {
			t.Fatalf("pending bytes changed before settlement: %s", sha)
		}
	}
	if used := memory.InUse(); used != (toolcontract.ProgramMemoryUsage{}) {
		t.Fatalf("settlement retained memory: %+v", used)
	}
}

func TestAgentEffectBatchAdmitsBeforeIntentAndReleasesAbandonment(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	effect := batchFilePlan(p, root, "file", "original")
	limited, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 32 << 20, State: 1})
	testutil.FailErr(t, "limited memory", err)
	_, err = service.PrepareEffects(t.Context(), limited, []sourceeffect.Plan{effect})
	var limit *toolcontract.ProgramMemoryLimitError
	if !errors.As(err, &limit) || limit.Account != "state" {
		t.Fatalf("pending-record admission error=%v", err)
	}
	var count int
	testutil.FailErr(t, "count refused intents", service.Journal.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_mutations`).Scan(&count))
	if count != 0 || limited.InUse() != (toolcontract.ProgramMemoryUsage{}) {
		t.Fatalf("refusal allocated intent or retained memory: count=%d usage=%+v", count, limited.InUse())
	}
	memory := testSourceOperationMemory(t)
	batch, err := service.PrepareEffects(t.Context(), memory, []sourceeffect.Plan{effect})
	testutil.FailErr(t, "prepare abandoned page", err)
	id := batch.IDs()[0]
	testutil.FailErr(t, "record definite abandonment", batch.Record(t.Context(), []sourceeffect.Completion{{}}))
	batch.Close()
	row, _, err := service.Journal.load(t.Context(), id)
	testutil.FailErr(t, "read abandoned intent", err)
	if row.Status != sourceMutationFailed || memory.InUse() != (toolcontract.ProgramMemoryUsage{}) {
		t.Fatalf("abandonment status=%s usage=%+v", row.Status, memory.InUse())
	}
}
