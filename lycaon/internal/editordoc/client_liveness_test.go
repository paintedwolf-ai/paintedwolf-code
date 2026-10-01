package editordoc

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
)

// quietFor exceeds the short grace periods used here.
const quietFor = 250 * time.Millisecond

func TestClientLivenessRepresencePresenceOnceTheLastStreamIsGone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		represenced := make(chan string, 1)
		clients := NewClientLiveness(10*time.Millisecond, func(clientID string) { represenced <- clientID })

		clients.Connected("window-a")()

		select {
		case client := <-represenced:
			if client != "window-a" {
				t.Fatalf("represenced presence for %q, want window-a", client)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a client whose last stream ended kept its presence")
		}
	})
}

func TestClientLivenessKeepsPresenceWhileAnotherStreamRemains(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		represenced := make(chan string, 1)
		clients := NewClientLiveness(10*time.Millisecond, func(clientID string) { represenced <- clientID })

		disconnectFirst := clients.Connected("window-a")
		clients.Connected("window-a")
		disconnectFirst()

		select {
		case client := <-represenced:
			t.Fatalf("represenced %q while it still had a live stream", client)
		case <-time.After(quietFor):
		}
		if count := clients.StreamCount("window-a"); count != 1 {
			t.Fatalf("stream count %d, want 1", count)
		}
	})
}

func TestClientLivenessReconnectInsideTheGraceKeepsPresence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		represenced := make(chan string, 1)
		clients := NewClientLiveness(quietFor, func(clientID string) { represenced <- clientID })

		clients.Connected("window-a")()
		clients.Connected("window-a")

		select {
		case client := <-represenced:
			t.Fatalf("represenced %q after it reconnected inside the grace", client)
		case <-time.After(2 * quietFor):
		}
	})
}

func TestClientLivenessIgnoresSupersededExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		represenced := make(chan struct{}, 2)
		clients := NewClientLiveness(time.Second, func(string) { represenced <- struct{}{} })
		clients.Connected("window")()
		clients.mu.Lock()
		first := clients.expiry["window"]
		clients.mu.Unlock()
		clients.Connected("window")()

		// A stopped timer may already have queued its callback.
		clients.expire("window", first)
		select {
		case <-represenced:
			t.Fatal("superseded expiry represenced the renewed presence")
		default:
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if got := len(represenced); got != 1 {
			t.Fatalf("current expiry represenced presence %d times, want 1", got)
		}
	})
}

func TestClientLivenessReconnectWaitsForPresenceRepresence(t *testing.T) {
	// Mutex waits do not let synctest advance its clock.
	releasing, finish := make(chan struct{}), make(chan struct{})
	clients := NewClientLiveness(0, func(string) {
		close(releasing)
		<-finish
	})
	clients.Connected("window")()
	select {
	case <-releasing:
	case <-time.After(testutil.Timeout(5 * time.Second)):
		close(finish)
		t.Fatal("presence represence did not start")
	}
	attempting, reconnected := make(chan struct{}), make(chan struct{})
	go func() {
		close(attempting)
		clients.Connected("window")
		close(reconnected)
	}()
	<-attempting
	select {
	case <-reconnected:
		t.Error("reconnect returned before the presence represence completed")
	case <-time.After(testutil.Timeout(quietFor)):
	}
	close(finish)
	select {
	case <-reconnected:
	case <-time.After(testutil.Timeout(5 * time.Second)):
		t.Fatal("reconnect did not finish after presence represence")
	}
	if got := clients.StreamCount("window"); got != 1 {
		t.Fatalf("stream count = %d, want 1", got)
	}
}

func TestDisconnectRemovesOnlyTheDepartingParticipant(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base"})
	d := f.open(t, "a.txt")
	_, err := f.service.Join(t.Context(), d.ID, f.project.ID, ReplicaJoin{ClientID: "one", Incarnation: uuid.NewString(), Epoch: 1})
	testutil.FailErr(t, "join first window", err)
	_, err = f.service.Join(t.Context(), d.ID, f.project.ID, ReplicaJoin{ClientID: "two", Incarnation: uuid.NewString(), Epoch: 1})
	testutil.FailErr(t, "join second window", err)
	f.service.DisconnectClient(t.Context(), "one")
	current, err := f.service.CurrentSnapshot(t.Context(), f.project.ID, d.ID)
	testutil.FailErr(t, "read participants", err)
	found := false
	for _, p := range current.Participants {
		if p.ClientID == "one" {
			t.Fatal("departed participant remained")
		}
		if p.ClientID == "two" {
			found = true
		}
	}
	if !found {
		t.Fatal("another participant was removed")
	}
}
