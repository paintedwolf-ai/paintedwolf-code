package pagedview

import (
	"testing"
	"time"
)

func TestNotifierCoalescesProgressAndFlushesTerminalChange(t *testing.T) {
	delivered := make(chan struct{}, 10)
	notifier := NewNotifier(time.Hour, func() { delivered <- struct{}{} })
	for range 1000 {
		notifier.Notify(false)
	}
	select {
	case <-delivered:
		t.Fatal("progress escaped its rate limit")
	default:
	}
	notifier.Notify(true)
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("terminal change was held behind progress")
	}
	notifier.Close()
	select {
	case <-delivered:
		t.Fatal("superseded progress was published twice")
	default:
	}
	notifier.Notify(true)
	select {
	case <-delivered:
		t.Fatal("closed notifier published")
	default:
	}
}

func TestNotifierCloseDrainsActivePublication(t *testing.T) {
	started, finish, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	notifier := NewNotifier(time.Hour, func() { close(started); <-finish })
	notifier.Notify(true)
	<-started
	go func() { notifier.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("active publication was not drained")
	default:
	}
	close(finish)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("publication did not drain")
	}
}
