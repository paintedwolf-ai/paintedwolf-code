package outboundhttp

import (
	"net"
	"testing"
)

// A body the transport has read but not written is still in host custody:
// only a connection write after the read dispatches it, once.
func TestBodyDispatchesOnlyWhenAWriteCarriesIt(t *testing.T) {
	var headers, body int
	obs := &dispatchObserver{onHeaders: func() { headers++ }, onBody: func() { body++ }}
	obs.gotConn()
	obs.wrote()
	obs.bodyRead()
	if headers != 1 || body != 0 {
		t.Fatalf("after a buffered read: headers=%d body=%d, want 1 and 0", headers, body)
	}
	obs.wrote()
	obs.bodyRead()
	obs.wrote()
	if headers != 1 || body != 1 {
		t.Fatalf("after writes: headers=%d body=%d, want 1 and 1", headers, body)
	}

	unsent := &dispatchObserver{onBody: func() { t.Fatal("a body whose write never happened was dispatched") }}
	unsent.gotConn()
	unsent.bodyRead()
}

// recordingConn notes whether the hand-off was recorded when each write began.
type recordingConn struct {
	net.Conn
	recorded *bool
	seen     []bool
}

func (c *recordingConn) Write(p []byte) (int, error) {
	c.seen = append(c.seen, *c.recorded)
	return len(p), nil
}

// The hand-off is recorded before the bytes leave, so a response that settles
// the request while a write is still returning cannot leave a value that may
// have reached the server recorded as undispatched.
func TestHandOffIsRecordedBeforeTheWrite(t *testing.T) {
	recorded := false
	obs := &dispatchObserver{onHeaders: func() { recorded = true }}
	obs.gotConn()
	conn := &recordingConn{recorded: &recorded}
	wrapped := &dispatchConn{Conn: conn, obs: obs}
	if _, err := wrapped.Write(nil); err != nil || recorded {
		t.Fatalf("an empty write recorded a hand-off: err=%v", err)
	}
	if _, err := wrapped.Write([]byte("GET / HTTP/1.1\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(conn.seen) != 2 || conn.seen[0] || !conn.seen[1] {
		t.Fatalf("hand-off recorded at each write = %v, want [false true]", conn.seen)
	}
}
