package editordoc

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/project/worktrees"
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
	head, err := ledger.History.ResolveHeadByFile(t.Context(), other.ID, other.SourceBranch, separate.FileID)
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

func TestOrphanedCheckoutDocumentLifecycleAndStatuses(t *testing.T) {
	f, _ := newLedgerAgentFixture(t, map[string]string{"a.txt": "base\n"})
	trunkDoc := f.open(t, "a.txt")
	if liveP, err := f.service.LiveWorkspace(t.Context(), f.project, trunkDoc.ID); err != nil || liveP.ID != f.project.ID {
		t.Fatalf("trunk LiveWorkspace = %v, %v", liveP, err)
	}
	if wsP, err := f.service.Workspace(t.Context(), f.project, trunkDoc.ID); err != nil || wsP.ID != f.project.ID {
		t.Fatalf("trunk Workspace = %v, %v", wsP, err)
	}
	if reopenedTrunk, err := f.service.OpenByID(t.Context(), f.project.ID, trunkDoc.ID, "", "window-1", nil); err != nil || reopenedTrunk.ID != trunkDoc.ID {
		t.Fatalf("trunk OpenByID = %v, %v", reopenedTrunk, err)
	}

	wtRoot := t.TempDir()
	testutil.FailErr(t, "write wt file", os.WriteFile(filepath.Join(wtRoot, "a.txt"), []byte("wt base\n"), 0o600))
	wtProj := withRootRefs(f.project, []projectroot.RootRef{{ID: f.rootID, Path: wtRoot}})
	wtProj.SourceBranch = sourcebranch.ForWorktree(uuid.NewString())
	wtDoc, err := f.service.Open(t.Context(), wtProj, "a.txt", f.rootID, "", "window-1", nil)
	testutil.FailErr(t, "open wt doc", err)

	if _, err := f.service.LiveWorkspace(t.Context(), f.project, wtDoc.ID); !errors.Is(err, worktrees.ErrCheckoutUnavailable) {
		t.Fatalf("orphaned LiveWorkspace err = %v", err)
	}
	if fallbackP, err := f.service.Workspace(t.Context(), f.project, wtDoc.ID); err != nil || fallbackP.ID != f.project.ID {
		t.Fatalf("orphaned Workspace fallback = %v, %v", fallbackP, err)
	}
	if _, err := f.service.OpenByID(t.Context(), f.project.ID, wtDoc.ID, "", "window-2", nil); !errors.Is(err, worktrees.ErrCheckoutUnavailable) {
		t.Fatalf("clean orphaned OpenByID err = %v", err)
	}

	statuses, err := f.service.Statuses(t.Context(), f.project.ID, []string{trunkDoc.ID, wtDoc.ID})
	testutil.FailErr(t, "statuses", err)
	var sawWTStatus bool
	for _, st := range statuses.Documents {
		if st.ID == wtDoc.ID {
			sawWTStatus = st.CheckoutUnavailable && st.Absent
		}
	}
	if !sawWTStatus {
		t.Fatalf("statuses missing unavailable checkout flag: %+v", statuses.Documents)
	}

	observed, err := f.service.ObserveByID(t.Context(), f.project.ID, wtDoc.ID, "window-1")
	testutil.FailErr(t, "ObserveByID orphaned", err)
	if !observed.CheckoutUnavailable || !observed.Absent {
		t.Fatalf("observed = %+v", observed)
	}

	edited, err := f.service.ReplaceSnapshot(t.Context(), wtDoc.ID, f.project.ID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "window-1", OperationID: uuid.NewString(), ExpectedRevision: observed.Revision},
		Content:         "orphaned draft\n", EOL: "lf",
	})
	testutil.FailErr(t, "edit orphaned draft", err)

	reopened, err := f.service.OpenByID(t.Context(), f.project.ID, edited.ID, "", "window-2", nil)
	testutil.FailErr(t, "OpenByID orphaned draft", err)
	if !reopened.CheckoutUnavailable || reopened.Draft != "orphaned draft\n" {
		t.Fatalf("reopened = %+v", reopened)
	}

	joined, err := f.service.Join(t.Context(), edited.ID, f.project.ID, ReplicaJoin{
		ClientID: "window-3", Incarnation: "inc-3",
	})
	testutil.FailErr(t, "Join orphaned draft", err)
	if !joined.CheckoutUnavailable {
		t.Fatalf("joined = %+v", joined)
	}
}
