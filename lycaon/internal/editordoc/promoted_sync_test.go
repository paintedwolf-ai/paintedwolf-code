package editordoc

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
)

func promotedSync(f externalFixture, path string, turn int) PromotedDocumentSync {
	return PromotedDocumentSync{
		RootID: f.rootID, Path: path, SessionID: "coord-session", JobID: "worker-job-42",
		Turn: turn, ToolCallID: "call-promote", ToolName: "promote_overlay",
	}
}

func holdPromoted(t *testing.T, f externalFixture, paths ...string) PromotedDocumentHold {
	t.Helper()
	refs := make([]PathRef, 0, len(paths))
	for _, path := range paths {
		refs = append(refs, PathRef{RootID: f.rootID, Path: path})
	}
	hold, err := f.service.HoldPromotedDocuments(t.Context(), f.project, refs)
	testutil.FailErr(t, "hold promoted documents", err)
	return hold
}

// promote stages and commits one promotion the way the worker promotion transaction does.
func promote(t *testing.T, f externalFixture, hold PromotedDocumentHold, syncs ...PromotedDocumentSync) map[PathRef]PromotedDocumentResult {
	t.Helper()
	results := hold.Stage(t.Context(), syncs)
	testutil.FailErr(t, "commit promotion", f.store.Tx(t.Context(), func(tx *sql.Tx) error { return hold.CommitTx(t.Context(), tx) }))
	hold.Release(t.Context(), true)
	return results
}

