package hitl

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func resolutionOrderStore(t *testing.T) *SQLStore {
	t.Helper()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "session", testdbseed.DefaultProjectID)
	return NewSQLStore(database)
}

// ownerAnswer is the host owner answering a checkpoint.
func ownerAnswer(t *testing.T, store *SQLStore) Resolution {
	t.Helper()
	return Resolution{By: authzledger.ResolvedByHuman, PersonID: testdbseed.OwnerID(t, store.db)}
}

func insertOrderCheckpoint(t *testing.T, store *SQLStore, id string) StoredCheckpoint {
	t.Helper()
	row := StoredCheckpoint{
		ID: id, SessionID: "session", ProjectID: testdbseed.DefaultProjectID,
		Kind: api.CheckpointKindToolApproval, Status: DecisionStatusPending, Type: DecisionTypeApprove,
		CreatedAt: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
	}
	testutil.FailErr(t, "insert pending checkpoint", store.Insert(t.Context(), row))
	return row
}

func TestLatestApprovalFollowsResolutionOrderAcrossTimestampTies(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		name := "admission order"
		if reverse {
			name = "reverse admission order"
		}
		t.Run(name, func(t *testing.T) {
			store := resolutionOrderStore(t)
			rows := []StoredCheckpoint{
				insertOrderCheckpoint(t, store, "z-first"),
				insertOrderCheckpoint(t, store, "a-second"),
			}
			if reverse {
				rows[0], rows[1] = rows[1], rows[0]
			}
			for i, status := range []DecisionStatus{DecisionStatusApproved, DecisionStatusRejected} {
				_, err := store.resolveCheckpoint(t.Context(), rows[i], status, nil, nil, rows[i].CreatedAt, ownerAnswer(t, store), nil)
				testutil.FailErr(t, "resolve checkpoint", err)
			}
			got, found, err := store.LatestResolvedToolApprovalStatus(t.Context(), "session")
			testutil.FailErr(t, "read latest approval", err)
			if !found || got != DecisionStatusRejected {
				t.Fatalf("latest = %s, found=%v; want last resolved rejection", got, found)
			}
		})
	}
}

func TestResolutionOrderRollsBackAndCascadesWithCheckpoint(t *testing.T) {
	store := resolutionOrderStore(t)
	first := insertOrderCheckpoint(t, store, "first")
	_, err := store.resolveCheckpoint(t.Context(), first, DecisionStatusRejected, nil, nil, first.CreatedAt, ownerAnswer(t, store), nil)
	testutil.FailErr(t, "resolve first checkpoint", err)
	second := insertOrderCheckpoint(t, store, "second")
	sealErr := errors.New("seal failed")
	_, err = store.resolveCheckpoint(t.Context(), second, DecisionStatusApproved, nil, nil, second.CreatedAt, ownerAnswer(t, store),
		func(*sql.Tx, StoredCheckpoint) error { return sealErr })
	if !errors.Is(err, sealErr) {
		t.Fatalf("resolve rollback = %v, want seal error", err)
	}
	got, found, err := store.LatestResolvedToolApprovalStatus(t.Context(), "session")
	testutil.FailErr(t, "read latest after rollback", err)
	if !found || got != DecisionStatusRejected {
		t.Fatalf("rolled-back decision became latest: %s, %v", got, found)
	}
	var count int
	testutil.FailErr(t, "read committed ordinals", store.db.QueryRowContext(t.Context(),
		"SELECT count(*) FROM checkpoint_resolution_ordinals").Scan(&count))
	if count != 1 {
		t.Fatalf("resolution ordinals = %d, want one committed decision", count)
	}
	_, err = store.db.ExecContext(t.Context(), "DELETE FROM checkpoints WHERE id = ?", first.ID)
	testutil.FailErr(t, "delete resolved checkpoint", err)
	testutil.FailErr(t, "read remaining ordinals", store.db.QueryRowContext(t.Context(),
		"SELECT count(*) FROM checkpoint_resolution_ordinals").Scan(&count))
	if count != 0 {
		t.Fatalf("orphaned resolution ordinals = %d", count)
	}
}
