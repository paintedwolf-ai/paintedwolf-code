package events

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPresenceNotLiveWithoutSubscriber(t *testing.T) {
	p := NewPresence(NewMemoryHub(), time.Minute)
	p.MarkUserAction()
	if p.attached() {
		t.Fatal("attached() = true with no subscriber")
	}
	if p.Live() {
		t.Fatal("Live() = true with a recent action but nobody attached")
	}
}

func TestPresenceNotLiveWithoutUserAction(t *testing.T) {
	hub := NewMemoryHub()
	p := NewPresence(hub, time.Minute)
	_, unsubscribe, err := hub.Subscribe(context.Background(), Subscription{Project: "", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	defer unsubscribe()

	if !p.attached() {
		t.Fatal("attached() = false with a live subscriber")
	}
	// Attachment alone does not establish user activity.
	if p.Live() {
		t.Fatal("Live() = true before any user action")
	}
}

func TestPresenceLiveThenGoesQuiet(t *testing.T) {
	hub := NewMemoryHub()
	p := NewPresence(hub, 50*time.Millisecond)
	clock := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	p.setClock(func() time.Time { return clock })
	_, unsubscribe, err := hub.Subscribe(context.Background(), Subscription{Project: "", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	defer unsubscribe()

	p.MarkUserAction()
	if !p.Live() {
		t.Fatal("Live() = false right after a user action")
	}
	clock = clock.Add(49 * time.Millisecond)
	if !p.Live() {
		t.Fatal("Live() = false before the action window elapsed")
	}
	clock = clock.Add(time.Millisecond)
	if p.Live() {
		t.Fatal("Live() = true after the action window elapsed")
	}
	p.MarkUserAction()
	if !p.Live() {
		t.Fatal("Live() = false after a fresh action re-opened the window")
	}
}

func TestPresenceGoesQuietWhenClientDetaches(t *testing.T) {
	hub := NewMemoryHub()
	p := NewPresence(hub, time.Minute)
	_, unsubscribe, err := hub.Subscribe(context.Background(), Subscription{Project: "", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	p.MarkUserAction()
	if !p.Live() {
		t.Fatal("Live() = false while attached with a recent action")
	}

	// Detachment ends presence even with a recent action.
	unsubscribe()
	if hub.SubscriberCount() != 0 {
		t.Fatalf("SubscriberCount() = %d want 0 after unsubscribe", hub.SubscriberCount())
	}
	if p.Live() {
		t.Fatal("Live() = true after the last client detached")
	}
}

// Wall-clock timestamps include time spent suspended.
func TestPresenceStampCarriesNoMonotonicReading(t *testing.T) {
	p := NewPresence(NewMemoryHub(), time.Minute)
	p.MarkUserAction()

	last := p.LastUserAction()
	if last != last.Round(0) {
		t.Fatalf("LastUserAction() = %v carries a monotonic reading; want wall clock only", last)
	}
}

func TestPresenceWindowCountsSuspendedTime(t *testing.T) {
	hub := NewMemoryHub()
	p := NewPresence(hub, 15*time.Minute)
	_, unsubscribe, err := hub.Subscribe(context.Background(), Subscription{Project: "", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	defer unsubscribe()

	clock := time.Date(2026, 7, 31, 22, 0, 0, 0, time.UTC)
	p.setClock(func() time.Time { return clock })
	p.MarkUserAction()
	if !p.Live() {
		t.Fatal("Live() = false right after a user action")
	}

	// The subscription survives suspension; the action window still expires.
	clock = clock.Add(9 * time.Hour)
	if p.Live() {
		t.Fatal("Live() = true at lid-open after a 9h suspend; scheduled work would run unattended")
	}
}

func TestPresenceNotLiveWhenClockJumpsBackwards(t *testing.T) {
	hub := NewMemoryHub()
	p := NewPresence(hub, 15*time.Minute)
	_, unsubscribe, err := hub.Subscribe(context.Background(), Subscription{Project: "", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	defer unsubscribe()

	clock := time.Date(2026, 7, 31, 22, 0, 0, 0, time.UTC)
	p.setClock(func() time.Time { return clock })
	p.MarkUserAction()

	// A backward clock adjustment does not renew activity.
	clock = clock.Add(-2 * time.Hour)
	if p.Live() {
		t.Fatal("Live() = true after the wall clock jumped backwards")
	}
}

func TestPresenceNilIsNotLive(t *testing.T) {
	var p *Presence
	p.MarkUserAction()
	if p.attached() || p.Live() {
		t.Fatal("nil Presence must report not attached and not live")
	}
}
