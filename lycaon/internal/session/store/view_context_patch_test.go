package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type viewContextStore interface {
	Create(context.Context, api.CreateSessionRequest, string) (*api.Session, error)
	AppendMessages(context.Context, string, ...api.Message) error
	GetMessage(context.Context, string, string) (api.Message, error)
	PatchMessageViewContext(context.Context, string, string, api.ViewContext) (api.Message, error)
}

func TestViewContextPatchRecordsOnceAndPreservesTheMessage(t *testing.T) {
	stores := map[string]func(t *testing.T) viewContextStore{
		"sql": func(t *testing.T) viewContextStore {
			db := testdbfixture.Open(t, "view-context.db")
			testdbseed.InsertProjectRoot(t, db, testdbseed.DefaultProjectID, t.TempDir())
			return NewSQL(db)
		},
		"memory": func(*testing.T) viewContextStore { return NewMemory() },
	}
	attached := []api.NavigationTarget{{ProjectID: testdbseed.DefaultProjectID, RootID: "r", Path: "b.go", EntryKind: api.NavigationEntryKindFile}}
	for name, open := range stores {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			sess, err := s.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			testutil.FailErr(t, "append", s.AppendMessages(t.Context(), sess.ID, api.Message{ID: "u", Role: api.MessageRoleUser, Content: "fix this", SourceContext: &api.SourceContext{Locations: attached}}))
			before, err := s.GetMessage(t.Context(), sess.ID, "u")
			testutil.FailErr(t, "read prompt", err)
			first := api.ViewContext{
				Editors:  []api.ViewEditor{{RootID: "r", Path: "a.go", DisplayPath: "a.go", Unsaved: true, Selection: &api.ViewSelection{StartLine: 2, EndLine: 4}}},
				Diffs:    []api.ViewPath{},
				Search:   &api.ViewSearch{Query: "Token"},
				Findings: []api.ViewFinding{{Fingerprint: "fp", RuleID: "rule", Level: api.FindingLevelLow}},
			}

			after, err := s.PatchMessageViewContext(t.Context(), sess.ID, "u", first)
			testutil.FailErr(t, "record view", err)
			if after.Content != before.Content || after.Seq <= before.Seq || after.SourceContext == nil || after.SourceContext.View == nil {
				t.Fatalf("patch changed the prompt: before=%+v after=%+v", before, after)
			}
			again, err := s.PatchMessageViewContext(t.Context(), sess.ID, "u", api.ViewContext{Editors: []api.ViewEditor{}, Diffs: []api.ViewPath{}, Findings: []api.ViewFinding{}})
			testutil.FailErr(t, "record a second view", err)
			stored, err := s.GetMessage(t.Context(), sess.ID, "u")
			testutil.FailErr(t, "reread prompt", err)
			if again.Seq != after.Seq || stored.SourceContext == nil || stored.SourceContext.View == nil || !reflect.DeepEqual(*stored.SourceContext.View, first) {
				t.Fatalf("stored source context = %+v; want the first view kept", stored.SourceContext)
			}
			if !reflect.DeepEqual(stored.SourceContext.Locations, attached) {
				t.Fatalf("recording the view changed attached locations: %+v", stored.SourceContext.Locations)
			}
			if _, err := s.PatchMessageViewContext(t.Context(), sess.ID, "missing", first); !errors.Is(err, ErrMessageNotFound) {
				t.Fatalf("missing prompt = %v", err)
			}
		})
	}
}
