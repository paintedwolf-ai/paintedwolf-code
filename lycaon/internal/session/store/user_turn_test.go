package store

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestUserContinuationKeepsCurrentTurnInMemoryAndSQL(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())

	stores := []struct {
		name  string
		store interface {
			Create(ctx context.Context, req api.CreateSessionRequest, projectID string) (*api.Session, error)
			AppendMessages(ctx context.Context, id string, msgs ...api.Message) error
			UserTurnOrdinal(ctx context.Context, sessionID string) (int, error)
		}
	}{
		{name: "memory", store: NewMemory()},
		{name: "sql", store: NewSQL(database)},
	}
	for _, test := range stores {
		t.Run(test.name, func(t *testing.T) {
			sess, err := test.store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			testutil.FailErr(t, "append user messages", test.store.AppendMessages(ctx, sess.ID,
				api.Message{ID: uuid.NewString(), Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "start"},
				api.Message{ID: uuid.NewString(), Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Kind: api.MessageKindUserContinuation, Content: "adjust"},
			))
			turn, err := test.store.UserTurnOrdinal(ctx, sess.ID)
			testutil.FailErr(t, "read user turn ordinal", err)
			if turn != 1 {
				t.Fatalf("user turn ordinal = %d want 1", turn)
			}
		})
	}
}

func TestTurnNumbersAreNotReusedAfterTranscriptTruncation(t *testing.T) {
	database := testdbfixture.Open(t, "turns.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	for name, st := range map[string]interface {
		Create(context.Context, api.CreateSessionRequest, string) (*api.Session, error)
		AppendMessages(context.Context, string, ...api.Message) error
		TruncateMessagesFrom(context.Context, string, string) (int, error)
		UserTurnOrdinal(context.Context, string) (int, error)
		Get(context.Context, string) (*api.Session, error)
	}{"memory": NewMemory(), "sql": NewSQL(database)} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			first := api.Message{ID: uuid.NewString(), Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "first"}
			second := first
			second.ID = uuid.NewString()
			testutil.FailErr(t, "append turns", st.AppendMessages(ctx, sess.ID, first, second))
			_, err = st.TruncateMessagesFrom(ctx, sess.ID, first.ID)
			testutil.FailErr(t, "truncate turns", err)
			empty, err := st.Get(ctx, sess.ID)
			testutil.FailErr(t, "read rewound session", err)
			if empty.CurrentTurn != 0 {
				t.Fatalf("empty current turn=%d", empty.CurrentTurn)
			}
			next := first
			next.ID = uuid.NewString()
			testutil.FailErr(t, "append next turn", st.AppendMessages(ctx, sess.ID, next))
			current, err := st.UserTurnOrdinal(ctx, sess.ID)
			testutil.FailErr(t, "read permanent turn", err)
			if current != 3 {
				t.Fatalf("next turn=%d, want 3", current)
			}
		})
	}
}
