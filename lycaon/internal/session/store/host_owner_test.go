package store

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type authorshipTestStore interface {
	HostOwner(context.Context) (people.Person, error)
	Create(context.Context, api.CreateSessionRequest, string) (*api.Session, error)
	CreateChild(context.Context, *api.Session, api.SpawnChildRequest) (*api.Session, error)
	PutPromptSubmission(context.Context, PromptSubmission) (*PromptSubmission, bool, error)
	AppendMessages(context.Context, string, ...api.Message) error
	UpdateMessage(context.Context, string, string, api.Message) (api.Message, error)
	GetMessage(context.Context, string, string) (api.Message, error)
}

func authorshipStores() []struct {
	name string
	open func(*testing.T) authorshipTestStore
} {
	return []struct {
		name string
		open func(*testing.T) authorshipTestStore
	}{
		{name: "memory", open: func(*testing.T) authorshipTestStore { return NewMemory() }},
		{name: "sql", open: func(t *testing.T) authorshipTestStore {
			handle := testdbfixture.Open(t, "authorship.db")
			testdbseed.InsertProjectRoot(t, handle, testdbseed.DefaultProjectID, t.TempDir())
			return NewSQL(handle)
		}},
	}
}

func TestSessionsBelongToTheActingPersonAndChildrenShareTheOwner(t *testing.T) {
	for _, fixture := range authorshipStores() {
		t.Run(fixture.name, func(t *testing.T) {
			st := fixture.open(t)
			owner, err := st.HostOwner(t.Context())
			testutil.FailErr(t, "read host owner", err)

			hostStarted, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create host-started session", err)
			if hostStarted.OwnerPersonID != owner.ID {
				t.Fatalf("host-started owner = %q, want host owner %q", hostStarted.OwnerPersonID, owner.ID)
			}
			requested, err := st.Create(people.WithCaller(t.Context(), owner), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create requested session", err)
			if requested.OwnerPersonID != owner.ID {
				t.Fatalf("requested owner = %q, want caller %q", requested.OwnerPersonID, owner.ID)
			}
			child, err := st.CreateChild(t.Context(), requested, api.SpawnChildRequest{AgentType: "implementer"})
			testutil.FailErr(t, "create child", err)
			if child.OwnerPersonID != requested.OwnerPersonID {
				t.Fatalf("child owner = %q, want parent owner %q", child.OwnerPersonID, requested.OwnerPersonID)
			}
			orphan := *requested
			orphan.OwnerPersonID = ""
			if _, err := st.CreateChild(t.Context(), &orphan, api.SpawnChildRequest{AgentType: "implementer"}); !errors.Is(err, ErrSessionOwnerRequired) {
				t.Fatalf("ownerless child err = %v, want ErrSessionOwnerRequired", err)
			}
		})
	}
}

func TestPromptSubmissionsNameTheirSenderOnlyForUserOrigin(t *testing.T) {
	for _, fixture := range authorshipStores() {
		t.Run(fixture.name, func(t *testing.T) {
			st := fixture.open(t)
			sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			submission := func(origin PromptSubmissionOrigin, sender string) PromptSubmission {
				return PromptSubmission{ID: uuid.NewString(), SessionID: sess.ID, ProjectID: sess.ProjectID,
					InputDigest: "digest", InputJSON: `{}`, Origin: origin, SubmittedBy: sender}
			}
			for name, in := range map[string]PromptSubmission{
				"user without sender": submission(PromptSubmissionOriginUser, ""),
				"host with sender":    submission(PromptSubmissionOriginLoopWake, sess.OwnerPersonID),
			} {
				if _, _, err := st.PutPromptSubmission(t.Context(), in); !errors.Is(err, ErrPromptSubmissionAuthorship) {
					t.Fatalf("%s: err = %v, want ErrPromptSubmissionAuthorship", name, err)
				}
			}
			sent := submission(PromptSubmissionOriginUser, sess.OwnerPersonID)
			stored, _, err := st.PutPromptSubmission(t.Context(), sent)
			testutil.FailErr(t, "admit user prompt", err)
			if stored.SubmittedBy != sess.OwnerPersonID {
				t.Fatalf("stored sender = %q, want %q", stored.SubmittedBy, sess.OwnerPersonID)
			}
			replay := sent
			replay.SubmittedBy = uuid.NewString()
			var conflict *PromptSubmissionConflictError
			if _, _, err := st.PutPromptSubmission(t.Context(), replay); !errors.As(err, &conflict) {
				t.Fatalf("replay by another sender err = %v, want conflict", err)
			}
		})
	}
}

func TestMessageAuthorIsImmutableAcrossUpdates(t *testing.T) {
	for _, fixture := range authorshipStores() {
		t.Run(fixture.name, func(t *testing.T) {
			st := fixture.open(t)
			sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			msg := api.Message{ID: uuid.NewString(), Role: api.MessageRoleUser, Content: "ship it",
				Origin: api.MessageOriginUser, AuthorPersonID: sess.OwnerPersonID}
			testutil.FailErr(t, "append prompt", st.AppendMessages(t.Context(), sess.ID, msg))
			edited := msg
			edited.Content, edited.AuthorPersonID = "ship it now", ""
			updated, err := st.UpdateMessage(t.Context(), sess.ID, msg.ID, edited)
			testutil.FailErr(t, "update prompt", err)
			if updated.AuthorPersonID != sess.OwnerPersonID {
				t.Fatalf("updated author = %q, want %q", updated.AuthorPersonID, sess.OwnerPersonID)
			}
			stored, err := st.GetMessage(t.Context(), sess.ID, msg.ID)
			testutil.FailErr(t, "read prompt", err)
			if stored.AuthorPersonID != sess.OwnerPersonID {
				t.Fatalf("stored author = %q, want %q", stored.AuthorPersonID, sess.OwnerPersonID)
			}
		})
	}
}
