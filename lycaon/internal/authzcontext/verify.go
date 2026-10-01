package authzcontext

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/db"
)

// ChainTable names which per-session hash chain Verify is walking.
type ChainTable string

const (
	ChainTableContexts ChainTable = "authorization_contexts"
	ChainTableEvents   ChainTable = "authz_events"
)

// Break describes the first hash-chain mismatch Verify finds.
type Break struct {
	SessionID  string
	Table      ChainTable
	ContextSeq int
	EventSeq   int
	Reason     string
}

// Verify walks every per-session authorization_contexts and authz_events chain.
func Verify(ctx context.Context, database db.Handle) (*Break, error) {
	if database == nil {
		return nil, fmt.Errorf("authzcontext verify: nil db")
	}
	sessions, err := listContextSessions(ctx, database)
	if err != nil {
		return nil, err
	}
	eventSessions, err := listEventSessions(ctx, database)
	if err != nil {
		return nil, err
	}
	sessions = unionSessions(sessions, eventSessions)
	store := NewSQLStore(database)
	for _, sessionID := range sessions {
		rows, err := store.ListContexts(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		if br := VerifyContexts(sessionID, rows); br != nil {
			return br, nil
		}
		events, err := store.ListEvents(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		if br := VerifyEvents(sessionID, events); br != nil {
			return br, nil
		}
	}
	return nil, nil
}

func listContextSessions(ctx context.Context, database db.Handle) ([]string, error) {
	qrows, err := database.QueryContext(ctx, `SELECT DISTINCT session_id FROM authorization_contexts ORDER BY session_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = qrows.Close() }()
	var out []string
	for qrows.Next() {
		var id string
		if err := qrows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, qrows.Err()
}

// VerifyContexts verifies an in-memory context chain.
func VerifyContexts(sessionID string, rows []Context) *Break {
	var prevHash string
	for i, row := range rows {
		wantSeq := i + 1
		if row.ContextSeq != wantSeq {
			return &Break{SessionID: sessionID, Table: ChainTableContexts, ContextSeq: row.ContextSeq, Reason: "context_seq gap or reorder"}
		}
		if row.PrevHash != prevHash {
			return &Break{SessionID: sessionID, Table: ChainTableContexts, ContextSeq: row.ContextSeq, Reason: "prev_hash mismatch"}
		}
		payload := chainPayloadFromContext(row)
		hashVersion := row.HashVersion
		if hashVersion != HashVersion1 {
			return &Break{SessionID: sessionID, Table: ChainTableContexts, ContextSeq: row.ContextSeq, Reason: "unsupported hash_version"}
		}
		got, err := ComputeRowHash(hashVersion, sessionID, row.ContextSeq, chainTimestamp(row), payload, row.PrevHash)
		if err != nil {
			return &Break{SessionID: sessionID, Table: ChainTableContexts, ContextSeq: row.ContextSeq, Reason: err.Error()}
		}
		if got != row.RowHash {
			return &Break{SessionID: sessionID, Table: ChainTableContexts, ContextSeq: row.ContextSeq, Reason: "row_hash mismatch"}
		}
		prevHash = row.RowHash
	}
	return nil
}

func listEventSessions(ctx context.Context, database db.Handle) ([]string, error) {
	qrows, err := database.QueryContext(ctx, `SELECT DISTINCT session_id FROM authz_events ORDER BY session_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = qrows.Close() }()
	var out []string
	for qrows.Next() {
		var id string
		if err := qrows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, qrows.Err()
}

func unionSessions(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	var out []string
	for _, id := range append(a, b...) {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// VerifyEvents verifies an in-memory event chain.
func VerifyEvents(sessionID string, rows []Event) *Break {
	var prevHash string
	for i, row := range rows {
		wantSeq := i + 1
		if row.EventSeq != wantSeq {
			return &Break{SessionID: sessionID, Table: ChainTableEvents, EventSeq: row.EventSeq, Reason: "event_seq gap or reorder"}
		}
		if row.PrevHash != prevHash {
			return &Break{SessionID: sessionID, Table: ChainTableEvents, EventSeq: row.EventSeq, Reason: "prev_hash mismatch"}
		}
		payload := eventChainPayloadFromEvent(row)
		hashVersion := row.HashVersion
		if hashVersion != HashVersion1 {
			return &Break{SessionID: sessionID, Table: ChainTableEvents, EventSeq: row.EventSeq, Reason: "unsupported hash_version"}
		}
		got, err := ComputeEventRowHash(hashVersion, sessionID, row.EventSeq, eventChainTimestamp(row), payload, row.PrevHash)
		if err != nil {
			return &Break{SessionID: sessionID, Table: ChainTableEvents, EventSeq: row.EventSeq, Reason: err.Error()}
		}
		if got != row.RowHash {
			return &Break{SessionID: sessionID, Table: ChainTableEvents, EventSeq: row.EventSeq, Reason: "row_hash mismatch"}
		}
		prevHash = row.RowHash
	}
	return nil
}
