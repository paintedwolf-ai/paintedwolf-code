package sourceledger

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func insertWalkMessage(t *testing.T, store *Store, id, session, kind, visibility, content string, ord int64) {
	t.Helper()
	testdbseed.InsertSessionEntry(t, store.sqlDB, "entry-"+id, session, "utterance", id, ord)
	_, err := store.sqlDB.ExecContext(t.Context(), `
		INSERT INTO messages (id, entry_id, session_id, role, content, origin, authority,
		 trust_tier, kind, visibility, ord, ts)
		VALUES (?, ?, ?, 'user', ?, 'user', 'user', 'trusted', ?, ?, ?, ?)
	`, id, "entry-"+id, session, content, kind, visibility, ord, db.FormatTime(time.Now().UTC()))
	testutil.FailErr(t, "insert walk message", err)
}

func TestWalkChaptersUseCompleteTranscriptOrdinals(t *testing.T) {
	store, ctx := openLedger(t)
	testdbseed.InsertSession(t, store.sqlDB, "s1", "p1")
	insertWalkMessage(t, store, "first", "s1", "", "transcript", "First request", 1)
	insertWalkMessage(t, store, "host", "s1", "", "internal", "Host control", 2)
	insertWalkMessage(t, store, "continuation", "s1", "user_continuation", "transcript", "More detail", 3)
	insertWalkMessage(t, store, "workflow", "s1", "workflow_boundary", "transcript", "Workflow", 4)
	insertWalkMessage(t, store, "second", "s1", "", "transcript", "Fix\n the editor", 5)
	for i := 1; i <= 2; i++ {
		mustRecord(t, store, ctx, RecordInput{
			RecordLocation: RecordLocation{RootID: "r1", Path: fmt.Sprintf("file-%d", i)},
			ProjectID:      "p1",
			SessionID:      "s1", Turn: i, OperationID: fmt.Sprintf("op-%d", i),
			Op: api.SourceChangeOpCreate, Origin: api.SourceChangeOriginAgent, After: []byte("hello")})
	}
	page, err := store.Walk.QueryWalk(ctx, "p1", Baseline{Kind: BaselineSession, SessionID: "s1"}, 1, 0, CommitLens{})
	testutil.FailErr(t, "load newest walk page", err)
	if len(page.Turns) != 1 || page.Turns[0].Turn != 2 || page.Turns[0].MessageID != "second" || page.Turns[0].Prompt != "Fix the editor" {
		t.Fatalf("newest page turns = %+v", page.Turns)
	}
	page, err = store.Walk.QueryWalk(ctx, "p1", Baseline{Kind: BaselineSession, SessionID: "s1"}, 1, page.NextBeforeOrdinal, CommitLens{})
	testutil.FailErr(t, "load older walk page", err)
	if len(page.Turns) != 1 || page.Turns[0].Turn != 1 || page.Turns[0].MessageID != "first" {
		t.Fatalf("older page turns = %+v", page.Turns)
	}
}

func TestWalkChapterMetadataCannotCrossProjectBoundary(t *testing.T) {
	store, ctx := openLedger(t)
	testdbseed.InsertSession(t, store.sqlDB, "other", "p2")
	insertWalkMessage(t, store, "private-message", "other", "", "transcript", "Other project request", 1)
	turns, err := store.Walk.walkTurns(ctx, "p1", []Effect{{SessionID: "other", Turn: 1}}, nil)
	testutil.FailErr(t, "load walk turn metadata", err)
	if len(turns) != 0 {
		t.Fatalf("cross-project metadata = %+v", turns)
	}
}

