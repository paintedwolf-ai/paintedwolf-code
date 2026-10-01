package progress_test

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/lycaon/lycaon/internal/progress"
)

func TestClockBanksActiveTimeAcrossRefcount(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := t.Name()
		t.Cleanup(func() { progress.ForgetClock(root) })
		if progress.Clock(root).Running() {
			t.Fatal("fresh clock should not be running")
		}

		progress.TurnStarted(root) // coordinator turn
		coordinatorSince := progress.Clock(root).RunningAt
		progress.TurnStarted(root) // overlapping worker turn
		if clock := progress.Clock(root); clock.ActiveMs != 0 || !clock.Running() || !clock.RunningAt.Equal(coordinatorSince) {
			t.Fatal("overlapping worker should share the coordinator's live span")
		}

		progress.TurnFinished(root) // worker done, coordinator still active
		if clock := progress.Clock(root); !clock.Running() || !clock.SettledAt.IsZero() {
			t.Fatal("clock should still run, unsettled, while one turn remains active")
		}

		time.Sleep(20 * time.Millisecond)
		progress.TurnFinished(root) // all idle → bank
		clock := progress.Clock(root)
		if clock.Running() {
			t.Fatal("clock should pause once all turns finish")
		}
		if clock.ActiveMs != 20 || clock.WorkMs != 20 {
			t.Fatalf("banked clock = %+v, want exactly 20 ms active and work time", clock)
		}
		if clock.SettledAt.IsZero() {
			t.Fatal("paused clock should record when it settled")
		}
	})
}

func TestWaitsBankActiveButNotWorkTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := t.Name()
		t.Cleanup(func() { progress.ForgetClock(root) })
		progress.TurnStarted(root)
		progress.WaitStarted(root)
		time.Sleep(30 * time.Millisecond)
		progress.WaitFinished(root)
		progress.TurnFinished(root)
		clock := progress.Clock(root)
		if clock.ActiveMs != 30 {
			t.Fatalf("active = %d ms, want the wait counted", clock.ActiveMs)
		}
		if clock.WorkMs != 0 {
			t.Fatalf("work = %d ms of %d active, want the wait excluded", clock.WorkMs, clock.ActiveMs)
		}
	})
}

func TestOpenTurnZeroesAccrualButKeepsRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := t.Name()
		t.Cleanup(func() { progress.ForgetClock(root) })
		progress.TurnStarted(root)
		defer progress.TurnFinished(root)
		time.Sleep(15 * time.Millisecond)

		progress.OpenTurn(root)
		progress.AnchorTurn(root, "prompt-1")
		clock := progress.Clock(root)
		if !clock.Running() {
			t.Fatal("opening a turn should preserve the running state")
		}
		if clock.ActiveMs != 0 || clock.OpeningMessageID != "prompt-1" {
			t.Fatalf("opened turn = %+v, want zero banked time anchored to prompt-1", clock)
		}
	})
}

func TestRestoreTurnClockOnlySeedsAnUnclockedRoot(t *testing.T) {
	root := t.Name()
	t.Cleanup(func() { progress.ForgetClock(root) })
	settled := time.Date(2026, time.September, 11, 16, 16, 0, 0, time.UTC)
	durable := progress.TurnClock{OpeningMessageID: "prompt-1", ActiveMs: 348000, WorkMs: 300000, SettledAt: settled}
	if !progress.RestoreTurnClock(root, durable) {
		t.Fatal("restore should seed a root this process has not clocked")
	}
	if got := progress.Clock(root); got != durable {
		t.Fatalf("restored clock = %+v, want %+v", got, durable)
	}
	if progress.RestoreTurnClock(root, progress.TurnClock{OpeningMessageID: "stale", ActiveMs: 1}) {
		t.Fatal("restore must not overwrite a clocked root")
	}
}

func TestClockUnknownRootIsZero(t *testing.T) {
	if clock := progress.Clock("never-seen"); clock != (progress.TurnClock{}) {
		t.Fatalf("unknown root clock = %+v", clock)
	}
}
