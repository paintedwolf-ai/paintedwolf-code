package outboundhttp

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
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

func newHopTracer(onConn func(local, remote netip.AddrPort), onReady ...func()) (*hopTracer, *httptrace.ClientTrace) {
	t := &hopTracer{}
	first := func(at *time.Time) {
		t.mu.Lock()
		defer t.mu.Unlock()
		if at.IsZero() {
			*at = time.Now()
		}
	}
	notifyReady := func() {
		for _, fn := range onReady {
			if fn != nil {
				fn()
			}
		}
	}
	trace := &httptrace.ClientTrace{
		ConnectStart:      func(string, string) { first(&t.tcpStart) },
		ConnectDone:       func(string, string, error) { first(&t.tcpDone) },
		TLSHandshakeStart: func() { first(&t.tlsStart) },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			first(&t.tlsDone)
			if err == nil {
				notifyReady()
			}
		},
		WroteRequest: func(httptrace.WroteRequestInfo) { first(&t.reqSent) },
		GotFirstResponseByte: func() {
			first(&t.ttfbTime)
		},
		GotConn: func(info httptrace.GotConnInfo) {
			notifyReady()
			if local, ok := info.Conn.LocalAddr().(*net.TCPAddr); ok && onConn != nil {
				if remote, ok := info.Conn.RemoteAddr().(*net.TCPAddr); ok {
					onConn(local.AddrPort(), remote.AddrPort())
				}
			}
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

type firstHopWriteObserver struct {
	mu             sync.Mutex
	cleartext      bool
	readyForHTTP   bool
	hasBody        bool
	headerTail     []byte
	headersDone    bool
	bodyReadBytes  int64
	headersWritten bool
	bodyWritten    bool
	onHeaders      func()
	onBody         func()
}

func observeFirstHopWrites(transport *http.Transport, cleartext, hasBody bool, onHeaders, onBody func()) *firstHopWriteObserver {
	obs := &firstHopWriteObserver{
		cleartext:    cleartext,
		readyForHTTP: cleartext,
		hasBody:      hasBody,
		onHeaders:    onHeaders,
		onBody:       onBody,
	}
	dial := transport.DialContext
	if dial == nil {
		var d net.Dialer
		dial = d.DialContext
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &firstHopWriteConn{Conn: conn, obs: obs}, nil
	}
	return obs
}

func (o *firstHopWriteObserver) markReadyForHTTP() {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.readyForHTTP = true
	o.mu.Unlock()
}

func (o *firstHopWriteObserver) recordBodyRead(n int) {
	if o == nil || n <= 0 {
		return
	}
	o.mu.Lock()
	o.bodyReadBytes += int64(n)
	o.mu.Unlock()
}

func (o *firstHopWriteObserver) recordConnWrite(written []byte) {
	if o == nil || len(written) == 0 {
		return
	}
	var fireHeaders, fireBody func()
	o.mu.Lock()
	if !o.readyForHTTP {
		o.mu.Unlock()
		return
	}
	if !o.headersWritten {
		o.headersWritten = true
		fireHeaders = o.onHeaders
	}
	if o.hasBody && !o.bodyWritten {
		if o.cleartext {
			if o.headersDone {
				o.bodyWritten = true
				fireBody = o.onBody
			} else {
				combined := append(append([]byte(nil), o.headerTail...), written...)
				if idx := bytes.Index(combined, []byte("\r\n\r\n")); idx >= 0 {
					o.headersDone = true
					o.headerTail = nil
					if len(combined)-(idx+4) > 0 {
						o.bodyWritten = true
						fireBody = o.onBody
					}
				} else if len(combined) > 3 {
					o.headerTail = append(o.headerTail[:0], combined[len(combined)-3:]...)
				} else {
					o.headerTail = append(o.headerTail[:0], combined...)
				}
			}
		} else if o.bodyReadBytes > 0 {
			o.bodyWritten = true
			fireBody = o.onBody
		}
	}
	o.mu.Unlock()
	if fireHeaders != nil {
		fireHeaders()
	}
	if fireBody != nil {
		fireBody()
	}
}

type firstHopWriteConn struct {
	net.Conn
	obs *firstHopWriteObserver
}

func (c *firstHopWriteConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.obs.recordConnWrite(p[:n])
	}
	return n, err
}

type firstHopBodyReadCloser struct {
	io.ReadCloser
	obs *firstHopWriteObserver
}

func (b *firstHopBodyReadCloser) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.obs.recordBodyRead(n)
	}
	return n, err
}

