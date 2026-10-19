package presence

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStepAwayInvalidatesPendingAndAlreadyVerifiedPresence(t *testing.T) {
	for _, reason := range []EndReason{EndScreenLocked, EndSleep, EndAppQuit} {
		t.Run(string(reason), func(t *testing.T) {
			broker, key := configuredBroker(t)
			unlocks := NewUnlocks(broker)
			begin := func(subject string) Challenge {
				challenge, err := broker.Begin(Claim{Purpose: PurposeUnlock, PersonID: "owner", WindowLabel: "main", Key: subject})
				testutil.FailErr(t, "begin", err)
				return challenge
			}
			delayed := begin("delayed")
			verified, err := broker.Complete(sign(key, delayed, AuthenticatorLinux), PurposeUnlock, "owner")
			testutil.FailErr(t, "verify before lock", err)
			pending := begin("pending")
			var ended []EndReason
			unlocks.SetObserver(UnlockObserver{Ended: func(_ Unlock, reason EndReason, _ time.Time) { ended = append(ended, reason) }})
			unlocks.LockAll(reason)
			if _, err := broker.Complete(sign(key, pending, AuthenticatorLinux), PurposeUnlock, "owner"); !errors.Is(err, ErrChallengeNotFound) {
				t.Fatalf("pending after lock = %v", err)
			}
			if unlocks.Open(NewUnlock("chat", verified)) {
				t.Fatal("a delayed commit reopened the locked vault")
			}
			if _, active := unlocks.Active("chat"); active {
				t.Fatal("a stale unlock survived lock")
			}
			if len(ended) != 1 || ended[0] != reason {
				t.Fatalf("delayed unlock audit = %v", ended)
			}
			fresh := begin("fresh")
			current, err := broker.Complete(sign(key, fresh, AuthenticatorLinux), PurposeUnlock, "owner")
			testutil.FailErr(t, "verify after return", err)
			if !unlocks.Open(NewUnlock("chat", current)) {
				t.Fatal("fresh presence did not open the vault")
			}
		})
	}
}

func TestStepAwayAndDelayedUnlockCommitCannotLeaveAuthorityOpen(t *testing.T) {
	for range 100 {
		broker, key := configuredBroker(t)
		unlocks := NewUnlocks(broker)
		challenge, err := broker.Begin(Claim{Purpose: PurposeUnlock, PersonID: "owner", WindowLabel: "main", Key: "race"})
		testutil.FailErr(t, "begin", err)
		verified, err := broker.Complete(sign(key, challenge, AuthenticatorLinux), PurposeUnlock, "owner")
		testutil.FailErr(t, "complete", err)
		var workers sync.WaitGroup
		workers.Add(2)
		go func() { defer workers.Done(); unlocks.Open(NewUnlock("chat", verified)) }()
		go func() { defer workers.Done(); unlocks.LockAll(EndScreenLocked) }()
		workers.Wait()
		if _, active := unlocks.Active("chat"); active {
			t.Fatal("step-away race left authority open")
		}
	}
}
