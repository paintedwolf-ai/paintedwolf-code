package outboundhttp

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/testutil"
)

const (
	headerSecret = "header-secret-5521"
	bodySecret   = "body-secret-8813"
)

type sinkBehavior int

const (
	// refuseBeforeBody answers 401 as soon as headers arrive, without 100 Continue.
	refuseBeforeBody sinkBehavior = iota
	// closeAfterHeaders drops the connection once headers arrive.
	closeAfterHeaders
	// readAllThenClose reads the whole request and closes without a response.
	readAllThenClose
	// answer reads the whole request and replies 204.
	answer
)

type dispatchSink struct {
	addr     netip.AddrPort
	roots    *x509.CertPool
	received atomic.Value
	conns    atomic.Int32
}

func startDispatchSink(t *testing.T, useTLS bool, behavior sinkBehavior, bodyLen int) *dispatchSink {
	t.Helper()
	sink := &dispatchSink{}
	sink.received.Store("")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen", err)
	t.Cleanup(func() { _ = ln.Close() })
	if useTLS {
		issuer := httptest.NewUnstartedServer(nil)
		issuer.StartTLS()
		cert := issuer.TLS.Certificates[0]
		sink.roots = x509.NewCertPool()
		sink.roots.AddCert(issuer.Certificate())
		issuer.Close()
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	}
	sink.addr = netip.MustParseAddrPort(ln.Addr().String())
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		sink.conns.Add(1)
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var got []byte
		buf := make([]byte, 32<<10)
		headerEnd := -1
		for headerEnd < 0 {
			n, err := conn.Read(buf)
			got = append(got, buf[:n]...)
			headerEnd = bytes.Index(got, []byte("\r\n\r\n"))
			if err != nil {
				sink.received.Store(string(got))
				return
			}
		}
		switch behavior {
		case refuseBeforeBody:
			_, _ = io.WriteString(conn, "HTTP/1.1 401 Unauthorized\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
			_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			rest, _ := io.ReadAll(conn)
			got = append(got, rest...)
		case closeAfterHeaders:
		case readAllThenClose, answer:
			for len(got)-headerEnd-4 < bodyLen {
				n, err := conn.Read(buf)
				got = append(got, buf[:n]...)
				if err != nil {
					break
				}
			}
			if behavior == answer {
				_, _ = io.WriteString(conn, "HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n")
			}
		}
		sink.received.Store(string(got))
	}()
	return sink
}

func (s *dispatchSink) wait(t *testing.T) string {
	t.Helper()
	var got string
	testutil.WaitFor(t, func() bool {
		got = s.received.Load().(string)
		return got != ""
	})
	return got
}

type dispatchCounts struct{ headers, body atomic.Int32 }

func dispatchRequest(sink *dispatchSink, useTLS bool, body []byte, expectContinue bool, counts *dispatchCounts) Request {
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	headers := []Header{{Name: "X-Credential", Value: headerSecret}}
	if expectContinue {
		headers = append(headers, Header{Name: "Expect", Value: "100-continue"})
	}
	req := Request{
		Class: egressclass.AgentHTTPRequest, Method: "POST",
		URL:           scheme + "://127.0.0.1:" + strconv.Itoa(int(sink.addr.Port())) + "/upload",
		OriginHeaders: headers, Body: body, Timeout: 5 * time.Second,
		AllowAddress:                func(addr netip.Addr, p uint16) bool { return addr.IsLoopback() && p == sink.addr.Port() },
		OnFirstHopHeadersDispatched: func() { counts.headers.Add(1) },
		OnFirstHopBodyDispatched:    func() { counts.body.Add(1) },
	}
	if useTLS {
		req.TLS = &ClientTLS{Roots: sink.roots}
	}
	return req
}

