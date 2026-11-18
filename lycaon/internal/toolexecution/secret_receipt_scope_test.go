package toolexecution

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

func TestConcurrentContestConsumesOnlyTheExactRedactionOnce(t *testing.T) {
	store := newSecretReceiptRuntime()
	values := []secretmatch.SecretFingerprint{"first", "second", "first"}
	token := store.Mint("chat", "service", values)
	for _, changed := range [][]secretmatch.SecretFingerprint{nil, {"first"}, {"first", "third"}, {"first", "second", "third"}} {
		if _, ok := store.Consume("chat", token, "service", changed); ok {
			t.Fatal("a different fingerprint set spent the receipt")
		}
	}
	var spent atomic.Int32
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			if _, ok := store.Consume("chat", token, "service", []secretmatch.SecretFingerprint{"second", "first"}); ok {
				spent.Add(1)
			}
		})
	}
	group.Wait()
	if spent.Load() != 1 {
		t.Fatalf("receipt spent %d times", spent.Load())
	}
}
