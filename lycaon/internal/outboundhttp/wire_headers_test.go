package outboundhttp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestWireCaptureKeepsFinalBlockInReceivedOrder(t *testing.T) {
	capture := &wireCapture{}
	for _, chunk := range []string{"HTTP/1.1 100 Continue\r\n\r\nHTTP/1.1 201 Cre", "ated\r\nZ-First: 1\r\nSet-Cookie: s=1\r\nA-Last:  two\r\n\r\nbody"} {
		capture.record([]byte(chunk))
	}
	wire := capture.headers(201)
	if wire == nil || wire.StatusLine != "HTTP/1.1 201 Created" || len(wire.Headers) != 3 || wire.Headers[0] != (Header{Name: "Z-First", Value: "1"}) || wire.Headers[1] != (Header{Name: "Set-Cookie", Value: "s=1"}) || wire.Headers[2] != (Header{Name: "A-Last", Value: "two"}) || wire.Raw != "HTTP/1.1 201 Created\r\nZ-First: 1\r\nSet-Cookie: s=1\r\nA-Last:  two\r\n\r\n" {
		t.Fatalf("wire headers %+v", wire)
	}
	if capture.headers(200) != nil {
		t.Fatal("a block for another status was claimed")
	}
	partial := &wireCapture{}
	partial.record([]byte("HTTP/1.1 200 OK\r\nA: b\r\n"))
	if partial.headers(200) != nil {
		t.Fatal("an incomplete block was claimed")
	}
}

func TestBoundConnectLimitsEachDial(t *testing.T) {
	const limit = 20 * time.Millisecond
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		if deadline, ok := ctx.Deadline(); !ok || deadline.After(time.Now().Add(limit)) {
			t.Errorf("dial deadline = %v, %t; want within %v", deadline, ok, limit)
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	boundConnect(transport, limit)
	if _, err := transport.DialContext(context.Background(), "tcp", "192.0.2.1:80"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dial was not bounded: %v", err)
	}
	unbounded := &http.Transport{}
	boundConnect(unbounded, time.Second)
	if unbounded.DialContext != nil {
		t.Fatal("a transport without a dialer gained one")
	}
}
