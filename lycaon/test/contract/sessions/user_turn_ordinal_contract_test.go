package contract

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	wire "github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// turnOrdinalTranscript holds one row of every shape that decides whether a
// user-role message opens a turn, so the SQL and Go counters are compared over
// the disagreements rather than over the easy case.
func turnOrdinalTranscript() []wire.Message {
	now := time.Now().UTC()
	msg := func(m wire.Message) wire.Message {
		m.ID = uuid.NewString()
		m.Content = "x"
		m.Origin = wire.MessageOriginUser
		m.Authority = wire.ContentAuthorityUser
		m.TrustTier = wire.ContentTrustTierTrusted
		m.CreatedAt = now
		now = now.Add(time.Second)
		return m
	}
	return []wire.Message{
		// Counts: three plain user turns.
		msg(wire.Message{Role: wire.MessageRoleUser}),
		msg(wire.Message{Role: wire.MessageRoleUser, Visibility: wire.MessageVisibilityTranscript}),
		msg(wire.Message{Role: wire.MessageRoleUser}),
		// Does not count: explicit user direction delivered inside the open turn.
		msg(wire.Message{Role: wire.MessageRoleUser, Kind: wire.MessageKindUserContinuation}),
		// Does not count: assistant and tool rows.
		msg(wire.Message{Role: wire.MessageRoleAssistant, Origin: wire.MessageOriginModel, Authority: wire.ContentAuthorityNone}),
		// Does not count: an internal host row (kick, ambient boundary).
		msg(wire.Message{
			Role:       wire.MessageRoleUser,
			Origin:     wire.MessageOriginHost,
			Authority:  wire.ContentAuthoritySystem,
			Visibility: wire.MessageVisibilityInternal,
		}),
		// Does not count: workflow span bookkeeping, by kind and by typed meta.
		msg(wire.Message{Role: wire.MessageRoleUser, Kind: wire.MessageKindWorkflowBoundary}),
		msg(wire.Message{
			Role:             wire.MessageRoleUser,
			WorkflowBoundary: &wire.WorkflowBoundaryMeta{},
		}),
	}
}

// The source ledger stamps api.UserTurnOrdinal on every write and the session
// wire publishes it, so a client can address "the turn I am in". The SQL count
// behind Session.current_turn has to agree with the Go predicate exactly, or the
// review lens asks for a turn no row was written against.
func TestUserTurnOrdinalSQLMatchesGoPredicate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "user-turn-contract.db")

	sqlStore := store.NewSQL(sqlDB)
	memStore := store.NewMemory()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())

	sqlSess, err := sqlStore.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	contractcheck.FailErr(t, "create sql session", err)
	memSess, err := memStore.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	contractcheck.FailErr(t, "create memory session", err)

	transcript := turnOrdinalTranscript()
	contractcheck.FailErr(t, "append sql messages", sqlStore.AppendMessages(ctx, sqlSess.ID, transcript...))
	contractcheck.FailErr(t, "append memory messages", memStore.AppendMessages(ctx, memSess.ID, transcript...))

	want := wire.UserTurnOrdinal(transcript)
	if want != 3 {
		t.Fatalf("fixture drifted: api.UserTurnOrdinal = %d, want 3 user turns", want)
	}

	gotSQL, err := sqlStore.UserTurnOrdinal(ctx, sqlSess.ID)
	contractcheck.FailErr(t, "sql user turn ordinal", err)
	if gotSQL != want {
		t.Fatalf("SQL CountSessionUserTurns = %d, api.UserTurnOrdinal = %d", gotSQL, want)
	}

	gotMem, err := memStore.UserTurnOrdinal(ctx, memSess.ID)
	contractcheck.FailErr(t, "memory user turn ordinal", err)
	if gotMem != want {
		t.Fatalf("memory UserTurnOrdinal = %d, api.UserTurnOrdinal = %d", gotMem, want)
	}

	// The same number reaches the wire, which is what a turn scope cites back.
	hydrated, err := sqlStore.Get(ctx, sqlSess.ID)
	contractcheck.FailErr(t, "get sql session", err)
	if hydrated.CurrentTurn != want {
		t.Fatalf("Session.current_turn = %d, want %d", hydrated.CurrentTurn, want)
	}
}