func TestFirstHopDispatchSeparatesHeadersFromBody(t *testing.T) {
	small := []byte("payload=" + bodySecret)
	large := append(bytes.Repeat([]byte("a"), 4<<20), []byte(bodySecret)...)
	for _, useTLS := range []bool{false, true} {
		name := "http"
		if useTLS {
			name = "https"
		}
		t.Run(name, func(t *testing.T) {
			t.Run("refusal_before_continue_dispatches_headers_only", func(t *testing.T) {
				sink := startDispatchSink(t, useTLS, refuseBeforeBody, len(small))
				var counts dispatchCounts
				resp, err := Do(t.Context(), dispatchRequest(sink, useTLS, small, true, &counts))
				testutil.FailErr(t, "exchange", err)
				got := sink.wait(t)
				if resp.Status != 401 || !strings.Contains(got, headerSecret) || strings.Contains(got, bodySecret) {
					t.Fatalf("status %d, sink received %q", resp.Status, got)
				}
				if counts.headers.Load() != 1 || counts.body.Load() != 0 {
					t.Fatalf("dispatch headers=%d body=%d, want 1 and 0", counts.headers.Load(), counts.body.Load())
				}
			})
			t.Run("delivered_without_response_dispatches_both", func(t *testing.T) {
				sink := startDispatchSink(t, useTLS, readAllThenClose, len(small))
				var counts dispatchCounts
				_, err := Do(t.Context(), dispatchRequest(sink, useTLS, small, false, &counts))
				if err == nil {
					t.Fatal("exchange without a response succeeded")
				}
				if got := sink.wait(t); !strings.Contains(got, bodySecret) {
					t.Fatalf("sink did not receive the body: %q", got)
				}
				if counts.headers.Load() != 1 || counts.body.Load() != 1 {
					t.Fatalf("dispatch headers=%d body=%d, want 1 and 1", counts.headers.Load(), counts.body.Load())
				}
			})
			// The transport took the body, so the host cannot claim it stayed in
			// custody even though this sink never read the late secret.
			t.Run("late_body_secret_after_peer_close_still_counts_as_dispatched", func(t *testing.T) {
				sink := startDispatchSink(t, useTLS, closeAfterHeaders, len(large))
				var counts dispatchCounts
				_, err := Do(t.Context(), dispatchRequest(sink, useTLS, large, false, &counts))
				if err == nil {
					t.Fatal("exchange against a closed peer succeeded")
				}
				if got := sink.wait(t); strings.Contains(got, bodySecret) {
					t.Fatalf("sink unexpectedly read the late secret")
				}
				if counts.headers.Load() != 1 || counts.body.Load() != 1 {
					t.Fatalf("dispatch headers=%d body=%d, want 1 and 1", counts.headers.Load(), counts.body.Load())
				}
			})
			t.Run("completed_exchange_dispatches_each_part_once", func(t *testing.T) {
				sink := startDispatchSink(t, useTLS, answer, len(large))
				var counts dispatchCounts
				resp, err := Do(t.Context(), dispatchRequest(sink, useTLS, large, false, &counts))
				testutil.FailErr(t, "exchange", err)
				if got := sink.wait(t); resp.Status != 204 || !strings.HasSuffix(got, bodySecret) {
					t.Fatalf("status %d, sink tail %q", resp.Status, got[max(0, len(got)-32):])
				}
				if counts.headers.Load() != 1 || counts.body.Load() != 1 {
					t.Fatalf("dispatch headers=%d body=%d, want 1 and 1", counts.headers.Load(), counts.body.Load())
				}
			})
		})
	}
	// A failed handshake writes TLS records but never request bytes.
	t.Run("untrusted_tls_peer_dispatches_nothing", func(t *testing.T) {
		sink := startDispatchSink(t, true, answer, len(small))
		var counts dispatchCounts
		req := dispatchRequest(sink, true, small, false, &counts)
		req.TLS = nil
		if _, err := Do(t.Context(), req); err == nil {
			t.Fatal("exchange with an untrusted peer succeeded")
		}
		if counts.headers.Load() != 0 || counts.body.Load() != 0 {
			t.Fatalf("dispatch headers=%d body=%d, want none", counts.headers.Load(), counts.body.Load())
		}
	})
}
