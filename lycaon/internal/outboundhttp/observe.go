package outboundhttp

import (
	"crypto/tls"
	"net"
	"net/http/httptrace"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// HopObservation is what the client saw on one hop: the request line and
// header fields as written, the peer it reached, and phase completion times
// measured from the start of the exchange. Command frontends present it; it
// never enters structured results.
type HopObservation struct {
	Method        string
	RequestTarget string
	// Sent holds the header fields in the order the transport wrote them.
	Sent []Header
	// Remote is the connected peer; it is invalid for a unix socket.
	Remote netip.AddrPort
	TLS    *TLSInfo
	// Phase times are cumulative from the exchange start, as curl reports
	// them. AppConnect is zero for a cleartext hop.
	NameLookup, Connect, AppConnect, StartTransfer time.Duration
}

type hopTracer struct {
	mu                sync.Mutex
	tcpStart, tcpDone time.Time
	tlsStart, tlsDone time.Time
	reqSent           time.Time
	ttfbTime          time.Time
	remote            netip.AddrPort
	sent              []Header
}

func newHopTracer() (*hopTracer, *httptrace.ClientTrace) {
	t := &hopTracer{}
	first := func(at *time.Time) {
		t.mu.Lock()
		defer t.mu.Unlock()
		if at.IsZero() {
			*at = time.Now()
		}
	}
	trace := &httptrace.ClientTrace{
		ConnectStart:      func(string, string) { first(&t.tcpStart) },
		ConnectDone:       func(string, string, error) { first(&t.tcpDone) },
		TLSHandshakeStart: func() { first(&t.tlsStart) },
		TLSHandshakeDone:  func(tls.ConnectionState, error) { first(&t.tlsDone) },
		WroteRequest:      func(httptrace.WroteRequestInfo) { first(&t.reqSent) },
		GotFirstResponseByte: func() {
			first(&t.ttfbTime)
		},
		GotConn: func(info httptrace.GotConnInfo) {
			if addr, ok := info.Conn.RemoteAddr().(*net.TCPAddr); ok {
				t.mu.Lock()
				t.remote = addr.AddrPort()
				t.mu.Unlock()
			}
		},
		WroteHeaderField: func(key string, values []string) {
			t.mu.Lock()
			defer t.mu.Unlock()
			if key == ":authority" {
				key = "Host"
			} else if strings.HasPrefix(key, ":") {
				return
			}
			for _, value := range values {
				t.sent = append(t.sent, Header{Name: key, Value: value})
			}
		},
	}
	return t, trace
}

func (t *hopTracer) timing(ioStarted time.Time, dnsElapsed int64) *HopTiming {
	t.mu.Lock()
	defer t.mu.Unlock()
	var tcpConnect, tlsDuration, ttfb int64
	if !t.tcpDone.IsZero() && !t.tcpStart.IsZero() {
		tcpConnect = t.tcpDone.Sub(t.tcpStart).Milliseconds()
	}
	if !t.tlsDone.IsZero() && !t.tlsStart.IsZero() {
		tlsDuration = t.tlsDone.Sub(t.tlsStart).Milliseconds()
	}
	if !t.ttfbTime.IsZero() {
		if !t.reqSent.IsZero() {
			ttfb = t.ttfbTime.Sub(t.reqSent).Milliseconds()
		} else {
			ttfb = t.ttfbTime.Sub(ioStarted).Milliseconds() - dnsElapsed - tcpConnect - tlsDuration
			if ttfb < 0 {
				ttfb = 0
			}
		}
	}
	return &HopTiming{
		DNS:        dnsElapsed,
		TCPConnect: tcpConnect,
		TLS:        tlsDuration,
		TTFB:       ttfb,
	}
}

// observation records the hop against the exchange start. A phase the hop
// never reached reports the preceding phase's time, so the values stay
// ordered as curl's are.
func (t *hopTracer) observation(begun, resolved time.Time, method, target string) *HopObservation {
	t.mu.Lock()
	defer t.mu.Unlock()
	since := func(at time.Time, floor time.Duration) time.Duration {
		if at.IsZero() || at.Sub(begun) < floor {
			return floor
		}
		return at.Sub(begun)
	}
	o := &HopObservation{Method: method, RequestTarget: target, Sent: append([]Header(nil), t.sent...), Remote: t.remote}
	o.NameLookup = since(resolved, 0)
	o.Connect = since(t.tcpDone, o.NameLookup)
	if !t.tlsDone.IsZero() {
		o.AppConnect = since(t.tlsDone, o.Connect)
	}
	o.StartTransfer = since(t.ttfbTime, max(o.Connect, o.AppConnect))
	return o
}
