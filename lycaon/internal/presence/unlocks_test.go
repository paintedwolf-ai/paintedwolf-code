package presence

import (
	"testing"
	"time"
)

type unlockClock struct{ at time.Time }

func (c *unlockClock) now() time.Time             { return c.at }
func (c *unlockClock) advance(step time.Duration) { c.at = c.at.Add(step) }

func recordingUnlocks() (*Unlocks, *unlockClock, *[]EndReason) {
	clock := &unlockClock{at: time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)}
	unlocks := NewUnlocks()
	unlocks.SetClock(clock.now)
	var ended []EndReason
	unlocks.SetObserver(UnlockObserver{Ended: func(_ Unlock, reason EndReason, _ time.Time) { ended = append(ended, reason) }})
	return unlocks, clock, &ended
}

func (c *unlockClock) unlock(chat, id string) Unlock {
	return NewUnlock(chat, Verified{
		ChallengeID: id, PersonID: "owner", Authenticator: AuthenticatorMacOS, WindowLabel: "main", VerifiedAt: c.at,
	})
}

func TestUnlockLapsesWhenIdle(t *testing.T) {
	unlocks, clock, ended := recordingUnlocks()
	unlocks.Open(clock.unlock("chat", "one"))
	clock.advance(UnlockIdle - time.Second)
	if _, ok := unlocks.Use("chat"); !ok {
		t.Fatal("an unlock ended before its idle deadline")
	}
	clock.advance(UnlockIdle - time.Second)
	if _, ok := unlocks.Active("chat"); !ok {
		t.Fatal("a use did not extend the idle deadline")
	}
	clock.advance(2 * time.Second)
	if _, ok := unlocks.Active("chat"); ok {
		t.Fatal("an idle unlock stayed open")
	}
	if len(*ended) != 1 || (*ended)[0] != EndIdle {
		t.Fatalf("ended = %v", *ended)
	}
}

// A busy chat still closes at the ceiling.
func TestUnlockClosesAtTheCeilingHoweverBusy(t *testing.T) {
	unlocks, clock, ended := recordingUnlocks()
	opened := clock.unlock("chat", "one")
	unlocks.Open(opened)
	for clock.at.Before(opened.ExpiresAt().Add(-UnlockIdle)) {
		clock.advance(UnlockIdle / 2)
		if _, ok := unlocks.Use("chat"); !ok {
			t.Fatalf("a busy unlock ended early at %s", clock.at)
		}
	}
	clock.at = opened.ExpiresAt()
	if _, ok := unlocks.Use("chat"); ok {
		t.Fatal("an unlock outlived its ceiling")
	}
	if (*ended)[len(*ended)-1] != EndCeiling {
		t.Fatalf("ended = %v", *ended)
	}
}

func TestUnlocksAreChatScopedAndLockTogether(t *testing.T) {
	unlocks, clock, ended := recordingUnlocks()
	unlocks.Open(clock.unlock("chat-a", "one"))
	if _, ok := unlocks.Active("chat-b"); ok {
		t.Fatal("an unlock in one chat opened another")
	}
	unlocks.Open(clock.unlock("chat-b", "two"))
	if closed := unlocks.LockAll(EndScreenLocked); closed != 2 {
		t.Fatalf("lock all ended %d unlocks", closed)
	}
	if _, ok := unlocks.Active("chat-a"); ok {
		t.Fatal("an unlock survived the screen lock")
	}
	if len(*ended) != 2 || (*ended)[0] != EndScreenLocked {
		t.Fatalf("ended = %v", *ended)
	}
}

func TestUnlockRenewalEndsThePreviousWindow(t *testing.T) {
	unlocks, clock, ended := recordingUnlocks()
	unlocks.Open(clock.unlock("chat", "one"))
	renewed := clock.unlock("chat", "two")
	unlocks.Open(renewed)
	if current, ok := unlocks.Active("chat"); !ok || current.ID != renewed.ID {
		t.Fatalf("current unlock = %+v", current)
	}
	if len(*ended) != 1 || (*ended)[0] != EndRenewed {
		t.Fatalf("ended = %v", *ended)
	}
}

func TestSweepClosesOnlyLapsedWindows(t *testing.T) {
	unlocks, clock, _ := recordingUnlocks()
	unlocks.Open(clock.unlock("stale", "one"))
	clock.advance(UnlockIdle)
	unlocks.Open(clock.unlock("fresh", "two"))
	if closed := unlocks.Sweep(); closed != 1 {
		t.Fatalf("sweep ended %d unlocks", closed)
	}
	if _, ok := unlocks.Active("fresh"); !ok {
		t.Fatal("sweep ended a fresh unlock")
	}
}

func TestOnlyStepAwayReasonsAreClientLocks(t *testing.T) {
	for _, reason := range []EndReason{EndScreenLocked, EndSleep, EndAppQuit, EndManual} {
		if !IsLockReason(reason) {
			t.Errorf("%s is not a client lock reason", reason)
		}
	}
	for _, reason := range []EndReason{EndIdle, EndCeiling, EndRenewed, EndRestart} {
		if IsLockReason(reason) {
			t.Errorf("a client could claim %s", reason)
		}
	}
}
