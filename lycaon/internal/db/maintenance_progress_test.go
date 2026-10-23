package db

import (
	"database/sql"
	"testing"
	"testing/synctest"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMaintenanceProgressUnderContinuousWrites(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &Store{writer: &sql.DB{}}
		done := make(chan error, 1)
		go func() { done <- waitForMaintenanceTurn(t.Context(), store) }()
		synctest.Wait()
		for tick := 1; tick <= 10; tick++ {
			store.writeCount.Add(1)
			time.Sleep(100 * time.Millisecond)
			synctest.Wait()
			select {
			case err := <-done:
				testutil.FailErr(t, "admit bounded maintenance turn", err)
				if tick != 10 {
					t.Fatalf("admitted after %d busy ticks", tick)
				}
				return
			default:
			}
		}
		t.Fatal("continuous writes starved bounded maintenance")
	})
}
