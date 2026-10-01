package store

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type turnSourceBriefTestStore interface {
	turnClockTestStore
	PutTurnSourceBrief(ctx context.Context, sessionID, openingMessageID, briefJSON string) error
	TurnSourceBriefs(ctx context.Context, sessionID string) (map[string]string, error)
}

// A turn's brief is written once, when the turn opens; a later write for the
// same turn keeps the first, and a message that opened no turn takes none.
func TestTurnSourceBriefIsFixedWhenTheTurnOpensParity(t *testing.T) {
	stores := []struct {
		name string
		open func(*testing.T) turnSourceBriefTestStore
	}{
		{name: "memory", open: func(*testing.T) turnSourceBriefTestStore { return NewMemory() }},
		{name: "sql", open: func(t *testing.T) turnSourceBriefTestStore { return openTurnSQLStore(t).(turnSourceBriefTestStore) }},
	}
	for _, fixture := range stores {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := t.Context()
			st := fixture.open(t)
			sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			first := appendUserPrompt(t, st, sess.ID, "first")
			second := appendUserPrompt(t, st, sess.ID, "second")

			none, err := st.TurnSourceBriefs(ctx, sess.ID)
			testutil.FailErr(t, "list before any brief", err)
			if len(none) != 0 {
				t.Fatalf("briefs before any write = %v", none)
			}
			testutil.FailErr(t, "put second brief", st.PutTurnSourceBrief(ctx, sess.ID, second, `{"other_files":2}`))
			testutil.FailErr(t, "rewrite second brief", st.PutTurnSourceBrief(ctx, sess.ID, second, `{"other_files":9}`))
			testutil.FailErr(t, "put brief for no turn", st.PutTurnSourceBrief(ctx, sess.ID, "not-a-turn", `{"other_files":1}`))
			testutil.FailErr(t, "put empty brief", st.PutTurnSourceBrief(ctx, sess.ID, first, " "))

			got, err := st.TurnSourceBriefs(ctx, sess.ID)
			testutil.FailErr(t, "list briefs", err)
			if len(got) != 1 || got[second] != `{"other_files":2}` {
				t.Fatalf("briefs = %v, want only the second turn's first write", got)
			}
		})
	}
}
