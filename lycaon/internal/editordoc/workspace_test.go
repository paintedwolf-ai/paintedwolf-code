package editordoc

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPhysicalWorkspacesNeverShareDocumentsOrPublicationTargets(t *testing.T) {
	f, ledger := newLedgerAgentFixture(t, map[string]string{"a.txt": "base tree\n"})
	otherRoot := t.TempDir()
	testutil.FailErr(t, "write other workspace", os.WriteFile(filepath.Join(otherRoot, "a.txt"), []byte("other tree\n"), 0o600))
	other := project.WithRootRefs(f.project, []projectroot.RootRef{{ID: f.rootID, Path: otherRoot}})
	other.SourceBranch = sourcebranch.ForWorktree(uuid.NewString())
	base := f.open(t, "a.txt")
	separate, err := f.service.Open(t.Context(), other, "a.txt", f.rootID, "", "other-window", nil)
	testutil.FailErr(t, "open same logical path in other workspace", err)
	if base.ID == separate.ID || base.FileID == separate.FileID || base.WorkspaceID == separate.WorkspaceID {
		t.Fatal("physical workspaces shared document or saved history identity")
	}
	separate, err = f.service.ReplaceSnapshot(t.Context(), separate.ID, other.ID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "other-window", OperationID: uuid.NewString(), ExpectedRevision: separate.Revision},
		Content:         "other draft\n", EOL: "lf",
	})
	testutil.FailErr(t, "edit other workspace", err)
	_, err = f.service.Save(t.Context(), f.project, separate.ID, "other-window", uuid.NewString(), "", 0, separate.Revision)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace publication error = %v", err)
	}
	_, err = f.service.ObserveDisk(t.Context(), f.project, separate.ID, "other-window")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace observation error = %v", err)
	}
	f.service.roots = fixedRoots{p: other}
	result, err := f.service.ApplyAgentEdit(t.Context(), agentEdit(separate, "other draft\nagent\n"))
	testutil.FailErr(t, "publish agent edit in addressed workspace", err)
	if !result.Saved || f.disk(t, "a.txt") != "base tree\n" {
		t.Fatal("agent publication changed the base tree")
	}
	raw, err := os.ReadFile(filepath.Join(otherRoot, "a.txt"))
	testutil.FailErr(t, "read published workspace", err)
	if string(raw) != "other draft\nagent\n" {
		t.Fatalf("wrong workspace bytes: %q", raw)
	}
	head, err := ledger.ResolveHeadByFile(t.Context(), other.ID, other.SourceBranch, separate.FileID)
	testutil.FailErr(t, "read independent history", err)
	if head.VersionID == "" {
		t.Fatal("workspace publication has no saved history")
	}
}

func TestWorktreeSharesDocumentsInUnchangedRoots(t *testing.T) {
	f, _ := newLedgerAgentFixture(t, map[string]string{"a.txt": "shared\n"})
	otherRoot := t.TempDir()
	f.project.Roots = append(f.project.Roots, project.Root{ID: "other-root", Path: otherRoot})
	worktree := project.WithWorktree(f.project, project.Binding{ID: uuid.NewString(), Toplevel: otherRoot, WorktreePath: t.TempDir()})
	base := f.open(t, "a.txt")
	shared, err := f.service.Open(t.Context(), worktree, "a.txt", f.rootID, "", "other-window", nil)
	testutil.FailErr(t, "open unchanged root through worktree", err)
	if shared.ID != base.ID || shared.FileID != base.FileID {
		t.Fatal("unchanged root forked its document or history")
	}
	shared, err = f.service.ReplaceSnapshot(t.Context(), shared.ID, shared.ProjectID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "other-window", OperationID: uuid.NewString(), ExpectedRevision: shared.Revision},
		Content:         "shared typing\n", EOL: "lf",
	})
	testutil.FailErr(t, "type in shared root", err)
	_, err = f.service.Save(t.Context(), worktree, shared.ID, "other-window", uuid.NewString(), "", 0, shared.Revision)
	testutil.FailErr(t, "publish shared root through worktree", err)
	if f.disk(t, "a.txt") != "shared typing\n" {
		t.Fatal("shared root publication lost text")
	}
}

func TestAgentReadStartsCollaborationForClosedText(t *testing.T) {
	f, _ := newLedgerAgentFixture(t, map[string]string{"a.txt": "closed\n"})
	doc, ok, err := f.service.OpenText(t.Context(), f.project.ID, sourcebranch.Trunk, f.rootID, "a.txt")
	testutil.FailErr(t, "read closed text", err)
	if !ok || doc.ID == "" {
		t.Fatal("closed editable text bypassed collaboration")
	}
	result, err := f.service.ApplyAgentEdit(t.Context(), agentEdit(doc, "agent\n"))
	testutil.FailErr(t, "edit closed text", err)
	opened := f.open(t, "a.txt")
	if opened.ID != doc.ID || !result.Saved || opened.Draft != "agent\n" {
		t.Fatal("opening lost closed-file collaborative history")
	}
}

func TestCurrentSnapshotCarriesItsWorkspace(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "first"})
	opened := f.open(t, "a.txt")
	current, err := f.service.CurrentSnapshot(t.Context(), f.project.ID, opened.ID)
	testutil.FailErr(t, "read current document", err)
	if current.WorkspaceID == "" || current.WorkspaceID != opened.WorkspaceID {
		t.Fatalf("current snapshot workspace = %q, opened = %q", current.WorkspaceID, opened.WorkspaceID)
	}
}
