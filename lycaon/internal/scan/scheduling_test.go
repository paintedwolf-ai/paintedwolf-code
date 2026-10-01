package scan

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
)

func TestFailedScanTransactionDoesNotWakeServices(t *testing.T) {
	store := NewSQLStore(testdbfixture.Open(t, "scan.db"))
	err := store.inTx(t.Context(), func(_ *db.Queries, _ *sql.Tx) error { return errors.New("rollback") })
	if err == nil {
		t.Fatal("failed transaction committed")
	}
	select {
	case <-store.QueueChanged.Wake():
		t.Fatal("rollback woke runner")
	case <-store.SeriesChanged.Wake():
		t.Fatal("rollback woke cadence")
	default:
	}
}
