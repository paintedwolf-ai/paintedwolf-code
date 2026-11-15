package outboundhttp

import "testing"

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
