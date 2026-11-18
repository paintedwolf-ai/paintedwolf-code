package editordoc

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
)

var autosaveCall = AgentCall{SessionID: "chat-1", Turn: 2, ToolCallID: "call-autosave", ToolName: "command"}

func (f agentFixture) typeDraft(t *testing.T, d *Document, content string) *Document {
	t.Helper()
	typed, err := f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision}, Content: content, EOL: "lf"})
	testutil.FailErr(t, "type draft", err)
	return typed
}

// An agent operation saves the person's draft first, and the saved bytes stay
// the person's own contribution.
func TestSaveForAgentOperationSavesThePersonsDraftUnderTheirAuthorship(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"other.txt": "outside\n"})
	testutil.FailErr(t, "make directory", os.Mkdir(filepath.Join(f.root, "dir"), 0o755))
	f.write(t, "dir/a.txt", "saved\n")
	f.write(t, "dir/b.txt", "clean\n")
	a := f.typeDraft(t, f.open(t, "dir/a.txt"), "typed by the person\n")
	other := f.typeDraft(t, f.open(t, "other.txt"), "untouched draft\n")
	f.open(t, "dir/b.txt")
	testutil.FailErr(t, "save subtree", f.service.SaveForAgentOperation(t.Context(), f.project.ID, f.project.SourceBranch, a.RootID, "dir", true, autosaveCall))
	if got := f.disk(t, "dir/a.txt"); got != "typed by the person\n" {
		t.Fatalf("disk = %q", got)
	}
	stored, err := f.store.Get(t.Context(), a.ID)
	testutil.FailErr(t, "read saved document", err)
	if stored.Dirty {
		t.Fatalf("document still dirty: %+v", stored)
	}
	if f.disk(t, "other.txt") != "outside\n" {
		t.Fatal("a draft outside the operation's path was saved")
	}
	if kept, err := f.store.Get(t.Context(), other.ID); err != nil || !kept.Dirty {
		t.Fatalf("draft outside the path = %+v, %v", kept, err)
	}
	var agentContributions, personContributions int
	testutil.FailErr(t, "count contributions", f.store.db.QueryRowContext(t.Context(),
		`SELECT COALESCE(SUM(origin='agent'),0), COALESCE(SUM(origin!='agent'),0) FROM source_text_contributions WHERE document_id=?`, a.ID).Scan(&agentContributions, &personContributions))
	if agentContributions != 0 || personContributions == 0 {
		t.Fatalf("contributions: agent=%d person=%d", agentContributions, personContributions)
	}
}

// A draft held over a file that changed outside it is not saved over that change.
func TestSaveForAgentOperationRefusesAFileChangedOutsideItsDocument(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "saved\n"})
	d := f.typeDraft(t, f.open(t, "a.txt"), "typed\n")
	f.write(t, "a.txt", "external\n")
	err := f.service.SaveForAgentOperation(t.Context(), f.project.ID, f.project.SourceBranch, d.RootID, "a.txt", false, autosaveCall)
	var conflict *ExecutionConflict
	if !errors.As(err, &conflict) || conflict.Path != "a.txt" || conflict.Pinned.DiskSHA256 == conflict.Current.DiskSHA256 || conflict.Current.Absent {
		t.Fatalf("err=%v", err)
	}
	if f.disk(t, "a.txt") != "external\n" {
		t.Fatal("the outside change was overwritten")
	}
	if kept, err := f.store.Get(t.Context(), d.ID); err != nil || kept.Draft != "typed\n" {
		t.Fatalf("draft = %+v, %v", kept, err)
	}
	testutil.FailErr(t, "remove file", os.Remove(filepath.Join(f.root, "a.txt")))
	err = f.service.SaveForAgentOperation(t.Context(), f.project.ID, f.project.SourceBranch, d.RootID, "a.txt", false, autosaveCall)
	if !errors.As(err, &conflict) || !conflict.Current.Absent {
		t.Fatalf("deleted file: err=%v", err)
	}
}
