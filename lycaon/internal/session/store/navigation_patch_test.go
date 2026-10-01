package store

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestNavigationPatchPreservesMessageAndRejectsStaleResults(t *testing.T) {
	db := testdbfixture.Open(t, "navigation-patch.db")
	testdbseed.InsertProjectRoot(t, db, testdbseed.DefaultProjectID, t.TempDir())
	s := NewSQL(db)
	sess, err := s.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	initial := []api.NavigationReference{{Mention: "a.go", Path: "a.go", ProjectID: testdbseed.DefaultProjectID, ID: "ref-0", Syntax: "code", Status: api.NavigationPending}}
	testutil.FailErr(t, "append", s.AppendMessages(t.Context(), sess.ID, api.Message{ID: "m", Role: api.MessageRoleAssistant, Content: "`a.go`", NavigationRefs: initial}))
	before, err := s.GetMessage(t.Context(), sess.ID, "m")
	testutil.FailErr(t, "read initial message", err)
	bound := append([]api.NavigationReference(nil), initial...)
	bound[0].RootID = "r"
	bound[0].Path = "src/a.go"
	bound[0].EntryKind = api.NavigationEntryKindFile
	bound[0].Status = api.NavigationResolved
	bound[0].WorkerID = "first"
	after, err := s.PatchMessageNavigation(t.Context(), sess.ID, "m", NavigationContentHash(before.Content), initial, bound)
	testutil.FailErr(t, "bind target", err)
	if after.Content != before.Content || after.Ord != before.Ord || after.Seq <= before.Seq || !after.CreatedAt.Equal(before.CreatedAt) {
		t.Fatalf("patch changed message identity: before=%+v after=%+v", before, after)
	}
	stale, err := s.PatchMessageNavigation(t.Context(), sess.ID, "m", NavigationContentHash(before.Content), initial, initial)
	testutil.FailErr(t, "concurrent stale lookup", err)
	if stale.NavigationRefs[0].WorkerID != "first" || stale.Seq != after.Seq {
		t.Fatal("stale lookup replaced binding")
	}
	_, err = s.PatchMessageNavigation(t.Context(), sess.ID, "m", NavigationContentHash("different text"), bound, initial)
	if !errors.Is(err, ErrNavigationContentChanged) {
		t.Fatalf("content guard: %v", err)
	}
}
