//go:build darwin || linux

package native

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestNativeMoveAndAttachKeepFileAndDocumentIdentity(t *testing.T) {
	ctx := t.Context()
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "find home directory", err)
	// Default temporary roots are intentionally excluded from outside history.
	base, err := os.MkdirTemp(home, "painted-wolf-outside-move-")
	testutil.FailErr(t, "create outside fixture", err)
	t.Cleanup(func() { testutil.FailErr(t, "remove outside fixture", os.RemoveAll(base)) })
	inside, outside := filepath.Join(base, "project"), filepath.Join(base, "other")
	for _, dir := range []string{inside, outside} {
		testutil.FailErr(t, "create fixture folder", os.Mkdir(dir, 0o700))
	}
	st := bindLedgerForWrites(t, inside)
	database := st.LedgerDB()
	testdbseed.InsertSession(t, database, "s1", "p1")
	now := db.FormatTime(time.Now().UTC())
	_, err = database.ExecContext(ctx, `INSERT INTO project_roots(id, project_id, path, label, is_primary, added_at, kind)
		VALUES ('outside', 'p1', '/', 'outside', 0, ?, 'outside')`, now)
	testutil.FailErr(t, "insert outside namespace", err)

	start, moved := filepath.Join(inside, "start.txt"), filepath.Join(outside, "moved.txt")
	testutil.FailErr(t, "seed source", os.WriteFile(start, []byte("original\n"), 0o640))
	original, err := st.TrackFile(ctx, sourceledger.TrackInput{ProjectID: "p1", RootID: "r1", Path: "start.txt",
		EntryKind: sourceledger.EntryKindFile, SHA256: textfile.SHA256([]byte("original\n")), Content: []byte("original\n"), Size: 9})
	testutil.FailErr(t, "track original file", err)
	_, err = database.ExecContext(ctx, `INSERT INTO editor_documents (
		branch_id, id, project_id, file_id, root_id, path, draft, base_content, base_sha256, encoding,
		size_bytes, eol, base_eol, dirty, created_at, updated_at, opened_at
	) VALUES ('', 'doc', 'p1', ?, 'r1', 'start.txt', 'draft', 'original', ?, 'utf-8', 9, 'lf', 'lf', 1, ?, ?, ?)`,
		original.FileID, textfile.SHA256([]byte("original\n")), now, now, now)
	testutil.FailErr(t, "seed dirty document", err)

	tctx := nativefixture.Context(inside)
	tctx.Identity.ProjectID, tctx.Identity.SessionID, tctx.Identity.UserTurn = "p1", "s1", 1
	tctx.Source.OutsideRoot = projectroot.OutsideRef("outside")
	tctx.Source.SourceLedger = st
	tctx.Source.SourceMutations = projectsource.NewSourceMutationService(database, st)
	tctx.Files.ApprovedFileAccess = []hitl.GrantedPathDelta{{Path: fspath.CanonicalPath(moved), Write: true}}
	tool := &MoveTool{Boundary: nativefixture.Boundary(t)}
	move := func(from, to string) {
		t.Helper()
		result, err := tool.Move(ctx, MoveInput{Pairs: []FilePair{{From: from, To: to}}}, tctx)
		testutil.FailErr(t, "move between namespaces", err)
		if len(result.Moved) != 1 {
			t.Fatalf("move result = %+v", result)
		}
	}
	checkAddress := func(rootID, path string) {
		t.Helper()
		head, err := st.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, rootID, path)
		testutil.FailErr(t, "resolve moved head", err)
		if head.FileID != original.FileID {
			t.Fatalf("head identity = %s, want %s", head.FileID, original.FileID)
		}
		var docRoot, docPath, docDraft string
		var dirty bool
		testutil.FailErr(t, "check document address", database.QueryRowContext(ctx,
			`SELECT root_id, path, draft, dirty FROM editor_documents WHERE id = 'doc' AND file_id = ?`,
			original.FileID).Scan(&docRoot, &docPath, &docDraft, &dirty))
		if docRoot != rootID || docPath != path || docDraft != "draft" || !dirty {
			t.Fatalf("document = %s %s %q dirty %v", docRoot, docPath, docDraft, dirty)
		}
	}

	move("start.txt", moved)
	outsidePath, _ := projectroot.OutsideRel(fspath.CanonicalPath(moved))
	checkAddress("outside", outsidePath)
	if _, err := st.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, "r1", "start.txt"); !errors.Is(err, sourceledger.ErrHistoryNotFound) {
		t.Fatalf("old path remains live: %v", err)
	}
	move(moved, "returned.txt")
	checkAddress("r1", "returned.txt")
	move("returned.txt", moved)
	registered, err := project.NewSQLRegistry(database).AttachRoot(ctx, "p1", project.AttachRootParams{Path: outside})
	testutil.FailErr(t, "attach outside folder", err)
	checkAddress(registered.Added.ID, "moved.txt")

	walk, err := st.Walk.QueryWalk(ctx, "p1", sourceledger.Baseline{Kind: sourceledger.BaselineSession, SessionID: "s1"}, 10, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "read move history", err)
	if len(walk.Files) != 1 || len(walk.Files[0].Effects) != 3 {
		t.Fatalf("move history = %+v", walk.Files)
	}
	for _, effect := range walk.Files[0].Effects {
		if effect.Op != api.SourceChangeOpRename || effect.FromRootID == "" || effect.FromRootID == effect.RootID {
			t.Fatalf("move effect = %+v", effect)
		}
		comparison, err := st.Comparisons.CompareEffect(ctx, "p1", effect.ID)
		testutil.FailErr(t, "compare cross-root rename", err)
		if !comparison.LocationChanged || comparison.Before.Content != "original\n" || comparison.After.Content != "original\n" {
			t.Fatalf("rename comparison = %+v", comparison)
		}
	}
}
