//go:build darwin || linux

package sourceapi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOutsideNavigationFollowsTheRecordedFile(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "chat", "p1")
	ledger := sourceledger.New(database, filepath.Join(t.TempDir(), "content"))
	outside, err := db.New(database).GetProjectOutsideRootID(ctx, "p1")
	testutil.FailErr(t, "read outside namespace", err)
	p := &project.Project{ID: "p1", Outside: project.Root{ID: outside, ProjectID: "p1", Path: "/", Kind: project.RootKindOutside}}
	root := t.TempDir()
	old, moved := filepath.Join(root, "old.txt"), filepath.Join(root, "moved.txt")
	oldRel, _ := projectroot.OutsideRel(fspath.CanonicalPath(old))
	movedRel, _ := projectroot.OutsideRel(fspath.CanonicalPath(moved))
	content := []byte("original")
	testutil.FailErr(t, "write fixture bytes", os.WriteFile(moved, content, 0o600))
	write := sourceledger.RecordInput{
		ProjectID: p.ID, SessionID: "chat",
		RecordLocation: sourceledger.RecordLocation{RootID: outside, Path: oldRel},
		Origin:         api.SourceChangeOriginAgent, Op: api.SourceChangeOpCreate, After: content,
	}
	testutil.FailErr(t, "record initial file", ledger.Record(ctx, write))
	head, err := ledger.History.ResolveHead(ctx, p.ID, sourcebranch.Trunk, outside, oldRel)
	testutil.FailErr(t, "resolve initial file identity", err)
	rename := write
	rename.Path, rename.FromPath, rename.Op = movedRel, oldRel, api.SourceChangeOpRename
	testutil.FailErr(t, "record outside rename", ledger.Record(ctx, rename))
	testutil.FailErr(t, "reuse old address", os.WriteFile(old, []byte("replacement"), 0o600))
	testutil.FailErr(t, "record replacement at original address", ledger.Record(ctx, write))

	workspace := &Workspace{SourceLedger: ledger}
	ref := api.NavigationReference{
		ProjectID: p.ID, RootID: outside, Path: oldRel, FileID: head.FileID,
		EntryKind: api.NavigationEntryKindFile, Status: api.NavigationResolved,
	}
	resolved := workspace.ResolveNavigationPaths(ctx, p, sourcebranch.Trunk, []api.NavigationReference{ref})[0]
	if resolved.Status != api.NavigationResolved || resolved.Path != movedRel || resolved.FileID != head.FileID {
		t.Fatalf("stable reference resolved to %+v", resolved)
	}
	_, err = database.ExecContext(ctx, `DELETE FROM source_chat_files WHERE file_id = ?`, head.FileID)
	testutil.FailErr(t, "remove chat reference", err)
	released := workspace.ResolveNavigationPaths(ctx, p, sourcebranch.Trunk, []api.NavigationReference{ref})[0]
	if released.Status != api.NavigationUnavailable {
		t.Fatalf("unheld outside file resolved to %+v", released)
	}

	attached, err := project.NewSQLRegistry(database).AttachRoot(ctx, p.ID, project.AttachRootParams{Path: root})
	testutil.FailErr(t, "attach the containing folder", err)
	adopted := workspace.ResolveNavigationPaths(ctx, attached.After, sourcebranch.Trunk, []api.NavigationReference{ref})[0]
	if adopted.Status != api.NavigationResolved || adopted.RootID != attached.Added.ID || adopted.Path != "moved.txt" || adopted.FileID != head.FileID {
		t.Fatalf("adopted reference resolved to %+v", adopted)
	}
	ref.FileID = "unknown"
	invalid := workspace.ResolveNavigationPaths(ctx, p, sourcebranch.Trunk, []api.NavigationReference{ref})[0]
	if invalid.Status != api.NavigationUnavailable {
		t.Fatalf("unknown identity resolved to %+v", invalid)
	}
}
