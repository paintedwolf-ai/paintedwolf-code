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
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestNativeMoveAndAttachKeepFileAndDocumentIdentity(t *testing.T) {
	ctx := t.Context()
	st, tctx, inside, outside := outsideNativeFixture(t)
	database := st.LedgerDB()
	now := time.Now().UTC().Format(time.RFC3339Nano)
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
	checkAddress(tctx.Source.OutsideRoot.ID, outsidePath)
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

func outsideNativeFixture(t *testing.T) (*sourceledger.Store, tools.ToolContext, string, string) {
	t.Helper()
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
	outsideID, err := db.New(database).GetProjectOutsideRootID(ctx, "p1")
	testutil.FailErr(t, "read outside namespace", err)

	tctx := nativefixture.Context(inside)
	tctx.Identity.ProjectID, tctx.Identity.SessionID, tctx.Identity.UserTurn = "p1", "s1", 1
	tctx.Source.OutsideRoot = projectroot.OutsideRef(outsideID)
	tctx.Source.SourceLedger = st
	tctx.Source.SourceMutations = projectsource.NewSourceMutationService(database, st)
	return st, tctx, inside, outside
}

func TestNativeOutsideMutationsRetainExactEffects(t *testing.T) {
	ctx := t.Context()
	st, tc, _, outside := outsideNativeFixture(t)
	tc.Identity.ToolCallID = "outside-edit"
	name, renamed := filepath.Join(outside, "note.txt"), filepath.Join(outside, "renamed.txt")
	first, edited := []byte("first"), []byte("edited")
	target, next := testMutationTarget(name), testMutationTarget(renamed)
	memory := programDoorMemory(t)
	for _, step := range []struct {
		op            api.SourceChangeOp
		before, after string
		run           func() error
	}{
		{api.SourceChangeOpCreate, "", "first", func() error {
			return applyAgentFile(ctx, tc, target, first, nil, "")
		}},
		{api.SourceChangeOpWrite, "first", "edited", func() error {
			return applyAgentFile(ctx, tc, target, edited, first, textfile.SHA256(first))
		}},
		{api.SourceChangeOpWrite, "edited", "edited", func() error {
			return chmodAgentPath(ctx, tc, target, 0o644)
		}},
		{api.SourceChangeOpRename, "edited", "edited", func() error {
			return renameAgentEntry(ctx, tc, target, next, 0, memory, nil)
		}},
		{api.SourceChangeOpDelete, "edited", "", func() error {
			return removeAgentPath(ctx, tc, next, memory)
		}},
	} {
		tc.Effects.Out = &tools.ToolInvocationOut{}
		testutil.FailErr(t, "apply native outside operation", step.run())
		walk, err := st.Walk.QueryWalk(ctx, "p1", sourceledger.Baseline{
			Kind: sourceledger.BaselineTurn, SessionID: "s1", Turn: tc.Identity.UserTurn,
		}, 5, 0, sourceledger.CommitLens{})
		testutil.FailErr(t, "read exact outside operation", err)
		if len(walk.Files) != 1 || len(walk.Files[0].Effects) != 1 {
			t.Fatalf("turn %d has changes %+v", tc.Identity.UserTurn, walk.Files)
		}
		effect := walk.Files[0].Effects[0]
		if effect.Op != step.op || effect.RootID != tc.Source.OutsideRoot.ID || effect.ToolCallID != "outside-edit" || effect.CaptureQuality != "exact" {
			t.Fatalf("turn %d effect = %+v", tc.Identity.UserTurn, effect)
		}
		locations := tc.Effects.Out.SourceContext
		if locations == nil || len(locations.Locations) == 0 {
			t.Fatalf("turn %d did not publish navigation", tc.Identity.UserTurn)
		}
		for _, location := range locations.Locations {
			if location.FileID != effect.FileID {
				t.Fatalf("turn %d location = %+v", tc.Identity.UserTurn, location)
			}
		}
		comparison, err := st.Comparisons.CompareEffect(ctx, "p1", effect.ID)
		testutil.FailErr(t, "compare exact outside operation", err)
		if comparison.Before.Content != step.before || comparison.After.Content != step.after {
			t.Fatalf("turn %d comparison = %+v", tc.Identity.UserTurn, comparison)
		}
		var refs int
		testutil.FailErr(t, "read outside reference", st.LedgerDB().QueryRowContext(ctx,
			`SELECT count(*) FROM source_chat_files WHERE session_id = 's1' AND file_id = ? AND changed = 1`, effect.FileID).Scan(&refs))
		if refs != 1 {
			t.Fatalf("turn %d has %d chat file references", tc.Identity.UserTurn, refs)
		}
		tc.Identity.UserTurn++
	}
}
