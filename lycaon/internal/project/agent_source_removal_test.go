package project

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/pkg/api"
)

func agentRemoval(p *Project, path, reviewed string) sourceeffect.Removal {
	return sourceeffect.Removal{
		Record: sourceledger.RecordInput{
			ProjectID: p.ID, RootID: p.Roots[0].ID, Path: path, Op: api.SourceChangeOpDelete,
			Origin: api.SourceChangeOriginAgent, SessionID: "session-1", Turn: 3,
			ToolCallID: "call-1", ToolName: "delete",
		},
		Change: sourcefeed.Change{
			ProjectID: p.ID, WorkspaceID: p.WorkspaceID(), WorkspaceKind: api.SourceWorkspaceKindProject,
		},
		RootPath:       p.Roots[0].Path,
		ReviewedSHA256: reviewed,
	}
}

func refuseTrash(t *testing.T, service *SourceMutationService) {
	t.Helper()
	service.SetTrashMover(func(context.Context, string) error {
		t.Fatal("an agent removal reached the system Trash")
		return nil
	})
}

// Recovery retains bytes and permissions beyond the history content limit.
func TestAgentRemovalKeepsWhatHistoryCannotAndSkipsTrash(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	refuseTrash(t, service)
	body := bytes.Repeat([]byte{0, 1, 2, 255}, sourceledger.MaxRevisionContentBytes/2)
	testutil.FailErr(t, "seed large executable", os.WriteFile(filepath.Join(root, "tool.bin"), body, 0o750))

	id, err := service.RemoveEntry(t.Context(), agentRemoval(p, "tool.bin", textfile.SHA256(body)))
	testutil.FailErr(t, "remove entry", err)
	if _, err := os.Lstat(filepath.Join(root, "tool.bin")); !os.IsNotExist(err) {
		t.Fatalf("removed file remains: %v", err)
	}

	history, err := service.History(t.Context(), p.ID)
	testutil.FailErr(t, "read history", err)
	if history.Undo == nil || history.Undo.ID != id || history.Undo.Kind != "delete" ||
		history.Undo.Label != "Undo deletion of tool.bin" || history.Undo.IsDir {
		t.Fatalf("undo head = %+v", history.Undo)
	}
	undoHistoryHead(t, service, p, id)
	restored, err := os.ReadFile(filepath.Join(root, "tool.bin"))
	testutil.FailErr(t, "read restored file", err)
	info, err := os.Stat(filepath.Join(root, "tool.bin"))
	testutil.FailErr(t, "stat restored file", err)
	if !bytes.Equal(restored, body) || info.Mode().Perm() != 0o750 {
		t.Fatalf("restored %d bytes with mode %v, want %d bytes with 0750", len(restored), info.Mode().Perm(), len(body))
	}
	redoHistoryHead(t, service, p, id)
	if _, err := os.Lstat(filepath.Join(root, "tool.bin")); !os.IsNotExist(err) {
		t.Fatalf("redo left the file in place: %v", err)
	}
}

func TestAgentRemovalIsAttributedToTheToolCall(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	refuseTrash(t, service)
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(root, "note.txt"), []byte("note"), 0o600))
	_, err := service.RemoveEntry(t.Context(), agentRemoval(p, "note.txt", textfile.SHA256([]byte("note"))))
	testutil.FailErr(t, "remove entry", err)
	var origin, session, toolCall, tool string
	var person any
	testutil.FailErr(t, "read operation", service.db.QueryRowContext(t.Context(),
		`SELECT origin, person_id, session_id, tool_call_id, tool_name FROM source_operations WHERE project_id=? ORDER BY committed_ts DESC LIMIT 1`, p.ID).
		Scan(&origin, &person, &session, &toolCall, &tool))
	if origin != string(api.SourceChangeOriginAgent) || person != nil || session != "session-1" || toolCall != "call-1" || tool != "delete" {
		t.Fatalf("operation = origin %s person %v session %s call %s tool %s", origin, person, session, toolCall, tool)
	}
}

// The file the review showed is the only file removed.
func TestAgentRemovalRefusesAFileChangedAfterReview(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	refuseTrash(t, service)
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(root, "note.txt"), []byte("changed"), 0o600))
	_, err := service.RemoveEntry(t.Context(), agentRemoval(p, "note.txt", textfile.SHA256([]byte("reviewed"))))
	if !errors.Is(err, ErrSourceWriteConflict) {
		t.Fatalf("remove changed file = %v", err)
	}
	assertSourceHistoryFile(t, root, "note.txt", "changed")
}

// A folder with contents would remove entries nobody reviewed.
func TestAgentRemovalRefusesAFolderWithContents(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	refuseTrash(t, service)
	testutil.FailErr(t, "seed folder", os.MkdirAll(filepath.Join(root, "dir"), 0o750))
	testutil.FailErr(t, "seed child", os.WriteFile(filepath.Join(root, "dir", "child"), []byte("x"), 0o600))
	if _, err := service.RemoveEntry(t.Context(), agentRemoval(p, "dir", "")); !errors.Is(err, ErrSourceNotEmpty) {
		t.Fatalf("remove folder with contents = %v", err)
	}
	assertSourceHistoryFile(t, root, "dir/child", "x")
}

// A link is removed as a link; undo puts the link back and the target is untouched.
func TestAgentRemovalOfALinkKeepsItsTarget(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	refuseTrash(t, service)
	testutil.FailErr(t, "seed target", os.WriteFile(filepath.Join(root, "target.txt"), []byte("target"), 0o600))
	testutil.FailErr(t, "seed link", os.Symlink("target.txt", filepath.Join(root, "link")))
	id, err := service.RemoveEntry(t.Context(), agentRemoval(p, "link", ""))
	testutil.FailErr(t, "remove link", err)
	assertSourceHistoryFile(t, root, "target.txt", "target")
	undoHistoryHead(t, service, p, id)
	got, err := os.Readlink(filepath.Join(root, "link"))
	testutil.FailErr(t, "read restored link", err)
	if got != "target.txt" {
		t.Fatalf("restored link = %q", got)
	}
}

// A worker overlay is a private copy its promotion records.
func TestAgentRemovalRefusesAWorkerBranch(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(root, "note.txt"), []byte("note"), 0o600))
	removal := agentRemoval(p, "note.txt", textfile.SHA256([]byte("note")))
	worker, err := sourcebranch.ForWorker("job-1")
	testutil.FailErr(t, "worker branch", err)
	removal.Record.BranchID = worker
	if _, err := service.RemoveEntry(t.Context(), removal); err == nil {
		t.Fatal("worker-branch removal accepted")
	}
	assertSourceHistoryFile(t, root, "note.txt", "note")
}