func TestWalkEffectsByIDAndGroup(t *testing.T) {
	store, ctx := openLedger(t)
	testdbseed.InsertProject(t, store.sqlDB, "p2")
	_, err := store.sqlDB.ExecContext(ctx, `
		INSERT INTO project_roots (id, project_id, path, label, is_primary, added_at, kind)
		VALUES ('r2', 'p2', '/tmp/source-ledger-other', 'other', 1, ?, 'attached')
	`, db.FormatTime(time.Now().UTC()))
	testutil.FailErr(t, "insert second root", err)

	if _, err := store.Walk.EffectsByID(ctx, "p1", make([]string, maxEffectsByID+1)); err == nil {
		t.Fatal("EffectsByID accepted an unbounded id list")
	}
	if _, err := store.Walk.EffectsInGroup(ctx, "p1", "", ""); err == nil {
		t.Fatal("EffectsInGroup accepted an empty group")
	}
	if _, err := store.Walk.EffectsInGroup(ctx, "p1", "cw-1", "gt-1"); err == nil {
		t.Fatal("EffectsInGroup accepted both group ids")
	}

	seedWalkGit(t, store, "gt-1", "s1", 1, "", time.Now().UTC())
	testutil.FailErr(t, "record grouped effects", store.RecordBatch(ctx, []RecordInput{
		{
			RecordLocation:  RecordLocation{RootID: "r1", Path: "first.txt"},
			ProjectID:       "p1",
			OperationID:     "op-gt-1",
			GitTransitionID: "gt-1",
			Cause:           CauseGitOperation,
			CaptureQuality:  CaptureReconciled,
			Op:              api.SourceChangeOpCreate,
			Origin:          api.SourceChangeOriginExternal,
			After:           []byte("one"),
		},
		{
			RecordLocation:  RecordLocation{RootID: "r1", Path: "second.txt"},
			ProjectID:       "p1",
			OperationID:     "op-gt-1",
			GitTransitionID: "gt-1",
			Cause:           CauseGitOperation,
			CaptureQuality:  CaptureReconciled,
			Op:              api.SourceChangeOpCreate,
			Origin:          api.SourceChangeOriginExternal,
			After:           []byte("two"),
		},
	}))
	mustRecord(t, store, ctx, RecordInput{
		RecordLocation: RecordLocation{RootID: "r2", Path: "other.txt"},
		ProjectID:      "p2",
		OperationID:    "op-p2",
		Op:             api.SourceChangeOpCreate,
		Origin:         api.SourceChangeOriginExternal,
		After:          []byte("other"),
	})

	rows, err := store.sqlDB.QueryContext(ctx, `SELECT project_id, id FROM source_effects ORDER BY project_id, ordinal`)
	testutil.FailErr(t, "list seeded effect ids", err)
	defer func() { _ = rows.Close() }()
	var p1IDs, p2IDs []string
	for rows.Next() {
		var projectID, id string
		testutil.FailErr(t, "scan effect id", rows.Scan(&projectID, &id))
		if projectID == "p1" {
			p1IDs = append(p1IDs, id)
		} else {
			p2IDs = append(p2IDs, id)
		}
	}
	testutil.FailErr(t, "iterate effect rows", rows.Err())
	if len(p1IDs) != 2 || len(p2IDs) != 1 {
		t.Fatalf("seeded effect counts = (%d, %d), want (2, 1)", len(p1IDs), len(p2IDs))
	}

	byID, err := store.Walk.EffectsByID(ctx, "p1", []string{p1IDs[1], p2IDs[0], p1IDs[0]})
	testutil.FailErr(t, "read effects by id", err)
	if len(byID) != 2 || byID[0].ID != p1IDs[0] || byID[1].ID != p1IDs[1] || byID[0].Path != "first.txt" || byID[1].Path != "second.txt" {
		t.Fatalf("EffectsByID = %+v", byID)
	}

	grouped, err := store.Walk.EffectsInGroup(ctx, "p1", "", "gt-1")
	testutil.FailErr(t, "read grouped effects", err)
	if len(grouped) != 2 || grouped[0].ID != p1IDs[0] || grouped[1].ID != p1IDs[1] || grouped[0].GitTransitionID != "gt-1" {
		t.Fatalf("EffectsInGroup = %+v", grouped)
	}
	crossProject, err := store.Walk.EffectsInGroup(ctx, "p2", "", "gt-1")
	testutil.FailErr(t, "read cross-project group", err)
	if len(crossProject) != 0 {
		t.Fatalf("cross-project EffectsInGroup = %+v", crossProject)
	}

	seedWalkGit(t, store, "gt-oversized", "s1", 1, "", time.Now().UTC())
	oversized := make([]RecordInput, maxEffectsByID+1)
	for i := range oversized {
		oversized[i] = RecordInput{
			RecordLocation:  RecordLocation{RootID: "r1", Path: fmt.Sprintf("bulk-%03d.txt", i)},
			ProjectID:       "p1",
			OperationID:     "op-gt-oversized",
			GitTransitionID: "gt-oversized",
			Cause:           CauseGitOperation,
			CaptureQuality:  CaptureReconciled,
			Op:              api.SourceChangeOpCreate,
			Origin:          api.SourceChangeOriginExternal,
			After:           []byte("x"),
		}
	}
	testutil.FailErr(t, "record oversized group", store.RecordBatch(ctx, oversized))
	if _, err := store.Walk.EffectsInGroup(ctx, "p1", "", "gt-oversized"); !errors.Is(err, ErrGroupTooLarge) {
		t.Fatalf("oversized EffectsInGroup error = %v, want ErrGroupTooLarge", err)
	}
}