func contributionCount(t *testing.T, f externalFixture, documentID string) int {
	t.Helper()
	var count int
	testutil.FailErr(t, "count contributions", f.store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_text_contributions WHERE document_id=?`, documentID).Scan(&count))
	return count
}

func TestPromotedDocumentCommitsWithAgentAttribution(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base\n"})
	document := f.open(t, "a.txt")
	*f.changes = nil
	hold := holdPromoted(t, f, "a.txt")
	promotedContent := "base\npromoted line\n"
	f.write(t, "a.txt", promotedContent)

	results := promote(t, f, hold, promotedSync(f, "a.txt", 2))
	res, ok := results[PathRef{RootID: f.rootID, Path: "a.txt"}]
	if !ok || res.DocumentID != document.ID || res.TextBefore == nil || res.TextAfter == nil {
		t.Fatalf("promotion result = %+v found=%v", res, ok)
	}
	stored, err := f.store.Get(t.Context(), document.ID)
	testutil.FailErr(t, "reload document", err)
	if stored.Draft != promotedContent || stored.BaseContent != promotedContent || stored.Dirty {
		t.Fatalf("document not promoted: %+v", stored)
	}
	if stored.BaseSHA256 != textfile.SHA256([]byte(promotedContent)) {
		t.Fatalf("BaseSHA256=%q", stored.BaseSHA256)
	}
	if res.TextAfter.Revision != stored.Revision {
		t.Fatalf("text after revision=%d want %d", res.TextAfter.Revision, stored.Revision)
	}
	var origin, sessionID, jobID, toolCallID, toolName string
	var turn int
	row := f.store.db.QueryRowContext(t.Context(), `SELECT origin, session_id, job_id, turn, tool_call_id, tool_name FROM source_text_contributions WHERE document_id=? AND revision=?`, document.ID, stored.Revision)
	testutil.FailErr(t, "query contribution", row.Scan(&origin, &sessionID, &jobID, &turn, &toolCallID, &toolName))
	if origin != "agent" || sessionID != "coord-session" || jobID != "worker-job-42" || turn != 2 || toolCallID != "call-promote" || toolName != "promote_overlay" {
		t.Fatalf("contribution = %s %s %s %d %s %s", origin, sessionID, jobID, turn, toolCallID, toolName)
	}
	if len(*f.changes) == 0 {
		t.Fatal("committed promotion published no document change")
	}

	testutil.FailErr(t, "observe after promotion", f.service.ObserveExternal(t.Context(), f.project, nil))
	if n := contributionCount(t, f, document.ID); n != 1 {
		t.Fatalf("contributions = %d, want only the promotion", n)
	}
	after, err := f.store.Get(t.Context(), document.ID)
	testutil.FailErr(t, "reload after observe", err)
	if after.Revision != stored.Revision {
		t.Fatalf("observation advanced revision %d -> %d", stored.Revision, after.Revision)
	}
}

func TestFailedPromotionLeavesDocumentUnchanged(t *testing.T) {
	for _, stage := range []string{"staged", "written"} {
		t.Run(stage, func(t *testing.T) {
			f := newExternalFixture(t, map[string]string{"a.txt": "base\n"})
			document := f.open(t, "a.txt")
			before := contributionCount(t, f, document.ID)
			*f.changes = nil
			hold := holdPromoted(t, f, "a.txt")
			f.write(t, "a.txt", "promoted\n")
			if results := hold.Stage(t.Context(), []PromotedDocumentSync{promotedSync(f, "a.txt", 1)}); len(results) != 1 {
				t.Fatalf("staged results = %+v", results)
			}
			if stage == "written" {
				rollback := errors.New("promotion commit failed")
				err := f.store.Tx(t.Context(), func(tx *sql.Tx) error {
					if err := hold.CommitTx(t.Context(), tx); err != nil {
						return err
					}
					return rollback
				})
				if !errors.Is(err, rollback) {
					t.Fatalf("commit = %v", err)
				}
			}
			// The promotion rolls its bytes back before releasing the hold.
			f.write(t, "a.txt", "base\n")
			hold.Release(t.Context(), false)

			stored, err := f.store.Get(t.Context(), document.ID)
			testutil.FailErr(t, "reload document", err)
			if stored.Revision != document.Revision || stored.Draft != "base\n" || stored.BaseContent != "base\n" {
				t.Fatalf("failed promotion changed document: %+v", stored)
			}
			if n := contributionCount(t, f, document.ID); n != before {
				t.Fatalf("contributions = %d, want %d", n, before)
			}
			if len(*f.changes) != 0 {
				t.Fatalf("failed promotion published changes: %+v", *f.changes)
			}
			// The discarded import must not survive in the live replica.
			checkpoint, err := f.service.publicationCheckpoint(t.Context(), stored)
			testutil.FailErr(t, "read replica after failed promotion", err)
			if len(checkpoint) == 0 {
				t.Fatal("replica checkpoint missing")
			}
		})
	}
}

func TestHeldPromotionWinsExternalObservationRace(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base\n"})
	f.service.SetSourceMutations(projectsource.NewSourceMutationService(f.store.db, sourceledger.New(f.store.db, "")))
	document := f.open(t, "a.txt")
	hold := holdPromoted(t, f, "a.txt")
	f.write(t, "a.txt", "base\npromoted\n")

	// The watcher observes the landed bytes before the promotion stages them.
	testutil.FailErr(t, "observe during promotion", f.service.ObserveExternal(t.Context(), f.project, []PathRef{{RootID: f.rootID, Path: "a.txt"}}))
	observed, err := f.store.Get(t.Context(), document.ID)
	testutil.FailErr(t, "reload observed document", err)
	if observed.Revision != document.Revision {
		t.Fatal("external observation imported bytes reserved by the promotion")
	}

	results := promote(t, f, hold, promotedSync(f, "a.txt", 4))
	res := results[PathRef{RootID: f.rootID, Path: "a.txt"}]
	if res.TextBefore == nil || res.TextAfter == nil {
		t.Fatalf("promotion lost text states: %+v", res)
	}
	var jobID string
	stored, err := f.store.Get(t.Context(), document.ID)
	testutil.FailErr(t, "reload promoted document", err)
	testutil.FailErr(t, "read job attribution", f.store.db.QueryRowContext(t.Context(), `SELECT job_id FROM source_text_contributions WHERE document_id=? AND revision=?`, document.ID, stored.Revision).Scan(&jobID))
	if jobID != "worker-job-42" {
		t.Fatalf("job_id = %q", jobID)
	}
}

func TestReleaseReobservesHeldPathsNotImported(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base\n"})
	f.service.SetSourceMutations(projectsource.NewSourceMutationService(f.store.db, sourceledger.New(f.store.db, "")))
	document := f.open(t, "a.txt")
	hold := holdPromoted(t, f, "a.txt")
	f.write(t, "a.txt", "changed while held\n")
	// A promotion that stages nothing for the path still settles the document on release.
	promote(t, f, hold)
	stored, err := f.store.Get(t.Context(), document.ID)
	testutil.FailErr(t, "reload document", err)
	if stored.BaseContent != "changed while held\n" {
		t.Fatalf("held path was not re-observed: %q", stored.BaseContent)
	}
}

func TestPromotedDocumentMergesDirtyDraft(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base\n"})
	document := f.open(t, "a.txt")
	_, err := f.service.ReplaceSnapshot(t.Context(), document.ID, f.project.ID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: document.Revision},
		Content:         "base\nuser edits\n",
		EOL:             "lf",
	})
	testutil.FailErr(t, "replace snapshot", err)
	hold := holdPromoted(t, f, "a.txt")
	promotedContent := "promoted base\n"
	f.write(t, "a.txt", promotedContent)

	if results := promote(t, f, hold, promotedSync(f, "a.txt", 3)); len(results) != 1 {
		t.Fatalf("results = %+v", results)
	}
	stored, err := f.store.Get(t.Context(), document.ID)
	testutil.FailErr(t, "reload document", err)
	if !stored.Dirty || stored.BaseSHA256 != textfile.SHA256([]byte(promotedContent)) {
		t.Fatalf("dirty draft not merged over promoted base: %+v", stored)
	}
}

func TestPromotedDocumentIgnoresUntrackedPath(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base\n"})
	hold := holdPromoted(t, f, "untracked.txt")
	if results := promote(t, f, hold, promotedSync(f, "untracked.txt", 1)); len(results) != 0 {
		t.Fatalf("untracked path staged: %+v", results)
	}
}
