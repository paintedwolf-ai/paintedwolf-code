package events

import (
	"testing"
	"time"
)

func TestBurstEndOwesCommentsOnlyAfterAnEvent(t *testing.T) {
	burst := NewBurstEnd()
	defer burst.Stop()
	if burst.C() != nil {
		t.Fatal("a stream that wrote no event owes no burst-end comment")
	}

	for round := range 2 {
		burst.Wrote()
		for comment := range burstEndDelays {
			select {
			case <-burst.C():
			case <-time.After(5 * time.Second):
				t.Fatalf("round %d: burst-end comment %d never came due", round, comment)
			}
			burst.Ended()
		}
		if burst.C() != nil {
			t.Fatalf("round %d: the schedule still owes a comment after its last one", round)
		}
	}
}

func TestBurstEndRestartsWhenEventsContinue(t *testing.T) {
	burst := NewBurstEnd()
	defer burst.Stop()
	burst.Wrote()
	select {
	case <-burst.C():
	case <-time.After(5 * time.Second):
		t.Fatal("the first burst-end comment never came due")
	}
	burst.Ended()
	// A new event replaces the remaining schedule with a fresh one.
	burst.Wrote()
	if burst.next != 0 {
		t.Fatalf("restarted schedule next = %d, want 0", burst.next)
	}
	select {
	case <-burst.C():
	case <-time.After(5 * time.Second):
		t.Fatal("the restarted burst-end comment never came due")
	}
	burst.Ended()
	if burst.next != 1 || burst.C() == nil {
		t.Fatalf("after first restarted comment: next = %d, C = %v, want next=1 and second comment still scheduled", burst.next, burst.C())
	}
}
