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
