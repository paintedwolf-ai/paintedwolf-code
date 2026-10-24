package db

import (
	"context"
	"database/sql"
	"errors"
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

func TestMaintenanceCancellationAndFailedAdmissionPreserveTheirCause(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	store := &Store{writer: &sql.DB{}}
	if err := RunMaintenance(ctx, store, DefaultRetention()); !errors.Is(err, context.Canceled) {
		t.Fatalf("maintenance cancellation = %v", err)
	}
	if err := waitForMaintenanceTurn(ctx, store); !errors.Is(err, context.Canceled) {
		t.Fatalf("turn cancellation = %v", err)
	}
	refused := errors.New("writer admission refused")
	err := drainMaintenance(t.Context(), store, DefaultRetention(), func(context.Context, *Store) error {
		return refused
	})
	if !errors.Is(err, refused) {
		t.Fatalf("failed admission = %v", err)
	}
	testutil.FailErr(t, "nil store maintenance", RunMaintenance(t.Context(), nil, DefaultRetention()))
	testutil.FailErr(t, "disabled maintenance", RunMaintenance(t.Context(), store, RetentionConfig{}))
}
