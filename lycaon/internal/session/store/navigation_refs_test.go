package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/jsonblob"
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestNavigationReferencesRoundTripVersionedBlob(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "navigation-refs.db")
	rootID := testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	s := NewSQL(sqlDB)
	ctx := context.Background()
	sess, err := s.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	want := api.NavigationReference{
		ID: "ref-0", Syntax: "code", Status: api.NavigationResolved, Explicit: true, Mention: "scripts",
		ProjectID: testdbseed.DefaultProjectID,
		RootID:    rootID,
		Path:      "scripts",
		EntryKind: api.NavigationEntryKindFolder,
	}
	testutil.FailErr(t, "append message", s.AppendMessages(ctx, sess.ID, api.Message{
		ID: "assistant-1", Role: api.MessageRoleAssistant, Content: "scripts/",
		NavigationRefs: []api.NavigationReference{want}, CreatedAt: time.Now().UTC(),
	}))
	msgs, err := s.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "read messages", err)
	if len(msgs) != 1 || len(msgs[0].NavigationRefs) != 1 || !reflect.DeepEqual(msgs[0].NavigationRefs[0], want) {
		t.Fatalf("navigation refs = %+v", msgs)
	}
	var raw string
	testutil.FailErr(t, "read raw blob", sqlDB.QueryRowContext(ctx,
		`SELECT navigation_refs_json FROM messages WHERE id = 'assistant-1'`).Scan(&raw))
	if !strings.Contains(raw, `"v":1`) || !strings.Contains(raw, `"refs"`) {
		t.Fatalf("raw navigation blob = %s", raw)
	}

	_, err = sqlDB.ExecContext(ctx,
		`UPDATE messages SET navigation_refs_json = '{"v":2,"refs":[]}' WHERE id = 'assistant-1'`)
	if err == nil {
		t.Fatal("baseline accepted a newer navigation envelope")
	}
	_, err = sourceref.DecodeMetadata(sql.NullString{String: `{"v":2,"refs":[]}`, Valid: true})
	if !errors.Is(err, jsonblob.ErrBlobTooNew) {
		t.Fatalf("newer blob err = %v", err)
	}
	msgs, err = s.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "read after rejected write", err)
	if len(msgs) != 1 || len(msgs[0].NavigationRefs) != 1 || !reflect.DeepEqual(msgs[0].NavigationRefs[0], want) {
		t.Fatal("rejected envelope changed the stored binding")
	}
}

func TestNavigationReferencesRejectMalformedDurableTargets(t *testing.T) {
	for name, refs := range map[string][]api.NavigationReference{
		"escape":       {{ID: "ref-0", Syntax: "code", Status: api.NavigationResolved, Explicit: true, Mention: "../secret", ProjectID: "project-1", RootID: "root-1", Path: "../secret", EntryKind: api.NavigationEntryKindFile}},
		"unknown kind": {{ID: "ref-0", Syntax: "code", Status: api.NavigationResolved, Explicit: true, Mention: "file.txt", ProjectID: "project-1", RootID: "root-1", Path: "file.txt", EntryKind: "socket"}},
		"duplicate mention": {
			{ID: "ref-0", Syntax: "code", Status: api.NavigationResolved, Explicit: true, Mention: "file.txt", ProjectID: "project-1", RootID: "root-1", Path: "a/file.txt", EntryKind: api.NavigationEntryKindFile},
			{ID: "ref-0", Syntax: "code", Status: api.NavigationResolved, Explicit: true, Mention: "file.txt", ProjectID: "project-1", RootID: "root-1", Path: "b/file.txt", EntryKind: api.NavigationEntryKindFile},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := sourceref.EncodeMetadata(refs, nil); err == nil {
				t.Fatal("marshal accepted malformed navigation references")
			}
		})
	}
	if _, err := sourceref.DecodeMetadata(sql.NullString{
		String: `{"v":1,"refs":[{"mention":"file.txt","project_id":"project-1","root_id":"root-1","path":"file.txt","entry_kind":"socket"}]}`,
		Valid:  true,
	}); err == nil {
		t.Fatal("unmarshal accepted malformed navigation references")
	}
}
