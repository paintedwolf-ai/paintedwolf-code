package store

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type turnLoadTestStore interface {
	turnClockTestStore
	PutTurnLoadReceipt(context.Context, TurnLoadReceipt) (TurnLoadReceipt, error)
	ListTurnLoadReceiptsForTurns(context.Context, []string) ([]TurnLoadReceipt, error)
}

var turnLoadStores = []struct {
	name string
	open func(*testing.T) turnLoadTestStore
}{
	{name: "memory", open: func(*testing.T) turnLoadTestStore { return NewMemory() }},
	{name: "sql", open: func(t *testing.T) turnLoadTestStore { return openTurnSQLStore(t).(turnLoadTestStore) }},
}

func TestTurnLoadReceiptsListByTheTurnsThatOpenedThemParity(t *testing.T) {
	for _, fixture := range turnLoadStores {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := t.Context()
			st := fixture.open(t)
			sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			first := appendUserPrompt(t, st, sess.ID, "first")
			second := appendUserPrompt(t, st, sess.ID, "second")

			put := func(opening, trigger, call string) TurnLoadReceipt {
				stored, err := st.PutTurnLoadReceipt(ctx, TurnLoadReceipt{
					SessionID: sess.ID, OpeningMessageID: opening, ToolCallID: call, Trigger: trigger,
					Decisions: "{}", Standing: "{}", StateJSON: "{}",
				})
				testutil.FailErr(t, "put "+trigger+" receipt", err)
				return stored
			}
			turn1 := put(first, TurnLoadTriggerTurn, "")
			request1 := put(first, TurnLoadTriggerRequest, "call-1")
			turn2 := put(second, TurnLoadTriggerTurn, "")
			orphan := put("", TurnLoadTriggerTurn, "")
			if turn1.ID == 0 || request1.ID <= turn1.ID || turn2.ID <= request1.ID {
				t.Fatalf("receipt ids must increase in recording order: %d %d %d", turn1.ID, request1.ID, turn2.ID)
			}

			rows, err := st.ListTurnLoadReceiptsForTurns(ctx, []string{first})
			testutil.FailErr(t, "list first turn", err)
			if len(rows) != 2 || rows[0].ID != turn1.ID || rows[1].ID != request1.ID || rows[1].ToolCallID != "call-1" {
				t.Fatalf("first turn receipts = %+v", rows)
			}
			rows, err = st.ListTurnLoadReceiptsForTurns(ctx, []string{second, first, " "})
			testutil.FailErr(t, "list both turns", err)
			if len(rows) != 3 || rows[2].ID != turn2.ID || rows[2].OpeningMessageID != second {
				t.Fatalf("both turns = %+v", rows)
			}
			rows, err = st.ListTurnLoadReceiptsForTurns(ctx, nil)
			testutil.FailErr(t, "list no turns", err)
			if len(rows) != 0 {
				t.Fatalf("no turns must list nothing, got %+v", rows)
			}
			if orphan.ID <= turn2.ID || orphan.OpeningMessageID != "" {
				t.Fatalf("a receipt without a turn is still recorded: %+v", orphan)
			}
		})
	}
}
