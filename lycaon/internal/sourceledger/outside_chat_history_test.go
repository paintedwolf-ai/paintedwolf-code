package sourceledger

import (
	"testing"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOutsideHistorySharesFileIdentityAndKeepsChatAttribution(t *testing.T) {
	store, ctx := openLedger(t)
	for _, session := range []string{"first", "second", "child"} {
		testdbseed.InsertSession(t, store.sqlDB, session, "p1")
	}
	testdbseed.InsertSession(t, store.sqlDB, "other", "p2")
	_, err := store.sqlDB.ExecContext(ctx, `UPDATE sessions SET parent_session_id = 'first' WHERE id = 'child'`)
	testutil.FailErr(t, "set worker parent", err)
	outsideOne, err := store.queries.GetProjectOutsideRootID(ctx, "p1")
	testutil.FailErr(t, "read first outside namespace", err)
	outsideTwo, err := store.queries.GetProjectOutsideRootID(ctx, "p2")
	testutil.FailErr(t, "read second outside namespace", err)

	for i, item := range []struct{ project, root, session, job, content string }{
		{"p1", outsideOne, "first", "", "a"},
		{"p1", outsideOne, "second", "", "b"},
		{"p1", outsideOne, "child", "worker-job", "c"},
		{"p2", outsideTwo, "other", "", "d"},
	} {
		mustRecord(t, store, ctx, RecordInput{
			ProjectID: item.project, SessionID: item.session, JobID: item.job, Turn: i + 1,
			RecordLocation: RecordLocation{RootID: item.root, Path: "outside/note.txt"},
			Origin:         api.SourceChangeOriginAgent, Op: api.SourceChangeOpWrite, After: []byte(item.content),
		})
	}
	first, err := store.History.ResolveHead(ctx, "p1", sourcebranch.Trunk, outsideOne, "outside/note.txt")
	testutil.FailErr(t, "read first file head", err)
	other, err := store.History.ResolveHead(ctx, "p2", sourcebranch.Trunk, outsideTwo, "outside/note.txt")
	testutil.FailErr(t, "read other file head", err)
	if first.FileID == other.FileID {
		t.Fatal("projects shared an outside file identity")
	}

	for _, chat := range []struct {
		project, session, fileID string
		effects                  int
	}{
		{"p1", "first", first.FileID, 2},
		{"p1", "second", first.FileID, 1},
		{"p1", "child", "", 0},
		{"p2", "other", other.FileID, 1},
	} {
		walk, err := store.Walk.QueryWalk(ctx, chat.project, Baseline{Kind: BaselineSession, SessionID: chat.session}, 10, 0, CommitLens{})
		testutil.FailErr(t, "read chat Review", err)
		refs, err := store.queries.ListSourceChatFiles(ctx, chat.session)
		testutil.FailErr(t, "read chat references", err)
		if chat.effects == 0 {
			if len(walk.Files) != 0 || len(refs) != 0 {
				t.Fatalf("worker chat inherited a root-chat location: %+v %+v", walk.Files, refs)
			}
			continue
		}
		if len(walk.Files) != 1 || len(walk.Files[0].Effects) != chat.effects || len(refs) != 1 || refs[0].FileID != chat.fileID {
			t.Fatalf("chat %s has changes %+v and references %+v", chat.session, walk.Files, refs)
		}
		var workers int
		for _, effect := range walk.Files[0].Effects {
			if effect.FileID != chat.fileID || effect.SessionID != chat.session {
				t.Fatalf("chat %s received effect %+v", chat.session, effect)
			}
			if effect.JobID == "worker-job" {
				workers++
			}
		}
		if (chat.session == "first" && workers != 1) || (chat.session != "first" && workers != 0) {
			t.Fatalf("chat %s has %d worker effects", chat.session, workers)
		}
	}
}
