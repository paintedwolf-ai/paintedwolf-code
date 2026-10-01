package queue

import (
	"fmt"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPendingReadDuringEveryReceiptCommit(t *testing.T) {
	for _, operation := range []string{"next", "send", "reserve", "cancel", "remove", "edit"} {
		t.Run(operation, func(t *testing.T) {
			s := New()
			d := appendQueueItem(s, "session", "item", "first")
			held := operation == "reserve" || operation == "remove" || operation == "edit"
			if held {
				var err error
				d, err = s.SetHold("session", d.Revision, true)
				testutil.FailErr(t, "hold draft", err)
			}
			if operation == "send" || operation == "cancel" {
				var err error
				d, err = s.RequestSend("session", d.Revision, commitQueueTurn)
				testutil.FailErr(t, "reserve draft", err)
			}
			read := make(chan bool, 1)
			commit := func() error {
				go func() { read <- s.AwaitsPerson("session", "item") }()
				select {
				case got := <-read:
					if got != held {
						return fmt.Errorf("pending hold = %v, want committed %v", got, held)
					}
					return nil
				case <-time.After(time.Second):
					return fmt.Errorf("pending read blocked behind receipt commit")
				}
			}
			commitTurn := func([]api.QueueItem) error { return commit() }
			var err error
			switch operation {
			case "next":
				_, _, err = s.TakeNextTurn("session", commitTurn)
			case "send":
				_, _, err = s.TakeSendTurn("session", commitTurn)
			case "reserve":
				_, err = s.RequestSend("session", d.Revision, commitTurn)
			case "cancel":
				_, err = s.CancelSend("session", d.Revision, commitTurn)
			case "remove":
				_, err = s.Remove("session", d.Revision, []string{"item"}, commit)
			case "edit":
				_, err = s.UpdateText("session", d.Revision, "item", "updated", commit)
			}
			testutil.FailErr(t, "commit while publishing pending state", err)
			if got, want := s.AwaitsPerson("session", "item"), operation == "edit"; got != want {
				t.Fatalf("committed hold = %v, want %v", got, want)
			}
		})
	}
}

func TestPublishedHoldTracksAppendRemoveAndClear(t *testing.T) {
	for _, clear := range []string{"cancel all", "clear"} {
		t.Run(clear, func(t *testing.T) {
			s := New()
			_, err := s.SetHold("session", 0, true)
			testutil.FailErr(t, "hold empty draft", err)
			appendQueueItem(s, "session", "a", "first")
			d := appendQueueItem(s, "session", "b", "second")
			if !s.AwaitsPerson("session", "a") || !s.AwaitsPerson("session", "b") {
				t.Fatal("new held items not published")
			}
			_, err = s.Remove("session", d.Revision, []string{"a"}, commitQueueMutation)
			testutil.FailErr(t, "remove first item", err)
			if s.AwaitsPerson("session", "a") || !s.AwaitsPerson("session", "b") {
				t.Fatal("removal changed wrong hold state")
			}
			if clear == "clear" {
				s.Clear("session")
			} else {
				s.CancelAll("session")
			}
			if s.AwaitsPerson("session", "b") {
				t.Fatal("cleared draft retained hold state")
			}
		})
	}
}
